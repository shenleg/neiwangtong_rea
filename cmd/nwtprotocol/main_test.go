// 本文件验证离线协议命令的参数、长度预算、单帧约束与 I/O 错误，不使用网络。
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"

	"neiwangtong-gateway/internal/nativeproto"
)

// TestOfflineRoundTrip 验证独立命令可以离线编解码两种业务命令，且不把结果标为送达。
func TestOfflineRoundTrip(t *testing.T) {
	for _, command := range []string{"1004", "3011"} {
		var encoded, decoded, stderr bytes.Buffer
		text := "<SYNTHETIC>测试🙂</SYNTHETIC>"
		if code := run([]string{"-mode", "encode", "-command", command}, strings.NewReader(text), &encoded, &stderr); code != 0 {
			t.Fatalf("encode = %d, stderr = %s", code, &stderr)
		}
		if code := run([]string{"-mode", "decode"}, &encoded, &decoded, &stderr); code != 0 {
			t.Fatalf("decode = %d, stderr = %s", code, &stderr)
		}
		var frame nativeproto.Frame
		if err := json.Unmarshal(decoded.Bytes(), &frame); err != nil || frame.Payload != text {
			t.Fatalf("frame = %+v, err = %v", frame, err)
		}
		if bytes.Contains(decoded.Bytes(), []byte("recipient_observed")) {
			t.Fatal("offline result claimed delivery")
		}
	}
}

// TestRejectArguments 验证缺失模式、缺失命令及越界参数在处理正文前明确失败。
func TestRejectArguments(t *testing.T) {
	cases := [][]string{
		nil,
		{"-mode", "send"},
		{"-mode", "encode"},
		{"-mode", "encode", "-command", "-1"},
		{"-mode", "encode", "-command", "4294967296"},
		{"-mode", "decode", "-command", "1004"},
		{"-mode", "decode", "-max-frame-bytes", "8"},
		{"-mode", "decode", "-max-frame-bytes", "4294967296"},
		{"-mode", "decode", "unexpected"},
	}
	for _, args := range cases {
		var stdout, stderr bytes.Buffer
		if code := run(args, strings.NewReader("x"), &stdout, &stderr); code != 2 || stdout.Len() != 0 || stderr.Len() == 0 {
			t.Fatalf("%v: code = %d, out = %q, err = %q", args, code, &stdout, &stderr)
		}
	}
}

// TestRejectInvalidFrames 验证命令不截断超预算正文，也不静默丢弃二进制帧后的额外内容。
func TestRejectInvalidFrames(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := run([]string{"-mode", "encode", "-command", "1004", "-max-frame-bytes", "9"}, strings.NewReader("xx"), &stdout, &stderr); code != 1 || stdout.Len() != 0 {
		t.Fatalf("oversized: code = %d, output = %q", code, &stdout)
	}
	frame, err := nativeproto.Encode(1004, "<SYNTHETIC/>")
	if err != nil {
		t.Fatal(err)
	}
	for _, input := range [][]byte{nil, frame[:len(frame)-1], append(append([]byte{}, frame...), 0)} {
		stdout.Reset()
		stderr.Reset()
		if code := run([]string{"-mode", "decode"}, bytes.NewReader(input), &stdout, &stderr); code != 1 || stdout.Len() != 0 {
			t.Fatalf("invalid: code = %d, output = %q", code, &stdout)
		}
	}
}

// failedWriter 模拟外部输出故障；writeErr 非空时返回该错误，否则返回 io.ErrShortWrite。
type failedWriter struct {
	writeErr error
}

// Write 模拟输出失败，忽略 p 内容，返回零及预设错误或短写错误，不产生文件或网络副作用。
func (w failedWriter) Write(p []byte) (int, error) {
	if w.writeErr == nil {
		return 0, io.ErrShortWrite
	}
	return 0, w.writeErr
}

// TestOutputFailure 验证二进制短写、I/O 错误和 JSON 输出失败不会报告离线转换成功。
func TestOutputFailure(t *testing.T) {
	frame, err := nativeproto.Encode(1004, "<SYNTHETIC/>")
	if err != nil {
		t.Fatal(err)
	}
	for _, writer := range []io.Writer{failedWriter{}, failedWriter{writeErr: errors.New("synthetic output failure")}} {
		var stderr bytes.Buffer
		if code := run([]string{"-mode", "encode", "-command", "1004"}, strings.NewReader("<SYNTHETIC/>"), writer, &stderr); code != 1 {
			t.Fatalf("encode code = %d", code)
		}
		if code := run([]string{"-mode", "decode"}, bytes.NewReader(frame), writer, &stderr); code != 1 {
			t.Fatalf("decode code = %d", code)
		}
	}
}
