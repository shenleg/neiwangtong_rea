// 本文件以合成 ID 检查参数、本地配置和只读默认行为，不访问真实配置或向客户端发送消息。
package main

import (
	"runtime"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"neiwangtong-gateway/internal/ipc"
)

// writeTestConfig 将合成配置写入测试临时目录并返回路径；写入失败时终止当前测试。
func writeTestConfig(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestRejectArguments 验证不接受任意群、缺值正文、重复开关和缺失路径。
func TestRejectArguments(t *testing.T) {
	for _, args := range [][]string{nil, {"-exe"}, {"-group-id", "synthetic"}, {"-text"}, {"-exe", "synthetic", "extra"}, {"-exe", "a", "-exe", "b"}, {"-send", "-send", "-exe", "synthetic"}, {"-exe", "synthetic", "-send"}, {"-exe", "synthetic", "-config", "a", "-config", "b"}} {
		if err := run(args); err == nil {
			t.Fatalf("参数未被拒绝: %v", args)
		}
	}
}

// TestTextArgument 验证 -text 原样传递到执行入口，缺省解析为固定实验正文。
func TestTextArgument(t *testing.T) {
	var texts []string
	execute := func(_ string, _ ipc.Config, _ bool, text string, _ io.Reader, _ io.Writer) error {
		texts = append(texts, text)
		return nil
	}
	if err := runWith([]string{"-exe", "synthetic.exe"}, execute); err != nil {
		t.Fatal(err)
	}
	if err := runWith([]string{"-exe", "synthetic.exe", "-text", "hello world"}, execute); err != nil {
		t.Fatal(err)
	}
	if len(texts) != 2 || texts[0] != ipc.Message || texts[1] != "hello world" {
		t.Fatal("-text 未原样传递或缺省未解析为固定正文")
	}
}

// TestNoImplicitSend 验证只读命令没有隐式发送，实验发送缺少群目标时在执行前被拒绝。
func TestNoImplicitSend(t *testing.T) {
	calls := 0
	execute := func(_ string, config ipc.Config, send bool, _ string, _ io.Reader, _ io.Writer) error {
		calls++
		if config.AuthorizedGroupID != "" || send {
			t.Fatal("未指定配置却获得群目标或发送请求")
		}
		return nil
	}
	if err := runWith([]string{"-exe", "synthetic.exe"}, execute); err != nil {
		t.Fatal(err)
	}
	if err := runWith([]string{"-exe", "synthetic.exe", "-send"}, execute); err == nil {
		t.Fatal("没有配置仍进入发送流程")
	}
	if calls != 1 {
		t.Fatalf("执行次数=%d", calls)
	}
}

// TestConfigPreservesGroupID 验证完整配置保留原始大小写，并且指定配置不启用发送。
func TestConfigPreservesGroupID(t *testing.T) {
	id := "0123456789aBcDeF0123456789AbCdEf"
	path := writeTestConfig(t, "{\n  \"authorized_group_id\": \""+id+"\"\n}\n")
	got, err := ipc.LoadConfig(path)
	if err != nil || got.AuthorizedGroupID != id {
		t.Fatalf("配置未原样读取: %v", err)
	}
	calls := 0
	if err := runWith([]string{"-exe", "synthetic.exe", "-config", path}, func(exe string, config ipc.Config, send bool, _ string, _ io.Reader, out io.Writer) error {
		calls++
		if exe != "synthetic.exe" || config.AuthorizedGroupID != id || send {
			t.Fatal("配置未传递给只读预检，或隐式启用了发送")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("执行次数=%d", calls)
	}
}

// TestConfigRejectsAmbiguity 验证重复键、多目标、异常格式和尾随内容均停止，不返回部分结果。
func TestConfigRejectsAmbiguity(t *testing.T) {
	valid := `"0123456789abcdef0123456789ABCDEF"`
	for name, content := range map[string]string{
		"empty_file":    "",
		"empty_object":  `{}`,
		"null":          `null`,
		"array":         `[]`,
		"unknown":       `{"group_id":` + valid + `}`,
		"duplicate":     `{"authorized_group_id":` + valid + `,"authorized_group_id":` + valid + `}`,
		"extra_field":   `{"authorized_group_id":` + valid + `,"name":"qun"}`,
		"extra_object":  `{"authorized_group_id":` + valid + `}{}`,
		"trailing_text": `{"authorized_group_id":` + valid + `}junk`,
		"target_list":   `{"authorized_group_id":[` + valid + `]}`,
		"null_id":       `{"authorized_group_id":null}`,
		"number_id":     `{"authorized_group_id":123}`,
		"empty_id":      `{"authorized_group_id":""}`,
		"short_id":      `{"authorized_group_id":"0123456789abcdef"}`,
		"long_id":       `{"authorized_group_id":"0123456789abcdef0123456789abcdef0"}`,
		"non_hex":       `{"authorized_group_id":"0123456789abcdef0123456789abcdeg"}`,
		"space":         `{"authorized_group_id":"0123456789abcdef0123456789abcdef "}`,
		"newline":       `{"authorized_group_id":"0123456789abcdef0123456789abcdef\n"}`,
		"nul":           `{"authorized_group_id":"0123456789abcdef0123456789abcdef\u0000"}`,
		"broken_json":   `{"authorized_group_id":`,
	} {
		t.Run(name, func(t *testing.T) {
			path := writeTestConfig(t, content)
			got, err := ipc.LoadConfig(path)
			if err == nil || got != (ipc.Config{}) {
				t.Fatal("无效配置返回授权值")
			}
			if strings.Contains(err.Error(), "0123456789") {
				t.Fatal("错误信息泄露配置值")
			}
			if err := runWith([]string{"-exe", "synthetic.exe", "-config", path}, func(string, ipc.Config, bool, string, io.Reader, io.Writer) error {
				t.Fatal("无效配置进入实验流程")
				return nil
			}); err == nil {
				t.Fatal("无效配置没有报错")
			}
		})
	}
}

// TestUserConfigEnablesUserSend 验证仅用户配置可只读预检，-send 时用户目标原样进入发送执行。
// 历史上 user 分支未接线时曾断言拒绝发送；接线后该行为翻转为放行，群方法不被调用的断言在experiment层。
func TestUserConfigEnablesUserSend(t *testing.T) {
	path := writeTestConfig(t, `{"authorized_user_id":"synthetic-user_1"}`)
	calls := 0
	sendFlags := make([]bool, 0, 2)
	execute := func(_ string, config ipc.Config, send bool, _ string, _ io.Reader, _ io.Writer) error {
		calls++
		sendFlags = append(sendFlags, send)
		if config.AuthorizedUserID != "synthetic-user_1" || config.AuthorizedGroupID != "" {
			t.Fatal("用户目标未原样传递给发送执行")
		}
		return nil
	}
	if err := runWith([]string{"-exe", "synthetic.exe", "-config", path}, execute); err != nil {
		t.Fatal(err)
	}
	err := runWith([]string{"-exe", "synthetic.exe", "-config", path, "-send"}, execute)
	// -send 的平台门槛先于执行入口：386 构建应进入执行，其他构建应被明确拒绝。
	if runtime.GOOS == "windows" && runtime.GOARCH == "386" {
		if err != nil {
			t.Fatal(err)
		}
		if calls != 2 || sendFlags[0] || !sendFlags[1] {
			t.Fatal("386构建的只读与发送开关未按命令传递")
		}
	} else {
		if err == nil || !strings.Contains(err.Error(), "386") {
			t.Fatal("非386构建的发送请求未被平台门槛拒绝")
		}
		if calls != 1 || sendFlags[0] {
			t.Fatal("非386构建不应进入发送执行")
		}
	}
}

// TestBothTargetsRejected 验证同时包含群与用户目标的配置在发送执行前被拒绝，不能任选其一。
func TestBothTargetsRejected(t *testing.T) {
	path := writeTestConfig(t, `{"authorized_group_id":"0123456789abcdef0123456789ABCDEF","authorized_user_id":"synthetic-user_1"}`)
	if err := runWith([]string{"-exe", "synthetic.exe", "-config", path, "-send"}, func(string, ipc.Config, bool, string, io.Reader, io.Writer) error {
		t.Fatal("双目标配置进入发送执行")
		return nil
	}); err == nil {
		t.Fatal("双目标配置未被拒绝")
	}
}

// TestObserveArguments 验证独立观察模式的参数校验：缺类别或状态、非法类别、与-send冲突均被拒绝。
func TestObserveArguments(t *testing.T) {
	for _, args := range [][]string{
		{"-exe", "synthetic.exe", "-observe", "NWT-GW-SYNTHETIC-1"},
		{"-exe", "synthetic.exe", "-observe", "NWT-GW-SYNTHETIC-1", "-observe-kind", "client"},
		{"-exe", "synthetic.exe", "-observe", "NWT-GW-SYNTHETIC-1", "-observe-state", "stable"},
		{"-exe", "synthetic.exe", "-observe", "NWT-GW-SYNTHETIC-1", "-observe-kind", "bogus", "-observe-state", "stable"},
		{"-exe", "synthetic.exe", "-observe", "NWT-GW-SYNTHETIC-1", "-observe-kind", "client", "-observe-state", "stable", "-send"},
	} {
		if err := run(args); err == nil {
			t.Fatalf("观察参数未被拒绝: %v", args)
		}
	}
}

// TestConfigMissingFile 验证磁盘读取失败直接报错，不静默恢复为空授权或其他目标。
func TestConfigMissingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing.json")
	if got, err := ipc.LoadConfig(path); err == nil || got != (ipc.Config{}) {
		t.Fatal("配置文件不存在仍返回授权")
	}
}
