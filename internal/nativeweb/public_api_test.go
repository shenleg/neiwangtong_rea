// 本文件以合成后台验证远端main风格公开接口、精确名称解析和原生发送结果；不使用真实对端。
package nativeweb

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"neiwangtong-gateway/internal/nativeproto"
)

// startPublicManager 启动合成管理器并返回同源HTTP处理器，初始化失败终止；不绑定网络socket。
func startPublicManager(t *testing.T) (*Manager, *fakeSession, http.Handler) {
	t.Helper()
	manager, fake := testManager(t)
	if err := manager.Start(manager.State().Name); err != nil {
		t.Fatal(err)
	}
	awaitPhase(t, manager, "online")
	return manager, fake, testHandler(t, manager)
}

// gatedRestartSession 在合成会话取消后等待测试释放，模拟旧会话清理；err模拟清理失败。
type gatedRestartSession struct {
	*fakeSession
	stopping chan struct{}
	release  chan struct{}
	err      error
}

// Run 发布就绪事件并在取消后等待清理许可，返回预设清理错误，不访问网络。
func (s *gatedRestartSession) Run(ctx context.Context, observe func(nativeproto.ClientEvent) error) (nativeproto.ClientResult, error) {
	if err := observe(nativeproto.ClientEvent{Stage: "protocol_ready"}); err != nil {
		return nativeproto.ClientResult{}, err
	}
	<-ctx.Done()
	close(s.stopping)
	<-s.release
	return nativeproto.ClientResult{}, s.err
}

// gatedSendSession 模拟已提交但尚未完成的发送，释放后返回未知投递结果。
type gatedSendSession struct {
	*fakeSession
	sending chan struct{}
	release chan struct{}
}

// SendUser 等待测试释放发送，返回已提交但结果未知；不访问网络。
func (s *gatedSendSession) SendUser(_ context.Context, _, _ string) (nativeproto.ChatResult, error) {
	close(s.sending)
	<-s.release
	return nativeproto.ChatResult{Command: nativeproto.CommandUserMessage, MessageID: "synthetic", Submitted: true, Delivery: "unknown"}, errors.New("synthetic interrupted send")
}

