// 本文件仅使用loopback验证联系人连接中断后自动恢复，且不重发旧消息。
package nativeproto

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"sync/atomic"
	"testing"
	"time"
)

// TestHostReconnectsWithoutResending 验证主动和被动连接失败后自动重建，原消息不会再次提交。
func TestHostReconnectsWithoutResending(t *testing.T) {
	for _, order := range []byte{1, 250} {
		t.Run(fmt.Sprintf("order-%d", order), func(t *testing.T) { testHostReconnectsWithoutResending(t, order) })
	}
}

// testHostReconnectsWithoutResending 用给定会话顺序运行单边故障和消息未知结果的loopback验证；失败时终止测试。
func testHostReconnectsWithoutResending(t *testing.T, order byte) {
	t.Helper()
	d1, c1 := hostTestSocket(t, "127.0.0.1"), hostTestSocket(t, "127.0.0.1")
	d2, c2 := hostTestSocket(t, "127.0.0.2"), hostTestSocket(t, "127.0.0.2")
	n1 := testClientNode(t, d1, c1, "reconnect-host-1", order)
	n2 := testClientNode(t, d2, c2, "reconnect-host-2", 100)
	prefix := netip.MustParsePrefix("127.0.0.0/8")
	h1, err := NewHost(d1, c1, HostConfig{Client: hostTestConfig(t, n1), Prefix: prefix, Seeds: []netip.AddrPort{d2.LocalAddr().(*net.UDPAddr).AddrPort()}, MaxPeers: 1})
	if err != nil {
		t.Fatal(err)
	}
	h2, err := NewHost(d2, c2, HostConfig{Client: hostTestConfig(t, n2), Prefix: prefix, Seeds: []netip.AddrPort{d1.LocalAddr().(*net.UDPAddr).AddrPort()}, MaxPeers: 1})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	received := make(chan struct{}, 4)
	done := make(chan residentRunResult, 2)
	var breakNext atomic.Bool
	go func() {
		result, err := h1.Run(ctx, func(event ClientEvent) error {
			if event.Stage == "chat_send_planned" && breakNext.CompareAndSwap(true, false) {
				h1.mu.Lock()
				peer := h1.identities[n2.UserID]
				h1.mu.Unlock()
				return peer.conn.Close()
			}
			return nil
		})
		done <- residentRunResult{result, err}
	}()
	go func() {
		result, err := h2.Run(ctx, func(event ClientEvent) error {
			if event.Stage == "unsupported_business_command" && event.Command == CommandUserMessage {
				received <- struct{}{}
			}
			return nil
		})
		done <- residentRunResult{result, err}
	}()
	awaitHostSnapshot(t, h1, func(s ClientSnapshot) bool { return len(s.OnlineUserIDs) == 1 })
	awaitHostSnapshot(t, h2, func(s ClientSnapshot) bool { return len(s.OnlineUserIDs) == 1 })
	h1.mu.Lock()
	oldPeerID := h1.identities[n2.UserID].client.config.Transport.LocalPeerID
	h1.mu.Unlock()
	result, err := h1.SendUser(ctx, n2.UserID, "重连前的合成消息")
	if err != nil || !result.Submitted {
		t.Fatalf("首次发送: %+v %v", result, err)
	}
	select {
	case <-received:
	case <-time.After(time.Second):
		t.Fatal("合成对端未收到消息")
	}
	// 在Queue之前关闭虚拟端点，模拟已接受提交但写出失败；这个结果不得在重连后重发。
	breakNext.Store(true)
	unknown, err := h1.SendUser(ctx, n2.UserID, "结果未知的合成消息")
	if err == nil || !unknown.Submitted || unknown.TransportAcknowledged || unknown.Delivery != "unknown" || unknown.MessageID == "" {
		t.Fatalf("未保留未知结果: %+v %v", unknown, err)
	}
	awaitHostSnapshot(t, h1, func(s ClientSnapshot) bool { return len(s.OnlineUserIDs) == 0 })
	awaitHostSnapshot(t, h2, func(s ClientSnapshot) bool { return len(s.OnlineUserIDs) == 0 })
	awaitHostSnapshot(t, h1, func(s ClientSnapshot) bool { return len(s.OnlineUserIDs) == 1 })
	awaitHostSnapshot(t, h2, func(s ClientSnapshot) bool { return len(s.OnlineUserIDs) == 1 })
	h1.mu.Lock()
	newPeerID := h1.identities[n2.UserID].client.config.Transport.LocalPeerID
	h1.mu.Unlock()
	if oldPeerID == newPeerID {
		t.Fatal("重连复用了旧peer ID，旧数据包可能进入新会话")
	}
	// 模拟旧连接迟到的DISCONNECT；新连接必须忽略它。
	oldPacket, err := EncodeENet(ENetPacket{PeerID: oldPeerID, Commands: []ENetCommand{{Kind: 4, Channel: 255, Control: make([]byte, 4)}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c2.WriteToUDPAddrPort(oldPacket, c1.LocalAddr().(*net.UDPAddr).AddrPort()); err != nil {
		t.Fatal(err)
	}
	select {
	case <-received:
		t.Fatal("重连时自动重发了旧消息")
	case <-time.After(200 * time.Millisecond):
	}
	fresh, err := h1.SendUser(ctx, n2.UserID, "重连后的新消息")
	if err != nil || !fresh.Submitted || !fresh.TransportAcknowledged || fresh.MessageID == unknown.MessageID {
		t.Fatalf("新连接不能发送新消息: %+v %v", fresh, err)
	}
	select {
	case <-received:
	case <-time.After(time.Second):
		t.Fatal("合成对端未收到新消息")
	}
	cancel()
	for range 2 {
		select {
		case <-done:
		case <-time.After(4 * time.Second):
			t.Fatal("Host未退出")
		}
	}
}

// TestHostReconnectsMovedPeer 验证失联身份从新端点公告时恢复连接，另一个联系人不受影响。
func TestHostReconnectsMovedPeer(t *testing.T) {
	messages := make(chan hostObservedText, 8)
	run := hostSyntheticFixture(t, 2, nil, nil, func(index int, frame Frame) (*Frame, error) {
		message, err := decodeHostText(index, frame)
		if err != nil {
			return nil, err
		}
		messages <- message
		if index == 0 && message.text == "触发连接失败" {
			return &Frame{Command: CommandReady, Payload: "<broken/>"}, nil
		}
		return nil, nil
	})
	awaitHostSnapshot(t, run.host, func(s ClientSnapshot) bool { return len(s.OnlineUserIDs) == 2 })
	if _, err := run.host.SendUser(context.Background(), "peer-original-0", "触发连接失败"); err != nil {
		t.Fatal(err)
	}
	<-messages
	awaitResidentEvent(t, run.events, "peer_failed", "")
	awaitHostSnapshot(t, run.host, func(s ClientSnapshot) bool { return len(s.OnlineUserIDs) == 1 })
	// 两次退避发现可能跨过三个调度tick，不能复用只等两秒的普通事件等待器。
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	var retries []int64
	for len(retries) < 2 {
		select {
		case event := <-run.events:
			if event.Stage == "reconnect_discovery" {
				retries = append(retries, event.ElapsedMS)
			}
		case <-deadline.C:
			t.Fatalf("未收到两次退避发现: %v", retries)
		}
	}
	if retries[1]-retries[0] < 900 {
		t.Fatalf("离线端点的重连间隔过短: %dms", retries[1]-retries[0])
	}

	d, c := hostTestSocket(t, "127.0.0.9"), hostTestSocket(t, "127.0.0.9")
	node := testClientNode(t, d, c, "peer-original-0", 1)
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()
	peerDone := make(chan error, 1)
	go func() {
		peerDone <- serveSyntheticNativeWithHandler(ctx, d, c, node, true, func(frame Frame) (*Frame, error) {
			message, err := decodeHostText(0, frame)
			if err == nil {
				messages <- message
			}
			return nil, err
		})
	}()
	wire, err := node.Encode()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.WriteToUDPAddrPort(wire, run.host.discovery.LocalAddr().(*net.UDPAddr).AddrPort()); err != nil {
		t.Fatal(err)
	}
	snapshot := awaitHostSnapshot(t, run.host, func(s ClientSnapshot) bool { return len(s.OnlineUserIDs) == 2 && len(s.Contacts) == 2 })
	for _, contact := range snapshot.Contacts {
		if contact.UserID == node.UserID && contact.Discovery != d.LocalAddr().(*net.UDPAddr).AddrPort() {
			t.Fatalf("联系人端点未更新: %+v", contact)
		}
	}
	for i := 0; i < 2; i++ {
		if _, err := run.host.SendUser(ctx, fmt.Sprintf("peer-original-%d", i), "重连后独立路由"); err != nil {
			t.Fatal(err)
		}
		select {
		case message := <-messages:
			if message.peer != i {
				t.Fatalf("发给%d却由%d接收", i, message.peer)
			}
		case <-time.After(time.Second):
			t.Fatal("合成联系人未收到新消息")
		}
	}
	run.cancel()
	select {
	case result := <-run.done:
		if result.err != nil || result.result.GracefulExit || result.result.ChatSends != 3 {
			t.Fatalf("重连丢失历史发送或失败事实: %+v %v", result.result, result.err)
		}
	case <-time.After(4 * time.Second):
		t.Fatal("Host未退出")
	}
	select {
	case err := <-peerDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("重连后的合成对端未退出")
	}
}
