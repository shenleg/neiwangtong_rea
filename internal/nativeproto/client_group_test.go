// 本文件用合成成员与loopback验证3011连续提交、整群限制和单聊回执隔离；不访问真实群。
package nativeproto

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
)

// TestSelectGroupRequiresAllMembers 验证未同步、缺本机/对端或有其他成员时均拒绝整群发送。
func TestSelectGroupRequiresAllMembers(t *testing.T) {
	for _, members := range [][]string{nil, {"local"}, {"remote"}, {"local", "remote", "other"}} {
		client := &Client{config: ClientConfig{Node: NodePacket{UserID: "local"}}, remoteNode: NodePacket{UserID: "remote"}, groups: map[string]GroupInfo{"g": {GroupID: "g", Members: members}}}
		if err := client.selectGroup("g"); err == nil {
			t.Fatalf("接受不完整群: %v", members)
		}
		if err := client.selectGroup("missing"); err == nil {
			t.Fatal("接受未知群")
		}
	}
	client := &Client{config: ClientConfig{Node: NodePacket{UserID: "local"}}, remoteNode: NodePacket{UserID: "remote"}, groups: map[string]GroupInfo{"g": {GroupID: "g", Members: []string{"local", "remote"}}}}
	if err := client.selectGroup("g"); err != nil {
		t.Fatal(err)
	}
}

// TestResidentContinuousGroupMessages 验证两种连接角色下同一群连续两次提交使用独立ID/GUID，1005不能确认3011。
func TestResidentContinuousGroupMessages(t *testing.T) {
	for _, active := range []bool{false, true} {
		t.Run(fmt.Sprint(active), func(t *testing.T) {
			events := make(chan ClientEvent, 128)
			messages := make(chan GroupMessage, 2)
			release := make(chan struct{})
			client, cancel, done, peerDone := residentFixture(t, active, func(frame Frame) (*Frame, error) {
				message, err := DecodeGroupMessage(frame)
				if err != nil {
					return nil, err
				}
				messages <- message
				// 故意使用同MSG_ID的单聊ACK，必须只记录unmatched，不能更新群确认。
				ack, err := EncodeMessageAck(CommandMessageAck, MessageAck{MessageID: message.MessageID})
				return &ack, err
			}, func(event ClientEvent) error {
				events <- event
				if event.Stage == "protocol_ready" {
					<-release
				}
				return nil
			})
			awaitResidentEvent(t, events, "protocol_ready", "")
			// Run此时停在回调中；close后才恢复网络与请求，避免fixture并发写成员表。
			client.config.GroupChat = true
			client.groups["synthetic-group"] = GroupInfo{GroupID: "synthetic-group", Name: "Synthetic", Members: []string{client.config.Node.UserID, "synthetic-peer"}}
			close(release)
			if result, err := client.SendGroup(context.Background(), "missing", "not sent"); err == nil || result.Submitted {
				t.Fatal("未知群被提交")
			}
			var last uint64
			guids := map[string]bool{}
			for i := 0; i < 2; i++ {
				text := fmt.Sprintf("合成群消息%d\n第二行🙂", i)
				result, err := client.SendGroup(context.Background(), "synthetic-group", text)
				if err != nil || !result.Submitted || !result.TransportAcknowledged || result.Command != 3011 || result.Delivery != "unknown" {
					t.Fatalf("result=%+v err=%v", result, err)
				}
				message := <-messages
				var content textDocument
				if err := json.Unmarshal([]byte(message.Content), &content); err != nil {
					t.Fatal(err)
				}
				if message.GroupID != "synthetic-group" || message.MessageID <= last || guids[content.ID] || content.Data[0].Text.Value != text {
					t.Fatalf("message=%+v content=%+v", message, content)
				}
				last = message.MessageID
				guids[content.ID] = true
				awaitResidentEvent(t, events, "unmatched_chat_ack", result.MessageID)
			}
			result := finishResident(t, cancel, done, peerDone)
			if result.ChatSends != 2 || result.DiscoverySends != 1 || result.Chat.BusinessAcknowledged {
				t.Fatalf("result=%+v chat=%+v", result, result.Chat)
			}
		})
	}
}

// TestGroupRequestValidation 验证群目标与用户目标互斥、取消/未就绪不生成发送请求。
func TestGroupRequestValidation(t *testing.T) {
	for _, request := range []ChatRequest{{Text: "x"}, {UserID: "u", GroupID: "g", Text: "x"}, {GroupID: "g", Text: ""}} {
		if err := ValidateChatRequest(request); err == nil {
			t.Fatalf("接受非法请求: %+v", request)
		}
	}
	if _, err := new(Client).SendGroup(context.Background(), "g", "x"); err == nil {
		t.Fatal("未就绪接受群发送")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if result, err := new(Client).SendGroup(ctx, "g", "x"); err != context.Canceled || result.Submitted || result.Command != 3011 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}
