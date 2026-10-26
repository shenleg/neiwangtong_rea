// 本文件以合成身份和loopback对端验证一次聊天提交、显式目标匹配以及传输确认不等于送达。
package nativeproto

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"
)

// newChatTestClient 以测试socket构造一次限时聊天客户端，返回本机实例与模拟端节点；初始化错误终止测试。
func newChatTestClient(t *testing.T, peerDiscovery, peerConn *net.UDPConn, active bool, request ChatRequest) (*Client, NodePacket) {
	t.Helper()
	discovery, conn := udpTestSocket(t), udpTestSocket(t)
	localOrder, remoteOrder := byte(1), byte(2)
	if active {
		localOrder, remoteOrder = 2, 1
	}
	local := testClientNode(t, discovery, conn, "synthetic-client", localOrder)
	remote := testClientNode(t, peerDiscovery, peerConn, "synthetic-peer", remoteOrder)
	profile, err := ProbeHandshake("Synthetic chat node")
	if err != nil {
		t.Fatal(err)
	}
	directory, err := NewDirectory(local.UserID, []Contact{{UserID: request.UserID, Name: "Synthetic peer", Discovery: peerDiscovery.LocalAddr().(*net.UDPAddr).AddrPort()}})
	if err != nil {
		t.Fatal(err)
	}
	client, err := NewClient(discovery, conn, ClientConfig{Node: local, Profile: profile,
		RemoteDiscovery: peerDiscovery.LocalAddr().(*net.UDPAddr).AddrPort(), Transport: runtimeConfig(0, 1),
		Duration: 600 * time.Millisecond, ShutdownTimeout: time.Second, DiscoveryTimeout: time.Second, HandshakeTimeout: time.Second, Contacts: directory, Chat: &request})
	if err != nil {
		t.Fatal(err)
	}
	return client, remote
}

// TestClientChatExactlyOnce 验证主动/被动单聊均只在双向READY后提交一次，业务回执也不会变成独立接收成功。
func TestClientChatExactlyOnce(t *testing.T) {
	for _, active := range []bool{true, false} {
		for _, ackCommand := range []uint32{0, CommandMessageAck, CommandMessageReceipt} {
			t.Run(fmt.Sprintf("active=%v/ack=%d", active, ackCommand), func(t *testing.T) {
				peerDiscovery, peerConn := udpTestSocket(t), udpTestSocket(t)
				request := ChatRequest{UserID: "synthetic-peer", Text: "合成消息<&>\n第二行🙂"}
				client, remote := newChatTestClient(t, peerDiscovery, peerConn, active, request)
				ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
				defer cancel()
				count := 0
				done := make(chan error, 1)
				go func() {
					done <- serveSyntheticNativeWithHandler(ctx, peerDiscovery, peerConn, remote, active, func(frame Frame) (*Frame, error) {
						count++
						if frame != client.chat.frame {
							return nil, fmt.Errorf("unexpected chat frame: %d", frame.Command)
						}
						if ackCommand == 0 {
							return nil, nil
						}
						message, err := DecodeUserMessage(frame)
						if err != nil {
							return nil, err
						}
						ack, err := EncodeMessageAck(ackCommand, MessageAck{MessageID: message.MessageID})
						return &ack, err
					})
				}()
				planned := 0
				result, err := client.Run(ctx, func(event ClientEvent) error {
					if event.Stage == "chat_send_planned" {
						planned++
						if event.State != PeerOnline || event.MessageID != client.result.Chat.MessageID {
							return fmt.Errorf("chat planned before readiness or without message ID")
						}
					}
					return nil
				})
				if err != nil {
					t.Fatal(err)
				}
				if err := <-done; err != nil {
					t.Fatal(err)
				}
				if count != 1 || planned != 1 || !result.ProtocolReady || !result.GracefulExit || !result.Chat.Submitted || !result.Chat.TransportAcknowledged || result.Chat.BusinessAcknowledged != (ackCommand == CommandMessageAck) || result.Chat.ReceiptReceived != (ackCommand == CommandMessageReceipt) || result.Chat.Delivery != "unknown" {
					t.Fatalf("count=%d planned=%d result=%+v chat=%+v", count, planned, result, result.Chat)
				}
			})
		}
	}
}

