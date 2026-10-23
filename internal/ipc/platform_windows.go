//go:build windows

// 本文件只读核对客户端文件/进程身份，并封装单次、有期限的 WM_COPYDATA；不访问目标内存。
package ipc

import (
	"crypto/sha256"
	"fmt"
	"runtime"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"neiwangtong-gateway/internal/windowprobe"
)

var (
	user32                   = syscall.NewLazyDLL("user32.dll")
	kernel32                 = syscall.NewLazyDLL("kernel32.dll")
	advapi32                 = syscall.NewLazyDLL("advapi32.dll")
	getThreadDesktop         = user32.NewProc("GetThreadDesktop")
	getUserObjectInformation = user32.NewProc("GetUserObjectInformationW")
	getCurrentThreadID       = kernel32.NewProc("GetCurrentThreadId")
	processIDToSessionID     = kernel32.NewProc("ProcessIdToSessionId")
	getTokenInformation      = advapi32.NewProc("GetTokenInformation")
	sendMessageTimeout       = user32.NewProc("SendMessageTimeoutW")
	createWindowEx           = user32.NewProc("CreateWindowExW")
	destroyWindow            = user32.NewProc("DestroyWindow")
	getWindowThreadProcessID = user32.NewProc("GetWindowThreadProcessId")
	setLastError             = kernel32.NewProc("SetLastError")
)

const (
	processQueryLimitedInformation = 0x1000
	wmCopyData                     = 0x004a
	businessData                   = 0x0fecf10f // E22-I：0x44a119 的文本业务分支。
	sendTimeoutMS                  = 5000       // 实验等待上限；不是客户端协议值，也不据超时重试。
	smtoAbortIfHung                = 0x0002
	smtoErrorOnExit                = 0x0020
)

// COPYDATASTRUCT 必须按发送进程 ABI 布局；386 为 12 字节，amd64 为 24 字节。
type copyData struct {
	Data    uintptr
	Size    uint32
	Payload unsafe.Pointer
}

func checkClient(path string, previous *windowprobe.Target) (windowprobe.Target, Preflight, error) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	target, err := windowprobe.Resolve(path, previous)
	if err != nil {
		return windowprobe.Target{}, Preflight{}, err
	}
	if !target.Window.Visible || target.Window.Minimized {
		return windowprobe.Target{}, Preflight{}, fmt.Errorf("首次实验只允许正常显示、非最小化的唯一候选")
	}
	digest, err := hashClient(target.Process.Path)
	if err != nil {
		return windowprobe.Target{}, Preflight{}, err
	}
	h, err := openBoundProcess(target)
	if err != nil {
		return windowprobe.Target{}, Preflight{}, err
	}
	defer syscall.CloseHandle(h)
	if err := sameSecurityContext(h); err != nil {
		return windowprobe.Target{}, Preflight{}, err
	}
	var session uint32
	ok, _, callErr := processIDToSessionID.Call(uintptr(syscall.Getpid()), uintptr(unsafe.Pointer(&session)))
	if ok == 0 {
		return windowprobe.Target{}, Preflight{}, fmt.Errorf("ProcessIdToSessionId: %w", callErr)
	}
	if target.Process.SessionID == nil || *target.Process.SessionID != session {
		return windowprobe.Target{}, Preflight{}, fmt.Errorf("发送方与客户端不在同一 session")
	}
	tid, _, _ := getCurrentThreadID.Call()
	ours, err := desktopName(uint32(tid))
	if err != nil {
		return windowprobe.Target{}, Preflight{}, err
	}
	theirs, err := desktopName(target.Window.TID)
	if err != nil {
		return windowprobe.Target{}, Preflight{}, err
	}
	if !strings.EqualFold(ours, theirs) {
		return windowprobe.Target{}, Preflight{}, fmt.Errorf("发送方与客户端不在同一 desktop")
	}
	// 哈希和身份查询期间可能出现新对话框或重启；返回前重新 Resolve，禁止换实例。
	target, err = windowprobe.Resolve(path, &target)
	if err != nil {
		return windowprobe.Target{}, Preflight{}, err
	}
	if !target.Window.Visible || target.Window.Minimized {
		return windowprobe.Target{}, Preflight{}, fmt.Errorf("复核时客户端不再正常显示")
	}
	return target, Preflight{PID: target.Process.PID, HWND: target.Window.HWND, CreationTime: target.Process.CreationTime, SHA256: digest, SessionID: session, SameUserAndIntegrity: true, SameDesktop: true, Reliability: target.Reliability}, nil
}

