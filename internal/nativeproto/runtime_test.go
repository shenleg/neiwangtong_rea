// 本文件以显式时间和合成 datagram 验证 ENet 运行核心的丢包、窗口、乱序、重发和断开，不创建网络。
package nativeproto

import (
	"bytes"
	"encoding/binary"
	"strings"
	"testing"
	"time"
)

// runtimeConfig 返回显式、小型传输预算；peer/nonce 只标识本次合成连接，不含真实身份。
func runtimeConfig(peer uint16, nonce byte) ENetConfig {
	return ENetConfig{LocalPeerID: peer, ConnectID: [4]byte{nonce, 2, 3, 4}, MTU: 1400, WindowBytes: 65536,
		MaxMessageBytes: 1 << 20, MaxQueuedBytes: 2 << 20, MaxPendingCommands: 1024,
		RetransmitAfter: 10 * time.Millisecond, MaxAttempts: 4, ReceiveTimeout: 2 * time.Second, PingInterval: time.Second}
}

// runtimeHandle 将 events 中所有 datagram 交给 receiver，返回 ACK/消息的合并结果；失败终止 t。
func runtimeHandle(t *testing.T, receiver *ENetRuntime, events ENetEvents, at time.Duration) ENetEvents {
	t.Helper()
	var result ENetEvents
	for _, data := range events.Datagrams {
		response, err := receiver.Handle(data, at)
		if err != nil {
			t.Fatal(err)
		}
		result.Datagrams = append(result.Datagrams, response.Datagrams...)
		result.Messages = append(result.Messages, response.Messages...)
	}
	return result
}

// runtimePair 建立两个独立 peer 的合成握手，返回已连接的主动/被动核心；不会调用真实 socket。
func runtimePair(t *testing.T) (*ENetRuntime, *ENetRuntime) {
	t.Helper()
	a, err := NewENetRuntime(runtimeConfig(7, 1))
	if err != nil {
		t.Fatal(err)
	}
	b, err := NewENetRuntime(runtimeConfig(9, 2))
	if err != nil {
		t.Fatal(err)
	}
	start, err := a.Start(0)
	if err != nil {
		t.Fatal(err)
	}
	verify := runtimeHandle(t, b, start, 0)
	ack := runtimeHandle(t, a, verify, 0)
	runtimeHandle(t, b, ack, 0)
	if a.Status().State != ENetConnected || b.Status().State != ENetConnected {
		t.Fatal("pair not connected")
	}
	return a, b
}

