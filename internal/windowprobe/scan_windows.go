//go:build windows

// 本文件封装 Toolhelp 和 User32 的只读元数据 API，不包含任何发送消息或进程内存访问 API。
package windowprobe

import (
	"errors"
	"fmt"
	"runtime"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"
)

var (
	user32                    = syscall.NewLazyDLL("user32.dll")
	kernel32                  = syscall.NewLazyDLL("kernel32.dll")
	enumWindows               = user32.NewProc("EnumWindows")
	getWindowThreadProcessID  = user32.NewProc("GetWindowThreadProcessId")
	getClassName              = user32.NewProc("GetClassNameW")
	getWindowText             = user32.NewProc("GetWindowTextW")
	isWindowVisible           = user32.NewProc("IsWindowVisible")
	isIconic                  = user32.NewProc("IsIconic")
	getWindow                 = user32.NewProc("GetWindow")
	getAncestor               = user32.NewProc("GetAncestor")
	getWindowRect             = user32.NewProc("GetWindowRect")
	getWindowLong             = user32.NewProc("GetWindowLongW")
	isWindow                  = user32.NewProc("IsWindow")
	setLastError              = kernel32.NewProc("SetLastError")
	processIDToSessionID      = kernel32.NewProc("ProcessIdToSessionId")
	queryFullProcessImageName = kernel32.NewProc("QueryFullProcessImageNameW")
)

// NewCallback 的槽位不会释放；重复扫描复用同一个 callback，并串行保护其临时采集上下文。
var (
	scanMu         sync.Mutex
	activeReport   *Report
	activeTargets  map[uint32]bool
	windowCallback = syscall.NewCallback(collectWindow)
)

func collectWindow(hwnd, _ uintptr) uintptr {
	pid, _, err := windowPID(hwnd)
	if err != nil {
		activeReport.Issues = append(activeReport.Issues, fmt.Sprintf("枚举窗口 %s: %v", Handle(hwnd), err))
		return 1
	}
	if activeTargets[pid] {
		activeReport.Windows = append(activeReport.Windows, readWindow(hwnd, pid))
	}
	return 1
}

const (
	gwOwner                        = 4
	gaParent                       = 1
	gaRoot                         = 2
	gaRootOwner                    = 3
	wsChild                        = 0x40000000
	processQueryLimitedInformation = 0x1000 // 只查询元数据，不申请进程内存或写入权限。
)

// Scan 查找 ShiYeLine.exe 并采集当前 desktop 的顶层窗口元数据，随后进行关系复查与评估。
// 不启动客户端、不读取子控件、不发送消息。初始进程快照失败返回错误；局部失败保留在报告中。
func Scan() (Report, error) {
	scanMu.Lock()
	defer scanMu.Unlock()
	defer func() { activeReport, activeTargets = nil, nil }()
	// GetWindowTextW / GetWindowLongW 的零返回既可能正常也可能失败，清除与读取 LastError 必须在同一线程。
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	r := Report{StartedAt: time.Now().UTC(), Windows: []Window{}, Issues: []string{}}
	processes, err := findProcesses()
	if err != nil {
		return r, err
	}
	r.Processes = processes
	targets := make(map[uint32]bool, len(processes))
	for _, p := range processes {
		targets[p.PID] = true
	}
	if len(targets) != 0 {
		activeReport, activeTargets = &r, targets
		ok, _, callErr := enumWindows.Call(windowCallback, 0)
		if ok == 0 {
			r.Issues = append(r.Issues, apiError("EnumWindows", callErr).Error())
		}
	}
	for i := range r.Windows {
		recheckWindow(&r.Windows[i])
	}
	after, err := findProcesses()
	if err != nil {
		r.Issues = append(r.Issues, fmt.Sprintf("进程复查失败: %v", err))
	} else if !sameProcesses(processes, after) {
		r.Issues = append(r.Issues, "采集前后 ShiYeLine.exe 进程集合、路径、创建时间、存活状态或会话发生变化；请重新探测。")
	}
	sort.Slice(r.Windows, func(i, j int) bool {
		if r.Windows[i].PID != r.Windows[j].PID {
			return r.Windows[i].PID < r.Windows[j].PID
		}
		return r.Windows[i].HWND < r.Windows[j].HWND
	})
	r.FinishedAt = time.Now().UTC()
	Assess(&r)
	return r, nil
}

func findProcesses() ([]Process, error) {
	snapshot, err := syscall.CreateToolhelp32Snapshot(syscall.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return nil, fmt.Errorf("CreateToolhelp32Snapshot: %w", err)
	}
	defer syscall.CloseHandle(snapshot)
	entry := syscall.ProcessEntry32{Size: uint32(unsafe.Sizeof(syscall.ProcessEntry32{}))}
	result := []Process{}
	for err = syscall.Process32First(snapshot, &entry); err == nil; err = syscall.Process32Next(snapshot, &entry) {
		name := syscall.UTF16ToString(entry.ExeFile[:])
		if !strings.EqualFold(name, "ShiYeLine.exe") {
			continue
		}
		p, identityErr := inspectProcess(entry.ProcessID)
		p.Name = name
		if identityErr != nil {
			p.Issues = append(p.Issues, identityErr.Error())
		}
		result = append(result, p)
	}
	if !errors.Is(err, syscall.ERROR_NO_MORE_FILES) {
		return nil, fmt.Errorf("Process32First/Next: %w", err)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].PID < result[j].PID })
	return result, nil
}

