// 本文件仅用loopback验证共享socket虚拟端点的收写隔离、独立时限、关闭责任和有界入站队列。
package nativeproto

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"os"
	"testing"
	"time"
)

// testHostPacketConn用两个新loopback socket创建虚拟端点；返回Host、对端和端点，绑定失败终止测试。
func testHostPacketConn(t *testing.T) (*net.UDPConn, *net.UDPConn, *hostPacketConn) {
	t.Helper()
	host, peer := udpTestSocket(t), udpTestSocket(t)
	endpoint := newHostPacketConn(host, host.LocalAddr().(*net.UDPAddr).AddrPort(), peer.LocalAddr().(*net.UDPAddr).AddrPort())
	t.Cleanup(func() { _ = endpoint.Close() })
	return host, peer, endpoint
}

// assertPacketTimeout验证虚拟超时既满足标准网络Timeout契约，也可用errors.Is识别截止错误。
func assertPacketTimeout(t *testing.T, err error) {
	t.Helper()
	var timeout net.Error
	if !errors.As(err, &timeout) || !timeout.Timeout() || !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("不是网络截止错误: %v", err)
	}
}

// TestPacketConnPublicNilConstructors验证旧导出构造函数在转为接口之前拒绝nil真实socket，避免typed-nil崩溃。
func TestPacketConnPublicNilConstructors(t *testing.T) {
	conn := udpTestSocket(t)
	for _, sockets := range [][2]*net.UDPConn{{nil, nil}, {nil, conn}, {conn, nil}} {
		if _, err := NewClient(sockets[0], sockets[1], ClientConfig{}); err == nil {
			t.Fatal("NewClient接受nil socket")
		}
	}
	if _, err := NewUDPTransport(nil, netip.AddrPort{}, ENetConfig{}); err == nil {
		t.Fatal("NewUDPTransport接受nil socket")
	}
}

// TestHostPacketConnSharedIsolation验证两个端点共用一个真实socket，Host分派复制字节，写目标不能串到另一peer。
func TestHostPacketConnSharedIsolation(t *testing.T) {
	host, peerA, a := testHostPacketConn(t)
	peerB := udpTestSocket(t)
	b := newHostPacketConn(host, a.local, peerB.LocalAddr().(*net.UDPAddr).AddrPort())
	t.Cleanup(func() { _ = b.Close() })
	if a.LocalAddr().(*net.UDPAddr).AddrPort() != a.local || a.RemoteAddr() != nil {
		t.Fatal("虚拟socket未保留未连接UDP地址契约")
	}
	if err := host.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	buffer := make([]byte, 64)
	for _, item := range []struct {
		peer     *net.UDPConn
		endpoint *hostPacketConn
		body     string
	}{{peerA, a, "packet-a"}, {peerB, b, "packet-b"}} {
		if _, err := item.peer.WriteToUDPAddrPort([]byte(item.body), a.local); err != nil {
			t.Fatal(err)
		}
		n, source, err := host.ReadFromUDPAddrPort(buffer)
		if err != nil || source != item.endpoint.remote {
			t.Fatalf("Host read source=%s err=%v", source, err)
		}
		if err := item.endpoint.deliver(buffer[:n], source); err != nil {
			t.Fatal(err)
		}
		clear(buffer) // Host下次读会复用同一缓冲区，端点必须已经复制。
	}
	for _, item := range []struct {
		peer     *net.UDPConn
		endpoint *hostPacketConn
		body     string
	}{{peerA, a, "packet-a"}, {peerB, b, "packet-b"}} {
		if err := item.endpoint.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
			t.Fatal(err)
		}
		n, source, err := item.endpoint.ReadFromUDPAddrPort(buffer)
		if err != nil || source != item.endpoint.remote || string(buffer[:n]) != item.body {
			t.Fatalf("endpoint read=%q source=%s err=%v", buffer[:n], source, err)
		}
		if _, err := item.endpoint.WriteToUDPAddrPort([]byte(item.body), item.endpoint.remote); err != nil {
			t.Fatal(err)
		}
		if err := item.peer.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
			t.Fatal(err)
		}
		n, source, err = item.peer.ReadFromUDPAddrPort(buffer)
		if err != nil || source != a.local || string(buffer[:n]) != item.body {
			t.Fatalf("peer read=%q source=%s err=%v", buffer[:n], source, err)
		}
	}
	if _, err := a.WriteToUDPAddrPort([]byte("wrong peer"), b.remote); err == nil {
		t.Fatal("允许写到另一端点")
	}
}

