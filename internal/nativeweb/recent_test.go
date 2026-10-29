// 本文件用合成会话验证最近联系人的收发排序、去重、生命周期及状态副本，不连接真实客户端。
package nativeweb

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"neiwangtong-gateway/internal/nativeproto"
)

// TestRecentConversationsTrackBothDirections 验证网页与公开API发送、单聊及群聊接收均更新最近会话，重复请求不改变排序。
func TestRecentConversationsTrackBothDirections(t *testing.T) {
	manager, fake := testManager(t)
	if len(manager.State().RecentConversations) != 0 {
		t.Fatal("未收发消息时不应产生最近联系人")
	}
	if err := manager.Start(manager.State().Name); err != nil {
		t.Fatal(err)
	}
	awaitPhase(t, manager, "online")
	if len(manager.State().RecentConversations) != 0 {
		t.Fatal("仅学习目录不应产生最近联系人")
	}
	handler := testHandler(t, manager)
	sent := performRequest(handler, http.MethodPost, "/api/messages/user", `{"request_id":"recent-send-user","user_id":"synthetic-peer","text":"单聊临时正文"}`)
	if sent.Code != http.StatusOK {
		t.Fatal(sent.Body.String())
	}
	receive := incomingCallback(t, fake)
	if err := receive(nativeproto.IncomingMessage{Command: nativeproto.CommandUserMessage, SenderID: "other-peer", SenderName: "其他联系人", Text: "收到临时正文"}); err != nil {
		t.Fatal(err)
	}
	if err := receive(nativeproto.IncomingMessage{Command: nativeproto.CommandGroupMessage, SenderID: "other-peer", GroupID: "group-three", Text: "群聊临时正文"}); err != nil {
		t.Fatal(err)
	}
	recent := manager.State().RecentConversations
	if len(recent) != 3 || recent[0].Kind != "group" || recent[0].ID != "group-three" || recent[0].Name != "多成员群" || recent[1].ID != "other-peer" || recent[2].ID != "synthetic-peer" {
		t.Fatalf("最近列表或排序错误: %+v", recent)
	}
	for i, entry := range recent {
		if entry.LastActivityAt.IsZero() || i > 0 && entry.LastActivityAt.After(recent[i-1].LastActivityAt) {
			t.Fatalf("最近收发时间错误: %+v", recent)
		}
	}
	// 查询原请求不是一次新发送，不能把它移到最近列表顶部。
	repeated := performRequest(handler, http.MethodPost, "/api/messages/user", `{"request_id":"recent-send-user","user_id":"synthetic-peer","text":"单聊临时正文"}`)
	if repeated.Code != http.StatusOK || fake.calls.Load() != 1 || manager.State().RecentConversations[0] != recent[0] {
		t.Fatal("重复请求改变了最近列表或重复发送")
	}
	public := performRequest(handler, http.MethodPost, "/send/group", `{"group_id":"group-two","text":"公开群聊正文"}`)
	if public.Code != http.StatusOK || manager.State().RecentConversations[0].ID != "group-two" {
		t.Fatalf("公开接口未更新最近会话: %d %s", public.Code, public.Body.String())
	}
	if err := receive(nativeproto.IncomingMessage{Command: nativeproto.CommandUserMessage, SenderID: "synthetic-peer", SenderName: "合成联系人", Text: "再次接收"}); err != nil {
		t.Fatal(err)
	}
	response := performRequest(handler, http.MethodGet, "/api/state", "")
	var state State
	if err := json.Unmarshal(response.Body.Bytes(), &state); err != nil {
		t.Fatal(err)
	}
	if len(state.RecentConversations) != 4 || state.RecentConversations[0].ID != "synthetic-peer" {
		t.Fatalf("重复目标未置顶或新增了重复项: %+v", state.RecentConversations)
	}
	state.RecentConversations[0].Name = "外部修改"
	if manager.State().RecentConversations[0].Name == "外部修改" {
		t.Fatal("状态泄露最近列表的可变切片")
	}
}

