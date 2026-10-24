// 本文件验证命令传递实验群目标及发送开关，不接收记录标识或开放任意正文参数。
package main

import (
	"io"
	"runtime"
	"testing"

	"neiwangtong-gateway/internal/ipc"
)

// TestSendWithoutAttemptArgument 验证386命令无需手填标识即可把目标配置和显式发送开关交给执行入口。
func TestSendWithoutAttemptArgument(t *testing.T) {
	if runtime.GOOS != "windows" || runtime.GOARCH != "386" {
		t.Skip("实际发送参数只允许Windows 386；其他构建的拒绝行为由参数测试覆盖")
	}
	path := writeTestConfig(t, `{"authorized_group_id":"`+syntheticGroupID+`"}`)
	calls := 0
	err := runWith([]string{"-exe", "synthetic.exe", "-config", path, "-send"}, func(exe string, config ipc.Config, send bool, _ io.Reader, _ io.Writer) error {
		calls++
		if exe != "synthetic.exe" || config.AuthorizedGroupID != syntheticGroupID || !send {
			t.Fatal("未保留原始目标配置或显式发送请求")
		}
		return nil
	})
	if err != nil || calls != 1 {
		t.Fatalf("参数未传递: calls=%d err=%v", calls, err)
	}
}

// TestAttemptArgumentRemoved 验证旧标识参数被明确拒绝，不能手动复用历史记录标识。
func TestAttemptArgumentRemoved(t *testing.T) {
	for _, args := range [][]string{
		{"-exe", "synthetic.exe", "-attempt-id", syntheticAttemptID},
		{"-exe", "synthetic.exe", "-attempt-id", "NWT-GW-ONE", "-attempt-id", "NWT-GW-TWO"},
	} {
		if err := runWith(args, func(string, ipc.Config, bool, io.Reader, io.Writer) error {
			t.Fatal("旧标识参数仍进入执行")
			return nil
		}); err == nil {
			t.Fatal("没有拒绝已移除的任务参数")
		}
	}
}
