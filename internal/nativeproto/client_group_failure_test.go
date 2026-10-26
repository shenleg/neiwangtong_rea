// 本文件验证群聊提交前持久化失败、提交后未知及帧预算；只使用合成群与loopback传输。
package nativeproto

import (
	"context"
	"errors"
	"testing"
	"time"
)

// TestGroupRecordFailurePreventsSubmit 验证3011同样先保存意图，记录失败不Queue且不分配第二条补发。
func TestGroupRecordFailurePreventsSubmit(t *testing.T) {
	transport, _, _ := udpTestPair(t)
	peer := newSyntheticPeer(t)
	peer.groupMessageVersions[0] = "2:1"
	chat, result, err := prepareChat(ChatRequest{GroupID: "synthetic-group", Text: "synthetic"}, 32768)
	if err != nil {
		t.Fatal(err)
	}
	recordErr := errors.New("synthetic group record failure")
	client := &Client{peer: peer, transport: transport, origin: time.Now(), chats: map[string]*ChatResult{}, observe: func(ClientEvent) error { return recordErr }}
	err = client.sendPreparedChat(context.Background(), chat, result)
	if !errors.Is(err, recordErr) || result.Submitted || result.MessageID == "" || transport.sendSubmissions != 0 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

// TestGroupSubmissionFailurePreservesID 验证进入Queue后未获确认时保留3011未知结果，传输不接受重提。
func TestGroupSubmissionFailurePreservesID(t *testing.T) {
	transport, _, _ := udpTestPair(t)
	peer := newSyntheticPeer(t)
	peer.groupMessageVersions[0] = "2:1"
	chat, result, err := prepareChat(ChatRequest{GroupID: "synthetic-group", Text: "synthetic"}, 32768)
	if err != nil {
		t.Fatal(err)
	}
	id := result.MessageID
	client := &Client{peer: peer, transport: transport, origin: time.Now(), chats: map[string]*ChatResult{}, observe: func(ClientEvent) error { return nil }}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if err := client.sendPreparedChat(ctx, chat, result); err == nil || !result.Submitted || result.TransportAcknowledged || result.MessageID != id || result.Delivery != "unknown" {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	before := transport.sendSubmissions
	if err := transport.Send(context.Background(), chat.wire); err == nil || transport.sendSubmissions != before {
		t.Fatal("失败会话重新提交群消息")
	}
}

// TestGroupFrameBudget 验证外层群标识和正文合计超过传输预算时不产生可提交消息。
func TestGroupFrameBudget(t *testing.T) {
	if _, _, err := prepareChat(ChatRequest{GroupID: "synthetic-group", Text: "synthetic"}, 16); err == nil {
		t.Fatal("接受超预算群帧")
	}
}