// TestHostPacketConnReadDeadlineChanges验证已阻塞读取能缩短、延长或清除时限，且不影响另一个端点。
func TestHostPacketConnReadDeadlineChanges(t *testing.T) {
	host, _, a := testHostPacketConn(t)
	b := newHostPacketConn(host, a.local, netip.MustParseAddrPort("127.0.0.1:1"))
	t.Cleanup(func() { _ = b.Close() })
	for _, clearDeadline := range []bool{false, true} {
		if err := a.SetReadDeadline(time.Now().Add(50 * time.Millisecond)); err != nil {
			t.Fatal(err)
		}
		done := make(chan error, 1)
		go func() {
			_, _, err := a.ReadFromUDPAddrPort(make([]byte, 16))
			done <- err
		}()
		deadline := time.Now().Add(time.Second)
		if clearDeadline {
			deadline = time.Time{}
		}
		if err := a.SetReadDeadline(deadline); err != nil {
			t.Fatal(err)
		}
		select {
		case err := <-done:
			t.Fatalf("原时限仍生效: %v", err)
		case <-time.After(75 * time.Millisecond):
		}
		if err := a.SetReadDeadline(time.Now().Add(-time.Second)); err != nil {
			t.Fatal(err)
		}
		select {
		case err := <-done:
			assertPacketTimeout(t, err)
		case <-time.After(time.Second):
			t.Fatal("修改deadline未唤醒阻塞读")
		}
	}
	if err := b.deliver([]byte("unaffected"), b.remote); err != nil {
		t.Fatal(err)
	}
	if _, _, err := b.ReadFromUDPAddrPort(make([]byte, 16)); err != nil {
		t.Fatalf("另一端点受deadline影响: %v", err)
	}
}

