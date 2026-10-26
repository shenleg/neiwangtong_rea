// 本文件按显式环境开关启动真实Chrome，针对Go合成后台验收Swagger；默认跳过，不连接真实内网通或读取local资料。
package nativeweb

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// TestSwaggerBrowserInteraction 验证真实Swagger的无鉴权与Try it out，只有NWT_SWAGGER_BROWSER=1时执行，缺驱动或交互失败即报错。
func TestSwaggerBrowserInteraction(t *testing.T) {
	if os.Getenv("NWT_SWAGGER_BROWSER") != "1" {
		t.Skip("未显式启用Chrome验收；普通测试不要求浏览器或Node")
	}
	manager, fake, _ := startPublicManager(t)
	// 浏览器还会主动下线再按新名称上线，合成会话每次重新发布在线，不创建真实socket。
	manager.factory = func(Config, string, *journal) (session, error) {
		fake.mu.Lock()
		fake.snapshot.Online = true
		fake.mu.Unlock()
		return fake, nil
	}
	server := httptest.NewUnstartedServer(http.NotFoundHandler())
	handler, err := NewHandler(manager, netip.MustParseAddrPort(server.Listener.Addr().String()))
	if err != nil {
		t.Fatal(err)
	}
	server.Config.Handler = handler
	server.Start()
	defer server.Close()
	script, err := filepath.Abs("../../tools/nwtweb-api-browser-check.mjs")
	if err != nil {
		t.Fatal(err)
	}
	output := os.Getenv("NWT_BROWSER_OUTPUT")
	if output == "" {
		output = t.TempDir()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "node", script, server.URL, output)
	result, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("Swagger真实浏览器验收失败：%v\n%s", err, result)
	}
	t.Log(string(result))
	if fake.calls.Load() != 2 {
		t.Fatalf("Swagger应只提交明确执行的两条合成请求，实际%d", fake.calls.Load())
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if fake.requests[0].UserID != "synthetic-peer" || fake.requests[0].Text != "Swagger 合成单聊\n第二行🙂" || fake.requests[1].GroupID != "group-two" || fake.requests[1].Text != "Swagger 合成群聊\r\n第二行" {
		t.Fatalf("Swagger发送目标或正文被改动：%+v", fake.requests)
	}
}
