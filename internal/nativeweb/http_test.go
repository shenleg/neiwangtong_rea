// 本文件离线验证HTTP输入校验、同源限制和网页资源，不向真实客户端发送消息。
package nativeweb

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
)

// authenticatedHandler 为测试请求附上通过真实认证流程获得的会话 cookie。
type authenticatedHandler struct {
	handler http.Handler
	cookie  *http.Cookie
}

func (h *authenticatedHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	r.AddCookie(h.cookie)
	h.handler.ServeHTTP(w, r)
}

// newAuthenticatedHandler 创建指定地址的处理器，初始化或登录并返回携带会话的处理器；失败时终止测试。
func newAuthenticatedHandler(t *testing.T, manager *Manager, address netip.AddrPort) *authenticatedHandler {
	t.Helper()
	var output bytes.Buffer
	handler, err := NewHandler(manager, address, &output)
	if err != nil {
		t.Fatal(err)
	}
	const password = "test-password-12345"
	path := "/auth/setup"
	body := map[string]string{"password": password}
	if output.Len() == 0 {
		path = "/auth/login"
	} else {
		code := strings.TrimSpace(strings.TrimPrefix(output.String(), "初始化码："))
		if code == "" || code == strings.TrimSpace(output.String()) {
			t.Fatalf("初始化码输出格式错误：%q", output.String())
		}
		body["code"] = code
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	host := address
	if address.Addr().IsUnspecified() {
		host = netip.AddrPortFrom(netip.MustParseAddr("127.0.0.1"), address.Port())
	}
	request := httptest.NewRequest(http.MethodPost, "http://"+host.String()+path, bytes.NewReader(encoded))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Origin", "http://"+host.String())
	request = request.WithContext(context.WithValue(request.Context(), http.LocalAddrContextKey, &net.TCPAddr{IP: net.ParseIP(host.Addr().String()), Port: int(host.Port())}))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	cookies := response.Result().Cookies()
	if response.Code != http.StatusOK || len(cookies) == 0 {
		t.Fatalf("认证失败：%d %s", response.Code, response.Body.String())
	}
	return &authenticatedHandler{handler: handler, cookie: cookies[0]}
}

// testHandler 创建回环地址的已认证合成处理器，构造或登录失败时终止测试。
func testHandler(t *testing.T, manager *Manager) http.Handler {
	t.Helper()
	return newAuthenticatedHandler(t, manager, netip.MustParseAddrPort("127.0.0.1:8788"))
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

// TestHTTPSameOriginProtection 验证已认证请求仍拒绝DNS重绑定Host、跨站Origin及简单表单写入。
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
}

// TestLANAuthentication 验证私网接口要求认证，同时保留跨站和监听地址限制。
func TestLANAuthentication(t *testing.T) {
	manager, fake := testManager(t)
	address := netip.MustParseAddrPort("192.168.50.10:8788")
	handler := newAuthenticatedHandler(t, manager, address)
	for _, path := range []string{"/api/state", "/contacts", "/groups"} {
		request := httptest.NewRequest(http.MethodGet, "http://"+address.String()+path, nil)
		response := httptest.NewRecorder()
		handler.handler.ServeHTTP(response, request)
		if response.Code != http.StatusUnauthorized {
			t.Fatalf("%s 未拒绝未登录请求：%d", path, response.Code)
		}
		response = httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusOK {
			t.Fatalf("%s 登录后访问失败：%d %s", path, response.Code, response.Body.String())
		}
	}
	request := httptest.NewRequest(http.MethodPost, "http://"+address.String()+"/api/session/start", strings.NewReader(`{}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Origin", "http://attacker.test")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatal("认证后不应开放跨站调用")
	}
	for _, invalid := range []netip.AddrPort{netip.MustParseAddrPort("[::]:8788"), netip.MustParseAddrPort("8.8.8.8:8788")} {
		if _, err := NewHandler(manager, invalid, io.Discard); err == nil {
			t.Fatal("仅支持IPv4回环、私有或通配地址")
		}
	}
	if manager.State().Phase != "offline" || fake.calls.Load() != 0 {
		t.Fatal("认证检查不应触发协议连接或发送")
	}
}

// TestWildcardListenerHostAndOrigin 验证通配监听使用连接实际到达的本机地址检查Host，同源访问多网卡且仍拒绝错误Host或Origin。
func TestWildcardListenerHostAndOrigin(t *testing.T) {
	manager, fake := testManager(t)
	handler := newAuthenticatedHandler(t, manager, netip.MustParseAddrPort("0.0.0.0:8788"))
	for _, tc := range []struct {
		local, host, origin, site string
		want                      int
	}{
		{"10.1.1.30", "10.1.1.30:8788", "http://10.1.1.30:8788", "", 200},
		{"192.168.50.10", "192.168.50.10:8788", "http://192.168.50.10:8788", "", 200},
		{"127.0.0.1", "127.0.0.1:8788", "", "", 200},
		{"127.0.0.1", "localhost:8788", "http://localhost:8788", "", 200},
		{"10.1.1.30", "localhost:8788", "", "", 403},
		{"10.1.1.30", "attacker.test:8788", "", "", 403},
		{"10.1.1.30", "10.1.1.31:8788", "", "", 403},
		{"10.1.1.30", "10.1.1.30:8789", "", "", 403},
		{"10.1.1.30", "0.0.0.0:8788", "", "", 403},
		{"10.1.1.30", "10.1.1.30:8788", "http://attacker.test", "", 403},
		{"10.1.1.30", "10.1.1.30:8788", "null", "", 403},
		{"10.1.1.30", "10.1.1.30:8788", "", "cross-site", 403},
		{"", "10.1.1.30:8788", "", "", 403},
	} {
		request := httptest.NewRequest(http.MethodGet, "http://"+tc.host+"/api/state", nil)
		if tc.local != "" {
			request = request.WithContext(context.WithValue(request.Context(), http.LocalAddrContextKey, &net.TCPAddr{IP: net.ParseIP(tc.local), Port: 8788}))
		}
		request.Header.Set("Origin", tc.origin)
		request.Header.Set("Sec-Fetch-Site", tc.site)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != tc.want {
			t.Fatalf("local=%s host=%s origin=%s site=%s: %d %s", tc.local, tc.host, tc.origin, tc.site, response.Code, response.Body.String())
		}
	}
	if manager.State().Phase != "offline" || fake.calls.Load() != 0 {
		t.Fatal("监听方式不应触发协议上线或发送")
	}
}

// TestWildcardHandlerUsesRealLocalAddress 验证真实HTTP服务提供的LocalAddrContext支持通配Handler，仅通过loopback读取页面及接口。
func TestWildcardHandlerUsesRealLocalAddress(t *testing.T) {
	manager, _ := testManager(t)
	server := httptest.NewUnstartedServer(http.NotFoundHandler())
	defer server.Close()
	bound := server.Listener.Addr().(*net.TCPAddr).AddrPort()
	handler := newAuthenticatedHandler(t, manager, netip.AddrPortFrom(netip.IPv4Unspecified(), bound.Port()))
	server.Config.Handler = handler
	server.Start()
	for _, path := range []string{"/", "/api/state", "/contacts", "/groups", "/docs", "/openapi.json"} {
		response, err := server.Client().Get(server.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		_, readErr := io.Copy(io.Discard, response.Body)
		closeErr := response.Body.Close()
		if readErr != nil || closeErr != nil || response.StatusCode != http.StatusOK {
			t.Fatalf("%s: %d %v %v", path, response.StatusCode, readErr, closeErr)
		}
	}
}

// TestHTTPListenerClosesWithoutStartingSession 验证真实本地HTTP能提供页面且未点击上线不创建协议记录，关闭后端口不再可用。
func TestHTTPListenerClosesWithoutStartingSession(t *testing.T) {
	manager, _ := testManager(t)
	server := httptest.NewUnstartedServer(http.NotFoundHandler())
	address := server.Listener.Addr().String()
	handler := newAuthenticatedHandler(t, manager, netip.MustParseAddrPort(address))
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
