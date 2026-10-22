//go:build !windows

// 本文件使非 Windows 构建明确拒绝平台操作，纯缓冲区和单次控制测试仍可运行。
package ipcexperiment

import (
	"fmt"
	"neiwangtong-gateway/internal/windowprobe"
)

func checkClient(string, *windowprobe.Target) (windowprobe.Target, Preflight, error) {
	return windowprobe.Target{}, Preflight{}, fmt.Errorf("IPC 实验只支持 Windows")
}

func sendOnce(windowprobe.Target, []byte) (CallResult, error) {
	return CallResult{}, fmt.Errorf("IPC 实验只支持 Windows 386")
}