// inspectProcess 只请求查询和同步权限；拒绝访问时明确失败，不提权或回退到同名匹配。
func inspectProcess(pid uint32) (Process, error) {
	h, err := syscall.OpenProcess(processQueryLimitedInformation|syscall.SYNCHRONIZE, false, pid) // PROCESS_QUERY_LIMITED_INFORMATION。
	if err != nil {
		return Process{PID: pid}, fmt.Errorf("OpenProcess(%d): %w", pid, err)
	}
	defer syscall.CloseHandle(h)
	return readProcess(h, pid)
}

// readProcess 从同一个进程 handle 查询路径、创建时间、session 与存活状态，避免 PID 复用混淆。
func readProcess(h syscall.Handle, pid uint32) (Process, error) {
	p := Process{PID: pid, Issues: []string{}}
	buffer := make([]uint16, 32768) // Windows 扩展路径上限，不能沿用 MAX_PATH 截断路径。
	size := uint32(len(buffer))
	ok, _, err := queryFullProcessImageName.Call(uintptr(h), 0, uintptr(unsafe.Pointer(&buffer[0])), uintptr(unsafe.Pointer(&size)))
	if ok == 0 {
		return p, apiError("QueryFullProcessImageNameW", err)
	}
	p.Path = syscall.UTF16ToString(buffer[:size])
	var creation, exit, kernel, user syscall.Filetime
	if err := syscall.GetProcessTimes(h, &creation, &exit, &kernel, &user); err != nil {
		return p, fmt.Errorf("GetProcessTimes: %w", err)
	}
	p.CreationTime = uint64(creation.HighDateTime)<<32 | uint64(creation.LowDateTime)
	var session uint32
	ok, _, err = processIDToSessionID.Call(uintptr(pid), uintptr(unsafe.Pointer(&session)))
	if ok == 0 {
		return p, apiError("ProcessIdToSessionId", err)
	}
	p.SessionID = &session
	state, waitErr := syscall.WaitForSingleObject(h, 0)
	if waitErr != nil {
		return p, fmt.Errorf("WaitForSingleObject: %w", waitErr)
	}
	if state != syscall.WAIT_TIMEOUT {
		return p, fmt.Errorf("进程 %d 已退出或等待状态异常: %d", pid, state)
	}
	p.Alive = true
	return p, nil
}

func sameProcesses(a, b []Process) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if !sameProcess(a[i], b[i]) || a[i].Alive != b[i].Alive {
			return false
		}
		if len(b[i].Issues) != 0 {
			return false
		}
	}
	return true
}

func windowPID(hwnd uintptr) (uint32, uint32, error) {
	var pid uint32
	tid, _, err := getWindowThreadProcessID.Call(hwnd, uintptr(unsafe.Pointer(&pid)))
	if tid == 0 {
		return 0, 0, apiError("GetWindowThreadProcessId", err)
	}
	return pid, uint32(tid), nil
}

func readWindow(hwnd uintptr, expectedPID uint32) Window {
	w := Window{HWND: Handle(hwnd), PID: expectedPID, Issues: []string{}}
	pid, tid, err := windowPID(hwnd)
	if err != nil || pid != expectedPID {
		w.Issues = append(w.Issues, fmt.Sprintf("窗口 PID 在读取前失效或改变: PID=%d, error=%v", pid, err))
		return w
	}
	w.TID = tid
	w.Class, err = readText(hwnd, getClassName, 256)
	if err != nil {
		w.Issues = append(w.Issues, err.Error())
	}
	// 跨进程 GetWindowTextW 只取系统维护的 caption；不读取编辑框或其他子控件文本。
	// 32768 个 UTF-16 单元给标题留足空间；达到边界时明确报不完整，不静默截断。
	w.Title, err = readText(hwnd, getWindowText, 32768)
	if err != nil {
		w.Issues = append(w.Issues, err.Error())
	}
	visible, _, _ := isWindowVisible.Call(hwnd)
	minimized, _, _ := isIconic.Call(hwnd)
	w.Visible, w.Minimized = visible != 0, minimized != 0
	owner, _, _ := getWindow.Call(hwnd, gwOwner)
	parent, _, _ := getAncestor.Call(hwnd, gaParent)
	root, _, _ := getAncestor.Call(hwnd, gaRoot)
	rootOwner, _, _ := getAncestor.Call(hwnd, gaRootOwner)
	w.Owner, w.Parent, w.Root, w.RootOwner = Handle(owner), Handle(parent), Handle(root), Handle(rootOwner)
	if root == 0 || rootOwner == 0 {
		w.Issues = append(w.Issues, "GetAncestor 未返回有效 root/root_owner。")
	}
	w.Style, err = readStyle(hwnd, -16) // GWL_STYLE，使用 LONG 而非指针，不受进程位数影响。
	if err != nil {
		w.Issues = append(w.Issues, err.Error())
	}
	w.ExStyle, err = readStyle(hwnd, -20) // GWL_EXSTYLE。
	if err != nil {
		w.Issues = append(w.Issues, err.Error())
	}
	w.TopLevel = root == hwnd && w.Style&wsChild == 0
	var rect struct{ Left, Top, Right, Bottom int32 }
	ok, _, callErr := getWindowRect.Call(hwnd, uintptr(unsafe.Pointer(&rect)))
	if ok == 0 {
		w.Issues = append(w.Issues, apiError("GetWindowRect", callErr).Error())
	} else {
		w.Rect = &Rect{Left: rect.Left, Top: rect.Top, Right: rect.Right, Bottom: rect.Bottom,
			Width: int64(rect.Right) - int64(rect.Left), Height: int64(rect.Bottom) - int64(rect.Top)}
	}
	return w
}