func hashClient(path string) (string, error) {
	name, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return "", err
	}
	// 核对期间拒绝其他程序写入或替换文件，不以可变路径内容作为哈希依据。
	h, err := syscall.CreateFile(name, syscall.GENERIC_READ, syscall.FILE_SHARE_READ, nil, syscall.OPEN_EXISTING, syscall.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		return "", fmt.Errorf("打开客户端文件: %w", err)
	}
	defer syscall.CloseHandle(h)
	hash := sha256.New()
	b := make([]byte, 64*1024)
	for {
		var n uint32
		if err := syscall.ReadFile(h, b, &n, nil); err != nil {
			return "", fmt.Errorf("读取客户端文件: %w", err)
		}
		if n == 0 {
			break
		}
		if _, err := hash.Write(b[:n]); err != nil {
			return "", err
		}
	}
	digest := fmt.Sprintf("%x", hash.Sum(nil))
	if digest != SampleSHA256 {
		return "", fmt.Errorf("客户端 SHA-256 不匹配: %s", digest)
	}
	return digest, nil
}

func openBoundProcess(target windowprobe.Target) (syscall.Handle, error) {
	h, err := syscall.OpenProcess(processQueryLimitedInformation|syscall.SYNCHRONIZE, false, target.Process.PID)
	if err != nil {
		return 0, fmt.Errorf("OpenProcess: %w", err)
	}
	var creation, exit, kernel, user syscall.Filetime
	if err := syscall.GetProcessTimes(h, &creation, &exit, &kernel, &user); err != nil {
		syscall.CloseHandle(h)
		return 0, err
	}
	if uint64(creation.HighDateTime)<<32|uint64(creation.LowDateTime) != target.Process.CreationTime {
		syscall.CloseHandle(h)
		return 0, fmt.Errorf("进程创建时间变化，拒绝使用旧 PID")
	}
	state, err := syscall.WaitForSingleObject(h, 0)
	if err != nil || state != syscall.WAIT_TIMEOUT {
		syscall.CloseHandle(h)
		return 0, fmt.Errorf("客户端已退出或等待检查失败: state=%d error=%v", state, err)
	}
	return h, nil
}

func sameSecurityContext(h syscall.Handle) error {
	var target, self syscall.Token
	if err := syscall.OpenProcessToken(h, syscall.TOKEN_QUERY, &target); err != nil {
		return fmt.Errorf("读取客户端身份元数据: %w", err)
	}
	defer target.Close()
	current, err := syscall.GetCurrentProcess()
	if err != nil {
		return err
	}
	if err := syscall.OpenProcessToken(current, syscall.TOKEN_QUERY, &self); err != nil {
		return err
	}
	defer self.Close()
	u1, err := target.GetTokenUser()
	if err != nil {
		return err
	}
	u2, err := self.GetTokenUser()
	if err != nil {
		return err
	}
	sid1, err := u1.User.Sid.String()
	if err != nil {
		return err
	}
	sid2, err := u2.User.Sid.String()
	if err != nil {
		return err
	}
	if sid1 != sid2 {
		return fmt.Errorf("发送方与客户端不是同一 Windows 用户")
	}
	i1, err := integritySID(target)
	if err != nil {
		return err
	}
	i2, err := integritySID(self)
	if err != nil {
		return err
	}
	if i1 != i2 {
		return fmt.Errorf("发送方与客户端完整性级别不同；禁止提权或修改消息过滤")
	}
	return nil
}

func integritySID(token syscall.Token) (string, error) {
	const tokenIntegrityLevel = 25
	var size uint32
	ok, _, err := getTokenInformation.Call(uintptr(token), tokenIntegrityLevel, 0, 0, uintptr(unsafe.Pointer(&size)))
	if ok != 0 || err != syscall.ERROR_INSUFFICIENT_BUFFER {
		return "", fmt.Errorf("查询完整性信息长度: %v", err)
	}
	b := make([]byte, size)
	ok, _, err = getTokenInformation.Call(uintptr(token), tokenIntegrityLevel, uintptr(unsafe.Pointer(&b[0])), uintptr(size), uintptr(unsafe.Pointer(&size)))
	if ok == 0 {
		return "", fmt.Errorf("读取完整性级别: %w", err)
	}
	label := (*syscall.SIDAndAttributes)(unsafe.Pointer(&b[0]))
	sid, err := label.Sid.String()
	runtime.KeepAlive(b)
	return sid, err
}

