// 本文件验证认证持久化、Cookie、API密钥及跨站请求防护。
package nativeweb

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// authRequest 创建不联网的同源认证请求，可附带 Cookie 或 Bearer。
func authRequest(h http.Handler, method, path, body, origin string, cookie *http.Cookie, bearer string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, "http://127.0.0.1:8788"+path, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	if origin != "" {
		r.Header.Set("Origin", origin)
	}
	if cookie != nil {
		r.AddCookie(cookie)
	}
	if bearer != "" {
		r.Header.Set("Authorization", bearer)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

// TestAuthLifecycle 验证首次初始化、重启、撤销和凭据损坏时禁止访问。
func TestAuthLifecycle(t *testing.T) {
	dir := t.TempDir()
	var output bytes.Buffer
	a, err := newAuth(dir, &output)
	if err != nil {
		t.Fatal(err)
	}
	code := strings.TrimSpace(strings.TrimPrefix(output.String(), "初始化码："))
	if len(code) != 43 {
		t.Fatal("初始化码无效")
	}
	h := http.HandlerFunc(a.serve)
	origin := "http://127.0.0.1:8788"
	w := authRequest(h, "POST", "/auth/setup", `{"code":"wrong","password":"password123"}`, origin, nil, "")
	if w.Code != 401 {
		t.Fatal(w.Code)
	}
	body, _ := json.Marshal(map[string]string{"code": code, "password": "password123"})
	w = authRequest(h, "POST", "/auth/setup", string(body), origin, nil, "")
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	cookie := w.Result().Cookies()[0]
	if !cookie.HttpOnly || cookie.Secure || cookie.SameSite != http.SameSiteStrictMode {
		t.Fatal(cookie)
	}
	w = authRequest(h, "POST", "/auth/api-key", "{}", origin, cookie, "")
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	var v map[string]string
	if err = json.Unmarshal(w.Body.Bytes(), &v); err != nil {
		t.Fatal(err)
	}
	key := v["api_key"]
	a.mu.Lock()
	ok, _ := a.authenticate(httptest.NewRequest("GET", "/", nil))
	a.mu.Unlock()
	if ok {
		t.Fatal("无凭据通过")
	}
	a2, err := newAuth(dir, &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	h2 := http.HandlerFunc(a2.serve)
	w = authRequest(h2, "GET", "/auth/status", "", "", cookie, "Bearer "+key)
	if !strings.Contains(w.Body.String(), `"authenticated":true`) {
		t.Fatal(w.Body)
	}
	w = authRequest(h2, "GET", "/auth/status", "", "", cookie, "")
	if !strings.Contains(w.Body.String(), `"authenticated":false`) {
		t.Fatal(w.Body)
	}
	w = authRequest(h2, "POST", "/auth/login", `{"password":"password123"}`, origin, nil, "")
	if w.Code != 200 {
		t.Fatal(w.Code)
	}
	cookie = w.Result().Cookies()[0]
	w = authRequest(h2, "DELETE", "/auth/api-key", "{}", origin, cookie, "")
	if w.Code != 200 {
		t.Fatal(w.Code)
	}
	w = authRequest(h2, "GET", "/auth/status", "", "", nil, "Bearer "+key)
	if !strings.Contains(w.Body.String(), `"authenticated":false`) {
		t.Fatal(w.Body)
	}
	w = authRequest(h2, "POST", "/auth/logout", "{}", origin, cookie, "")
	if w.Code != 200 {
		t.Fatal(w.Code)
	}
	w = authRequest(h2, "GET", "/auth/status", "", "", cookie, "")
	if !strings.Contains(w.Body.String(), `"authenticated":false`) {
		t.Fatal(w.Body)
	}
	if err = os.Rename(filepath.Join(dir, "auth.json"), filepath.Join(dir, "lost")); err != nil {
		t.Fatal(err)
	}
	w = authRequest(h2, "GET", "/auth/status", "", "", nil, "")
	if w.Code != 500 {
		t.Fatal(w.Code)
	}
	if _, err = newAuth(dir, &bytes.Buffer{}); err == nil {
		t.Fatal("丢失凭据后重新初始化")
	}
}

// TestAuthPasswordRuleHint 验证认证页面展示密码字节长度规则并将提示关联到输入框。
func TestAuthPasswordRuleHint(t *testing.T) {
	manager, _ := testManager(t)
	h, err := NewHandler(manager, netip.MustParseAddrPort("127.0.0.1:8788"), &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	w := authRequest(h, "GET", "/", "", "", nil, "")
	if w.Code != http.StatusOK {
		t.Fatalf("认证页面返回 %d", w.Code)
	}
	for _, text := range []string{`aria-describedby="auth-password-hint"`, `id="auth-password-hint"`, "8～1024 个 UTF-8 字节", "不要求大小写、数字或特殊符号"} {
		if !strings.Contains(w.Body.String(), text) {
			t.Errorf("认证页面缺少密码规则提示 %q", text)
		}
	}
}

// TestAuthProtection 验证静态认证资源公开，而敏感路由和跨站写入被拒绝。
func TestAuthProtection(t *testing.T) {
	manager, _ := testManager(t)
	var out bytes.Buffer
	h, err := NewHandler(manager, netip.MustParseAddrPort("127.0.0.1:8788"), &out)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"/health", "/api/state", "/docs", "/openapi.json"} {
		if w := authRequest(h, "GET", p, "", "", nil, ""); w.Code != 401 {
			t.Fatalf("%s: %d", p, w.Code)
		}
	}
	if w := authRequest(h, "GET", "/", "", "", nil, ""); w.Code != 200 {
		t.Fatal(w.Code)
	}
	code := strings.TrimSpace(strings.TrimPrefix(out.String(), "初始化码："))
	b, _ := json.Marshal(map[string]string{"code": code, "password": "password123"})
	w := authRequest(h, "POST", "/auth/setup", string(b), "", nil, "")
	if w.Code != 403 {
		t.Fatal(w.Code)
	}
	w = authRequest(h, "POST", "/auth/setup", string(b), "http://127.0.0.1:8788", nil, "")
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	c := w.Result().Cookies()[0]
	w = authRequest(h, "POST", "/api/session/stop", "{}", "", c, "")
	if w.Code != 403 {
		t.Fatal(w.Code)
	}
}
