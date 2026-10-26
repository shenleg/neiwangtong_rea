// 本文件验证Web命令默认只启动HTTP、只读校验及参数拒绝；测试不连接真实内网通节点。
package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// syntheticArgs 返回仅用于测试的明确loopback配置，dir保存临时独立资料；无I/O或错误。
func syntheticArgs(dir string) []string {
	return []string{"-local", "127.0.0.1", "-prefix", "127.0.0.0/8", "-remote", "127.0.0.2:9011",
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
	args := append(syntheticArgs(dir), "-lan", "-listen", "192.168.50.10:8788", "-check")
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

// TestDiscoverCheckWithoutSeed 验证显式子网发现无需指定单个对端，且校验不绑定端口或发广播。
func TestDiscoverCheckWithoutSeed(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "not-created")
	args := append(syntheticArgs(dir), "-remote", "", "-discover", "-session-duration", "30s", "-check")
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

// TestRejectInvalidOptions 验证非回环监听、错误端口和广播目标在任何文件或socket创建前拒绝。
func TestRejectInvalidOptions(t *testing.T) {
	for _, extra := range [][]string{
		{"-listen", "0.0.0.0:8788"}, {"-listen", "192.168.50.10:8788"},
		{"-lan", "-listen", "0.0.0.0:8788"}, {"-lan", "-listen", "8.8.8.8:8788"},
		{"-discovery-port", "65536"}, {"-enet-port", "0"}, {"-session-duration", "-1s"},
		{"-remote", "127.255.255.255:9011"}, {"-data-dir", "relative"}, {"-name", ""}, {"extra"},
	} {
		var stdout, stderr bytes.Buffer
		args := append(syntheticArgs(filepath.Join(t.TempDir(), "not-created")), extra...)
		if code := run(context.Background(), args, &stdout, &stderr); code != 2 {
			t.Fatalf("%v: code=%d stderr=%s", extra, code, stderr.String())
		}
	}
}