func desktopName(tid uint32) (string, error) {
	h, _, err := getThreadDesktop.Call(uintptr(tid))
	if h == 0 {
		return "", fmt.Errorf("GetThreadDesktop: %w", err)
	}
	// 返回的是线程现有 desktop，不能 CloseDesktop。
	var needed uint32
	ok, _, err := getUserObjectInformation.Call(h, 2, 0, 0, uintptr(unsafe.Pointer(&needed))) // UOI_NAME。
	if ok != 0 || err != syscall.ERROR_INSUFFICIENT_BUFFER {
		return "", fmt.Errorf("查询 desktop 名称长度: %v", err)
	}
	b := make([]uint16, (needed+1)/2)
	ok, _, err = getUserObjectInformation.Call(h, 2, uintptr(unsafe.Pointer(&b[0])), uintptr(len(b)*2), uintptr(unsafe.Pointer(&needed)))
	if ok == 0 {
		return "", fmt.Errorf("读取 desktop 名称: %w", err)
	}
	return syscall.UTF16ToString(b), nil
}

func sendOnce(target windowprobe.Target, b []byte) (CallResult, error) {
	if runtime.GOARCH != "386" {
		return CallResult{}, fmt.Errorf("本次实验只允许 Windows 386 构建")
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	h, err := openBoundProcess(target)
	if err != nil {
		return CallResult{}, err
	}
	defer syscall.CloseHandle(h)
	// 独立 message-only STATIC 窗口提供有效发送方 HWND，不创建目标客户端窗口。
	class, _ := syscall.UTF16PtrFromString("STATIC")
	sender, _, err := createWindowEx.Call(0, uintptr(unsafe.Pointer(class)), 0, 0, 0, 0, 0, 0, ^uintptr(2), 0, 0, 0)
	if sender == 0 {
		return CallResult{}, fmt.Errorf("创建发送方窗口: %w", err)
	}
	defer destroyWindow.Call(sender)
	var pid uint32
	tid, _, err := getWindowThreadProcessID.Call(uintptr(target.Window.HWND), uintptr(unsafe.Pointer(&pid)))
	if tid == 0 || pid != target.Process.PID || uint32(tid) != target.Window.TID {
		return CallResult{}, fmt.Errorf("调用前窗口身份变化: %v", err)
	}
	cd := copyData{Data: businessData, Size: uint32(len(b)), Payload: unsafe.Pointer(&b[0])}
	var result uintptr
	// 先解析延迟加载符号，避免首次解析覆盖刚清零的 LastError。
	if err := sendMessageTimeout.Find(); err != nil {
		return CallResult{}, err
	}
	start := time.Now()
	setLastError.Call(0)
	api, _, callErr := sendMessageTimeout.Call(uintptr(target.Window.HWND), wmCopyData, sender, uintptr(unsafe.Pointer(&cd)), smtoAbortIfHung|smtoErrorOnExit, sendTimeoutMS, uintptr(unsafe.Pointer(&result)))
	// syscall.LazyProc.Call 的第三返回值是本次调用立即捕获的 LastError；线程固定避免清零与调用分离。
	runtime.KeepAlive(cd)
	runtime.KeepAlive(b)
	code := uint32(callErr.(syscall.Errno))
	call := classifyCall(uint64(api), uint64(result), code, time.Since(start))
	state, waitErr := syscall.WaitForSingleObject(h, 0)
	if waitErr != nil {
		return call, fmt.Errorf("调用后进程存活检查失败: %w", waitErr)
	}
	alive := state == syscall.WAIT_TIMEOUT
	call.ClientAliveAfter = &alive
	if !alive {
		return call, fmt.Errorf("调用后客户端已退出或状态异常=%d，立即停止；结果 unknown", state)
	}
	return call, nil
}
