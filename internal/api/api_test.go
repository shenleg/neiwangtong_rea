// 本文件用替代 api 实现验证 HTTP 层的路由、状态码与 JSON 编解码；不接触真实客户端或数据库。
package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"neiwangtong-gateway/internal/agent"
	"neiwangtong-gateway/internal/contacts"
	"neiwangtong-gateway/internal/groups"
	"neiwangtong-gateway/internal/ipc"
)

// fakeAPI 记录处理器委托的调用，可注入各类错误；实现 api 接口。
type fakeAPI struct {
	contacts      []contacts.Contact
	groups        []groups.Group
	resolveUser   func(name string) (contacts.Contact, error)
	resolveGroup  func(name string) (groups.Group, error)
	sendUser      func(userID, text string) (ipc.SendResult, error)
	sendGroup     func(groupID, text string) (ipc.SendResult, error)
	preflightErr  error
	userCalls     []struct{ id, text string }
	groupCalls    []struct{ id, text string }
}

func (f *fakeAPI) Preflight() (ipc.Preflight, error) {
	return ipc.Preflight{PID: 1234}, f.preflightErr
}

func (f *fakeAPI) ListContacts() (agent.ContactsSnapshot, error) {
	if f.contacts == nil {
		return agent.ContactsSnapshot{}, agent.ErrNotConfigured
	}
	return agent.ContactsSnapshot{Table: "OrgStruct_T", Users: f.contacts}, nil
}

func (f *fakeAPI) ResolveUserName(name string) (contacts.Contact, error) {
	return f.resolveUser(name)
}

func (f *fakeAPI) ListGroups() (agent.GroupsSnapshot, error) {
	if f.groups == nil {
		return agent.GroupsSnapshot{}, agent.ErrNotConfigured
	}
	return agent.GroupsSnapshot{Table: "QGroupInfo_T", Groups: f.groups}, nil
}

func (f *fakeAPI) ResolveGroupName(name string) (groups.Group, error) {
	return f.resolveGroup(name)
}

func (f *fakeAPI) SendToUser(userID, text string) (ipc.SendResult, error) {
	f.userCalls = append(f.userCalls, struct{ id, text string }{userID, text})
	return f.sendUser(userID, text)
}

func (f *fakeAPI) SendToGroup(groupID, text string) (ipc.SendResult, error) {
	f.groupCalls = append(f.groupCalls, struct{ id, text string }{groupID, text})
	return f.sendGroup(groupID, text)
}

// okSend 返回一次成功调用的合成结果。
func okSend(id string) (ipc.SendResult, error) {
	return ipc.SendResult{AttemptID: id, AttemptReserved: true, Delivery: ipc.Unknown}, nil
}

// doJSON 对 mux 发起一次请求并返回状态码与解析后的响应体。
func doJSON(t *testing.T, mux *http.ServeMux, method, path, body string) (int, map[string]any) {
	t.Helper()
	var reader *bytes.Reader
	if body == "" {
		reader = bytes.NewReader(nil)
	} else {
		reader = bytes.NewReader([]byte(body))
	}
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, httptest.NewRequest(method, path, reader))
	var response map[string]any
	if recorder.Body.Len() > 0 {
		// 路由层自动生成的 405 是纯文本；本服务自身的响应始终是 JSON。
		if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil && recorder.Code < 400 {
			t.Fatalf("解析响应 %q: %v", recorder.Body.String(), err)
		}
	}
	return recorder.Code, response
}

func defaultResolve() (func(string) (contacts.Contact, error), func(string) (groups.Group, error)) {
	resolveUser := func(name string) (contacts.Contact, error) {
		if name == "Alice" {
			return contacts.Contact{Name: "Alice", UserID: "u-1"}, nil
		}
		if name == "Carol" {
			return contacts.Contact{}, &agent.AmbiguousContactsError{Matches: []contacts.Contact{{Name: "Carol", UserID: "u-2"}, {Name: "Carol", UserID: "u-3"}}}
		}
		return contacts.Contact{}, fmt.Errorf("%w: Nobody", agent.ErrNotFound)
	}
	resolveGroup := func(name string) (groups.Group, error) {
		if name == "研发群" {
			return groups.Group{Name: "研发群", GroupID: "g-1"}, nil
		}
		return groups.Group{}, fmt.Errorf("%w: x", agent.ErrNotFound)
	}
	return resolveUser, resolveGroup
}

// TestHealth 验证健康检查正常与客户端不可用两种状态。
func TestHealth(t *testing.T) {
	resolveUser, resolveGroup := defaultResolve()
	mux := NewMux(&fakeAPI{resolveUser: resolveUser, resolveGroup: resolveGroup})
	if code, body := doJSON(t, mux, "GET", "/health", ""); code != 200 || body["pid"] != float64(1234) {
		t.Errorf("健康检查 code=%d body=%v", code, body)
	}
	failing := NewMux(&fakeAPI{preflightErr: errors.New("找不到窗口")})
	if code, _ := doJSON(t, failing, "GET", "/health", ""); code != 503 {
		t.Errorf("预检失败应 503，实际 %d", code)
	}
}

