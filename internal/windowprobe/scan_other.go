//go:build !windows

// 本文件让非 Windows 环境可以运行纯分类测试，但拒绝执行窗口探测。
package windowprobe

import "errors"

// Scan 在非 Windows 平台返回明确错误，不模拟窗口或回退到其他探测方式。
func Scan() (Report, error) {
	return Report{}, errors.New("窗口探测器仅支持 Windows")
}

func verifyTarget(Target) error {
	return errors.New("窗口复核仅支持 Windows")
}
