// 本文件用合成 ENet datagram 验证控制命令、可靠数据、分片边界及解析所有权，不使用网络。
package nativeproto

import (
	"bytes"
	"encoding/binary"
	"reflect"
	"testing"
)

// syntheticENetCommand 按 kind 生成字段明确的合成命令，返回独立数据；未知 kind 由调用者避免。
func syntheticENetCommand(kind uint8) ENetCommand {
	command := ENetCommand{Kind: kind, Channel: 0, Sequence: 7, AckRequired: kind != 1}
	length, _ := enetBodyLength(kind)
	switch kind {
	case 6, 7, 9:
		command.Data = []byte("synthetic payload")
		if kind != 6 {
			command.SecondarySequence = 3
		}
	case 8, 12:
		command.Data = []byte("part")
		command.Fragment = &ENetFragment{StartSequence: 7, Count: 2, Number: 0, TotalLength: 8, Offset: 0}
	default:
		command.Channel = 255
		if length > 0 {
			command.Control = bytes.Repeat([]byte{0x23}, length)
		}
	}
	return command
}

// TestENetEveryCommandRoundTrip 验证 1..12 的固定命令长度、sent-time 与数据字段不被误读。
func TestENetEveryCommandRoundTrip(t *testing.T) {
	for kind := uint8(1); kind <= 12; kind++ {
		for _, withTime := range []bool{false, true} {
			packet := ENetPacket{PeerID: 17, SessionID: 2, Commands: []ENetCommand{syntheticENetCommand(kind)}}
			if withTime {
				stamp := uint16(0)
				packet.SentTime = &stamp
			}
			encoded, err := EncodeENet(packet)
			if err != nil {
				t.Fatal(err)
			}
			decoded, err := DecodeENet(encoded)
			if err != nil || !reflect.DeepEqual(decoded, packet) {
				t.Fatalf("kind %d: decoded = %+v, want = %+v, err = %v", kind, decoded, packet, err)
			}
			reencoded, err := EncodeENet(decoded)
			if err != nil || !bytes.Equal(encoded, reencoded) {
				t.Fatalf("kind %d changed bytes: %v", kind, err)
			}
		}
	}
}

// TestENetReliableWrapsApplication 验证 ENet 的 data length 不包含 command 头，业务长度仍含自己的头。
func TestENetReliableWrapsApplication(t *testing.T) {
	application, err := Encode(1016, `<X_HEARTBEAT docver="1" />`)
	if err != nil {
		t.Fatal(err)
	}
	stamp := uint16(123)
	packet := ENetPacket{SentTime: &stamp, Commands: []ENetCommand{{Kind: 6, Channel: 0, Sequence: 1, AckRequired: true, Data: application}}}
	encoded, err := EncodeENet(packet)
	if err != nil {
		t.Fatal(err)
	}
	if len(encoded) != 44 || binary.BigEndian.Uint16(encoded[8:]) != 34 || binary.BigEndian.Uint32(encoded[10:]) != 34 || binary.BigEndian.Uint32(encoded[14:]) != 1016 {
		t.Fatalf("wrong layer lengths: %x", encoded)
	}
}

// TestENetMultipleCommandsAndOwnership 验证一包多命令、保留顺序及输入切片的独立所有权。
func TestENetMultipleCommandsAndOwnership(t *testing.T) {
	packet := ENetPacket{PeerID: 4095, SessionID: 3, Commands: []ENetCommand{syntheticENetCommand(1), syntheticENetCommand(6), syntheticENetCommand(4)}}
	encoded, err := EncodeENet(packet)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeENet(encoded)
	if err != nil || !reflect.DeepEqual(decoded, packet) {
		t.Fatalf("decoded = %+v, err = %v", decoded, err)
	}
	clear(encoded)
	if !bytes.Equal(decoded.Commands[1].Data, []byte("synthetic payload")) || decoded.Commands[0].Control[0] != 0x23 {
		t.Fatal("decoded command aliases input")
	}
}

// TestENetRejectsMalformedPacket 验证截断、压缩、未知命令和保留标志不会被当成数据头自动跳过。
func TestENetRejectsMalformedPacket(t *testing.T) {
	stamp := uint16(1)
	valid, err := EncodeENet(ENetPacket{SentTime: &stamp, Commands: []ENetCommand{syntheticENetCommand(6)}})
	if err != nil {
		t.Fatal(err)
	}
	for length := 0; length < len(valid); length++ {
		if _, err := DecodeENet(valid[:length]); err == nil {
			t.Fatalf("accepted truncated length %d", length)
		}
	}
	for _, mutate := range []func([]byte){
		func(data []byte) { data[0] |= 0x40 },
		func(data []byte) { data[4] = 0 },
		func(data []byte) { data[4] = 13 },
		func(data []byte) { data[4] |= 0x10 },
		func(data []byte) { binary.BigEndian.PutUint16(data[8:], 65535) },
	} {
		invalid := append([]byte(nil), valid...)
		mutate(invalid)
		if _, err := DecodeENet(invalid); err == nil {
			t.Fatalf("accepted malformed %x", invalid)
		}
	}
	if _, err := DecodeENet(append(valid, 0)); err == nil {
		t.Fatal("ignored trailing byte")
	}
}

// TestENetRejectsInvalidCommandFields 验证错误字段组合和资源预算在编码前失败，不静默丢字段。
func TestENetRejectsInvalidCommandFields(t *testing.T) {
	cases := []ENetCommand{
		{Kind: 0}, {Kind: 13}, {Kind: 1}, {Kind: 5, Control: []byte{0}},
		{Kind: 5, Data: []byte{0}}, {Kind: 6, Control: []byte{0}},
		{Kind: 6, Data: make([]byte, 65536)}, {Kind: 6, SecondarySequence: 1},
		{Kind: 6, Fragment: &ENetFragment{}}, {Kind: 8},
		{Kind: 8, Data: []byte{1}, Fragment: &ENetFragment{Count: 0, TotalLength: 1}},
		{Kind: 8, Data: []byte{1}, Fragment: &ENetFragment{Count: 1, Number: 1, TotalLength: 1}},
		{Kind: 8, Data: []byte{1}, Fragment: &ENetFragment{Count: 1, Offset: 1, TotalLength: 1}},
	}
	for _, command := range cases {
		if _, err := EncodeENet(ENetPacket{Commands: []ENetCommand{command}}); err == nil {
			t.Fatalf("accepted invalid command %+v", command)
		}
	}
	for _, packet := range []ENetPacket{
		{}, {PeerID: 4096, Commands: []ENetCommand{{Kind: 5}}},
		{SessionID: 4, Commands: []ENetCommand{{Kind: 5}}},
		{Commands: make([]ENetCommand, enetMaxCommands+1)},
	} {
		if _, err := EncodeENet(packet); err == nil {
			t.Fatalf("accepted invalid packet %+v", packet)
		}
	}
}
