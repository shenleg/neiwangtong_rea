//go:build windows

// 本文件仅向测试子进程自己的隐藏窗口发送 WM_COPYDATA，不调用内网通窗口。
package ipc

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"
	"unsafe"

	"neiwangtong-gateway/internal/windowprobe"
)

// TestNativeLayout 验证 COPYDATASTRUCT 在两种发送进程 ABI 下的偏移和总大小。
func TestNativeLayout(t *testing.T) {
	var cd copyData
	wantSize, wantPayload := uintptr(12), uintptr(8)
	if runtime.GOARCH == "amd64" {
		wantSize, wantPayload = 24, 16
	}
	if unsafe.Sizeof(cd) != wantSize || unsafe.Offsetof(cd.Size) != unsafe.Sizeof(uintptr(0)) || unsafe.Offsetof(cd.Payload) != wantPayload {
		t.Fatalf("错误 ABI: size=%d cbData=%d lpData=%d", unsafe.Sizeof(cd), unsafe.Offsetof(cd.Size), unsafe.Offsetof(cd.Payload))
	}
}

// TestHashRejectsMismatch 验证文件读取后的哈希不符会明确拒绝，不采用文件名或版本回退。
func TestHashRejectsMismatch(t *testing.T) {
	path := filepath.Join(t.TempDir(), "synthetic.exe")
	if err := os.WriteFile(path, []byte("synthetic-not-a-client"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := hashClient(path); err == nil || !strings.Contains(err.Error(), "SHA-256 不匹配") {
		t.Fatalf("未拒绝错误样本: %v", err)
	}
}

// TestOwnContext 验证只读 SID/完整性级别及 desktop 查询，且不输出 SID 或读取凭据。
func TestOwnContext(t *testing.T) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	h, err := syscall.GetCurrentProcess()
	if err != nil {
		t.Fatal(err)
	}
	if err := sameSecurityContext(h); err != nil {
		t.Fatal(err)
	}
	tid, _, _ := getCurrentThreadID.Call()
	name, err := desktopName(uint32(tid))
	if err != nil || name == "" {
		t.Fatalf("desktop 查询失败: %v", err)
	}
}

// TestReceiverProcess 是测试子进程入口；只创建自身隐藏窗口，接收后将合成载荷回报给父测试。
func TestReceiverProcess(t *testing.T) {
	if os.Getenv("NWT_IPC_FIXTURE") != "1" {
		t.Skip("仅由跨进程 fixture 启动")
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	class, _ := syscall.UTF16PtrFromString("STATIC")
	hwnd, _, err := createWindowEx.Call(0, uintptr(unsafe.Pointer(class)), 0, 0, 0, 0, 0, 0, ^uintptr(2), 0, 0, 0)
	if hwnd == 0 {
		t.Fatal(err)
	}
	defer destroyWindow.Call(hwnd)
	defWindowProc := user32.NewProc("DefWindowProcW")
	callback := syscall.NewCallback(func(w, msg, wp uintptr, lp unsafe.Pointer) uintptr {
		if msg != wmCopyData {
			r, _, _ := defWindowProc.Call(w, msg, wp, uintptr(lp))
			return r
		}
		cd := (*copyData)(lp)
		data := append([]byte(nil), unsafe.Slice((*byte)(cd.Payload), int(cd.Size))...)
		_ = json.NewEncoder(os.Stdout).Encode(struct {
			Data    uint64
			Payload []byte
			Sender  bool
		}{uint64(cd.Data), data, wp != 0})
		if os.Getenv("NWT_IPC_FIXTURE_DELAY") == "1" {
			time.Sleep(6 * time.Second)
		}
		return 1
	})
	subclass := user32.NewProc("SetWindowLongW")
	if runtime.GOARCH == "amd64" {
		subclass = user32.NewProc("SetWindowLongPtrW")
	}
	// GWLP_WNDPROC=-4；仅修改本测试子进程自己的窗口，绝不对客户端执行。
	old, _, err := subclass.Call(hwnd, ^uintptr(3), callback)
	if old == 0 {
		t.Fatal(err)
	}
	h, err := syscall.GetCurrentProcess()
	if err != nil {
		t.Fatal(err)
	}
	var creation, exit, kernel, user syscall.Filetime
	if err := syscall.GetProcessTimes(h, &creation, &exit, &kernel, &user); err != nil {
		t.Fatal(err)
	}
	tid, _, _ := getCurrentThreadID.Call()
	target := windowprobe.Target{Process: windowprobe.Process{PID: uint32(os.Getpid()), CreationTime: uint64(creation.HighDateTime)<<32 | uint64(creation.LowDateTime)}, Window: windowprobe.Window{HWND: windowprobe.Handle(hwnd), TID: uint32(tid)}}
	if err := json.NewEncoder(os.Stdout).Encode(target); err != nil {
		t.Fatal(err)
	}
	go func() {
		_, _ = io.Copy(io.Discard, os.Stdin)
		user32.NewProc("PostThreadMessageW").Call(tid, 0x12, 0, 0)
	}() // WM_QUIT 在父进程关闭 stdin 后结束 fixture。
	// MSG 中 lPrivate 是 Win32 的尾字段；使用原生 uintptr 保持 ABI。
	var msg struct {
		HWND           uintptr
		Message        uint32
		WParam, LParam uintptr
		Time           uint32
		X, Y           int32
		Private        uint32
	}
	getMessage := user32.NewProc("GetMessageW")
	for {
		ok, _, err := getMessage.Call(uintptr(unsafe.Pointer(&msg)), 0, 0, 0)
		if int32(ok) == -1 {
			t.Fatal(err)
		}
		if ok == 0 {
			break
		}
		user32.NewProc("TranslateMessage").Call(uintptr(unsafe.Pointer(&msg)))
		user32.NewProc("DispatchMessageW").Call(uintptr(unsafe.Pointer(&msg)))
	}
}

// receiverFixture 启动只接收合成数据的独立测试进程，返回定位信息、结果解码器和清理函数。
// delay 控制接收处理是否超过发送期限；启动或握手失败会终止测试，清理只关闭本测试的 stdin。
func receiverFixture(t *testing.T, delay bool) (windowprobe.Target, *json.Decoder, func()) {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestReceiverProcess$", "-test.timeout=20s")
	cmd.Env = append(os.Environ(), "NWT_IPC_FIXTURE=1", fmt.Sprintf("NWT_IPC_FIXTURE_DELAY=%d", map[bool]int{false: 0, true: 1}[delay]))
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	cleanup := func() {
		_ = stdin.Close()
		_, _ = io.Copy(io.Discard, stdout)
		if err := cmd.Wait(); err != nil {
			t.Errorf("fixture 退出: %v; %s", err, stderr.String())
		}
	}
	decoder := json.NewDecoder(stdout)
	var target windowprobe.Target
	if err := decoder.Decode(&target); err != nil {
		cleanup()
		t.Fatal(err)
	}
	return target, decoder, cleanup
}

// TestCrossProcessSend 验证真实跨进程复制、固定业务标记、有效发送方 HWND 及 LRESULT 分离。
func TestCrossProcessSend(t *testing.T) {
	if runtime.GOARCH != "386" {
		t.Skip("本次实验只允许 386 发送，amd64 只测 ABI 和只读身份")
	}
	target, decoder, cleanup := receiverFixture(t, false)
	defer cleanup()
	payload, err := buildPayload("synthetic-group_1")
	if err != nil {
		t.Fatal(err)
	}
	r, err := sendOnce(target, payload)
	if err != nil {
		t.Fatal(err)
	}
	if !r.Completed || !r.LResultValid || r.LResult != 1 || r.Timeout != "no" {
		t.Fatalf("调用结果错误: %+v", r)
	}
	var observed struct {
		Data    uint64
		Payload []byte
		Sender  bool
	}
	if err := decoder.Decode(&observed); err != nil {
		t.Fatal(err)
	}
	if observed.Data != businessData || !observed.Sender || !bytes.Equal(observed.Payload, payload) {
		t.Fatal("跨进程接收字节或发送方 HWND 不符")
	}
}

// TestCrossProcessTimeout 验证 fixture 已接收载荷但处理未结束时仍会超时，且不重复调用。
func TestCrossProcessTimeout(t *testing.T) {
	if runtime.GOARCH != "386" {
		t.Skip("仅在 386 测试真实超时调用")
	}
	target, decoder, cleanup := receiverFixture(t, true)
	defer cleanup()
	payload, err := buildPayload("synthetic-group_1")
	if err != nil {
		t.Fatal(err)
	}
	r, err := sendOnce(target, payload)
	if err != nil {
		t.Fatal(err)
	}
	if r.Completed || r.LResultValid || r.ElapsedMS > 8000 {
		t.Fatalf("超时调用结果错误: %+v", r)
	}
	if r.Timeout != "yes" && r.Timeout != "not_reported" {
		t.Fatalf("超时分类错误: %+v", r)
	}
	var observed struct {
		Data    uint64
		Payload []byte
		Sender  bool
	}
	if err := decoder.Decode(&observed); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(observed.Payload, payload) {
		t.Fatal("超时不能解释为接收端没有处理载荷")
	}
	t.Logf("fixture 超时调用结果: %+v；接收端已复制合成载荷", r)
}
