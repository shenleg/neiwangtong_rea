//go:build windows

// 本文件用测试进程自己的隐藏窗口验证 Win32 元数据 API，不改变目标客户端或读取其内容。
package windowprobe

import (
	"encoding/json"
	"os"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"testing"
	"unsafe"
)

// fixtureWindow 在当前锁定线程创建本测试进程的隐藏 STATIC popup，返回 HWND 和清理函数。
// owner 指定本测试创建的 owner，title 为合成 caption；API 或编码失败立即终止测试。
func fixtureWindow(t *testing.T, owner uintptr, title string) (uintptr, func()) {
	t.Helper()
	class, err := syscall.UTF16PtrFromString("STATIC")
	if err != nil {
		t.Fatal(err)
	}
	caption, err := syscall.UTF16PtrFromString(title)
	if err != nil {
		t.Fatal(err)
	}
	// WS_POPUP 不含 WS_VISIBLE，不显示 UI；只创建本进程窗口，绝不接入内网通。
	hwnd, _, callErr := user32.NewProc("CreateWindowExW").Call(0, uintptr(unsafe.Pointer(class)),
		uintptr(unsafe.Pointer(caption)), 0x80000000, 100, 100, 300, 500, owner, 0, 0, 0)
	if hwnd == 0 {
		t.Fatal(apiError("CreateWindowExW (test fixture)", callErr))
	}
	return hwnd, func() {
		if ok, _, err := user32.NewProc("DestroyWindow").Call(hwnd); ok == 0 {
			t.Error(apiError("DestroyWindow (test fixture)", err))
		}
	}
}

// TestWindowMetadata 验证 Unicode caption、空标题、owner/root_owner、尺寸和复查，不依赖内网通。
func TestWindowMetadata(t *testing.T) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	owner, closeOwner := fixtureWindow(t, 0, "")
	defer closeOwner()
	hwnd, closeWindow := fixtureWindow(t, owner, "合成窗口标题")
	defer closeWindow()
	w := readWindow(hwnd, uint32(os.Getpid()))
	recheckWindow(&w)
	if w.Class != "Static" || w.Title != "合成窗口标题" || w.Rect == nil || !w.Stable || len(w.Issues) != 0 {
		t.Fatalf("fixture 元数据读取失败: %+v", w)
	}
	if w.Visible || !w.TopLevel || w.Owner != Handle(owner) || w.Root != Handle(hwnd) || w.RootOwner != Handle(owner) {
		t.Fatalf("窗口关系不符: %+v", w)
	}
	if w.Rect.Width != 300 || w.Rect.Height != 500 {
		t.Fatalf("RECT 尺寸计算错误: %+v", w.Rect)
	}
	// 先留下非零 LastError，正常空 caption 仍应成功，避免把旧错误误报为读取失败。
	setLastError.Call(5)
	if text, err := readText(owner, getWindowText, 32768); text != "" || err != nil {
		t.Fatalf("空标题不应复用旧 LastError: text=%q, error=%v", text, err)
	}
}

// TestInvalidHandle 验证失效 HWND 产生明确错误，且复查会撤销旧的 stable 状态。
func TestInvalidHandle(t *testing.T) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if _, _, err := windowPID(0); err == nil {
		t.Fatal("空 HWND 的 PID 查询未失败")
	}
	if _, err := readText(0, getClassName, 256); err == nil {
		t.Fatal("空 HWND 的类名查询未失败")
	}
	if _, err := readStyle(0, -16); err == nil {
		t.Fatal("空 HWND 的 style 查询未失败")
	}
	w := Window{Stable: true}
	recheckWindow(&w)
	if w.Stable || len(w.Issues) == 0 {
		t.Fatal("失效窗口复查未撤销 stable 状态")
	}
}

// TestProcessSnapshot 验证进程筛选只返回目标文件名，并按 PID 排序。
func TestProcessSnapshot(t *testing.T) {
	processes, err := findProcesses()
	if err != nil {
		t.Fatal(err)
	}
	for i, p := range processes {
		if p.PID == 0 || !strings.EqualFold(p.Name, "ShiYeLine.exe") {
			t.Fatal("进程筛选返回非目标项")
		}
		if i > 0 && processes[i-1].PID >= p.PID {
			t.Fatal("PID 未严格排序")
		}
	}
}

