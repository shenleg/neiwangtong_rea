// 本文件用合成帧及无身份的原客户端心跳/退出帧验证字节序、XTEA 和流读取；不接触真实客户端或网络。
package nativeproto

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"io"
	"strings"
	"testing"
)

// TestXTEAZeroVector 验证标准 XTEA 的已知答案，避免只用自身解密验证自身加密。
func TestXTEAZeroVector(t *testing.T) {
	block := make([]byte, 8)
	xteaBlock(block, [4]uint32{}, false)
	if got := hex.EncodeToString(block); got != "dee9d4d8f7131ed9" {
		t.Fatalf("ciphertext = %s", got)
	}
	xteaBlock(block, [4]uint32{}, true)
	if !bytes.Equal(block, make([]byte, 8)) {
		t.Fatalf("plaintext = %x", block)
	}
}

// TestSampleKeyFrameVector 验证按原始指令独立计算的合成向量，包括固定密钥、大端帧头和明文尾字节。
func TestSampleKeyFrameVector(t *testing.T) {
	// 该向量由独立 Python 参考计算产生，不是原客户端执行或真实抓包结果。
	const want = "00000011000003ec8564d0424f8cfe2278"
	encoded, err := Encode(1004, "12345678x")
	if err != nil || hex.EncodeToString(encoded) != want {
		t.Fatalf("encoded = %x, err = %v", encoded, err)
	}
	vector, err := hex.DecodeString(want)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := Decode(bytes.NewReader(vector), 17)
	if err != nil || decoded != (Frame{Command: 1004, Payload: "12345678x"}) {
		t.Fatalf("decoded = %+v, err = %v", decoded, err)
	}
}

// TestCapturedHeartbeatFrame 验证原客户端被动抓包的 1016 心跳与 Go 编解码逐字节一致。
func TestCapturedHeartbeatFrame(t *testing.T) {
	// 来源：origin/main 的 86bce663，capture.pcapng 帧 1、23、24、27、29。
	// 仅保留无身份信息的应用帧，不含 ENet 连接标识、IP 或用户资料；原始包摘要见采集验证报告。
	const capturedHex = "00000022000003f8d529c5439de045c9d3dc11866769e1fedfa896e8e704a5532f3e"
	const payload = `<X_HEARTBEAT docver="1" />`
	captured, err := hex.DecodeString(capturedHex)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := Decode(bytes.NewReader(captured), uint32(len(captured)))
	if err != nil || decoded != (Frame{Command: 1016, Payload: payload}) {
		t.Fatalf("decoded = %+v, err = %v", decoded, err)
	}
	encoded, err := Encode(1016, payload)
	if err != nil || !bytes.Equal(encoded, captured) {
		t.Fatalf("encoded = %x, err = %v", encoded, err)
	}
}

// TestCapturedQuitFrame 验证退出采集中的 1017 应用帧，不能将其误当 ENet DISCONNECT。
func TestCapturedQuitFrame(t *testing.T) {
	// 来源：c734328d 的 20261025-093200-exit/capture.pcapng 帧 151、152。
	// 29 字节应用帧不含身份资料；其摘要与捕获字节一致，正文尾部五字节不加密。
	const capturedHex = "0000001d000003f9078c112e9c8bc952426afbd748cd5e933122202f3e"
	const payload = `<X_QUIT docver="1" />`
	captured, err := hex.DecodeString(capturedHex)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := Decode(bytes.NewReader(captured), uint32(len(captured)))
	if err != nil || decoded != (Frame{Command: 1017, Payload: payload}) {
		t.Fatalf("decoded = %+v, err = %v", decoded, err)
	}
	encoded, err := Encode(1017, payload)
	if err != nil || !bytes.Equal(encoded, captured) {
		t.Fatalf("encoded = %x, err = %v", encoded, err)
	}
}

