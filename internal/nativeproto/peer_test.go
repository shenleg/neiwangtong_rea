// 本文件以合成双向会话验证节点、握手、READY、心跳和退出顺序，不发送网络流量。
package nativeproto

import (
	"bytes"
	"testing"
	"time"
)

// observeSyntheticFrame 通过实际业务编码向 peer 提交 frame；outbound 指明方向，at 提供单调时间。
// 编码或状态验证失败会终止 t；不访问真实客户端或 socket。
func observeSyntheticFrame(t *testing.T, peer *Peer, outbound bool, frame Frame, at time.Duration) {
	t.Helper()
	data, err := Encode(frame.Command, frame.Payload)
	if err != nil {
		t.Fatal(err)
	}
	if err := peer.Observe(outbound, data, at); err != nil {
		t.Fatal(err)
	}
}

// newSyntheticPeer 创建并观察节点和双向握手/READY，返回 online 离线实例；初始化失败终止 t。
func newSyntheticPeer(t *testing.T) *Peer {
	t.Helper()
	peer, err := NewPeer("local-synthetic", "remote-synthetic", 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	node, err := DecodeNodePacket(syntheticNode(4))
	if err != nil {
		t.Fatal(err)
	}
	node.UserID = "remote-synthetic"
	wire, err := node.Encode()
	if err != nil {
		t.Fatal(err)
	}
	if err := peer.Observe(false, wire, 0); err != nil {
		t.Fatal(err)
	}
	for _, direction := range []bool{false, true} {
		observeSyntheticFrame(t, peer, direction, mustHandshakeFrame(t), 0)
	}
	for _, direction := range []bool{true, false} {
		id := peer.senderID(direction)
		ready, err := EncodeReady(Ready{UserID: id})
		if err != nil {
			t.Fatal(err)
		}
		observeSyntheticFrame(t, peer, direction, ready, 0)
	}
	return peer
}

// TestPeerLifecycle 验证已观察到的双向时序、群请求原文处理，以及 QUIT 后独立传输关闭。
func TestPeerLifecycle(t *testing.T) {
	peer := newSyntheticPeer(t)
	if status := peer.Status(); status.State != PeerOnline || !status.NodeSeen || !status.LocalReady || !status.RemoteReady {
		t.Fatalf("status = %+v", status)
	}
	observeSyntheticFrame(t, peer, false, HeartbeatFrame(), time.Second)
	request, err := EncodeGroupInfoRequest("synthetic-group")
	if err != nil {
		t.Fatal(err)
	}
	observeSyntheticFrame(t, peer, true, request, 2*time.Second)
	observeSyntheticFrame(t, peer, false, Frame{Command: CommandGroupInfoResponse, Payload: `<X_QGROUP_REQ_INFO_RSP><QGROUP_INFO_VER>1</GROUP_INFO_VER></X_QGROUP_REQ_INFO_RSP>`}, 3*time.Second)
	observeSyntheticFrame(t, peer, true, QuitFrame(), 4*time.Second)
	if peer.Status().State != PeerClosing || !peer.Status().LocalQuit {
		t.Fatal("QUIT did not enter closing")
	}
	observeSyntheticFrame(t, peer, false, QuitFrame(), 4*time.Second)
	if err := peer.Disconnect(5 * time.Second); err != nil || peer.Status().State != PeerClosed {
		t.Fatalf("disconnect = %v, status = %+v", err, peer.Status())
	}
}

// TestPeerRejectsWrongOrderAndIdentity 验证 READY 不能代替握手或身份校验；失败不推进任何观察状态。
func TestPeerRejectsWrongOrderAndIdentity(t *testing.T) {
	peer, err := NewPeer("local-synthetic", "remote-synthetic", 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	ready, err := EncodeReady(Ready{UserID: "remote-synthetic"})
	if err != nil {
		t.Fatal(err)
	}
	for _, frame := range []Frame{ready, mustHandshakeFrame(t), HeartbeatFrame(), QuitFrame(), {Command: 1004, Payload: "<X_SEND_MSG/>"}} {
		before := peer.Status()
		wire, err := Encode(frame.Command, frame.Payload)
		if err != nil {
			t.Fatal(err)
		}
		if err := peer.Observe(false, wire, time.Second); err == nil || peer.Status() != before {
			t.Fatalf("accepted or advanced on wrong order: %+v", frame)
		}
	}
	node := syntheticNode(4)
	if err := peer.Observe(false, node, 0); err == nil {
		t.Fatal("accepted different node identity")
	}
	peer = newSyntheticPeer(t)
	for _, direction := range []bool{false, true} {
		wrongID := peer.senderID(!direction)
		frame, err := EncodeReady(Ready{UserID: wrongID})
		if err != nil {
			t.Fatal(err)
		}
		wire, err := Encode(frame.Command, frame.Payload)
		if err != nil {
			t.Fatal(err)
		}
		before := peer.Status()
		if err := peer.Observe(direction, wire, 0); err == nil || peer.Status() != before {
			t.Fatal("accepted opposite sender identity")
		}
	}
}

// TestPeerHeartbeatTimers 验证 60 秒发送与 120 秒接收检查分开，计时本身不制造发送或重试。
func TestPeerHeartbeatTimers(t *testing.T) {
	peer := newSyntheticPeer(t)
	if timers, err := peer.Timers(60*time.Second - 1); err != nil || timers != (PeerTimers{}) {
		t.Fatalf("early timers = %+v, %v", timers, err)
	}
	if timers, err := peer.Timers(60 * time.Second); err != nil || !timers.HeartbeatDue || timers.TimedOut {
		t.Fatalf("60s timers = %+v, %v", timers, err)
	}
	observeSyntheticFrame(t, peer, true, HeartbeatFrame(), 60*time.Second)
	if timers, err := peer.Timers(120 * time.Second); err != nil || !timers.TimedOut || !timers.HeartbeatDue {
		t.Fatalf("120s timers = %+v, %v", timers, err)
	}
	if peer.Status().State != PeerOnline {
		t.Fatal("timer silently disconnected peer")
	}
	observeSyntheticFrame(t, peer, false, HeartbeatFrame(), 120*time.Second)
	if timers, err := peer.Timers(120 * time.Second); err != nil || timers.TimedOut {
		t.Fatalf("inbound activity not counted: %+v, %v", timers, err)
	}
}

// TestPeerRejectsTrailingDataAndChanges 验证一条输入只含一帧，拒绝倒退时间及同会话握手变化。
func TestPeerRejectsTrailingDataAndChanges(t *testing.T) {
	peer := newSyntheticPeer(t)
	frame := HeartbeatFrame()
	wire, err := Encode(frame.Command, frame.Payload)
	if err != nil {
		t.Fatal(err)
	}
	if err := peer.Observe(false, append(append([]byte(nil), wire...), 0), 0); err == nil {
		t.Fatal("ignored trailing byte")
	}
	if err := peer.Observe(false, append(append([]byte(nil), wire...), wire...), 0); err == nil {
		t.Fatal("merged frames without boundaries")
	}
	modified := syntheticHandshake()
	modified.Features[0].Value = "changed"
	changed, err := EncodeHandshake(modified)
	if err != nil {
		t.Fatal(err)
	}
	data, err := Encode(changed.Command, changed.Payload)
	if err != nil {
		t.Fatal(err)
	}
	if err := peer.Observe(false, data, 0); err == nil {
		t.Fatal("accepted handshake change")
	}
	observeSyntheticFrame(t, peer, false, frame, time.Second)
	if err := peer.Observe(false, wire, 0); err == nil {
		t.Fatal("accepted backwards observation")
	}
	if _, err := peer.Timers(0); err == nil {
		t.Fatal("accepted backwards timer")
	}
	if err := peer.Disconnect(0); err == nil {
		t.Fatal("accepted backwards disconnect")
	}
}

// TestPeerCloseAndConstructor 验证关闭后拒绝消息、没有隐式重连，也不允许本机与对端身份相同。
func TestPeerCloseAndConstructor(t *testing.T) {
	for _, ids := range [][2]string{{"", "remote"}, {"same", "same"}, {"local", "\xff"}} {
		if _, err := NewPeer(ids[0], ids[1], 1024); err == nil {
			t.Fatal("accepted invalid identity")
		}
	}
	if _, err := NewPeer("local", "remote", 8); err == nil {
		t.Fatal("accepted impossible frame budget")
	}
	peer := newSyntheticPeer(t)
	observeSyntheticFrame(t, peer, true, QuitFrame(), 0)
	wire, err := Encode(CommandHeartbeat, HeartbeatFrame().Payload)
	if err != nil {
		t.Fatal(err)
	}
	if err := peer.Observe(false, wire, 0); err == nil {
		t.Fatal("accepted activity after QUIT")
	}
	if err := peer.Disconnect(0); err != nil {
		t.Fatal(err)
	}
	if err := peer.Disconnect(0); err != nil {
		t.Fatal(err)
	}
	if err := peer.Observe(false, wire, 0); err == nil {
		t.Fatal("reopened closed peer")
	}
	if timers, err := peer.Timers(time.Hour); err != nil || timers != (PeerTimers{}) {
		t.Fatalf("closed timers = %+v, %v", timers, err)
	}
}

// TestPeerFragmentedHandshakeIntegration 验证 ENet 乱序分片重组完成后，才将完整握手交给会话。
func TestPeerFragmentedHandshakeIntegration(t *testing.T) {
	peer, err := NewPeer("local-synthetic", "remote-synthetic", 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	node, err := DecodeNodePacket(syntheticNode(4))
	if err != nil {
		t.Fatal(err)
	}
	node.UserID = "remote-synthetic"
	nodeWire, err := node.Encode()
	if err != nil {
		t.Fatal(err)
	}
	if err := peer.Observe(false, nodeWire, 0); err != nil {
		t.Fatal(err)
	}
	frame := mustHandshakeFrame(t)
	wire, err := Encode(frame.Command, frame.Payload)
	if err != nil {
		t.Fatal(err)
	}
	r, err := NewReassembler(ReassemblyLimits{MaxMessageBytes: 1 << 20, MaxBufferedBytes: 2 << 20, MaxAssemblies: 2, MaxFragments: 4, TTL: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	cut := len(wire) / 2
	for _, number := range []int{1, 0} {
		start, end := 0, cut
		if number == 1 {
			start, end = cut, len(wire)
		}
		command := ENetCommand{Kind: 8, Channel: 0, Sequence: uint16(number + 1), AckRequired: true,
			Data: wire[start:end], Fragment: &ENetFragment{StartSequence: 1, Count: 2, Number: uint32(number), TotalLength: uint32(len(wire)), Offset: uint32(start)}}
		encoded, err := EncodeENet(ENetPacket{Commands: []ENetCommand{command}})
		if err != nil {
			t.Fatal(err)
		}
		packet, err := DecodeENet(encoded)
		if err != nil {
			t.Fatal(err)
		}
		assembled, err := r.Add(ENetStream{Flow: "remote-to-local"}, packet.Commands[0], 0)
		if err != nil {
			t.Fatal(err)
		}
		if assembled == nil {
			continue
		}
		if !bytes.Equal(assembled, wire) {
			t.Fatal("reassembled bytes changed")
		}
		if err := peer.Observe(false, assembled, 0); err != nil {
			t.Fatal(err)
		}
	}
	if peer.Status().State != PeerHandshaking || !peer.Status().RemoteHandshake || peer.Status().LocalHandshake {
		t.Fatalf("status = %+v", peer.Status())
	}
}
