// 本文件检查命令行和空授权群的拒绝行为，不运行客户端或发送消息。
package main

import "testing"

// TestRejectArguments 验证不接受任意群、正文、重复开关和缺失路径。
func TestRejectArguments(t *testing.T) {
	for _, args := range [][]string{nil, {"-exe"}, {"-group-id", "synthetic"}, {"-text", "other"}, {"-exe", "synthetic", "extra"}, {"-exe", "a", "-exe", "b"}, {"-send", "-send", "-exe", "synthetic"}, {"-exe", "synthetic", "-send"}} {
		if err := run(args); err == nil {
			t.Fatalf("参数未被拒绝: %v", args)
		}
	}
}

// TestAuthorizationNotBound 验证当前构建没有真实群授权，不存在可用的发送目标。
func TestAuthorizationNotBound(t *testing.T) {
	if authorizedGroupID != "" {
		t.Fatal("本轮仅完成实现与测试，不得预置真实群 ID")
	}
}
