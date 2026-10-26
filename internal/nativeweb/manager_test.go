// 本文件通过合成会话验证Web后台生命周期、并发请求去重、未知结果保留及记录失败，不连接真实对端。
package nativeweb

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"neiwangtong-gateway/internal/nativeproto"
)

type fakeSession struct {
	mu       sync.Mutex
	snapshot nativeproto.ClientSnapshot
	calls    atomic.Int32
	block    chan struct{}
	sendErr  error
	requests []nativeproto.ChatRequest
}

// Run 发布合成就绪事件并等待取消，返回正常退出；回调失败直接返回，不执行任何网络操作。
func (s *fakeSession) Run(ctx context.Context, observe func(nativeproto.ClientEvent) error) (nativeproto.ClientResult, error) {
	if err := observe(nativeproto.ClientEvent{Stage: "protocol_ready"}); err != nil {
		return nativeproto.ClientResult{}, err
	}
	<-ctx.Done()
	s.mu.Lock()
	s.snapshot.Online = false
	s.mu.Unlock()
	return nativeproto.ClientResult{ProtocolReady: true, GracefulExit: true}, observe(nativeproto.ClientEvent{Stage: "stopped"})
}

// Snapshot 复制合成联系人与群成员，供管理器并发读取；无I/O或错误。
func (s *fakeSession) Snapshot() nativeproto.ClientSnapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	result := s.snapshot
	result.OnlineUserIDs = append([]string(nil), result.OnlineUserIDs...)
	result.Contacts = append([]nativeproto.Contact(nil), result.Contacts...)
	result.Groups = append([]nativeproto.GroupInfo(nil), result.Groups...)
	for i := range result.Groups {
		result.Groups[i].Members = append([]string(nil), result.Groups[i].Members...)
	}
	return result
}

// SendUser 记录一次合成调用，可等待block模拟慢速提交；返回提交事实与预设错误，无网络操作。
func (s *fakeSession) SendUser(ctx context.Context, id, text string) (nativeproto.ChatResult, error) {
	s.mu.Lock()
	s.requests = append(s.requests, nativeproto.ChatRequest{UserID: id, Text: text})
	s.mu.Unlock()
	return s.send(ctx)
}

// send 用ctx等待合成阻塞并返回预设提交结果；取消或sendErr返回错误，无真实网络操作。
func (s *fakeSession) send(ctx context.Context) (nativeproto.ChatResult, error) {
	s.calls.Add(1)
	if s.block != nil {
		select {
		case <-s.block:
		case <-ctx.Done():
			return nativeproto.ChatResult{MessageID: "42", Submitted: true, Delivery: "unknown"}, ctx.Err()
		}
	}
	return nativeproto.ChatResult{Command: nativeproto.CommandUserMessage, MessageID: "42", Submitted: true, TransportAcknowledged: s.sendErr == nil, Delivery: "unknown"}, s.sendErr
}

// SendGroup 复用合成发送计数并返回3011结果；参数只用于模拟管理器调用，无外部I/O。
func (s *fakeSession) SendGroup(ctx context.Context, id, text string) (nativeproto.ChatResult, error) {
	s.mu.Lock()
	s.requests = append(s.requests, nativeproto.ChatRequest{GroupID: id, Text: text})
	s.mu.Unlock()
	result, err := s.send(ctx)
	result.Command = nativeproto.CommandGroupMessage
	return result, err
}

// testManager 创建私有临时目录与合成后台，注册正常退出清理；构造失败立即终止测试。
func testManager(t *testing.T) (*Manager, *fakeSession) {
	t.Helper()
	config := Config{Local: netip.MustParseAddr("127.0.0.1"), Prefix: netip.MustParsePrefix("127.0.0.0/8"),
		Remote: netip.MustParseAddrPort("127.0.0.2:9011"), Name: "合成Web节点", DataDir: t.TempDir(), DiscoveryPort: 9011, ENetPort: 9012}
	manager, err := New(config)
	if err != nil {
		t.Fatal(err)
	}
	fake := &fakeSession{snapshot: nativeproto.ClientSnapshot{OwnUserID: "synthetic-own", Online: true, PeerUserID: "synthetic-peer", OnlineUserIDs: []string{"synthetic-peer"},
		Contacts: []nativeproto.Contact{{UserID: "synthetic-peer", Name: "合成联系人", Discovery: config.Remote},
			{UserID: "other-peer", Name: "其他联系人", Discovery: netip.MustParseAddrPort("127.0.0.3:9011")}},
		Groups: []nativeproto.GroupInfo{{GroupID: "group-two", Name: "双成员群", Members: []string{"synthetic-own", "synthetic-peer"}},
			{GroupID: "group-three", Name: "多成员群", Members: []string{"synthetic-own", "synthetic-peer", "other-peer"}}}}}
	manager.factory = func(Config, string, *journal) (session, error) { return fake, nil }
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = manager.Close(ctx)
	})
	return manager, fake
}

