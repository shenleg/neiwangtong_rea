// 本文件将客户端绑定、显式目标与自定义正文发送、持久化调用记录封装为非交互式Adapter。
package ipc

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"regexp"
	"runtime"
	"sync"
	"time"

	"neiwangtong-gateway/internal/windowprobe"
)

// 保留历史标识格式以读取旧日志；固定前缀排除Windows设备名与路径穿越，长度只限制文件名。
var attemptIDPattern = regexp.MustCompile(`^NWT-GW-[A-Za-z0-9][A-Za-z0-9_-]{0,120}$`)

type dependencies struct {
	check        func(string, *windowprobe.Target) (windowprobe.Target, Preflight, error)
	send         func(windowprobe.Target, []byte) (CallResult, error)
	journalPath  func(string) (string, error)
	allowSend    func() error
	newAttemptID func() (string, error)
}

// Adapter 绑定一个正常登录的本机客户端实例，接收目标由每次发送调用显式指定，不检查目标授权名单。
// 方法可并发调用，同一Adapter内串行执行；零值不可用，必须通过NewAdapter创建。
// 每次发送内部生成独立记录标识，不读取控制台、不自动重试；再次调用就是新的发送请求。
type Adapter struct {
	mu      sync.Mutex
	exePath string
	target  windowprobe.Target
	d       dependencies
}

// NewAdapter 只读校验并绑定exePath指定的唯一客户端，返回可复用Adapter，不创建发送记录或发送。
// 路径、窗口、样本或安全上下文错误时返回错误；不配置或绑定接收目标。
func NewAdapter(exePath string) (*Adapter, error) {
	return newAdapter(exePath, dependencies{
		check:        checkClient,
		send:         sendOnce,
		journalPath:  journalPath,
		allowSend:    allowSend,
		newAttemptID: newAttemptID,
	})
}

func newAdapter(exePath string, d dependencies) (*Adapter, error) {
	target, _, err := d.check(exePath, nil)
	if err != nil {
		return nil, err
	}
	return &Adapter{exePath: exePath, target: target, d: d}, nil
}

// Preflight 只读重新核对绑定的同一客户端与窗口，返回结构化身份信息；不输出、不创建日志或发送。
// 实例、窗口或安全上下文变化时返回错误，不自动切换目标。
func (a *Adapter) Preflight() (Preflight, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	target, info, err := a.d.check(a.exePath, &a.target)
	if err != nil {
		return Preflight{}, err
	}
	a.target = target
	return info, nil
}

// SendGroup 使用已绑定客户端向groupID指定的原生群发送一次message，不检查调用方的目标权限。
// groupID须为32个十六进制字符；message须为非空有效UTF-8且无NUL，按内容检查序列化容量。
// 字面的\n和\r\n分别转为实际LF和CRLF，实际换行保留；不解释其他转义或自动拆分正文。
// 内部生成记录标识并通过SendResult.AttemptID返回；每次调用是新请求，不自动重试。
// 输入、平台、身份、日志或Win32失败时返回错误及已发生的调用事实；Delivery始终为Unknown，不代表送达。
func (a *Adapter) SendGroup(groupID, message string) (SendResult, error) {
	return a.send("group_id", groupID, message)
}

// SendUser 使用已绑定客户端向userID指定的原始用户发送一次message，不要求预配置用户或检查目标权限。
// userID须为1至199字节有效UTF-8且不含NUL，不按名称/IP查找或改写ID；正文规则与SendGroup相同。
// 内部生成记录标识并通过SendResult.AttemptID返回；每次调用是新请求，不自动重试。
// 输入、平台、身份、日志或Win32失败时返回错误及已发生的调用事实；Delivery始终为Unknown，不代表送达。
func (a *Adapter) SendUser(userID, message string) (SendResult, error) {
	return a.send("user_id", userID, message)
}

func (a *Adapter) send(selector, targetID, message string) (result SendResult, err error) {
	result.Delivery = Unknown
	message, err = prepareMessage(message)
	if err != nil {
		return result, err
	}
	if selector == "group_id" && !groupIDPattern.MatchString(targetID) {
		return result, fmt.Errorf("群ID必须是32个十六进制字符；未发送")
	}
	payload, err := buildPayload(selector, targetID, message)
	if err != nil {
		return result, err
	}
	if err := a.d.allowSend(); err != nil {
		return result, err
	}
	attemptID, err := a.d.newAttemptID()
	if err != nil {
		return result, err
	}
	result.AttemptID = attemptID

	a.mu.Lock()
	defer a.mu.Unlock()
	target, info, err := a.d.check(a.exePath, &a.target)
	if err != nil {
		return result, err
	}
	a.target = target
	result.Preflight = info
	path, err := a.d.journalPath(attemptID)
	if err != nil {
		return result, err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return result, fmt.Errorf("拒绝创建发送记录（已存在则停止，不更换标识重试）: %w", err)
	}
	result.AttemptReserved = true
	defer func() { err = errors.Join(err, f.Close()) }()
	if err := appendEvent(f, map[string]any{"event": "attempt_reserved", "time": time.Now().UTC(), "attempt_id": attemptID, selector: targetID, "text": message, "preflight": info, "cbData": len(payload), "delivery": Unknown}); err != nil {
		return result, err
	}
	// 记录已占用，最终身份检查失败也保留；不能把unknown结果当作未发送而自动重试。
	target, info, err = a.d.check(a.exePath, &target)
	if err != nil {
		return result, recordFailure(f, err)
	}
	a.target = target
	result.Preflight = info
	call, callErr := a.d.send(target, payload)
	result.Call = call
	if err := appendEvent(f, map[string]any{"event": "call_result", "time": time.Now().UTC(), "result": call, "delivery": Unknown, "error": errorText(callErr)}); err != nil {
		return result, errors.Join(callErr, err)
	}
	if callErr != nil {
		return result, callErr
	}
	if !call.Completed {
		return result, fmt.Errorf("Win32调用未成功完成: error=%d timeout=%s；结果unknown，不重试", call.LastError, call.Timeout)
	}
	return result, nil
}

func newAttemptID() (string, error) {
	// 128位随机值用于独立调用记录；磁盘仍独占创建，发生碰撞时直接失败，不重试。
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return "", fmt.Errorf("生成发送记录标识: %w", err)
	}
	return "NWT-GW-" + hex.EncodeToString(id[:]), nil
}

func validateAttemptID(attemptID string) error {
	if !attemptIDPattern.MatchString(attemptID) {
		return fmt.Errorf("独立尝试标识须以NWT-GW-开头，总长8至128字符，后缀首字符为字母或数字且仅含ASCII字母数字、下划线或连字符")
	}
	return nil
}

func allowSend() error {
	if runtime.GOOS != "windows" || runtime.GOARCH != "386" {
		return fmt.Errorf("实际IPC发送只允许Windows 386构建")
	}
	return nil
}
