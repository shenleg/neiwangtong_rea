// 本文件在显式启用的官方参考互通中加入确定性 loopback 丢包；不代理或连接任何外部端点。
package nativeproto

import (
	"bytes"
	"context"
	"net"
	"net/netip"
	"sync/atomic"
	"testing"
	"time"
)

// referenceLossProxy 在两个已知 loopback 端点间转发，仅丢首个出站分片和首个数据 ACK。
// 返回代理端点与丢包标记；socket/协程由 t 清理，I/O 失败会导致上层超时而非虚构互通成功。
func referenceLossProxy(t *testing.T, client, server netip.AddrPort) (netip.AddrPort, *[2]atomic.Bool) {
	t.Helper()
	conn := udpTestSocket(t)
	client = netip.AddrPortFrom(client.Addr().Unmap(), client.Port())
	server = netip.AddrPortFrom(server.Addr().Unmap(), server.Port())
	dropped := &[2]atomic.Bool{}
	done := make(chan struct{})
	go func() {
		defer close(done)
		buffer := make([]byte, enetMaxDatagram)
		for {
			count, source, err := conn.ReadFromUDPAddrPort(buffer)
			if err != nil {
				return
			}
			source = netip.AddrPortFrom(source.Addr().Unmap(), source.Port())
			var target netip.AddrPort
			switch source {
			case client:
				target = server
			case server:
				target = client
			default:
				continue
			}
			packet, err := DecodeENet(buffer[:count])
			if err != nil {
				return
			}
			drop := false
			for _, command := range packet.Commands {
				if source == client && command.Kind == 8 && dropped[0].CompareAndSwap(false, true) {
					drop = true
				}
				if source == server && command.Kind == 1 && command.Channel == 0 && dropped[1].CompareAndSwap(false, true) {
					drop = true
				}
			}
			if drop {
				continue
			}
			if _, err := conn.WriteToUDPAddrPort(buffer[:count], target); err != nil {
				return
			}
		}
	}()
	t.Cleanup(func() { _ = conn.Close(); <-done })
	return conn.LocalAddr().(*net.UDPAddr).AddrPort(), dropped
}

// TestENetReferenceLoss 验证与独立官方库互通时，丢片和丢 ACK 不导致重复应用提交或丢失回显。
func TestENetReferenceLoss(t *testing.T) {
	cmd, server, stderr := referenceProcess(t, "server", 0)
	conn := udpTestSocket(t)
	client := conn.LocalAddr().(*net.UDPAddr).AddrPort()
	remote, dropped := referenceLossProxy(t, client, server)
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
	data := bytes.Repeat([]byte("synthetic-loss-test"), 3000)
	if err := transport.Send(ctx, data); err != nil {
		t.Fatal(err)
	}
	echo, err := transport.Receive(ctx)
	if err != nil || !bytes.Equal(echo, data) {
		t.Fatalf("loss echo length=%d, err=%v", len(echo), err)
	}
	if !dropped[0].Load() || !dropped[1].Load() {
		t.Fatal("loss injection did not occur")
	}
	if err := transport.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatalf("reference exit: %v, %s", err, stderr)
	}
}
