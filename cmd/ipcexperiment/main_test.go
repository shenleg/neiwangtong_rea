// 本文件以合成 ID 检查参数、本地配置和只读默认行为，不访问真实配置或向客户端发送消息。
package main

import (
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

// TestRejectArguments 验证不接受任意群、正文、重复开关和缺失路径。
func TestRejectArguments(t *testing.T) {
	for _, args := range [][]string{nil, {"-exe"}, {"-group-id", "synthetic"}, {"-text", "other"}, {"-exe", "synthetic", "extra"}, {"-exe", "a", "-exe", "b"}, {"-send", "-send", "-exe", "synthetic"}, {"-exe", "synthetic", "-send"}, {"-exe", "synthetic", "-config", "a", "-config", "b"}} {
		if err := run(args); err == nil {
			t.Fatalf("参数未被拒绝: %v", args)
		}
	}
}

// TestNoImplicitAuthorization 验证不指定配置时没有群绑定，实际发送请求在执行前被拒绝。
func TestNoImplicitAuthorization(t *testing.T) {
	calls := 0
	execute := func(_ string, config ipc.Config, attemptID string, send bool, _ io.Reader, _ io.Writer) error {
		calls++
		if config.AuthorizedGroupID != "" || attemptID != "" || send {
			t.Fatal("未指定配置却获得群绑定或发送请求")
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
	if err := runWith([]string{"-exe", "synthetic.exe", "-config", path}, func(exe string, config ipc.Config, attemptID string, send bool, _ io.Reader, out io.Writer) error {
		calls++
		if exe != "synthetic.exe" || config.AuthorizedGroupID != id || attemptID != "" || send {
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
			if err := runWith([]string{"-exe", "synthetic.exe", "-config", path}, func(string, ipc.Config, string, bool, io.Reader, io.Writer) error {
				t.Fatal("无效配置进入实验流程")
				return nil
			}); err == nil {
				t.Fatal("无效配置没有报错")
			}
		})
	}
}

// TestUserConfigDoesNotEnableCommandSend 验证仅用户配置可只读预检，但不会让原群实验命令发送单聊。
func TestUserConfigDoesNotEnableCommandSend(t *testing.T) {
	path := writeTestConfig(t, `{"authorized_user_id":"synthetic-user_1"}`)
	calls := 0
	execute := func(_ string, config ipc.Config, _ string, send bool, _ io.Reader, _ io.Writer) error {
		calls++
		if send || config.AuthorizedUserID != "synthetic-user_1" || config.AuthorizedGroupID != "" {
			t.Fatal("用户配置被改变或隐式请求发送")
		}
		return nil
	}
	if err := runWith([]string{"-exe", "synthetic.exe", "-config", path}, execute); err != nil {
		t.Fatal(err)
	}
	if err := runWith([]string{"-exe", "synthetic.exe", "-config", path, "-send", "-attempt-id", "NWT-GW-SYNTHETIC-USER"}, execute); err == nil {
		t.Fatal("仅用户配置仍进入命令发送")
	}
	if calls != 1 {
		t.Fatal("命令执行了用户发送")
	}
}

// TestConfigMissingFile 验证磁盘读取失败直接报错，不静默恢复为空授权或其他目标。
func TestConfigMissingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing.json")
	if got, err := ipc.LoadConfig(path); err == nil || got != (ipc.Config{}) {
		t.Fatal("配置文件不存在仍返回授权")
	}
}