// TestClientChatRecordFailurePreventsSubmit 验证记录发送意图失败时不Queue；退出控制仍正常，不能用新请求补发。
func TestClientChatRecordFailurePreventsSubmit(t *testing.T) {
	peerDiscovery, peerConn := udpTestSocket(t), udpTestSocket(t)
	client, remote := newChatTestClient(t, peerDiscovery, peerConn, true, ChatRequest{UserID: "synthetic-peer", Text: "synthetic"})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- serveSyntheticNative(ctx, peerDiscovery, peerConn, remote, true) }()
	recordErr := errors.New("synthetic record failure")
	result, err := client.Run(ctx, func(event ClientEvent) error {
		if event.Stage == "chat_send_planned" {
			return recordErr
		}
		return nil
	})
	if !errors.Is(err, recordErr) || result.Chat.Submitted || result.Chat.TransportAcknowledged || result.Chat.Delivery != "unknown" {
		t.Fatalf("err=%v result=%+v", err, result.Chat)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if _, err := client.Run(ctx, func(ClientEvent) error { return nil }); err == nil {
		t.Fatal("reused failed chat attempt")
	}
}

// TestClientChatWaitsForBothReady 验证本机READY获传输ACK但远端未READY便断开时，聊天仍未提交。
func TestClientChatWaitsForBothReady(t *testing.T) {
	peerDiscovery, peerConn := udpTestSocket(t), udpTestSocket(t)
	client, remote := newChatTestClient(t, peerDiscovery, peerConn, false, ChatRequest{UserID: "synthetic-peer", Text: "synthetic"})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- closeAfterReady(ctx, peerDiscovery, peerConn, remote) }()
	result, err := client.Run(ctx, func(ClientEvent) error { return nil })
	if err == nil || result.ProtocolReady || result.Chat.Submitted || result.Chat.TransportAcknowledged {
		t.Fatalf("err=%v result=%+v chat=%+v", err, result, result.Chat)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if _, ok := client.Contact(); ok {
		t.Fatal("partial handshake exported verified contact")
	}
}