// TestRestartWaitsForAcceptedSend 验证重新上线等待已受理发送的最终记录，不自动补发。
func TestRestartWaitsForAcceptedSend(t *testing.T) {
	manager, fake := testManager(t)
	first := &gatedSendSession{fakeSession: fake, sending: make(chan struct{}), release: make(chan struct{})}
	var starts atomic.Int32
	manager.factory = func(_ Config, _ string, _ *journal, _ func(nativeproto.IncomingMessage) error) (session, error) {
		if starts.Add(1) == 1 {
			return first, nil
		}
		return fake, nil
	}
	if err := manager.Start(manager.State().Name); err != nil {
		t.Fatal(err)
	}
	awaitPhase(t, manager, "online")
	result := make(chan MessageResponse, 1)
	go func() {
		response, _ := manager.Send(context.Background(), "synthetic-request", nativeproto.ChatRequest{UserID: "synthetic-peer", Text: "synthetic"})
		result <- response
	}()
	<-first.sending
	if err := manager.Restart(); err != nil {
		t.Fatal(err)
	}
	awaitPhase(t, manager, "stopping")
	if starts.Load() != 1 {
		t.Fatal("发送未完成时启动了新会话")
	}
	close(first.release)
	response := <-result
	if !response.Result.Submitted || response.Result.Delivery != "unknown" || response.Error == "" {
		t.Fatalf("丢失发送事实: %+v", response)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if starts.Load() == 2 && manager.State().Phase == "online" {
			if fake.calls.Load() != 0 {
				t.Fatal("不应自动补发")
			}
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("发送完成后未重新上线")
}

// TestPublicRestart 验证认证、输入、旧会话清理顺序、重复请求及新会话状态，不连接真实对端。
func TestPublicRestart(t *testing.T) {
	manager, fake := testManager(t)
	first := &gatedRestartSession{fakeSession: fake, stopping: make(chan struct{}), release: make(chan struct{})}
	var starts atomic.Int32
	manager.factory = func(_ Config, _ string, _ *journal, _ func(nativeproto.IncomingMessage) error) (session, error) {
		if starts.Add(1) == 1 {
			return first, nil
		}
		return fake, nil
	}
	handler := testHandler(t, manager)
	if response := authRequest(handler.(*authenticatedHandler).handler, "POST", "/session/restart", "{}", "", nil, ""); response.Code != http.StatusUnauthorized {
		t.Fatalf("未认证请求: %d", response.Code)
	}
	for _, body := range []string{"", `{"name":"x"}`, `{} {}`, `null`} {
		if response := performRequest(handler, http.MethodPost, "/session/restart", body); response.Code != http.StatusBadRequest {
			t.Fatalf("非法请求 %q: %d", body, response.Code)
		}
	}
	if starts.Load() != 0 {
		t.Fatal("非法请求不应启动会话")
	}
	credentials := handler.(*authenticatedHandler)
	keyResponse := authRequest(credentials.handler, "POST", "/auth/api-key", "{}", "http://127.0.0.1:8788", credentials.cookie, "")
	var key map[string]string
	if keyResponse.Code != http.StatusOK || json.Unmarshal(keyResponse.Body.Bytes(), &key) != nil {
		t.Fatalf("生成API Key失败: %d %s", keyResponse.Code, keyResponse.Body.String())
	}
	if response := authRequest(credentials.handler, "POST", "/session/restart", `{}`, "", nil, "Bearer "+key["api_key"]); response.Code != http.StatusAccepted {
		t.Fatalf("离线上线: %d %s", response.Code, response.Body.String())
	}
	original := awaitPhase(t, manager, "online")
	manager.mu.Lock()
	manager.messages = []ReceivedMessage{{}}
	manager.receivedTotal = 1
	manager.mu.Unlock()
	if response := performRequest(handler, http.MethodPost, "/session/restart", `{}`); response.Code != http.StatusAccepted {
		t.Fatalf("重新上线: %d %s", response.Code, response.Body.String())
	}
	<-first.stopping
	if response := performRequest(handler, http.MethodPost, "/session/restart", `{}`); response.Code != http.StatusConflict || starts.Load() != 1 {
		t.Fatalf("退出中必须拒绝重复请求: %d", response.Code)
	}
	close(first.release)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		state := manager.State()
		if starts.Load() == 2 && state.Phase == "online" {
			if state.Name != original.Name || state.StartedAt.Equal(*original.StartedAt) || len(state.Messages) != 1 || state.ReceivedTotal != 1 {
				t.Fatalf("新会话状态错误: %+v", state)
			}
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("新会话未上线")
}

// TestRestartCancellationAndCleanupFailure 验证显式下线或清理失败不会启动待执行的新会话。
func TestRestartCancellationAndCleanupFailure(t *testing.T) {
	for _, tc := range []struct {
		name string
		stop bool
		err  error
	}{
		{name: "stop", stop: true},
		{name: "cleanup_failure", err: errors.New("synthetic cleanup error")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			manager, fake := testManager(t)
			first := &gatedRestartSession{fakeSession: fake, stopping: make(chan struct{}), release: make(chan struct{}), err: tc.err}
			var starts atomic.Int32
			manager.factory = func(_ Config, _ string, _ *journal, _ func(nativeproto.IncomingMessage) error) (session, error) {
				starts.Add(1)
				return first, nil
			}
			if err := manager.Start(manager.State().Name); err != nil {
				t.Fatal(err)
			}
			awaitPhase(t, manager, "online")
			if err := manager.Restart(); err != nil {
				t.Fatal(err)
			}
			<-first.stopping
			if tc.stop {
				manager.Stop()
			}
			close(first.release)
			phase := "offline"
			if tc.err != nil {
				phase = "failed"
			}
			state := awaitPhase(t, manager, phase)
			if starts.Load() != 1 || tc.err != nil && !strings.Contains(state.Error, tc.err.Error()) {
				t.Fatalf("不应启动新会话: %d %+v", starts.Load(), state)
			}
		})
	}
}

// TestRestartDuringStartupAndAfterFailure 验证启动期间重启不会提前打开第二个会话，失败后可显式重新上线。
func TestRestartDuringStartupAndAfterFailure(t *testing.T) {
	manager, fake := testManager(t)
	opened := make(chan struct{})
	release := make(chan struct{})
	var starts atomic.Int32
	manager.factory = func(_ Config, _ string, _ *journal, _ func(nativeproto.IncomingMessage) error) (session, error) {
		switch starts.Add(1) {
		case 1:
			close(opened)
			<-release
			return nil, context.Canceled
		case 2:
			return nil, errors.New("synthetic start failure")
		default:
			return fake, nil
		}
	}
	if err := manager.Start(manager.State().Name); err != nil {
		t.Fatal(err)
	}
	<-opened
	if err := manager.Restart(); err != nil || starts.Load() != 1 {
		t.Fatalf("启动中重新上线: %v", err)
	}
	close(release)
	state := awaitPhase(t, manager, "failed")
	if starts.Load() != 2 || !strings.Contains(state.Error, "synthetic start failure") {
		t.Fatalf("新会话启动错误未保留: %d %+v", starts.Load(), state)
	}
	if err := manager.Restart(); err != nil {
		t.Fatal(err)
	}
	awaitPhase(t, manager, "online")
	if starts.Load() != 3 {
		t.Fatal("失败后未启动新会话")
	}
}

// TestRestartCancelledByClose 验证程序关闭取消待执行的重新上线，即使等待旧会话超时也不会创建新会话。
func TestRestartCancelledByClose(t *testing.T) {
	manager, fake := testManager(t)
	first := &gatedRestartSession{fakeSession: fake, stopping: make(chan struct{}), release: make(chan struct{})}
	var starts atomic.Int32
	manager.factory = func(_ Config, _ string, _ *journal, _ func(nativeproto.IncomingMessage) error) (session, error) {
		starts.Add(1)
		return first, nil
	}
	if err := manager.Start(manager.State().Name); err != nil {
		t.Fatal(err)
	}
	awaitPhase(t, manager, "online")
	if err := manager.Restart(); err != nil {
		t.Fatal(err)
	}
	<-first.stopping
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := manager.Close(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("应报告等待取消: %v", err)
	}
	close(first.release)
	awaitPhase(t, manager, "offline")
	if starts.Load() != 1 || manager.Restart() == nil {
		t.Fatal("关闭后不得重新上线")
	}
}

// TestPublicHealthAndLists 验证健康状态按后台阶段返回200/503，列表使用原生快照且不冒充全网络名册。
func TestPublicHealthAndLists(t *testing.T) {
	manager, _ := testManager(t)
	handler := testHandler(t, manager)
	response := performRequest(handler, http.MethodGet, "/health", "")
	if response.Code != http.StatusServiceUnavailable || !strings.Contains(response.Body.String(), `"status":"unavailable"`) {
		t.Fatalf("离线健康检查：%d %s", response.Code, response.Body.String())
	}
	if err := manager.Start(manager.State().Name); err != nil {
		t.Fatal(err)
	}
	awaitPhase(t, manager, "online")
	response = performRequest(handler, http.MethodGet, "/health", "")
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"online_contacts":1`) || strings.Contains(response.Body.String(), `"pid"`) {
		t.Fatalf("原生健康状态错误：%d %s", response.Code, response.Body.String())
	}
	for _, path := range []string{"/contacts", "/groups"} {
		response = performRequest(handler, http.MethodGet, path, "")
		if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"complete":false`) || !strings.Contains(response.Body.String(), `"source":"native_protocol"`) {
			t.Fatalf("原生列表错误：%s %s", path, response.Body.String())
		}
	}
}

