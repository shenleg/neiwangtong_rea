// 本文件离线验证HTTP输入校验、同源限制和网页资源，不向真实客户端发送消息。
package nativeweb

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
)

// testHandler 以固定回环地址创建合成管理器的处理器，构造错误立即终止；不绑定socket。
func testHandler(t *testing.T, manager *Manager) http.Handler {
	t.Helper()
	handler, err := NewHandler(manager, netip.MustParseAddrPort("127.0.0.1:8788"))
	if err != nil {
		t.Fatal(err)
	}
	return handler
}

// performRequest 发送一条具有正常同源请求头的内存HTTP请求，返回完整响应；无外部I/O或错误。
func performRequest(handler http.Handler, method, path, body string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, "http://127.0.0.1:8788"+path, strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Origin", "http://127.0.0.1:8788")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

// TestHTTPRoutesAndSingleSubmission 验证网页查询、上线/下线及两类消息通过管理器，不把传输ACK显示成送达。
func TestHTTPRoutesAndSingleSubmission(t *testing.T) {
	manager, fake := testManager(t)
	handler := testHandler(t, manager)
	for _, path := range []string{"/", "/app.js", "/app.css", "/api/state", "/api/contacts", "/api/groups"} {
		response := performRequest(handler, http.MethodGet, path, "")
		if response.Code != http.StatusOK || response.Header().Get("Content-Security-Policy") == "" || response.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("%s: %d %s", path, response.Code, response.Body.String())
		}
	}
	if response := performRequest(handler, http.MethodPost, "/api/session/start", `{}`); response.Code != http.StatusAccepted {
		t.Fatalf("上线：%d %s", response.Code, response.Body.String())
	}
	awaitPhase(t, manager, "online")
	for _, tc := range []struct{ kind, body string }{
		{"user", `{"request_id":"http-user-1","user_id":"synthetic-peer","text":"合成正文\n第二行"}`},
		{"group", `{"request_id":"http-group-1","group_id":"group-two","text":"合成群消息"}`},
	} {
		response := performRequest(handler, http.MethodPost, "/api/messages/"+tc.kind, tc.body)
		var result MessageResponse
		if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		if response.Code != http.StatusOK || !result.Result.Submitted || result.Result.Delivery != "unknown" {
			t.Fatalf("发送结果错误：%d %+v", response.Code, result)
		}
	}
	if fake.calls.Load() != 2 {
		t.Fatal("未各发送一次")
	}
	response := performRequest(handler, http.MethodGet, "/api/groups", "")
	if !strings.Contains(response.Body.String(), `"complete":false`) {
		t.Fatal("群列表不得声称穷尽")
	}
	if response := performRequest(handler, http.MethodPost, "/api/session/stop", `{}`); response.Code != http.StatusAccepted {
		t.Fatal(response.Body.String())
	}
	awaitPhase(t, manager, "offline")
}

// TestHTTPRejectsInvalidInputBeforeSending 验证重复字段、尾随数据、类型/UTF-8错误和超长正文在进入协议前被拒绝。
func TestHTTPRejectsInvalidInputBeforeSending(t *testing.T) {
	manager, fake := testManager(t)
	handler := testHandler(t, manager)
	for name, body := range map[string]string{
		"empty": "", "null": "null", "array": "[]", "missing": `{"request_id":"request-1","user_id":"synthetic-peer"}`,
		"unknown":      `{"request_id":"request-1","user_id":"synthetic-peer","text":"正文","extra":1}`,
		"duplicate":    `{"request_id":"request-1","user_id":"synthetic-peer","text":"正文","text":"另一个"}`,
		"number":       `{"request_id":"request-1","user_id":"synthetic-peer","text":123}`,
		"null_text":    `{"request_id":"request-1","user_id":"synthetic-peer","text":null}`,
		"trailing":     `{"request_id":"request-1","user_id":"synthetic-peer","text":"正文"} {}`,
		"invalid_utf8": "{\"request_id\":\"request-1\",\"user_id\":\"synthetic-peer\",\"text\":\"\xff\"}",
		"oversized":    strings.Repeat("x", 30000),
		"long_text":    `{"request_id":"request-1","user_id":"synthetic-peer","text":"` + strings.Repeat("x", 4097) + `"}`,
	} {
		t.Run(name, func(t *testing.T) {
			response := performRequest(handler, http.MethodPost, "/api/messages/user", body)
			if response.Code != http.StatusBadRequest && response.Code != http.StatusUnprocessableEntity {
				t.Fatalf("坏输入未拒绝：%d %s", response.Code, response.Body.String())
			}
		})
	}
	if fake.calls.Load() != 0 {
		t.Fatal("坏输入触发发送")
	}
	for _, path := range []string{"/api/session/start", "/api/session/stop"} {
		if response := performRequest(handler, http.MethodPost, path, `{"extra":"value"}`); response.Code != http.StatusBadRequest {
			t.Fatal("生命周期接口不应忽略未知字段")
		}
	}
}