// TestProcessIdentity 验证本进程路径、创建时间和存活状态能从只读 handle 获取，错误 PID 立即失败。
func TestProcessIdentity(t *testing.T) {
	first, err := inspectProcess(uint32(os.Getpid()))
	if err != nil {
		t.Fatal(err)
	}
	second, err := inspectProcess(uint32(os.Getpid()))
	if err != nil {
		t.Fatal(err)
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if !samePath(first.Path, exe) || first.CreationTime == 0 || !first.Alive || first.SessionID == nil || !sameProcess(first, second) {
		t.Fatalf("进程身份不完整或改变: %+v / %+v", first, second)
	}
	if _, err := inspectProcess(0); err == nil {
		t.Fatal("PID 0 不应成功")
	}
	h, err := syscall.OpenProcess(processQueryLimitedInformation|syscall.SYNCHRONIZE, false, uint32(os.Getpid()))
	if err != nil {
		t.Fatal(err)
	}
	syscall.CloseHandle(h)
	if _, err := readProcess(h, uint32(os.Getpid())); err == nil {
		t.Fatal("已关闭 handle 不应成功")
	}
}

// TestFinalVerificationRejects 验证最后检查拒绝假路径、进程重用和非 SkinDialog/失效 HWND。
func TestFinalVerificationRejects(t *testing.T) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	p, err := inspectProcess(uint32(os.Getpid()))
	if err != nil {
		t.Fatal(err)
	}
	owner, closeOwner := fixtureWindow(t, 0, "")
	defer closeOwner()
	hwnd, closeMain := fixtureWindow(t, owner, "")
	defer closeMain()
	target := Target{Process: p, Window: readWindow(hwnd, p.PID), Owner: readWindow(owner, p.PID)}
	// 本进程的 GetWindowTextW 可能同步调用窗口过程，必须留在创建窗口的线程，不能用 t.Run 切换 goroutine。
	for _, what := range []string{"static class", "path", "creation", "pid", "destroyed"} {
		bad := target
		switch what {
		case "path":
			bad.Process.Path += ".wrong"
		case "creation":
			bad.Process.CreationTime++
		case "pid":
			bad.Process.PID = 0
		case "destroyed":
			bad.Window.HWND = 0
		}
		if err := verifyTarget(bad); err == nil {
			t.Fatalf("错误目标通过最终复核: %s", what)
		}
	}
}

// TestSharedEnumerationCallback 验证并发采集会串行复用 callback，且每次保留自身采集结果。
func TestSharedEnumerationCallback(t *testing.T) {
	var wg sync.WaitGroup
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, err := Scan()
			if err != nil {
				t.Error(err)
				return
			}
			if r.StartedAt.IsZero() || r.FinishedAt.Before(r.StartedAt) {
				t.Error("采集时间不完整")
			}
		}()
	}
	wg.Wait()
}

// TestResolveAuthorizedCandidate 仅在显式提供 EXE 路径时只读验证真实候选；不读取子控件或切换 UI。
func TestResolveAuthorizedCandidate(t *testing.T) {
	path := os.Getenv("WINDOWPROBE_TEST_EXE")
	if path == "" {
		t.Skip("未提供显式授权的测试 EXE 路径")
	}
	if os.Getenv("WINDOWPROBE_TEST_REJECT") == "1" {
		r, err := Scan()
		if err != nil {
			t.Fatal(err)
		}
		if r.Assessment.Status != "ambiguous" {
			t.Fatalf("预期多个候选，实际为 %s", r.Assessment.Status)
		}
		got, err := Resolve(path, nil)
		if err == nil || got.Window.HWND != 0 {
			t.Fatalf("预期拒绝但返回 target=%+v error=%v", got, err)
		}
		t.Logf("refused with zero HWND: %v", err)
		return
	}
	if baseline := os.Getenv("WINDOWPROBE_TEST_PREVIOUS"); baseline != "" {
		data, err := os.ReadFile(baseline)
		if err != nil {
			t.Fatal(err)
		}
		var previous Target
		if err := json.Unmarshal(data, &previous); err != nil {
			t.Fatal(err)
		}
		got, err := Resolve(path, &previous)
		if err == nil || got.Window.HWND != 0 {
			t.Fatalf("重启后沿用旧 HWND: target=%+v error=%v", got, err)
		}
		t.Logf("previous identity refused with zero HWND: %v", err)
	}
	first, err := Resolve(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	second, err := Resolve(path, &first)
	if err != nil {
		t.Fatal(err)
	}
	if !sameProcess(first.Process, second.Process) || second.CheckedAt.Before(first.CheckedAt) {
		t.Fatal("连续复核身份或时间不符")
	}
	t.Logf("PID=%d HWND=%s owner=%s class=%s reliability=%s; both final checks passed", second.Process.PID, second.Window.HWND, second.Owner.HWND, second.Window.Class, second.Reliability)
}

// TestProcessSetChanges 验证实例增加、PID/创建时间/路径改变、会话变化及复查失败都会阻止稳定性结论。
func TestProcessSetChanges(t *testing.T) {
	a, b := uint32(1), uint32(2)
	original := []Process{{PID: 10, SessionID: &a}}
	if !sameProcesses(original, original) {
		t.Fatal("相同快照未通过比较")
	}
	for _, after := range [][]Process{
		nil,
		{{PID: 11, SessionID: &a}},
		{{PID: 10, SessionID: &b}},
		{{PID: 10}},
		{{PID: 10, SessionID: &a, Path: `C:\different\ShiYeLine.exe`}},
		{{PID: 10, SessionID: &a, CreationTime: 123}},
		{{PID: 10, SessionID: &a, Alive: true}},
		{{PID: 10, SessionID: &a, Issues: []string{"failed"}}},
	} {
		if sameProcesses(original, after) {
			t.Fatal("进程或会话变化未被识别")
		}
	}
}