// awaitPhase 等待合成运行循环发布指定状态，最多两秒；超时报告最后快照并终止测试。
func awaitPhase(t *testing.T, manager *Manager, phase string) State {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		state := manager.State()
		if state.Phase == phase {
			return state
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("未进入%s：%+v", phase, manager.State())
	return State{}
}

// TestManagerLifecycleAndViews 验证关闭网页不参与后台生命周期，在线/成员限制及返回值复制不误报发送能力。
func TestManagerLifecycleAndViews(t *testing.T) {
	manager, _ := testManager(t)
	if manager.State().Phase != "offline" {
		t.Fatal("构造不应上线")
	}
	if err := manager.Start(manager.State().Name); err != nil {
		t.Fatal(err)
	}
	state := awaitPhase(t, manager, "online")
	if !state.Contacts[0].Sendable || state.Contacts[1].Sendable || !state.Groups[0].Sendable || state.Groups[1].Sendable {
		t.Fatalf("目标能力错误：%+v", state)
	}
	state.Groups[0].Members[0] = "changed"
	state.Contacts[0].Name = "changed"
	*state.StartedAt = time.Time{}
	if fresh := manager.State(); fresh.Groups[0].Members[0] == "changed" || fresh.Contacts[0].Name == "changed" || fresh.StartedAt.IsZero() {
		t.Fatal("State泄漏可变状态")
	}
	if err := manager.Start(manager.State().Name); err == nil {
		t.Fatal("重复上线必须拒绝")
	}
	manager.Stop()
	state = awaitPhase(t, manager, "offline")
	if state.Contacts[0].Sendable || state.Groups[0].Sendable {
		t.Fatal("下线后不能继续标记可发送")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := manager.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if err := manager.Start(manager.State().Name); err == nil {
		t.Fatal("关闭后不能重启")
	}
}

// TestAllThreeOnlineContactsAreVisible 回归Web不再用唯一PeerUserID判断在线，三个已就绪身份均可显示和选择。
func TestAllThreeOnlineContactsAreVisible(t *testing.T) {
	manager, fake := testManager(t)
	manager.config.Discover = true
	fake.mu.Lock()
	fake.snapshot.PeerUserID = ""
	fake.snapshot.OnlineUserIDs = []string{"synthetic-peer", "other-peer", "third-peer"}
	fake.snapshot.Contacts = append(fake.snapshot.Contacts, nativeproto.Contact{UserID: "third-peer", Name: "第三位合成联系人", Discovery: netip.MustParseAddrPort("127.0.0.4:9011")})
	fake.mu.Unlock()
	if err := manager.Start(manager.State().Name); err != nil {
		t.Fatal(err)
	}
	state := awaitPhase(t, manager, "online")
	if len(state.Contacts) != 3 || state.DiscoveryMode != "subnet" {
		t.Fatalf("三位联系人未全部显示：%+v", state)
	}
	for _, contact := range state.Contacts {
		if !contact.Online || !contact.Sendable {
			t.Fatalf("已就绪联系人被误标离线：%+v", contact)
		}
	}
	fake.mu.Lock()
	fake.snapshot.OnlineUserIDs = []string{"synthetic-peer", "third-peer"}
	fake.mu.Unlock()
	state = manager.State()
	if state.Contacts[1].Online || !state.Contacts[0].Online || !state.Contacts[2].Online {
		t.Fatalf("一人离线影响了其他人：%+v", state.Contacts)
	}
}

// TestConcurrentRequestDeduplication 验证同一网页request_id并发提交只触发一次协议调用，正文不同则明确拒绝。
func TestConcurrentRequestDeduplication(t *testing.T) {
	manager, fake := testManager(t)
	if err := manager.Start(manager.State().Name); err != nil {
		t.Fatal(err)
	}
	awaitPhase(t, manager, "online")
	request := nativeproto.ChatRequest{UserID: "synthetic-peer", Text: "第一行\n第二行"}
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			response, status := manager.Send(context.Background(), "synthetic-request-1", request)
			if status != http.StatusOK || !response.Result.Submitted || response.Result.Delivery != "unknown" {
				t.Errorf("发送结果：%d %+v", status, response)
			}
		}()
	}
	wg.Wait()
	if fake.calls.Load() != 1 {
		t.Fatalf("实际调用%d次", fake.calls.Load())
	}
	request.Text = "不同正文"
	if _, status := manager.Send(context.Background(), "synthetic-request-1", request); status != http.StatusConflict {
		t.Fatal("相同标识不同正文必须拒绝")
	}
	manager.Stop()
	awaitPhase(t, manager, "offline")
	files, err := filepath.Glob(filepath.Join(manager.config.DataDir, "session-*.jsonl"))
	if err != nil || len(files) != 1 {
		t.Fatalf("记录数：%v %v", files, err)
	}
	data, err := os.ReadFile(files[0])
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "第一行") || strings.Count(string(data), `"event":"chat_request"`) != 1 || !strings.Contains(string(data), `"message_id":"42"`) {
		t.Fatalf("发送摘要记录错误：%s", data)
	}
}

