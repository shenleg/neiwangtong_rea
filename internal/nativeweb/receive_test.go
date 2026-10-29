// 本文件用合成接收回调验证消息缓冲、认证读取、会话隔离与正文不落盘，不打开协议socket。
package nativeweb

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"neiwangtong-gateway/internal/nativeproto"
)

// incomingCallback取得已启动合成会话的接收回调；未配置时终止测试，不读取实际网络。
func incomingCallback(t *testing.T, fake *fakeSession) func(nativeproto.IncomingMessage) error {
	t.Helper()
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if fake.receive == nil {
		t.Fatal("未向协议会话配置接收回调")
	}
	return fake.receive
}

// TestReceivedMessagesArePrivateAndEphemeral 验证两种消息可经认证读取且不落盘，重新上线保留正文但拒绝旧会话回调。
func TestReceivedMessagesArePrivateAndEphemeral(t *testing.T) {
	manager, fake := testManager(t)
	handler := testHandler(t, manager)
	if err := manager.Start(manager.State().Name); err != nil {
		t.Fatal(err)
	}
	awaitPhase(t, manager, "online")
	receive := incomingCallback(t, fake)
	messages := []nativeproto.IncomingMessage{
		{Command: nativeproto.CommandUserMessage, MessageID: "17", SenderID: "synthetic-peer", SenderName: "对端甲", Text: "入站私聊独有正文\n第二行<&>🙂"},
		{Command: nativeproto.CommandGroupMessage, MessageID: "17", SenderID: "other-peer", SenderName: "对端乙", GroupID: "group-three", Text: "入站群聊独有正文\r\n末行"},
	}
	for _, message := range messages {
		if err := receive(message); err != nil {
			t.Fatal(err)
		}
	}
	response := performRequest(handler, http.MethodGet, "/api/state", "")
	var state State
	if err := json.Unmarshal(response.Body.Bytes(), &state); err != nil {
		t.Fatal(err)
	}
	if response.Code != http.StatusOK || response.Header().Get("Cache-Control") != "no-store" || len(state.Messages) != 2 || state.ReceivedTotal != 2 || state.ReceivedDropped != 0 {
		t.Fatalf("接收状态错误: %d %+v", response.Code, state)
	}
	for i, message := range state.Messages {
		if message.IncomingMessage != messages[i] || message.Sequence != uint64(i+1) || message.ReceivedAt.IsZero() {
			t.Fatalf("接收顺序或内容被修改: %+v", message)
		}
	}
	state.Messages[0].Text = "外部修改"
	if manager.State().Messages[0].Text != messages[0].Text {
		t.Fatal("接收快照未复制")
	}
	unauthorized := performRequest(handler.(*authenticatedHandler).handler, http.MethodGet, "/api/state", "")
	if unauthorized.Code != http.StatusUnauthorized || strings.Contains(unauthorized.Body.String(), "入站") {
		t.Fatal("未认证请求读取了聊天正文")
	}
	for _, path := range []string{"/health", "/contacts", "/groups", "/api/contacts", "/api/groups"} {
		response := performRequest(handler, http.MethodGet, path, "")
		if strings.Contains(response.Body.String(), "入站") {
			t.Fatalf("%s不应包含聊天正文", path)
		}
	}
	manager.Stop()
	awaitPhase(t, manager, "offline")
	files, err := filepath.Glob(filepath.Join(manager.config.DataDir, "session-*.jsonl"))
	if err != nil || len(files) != 1 {
		t.Fatalf("运行记录错误: %v %v", files, err)
	}
	data, err := os.ReadFile(files[0])
	if err != nil {
		t.Fatal(err)
	}
	for _, private := range []string{"入站私聊独有正文", "入站群聊独有正文", "对端甲", "对端乙"} {
		if strings.Contains(string(data), private) {
			t.Fatalf("运行日志泄露接收数据: %s", private)
		}
	}
	if err := manager.Start(manager.State().Name); err != nil {
		t.Fatal(err)
	}
	state = awaitPhase(t, manager, "online")
	if len(state.Messages) != 2 || state.ReceivedTotal != 2 || state.ReceivedDropped != 0 {
		t.Fatal("重新上线不应丢失本进程内接收内容")
	}
	if err := receive(messages[0]); err == nil || len(manager.State().Messages) != 2 {
		t.Fatal("旧会话回调污染了新会话")
	}
}

// TestReceivedMessagesBoundedByCountAndBytes 验证条数和字节预算均淘汰最旧消息，序号不回绕且公开丢弃计数。
func TestReceivedMessagesBoundedByCountAndBytes(t *testing.T) {
	for _, large := range []bool{false, true} {
		t.Run(fmt.Sprint(large), func(t *testing.T) {
			manager, fake := testManager(t)
			if err := manager.Start(manager.State().Name); err != nil {
				t.Fatal(err)
			}
			awaitPhase(t, manager, "online")
			receive := incomingCallback(t, fake)
			count, retained, text := maxReceivedMessages+3, maxReceivedMessages, "短消息"
			if large {
				text = strings.Repeat("a", 64<<10)
				retained = maxReceivedBytes / len(text)
				count = retained + 3
			}
			for i := 0; i < count; i++ {
				if err := receive(nativeproto.IncomingMessage{Command: nativeproto.CommandUserMessage, MessageID: fmt.Sprint(i + 1), SenderID: "synthetic-peer", Text: text}); err != nil {
					t.Fatal(err)
				}
			}
			state := manager.State()
			if len(state.Messages) != retained || state.ReceivedTotal != uint64(count) || state.ReceivedDropped != uint64(count-retained) || state.Messages[0].Sequence != 4 || state.Messages[retained-1].Sequence != uint64(count) {
				t.Fatalf("缓冲预算或淘汰顺序错误: 条数%d 总计%d 丢弃%d", len(state.Messages), state.ReceivedTotal, state.ReceivedDropped)
			}
			if err := manager.Close(context.Background()); err != nil {
				t.Fatal(err)
			}
			if len(manager.State().Messages) != 0 {
				t.Fatal("关闭管理器后仍保留正文")
			}
		})
	}
}

// TestConcurrentReceivedMessages 验证多对端并发接收时序号唯一且有序，轮询不会移除或重复插入消息。
func TestConcurrentReceivedMessages(t *testing.T) {
	manager, fake := testManager(t)
	if err := manager.Start(manager.State().Name); err != nil {
		t.Fatal(err)
	}
	awaitPhase(t, manager, "online")
	receive := incomingCallback(t, fake)
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if err := receive(nativeproto.IncomingMessage{Command: nativeproto.CommandUserMessage, MessageID: fmt.Sprint(i), SenderID: "synthetic-peer", Text: "并发接收"}); err != nil {
				t.Error(err)
			}
		}(i)
	}
	wg.Wait()
	for range 2 {
		state := manager.State()
		if len(state.Messages) != 100 || state.ReceivedTotal != 100 {
			t.Fatal("并发接收丢失或轮询重复插入消息")
		}
		for i, message := range state.Messages {
			if message.Sequence != uint64(i+1) {
				t.Fatal("并发接收序号不连续")
			}
		}
	}
}