// TestHostPacketConnWriteDeadlineChanges验证虚拟写等待的时限可更新，过期写不修改Host时限或发送数据。
func TestHostPacketConnWriteDeadlineChanges(t *testing.T) {
	host, peer, endpoint := testHostPacketConn(t)
	endpoint.writing <- struct{}{} // 模拟本端点已有一次系统写，下一次调用必须等待写位置。
	done := make(chan error, 1)
	go func() {
		_, err := endpoint.WriteToUDPAddrPort([]byte("expired"), endpoint.remote)
		done <- err
	}()
	if err := endpoint.SetWriteDeadline(time.Now().Add(-time.Second)); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		assertPacketTimeout(t, err)
	case <-time.After(time.Second):
		t.Fatal("修改deadline未唤醒等待写")
	}
	<-endpoint.writing
	if _, err := host.WriteToUDPAddrPort([]byte("host alive"), endpoint.remote); err != nil {
		t.Fatalf("虚拟过期污染了Host写时限: %v", err)
	}
	if err := peer.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	buffer := make([]byte, 32)
	if n, _, err := peer.ReadFromUDPAddrPort(buffer); err != nil || string(buffer[:n]) != "host alive" {
		t.Fatalf("收到过期写或Host写失败: %q %v", buffer[:n], err)
	}
	if err := endpoint.SetWriteDeadline(time.Time{}); err != nil {
		t.Fatal(err)
	}
	if err := host.SetWriteDeadline(time.Now().Add(-time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := endpoint.SetWriteDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	_, err := endpoint.WriteToUDPAddrPort([]byte("host deadline"), endpoint.remote)
	assertPacketTimeout(t, err) // 虚拟时限不能覆盖Host已有的系统时限。
}

// TestHostPacketConnCloseOwnership验证Close及时结束阻塞读写、重复调用无害，其他端点和真实socket仍可用。
func TestHostPacketConnCloseOwnership(t *testing.T) {
	host, _, endpoint := testHostPacketConn(t)
	readDone, writeDone := make(chan error, 1), make(chan error, 1)
	endpoint.writing <- struct{}{}
	go func() {
		_, _, err := endpoint.ReadFromUDPAddrPort(make([]byte, 1))
		readDone <- err
	}()
	go func() {
		_, err := endpoint.WriteToUDPAddrPort([]byte("blocked"), endpoint.remote)
		writeDone <- err
	}()
	if err := endpoint.Close(); err != nil {
		t.Fatal(err)
	}
	if err := endpoint.Close(); err != nil {
		t.Fatal(err)
	}
	for _, done := range []chan error{readDone, writeDone} {
		select {
		case err := <-done:
			if !errors.Is(err, net.ErrClosed) {
				t.Fatalf("closed error=%v", err)
			}
		case <-time.After(time.Second):
			t.Fatal("Close未唤醒阻塞操作")
		}
	}
	<-endpoint.writing
	if err := endpoint.deliver([]byte("closed"), endpoint.remote); !errors.Is(err, net.ErrClosed) {
		t.Fatalf("closed deliver=%v", err)
	}
	if err := endpoint.SetReadDeadline(time.Time{}); !errors.Is(err, net.ErrClosed) {
		t.Fatalf("closed read deadline=%v", err)
	}
	if err := endpoint.SetWriteDeadline(time.Time{}); !errors.Is(err, net.ErrClosed) {
		t.Fatalf("closed write deadline=%v", err)
	}
	peer := udpTestSocket(t)
	other := newHostPacketConn(host, endpoint.local, peer.LocalAddr().(*net.UDPAddr).AddrPort())
	defer other.Close()
	if _, err := other.WriteToUDPAddrPort([]byte("alive"), other.remote); err != nil {
		t.Fatalf("Close关闭了Host: %v", err)
	}
	if _, err := peer.WriteToUDPAddrPort([]byte("alive"), endpoint.local); err != nil {
		t.Fatal(err)
	}
	if err := host.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := host.ReadFromUDPAddrPort(make([]byte, 16)); err != nil {
		t.Fatalf("Host无法继续接收: %v", err)
	}
}

// TestHostPacketConnQueueLimit验证第257包明确失败，出队后可继续投递且不会丢弃原来的包。
func TestHostPacketConnQueueLimit(t *testing.T) {
	_, _, endpoint := testHostPacketConn(t)
	for i := 0; i < hostPacketQueueSize; i++ {
		if err := endpoint.deliver([]byte{byte(i)}, endpoint.remote); err != nil {
			t.Fatal(err)
		}
	}
	if err := endpoint.deliver([]byte("overflow"), endpoint.remote); err == nil {
		t.Fatal("超限未报错")
	}
	buffer := make([]byte, 1)
	for i := 0; i < hostPacketQueueSize; i++ {
		n, _, err := endpoint.ReadFromUDPAddrPort(buffer)
		if err != nil || n != 1 || buffer[0] != byte(i) {
			t.Fatalf("packet %d=%v err=%v", i, buffer, err)
		}
	}
	if err := endpoint.deliver([]byte("available"), endpoint.remote); err != nil {
		t.Fatalf("出队后仍不能投递: %v", err)
	}
}

// TestVirtualClientCancellation验证原Client可在虚拟socket上运行，发现等待取消后及时退出且不关闭Host。
func TestVirtualClientCancellation(t *testing.T) {
	client, _ := snapshotFixture(t, func(*ClientConfig) {})
	discoveryHost, enetHost := client.discovery.(*net.UDPConn), client.conn.(*net.UDPConn)
	client.discovery = newHostPacketConn(discoveryHost, discoveryHost.LocalAddr().(*net.UDPAddr).AddrPort(), client.config.RemoteDiscovery)
	client.conn = newHostPacketConn(enetHost, enetHost.LocalAddr().(*net.UDPAddr).AddrPort(), client.config.RemoteDiscovery)
	virtualClient, err := newClient(client.discovery, client.conn, client.config)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := virtualClient.Run(ctx, func(event ClientEvent) error {
			if event.Stage == "discovery_sent" {
				// 先让发现循环进入虚拟读等待，再取消，验证其100ms轮询时限能结束运行。
				time.AfterFunc(10*time.Millisecond, cancel)
			}
			return nil
		})
		done <- err
	}()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Run cancellation=%v", err)
		}
	case <-time.After(time.Second):
		cancel()
		t.Fatal("虚拟Client取消未及时退出")
	}
	for _, socket := range []*net.UDPConn{discoveryHost, enetHost} {
		if _, err := socket.WriteToUDPAddrPort([]byte("still owned by host"), client.config.RemoteDiscovery); err != nil {
			t.Fatalf("Run关闭了共享Host socket: %v", err)
		}
	}
}
