// 本文件以合成1008帧验证输入通知的结构、握手身份、五秒有效期与离线隔离，不访问真实网络。
package nativeproto

import (
	"context"
	"net/netip"
	"strings"
	"testing"
	"time"
)

// TestDecodeTyping 验证静态恢复的WRITTING拼写、TYPE=0与严格结构，不猜测停止值或附加身份字段。
func TestDecodeTyping(t *testing.T) {
	valid := Frame{Command: CommandTyping, Payload: `<X_SEND_WRITTING docver="1"><TYPE>0</TYPE></X_SEND_WRITTING>`}
	if err := DecodeTyping(valid); err != nil {
		t.Fatal(err)
	}
	for _, payload := range []string{
		"", strings.ReplaceAll(valid.Payload, "WRITTING", "WRITING"),
		strings.ReplaceAll(valid.Payload, ">0<", ">1<"),
		strings.ReplaceAll(valid.Payload, ">0<", ">-1<"),
		strings.ReplaceAll(valid.Payload, "<TYPE>0</TYPE>", "<TYPE>0</TYPE><TYPE>0</TYPE>"),
		strings.ReplaceAll(valid.Payload, "<TYPE>0</TYPE>", "<TYPE>0</TYPE><USER_ID>fake</USER_ID>"),
		strings.ReplaceAll(valid.Payload, `docver="1"`, `docver="2"`),
	} {
		if DecodeTyping(Frame{Command: CommandTyping, Payload: payload}) == nil {
			t.Fatalf("不应接受输入通知: %q", payload)
		}
	}
	valid.Command = CommandHeartbeat
	if DecodeTyping(valid) == nil {
		t.Fatal("命令不符未拒绝")
	}
}

// TestHostTypingIgnoresStoppedAndMismatchedPeers 验证多对端输入状态只归属当前READY身份，停止连接不会残留提示。
func TestHostTypingIgnoresStoppedAndMismatchedPeers(t *testing.T) {
	host := &Host{config: HostConfig{Client: ClientConfig{Node: NodePacket{UserID: "own"}}}, accepting: true, peers: make(map[netip.AddrPort]*hostPeer)}
	for i, id := range []string{"first", "stopped", "mismatch"} {
		client := &Client{}
		remoteID := id
		if id == "mismatch" {
			remoteID = "other"
		}
		client.snapshot.Store(&ClientSnapshot{Online: true, PeerUserID: remoteID, Typing: []TypingStatus{{UserID: remoteID, Until: time.Now().Add(TypingDuration)}}})
		host.peers[netip.AddrPortFrom(netip.MustParseAddr("127.0.0.2"), uint16(19000+i))] = &hostPeer{node: NodePacket{UserID: id}, client: client, stopped: id == "stopped"}
	}
	state := host.Snapshot()
	if len(state.Typing) != 1 || state.Typing[0].UserID != "first" {
		t.Fatalf("输入通知跨身份或停止连接泄露: %+v", state.Typing)
	}
}

// TestTypingSnapshotUsesReadyIdentity 验证通知绑定当前握手身份而非XML，并重复重置五秒、到期/断连后清除且不产生消息或ACK。
func TestTypingSnapshotUsesReadyIdentity(t *testing.T) {
	c, _ := incomingTestClient(t, nil)
	c.lifecycle.Store(clientOnline)
	wire, err := Encode(CommandTyping, `<X_SEND_WRITTING docver="1"><TYPE>0</TYPE></X_SEND_WRITTING>`)
	if err != nil {
		t.Fatal(err)
	}
	before := time.Now()
	if err := c.receive(context.Background(), wire); err != nil {
		t.Fatal(err)
	}
	snapshot := c.Snapshot()
	if len(snapshot.Typing) != 1 || snapshot.Typing[0].UserID != "sender" || snapshot.Typing[0].Until.Before(before.Add(TypingDuration)) || len(c.incomingAcks) != 0 {
		t.Fatalf("输入通知身份或有效期错误: %+v", snapshot)
	}
	first := c.typingUntil
	if err := c.receive(context.Background(), wire); err != nil || !c.typingUntil.After(first) {
		t.Fatalf("重复通知未重置计时: %v", err)
	}
	snapshot.Typing[0].UserID = "外部修改"
	if c.Snapshot().Typing[0].UserID != "sender" {
		t.Fatal("输入快照暴露可变切片")
	}
	c.typingUntil = time.Now().Add(-time.Second)
	c.publishSnapshot()
	if len(c.Snapshot().Typing) != 0 {
		t.Fatal("过期输入状态仍可见")
	}
	c.peer.status.State = PeerAwaitingNode
	if err := c.receive(context.Background(), wire); err != nil {
		t.Fatal(err)
	}
	if len(c.Snapshot().Typing) != 0 {
		t.Fatal("未就绪通知污染状态")
	}
}
