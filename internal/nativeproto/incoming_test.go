// 本文件使用合成帧验证入站纯文本、成员资格、去重和隐私，不连接真实网络。
package nativeproto

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

// incomingTestClient 构造已就绪的合成会话及事件记录；不产生网络操作。
func incomingTestClient(t *testing.T, callback func(IncomingMessage) error) (*Client, *[]ClientEvent) {
	t.Helper()
	peer, err := NewPeer("self", "sender", 1024*1024)
	if err != nil {
		t.Fatal(err)
	}
	peer.status.State = PeerOnline
	peer.status.LocalReady, peer.status.RemoteReady = true, true
	peer.messageVersions = [2]string{"1", "1"}
	peer.groupMessageVersions = [2]string{"1", "1"}
	events := []ClientEvent{}
	c := &Client{config: ClientConfig{Node: NodePacket{UserID: "self"}, GroupChat: true, ReceiveMessage: callback, Transport: ENetConfig{MaxMessageBytes: 1024 * 1024}}, peer: peer, remoteNode: NodePacket{UserID: "sender"}, contact: &Contact{Name: "合成发送者"}, origin: time.Now(), incoming: &incomingCache{}, groups: map[string]GroupInfo{}, result: ClientResult{UnhandledCommands: map[uint32]int{}}}
	c.observe = func(event ClientEvent) error { events = append(events, event); return nil }
	c.publishSnapshot()
	return c, &events
}

// incomingWire 编码合成聊天帧，失败时终止测试。
func incomingWire(t *testing.T, command uint32, id uint64, content string) []byte {
	t.Helper()
	var frame Frame
	var err error
	if command == CommandUserMessage {
		frame, err = EncodeUserMessage(UserMessage{MessageID: id, Content: content})
	} else {
		frame, err = EncodeGroupMessage(GroupMessage{GroupID: "group", MessageID: id, Content: content})
	}
	if err != nil {
		t.Fatal(err)
	}
	wire, err := Encode(frame.Command, frame.Payload)
	if err != nil {
		t.Fatal(err)
	}
	return wire
}

// TestIncomingMessages 验证纯文本和链接按序拼接、实体不二次解码，重复与编号冲突按内容区分。
func TestIncomingMessages(t *testing.T) {
	var got []IncomingMessage
	c, events := incomingTestClient(t, func(m IncomingMessage) error { got = append(got, m); return nil })
	c.groups["group"] = GroupInfo{GroupID: "group", Members: []string{"self", "sender", "other"}}
	content := `{"type":"0","dt":[{"txt":{"t":"normal","v":"字面 &amp;\n"}},{"lnk":{"v":"https://example.test"}}]}`
	wire := incomingWire(t, CommandUserMessage, 42, content)
	for _, w := range [][]byte{wire, wire, incomingWire(t, CommandUserMessage, 42, `{"type":"0","dt":[{"txt":{"t":"normal","v":"不同"}}]}`), incomingWire(t, CommandGroupMessage, 42, content)} {
		if err := c.receive(context.Background(), w); err != nil {
			t.Fatal(err)
		}
	}
	if len(got) != 3 || got[0].Text != "字面 &amp;\nhttps://example.test" || got[0].SenderID != "sender" || got[0].SenderName != "合成发送者" || got[2].GroupID != "group" {
		t.Fatalf("got=%+v", got)
	}
	if len(*events) != 3 || len(c.incomingAcks) != 3 {
		t.Fatalf("接收事件或单聊确认数错误: events=%+v ACK=%v", *events, c.incomingAcks)
	}
	for _, event := range *events {
		if event.Stage != "message_received" || strings.Contains(fmt.Sprint(event), "字面") {
			t.Fatalf("event=%+v", event)
		}
	}
	if c.peer.lastInbound == 0 {
		t.Fatal("没有更新入站活动时间")
	}
}

// TestIncomingDistinctContentGUID 验证对端重启后重复使用编号与文字时，不把新正文GUID误判为旧消息。
func TestIncomingDistinctContentGUID(t *testing.T) {
	calls := 0
	c, _ := incomingTestClient(t, func(IncomingMessage) error { calls++; return nil })
	for i := 0; i < 2; i++ {
		content, err := EncodeTextContent("相同的合成文字")
		if err != nil {
			t.Fatal(err)
		}
		wire := incomingWire(t, CommandUserMessage, 1, content)
		for range 2 {
			if err := c.receive(context.Background(), wire); err != nil {
				t.Fatal(err)
			}
		}
	}
	if calls != 2 || len(c.incomingAcks) != 4 {
		t.Fatalf("新消息或重复确认数量错误: 接收%d ACK%d", calls, len(c.incomingAcks))
	}
}

