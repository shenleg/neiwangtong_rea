// 本文件验证服务端临时收发记录和已读状态跨页面读取保留，仅使用合成会话，不连接真实客户端。
package nativeweb

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"neiwangtong-gateway/internal/nativeproto"
)

// TestSentMessagesSurvivePageReload 验证重新读取页面状态能恢复完整已发正文、目标和提交事实，且不会再次发送。
func TestSentMessagesSurvivePageReload(t *testing.T) {
	manager, fake := testManager(t)
	handler := testHandler(t, manager)
	if err := manager.Start(manager.State().Name); err != nil {
		t.Fatal(err)
	}
	awaitPhase(t, manager, "online")
	body := `{"request_id":"history-send-001","user_id":"synthetic-peer","text":"刷新后仍应显示的正文\n第二行🙂"}`
	if response := performRequest(handler, http.MethodPost, "/api/messages/user", body); response.Code != http.StatusOK {
		t.Fatal(response.Body.String())
	}
	for range 2 {
		response := performRequest(handler, http.MethodGet, "/api/state", "")
		var state struct {
			SentMessages []struct {
				Text     string          `json:"text"`
				TargetID string          `json:"target_id"`
				Response MessageResponse `json:"response"`
			} `json:"sent_messages"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &state); err != nil {
			t.Fatal(err)
		}
		if len(state.SentMessages) != 1 || state.SentMessages[0].Text != "刷新后仍应显示的正文\n第二行🙂" || state.SentMessages[0].TargetID != "synthetic-peer" || !state.SentMessages[0].Response.Result.Submitted || fake.calls.Load() != 1 {
			t.Fatalf("页面刷新丢失已发记录或重复发送: %+v，调用%d次", state, fake.calls.Load())
		}
	}
}

// TestHistoryLifetimeAndPrivacy 验证收发全文跨重新上线保留、新管理器清空，日志及未认证请求均不包含正文。
func TestHistoryLifetimeAndPrivacy(t *testing.T) {
	manager, fake := testManager(t)
	handler := testHandler(t, manager)
	if err := manager.Start(manager.State().Name); err != nil {
		t.Fatal(err)
	}
	awaitPhase(t, manager, "online")
	text := "仅允许内存保留的已发全文🙂"
	response, status := manager.Send(context.Background(), "private-history-001", nativeproto.ChatRequest{GroupID: "group-two", Text: text})
	if status != http.StatusOK || !response.Result.Submitted {
		t.Fatal(response)
	}
	if err := incomingCallback(t, fake)(nativeproto.IncomingMessage{Command: nativeproto.CommandUserMessage, SenderID: "synthetic-peer", Text: "内存接收全文"}); err != nil {
		t.Fatal(err)
	}
	original := manager.State()
	manager.Stop()
	awaitPhase(t, manager, "offline")
	if err := manager.Start(manager.State().Name); err != nil {
		t.Fatal(err)
	}
	state := awaitPhase(t, manager, "online")
	if len(state.SentMessages) != 1 || state.SentMessages[0].Text != text || len(state.Messages) != 1 || state.HistoryID != original.HistoryID {
		t.Fatal("重新上线丢失本进程收发记录")
	}
	state.SentMessages[0].Text = "外部修改"
	state.SentMessages[0].Response.Result.Delivery = "changed"
	if fresh := manager.State(); fresh.SentMessages[0].Text != text || fresh.SentMessages[0].Response.Result.Delivery != "unknown" {
		t.Fatal("发送快照暴露可变内部数据")
	}
	unauthorized := performRequest(handler.(*authenticatedHandler).handler, http.MethodGet, "/api/state", "")
	if unauthorized.Code != http.StatusUnauthorized || strings.Contains(unauthorized.Body.String(), text) {
		t.Fatal("未认证读取了已发正文")
	}
	if err := manager.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	files, err := filepath.Glob(filepath.Join(manager.config.DataDir, "session-*.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range files {
		data, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(data), text) || strings.Contains(string(data), "内存接收全文") {
			t.Fatal("收发正文写入磁盘")
		}
	}
	restarted, err := New(manager.config)
	if err != nil {
		t.Fatal(err)
	}
	if fresh := restarted.State(); len(fresh.SentMessages) != 0 || len(fresh.Messages) != 0 || fresh.HistoryID == original.HistoryID {
		t.Fatal("进程重启不应恢复聊天正文")
	}
}

// TestReadMarkingValidationAndGroupIsolation 验证已读接口受认证保护、拒绝非法输入，并按会话类型区分同ID的单聊和群聊。
func TestReadMarkingValidationAndGroupIsolation(t *testing.T) {
	manager, fake := testManager(t)
	handler := testHandler(t, manager)
	if err := manager.Start(manager.State().Name); err != nil {
		t.Fatal(err)
	}
	state := awaitPhase(t, manager, "online")
	receive := incomingCallback(t, fake)
	for _, message := range []nativeproto.IncomingMessage{
		{Command: nativeproto.CommandUserMessage, SenderID: "same-id", Text: "同ID单聊"},
		{Command: nativeproto.CommandGroupMessage, GroupID: "same-id", SenderID: "first", Text: "群成员甲"},
		{Command: nativeproto.CommandGroupMessage, GroupID: "same-id", SenderID: "second", Text: "群成员乙"},
	} {
		if err := receive(message); err != nil {
			t.Fatal(err)
		}
	}
	body := fmt.Sprintf(`{"started_at":%q,"sequence":"3"}`, state.StartedAt.Format(time.RFC3339Nano))
	if response := performRequest(handler.(*authenticatedHandler).handler, http.MethodPost, "/api/messages/read", body); response.Code != http.StatusUnauthorized {
		t.Fatal("未认证请求修改了已读状态")
	}
	for _, body := range []string{`{}`, `null`, `{"started_at":"invalid","sequence":"1"}`, fmt.Sprintf(`{"started_at":%q,"sequence":"-1"}`, state.StartedAt.Format(time.RFC3339Nano))} {
		if response := performRequest(handler, http.MethodPost, "/api/messages/read", body); response.Code != http.StatusBadRequest {
			t.Fatalf("非法已读请求: %d %s", response.Code, response.Body.String())
		}
	}
	if response := performRequest(handler, http.MethodPost, "/api/messages/read", body); response.Code != http.StatusNoContent {
		t.Fatal(response.Body.String())
	}
	messages := manager.State().Messages
	if messages[0].Read || !messages[1].Read || !messages[2].Read {
		t.Fatal("群已读标记应覆盖群成员但不能影响同ID单聊")
	}
}

// TestHistorySharesCountBudget 验证接收与发送共用条数预算，重复HTTP请求不会产生重复发送记录。
func TestHistorySharesCountBudget(t *testing.T) {
	manager, fake := testManager(t)
	if err := manager.Start(manager.State().Name); err != nil {
		t.Fatal(err)
	}
	awaitPhase(t, manager, "online")
	request := nativeproto.ChatRequest{UserID: "synthetic-peer", Text: "已发正文"}
	for range 2 {
		if _, code := manager.Send(context.Background(), "history-dedup-001", request); code != http.StatusOK {
			t.Fatal(code)
		}
	}
	if len(manager.State().SentMessages) != 1 || fake.calls.Load() != 1 {
		t.Fatal("重复查询增加了发送记录或调用次数")
	}
	receive := incomingCallback(t, fake)
	for range maxReceivedMessages {
		if err := receive(nativeproto.IncomingMessage{Command: nativeproto.CommandUserMessage, SenderID: "synthetic-peer", Text: "新消息"}); err != nil {
			t.Fatal(err)
		}
	}
	state := manager.State()
	if len(state.SentMessages) != 0 || len(state.Messages) != maxReceivedMessages {
		t.Fatal("收发记录未按共同预算淘汰最旧正文")
	}
}

// TestReadMessagesSurvivePageReload 验证查看单聊只标记该目标已显示的序号，刷新不恢复未读，尚未显示的新消息仍未读。
func TestReadMessagesSurvivePageReload(t *testing.T) {
	manager, fake := testManager(t)
	handler := testHandler(t, manager)
	if err := manager.Start(manager.State().Name); err != nil {
		t.Fatal(err)
	}
	state := awaitPhase(t, manager, "online")
	receive := incomingCallback(t, fake)
	for _, message := range []nativeproto.IncomingMessage{
		{Command: nativeproto.CommandUserMessage, SenderID: "synthetic-peer", Text: "已经查看"},
		{Command: nativeproto.CommandGroupMessage, GroupID: "group-two", SenderID: "synthetic-peer", Text: "尚未查看的群"},
		{Command: nativeproto.CommandUserMessage, SenderID: "synthetic-peer", Text: "请求前到达但尚未显示的新消息"},
	} {
		if err := receive(message); err != nil {
			t.Fatal(err)
		}
	}
	body := fmt.Sprintf(`{"started_at":%q,"sequence":"1"}`, state.StartedAt.Format(time.RFC3339Nano))
	for range 2 {
		if response := performRequest(handler, http.MethodPost, "/api/messages/read", body); response.Code != http.StatusNoContent {
			t.Fatalf("已读请求错误: %d %s", response.Code, response.Body.String())
		}
	}
	response := performRequest(handler, http.MethodGet, "/api/state", "")
	var snapshot struct {
		Messages []struct {
			Read bool `json:"read"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &snapshot); err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Messages) != 3 || !snapshot.Messages[0].Read || snapshot.Messages[1].Read || snapshot.Messages[2].Read {
		t.Fatalf("已读标记丢失或误标其他目标/尚未显示的新消息: %+v", snapshot)
	}
	manager.Stop()
	awaitPhase(t, manager, "offline")
	if err := manager.Start(manager.State().Name); err != nil {
		t.Fatal(err)
	}
	awaitPhase(t, manager, "online")
	if response := performRequest(handler, http.MethodPost, "/api/messages/read", body); response.Code != http.StatusConflict {
		t.Fatalf("旧会话标记应拒绝: %d", response.Code)
	}
	if err := manager.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}
