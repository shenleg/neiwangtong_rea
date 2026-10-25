// 本文件可选验证官方 ENet 1.3.18 参考进程互通；默认跳过，不下载或编译外部源码，全部端点限于 loopback。
package nativeproto

import (
	"bufio"
	"bytes"
	"context"
	"io"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// referenceProcess 启动操作者通过 ENET_REFERENCE_BINARY 明确提供的测试驱动，返回进程、loopback 地址与诊断。
// 默认无此变量则跳过；启动、READY 格式或端口错误终止 t；清理会结束本次进程，不操作真实客户端。
func referenceProcess(t *testing.T, role string, port uint16) (*exec.Cmd, netip.AddrPort, *bytes.Buffer) {
	t.Helper()
	binary := os.Getenv("ENET_REFERENCE_BINARY")
	if binary == "" {
		t.Skip("未显式提供 ENet 1.3.18 本机参考驱动，跳过独立库互通")
	}
	if !filepath.IsAbs(binary) {
		t.Fatal("ENET_REFERENCE_BINARY 须为绝对路径")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	t.Cleanup(cancel)
	args := []string{role}
	if role == "client" {
		args = append(args, strconv.Itoa(int(port)))
	}
	cmd := exec.CommandContext(ctx, binary, args...)
	stderr := &bytes.Buffer{}
	cmd.Stderr = stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if cmd.ProcessState == nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	})
	line, err := bufio.NewReader(stdout).ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Fields(line)
	if len(parts) != 2 || parts[0] != "READY" {
		t.Fatal("参考驱动没有有效 READY")
	}
	value, err := strconv.ParseUint(parts[1], 10, 16)
	if err != nil || value == 0 {
		t.Fatal("参考驱动没有有效 loopback 端口")
	}
	return cmd, netip.AddrPortFrom(netip.MustParseAddr("127.0.0.1"), uint16(value)), stderr
}

// TestENetReferenceGoConnects 验证 Go 主动方与独立官方 ENet 被动方交换跨发送窗口的分片消息并正常断开。
func TestENetReferenceGoConnects(t *testing.T) {
	cmd, remote, stderr := referenceProcess(t, "server", 0)
	conn := udpTestSocket(t)
	transport, err := NewUDPTransport(conn, remote, runtimeConfig(7, 1))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	defer func() { _ = transport.Close(ctx) }()
	if err := transport.Connect(ctx); err != nil {
		t.Fatal(err)
	}
	data := make([]byte, 90003)
	for i := range data {
		data[i] = byte((i*19 + 17) % 251)
	}
	if err := transport.Send(ctx, data); err != nil {
		t.Fatal(err)
	}
	echo, err := transport.Receive(ctx)
	if err != nil || !bytes.Equal(echo, data) {
		t.Fatalf("echo length=%d, err=%v", len(echo), err)
	}
	if err := transport.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatalf("reference exit: %v, %s", err, stderr)
	}
}

// TestENetReferenceGoAccepts 验证独立官方 ENet 主动方可连接 Go，被动方接收/回显大消息并确认远端断开。
func TestENetReferenceGoAccepts(t *testing.T) {
	conn := udpTestSocket(t)
	port := conn.LocalAddr().(*net.UDPAddr).AddrPort().Port()
	cmd, remote, stderr := referenceProcess(t, "client", port)
	transport, err := NewUDPTransport(conn, remote, runtimeConfig(9, 2))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	defer func() { _ = transport.Close(ctx) }()
	if err := transport.Accept(ctx); err != nil {
		t.Fatal(err)
	}
	data, err := transport.Receive(ctx)
	if err != nil {
		t.Fatal(err)
	}
	expected := make([]byte, 70003)
	for i := range expected {
		expected[i] = byte((i*17 + 31) % 251)
	}
	if !bytes.Equal(data, expected) {
		t.Fatalf("reference data differs, length=%d", len(data))
	}
	if err := transport.Send(ctx, data); err != nil {
		t.Fatal(err)
	}
	if _, err := transport.Receive(ctx); err != io.EOF {
		t.Fatalf("remote close = %v", err)
	}
	if err := transport.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatalf("reference exit: %v, %s", err, stderr)
	}
}