// TestRuntimeHandshakeLoss 验证 VERIFY 或最终 ACK 丢失后，重复建连不会分配新会话或丢掉最终 ACK。
func TestRuntimeHandshakeLoss(t *testing.T) {
	a, err := NewENetRuntime(runtimeConfig(7, 1))
	if err != nil {
		t.Fatal(err)
	}
	b, err := NewENetRuntime(runtimeConfig(9, 2))
	if err != nil {
		t.Fatal(err)
	}
	start, err := a.Start(0)
	if err != nil {
		t.Fatal(err)
	}
	firstVerify := runtimeHandle(t, b, start, 0)
	if b.Status().State != ENetAccepting {
		t.Fatal("listener prematurely connected")
	}
	verify, err := b.Tick(10 * time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if len(firstVerify.Datagrams) != 1 || len(verify.Datagrams) != 1 {
		t.Fatal("missing verify retry")
	}
	firstACK := runtimeHandle(t, a, verify, 11*time.Millisecond)
	if a.Status().State != ENetConnected || len(firstACK.Datagrams) != 1 {
		t.Fatal("active peer not connected")
	}
	verify, err = b.Tick(30 * time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	ack := runtimeHandle(t, a, verify, 31*time.Millisecond)
	runtimeHandle(t, b, ack, 32*time.Millisecond)
	if b.Status().State != ENetConnected || b.Status().PendingCommands != 0 {
		t.Fatal("lost final ACK was not recovered")
	}
}

// TestRuntimeReliableLossAndDuplicate 验证重传保留序号与字节，对端只首次交付，旧 sent-time 的合法 ACK 仍可确认。
func TestRuntimeReliableLossAndDuplicate(t *testing.T) {
	a, b := runtimePair(t)
	message := []byte("synthetic message")
	first, err := a.Queue(message, time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	message[0] = 'X'
	ack := runtimeHandle(t, b, first, 2*time.Millisecond)
	if len(ack.Messages) != 1 || string(ack.Messages[0]) != "synthetic message" {
		t.Fatal("message ownership changed")
	}
	retry, err := a.Tick(11 * time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	firstPacket, err := DecodeENet(first.Datagrams[0])
	if err != nil {
		t.Fatal(err)
	}
	retryPacket, err := DecodeENet(retry.Datagrams[0])
	if err != nil {
		t.Fatal(err)
	}
	if firstPacket.Commands[0].Sequence != retryPacket.Commands[0].Sequence || !bytes.Equal(firstPacket.Commands[0].Data, retryPacket.Commands[0].Data) {
		t.Fatal("retransmission changed submission")
	}
	duplicate := runtimeHandle(t, b, retry, 12*time.Millisecond)
	if len(duplicate.Messages) != 0 || len(duplicate.Datagrams) != 1 {
		t.Fatal("duplicate was redelivered or not ACKed")
	}
	runtimeHandle(t, a, ack, 13*time.Millisecond)
	if a.Status().PendingCommands != 0 || a.Status().QueuedBytes != 0 {
		t.Fatal("delayed valid ACK not accepted")
	}
}

// TestRuntimeWrongACKAndSession 验证无匹配 sent-time 的 ACK、旧 peer/session 不能释放新任务或推进接收计时。
func TestRuntimeWrongACKAndSession(t *testing.T) {
	a, b := runtimePair(t)
	sent, err := a.Queue([]byte("payload"), time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	ack := runtimeHandle(t, b, sent, 2*time.Millisecond)
	packet, err := DecodeENet(ack.Datagrams[0])
	if err != nil {
		t.Fatal(err)
	}
	packet.Commands[0].Control[3] ^= 0xff
	wrong, err := EncodeENet(packet)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.Handle(wrong, 3*time.Millisecond); err != nil || a.Status().PendingCommands != 1 {
		t.Fatal("forged ACK changed pending state")
	}
	packet, err = DecodeENet(ack.Datagrams[0])
	if err != nil {
		t.Fatal(err)
	}
	packet.SessionID = (packet.SessionID + 1) & 3
	old, err := EncodeENet(packet)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.Handle(old, 4*time.Millisecond); err != nil || a.Status().PendingCommands != 1 {
		t.Fatal("old session changed pending state")
	}
	runtimeHandle(t, a, ack, 5*time.Millisecond)
	if a.Status().PendingCommands != 0 {
		t.Fatal("valid ACK rejected")
	}
}

// TestRuntimeOrderedAndFragmented 验证消息乱序和片序乱序均按可靠序号首次交付，不跨消息混片。
func TestRuntimeOrderedAndFragmented(t *testing.T) {
	a, b := runtimePair(t)
	first, err := a.Queue([]byte("first"), 0)
	if err != nil {
		t.Fatal(err)
	}
	second, err := a.Queue(bytes.Repeat([]byte("part"), 1000), 0)
	if err != nil {
		t.Fatal(err)
	}
	for i, j := 0, len(second.Datagrams)-1; i < j; i, j = i+1, j-1 {
		second.Datagrams[i], second.Datagrams[j] = second.Datagrams[j], second.Datagrams[i]
	}
	out := runtimeHandle(t, b, second, 0)
	if len(out.Messages) != 0 {
		t.Fatal("out-of-order later message delivered")
	}
	runtimeHandle(t, a, out, 0)
	out = runtimeHandle(t, b, first, 0)
	if len(out.Messages) != 2 || string(out.Messages[0]) != "first" || !bytes.Equal(out.Messages[1], bytes.Repeat([]byte("part"), 1000)) {
		t.Fatal("ordered message delivery failed")
	}
	runtimeHandle(t, a, out, 0)
	if a.Status().PendingCommands != 0 || b.Status().ReceiveBytes != 0 {
		t.Fatal("budgets not released")
	}
}

// TestRuntimeSequenceWrap 验证分片跨 65535→0 仍按完整消息推进，不误判成旧包。
func TestRuntimeSequenceWrap(t *testing.T) {
	a, b := runtimePair(t)
	a.nextData = 65534
	b.lastData = 65534
	payload := bytes.Repeat([]byte("x"), 3000)
	sent, err := a.Queue(payload, 0)
	if err != nil {
		t.Fatal(err)
	}
	out := runtimeHandle(t, b, sent, 0)
	if len(out.Messages) != 1 || !bytes.Equal(out.Messages[0], payload) {
		t.Fatal("wrap failed")
	}
	runtimeHandle(t, a, out, 0)
	if a.Status().PendingCommands != 0 {
		t.Fatal("wrapped ACK not accepted")
	}
}

// TestRuntimeByteWindow 验证仅发送窗口允许的数据，ACK 后才发送余下已排队片段，而不是重新 Queue。
func TestRuntimeByteWindow(t *testing.T) {
	a, b := runtimePair(t)
	a.remoteWindow = 4096
	sent, err := a.Queue(bytes.Repeat([]byte("x"), 10000), 0)
	if err != nil {
		t.Fatal(err)
	}
	if a.Status().InFlightBytes > 4096 || len(sent.Datagrams) == 0 || a.Status().PendingCommands <= len(sent.Datagrams) {
		t.Fatal("window did not constrain initial flush")
	}
	var messages [][]byte
	for len(sent.Datagrams) > 0 {
		incoming := runtimeHandle(t, b, sent, 0)
		messages = append(messages, incoming.Messages...)
		sent = runtimeHandle(t, a, incoming, 0)
	}
	if len(messages) != 1 || len(messages[0]) != 10000 || a.Status().PendingCommands != 0 {
		t.Fatal("window failed to advance")
	}
}

// TestRuntimeRetryExhaustionAndTimeout 验证超时明确失败且保留未知发送，不自动重新连接或再次排队。
func TestRuntimeRetryExhaustionAndTimeout(t *testing.T) {
	r, err := NewENetRuntime(runtimeConfig(1, 3))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Start(0); err != nil {
		t.Fatal(err)
	}
	for _, at := range []time.Duration{10 * time.Millisecond, 30 * time.Millisecond, 70 * time.Millisecond} {
		if _, err := r.Tick(at); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := r.Tick(150 * time.Millisecond); err == nil || r.Status().State != ENetFailed || r.Status().PendingCommands != 1 {
		t.Fatal("retry exhaustion did not preserve unknown outcome")
	}
	if _, err := r.Start(time.Second); err == nil {
		t.Fatal("implicitly reopened failed runtime")
	}
	a, _ := runtimePair(t)
	if _, err := a.Tick(2 * time.Second); err == nil || !strings.Contains(err.Error(), "接收超时") {
		t.Fatal("missing receive timeout")
	}
}

// TestRuntimeDisconnectAndPing 验证 PING 与应用消息不同，且断开只有相应 ACK 才完成。
func TestRuntimeDisconnectAndPing(t *testing.T) {
	a, b := runtimePair(t)
	ping, err := a.Tick(time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if len(ping.Datagrams) != 1 {
		t.Fatal("missing idle PING")
	}
	packet, err := DecodeENet(ping.Datagrams[0])
	if err != nil || packet.Commands[0].Kind != 5 {
		t.Fatal("not PING")
	}
	if _, err := a.Disconnect(time.Second); err == nil {
		t.Fatal("disconnected with pending PING")
	}
	ack := runtimeHandle(t, b, ping, time.Second)
	if len(ack.Messages) != 0 {
		t.Fatal("PING became application data")
	}
	runtimeHandle(t, a, ack, time.Second)
	disconnect, err := a.Disconnect(time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if a.Status().State != ENetDisconnecting {
		t.Fatal("premature close")
	}
	response := runtimeHandle(t, b, disconnect, time.Second)
	if b.Status().State != ENetClosed || len(response.Datagrams) != 1 {
		t.Fatal("remote disconnected without ACK")
	}
	runtimeHandle(t, a, response, time.Second)
	if a.Status().State != ENetClosed {
		t.Fatal("disconnect ACK did not close")
	}
	if _, err := a.Queue([]byte("new"), time.Second); err == nil {
		t.Fatal("sent after close")
	}
}

// TestRuntimeAsymmetricSessionNegotiation 以不同 incoming/outgoing 值验证 ENet 的交叉赋值，不靠对称测试掩盖交换错误。
func TestRuntimeAsymmetricSessionNegotiation(t *testing.T) {
	r, err := NewENetRuntime(runtimeConfig(9, 2))
	if err != nil {
		t.Fatal(err)
	}
	body := r.connectionBody(7, 2, 1, true)
	stamp := uint16(77)
	data, err := EncodeENet(ENetPacket{PeerID: 4095, SentTime: &stamp, Commands: []ENetCommand{{Kind: 2, Channel: 255, Sequence: 1, AckRequired: true, Control: body}}})
	if err != nil {
		t.Fatal(err)
	}
	events, err := r.Handle(data, 0)
	if err != nil {
		t.Fatal(err)
	}
	verify, err := DecodeENet(events.Datagrams[0])
	if err != nil {
		t.Fatal(err)
	}
	if verify.SessionID != 3 || verify.Commands[0].Control[2] != 3 || verify.Commands[0].Control[3] != 2 || r.incomingSession != 2 || r.outgoingSession != 3 {
		t.Fatal("session IDs not assigned like ENet 1.3")
	}
	if !bytes.Equal(verify.Commands[0].Control[36:40], body[36:40]) {
		t.Fatal("opaque connect ID changed byte order")
	}
}

// TestRuntimeRejectsWrongHandshakeAndBudgets 验证错误连接标识、未支持 channel 和队列拒绝不会悄悄降级。
func TestRuntimeRejectsWrongHandshakeAndBudgets(t *testing.T) {
	a, err := NewENetRuntime(runtimeConfig(7, 1))
	if err != nil {
		t.Fatal(err)
	}
	b, err := NewENetRuntime(runtimeConfig(9, 2))
	if err != nil {
		t.Fatal(err)
	}
	start, err := a.Start(0)
	if err != nil {
		t.Fatal(err)
	}
	verify := runtimeHandle(t, b, start, 0)
	packet, err := DecodeENet(verify.Datagrams[0])
	if err != nil {
		t.Fatal(err)
	}
	packet.Commands[0].Control[36] ^= 0xff
	wrong, err := EncodeENet(packet)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.Handle(wrong, 0); err == nil || a.Status().State != ENetFailed {
		t.Fatal("wrong nonce connected")
	}
	a, _ = runtimePair(t)
	before := a.Status()
	if _, err := a.Queue(make([]byte, (1<<20)+1), 0); err == nil || a.Status() != before {
		t.Fatal("oversized data partially queued")
	}
	if _, err := a.Queue(nil, 0); err == nil || a.Status() != before {
		t.Fatal("empty data queued")
	}
	stamp := uint16(0)
	data, err := EncodeENet(ENetPacket{PeerID: a.config.LocalPeerID, SessionID: a.incomingSession, SentTime: &stamp,
		Commands: []ENetCommand{{Kind: 6, Channel: 1, Sequence: 1, AckRequired: true, Data: []byte("x")}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.Handle(data, 0); err == nil {
		t.Fatal("unsupported session data accepted")
	}
}

// TestRuntimeDataBeforeHandshakeACK 验证 UDP 乱序让数据先到时不提前确认或破坏握手，待重传再交付。
func TestRuntimeDataBeforeHandshakeACK(t *testing.T) {
	a, err := NewENetRuntime(runtimeConfig(7, 1))
	if err != nil {
		t.Fatal(err)
	}
	b, err := NewENetRuntime(runtimeConfig(9, 2))
	if err != nil {
		t.Fatal(err)
	}
	connect, err := a.Start(0)
	if err != nil {
		t.Fatal(err)
	}
	verify := runtimeHandle(t, b, connect, 0)
	ack := runtimeHandle(t, a, verify, 0)
	data, err := a.Queue([]byte("out-of-order-after-verify"), 0)
	if err != nil {
		t.Fatal(err)
	}
	early := runtimeHandle(t, b, data, 0)
	if len(early.Datagrams) != 0 || len(early.Messages) != 0 || b.Status().State != ENetAccepting {
		t.Fatal("data before ACK corrupted handshake")
	}
	runtimeHandle(t, b, ack, time.Millisecond)
	retry, err := a.Tick(10 * time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	out := runtimeHandle(t, b, retry, 11*time.Millisecond)
	if len(out.Messages) != 1 || string(out.Messages[0]) != "out-of-order-after-verify" {
		t.Fatal("retransmission not delivered after handshake")
	}
	runtimeHandle(t, a, out, 12*time.Millisecond)
	if a.Status().PendingCommands != 0 {
		t.Fatal("retransmission not confirmed")
	}
}

// TestRuntimeStaleConnectionDoesNotSubmit 验证同步驱动停顿后，已超时的连接不先发新业务数据再报超时。
func TestRuntimeStaleConnectionDoesNotSubmit(t *testing.T) {
	a, _ := runtimePair(t)
	events, err := a.Queue([]byte("must not be sent"), 2*time.Second)
	if err == nil || len(events.Datagrams) != 0 || a.Status().PendingCommands != 0 || a.Status().State != ENetFailed {
		t.Fatal("stale connection submitted data")
	}
}

// TestRuntimeOutstandingSequenceWindow 验证最早未确认的序号限制窗口，不能只凭剩余字节继续绕回覆盖它。
func TestRuntimeOutstandingSequenceWindow(t *testing.T) {
	a, _ := runtimePair(t)
	if _, err := a.Queue([]byte("pending"), 0); err != nil {
		t.Fatal(err)
	}
	a.nextData = 4096
	before := a.Status()
	if _, err := a.Queue([]byte("not queued"), 0); err == nil || a.Status() != before {
		t.Fatal("sequence span did not prevent reuse")
	}
}

// TestRuntimeACKNeedsEchoedTimestamp 验证伪造序号/时间的 ACK 无法替代 VERIFY_CONNECT 完成握手。
func TestRuntimeACKNeedsEchoedTimestamp(t *testing.T) {
	r, err := NewENetRuntime(runtimeConfig(7, 1))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Start(0); err != nil {
		t.Fatal(err)
	}
	body := make([]byte, 4)
	binary.BigEndian.PutUint16(body, 1)
	ack, err := EncodeENet(ENetPacket{PeerID: 7, Commands: []ENetCommand{{Kind: 1, Channel: 255, Sequence: 1, Control: body}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Handle(ack, 0); err != nil || r.Status().State != ENetConnecting || r.Status().PendingCommands != 1 {
		t.Fatal("ACK replaced VERIFY")
	}
}