// TestHTTPSameOriginProtection 验证无鉴权接口仍拒绝DNS重绑定Host、跨站Origin及简单表单写入。
func TestHTTPSameOriginProtection(t *testing.T) {
	manager, fake := testManager(t)
	handler := testHandler(t, manager)
	for _, tc := range []struct {
		name, host, origin, site, contentType string
		want                                  int
	}{
		{"host", "attacker.test:8788", "", "", "application/json", 403},
		{"origin", "127.0.0.1:8788", "https://attacker.test", "", "application/json", 403},
		{"null_origin", "127.0.0.1:8788", "null", "", "application/json", 403},
		{"fetch_site", "127.0.0.1:8788", "", "cross-site", "application/json", 403},
		{"form", "127.0.0.1:8788", "", "", "application/x-www-form-urlencoded", 415},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, "http://"+tc.host+"/api/session/start", strings.NewReader(`{}`))
			request.Header.Set("Origin", tc.origin)
			request.Header.Set("Sec-Fetch-Site", tc.site)
			request.Header.Set("Content-Type", tc.contentType)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != tc.want {
				t.Fatalf("%d %s", response.Code, response.Body.String())
			}
		})
	}
	if manager.State().Phase != "offline" || fake.calls.Load() != 0 {
		t.Fatal("跨站请求改变后台状态")
	}
	if _, err := NewHandler(manager, netip.MustParseAddrPort("0.0.0.0:8788")); err == nil {
		t.Fatal("不允许无鉴权暴露到所有网卡")
	}
}

// TestLANHasNoAuthentication 验证私有网卡的页面、Swagger和API无需凭据，忽略过期Authorization且保留跨站限制。
func TestLANHasNoAuthentication(t *testing.T) {
	manager, fake := testManager(t)
	address := netip.MustParseAddrPort("192.168.50.10:8788")
	handler, err := NewHandler(manager, address)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/", "/app.js", "/api/state", "/contacts", "/groups", "/docs", "/openapi.json"} {
		for _, authorization := range []string{"", "Basic expired-credentials", "Bearer ignored-token"} {
			request := httptest.NewRequest(http.MethodGet, "http://"+address.String()+path, nil)
			request.Header.Set("Authorization", authorization)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != http.StatusOK || response.Header().Get("WWW-Authenticate") != "" {
				t.Fatalf("%s仍要求鉴权：%d", path, response.Code)
			}
		}
	}
	request := httptest.NewRequest(http.MethodPost, "http://"+address.String()+"/api/session/stop", strings.NewReader(`{}`))
	request.Header.Set("Content-Type", "application/json; charset=UTF-8")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusAccepted {
		t.Fatalf("无凭据、无自定义请求头的JSON调用失败：%d %s", response.Code, response.Body.String())
	}
	request = httptest.NewRequest(http.MethodPost, "http://"+address.String()+"/api/session/start", strings.NewReader(`{}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Origin", "http://attacker.test")
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatal("取消鉴权不应开放跨站调用")
	}
	for _, invalid := range []netip.AddrPort{netip.MustParseAddrPort("0.0.0.0:8788"), netip.MustParseAddrPort("8.8.8.8:8788")} {
		if _, err := NewHandler(manager, invalid); err == nil {
			t.Fatal("仍需明确绑定回环或私有地址")
		}
	}
	if manager.State().Phase != "offline" || fake.calls.Load() != 0 {
		t.Fatal("无鉴权检查不应触发协议连接或发送")
	}
}

// TestHTTPListenerClosesWithoutStartingSession 验证真实本地HTTP能提供页面且未点击上线不创建协议记录，关闭后端口不再可用。
func TestHTTPListenerClosesWithoutStartingSession(t *testing.T) {
	manager, _ := testManager(t)
	server := httptest.NewUnstartedServer(http.NotFoundHandler())
	address := server.Listener.Addr().String()
	handler, err := NewHandler(manager, netip.MustParseAddrPort(address))
	if err != nil {
		t.Fatal(err)
	}
	server.Config.Handler = handler
	server.Start()
	response, err := server.Client().Get(server.URL + "/api/state")
	if err != nil {
		server.Close()
		t.Fatal(err)
	}
	_, readErr := io.Copy(io.Discard, response.Body)
	closeErr := response.Body.Close()
	server.Close()
	if readErr != nil || closeErr != nil || response.StatusCode != http.StatusOK || manager.State().Phase != "offline" {
		t.Fatalf("本地HTTP异常：%v %v %d", readErr, closeErr, response.StatusCode)
	}
	if _, err := http.Get("http://" + address + "/api/state"); err == nil {
		t.Fatal("HTTP端口没有关闭")
	}
	if err := manager.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}