func readText(hwnd uintptr, proc *syscall.LazyProc, capacity int) (string, error) {
	buffer := make([]uint16, capacity)
	setLastError.Call(0)
	n, _, err := proc.Call(hwnd, uintptr(unsafe.Pointer(&buffer[0])), uintptr(len(buffer)))
	if n == 0 && (proc == getClassName || err != syscall.Errno(0)) {
		return "", apiError(proc.Name, err)
	}
	text := syscall.UTF16ToString(buffer[:int(n)])
	if n >= uintptr(len(buffer)-1) {
		return text, fmt.Errorf("%s: 文本达到缓冲区边界，可能截断", proc.Name)
	}
	return text, nil
}

func readStyle(hwnd uintptr, index int32) (uint32, error) {
	setLastError.Call(0)
	value, _, err := getWindowLong.Call(hwnd, uintptr(index))
	if value == 0 && err != syscall.Errno(0) {
		return 0, apiError("GetWindowLongW", err)
	}
	return uint32(value), nil
}

func recheckWindow(w *Window) {
	w.Stable = false
	hwnd := uintptr(w.HWND)
	exists, _, _ := isWindow.Call(hwnd)
	pid, tid, err := windowPID(hwnd)
	if exists == 0 || err != nil || pid != w.PID || tid != w.TID {
		w.Issues = append(w.Issues, "复查时窗口已失效或 PID/TID 改变。")
		return
	}
	class, classErr := readText(hwnd, getClassName, 256)
	owner, _, _ := getWindow.Call(hwnd, gwOwner)
	root, _, _ := getAncestor.Call(hwnd, gaRoot)
	rootOwner, _, _ := getAncestor.Call(hwnd, gaRootOwner)
	style, styleErr := readStyle(hwnd, -16)
	visible, _, _ := isWindowVisible.Call(hwnd)
	if classErr != nil || styleErr != nil || class != w.Class || Handle(owner) != w.Owner ||
		Handle(root) != w.Root || Handle(rootOwner) != w.RootOwner || style != w.Style || (visible != 0) != w.Visible {
		w.Issues = append(w.Issues, fmt.Sprintf("复查时类名、关系、style 或可见性改变/读取失败: class_error=%v, style_error=%v", classErr, styleErr))
		return
	}
	w.Stable = true
}

// verifyTarget 持有进程 handle 完成最终复核，随后再次检查存活；不执行消息调用。
func verifyTarget(target Target) error {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	h, err := syscall.OpenProcess(processQueryLimitedInformation|syscall.SYNCHRONIZE, false, target.Process.PID)
	if err != nil {
		return fmt.Errorf("OpenProcess: %w", err)
	}
	defer syscall.CloseHandle(h)
	p, err := readProcess(h, target.Process.PID)
	if err != nil {
		return err
	}
	if !sameProcess(p, target.Process) {
		return fmt.Errorf("PID、路径、创建时间或 session 改变")
	}
	main := readWindow(uintptr(target.Window.HWND), p.PID)
	owner := readWindow(uintptr(target.Owner.HWND), p.PID)
	recheckWindow(&main)
	recheckWindow(&owner)
	if !usableSkin(main) || !usableSkin(owner) || !sameWindow(main, target.Window) || !sameWindow(owner, target.Owner) || owner.Visible || owner.Owner != 0 {
		return fmt.Errorf("窗口失效、类名/PID/TID/owner/root 改变，或隐藏 owner 特征消失；main_issues=%v owner_issues=%v", main.Issues, owner.Issues)
	}
	// 窗口读取期间进程也可退出；同一 handle 的最后检查不会误认复用此 PID 的新进程。
	_, err = readProcess(h, p.PID)
	return err
}

func apiError(name string, err error) error {
	if err == syscall.Errno(0) {
		return fmt.Errorf("%s 返回失败，未提供 Win32 错误码", name)
	}
	return fmt.Errorf("%s: %w", name, err)
}
