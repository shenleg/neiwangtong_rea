// 本文件验证Web命令默认只启动HTTP、只读校验及参数拒绝；测试不连接真实内网通节点。
package main

import (
	"bytes"
	"context"
	"errors"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// syntheticArgs 返回仅用于测试的明确loopback配置，dir保存临时独立资料；无I/O或错误。
func syntheticArgs(dir string) []string {
	return []string{"-local", "127.0.0.1", "-prefix", "127.0.0.0/8",
		"-name", "Synthetic-Web", "-data-dir", dir, "-listen", "127.0.0.1:0"}
}

// TestCheckCreatesNoFiles 验证-check不创建数据目录、身份或socket，即使同时指定-online也只校验。
func TestCheckCreatesNoFiles(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "not-created")
	args := append(syntheticArgs(dir), "-check", "-online")
	var stdout, stderr bytes.Buffer
	if code := run(context.Background(), args, &stdout, &stderr); code != 0 {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("只读校验创建目录：%v", err)
	}
}

// TestLANCheckRemainsReadonly 验证显式局域网配置可只读校验，不创建身份、目录或连接。
func TestLANCheckRemainsReadonly(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "not-created")
	args := append(syntheticArgs(dir), "-listen", "192.168.50.10:8788", "-check")
	var stdout, stderr bytes.Buffer
	if code := run(context.Background(), args, &stdout, &stderr); code != 0 {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	if strings.Contains(stdout.String(), "本次进程密码") {
		t.Fatal("只读校验不能生成密码")
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("只读校验创建目录：%v", err)
	}
}

// TestWildcardListenerOptions 验证默认允许所有IPv4网卡监听，校验及离线启动均不创建协议资料。
func TestWildcardListenerOptions(t *testing.T) {
	for _, check := range []bool{true, false} {
		dir := filepath.Join(t.TempDir(), "not-created")
		args := append(syntheticArgs(dir), "-listen", "0.0.0.0:0")
		if check {
			args = append(args, "-check", "-online")
		}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		var stdout, stderr bytes.Buffer
		if code := run(ctx, args, &stdout, &stderr); code != 0 {
			t.Fatalf("check=%t code=%d stderr=%s", check, code, stderr.String())
		}
		if !check && (!strings.Contains(stdout.String(), "http://127.0.0.1:") || !strings.Contains(stdout.String(), "HTTP监听：0.0.0.0:")) {
			t.Fatalf("通配监听应展示本机可访问地址与实际监听地址：%s", stdout.String())
		}
		if _, err := os.Stat(dir); !os.IsNotExist(err) {
			t.Fatalf("仅启动离线网页创建了协议资料：%v", err)
		}
	}
}

// TestDefaultSubnetCheck 验证默认子网发现仅需本机网络参数，校验不绑定端口或发广播。
func TestDefaultSubnetCheck(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "not-created")
	args := append(syntheticArgs(dir), "-session-duration", "30s", "-check")
	var stdout, stderr bytes.Buffer
	if code := run(context.Background(), args, &stdout, &stderr); code != 0 {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("只读发现校验创建目录：%v", err)
	}
}

// TestDefaultRemainsOfflineAndShutsDown 验证命令默认只监听网页，取消时退出且未创建任何协议资料。
func TestDefaultRemainsOfflineAndShutsDown(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "not-created")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var stdout, stderr bytes.Buffer
	if code := run(ctx, syntheticArgs(dir), &stdout, &stderr); code != 0 {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "http://127.0.0.1:") || !strings.Contains(stdout.String(), "/openapi.json") || !strings.Contains(stdout.String(), "/docs") || !strings.Contains(stdout.String(), "无鉴权") {
		t.Fatal("缺少网页、Swagger地址或无鉴权说明")
	}
	if strings.Contains(stdout.String(), "本次进程密码") || strings.Contains(stdout.String(), "访问用户名") {
		t.Fatal("初版不应生成或展示鉴权凭据")
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("默认HTTP启动不应创建协议资料：%v", err)
	}
}

// TestParseLocalNetwork 验证CIDR保留本机地址，旧参数仍可用，缺失或冲突的子网明确报错。
func TestParseLocalNetwork(t *testing.T) {
	for _, tc := range []struct {
		local, prefix, wantLocal, wantPrefix string
	}{
		{"192.168.1.50/24", "", "192.168.1.50", "192.168.1.0/24"},
		{"192.168.1.50/24", "192.168.1.0/24", "192.168.1.50", "192.168.1.0/24"},
		{"192.168.1.50/24", "192.168.1.10/24", "192.168.1.50", "192.168.1.0/24"},
		{"192.168.1.50", "192.168.1.0/24", "192.168.1.50", "192.168.1.0/24"},
		{"127.0.0.1/8", "", "127.0.0.1", "127.0.0.0/8"},
		{"", "", "", ""},
		{"192.168.1.50", "", "", ""},
		{"192.168.1.50/33", "", "", ""},
		{"192.168.1.50/24", "192.168.2.0/24", "", ""},
		{"192.168.1.50/24", "192.168.1.0/16", "", ""},
		{"192.168.1.50/24", "invalid", "", ""},
		{"::1/64", "", "", ""},
		{"::1", "::/64", "", ""},
		{"192.168.1.50", "::/64", "", ""},
	} {
		t.Run(tc.local+"_"+tc.prefix, func(t *testing.T) {
			local, prefix, err := parseLocalNetwork(tc.local, tc.prefix)
			if tc.wantLocal == "" {
				if err == nil {
					t.Fatal("应拒绝非法或冲突的网络参数")
				}
				return
			}
			if err != nil || local != netip.MustParseAddr(tc.wantLocal) || prefix != netip.MustParsePrefix(tc.wantPrefix) {
				t.Fatalf("local=%s prefix=%s err=%v", local, prefix, err)
			}
		})
	}
}