// TestPublicExactNameResolution 验证名称精确匹配、未命中404与同名409，歧义时不因其中一人在线而自动挑选。
func TestPublicExactNameResolution(t *testing.T) {
	_, fake, handler := startPublicManager(t)
	fake.mu.Lock()
	fake.snapshot.Contacts[0].Name = " Exact Name "
	fake.snapshot.Groups[0].Name = " Exact Group "
	fake.mu.Unlock()
	for _, tc := range []struct {
		path string
		want int
	}{
		{"/contacts?name=+Exact+Name+", 200}, {"/contacts?name=Exact+Name", 404}, {"/contacts?name=+exact+Name+", 404},
		{"/groups?name=+Exact+Group+", 200}, {"/groups?name=Exact+Group", 404},
		{"/contacts?name=a&name=b", 400}, {"/groups?name=%ZZ", 400}, {"/contacts?name=", 200}, {"/groups?name=", 200},
	} {
		response := performRequest(handler, http.MethodGet, tc.path, "")
		if response.Code != tc.want {
			t.Fatalf("%s: %d want%d %s", tc.path, response.Code, tc.want, response.Body.String())
		}
	}
	fake.mu.Lock()
	fake.snapshot.Contacts[1].Name = " Exact Name "
	fake.snapshot.Groups[1].Name = " Exact Group "
	fake.mu.Unlock()
	for _, path := range []string{"/contacts?name=+Exact+Name+", "/groups?name=+Exact+Group+"} {
		response := performRequest(handler, http.MethodGet, path, "")
		if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), `"status":"ambiguous"`) {
			t.Fatalf("未拒绝同名：%s %s", path, response.Body.String())
		}
	}
	response := performRequest(handler, http.MethodPost, "/send/user", `{"name":" Exact Name ","text":"不得按在线状态挑人"}`)
	if response.Code != http.StatusConflict || fake.calls.Load() != 0 {
		t.Fatalf("同名触发发送：%d %s", response.Code, response.Body.String())
	}
}