// TestBrowserCancellationDoesNotCancelAcceptedSend 回归：浏览器断开不能取消已受理发送，重复相同标识可取得原结果而不补发。
func TestBrowserCancellationDoesNotCancelAcceptedSend(t *testing.T) {
	manager, fake := testManager(t)
	fake.block = make(chan struct{})
	if err := manager.Start(manager.State().Name); err != nil {
		t.Fatal(err)
	}
	awaitPhase(t, manager, "online")
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan MessageResponse, 1)
	request := nativeproto.ChatRequest{UserID: "synthetic-peer", Text: "合成消息"}
	go func() {
		response, _ := manager.Send(ctx, "cancel-request-1", request)
		result <- response
	}()
	deadline := time.Now().Add(time.Second)
	for fake.calls.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if fake.calls.Load() != 1 {
		t.Fatal("请求未进入发送")
	}
	cancel()
	close(fake.block)
	response := <-result
	if response.Error != "" || !response.Result.TransportAcknowledged {
		t.Fatalf("网页取消影响了发送：%+v", response)
	}
	if _, status := manager.Send(context.Background(), "cancel-request-1", request); status != http.StatusOK || fake.calls.Load() != 1 {
		t.Fatal("重复请求不应再次发送")
	}
}

// TestUnknownSendIsRetainedWithoutRetry 验证提交后错误仍返回消息编号和unknown，重用同标识不会再次调用协议。
func TestUnknownSendIsRetainedWithoutRetry(t *testing.T) {
	manager, fake := testManager(t)
	fake.sendErr = errors.New("synthetic transport failure")
	if err := manager.Start(manager.State().Name); err != nil {
		t.Fatal(err)
	}
	awaitPhase(t, manager, "online")
	request := nativeproto.ChatRequest{GroupID: "group-two", Text: "合成群消息"}
	for i := 0; i < 2; i++ {
		response, status := manager.Send(context.Background(), "unknown-request-1", request)
		if status != http.StatusBadGateway || !response.Result.Submitted || response.Result.MessageID != "42" || response.Result.Delivery != "unknown" || response.Error == "" {
			t.Fatalf("未知事实丢失：%d %+v", status, response)
		}
	}
	if fake.calls.Load() != 1 {
		t.Fatal("未知结果被自动重发")
	}
}