// temporaryWorkingDir 切换到隔离工作目录并注册恢复；返回该目录，目录操作失败时终止测试，不可用于并行测试。
func temporaryWorkingDir(t *testing.T) string {
	t.Helper()
	previous, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(previous); err != nil {
			t.Error(err)
		}
	})
	return dir
}

// TestSimplifiedOptions 验证名称及目录可省略、相对目录可用，检查和离线HTTP启动均不创建协议资料。
func TestSimplifiedOptions(t *testing.T) {
	dir := temporaryWorkingDir(t)
	for _, extra := range [][]string{
		{"-check", "-online"},
		{"-data-dir", "relative", "-check"},
		{"-listen", "127.0.0.1:0"},
	} {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		var stdout, stderr bytes.Buffer
		args := append([]string{"-local", "127.0.0.1/8"}, extra...)
		if code := run(ctx, args, &stdout, &stderr); code != 0 {
			t.Fatalf("%v: code=%d stderr=%s", extra, code, stderr.String())
		}
		entries, err := os.ReadDir(dir)
		if err != nil || len(entries) != 0 {
			t.Fatalf("简化参数创建了资料：entries=%v err=%v", entries, err)
		}
	}
}

// TestSimplifiedOptionsReadExistingIdentity 验证默认目录和显式相对目录仍读取已有身份，损坏时拒绝而非静默新建。
func TestSimplifiedOptionsReadExistingIdentity(t *testing.T) {
	temporaryWorkingDir(t)
	for _, dir := range []string{"local/nativeclient/web-node", "relative"} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "identity"), []byte("corrupt"), 0600); err != nil {
			t.Fatal(err)
		}
		args := []string{"-local", "127.0.0.1/8", "-check"}
		if dir == "relative" {
			args = append(args, "-data-dir", dir)
		}
		var stdout, stderr bytes.Buffer
		if code := run(context.Background(), args, &stdout, &stderr); code != 2 || stderr.Len() == 0 {
			t.Fatalf("dir=%s code=%d stderr=%s", dir, code, stderr.String())
		}
		contents, err := os.ReadFile(filepath.Join(dir, "identity"))
		if err != nil || string(contents) != "corrupt" {
			t.Fatalf("损坏身份被改写：contents=%q err=%v", contents, err)
		}
	}
}

// TestAutomaticNetworkCommand 验证无需网络参数可只读校验，默认监听全部网卡；系统识别失败不创建资料或启动HTTP。
func TestAutomaticNetworkCommand(t *testing.T) {
	dir := temporaryWorkingDir(t)
	detect := func(requested netip.Addr) (netip.Addr, netip.Prefix, error) {
		if requested.IsValid() {
			t.Fatalf("未指定-local时不能要求某个IP：%s", requested)
		}
		return netip.MustParseAddr("127.0.0.1"), netip.MustParsePrefix("127.0.0.0/8"), nil
	}
	var stdout, stderr bytes.Buffer
	if code := runWithNetwork(context.Background(), []string{"-check", "-online"}, &stdout, &stderr, detect); code != 0 {
		t.Fatalf("默认识别校验失败：code=%d stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "本机IPv4：127.0.0.1") || !strings.Contains(stdout.String(), "发现子网：127.0.0.0/8") {
		t.Fatalf("未显示自动识别网络：%s", stdout.String())
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	stdout.Reset()
	stderr.Reset()
	if code := runWithNetwork(ctx, []string{"-listen", "0.0.0.0:0"}, &stdout, &stderr, detect); code != 0 || !strings.Contains(stdout.String(), "HTTP监听：0.0.0.0:") {
		t.Fatalf("自动识别后未启动离线网页：code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	failure := errors.New("synthetic network selection failure")
	if code := runWithNetwork(context.Background(), nil, &stdout, &stderr, func(netip.Addr) (netip.Addr, netip.Prefix, error) {
		return netip.Addr{}, netip.Prefix{}, failure
	}); code != 2 || !strings.Contains(stderr.String(), failure.Error()) {
		t.Fatalf("自动识别失败未明确报错：code=%d stderr=%s", code, stderr.String())
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 0 {
		t.Fatalf("未上线时创建了资料：%v %v", entries, err)
	}
}

// TestRejectInvalidOptions 验证非法监听、端口、子网及已移除的发现选项在任何文件或socket创建前拒绝。
func TestRejectInvalidOptions(t *testing.T) {
	for _, extra := range [][]string{
		{"-listen", "[::]:8788"}, {"-listen", "8.8.8.8:8788"}, {"-lan"},
		{"-discovery-port", "65536"}, {"-enet-port", "0"}, {"-session-duration", "-1s"},
		{"-data-dir", ""}, {"-name", ""}, {"extra"},
		{"-local", "127.0.0.1/31", "-prefix", ""}, {"-local", "127.0.0.1/8", "-prefix", "127.0.0.0/16"},
		{"-remote", "127.0.0.2:9011"}, {"-discover"}, {"-discover=false"},
	} {
		var stdout, stderr bytes.Buffer
		args := append(syntheticArgs(filepath.Join(t.TempDir(), "not-created")), extra...)
		if code := run(context.Background(), args, &stdout, &stderr); code != 2 {
			t.Fatalf("%v: code=%d stderr=%s", extra, code, stderr.String())
		}
	}
}