// TestClientChatRejectsDifferentDiscoveredIdentity 验证相同IP上的不同身份不能替代请求指定的目标，不继续建立ENet连接。
func TestClientChatRejectsDifferentDiscoveredIdentity(t *testing.T) {
	peerDiscovery, peerConn := udpTestSocket(t), udpTestSocket(t)
	client, remote := newChatTestClient(t, peerDiscovery, peerConn, true, ChatRequest{UserID: "another-synthetic-peer", Text: "synthetic"})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		if err := peerDiscovery.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
			done <- err
			return
		}
		buffer := make([]byte, 4096)
		_, source, err := peerDiscovery.ReadFromUDPAddrPort(buffer)
		if err != nil {
			done <- err
			return
		}
		remote.Type = 2
		wire, err := remote.Encode()
		if err == nil {
			_, err = peerDiscovery.WriteToUDPAddrPort(wire, source)
		}
		done <- err
	}()
	result, err := client.Run(ctx, func(ClientEvent) error { return nil })
	if err == nil || !strings.Contains(err.Error(), "身份与明确聊天目标不一致") || result.Chat.Submitted || result.ProtocolReady {
		t.Fatalf("err=%v result=%+v", err, result)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if err := peerConn.SetReadDeadline(time.Now().Add(20 * time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	var buffer [4096]byte
	if _, _, err := peerConn.ReadFromUDPAddrPort(buffer[:]); err == nil {
		t.Fatal("identity mismatch still initiated ENet")
	} else if timeout, ok := err.(net.Error); !ok || !timeout.Timeout() {
		t.Fatal(err)
	}
}

// TestClientChatRequiresOwnedList 验证缺列表、其他身份列表、未命中联系人和不一致端点不能创建可发送客户端。
func TestClientChatRequiresOwnedList(t *testing.T) {
	for _, mode := range []string{"missing", "other-owner", "unknown-contact", "wrong-endpoint", "local-version"} {
		t.Run(mode, func(t *testing.T) {
			peerDiscovery, peerConn := udpTestSocket(t), udpTestSocket(t)
			client, _ := newChatTestClient(t, peerDiscovery, peerConn, true, ChatRequest{UserID: "synthetic-peer", Text: "synthetic"})
			config := client.config
			switch mode {
			case "missing":
				config.Contacts = nil
			case "other-owner":
				var err error
				config.Contacts, err = NewDirectory("other-own", config.Contacts.ListContacts())
				if err != nil {
					t.Fatal(err)
				}
			case "unknown-contact":
				config.Chat = &ChatRequest{UserID: "not-in-list", Text: "synthetic"}
			case "wrong-endpoint":
				config.RemoteDiscovery = client.discovery.LocalAddr().(*net.UDPAddr).AddrPort()
			case "local-version":
				for i := range config.Profile.Features {
					if config.Profile.Features[i].Name == "MSG" {
						config.Profile.Features[i].Value = "unknown"
					}
				}
			}
			if _, err := newClient(client.discovery, client.conn, config); err == nil {
				t.Fatal("accepted invalid contact selection")
			}
		})
	}
}

// TestClientChatAckMatching 验证旧编号不影响当前请求，1005/1006分别记录且重复回执不触发新提交。
func TestClientChatAckMatching(t *testing.T) {
	for _, command := range []uint32{CommandMessageAck, CommandMessageReceipt} {
		peer := newSyntheticPeer(t)
		client := &Client{peer: peer, origin: time.Now(), chats: make(map[string]*ChatResult), result: ClientResult{Chat: &ChatResult{Command: CommandUserMessage, MessageID: "42", Submitted: true, Delivery: "unknown"}}, observe: func(ClientEvent) error { return nil }}
		client.trackChat(client.result.Chat)
		for _, id := range []uint64{41, 42, 42} {
			frame, err := EncodeMessageAck(command, MessageAck{MessageID: id})
			if err != nil {
				t.Fatal(err)
			}
			wire, err := Encode(frame.Command, frame.Payload)
			if err != nil {
				t.Fatal(err)
			}
			if err := client.receiveChatAck(wire, frame); err != nil {
				t.Fatal(err)
			}
			if id == 41 && (client.result.Chat.BusinessAcknowledged || client.result.Chat.ReceiptReceived) {
				t.Fatal("unmatched ID changed current request")
			}
		}
		if client.result.Chat.BusinessAcknowledged != (command == CommandMessageAck) || client.result.Chat.ReceiptReceived != (command == CommandMessageReceipt) || client.result.Chat.Delivery != "unknown" {
			t.Fatalf("chat=%+v", client.result.Chat)
		}
	}
}

// TestClientChatSubmissionFailure 区分发送前取消与已Queue后未获ACK，并保留同一个消息编号，不自动重提。
func TestClientChatSubmissionFailure(t *testing.T) {
	for _, queued := range []bool{false, true} {
		transport, _, _ := udpTestPair(t)
		peer := newSyntheticPeer(t)
		peer.messageVersions[0] = "2:1"
		chat, result, err := prepareChat(ChatRequest{UserID: "remote-synthetic", Text: "synthetic"}, 32768)
		if err != nil {
			t.Fatal(err)
		}
		id := result.MessageID
		client := &Client{peer: peer, transport: transport, chat: chat, origin: time.Now(), chats: make(map[string]*ChatResult), result: ClientResult{Chat: result}, observe: func(ClientEvent) error { return nil }}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
		if !queued {
			cancel()
		}
		err = client.sendChat(ctx)
		cancel()
		if err == nil || result.Submitted != queued || result.TransportAcknowledged || result.MessageID != id || result.Delivery != "unknown" {
			t.Fatalf("queued=%v err=%v result=%+v", queued, err, result)
		}
		if queued {
			before := transport.sendSubmissions
			if err := transport.Send(context.Background(), chat.wire); err == nil || transport.sendSubmissions != before {
				t.Fatal("failed transport accepted another submission")
			}
		}
	}
}

// TestChatUnsupportedVersionAndFrameBudget 验证不兼容版本在提交前失败，最终编码帧也受传输预算检查。
func TestChatUnsupportedVersionAndFrameBudget(t *testing.T) {
	chat, result, err := prepareChat(ChatRequest{UserID: "peer", Text: "synthetic"}, 32768)
	if err != nil {
		t.Fatal(err)
	}
	peer := newSyntheticPeer(t)
	peer.messageVersions[0] = "unknown"
	client := &Client{peer: peer, chat: chat, result: ClientResult{Chat: result}}
	if err := client.sendChat(context.Background()); err == nil || result.Submitted {
		t.Fatal("unsupported MSG version submitted chat")
	}
	if _, _, err := prepareChat(ChatRequest{UserID: "peer", Text: strings.Repeat("&", MaxTextBytes)}, 1024); err == nil {
		t.Fatal("encoded message escaped frame budget")
	}
	decoded, err := Decode(bytes.NewReader(chat.wire), 32768)
	if err != nil || decoded != chat.frame {
		t.Fatalf("frame=%+v err=%v", decoded, err)
	}
}

// TestValidateChatRequest 验证无效目标、XML字符、空文本及本地文本预算在建连前明确拒绝。
func TestValidateChatRequest(t *testing.T) {
	for _, request := range []ChatRequest{
		{Text: "synthetic"},
		{UserID: "peer"},
		{UserID: strings.Repeat("x", 65), Text: "synthetic"},
		{UserID: "bad\x00id", Text: "synthetic"},
		{UserID: "peer", Text: strings.Repeat("a", MaxTextBytes+1)},
		{UserID: "peer", Text: "bad\xfftext"},
		{UserID: "peer", Text: "bad\x00text"},
	} {
		if err := ValidateChatRequest(request); err == nil {
			t.Fatalf("accepted invalid request %+v", request)
		}
	}
}