// TestIncomingRejected 验证不完整群成员、能力不兼容、非文本及过长正文不会进入回调，也不会暴露正文。
func TestIncomingRejected(t *testing.T) {
	calls := 0
	c, events := incomingTestClient(t, func(IncomingMessage) error { calls++; return nil })
	valid := `{"type":"0","dt":[{"txt":{"t":"normal","v":"secret-body"}}]}`
	group := incomingWire(t, CommandGroupMessage, 1, valid)
	for _, members := range [][]string{nil, {"self"}, {"sender"}} {
		c.groups["group"] = GroupInfo{Members: members}
		if err := c.receive(context.Background(), group); err != nil {
			t.Fatal(err)
		}
	}
	c.peer.messageVersions[0] = "2"
	if err := c.receive(context.Background(), incomingWire(t, CommandUserMessage, 2, valid)); err != nil {
		t.Fatal(err)
	}
	c.peer.messageVersions[0] = "1"
	for _, content := range []string{`{"type":"0","dt":[{"img":{"v":"secret-body"}}]}`, `{"type":"0","dt":[{"txt":{"t":"normal","v":"` + strings.Repeat("x", maxIncomingTextBytes+1) + `"}}]}`} {
		if err := c.receive(context.Background(), incomingWire(t, CommandUserMessage, 3, content)); err != nil {
			t.Fatal(err)
		}
	}
	if calls != 0 || len(*events) != 6 || len(c.incomingAcks) != 0 {
		t.Fatalf("calls=%d events=%+v ACK=%v", calls, *events, c.incomingAcks)
	}
	for _, event := range *events {
		if event.Stage != "incoming_message_rejected" || strings.Contains(fmt.Sprint(event), "secret-body") {
			t.Fatalf("event=%+v", event)
		}
	}
}

// TestIncomingHostSnapshotAndIdentity 验证Host共享其他peer的完整快照，并按发送者身份区分相同消息编号。
func TestIncomingHostSnapshotAndIdentity(t *testing.T) {
	var got []IncomingMessage
	c, _ := incomingTestClient(t, func(m IncomingMessage) error { got = append(got, m); return nil })
	h := &Host{config: HostConfig{Client: ClientConfig{Node: NodePacket{UserID: "self"}}}, groups: map[string]hostGroup{"group": {info: GroupInfo{Members: []string{"self", "sender", "another"}}}}}
	c.incomingGroup = h.incomingMember
	wire := incomingWire(t, CommandGroupMessage, 1, `{"type":"0","dt":[{"txt":{"t":"normal","v":"ok"}}]}`)
	if err := c.receive(context.Background(), wire); err != nil {
		t.Fatal(err)
	}
	c.remoteNode.UserID, c.peer.remoteID = "another", "another"
	if err := c.receive(context.Background(), wire); err != nil {
		t.Fatal(err)
	}
	h.groups["group"] = hostGroup{invalid: true}
	if err := c.receive(context.Background(), wire); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].SenderID == got[1].SenderID {
		t.Fatalf("got=%+v", got)
	}
}

// TestIncomingCallbackFailureAndDisabled 验证回调错误原文不泄露，未启用时保留原有未处理行为。
func TestIncomingCallbackFailureAndDisabled(t *testing.T) {
	c, events := incomingTestClient(t, func(IncomingMessage) error { return errors.New("secret-callback-error") })
	wire := incomingWire(t, CommandUserMessage, 1, `{"type":"0","dt":[{"txt":{"t":"normal","v":"body"}}]}`)
	if err := c.receive(context.Background(), wire); err == nil || strings.Contains(err.Error(), "secret") || len(c.incomingAcks) != 0 {
		t.Fatalf("err=%v ACK=%v", err, c.incomingAcks)
	}
	c.config.ReceiveMessage = nil
	if err := c.receive(context.Background(), wire); err != nil {
		t.Fatal(err)
	}
	if c.result.UnhandledCommands[CommandUserMessage] != 1 || len(*events) != 1 || (*events)[0].Stage != "unsupported_business_command" {
		t.Fatalf("events=%+v", *events)
	}
}

// TestIncomingDrainAfterSendFailure 验证传输已缓存的入站聊天在发送失败后仍被处理。
func TestIncomingDrainAfterSendFailure(t *testing.T) {
	calls := 0
	c, _ := incomingTestClient(t, func(IncomingMessage) error { calls++; return nil })
	wire := incomingWire(t, CommandUserMessage, 1, `{"type":"0","dt":[{"txt":{"t":"normal","v":"ok"}}]}`)
	c.transport = &UDPTransport{messages: [][]byte{wire}, queuedBytes: uint64(len(wire)), failed: errors.New("send failed")}
	if err := c.drainIncoming(); err != nil || calls != 1 || len(c.transport.messages) != 0 || c.transport.queuedBytes != 0 {
		t.Fatalf("calls=%d err=%v", calls, err)
	}
}