// TestContactsEndpoints 验证联系人列表与名称查询的状态码映射。
func TestContactsEndpoints(t *testing.T) {
	resolveUser, resolveGroup := defaultResolve()
	api := &fakeAPI{contacts: []contacts.Contact{{Name: "Alice", UserID: "u-1"}}, resolveUser: resolveUser, resolveGroup: resolveGroup}
	mux := NewMux(api)
	if code, body := doJSON(t, mux, "GET", "/contacts", ""); code != 200 {
		t.Errorf("列表应 200，实际 %d", code)
	} else if list, ok := body["contacts"].([]any); !ok || len(list) != 1 {
		t.Errorf("列表内容不符: %v", body)
	}
	if code, body := doJSON(t, mux, "GET", "/contacts?name=Alice", ""); code != 200 || body["status"] != "unique" {
		t.Errorf("唯一命中应 200/unique，实际 %d %v", code, body)
	}
	if code, body := doJSON(t, mux, "GET", "/contacts?name=Carol", ""); code != 409 || body["status"] != "ambiguous" {
		t.Errorf("同名应 409/ambiguous，实际 %d %v", code, body)
	}
	if code, body := doJSON(t, mux, "GET", "/contacts?name=Nobody", ""); code != 404 || body["status"] != "not_found" {
		t.Errorf("未命中应 404/not_found，实际 %d %v", code, body)
	}
	unconfigured := NewMux(&fakeAPI{resolveUser: resolveUser, resolveGroup: resolveGroup})
	if code, _ := doJSON(t, unconfigured, "GET", "/contacts", ""); code != 503 {
		t.Errorf("未配置应 503，实际 %d", code)
	}
}

// TestGroupsEndpoints 验证群列表与群名查询的状态码映射。
func TestGroupsEndpoints(t *testing.T) {
	resolveUser, resolveGroup := defaultResolve()
	api := &fakeAPI{groups: []groups.Group{{Name: "研发群", GroupID: "g-1"}}, resolveUser: resolveUser, resolveGroup: resolveGroup}
	mux := NewMux(api)
	if code, _ := doJSON(t, mux, "GET", "/groups", ""); code != 200 {
		t.Errorf("列表应 200，实际 %d", code)
	}
	if code, body := doJSON(t, mux, "GET", "/groups?name=研发群", ""); code != 200 || body["status"] != "unique" {
		t.Errorf("唯一命中应 200/unique，实际 %d %v", code, body)
	}
	if code, _ := doJSON(t, mux, "GET", "/groups?name=x", ""); code != 404 {
		t.Errorf("未命中应 404，实际 %d", code)
	}
}

// TestSendUser 验证按 ID、按名称发送与各类请求错误、发送错误的状态码。
func TestSendUser(t *testing.T) {
	resolveUser, resolveGroup := defaultResolve()
	api := &fakeAPI{
		resolveUser:  resolveUser,
		resolveGroup: resolveGroup,
		sendUser:     func(userID, text string) (ipc.SendResult, error) { return okSend("NWT-GW-API-USER") },
	}
	mux := NewMux(api)

	if code, body := doJSON(t, mux, "POST", "/send/user",
		`{"user_id":"u-1","text":"hi"}`); code != 200 || body["attempt_id"] != "NWT-GW-API-USER" {
		t.Errorf("按 ID 发送应 200，实际 %d %v", code, body)
	}
	if code, _ := doJSON(t, mux, "POST", "/send/user",
		`{"name":"Alice","text":"hi"}`); code != 200 {
		t.Errorf("按名称发送应 200，实际 %d", code)
	}
	if !reflect.DeepEqual(api.userCalls, []struct{ id, text string }{{"u-1", "hi"}, {"u-1", "hi"}}) {
		t.Errorf("名称应解析为 ID 后发送: %+v", api.userCalls)
	}

	for name, body := range map[string]string{
		"缺正文": `{"user_id":"u-1"}`,
		"缺目标": `{"text":"hi"}`,
		"双目标": `{"user_id":"u-1","name":"Alice","text":"hi"}`,
		"未知字段": `{"user_id":"u-1","text":"hi","extra":1}`,
		"坏JSON": `{`,
	} {
		if code, _ := doJSON(t, mux, "POST", "/send/user", body); code != 400 {
			t.Errorf("%s 应 400，实际 %d", name, code)
		}
	}
}

// TestSendGroup 验证群发送按名称解析与错误映射。
func TestSendGroup(t *testing.T) {
	resolveUser, resolveGroup := defaultResolve()
	api := &fakeAPI{
		resolveUser:  resolveUser,
		resolveGroup: resolveGroup,
		sendGroup:    func(groupID, text string) (ipc.SendResult, error) { return okSend("NWT-GW-API-GROUP") },
	}
	mux := NewMux(api)
	if code, _ := doJSON(t, mux, "POST", "/send/group",
		`{"name":"研发群","text":"hi"}`); code != 200 {
		t.Errorf("按名称发送应 200，实际 %d", code)
	}
	if !reflect.DeepEqual(api.groupCalls, []struct{ id, text string }{{"g-1", "hi"}}) {
		t.Errorf("群名应解析为 ID 后发送: %+v", api.groupCalls)
	}
	if code, _ := doJSON(t, mux, "POST", "/send/group",
		`{"name":"不存在","text":"hi"}`); code != 404 {
		t.Errorf("群名未命中应 404，实际 %d", code)
	}
}

