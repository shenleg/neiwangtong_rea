// 本文件验证命令将已授权请求标识传给Adapter，不隐式复用旧任务或开放正文、目标参数。
package main

import (
	"io"
	"runtime"
	"testing"

	"neiwangtong-gateway/internal/ipc"
)

// TestExplicitAttemptArgument 验证386命令把配置和显式标识交给执行入口，不创建新标识。
func TestExplicitAttemptArgument(t *testing.T) {
	if runtime.GOOS != "windows" || runtime.GOARCH != "386" {
		t.Skip("实际发送参数只允许Windows 386；其他构建的拒绝行为由参数测试覆盖")
	}
	path := writeTestConfig(t, `{"authorized_group_id":"`+syntheticGroupID+`"}`)
	calls := 0
	err := runWith([]string{"-exe", "synthetic.exe", "-config", path, "-attempt-id", syntheticAttemptID, "-send"}, func(exe string, config ipc.Config, attemptID string, send bool, _ io.Reader, _ io.Writer) error {
		calls++
		if exe != "synthetic.exe" || config.AuthorizedGroupID != syntheticGroupID || attemptID != syntheticAttemptID || !send {
			t.Fatal("未保留原始配置或显式请求")
		}
		return nil
	})
	if err != nil || calls != 1 {
		t.Fatalf("参数未传递: calls=%d err=%v", calls, err)
	}
}

// TestNoRequestArgumentFallback 验证重复标识和未提供标识都不会进入执行入口，更不会自动生成。
func TestNoRequestArgumentFallback(t *testing.T) {
	path := writeTestConfig(t, `{"authorized_group_id":"`+syntheticGroupID+`"}`)
	for _, args := range [][]string{
		{"-exe", "synthetic.exe", "-attempt-id", "NWT-GW-ONE", "-attempt-id", "NWT-GW-TWO"},
		{"-exe", "synthetic.exe", "-config", path, "-send"},
	} {
		if err := runWith(args, func(string, ipc.Config, string, bool, io.Reader, io.Writer) error {
			t.Fatal("无明确单次请求却进入执行")
			return nil
		}); err == nil {
			t.Fatal("没有拒绝无效任务参数")
		}
	}
}