// TestIncomingLoopbackRoles 验证主动和被动角色均在READY后接收发送期间的入站消息。
func TestIncomingLoopbackRoles(t *testing.T) {
	for _, active := range []bool{true, false} {
		t.Run(fmt.Sprint(active), func(t *testing.T) {
			discovery, conn := udpTestSocket(t), udpTestSocket(t)
			client, remote := newChatTestClient(t, discovery, conn, active, ChatRequest{UserID: "synthetic-peer", Text: "outbound"})
			received := make(chan IncomingMessage, 1)
			client.config.ReceiveMessage = func(m IncomingMessage) error { received <- m; return nil }
			content, err := EncodeTextContent("loopback入站")
			if err != nil {
				t.Fatal(err)
			}
			frame, err := EncodeUserMessage(UserMessage{MessageID: 77, Content: content})
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			done := make(chan error, 1)
			acks := make(chan MessageAck, 4)
			go func() {
				done <- serveSyntheticNativeWithHandler(ctx, discovery, conn, remote, active, func(frame Frame) (*Frame, error) {
					if frame.Command == CommandMessageAck {
						ack, err := DecodeMessageAck(frame)
						if err != nil {
							return nil, err
						}
						acks <- ack
					}
					return nil, nil
				}, frame, frame)
			}()
			if _, err := client.Run(ctx, func(ClientEvent) error { return nil }); err != nil {
				t.Fatal(err)
			}
			if err := <-done; err != nil {
				t.Fatal(err)
			}
			select {
			case m := <-received:
				if m.Text != "loopback入站" || m.MessageID != "77" {
					t.Fatalf("message=%+v", m)
				}
			default:
				t.Fatal("未接收消息")
			}
			for range 2 {
				select {
				case ack := <-acks:
					if ack.MessageID != 77 {
						t.Fatalf("单聊确认编号错误: %+v", ack)
					}
				default:
					t.Fatal("接受的在线单聊及其重复提交均须返回1005")
				}
			}
		})
	}
}

// TestIncomingReadinessAndBoundedCache 验证未READY不回调，去重容量固定且淘汰后可再次接收。
func TestIncomingReadinessAndBoundedCache(t *testing.T) {
	calls := 0
	c, events := incomingTestClient(t, func(IncomingMessage) error { calls++; return nil })
	content := `{"type":"0","dt":[{"txt":{"t":"normal","v":"ok"}}]}`
	wire := incomingWire(t, CommandUserMessage, 1, content)
	c.peer.status.State = PeerReadying
	if err := c.receive(context.Background(), wire); err != nil {
		t.Fatal(err)
	}
	if calls != 0 || len(*events) != 1 || (*events)[0].Stage != "incoming_message_rejected" {
		t.Fatal("未就绪消息未拒绝")
	}
	c.peer.status.State = PeerOnline
	for i := 1; i <= incomingLimit+1; i++ {
		if err := c.receive(context.Background(), incomingWire(t, CommandUserMessage, uint64(i), content)); err != nil {
			t.Fatal(err)
		}
	}
	if len(c.incoming.seen) != incomingLimit {
		t.Fatalf("size=%d", len(c.incoming.seen))
	}
	if err := c.receive(context.Background(), wire); err != nil {
		t.Fatal(err)
	}
	if calls != incomingLimit+2 {
		t.Fatalf("calls=%d", calls)
	}
}

// TestIncomingDrainPreservesGroupOrder 验证发送失败时先应用缓存的3003，再判断后续3011成员资格。
func TestIncomingDrainPreservesGroupOrder(t *testing.T) {
	var got []IncomingMessage
	c, _ := incomingTestClient(t, func(m IncomingMessage) error { got = append(got, m); return nil })
	push := syntheticGroupPush(3003, "self", "sender")
	first, err := Encode(push.Command, push.Payload)
	if err != nil {
		t.Fatal(err)
	}
	content, err := EncodeTextContent("ordered")
	if err != nil {
		t.Fatal(err)
	}
	frame, err := EncodeGroupMessage(GroupMessage{GroupID: "synthetic-group", MessageID: 2, Content: content})
	if err != nil {
		t.Fatal(err)
	}
	second, err := Encode(frame.Command, frame.Payload)
	if err != nil {
		t.Fatal(err)
	}
	c.transport = &UDPTransport{messages: [][]byte{first, second}, queuedBytes: uint64(len(first) + len(second)), failed: errors.New("failed")}
	if err := c.drainIncoming(); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Text != "ordered" {
		t.Fatalf("messages=%+v", got)
	}
}
