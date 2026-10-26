// 本文件以loopback单对端验证常驻上线、连续单聊、并发调用串行化、回执关联及主动取消后的正常下线。
package nativeproto

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"strconv"
	"sync"
	"testing"
	"time"
)

type residentRunResult struct {
	result ClientResult
	err    error
}

type observedUserMessage struct {
	id      uint64
	content textDocument
}

// residentFixture 返回Duration=0的合成客户端与独立协议对端；handler只处理就绪后的业务，所有socket均限于loopback。
// 初始化失败终止测试；done与peerDone分别返回客户端及对端退出结果，cancel仅停止客户端，不提前杀死对端的退出ACK。
func residentFixture(t *testing.T, active bool, handler func(Frame) (*Frame, error), observe func(ClientEvent) error, initial ...Frame) (*Client, context.CancelFunc, <-chan residentRunResult, <-chan error) {
	t.Helper()
	discovery, conn := udpTestSocket(t), udpTestSocket(t)
	peerDiscovery, peerConn := udpTestSocket(t), udpTestSocket(t)
	localOrder, peerOrder := byte(1), byte(2)
	if active {
		localOrder, peerOrder = 2, 1
	}
	local := testClientNode(t, discovery, conn, "synthetic-local", localOrder)
	remote := testClientNode(t, peerDiscovery, peerConn, "synthetic-peer", peerOrder)
	directory, err := NewDirectory(local.UserID, []Contact{{UserID: remote.UserID, Name: "Synthetic peer", Discovery: peerDiscovery.LocalAddr().(*net.UDPAddr).AddrPort()}})
	if err != nil {
		t.Fatal(err)
	}
	profile, err := ProbeHandshake("Synthetic resident")
	if err != nil {
		t.Fatal(err)
	}
	if len(initial) > 0 {
		for i := range profile.Features {
			if profile.Features[i].Name == "QGROUP" || profile.Features[i].Name == "QGROUP_MSG" {
				profile.Features[i].Value = "2:1"
			}
		}
	}
	client, err := NewClient(discovery, conn, ClientConfig{Node: local, Profile: profile, RemoteDiscovery: peerDiscovery.LocalAddr().(*net.UDPAddr).AddrPort(),
		Transport: runtimeConfig(0, 1), Contacts: directory, GroupChat: len(initial) > 0, Duration: 0, ShutdownTimeout: time.Second, DiscoveryTimeout: time.Second, HandshakeTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	peerCtx, peerCancel := context.WithTimeout(context.Background(), 5*time.Second)
	ctx, cancel := context.WithCancel(peerCtx)
	t.Cleanup(func() { cancel(); peerCancel() })
	done := make(chan residentRunResult, 1)
	peerDone := make(chan error, 1)
	go func() {
		peerDone <- serveSyntheticNativeWithHandler(peerCtx, peerDiscovery, peerConn, remote, active, handler, initial...)
	}()
	go func() {
		result, err := client.Run(ctx, observe)
		done <- residentRunResult{result, err}
	}()
	return client, cancel, done, peerDone
}

// awaitResidentEvent 等待指定阶段，未到达前收集其他事件；超时或事件错误由调用测试处理，不访问外部网络。
func awaitResidentEvent(t *testing.T, events <-chan ClientEvent, stage, id string) ClientEvent {
	t.Helper()
	deadline := time.NewTimer(2 * time.Second)
	defer deadline.Stop()
	for {
		select {
		case event := <-events:
			if event.Stage == stage && (id == "" || event.MessageID == id) {
				return event
			}
		case <-deadline.C:
			t.Fatalf("missing event %s id=%s", stage, id)
		}
	}
}

// finishResident 停止客户端并等待双方正常退出，返回最终运行结果；退出失败或超时终止当前测试。
func finishResident(t *testing.T, cancel context.CancelFunc, done <-chan residentRunResult, peerDone <-chan error) ClientResult {
	t.Helper()
	cancel()
	select {
	case value := <-done:
		if value.err != nil || !value.result.ProtocolReady || !value.result.GracefulExit {
			t.Fatalf("result=%+v err=%v", value.result, value.err)
		}
		select {
		case err := <-peerDone:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("peer did not close")
		}
		return value.result
	case <-time.After(2 * time.Second):
		t.Fatal("resident client did not stop")
		return ClientResult{}
	}
}

// residentAckHandler 解析合成1004并把完整文本结构发给messages，返回同编号1005；坏命令/正文返回错误。
func residentAckHandler(messages chan<- observedUserMessage) func(Frame) (*Frame, error) {
	return func(frame Frame) (*Frame, error) {
		message, err := DecodeUserMessage(frame)
		if err != nil {
			return nil, err
		}
		var content textDocument
		if err := json.Unmarshal([]byte(message.Content), &content); err != nil {
			return nil, err
		}
		messages <- observedUserMessage{id: message.MessageID, content: content}
		ack, err := EncodeMessageAck(CommandMessageAck, MessageAck{MessageID: message.MessageID})
		return &ack, err
	}
}

// TestResidentContinuousUserMessages 验证两种连接角色均只上线一次，四条消息使用同一连接、递增MSG_ID和独立正文GUID。
func TestResidentContinuousUserMessages(t *testing.T) {
	for _, active := range []bool{true, false} {
		t.Run(fmt.Sprintf("active=%v", active), func(t *testing.T) {
			events := make(chan ClientEvent, 128)
			messages := make(chan observedUserMessage, 4)
			client, cancel, done, peerDone := residentFixture(t, active, residentAckHandler(messages), func(event ClientEvent) error { events <- event; return nil })
			awaitResidentEvent(t, events, "protocol_ready", "")
			ctx, stop := context.WithTimeout(context.Background(), 3*time.Second)
			defer stop()
			if result, err := client.SendUser(ctx, "not-in-list", "synthetic"); err == nil || result.Submitted {
				t.Fatal("unknown recipient was submitted")
			}
			var lastID uint64
			contentIDs := make(map[string]bool)
			for i := 0; i < 4; i++ {
				text := fmt.Sprintf("连续合成消息%d\n第二行🙂", i)
				result, err := client.SendUser(ctx, "synthetic-peer", text)
				if err != nil || !result.Submitted || !result.TransportAcknowledged || result.Delivery != "unknown" {
					t.Fatalf("result=%+v err=%v", result, err)
				}
				id, err := strconv.ParseUint(result.MessageID, 10, 64)
				if err != nil || id <= lastID {
					t.Fatalf("id=%d last=%d err=%v", id, lastID, err)
				}
				lastID = id
				observed := <-messages
				if observed.id != id || len(observed.content.Data) != 1 || observed.content.Data[0].Text.Value != text || contentIDs[observed.content.ID] {
					t.Fatalf("message=%+v result=%+v", observed, result)
				}
				contentIDs[observed.content.ID] = true
				awaitResidentEvent(t, events, "business_acknowledged", result.MessageID)
				if result.BusinessAcknowledged {
					t.Fatal("previously returned result changed after asynchronous ACK")
				}
			}
			result := finishResident(t, cancel, done, peerDone)
			if result.ChatSends != 4 || result.DiscoverySends != 1 || result.DiscoveryAttempts != 1 || !result.Chat.BusinessAcknowledged {
				t.Fatalf("result=%+v chat=%+v", result, result.Chat)
			}
			if _, err := client.SendUser(context.Background(), "synthetic-peer", "after-stop"); err == nil {
				t.Fatal("stopped client accepted another message")
			}
		})
	}
}

// TestResidentConcurrentCallers 验证并发调用只在Run中串行操作同一UDP连接，所有请求编号和正文均不重复。
func TestResidentConcurrentCallers(t *testing.T) {
	const count = 8
	events := make(chan ClientEvent, 128)
	messages := make(chan observedUserMessage, count)
	client, cancel, done, peerDone := residentFixture(t, true, residentAckHandler(messages), func(event ClientEvent) error { events <- event; return nil })
	awaitResidentEvent(t, events, "protocol_ready", "")
	ctx, stop := context.WithTimeout(context.Background(), 3*time.Second)
	defer stop()
	results := make(chan chatReply, count)
	var callers sync.WaitGroup
	for i := 0; i < count; i++ {
		callers.Add(1)
		go func(number int) {
			defer callers.Done()
			result, err := client.SendUser(ctx, "synthetic-peer", fmt.Sprintf("synthetic-%d", number))
			results <- chatReply{result, err}
		}(i)
	}
	callers.Wait()
	close(results)
	ids := make(map[string]bool)
	for response := range results {
		if response.err != nil || !response.result.Submitted || !response.result.TransportAcknowledged || ids[response.result.MessageID] {
			t.Fatalf("response=%+v", response)
		}
		ids[response.result.MessageID] = true
	}
	texts := make(map[string]bool)
	for i := 0; i < count; i++ {
		message := <-messages
		text := message.content.Data[0].Text.Value
		if texts[text] || !ids[strconv.FormatUint(message.id, 10)] {
			t.Fatalf("repeated or wrong message=%+v", message)
		}
		texts[text] = true
	}
	result := finishResident(t, cancel, done, peerDone)
	if result.ChatSends != count || result.DiscoverySends != 1 {
		t.Fatalf("result=%+v", result)
	}
}

// TestResidentNotReadyAndCanceledRequest 验证未上线和已取消请求均不提交；取消单条请求后仍可使用健康连接。
func TestResidentNotReadyAndCanceledRequest(t *testing.T) {
	if _, err := new(Client).SendUser(context.Background(), "peer", "synthetic"); err == nil {
		t.Fatal("idle client accepted message")
	}
	events := make(chan ClientEvent, 64)
	messages := make(chan observedUserMessage, 1)
	client, cancel, done, peerDone := residentFixture(t, false, residentAckHandler(messages), func(event ClientEvent) error { events <- event; return nil })
	awaitResidentEvent(t, events, "protocol_ready", "")
	ctx, stop := context.WithCancel(context.Background())
	stop()
	if result, err := client.SendUser(ctx, "synthetic-peer", "canceled"); err != context.Canceled || result.Submitted || result.MessageID != "" {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if result, err := client.SendUser(context.Background(), "synthetic-peer", "next-valid"); err != nil || !result.TransportAcknowledged {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	<-messages
	result := finishResident(t, cancel, done, peerDone)
	if result.ChatSends != 1 {
		t.Fatalf("chat sends=%d", result.ChatSends)
	}
}

// TestResidentWaitingCallCancellation 验证前一条阻塞记录时，等待中的第二条请求取消不会进入Queue或消耗消息编号。
func TestResidentWaitingCallCancellation(t *testing.T) {
	events := make(chan ClientEvent, 64)
	messages := make(chan observedUserMessage, 1)
	planned := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })
	client, cancel, done, peerDone := residentFixture(t, true, residentAckHandler(messages), func(event ClientEvent) error {
		events <- event
		if event.Stage == "chat_send_planned" {
			close(planned)
			<-release
		}
		return nil
	})
	awaitResidentEvent(t, events, "protocol_ready", "")
	firstDone := make(chan chatReply, 1)
	go func() {
		result, err := client.SendUser(context.Background(), "synthetic-peer", "first-only")
		firstDone <- chatReply{result, err}
	}()
	select {
	case <-planned:
	case <-time.After(2 * time.Second):
		t.Fatal("first request was not dispatched")
	}
	ctx, stop := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer stop()
	result, err := client.SendUser(ctx, "synthetic-peer", "must-not-send")
	if err != context.DeadlineExceeded || result.Submitted || result.MessageID != "" {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	releaseOnce.Do(func() { close(release) })
	select {
	case response := <-firstDone:
		if response.err != nil || !response.result.TransportAcknowledged {
			t.Fatalf("first result=%+v err=%v", response.result, response.err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("first request did not finish")
	}
	<-messages
	final := finishResident(t, cancel, done, peerDone)
	if final.ChatSends != 1 {
		t.Fatalf("chat sends=%d", final.ChatSends)
	}
}

// TestResidentHeartbeatAndTimeout 验证常驻循环在空闲时发送应用心跳，接收超时明确失败且不重新发现或连接。
func TestResidentHeartbeatAndTimeout(t *testing.T) {
	for _, timeout := range []bool{false, true} {
		t.Run(fmt.Sprintf("timeout=%v", timeout), func(t *testing.T) {
			events := make(chan ClientEvent, 64)
			var client *Client
			initialized := make(chan struct{})
			observe := func(event ClientEvent) error {
				if event.Stage == "protocol_ready" {
					<-initialized
					// 回调与网络调度在同一协程，以合成过去时间触发生产计时，不等待真实60/120秒。
					if timeout {
						client.peer.lastInbound = time.Since(client.origin) - 120*time.Second
					} else {
						client.peer.lastOutbound = time.Since(client.origin) - 60*time.Second
					}
				}
				events <- event
				return nil
			}
			var cancel context.CancelFunc
			var done <-chan residentRunResult
			var peerDone <-chan error
			client, cancel, done, peerDone = residentFixture(t, true, nil, observe)
			close(initialized)
			awaitResidentEvent(t, events, "protocol_ready", "")
			if timeout {
				select {
				case value := <-done:
					if value.err == nil || value.result.DiscoverySends != 1 || value.result.ChatSends != 0 {
						t.Fatalf("result=%+v err=%v", value.result, value.err)
					}
				case <-time.After(2 * time.Second):
					t.Fatal("application receive timeout did not stop client")
				}
				if err := <-peerDone; err != nil {
					t.Fatal(err)
				}
			} else {
				event := awaitResidentEvent(t, events, "transport_acknowledged", "")
				if event.Command != CommandHeartbeat {
					t.Fatalf("expected heartbeat, got %+v", event)
				}
				result := finishResident(t, cancel, done, peerDone)
				if result.ChatSends != 0 {
					t.Fatal("idle resident unexpectedly submitted chat")
				}
			}
		})
	}
}

// TestResidentDelayedAckAndBoundedHistory 验证旧消息ACK不会错误确认新消息，常驻内存只保留有限编号，缓存淘汰不改delivery。
func TestResidentDelayedAckAndBoundedHistory(t *testing.T) {
	client := &Client{peer: newSyntheticPeer(t), origin: time.Now(), chats: make(map[string]*ChatResult), observe: func(ClientEvent) error { return nil }}
	first := &ChatResult{Command: CommandUserMessage, MessageID: "1", Submitted: true, Delivery: "unknown"}
	second := &ChatResult{Command: CommandUserMessage, MessageID: "2", Submitted: true, Delivery: "unknown"}
	client.trackChat(first)
	client.trackChat(second)
	client.result.Chat = second
	ack, err := EncodeMessageAck(CommandMessageAck, MessageAck{MessageID: 1})
	if err != nil {
		t.Fatal(err)
	}
	wire, err := Encode(ack.Command, ack.Payload)
	if err != nil {
		t.Fatal(err)
	}
	if err := client.receiveChatAck(wire, ack); err != nil || !first.BusinessAcknowledged || second.BusinessAcknowledged {
		t.Fatalf("first=%+v second=%+v err=%v", first, second, err)
	}
	for id := 3; id <= recentChatLimit+2; id++ {
		client.trackChat(&ChatResult{Command: CommandUserMessage, MessageID: strconv.Itoa(id), Submitted: true, Delivery: "unknown"})
	}
	if len(client.chats) != recentChatLimit || len(client.chatOrder) != recentChatLimit || client.chats["1"] != nil || first.Delivery != "unknown" {
		t.Fatal("unbounded cache or delivery rewritten")
	}
}