// TestRecentConversationsSurviveSessionsNotProcess 验证最近列表独立于消息缓冲和协议会话，只在新进程管理器中清空且不写入日志。
func TestRecentConversationsSurviveSessionsNotProcess(t *testing.T) {
	manager, fake := testManager(t)
	if err := manager.Start(manager.State().Name); err != nil {
		t.Fatal(err)
	}
	awaitPhase(t, manager, "online")
	receive := incomingCallback(t, fake)
	if err := receive(nativeproto.IncomingMessage{Command: nativeproto.CommandGroupMessage, SenderID: "synthetic-peer", GroupID: "group-three", Text: "仅用于最近会话的群正文"}); err != nil {
		t.Fatal(err)
	}
	for range maxReceivedMessages + 1 {
		if err := receive(nativeproto.IncomingMessage{Command: nativeproto.CommandUserMessage, SenderID: "other-peer", SenderName: "其他联系人", Text: "淘汰旧正文"}); err != nil {
			t.Fatal(err)
		}
	}
	if len(manager.State().RecentConversations) != 2 || manager.State().ReceivedDropped == 0 {
		t.Fatal("正文淘汰不应移除最近联系人")
	}
	manager.Stop()
	state := awaitPhase(t, manager, "offline")
	if len(state.RecentConversations) != 2 {
		t.Fatal("下线丢失最近联系人")
	}
	if err := manager.Start(manager.State().Name); err != nil {
		t.Fatal(err)
	}
	state = awaitPhase(t, manager, "online")
	if len(state.Messages) != maxReceivedMessages || len(state.RecentConversations) != 2 {
		t.Fatal("重新上线应保留正文和最近联系人")
	}
	if err := manager.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	restarted, err := New(manager.config)
	if err != nil {
		t.Fatal(err)
	}
	if len(restarted.State().RecentConversations) != 0 {
		t.Fatal("新进程不应恢复最近联系人")
	}
	paths, err := filepath.Glob(filepath.Join(manager.config.DataDir, "session-*.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(data), "recent_conversations") || strings.Contains(string(data), "仅用于最近会话的群正文") {
			t.Fatal("最近列表或接收正文写入了日志")
		}
	}
}

// TestRecentConversationsUseSubmissionFacts 验证尚未提交的请求不计入最近联系人，已提交但确认失败的请求仍计入。
func TestRecentConversationsUseSubmissionFacts(t *testing.T) {
	manager, fake := testManager(t)
	request := nativeproto.ChatRequest{UserID: "synthetic-peer", Text: "合成正文"}
	manager.Send(context.Background(), "recent-offline-send", request)
	if len(manager.State().RecentConversations) != 0 {
		t.Fatal("离线拒绝不应更新最近联系人")
	}
	fake.sendErr = errors.New("合成传输失败")
	if err := manager.Start(manager.State().Name); err != nil {
		t.Fatal(err)
	}
	awaitPhase(t, manager, "online")
	response, status := manager.Send(context.Background(), "recent-unknown-send", request)
	if status != http.StatusBadGateway || !response.Result.Submitted || len(manager.State().RecentConversations) != 1 {
		t.Fatal("已经提交的未知结果应计入最近联系人")
	}
}

// TestRecentConversationKindsAndNames 验证同ID的群和单聊分别记录，当前名称更新及暂未同步的群不丢失已知名称。
func TestRecentConversationKindsAndNames(t *testing.T) {
	manager, fake := testManager(t)
	if err := manager.Start(manager.State().Name); err != nil {
		t.Fatal(err)
	}
	awaitPhase(t, manager, "online")
	receive := incomingCallback(t, fake)
	for _, message := range []nativeproto.IncomingMessage{
		{Command: nativeproto.CommandUserMessage, SenderID: "group-three", SenderName: "同ID联系人", Text: "单聊"},
		{Command: nativeproto.CommandGroupMessage, SenderID: "synthetic-peer", GroupID: "group-three", Text: "群聊"},
	} {
		if err := receive(message); err != nil {
			t.Fatal(err)
		}
	}
	fake.mu.Lock()
	fake.snapshot.Groups = nil
	fake.mu.Unlock()
	recent := manager.State().RecentConversations
	if len(recent) != 2 || recent[0].Kind != "group" || recent[0].Name != "多成员群" || recent[1].Kind != "user" || recent[1].Name != "同ID联系人" {
		t.Fatalf("同ID类型混淆或名称丢失: %+v", recent)
	}
}
