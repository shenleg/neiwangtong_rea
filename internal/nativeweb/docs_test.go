// 本文件验证内嵌Swagger/OpenAPI资源与实际HTTP路由一致，所有规范引用留在本地且不定义鉴权。
package nativeweb

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/netip"
	"strings"
	"testing"
)

// TestOpenAPIRoutesAndLocalReferences 验证规范覆盖所有接口，仅定义子网发现且本地引用有效，公开发送不要求调用方request_id或鉴权。
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
	schemas := components["schemas"].(map[string]any)
	modes := schemas["DiscoveryMode"].(map[string]any)["enum"].([]any)
	if len(modes) != 1 || modes[0] != "subnet" {
		t.Fatalf("Web只支持子网发现，实际枚举：%v", modes)
	}
	stateSchema := schemas["State"].(map[string]any)
	stateProperties := stateSchema["properties"].(map[string]any)
	remote := stateProperties["remote"].(map[string]any)
	if _, err := netip.ParsePrefix(remote["example"].(string)); err != nil {
		t.Fatalf("remote示例必须是发现子网CIDR：%v", err)
	}
	if !strings.Contains(remote["description"].(string), "子网 CIDR") {
		t.Fatal("remote应说明发现子网CIDR，而非单对端地址")
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

// TestGroupRecipientsSchemaAndExamples 验证逐成员字段可省略、两种响应均声明该字段，群成功和部分失败示例保留聚合事实。
func TestGroupRecipientsSchemaAndExamples(t *testing.T) {
	data, err := assets.ReadFile("assets/docs/openapi.json")
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err := json.Unmarshal(data, &document); err != nil {
		t.Fatal(err)
	}
	schemas := document["components"].(map[string]any)["schemas"].(map[string]any)
	for _, name := range []string{"ChatResult", "SendResponse"} {
		schema := schemas[name].(map[string]any)
		for _, required := range schema["required"].([]any) {
			if required == "recipients" {
				t.Errorf("%s的预检查失败必须允许省略recipients", name)
			}
		}
		recipients := schema["properties"].(map[string]any)["recipients"].(map[string]any)
		if recipients["items"].(map[string]any)["$ref"] != "#/components/schemas/RecipientResult" {
			t.Errorf("%s缺少逐成员结果定义", name)
		}
	}
	recipientProperties := schemas["RecipientResult"].(map[string]any)["properties"].(map[string]any)
	for _, field := range []string{"user_id", "peer_index", "skipped", "submitted", "transport_acknowledged", "error"} {
		if _, ok := recipientProperties[field]; !ok {
			t.Errorf("缺少逐成员事实字段：%s", field)
		}
	}
	paths := document["paths"].(map[string]any)
	for _, path := range []string{"/send/group", "/api/messages/group"} {
		responses := paths[path].(map[string]any)["post"].(map[string]any)["responses"].(map[string]any)
		for _, status := range []string{"200", "502"} {
			example := responses[status].(map[string]any)["content"].(map[string]any)["application/json"].(map[string]any)["example"].(map[string]any)
			if path == "/api/messages/group" {
				example = example["result"].(map[string]any)
			}
			if example["command"] != float64(3011) || example["submitted"] != true || example["transport_acknowledged"] != false || example["delivery"] != "unknown" || example["business_acknowledged"] != false || example["receipt_received"] != false {
				t.Fatalf("%s %s群聚合示例错误：%v", path, status, example)
			}
			recipients := example["recipients"].([]any)
			if len(recipients) != 2 || recipients[0].(map[string]any)["transport_acknowledged"] != true || recipients[0].(map[string]any)["skipped"] != false || recipients[1].(map[string]any)["skipped"] != (status == "200") {
				t.Fatalf("%s %s未区分离线跳过与实际路由失败：%v", path, status, recipients)
			}
			if status == "200" && (recipients[1].(map[string]any)["submitted"] != false || recipients[1].(map[string]any)["error"] == "") {
				t.Fatalf("%s离线成员被伪造为已提交：%v", path, recipients)
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

// TestChatAssetsUseSubnetDiscovery 验证页面展示子网发现及在线成员投递规则，不再要求全员就绪或保留单对端模式。
func TestChatAssetsUseSubnetDiscovery(t *testing.T) {
	for _, path := range []string{"assets/index.html", "assets/app.js", "assets/app.css", "assets/docs/openapi.json"} {
		data, err := assets.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		for _, obsolete := range []string{"单对端", "single_peer", "single-peer", "指定对端"} {
			if strings.Contains(string(data), obsolete) {
				t.Errorf("%s仍包含已移除模式：%s", path, obsolete)
			}
		}
		if path == "assets/index.html" {
			for _, required := range []string{"局域网发现 · 0 人在线", "发现子网", "支持多对端联系人、单聊和多成员群发送", "向在线就绪成员投递", "未投递", "不离线存储、不自动补发"} {
				if !strings.Contains(string(data), required) {
					t.Errorf("页面缺少子网发现说明：%s", required)
				}
			}
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
