// 本文件验证 API 服务的启动/停止生命周期与真实监听下的健康检查。
package api

import (
	"errors"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"neiwangtong-gateway/internal/ipc"
)

// TestServerLifecycle 验证启动、重复启动拒绝、健康检查可达与停止。
func TestServerLifecycle(t *testing.T) {
	server := NewServer("127.0.0.1:0", &fakeAPI{preflightErr: nil})
	if server.Running() {
		t.Fatal("未启动不应处于运行状态")
	}
	if err := server.Start(); err != nil {
		t.Fatal(err)
	}
	defer server.Stop()
	if !server.Running() || server.Addr() == "" {
		t.Fatalf("启动后应运行且有监听地址，实际 running=%v addr=%q", server.Running(), server.Addr())
	}
	if err := server.Start(); !errors.Is(err, ErrAlreadyRunning) {
		t.Fatalf("重复启动应返回 ErrAlreadyRunning，实际 %v", err)
	}
	response, err := http.Get("http://" + server.Addr() + "/health")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("健康检查状态 %d，预期 200", response.StatusCode)
	}
	if err := server.Stop(); err != nil {
		t.Fatal(err)
	}
	if server.Running() {
		t.Fatal("停止后不应处于运行状态")
	}
	// 停止后端口不再接受连接；允许短暂竞态，轮询确认。
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := http.Get("http://" + server.Addr() + "/health"); err != nil {
			return // 连接失败即端口已关闭
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("停止后端口仍可访问")
}

// TestServerSetAddr 验证运行中切换监听地址（回环↔通配）平滑生效且端口不变，
// 停止状态下 SetAddr 只记录地址、下次 Start 才生效——对应托盘外网监听勾选项。
func TestServerSetAddr(t *testing.T) {
	server := NewServer("127.0.0.1:0", &fakeAPI{})
	if err := server.Start(); err != nil {
		t.Fatal(err)
	}
	defer server.Stop()
	_, port, err := net.SplitHostPort(server.Addr())
	if err != nil {
		t.Fatal(err)
	}
	if err := server.SetAddr("0.0.0.0:" + port); err != nil {
		t.Fatal(err)
	}
	if !server.Running() {
		t.Fatal("切换地址后应保持运行")
	}
	// Windows 双栈下 0.0.0.0 监听回报 [::]，两者都算通配。
	if host, _, _ := net.SplitHostPort(server.Addr()); host != "0.0.0.0" && host != "::" {
		t.Fatalf("切换后监听 %s，预期通配地址", server.Addr())
	}
	// 通配监听下回环访问仍可达。
	response, err := http.Get("http://127.0.0.1:" + port + "/health")
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("通配监听健康检查状态 %d，预期 200", response.StatusCode)
	}
	if err := server.SetAddr("127.0.0.1:" + port); err != nil {
		t.Fatal(err)
	}
	if host, _, _ := net.SplitHostPort(server.Addr()); host != "127.0.0.1" {
		t.Fatalf("切回后监听 %s，预期 127.0.0.1", server.Addr())
	}
	// 停止状态下换地址：SetAddr 不启动，Start 后按新地址监听。
	if err := server.Stop(); err != nil {
		t.Fatal(err)
	}
	if err := server.SetAddr("127.0.0.1:0"); err != nil {
		t.Fatal(err)
	}
	if server.Running() {
		t.Fatal("停止状态下 SetAddr 不应启动服务")
	}
	if err := server.Start(); err != nil {
		t.Fatal(err)
	}
	if host, _, _ := net.SplitHostPort(server.Addr()); host != "127.0.0.1" {
		t.Fatalf("重新启动监听 %s，预期 127.0.0.1", server.Addr())
	}
}

// TestServerStopWhenIdle 验证未运行时 Stop 是空操作。
func TestServerStopWhenIdle(t *testing.T) {
	if err := NewServer("127.0.0.1:0", &fakeAPI{}).Stop(); err != nil {
		t.Fatalf("空停止应无错误，实际 %v", err)
	}
}

// TestServerServesMuxRoutes 验证真实监听下发送端点的错误映射仍然生效。
func TestServerServesMuxRoutes(t *testing.T) {
	resolveUser, resolveGroup := defaultResolve()
	server := NewServer("127.0.0.1:0", &fakeAPI{
		resolveUser:  resolveUser,
		resolveGroup: resolveGroup,
		sendUser:     func(userID, text string) (ipc.SendResult, error) { return okSend("NWT-GW-SRV") },
	})
	if err := server.Start(); err != nil {
		t.Fatal(err)
	}
	defer server.Stop()
	response, err := http.Post("http://"+server.Addr()+"/send/user", "application/json",
		strings.NewReader(`{"user_id":"u-1","text":"hi"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("发送状态 %d，预期 200", response.StatusCode)
	}
}
