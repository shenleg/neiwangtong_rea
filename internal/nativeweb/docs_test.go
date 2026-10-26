// 本文件验证内嵌Swagger/OpenAPI资源与实际HTTP路由一致，所有规范引用留在本地且不定义鉴权。
package nativeweb

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

// TestOpenAPIRoutesAndLocalReferences 验证规范覆盖所有公开/网页接口，本地引用有效，发送契约不要求调用方request_id或鉴权。
func TestOpenAPIRoutesAndLocalReferences(t *testing.T) {
	manager, _ := testManager(t)
	handler := testHandler(t, manager)
	response := performRequest(handler, http.MethodGet, "/openapi.json", "")
	var document map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &document); err != nil {
		t.Fatal(err)
	}
	if document["openapi"] != "3.0.3" {
		t.Fatal("不是OpenAPI 3规范")
	}
	if _, ok := document["security"]; ok {
		t.Fatal("初版不应定义鉴权")
	}
	components := document["components"].(map[string]any)
	if _, ok := components["securitySchemes"]; ok {
		t.Fatal("初版不应定义鉴权方案")
	}
	paths := document["paths"].(map[string]any)
	for _, path := range []string{"/health", "/contacts", "/groups", "/send/user", "/send/group", "/api/state", "/api/contacts", "/api/groups", "/api/session/start", "/api/session/stop", "/api/messages/user", "/api/messages/group"} {
		if _, ok := paths[path]; !ok {
			t.Errorf("缺少文档路径%s", path)
		}
	}
	checkLocalRefs(t, document, document)
	for path, methods := range paths {
		for method := range methods.(map[string]any) {
			if method != "get" {
				continue
			}
			response := performRequest(handler, http.MethodGet, path, "")
			if response.Code == http.StatusNotFound || response.Code == http.StatusMethodNotAllowed {
				t.Errorf("文档GET路径未注册：%s", path)
			}
		}
	}
}

// checkLocalRefs 遍历规范并检查每个引用能在root中找到，非本地或缺失引用使测试失败；无I/O或网络操作。
func checkLocalRefs(t *testing.T, root map[string]any, value any) {
	t.Helper()
	switch value := value.(type) {
	case map[string]any:
		if reference, ok := value["$ref"].(string); ok {
			if !strings.HasPrefix(reference, "#/") {
				t.Fatalf("不允许外部规范引用：%s", reference)
			}
			var target any = root
			for _, part := range strings.Split(strings.TrimPrefix(reference, "#/"), "/") {
				next, ok := target.(map[string]any)[part]
				if !ok {
					t.Fatalf("规范引用未命中：%s", reference)
				}
				target = next
			}
		}
		for _, child := range value {
			checkLocalRefs(t, root, child)
		}
	case []any:
		for _, child := range value {
			checkLocalRefs(t, root, child)
		}
	}
}

// TestSwaggerAssetsAreEmbedded 验证官方脚本/样式随二进制提供且校验值未变，文档与聊天页面采用各自需要的CSP。
func TestSwaggerAssetsAreEmbedded(t *testing.T) {
	manager, _ := testManager(t)
	handler := testHandler(t, manager)
	for _, tc := range []struct{ path, contentType string }{
		{"/docs", "text/html"}, {"/swagger/index.html", "text/html"}, {"/docs/init.js", "text/javascript"},
		{"/docs/style.css", "text/css"}, {"/swagger-ui/swagger-ui-bundle.js", "text/javascript"}, {"/swagger-ui/swagger-ui.css", "text/css"},
	} {
		response := performRequest(handler, http.MethodGet, tc.path, "")
		if response.Code != http.StatusOK || !strings.Contains(response.Header().Get("Content-Type"), tc.contentType) || response.Body.Len() == 0 {
			t.Fatalf("文档资产不可用：%s %d", tc.path, response.Code)
		}
		if response.Header().Get("WWW-Authenticate") != "" {
			t.Fatal("文档不应要求鉴权")
		}
	}
	for name, want := range map[string]string{
		"swagger-ui-bundle.js": "3f6abf70480db7e07914df4c98a77385f22ea585eaf457a3e6415ba0eae2d167",
		"swagger-ui.css":       "c3cfe8248c4b0b89673600fb829924eafbac6de4bc4b2b06f5f5243483074d0a",
	} {
		data, err := assets.ReadFile("assets/swagger/" + name)
		if err != nil {
			t.Fatal(err)
		}
		if actual := fmt.Sprintf("%x", sha256.Sum256(data)); actual != want {
			t.Fatalf("官方Swagger资产被修改：%s", name)
		}
	}
	page := performRequest(handler, http.MethodGet, "/docs", "")
	if !strings.Contains(page.Body.String(), "Swagger UI") || strings.Contains(page.Body.String(), "https://") || strings.Contains(page.Body.String(), "<script>") {
		t.Fatal("文档不应加载外部资源或内联脚本")
	}
	if !strings.Contains(page.Header().Get("Content-Security-Policy"), "style-src 'self' 'unsafe-inline'") {
		t.Fatal("Swagger样式策略不正确")
	}
	chat := performRequest(handler, http.MethodGet, "/", "")
	if strings.Contains(chat.Header().Get("Content-Security-Policy"), "unsafe-inline") {
		t.Fatal("文档所需策略不应放宽聊天页面")
	}
}