// TestVendorXMLBytesArePreserved 验证 codec 不修补实际 3005 中已观察到的标签错配。
func TestVendorXMLBytesArePreserved(t *testing.T) {
	// 原样本与实际报文都将 QGROUP_INFO_VER 错闭合成 GROUP_INFO_VER；这里只用无身份的合成正文。
	const payload = `<X_QGROUP_REQ_INFO_RSP><QGROUP_INFO_VER>1</GROUP_INFO_VER></X_QGROUP_REQ_INFO_RSP>`
	encoded, err := Encode(3005, payload)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := Decode(bytes.NewReader(encoded), uint32(len(encoded)))
	if err != nil || decoded != (Frame{Command: 3005, Payload: payload}) {
		t.Fatalf("decoded = %+v, err = %v", decoded, err)
	}
}

// TestFrameHeaderAndTail 验证头部字节序及完整块加密，末尾不足一块的数据不填充、不加密。
func TestFrameHeaderAndTail(t *testing.T) {
	for _, command := range []uint32{1004, 3011} {
		for length := 1; length <= 24; length++ {
			text := strings.Repeat("x", length)
			frame, err := Encode(command, text)
			if err != nil {
				t.Fatal(err)
			}
			if len(frame) != headerSize+length || binary.BigEndian.Uint32(frame) != uint32(len(frame)) || binary.BigEndian.Uint32(frame[4:]) != command {
				t.Fatalf("header = %x", frame[:headerSize])
			}
			tail := length / 8 * 8
			if string(frame[headerSize+tail:]) != text[tail:] {
				t.Fatalf("length %d: tail was modified", length)
			}
			if length >= 8 && bytes.Equal(frame[headerSize:headerSize+8], []byte(text[:8])) {
				t.Fatal("complete block was not encrypted")
			}
			decoded, err := Decode(bytes.NewReader(frame), uint32(len(frame)))
			if err != nil || decoded != (Frame{Command: command, Payload: text}) {
				t.Fatalf("decoded = %+v, err = %v", decoded, err)
			}
		}
	}
}

// TestFrameUTF8AndStream 验证 UTF-8 字节长度，以及连续读取时不吞掉下一帧。
func TestFrameUTF8AndStream(t *testing.T) {
	texts := []string{"<X_SEND_MSG>中文🙂</X_SEND_MSG>", "<X_QGROUP_SEND_MSG>合成测试</X_QGROUP_SEND_MSG>"}
	var stream bytes.Buffer
	for _, text := range texts {
		frame, err := Encode(1004, text)
		if err != nil {
			t.Fatal(err)
		}
		stream.Write(frame)
	}
	for _, text := range texts {
		frame, err := Decode(&stream, 1024)
		if err != nil || frame.Payload != text {
			t.Fatalf("frame = %+v, err = %v", frame, err)
		}
	}
	if stream.Len() != 0 {
		t.Fatalf("remaining = %d", stream.Len())
	}
}

// TestFrameRejectsInvalidInput 验证 strlen 输入约束，以及非法长度在分配正文前被拒绝。
func TestFrameRejectsInvalidInput(t *testing.T) {
	for _, text := range []string{"", "a\x00b", "\xff"} {
		if _, err := Encode(1004, text); err == nil {
			t.Fatalf("accepted %q", text)
		}
	}
	for _, length := range []uint32{0, 7, 8, 1025, 0xffffffff} {
		var header [8]byte
		binary.BigEndian.PutUint32(header[:], length)
		if _, err := Decode(bytes.NewReader(header[:]), 1024); err == nil {
			t.Fatalf("accepted length %d", length)
		}
	}
	for _, data := range [][]byte{nil, {0, 0, 0, 9}, {0, 0, 0, 9, 0, 0, 3, 0xec}} {
		if _, err := Decode(bytes.NewReader(data), 1024); !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
			t.Fatalf("data %x: err = %v", data, err)
		}
	}
	for _, payload := range []byte{0, 0xff} {
		if _, err := Decode(bytes.NewReader([]byte{0, 0, 0, 9, 0, 0, 3, 0xec, payload}), 1024); err == nil {
			t.Fatalf("accepted payload %x", payload)
		}
	}
}