// TestJournalFailurePreventsSending 验证摘要Sync失败时不进入协议，后台停止且明确保留失败状态。
func TestJournalFailurePreventsSending(t *testing.T) {
	manager, fake := testManager(t)
	if err := manager.Start(manager.State().Name); err != nil {
		t.Fatal(err)
	}
	awaitPhase(t, manager, "online")
	manager.mu.Lock()
	err := manager.current.log.file.Close()
	manager.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	response, status := manager.Send(context.Background(), "journal-failure-1", nativeproto.ChatRequest{UserID: "synthetic-peer", Text: "不应发送"})
	if status != http.StatusInternalServerError || response.Result.Submitted || fake.calls.Load() != 0 {
		t.Fatalf("记录失败仍发送：%d %+v", status, response)
	}
	if state := awaitPhase(t, manager, "failed"); state.Error == "" {
		t.Fatal("记录错误被吞掉")
	}
}

// TestCancelledRequestBeforeAcceptanceDoesNotSend 验证已经取消的HTTP请求不进入后台发送，也不占用去重标识。
func TestCancelledRequestBeforeAcceptanceDoesNotSend(t *testing.T) {
	manager, fake := testManager(t)
	if err := manager.Start(manager.State().Name); err != nil {
		t.Fatal(err)
	}
	awaitPhase(t, manager, "online")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	response, status := manager.Send(ctx, "already-cancelled-1", nativeproto.ChatRequest{UserID: "synthetic-peer", Text: "不应发送"})
	if status != http.StatusRequestTimeout || response.Result.Submitted || fake.calls.Load() != 0 {
		t.Fatalf("取消请求仍发送：%d %+v", status, response)
	}
}

// TestCancellationDoesNotHideCleanupFailure 验证主动取消可正常下线，但同时发生的关闭错误不能被忽略。
func TestCancellationDoesNotHideCleanupFailure(t *testing.T) {
	if !cancellationOnly(errors.Join(context.Canceled, errors.Join(context.Canceled))) {
		t.Fatal("纯取消应被识别")
	}
	if cancellationOnly(errors.Join(context.Canceled, errors.New("synthetic close error"))) || cancellationOnly(nil) {
		t.Fatal("清理错误不能当成主动取消")
	}
}

// TestFactoryFailureRemainsFailedWithoutReconnect 验证上线准备失败保留错误且不自动重连，网页仍可读取失败原因。
func TestFactoryFailureRemainsFailedWithoutReconnect(t *testing.T) {
	manager, _ := testManager(t)
	var calls atomic.Int32
	manager.factory = func(Config, string, *journal) (session, error) {
		calls.Add(1)
		return nil, errors.New("synthetic setup failure")
	}
	if err := manager.Start(manager.State().Name); err != nil {
		t.Fatal(err)
	}
	state := awaitPhase(t, manager, "failed")
	if state.Error != "synthetic setup failure" || calls.Load() != 1 {
		t.Fatalf("失败处理错误：%+v calls=%d", state, calls.Load())
	}
}

// TestDirectoryPersistenceAndCorruption 验证自己的联系人新快照可只读恢复；损坏最新文件不静默回退，历史文件不覆盖。
func TestDirectoryPersistenceAndCorruption(t *testing.T) {
	dir := t.TempDir()
	ownID := "00112233445566778899aabbccddeeff"
	if err := os.WriteFile(filepath.Join(dir, "identity"), []byte(ownID), 0600); err != nil {
		t.Fatal(err)
	}
	directory, err := nativeproto.NewDirectory(ownID, []nativeproto.Contact{{UserID: "synthetic-peer", Name: "合成联系人", Discovery: netip.MustParseAddrPort("127.0.0.2:9011")}})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "directory-0001.json")
	if err := saveDirectory(path, directory); err != nil {
		t.Fatal(err)
	}
	if err := saveDirectory(path, directory); err == nil {
		t.Fatal("已有快照不得覆盖")
	}
	loaded, err := loadDirectory(dir, ownID)
	if err != nil || len(loaded.ListContacts()) != 1 {
		t.Fatalf("恢复失败：%v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "directory-0002.json"), []byte(`{"contacts":`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadDirectory(dir, ownID); err == nil {
		t.Fatal("损坏新快照不得静默回退")
	}
	data, err := os.ReadFile(path)
	if err != nil || !json.Valid(data) {
		t.Fatal("历史文件损坏")
	}
}