// TestPublicSendByIDAndName 验证沿用main的ID/名称请求无需request_id，目标和全文原样交给协议，响应顶层保留原生事实。
func TestPublicSendByIDAndName(t *testing.T) {
	_, fake, handler := startPublicManager(t)
	cases := []struct {
		path, body string
		command    uint32
		request    nativeproto.ChatRequest
	}{
		{"/send/user", `{"user_id":"synthetic-peer","text":"第一行\n第二行🙂"}`, 1004, nativeproto.ChatRequest{UserID: "synthetic-peer", Text: "第一行\n第二行🙂"}},
		{"/send/user", `{"name":"合成联系人","text":"保留字面\\n"}`, 1004, nativeproto.ChatRequest{UserID: "synthetic-peer", Text: "保留字面\\n"}},
		{"/send/group", `{"group_id":"group-two","text":"群正文\r\n第二行"}`, 3011, nativeproto.ChatRequest{GroupID: "group-two", Text: "群正文\r\n第二行"}},
		{"/send/group", `{"name":"双成员群","text":" "}`, 3011, nativeproto.ChatRequest{GroupID: "group-two", Text: " "}},
	}
	ids := make(map[string]bool)
	for index, tc := range cases {
		response := performRequest(handler, http.MethodPost, tc.path, tc.body)
		var result publicSendResponse
		if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		if response.Code != http.StatusOK || result.RequestID == "" || ids[result.RequestID] || result.Command != tc.command || !result.Submitted || result.Delivery != "unknown" || strings.Contains(response.Body.String(), `"result":`) {
			t.Fatalf("发送结果错误：%d %s", response.Code, response.Body.String())
		}
		ids[result.RequestID] = true
		fake.mu.Lock()
		actual := fake.requests[index]
		fake.mu.Unlock()
		if actual != tc.request {
			t.Fatalf("目标或正文被修改：%+v want %+v", actual, tc.request)
		}
	}
	if fake.calls.Load() != int32(len(cases)) {
		t.Fatal("协议调用次数错误")
	}
	// 公开接口每次HTTP调用都是新请求，不能把相同正文误当成调用方已授权的自动幂等重放。
	response := performRequest(handler, http.MethodPost, cases[0].path, cases[0].body)
	var result publicSendResponse
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if response.Code != 200 || ids[result.RequestID] || fake.calls.Load() != 5 {
		t.Fatal("公开重复调用没有生成独立请求")
	}
}

