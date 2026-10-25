// 本文件以合成节点模板验证长度、字节序、身份槽和未知字节保留，不使用真实身份或网络。
package nativeproto

import (
	"bytes"
	"encoding/binary"
	"strings"
	"testing"
)

// syntheticNode 创建带非零未知字段的合成 304 字节模板；kind 决定类型，返回新切片，无外部 I/O。
func syntheticNode(kind uint32) []byte {
	data := bytes.Repeat([]byte{0xa5}, nodePacketSize)
	binary.BigEndian.PutUint32(data, nodePacketSize)
	binary.BigEndian.PutUint32(data[4:], kind)
	binary.BigEndian.PutUint32(data[8:], 1)
	binary.BigEndian.PutUint16(data[0x18:], 9011)
	binary.BigEndian.PutUint16(data[0x1a:], 9012)
	binary.BigEndian.PutUint16(data[0x1c:], 12345)
	binary.BigEndian.PutUint16(data[0xa5:], 54321)
	clear(data[0x1e : 0x1e+65])
	copy(data[0x1e:], "synthetic-local-id")
	return data
}

// TestNodeRoundTrip 验证三种节点包按已知偏移解码，并逐字节保留未知内容和 NUL 后原始槽字节。
func TestNodeRoundTrip(t *testing.T) {
	for _, kind := range []uint32{1, 2, 4} {
		data := syntheticNode(kind)
		data[0x1e+40] = 0x91
		packet, err := DecodeNodePacket(data)
		if err != nil {
			t.Fatal(err)
		}
		if packet.Type != kind || packet.UserID != "synthetic-local-id" || packet.DiscoveryPort != 9011 || packet.ENetPort != 9012 || packet.TCPPort != 12345 || packet.ReverseENetPort != 54321 {
			t.Fatalf("decoded = %+v", packet)
		}
		encoded, err := packet.Encode()
		if err != nil || !bytes.Equal(encoded, data) {
			t.Fatalf("round trip changed bytes: %v", err)
		}
		data[12] ^= 0xff
		if encoded[12] == data[12] {
			t.Fatal("node buffer aliases input")
		}
	}
}

// TestNodeUpdateKnownFields 验证更新类型、身份和端口不会覆盖其他字段，也不保留旧身份的残留字节。
func TestNodeUpdateKnownFields(t *testing.T) {
	original := syntheticNode(1)
	packet, err := DecodeNodePacket(original)
	if err != nil {
		t.Fatal(err)
	}
	packet.Type = 4
	packet.UserID = "新身份🙂"
	packet.ENetPort = 56789
	packet.TCPPort = 23456
	packet.ReverseENetPort = 65432
	encoded, err := packet.Encode()
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeNodePacket(encoded)
	if err != nil || decoded.UserID != packet.UserID || decoded.Type != 4 || decoded.ENetPort != 56789 || decoded.TCPPort != 23456 || decoded.ReverseENetPort != 65432 {
		t.Fatalf("decoded = %+v, err = %v", decoded, err)
	}
	for i := range original {
		known := i >= 4 && i < 8 || i >= 0x18 && i < 0x1e+65 || i >= 0xa5 && i < 0xa7
		if !known && encoded[i] != original[i] {
			t.Fatalf("unknown byte %#x changed", i)
		}
	}
	if !bytes.Equal(encoded[0x1e+len(packet.UserID):0x1e+65], make([]byte, 65-len(packet.UserID))) {
		t.Fatal("identity slot contains stale bytes")
	}
}

// TestNodeRejectsInvalidInput 验证外部模板损坏、无 NUL、无效 UTF-8 和未验证的类型会明确失败。
func TestNodeRejectsInvalidInput(t *testing.T) {
	cases := [][]byte{nil, syntheticNode(1)[:303], append(syntheticNode(1), 0), syntheticNode(3)}
	for _, mutate := range []func([]byte){
		func(data []byte) { binary.BigEndian.PutUint32(data, 312) },
		func(data []byte) { binary.BigEndian.PutUint32(data[8:], 2) },
		func(data []byte) { clear(data[0x1e : 0x1e+65]) },
		func(data []byte) { copy(data[0x1e:0x1e+65], bytes.Repeat([]byte{'x'}, 65)) },
		func(data []byte) { data[0x1e] = 0xff },
	} {
		data := syntheticNode(1)
		mutate(data)
		cases = append(cases, data)
	}
	for i, data := range cases {
		if _, err := DecodeNodePacket(data); err == nil {
			t.Fatalf("accepted invalid case %d", i)
		}
	}
	if _, err := (NodePacket{Type: 1, UserID: "not-a-template"}).Encode(); err == nil {
		t.Fatal("accepted zero-value template")
	}
}

// TestNodeIdentityBudget 验证身份槽按 UTF-8 字节计数，允许 64 字节而拒绝溢出、NUL 和非法类型。
func TestNodeIdentityBudget(t *testing.T) {
	packet, err := DecodeNodePacket(syntheticNode(1))
	if err != nil {
		t.Fatal(err)
	}
	packet.UserID = strings.Repeat("a", 64)
	if _, err := packet.Encode(); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"", strings.Repeat("a", 65), strings.Repeat("中", 22), "a\x00b", "\xff"} {
		packet.UserID = id
		if _, err := packet.Encode(); err == nil {
			t.Fatalf("accepted identity byte length %d", len(id))
		}
	}
	packet.UserID = "valid"
	packet.Type = 3
	if _, err := packet.Encode(); err == nil {
		t.Fatal("accepted unsupported node type")
	}
}