// TestSendErrorMapping 验证发送错误的三类映射：客户端不可用 503、
// 预约日志前的校验失败 400、已预约后的调用失败 502。
func TestSendErrorMapping(t *testing.T) {
	resolveUser, resolveGroup := defaultResolve()
	newAPI := func(send func(string, string) (ipc.SendResult, error)) *fakeAPI {
		return &fakeAPI{resolveUser: resolveUser, resolveGroup: resolveGroup, sendUser: send}
	}
	unavailable := newAPI(func(string, string) (ipc.SendResult, error) {
		return ipc.SendResult{}, fmt.Errorf("%w: 找不到窗口", agent.ErrClientUnavailable)
	})
	if code, _ := doJSON(t, NewMux(unavailable), "POST", "/send/user", `{"user_id":"u-1","text":"hi"}`); code != 503 {
		t.Errorf("客户端不可用应 503，实际 %d", code)
	}
	invalid := newAPI(func(string, string) (ipc.SendResult, error) {
		return ipc.SendResult{}, errors.New("群ID必须是32个十六进制字符；未发送")
	})
	if code, _ := doJSON(t, NewMux(invalid), "POST", "/send/user", `{"user_id":"u-1","text":"hi"}`); code != 400 {
		t.Errorf("预约前校验失败应 400，实际 %d", code)
	}
	failed := newAPI(func(string, string) (ipc.SendResult, error) {
		return ipc.SendResult{AttemptID: "NWT-GW-API-FAIL", AttemptReserved: true}, errors.New("Win32调用未成功完成")
	})
	if code, body := doJSON(t, NewMux(failed), "POST", "/send/user", `{"user_id":"u-1","text":"hi"}`); code != 502 || body["attempt_id"] != "NWT-GW-API-FAIL" {
		t.Errorf("预约后失败应 502 并携带调用事实，实际 %d %v", code, body)
	}
}

// TestRoutingRules 验证方法不符 405、未知路径 404。
func TestRoutingRules(t *testing.T) {
	resolveUser, resolveGroup := defaultResolve()
	mux := NewMux(&fakeAPI{resolveUser: resolveUser, resolveGroup: resolveGroup})
	if code, _ := doJSON(t, mux, "POST", "/contacts", ""); code != 405 {
		t.Errorf("错误方法应 405，实际 %d", code)
	}
	if code, _ := doJSON(t, mux, "GET", "/unknown", ""); code != 404 {
		t.Errorf("未知路径应 404，实际 %d", code)
	}
}

// TestConcurrentWriteMapping 验证读后复核错误以 503 返回，提示重试。
func TestConcurrentWriteMapping(t *testing.T) {
	_, resolveGroup := defaultResolve()
	busy := func(string) (contacts.Contact, error) {
		return contacts.Contact{}, fmt.Errorf("%w: nr 在读取期间被修改", agent.ErrConcurrentWrite)
	}
	mux := NewMux(&fakeAPI{resolveUser: busy, resolveGroup: resolveGroup})
	if code, body := doJSON(t, mux, "GET", "/contacts?name=Alice", ""); code != 503 || body["error"] == nil {
		t.Errorf("写事务竞态应 503，实际 %d %v", code, body)
	}
	sendMux := NewMux(&fakeAPI{resolveUser: busy, resolveGroup: resolveGroup,
		sendUser: func(string, string) (ipc.SendResult, error) { return okSend("x") }})
	if code, _ := doJSON(t, sendMux, "POST", "/send/user", `{"name":"Alice","text":"hi"}`); code != 503 {
		t.Errorf("按名发送遇写事务竞态应 503，实际 %d", code)
	}
}

// TestDocsRoutes 验证内置文档页与根路径重定向。
func TestDocsRoutes(t *testing.T) {
	resolveUser, resolveGroup := defaultResolve()
	mux := NewMux(&fakeAPI{resolveUser: resolveUser, resolveGroup: resolveGroup})
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, httptest.NewRequest("GET", "/docs", nil))
	if recorder.Code != 200 || !strings.Contains(recorder.Header().Get("Content-Type"), "text/html") {
		t.Fatalf("文档页应 200 text/html，实际 %d %s", recorder.Code, recorder.Header().Get("Content-Type"))
	}
	for _, want := range []string{"/send/user", "/send/group", "/contacts", "/groups", "/health", "试一下"} {
		if !strings.Contains(recorder.Body.String(), want) {
			t.Errorf("文档页缺少内容 %q", want)
		}
	}
	redirect := httptest.NewRecorder()
	mux.ServeHTTP(redirect, httptest.NewRequest("GET", "/", nil))
	if redirect.Code != 302 || redirect.Header().Get("Location") != "/docs" {
		t.Errorf("根路径应 302 到 /docs，实际 %d %s", redirect.Code, redirect.Header().Get("Location"))
	}
}