// TestPublicRejectsInvalidRequests 验证不明确的目标、混用ID、重复字段、坏JSON及正文预算在发送前拒绝。
func TestPublicRejectsInvalidRequests(t *testing.T) {
	_, fake, handler := startPublicManager(t)
	fake.mu.Lock()
	fake.snapshot.Groups = append(fake.snapshot.Groups, nativeproto.GroupInfo{GroupID: "group-offline", Name: "全员离线群", Members: []string{"synthetic-own", "other-peer"}})
	fake.mu.Unlock()
	for _, tc := range []struct {
		path, body string
		want       int
	}{
		{"/send/user", `{}`, 400},
		{"/send/user", `{"text":"正文"}`, 400},
		{"/send/user", `{"user_id":"synthetic-peer","name":"","text":"正文"}`, 400},
		{"/send/user", `{"user_id":"synthetic-peer","name":"合成联系人","text":"正文"}`, 400},
		{"/send/user", `{"group_id":"group-two","text":"正文"}`, 400},
		{"/send/group", `{"user_id":"synthetic-peer","text":"正文"}`, 400},
		{"/send/user", `{"user_id":"synthetic-peer","text":""}`, 400},
		{"/send/user", `{"user_id":"synthetic-peer","text":null}`, 400},
		{"/send/user", `{"user_id":"synthetic-peer","text":"正文","text":"不同正文"}`, 400},
		{"/send/user", `{"user_id":"synthetic-peer","text":"正文","request_id":"caller-request"}`, 400},
		{"/send/user", `{"user_id":"synthetic-peer","text":"正文"} {}`, 400},
		{"/send/user", `{"user_id":"synthetic-peer","text":"` + strings.Repeat("汉", 1366) + `"}`, 400},
		{"/send/user", `{"user_id":"missing-peer","text":"正文"}`, 404},
		{"/send/user", `{"name":"不存在","text":"正文"}`, 404},
		{"/send/user", `{"user_id":"other-peer","text":"正文"}`, 503},
		{"/send/group", `{"group_id":"group-offline","text":"正文"}`, 503},
	} {
		response := performRequest(handler, http.MethodPost, tc.path, tc.body)
		if response.Code != tc.want {
			t.Fatalf("%s %s: %d want %d %s", tc.path, tc.body, response.Code, tc.want, response.Body.String())
		}
	}
	if fake.calls.Load() != 0 {
		t.Fatal("坏请求触发了发送")
	}
}

// TestPublicGroupSkipsOfflineMember 验证在线成员投递成功时返回200及离线成员明细，后续成员上线不自动补发或修改旧结果。
func TestPublicGroupSkipsOfflineMember(t *testing.T) {
	_, fake, handler := startPublicManager(t)
	fake.recipients = []nativeproto.GroupRecipientResult{
		{UserID: "synthetic-peer", PeerIndex: 1, Submitted: true, TransportAcknowledged: true},
		{UserID: "other-peer", Skipped: true, Error: "协议连接尚未就绪，本次未投递"},
	}
	recorded := performRequest(handler, http.MethodPost, "/send/group", `{"group_id":"group-three","text":"向在线成员发送"}`)
	var response publicSendResponse
	if err := json.Unmarshal(recorded.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if recorded.Code != http.StatusOK || response.Error != "" || !response.Submitted || response.TransportAcknowledged || len(response.Recipients) != 2 || !response.Recipients[1].Skipped || fake.calls.Load() != 1 {
		t.Fatalf("离线成员阻断请求或被伪造为全部确认：%d %+v", recorded.Code, response)
	}
	fake.mu.Lock()
	fake.snapshot.OnlineUserIDs = append(fake.snapshot.OnlineUserIDs, "other-peer")
	fake.mu.Unlock()
	_ = performRequest(handler, http.MethodGet, "/api/state", "")
	body, err := json.Marshal(map[string]string{"request_id": response.RequestID, "group_id": "group-three", "text": "向在线成员发送"})
	if err != nil {
		t.Fatal(err)
	}
	cached := performRequest(handler, http.MethodPost, "/api/messages/group", string(body))
	var previous MessageResponse
	if err := json.Unmarshal(cached.Body.Bytes(), &previous); err != nil {
		t.Fatal(err)
	}
	if cached.Code != http.StatusOK || !previous.Result.Recipients[1].Skipped || fake.calls.Load() != 1 {
		t.Fatalf("成员上线触发补发或改写原请求：%d %+v", cached.Code, previous)
	}
}

// TestPublicRejectsMalformedBoundaryData 验证非法UTF-8、超长请求体、NUL正文及非JSON内容类型不会进入协议。
func TestPublicRejectsMalformedBoundaryData(t *testing.T) {
	_, fake, handler := startPublicManager(t)
	for _, body := range []string{
		"{\"user_id\":\"synthetic-peer\",\"text\":\"\xff\"}",
		strings.Repeat("x", 30000),
		`{"user_id":"synthetic-peer","text":"正文\u0000"}`,
	} {
		response := performRequest(handler, http.MethodPost, "/send/user", body)
		if response.Code != http.StatusBadRequest {
			t.Fatalf("坏输入未拒绝：%d %s", response.Code, response.Body.String())
		}
	}
	request := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:8788/send/user", strings.NewReader(`{"user_id":"synthetic-peer","text":"正文"}`))
	request.Header.Set("Content-Type", "text/plain")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusUnsupportedMediaType || fake.calls.Load() != 0 {
		t.Fatal("非JSON或坏输入触发发送")
	}
}

// TestPublicUnknownResultRetainsSubmission 验证提交后错误使用502且不丢失消息编号、Submitted和unknown，不自动重发。
func TestPublicUnknownResultRetainsSubmission(t *testing.T) {
	_, fake, handler := startPublicManager(t)
	fake.sendErr = errors.New("synthetic transport failure")
	response := performRequest(handler, http.MethodPost, "/send/user", `{"user_id":"synthetic-peer","text":"合成正文"}`)
	var result publicSendResponse
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if response.Code != http.StatusBadGateway || !result.Submitted || result.MessageID != "42" || result.RequestID == "" || result.Delivery != "unknown" || fake.calls.Load() != 1 {
		t.Fatalf("未知结果被改写：%d %s", response.Code, response.Body.String())
	}
}

// TestPublicOfflineRejectsSend 验证后台下线后目标仍可查询，但发送明确503且不进入协议。
func TestPublicOfflineRejectsSend(t *testing.T) {
	manager, fake, handler := startPublicManager(t)
	manager.Stop()
	awaitPhase(t, manager, "offline")
	response := performRequest(handler, http.MethodPost, "/send/user", `{"user_id":"synthetic-peer","text":"不应发送"}`)
	if response.Code != http.StatusServiceUnavailable || fake.calls.Load() != 0 {
		t.Fatalf("离线调用错误：%d %s", response.Code, response.Body.String())
	}
	if err := manager.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}

// TestPublicConcurrentCallsKeepSeparateIDs 验证并发公开请求各生成一个新标识，不合并相同正文、不增加协议调用次数。
func TestPublicConcurrentCallsKeepSeparateIDs(t *testing.T) {
	_, fake, handler := startPublicManager(t)
	var wg sync.WaitGroup
	ids := make(chan string, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			response := performRequest(handler, http.MethodPost, "/send/user", `{"user_id":"synthetic-peer","text":"并发合成正文"}`)
			var result publicSendResponse
			if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
				t.Error(err)
				return
			}
			if response.Code != http.StatusOK || !result.Submitted {
				t.Errorf("并发发送失败：%d %s", response.Code, response.Body.String())
				return
			}
			ids <- result.RequestID
		}()
	}
	wg.Wait()
	close(ids)
	seen := make(map[string]bool)
	for id := range ids {
		if id == "" || seen[id] {
			t.Fatalf("并发请求标识重复：%s", id)
		}
		seen[id] = true
	}
	if len(seen) != 8 || fake.calls.Load() != 8 {
		t.Fatal("公开并发请求未按次独立提交")
	}
}

// TestPublicThreeOnlineTargets 验证第三个联系人可按精确名称通过公开接口选择，不回退到原唯一对端。
func TestPublicThreeOnlineTargets(t *testing.T) {
	_, fake, handler := startPublicManager(t)
	fake.mu.Lock()
	fake.snapshot.OnlineUserIDs = []string{"synthetic-peer", "other-peer", "third-peer"}
	fake.snapshot.Contacts = append(fake.snapshot.Contacts, nativeproto.Contact{UserID: "third-peer", Name: "第三个联系人", Discovery: netip.MustParseAddrPort("127.0.0.4:9011")})
	fake.mu.Unlock()
	response := performRequest(handler, http.MethodPost, "/send/user", `{"name":"第三个联系人","text":"只交给第三个身份"}`)
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if response.Code != http.StatusOK || len(fake.requests) != 1 || fake.requests[0].UserID != "third-peer" {
		t.Fatalf("选错了对端：%d %s", response.Code, response.Body.String())
	}
}
