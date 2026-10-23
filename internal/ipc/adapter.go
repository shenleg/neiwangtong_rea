// 本文件将窗口身份、固定载荷、持久化单次控制和Win32调用封装为非交互式Adapter。
package ipc

import (
	"errors"
	"fmt"
	"os"
	"regexp"
	"runtime"
	"sync"
	"time"

	"neiwangtong-gateway/internal/windowprobe"
)

// 固定命名前缀排除Windows设备名与路径穿越；128字符上限仅限制日志文件名，不是IPC字段容量。
var attemptIDPattern = regexp.MustCompile(`^NWT-GW-[A-Za-z0-9][A-Za-z0-9_-]{0,120}$`)

type dependencies struct {
	check       func(string, *windowprobe.Target) (windowprobe.Target, Preflight, error)
	send        func(windowprobe.Target, []byte) (CallResult, error)
	journalPath func(string) (string, error)
	allowSend   func() error
}

// Adapter 绑定一个正常授权客户端实例与配置中的唯一原生群。
// 方法可并发调用，同一Adapter内串行执行；零值不可用，必须通过NewAdapter创建。
// 模块没有输入流、控制台确认、重试或自动任务ID；调用SendGroup代表上层已完成单次授权。
type Adapter struct {
	mu      sync.Mutex
	exePath string
	groupID string
	target  windowprobe.Target
	d       dependencies
}

// NewAdapter 校验配置并只读绑定exePath对应的唯一客户端，返回可复用Adapter，不创建尝试日志或发送。
// config为空时仅允许Preflight；配置、路径、窗口、样本或安全上下文错误时返回错误。
func NewAdapter(exePath string, config Config) (*Adapter, error) {
	return newAdapter(exePath, config, dependencies{checkClient, sendOnce, journalPath, allowSend})
}

func newAdapter(exePath string, config Config, d dependencies) (*Adapter, error) {
	if config.AuthorizedGroupID != "" && !groupIDPattern.MatchString(config.AuthorizedGroupID) {
		return nil, fmt.Errorf("授权群ID必须是32个十六进制字符")
	}
	target, _, err := d.check(exePath, nil)
	if err != nil {
		return nil, err
	}
	return &Adapter{exePath: exePath, groupID: config.AuthorizedGroupID, target: target, d: d}, nil
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

// SendGroup 向配置的唯一授权群发送一次固定Message，attemptID必须是上层明确授权的独立请求标识。
// 调用前独占持久化该标识，已有记录永久拒绝重复调用；不自动生成新标识或重试。
// 返回值始终保留已发生的调用事实且Delivery为Unknown；平台、身份、日志或Win32失败时同时返回错误。
// 此方法不读取输入、显示提示或等待接收观察，正常返回也不表示消息送达。
func (a *Adapter) SendGroup(attemptID string) (result SendResult, err error) {
	result = SendResult{AttemptID: attemptID, Delivery: Unknown}
	if err := validateAttemptID(attemptID); err != nil {
		return result, err
	}
	if a.groupID == "" {
		return result, fmt.Errorf("尚未绑定唯一授权group_id；未发送")
	}
	if err := a.d.allowSend(); err != nil {
		return result, err
	}
	payload, err := buildPayload(a.groupID)
	if err != nil {
		return result, err
	}

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
		return result, fmt.Errorf("拒绝创建单次尝试记录（已存在则禁止重发）: %w", err)
	}
	result.AttemptReserved = true
	defer func() { err = errors.Join(err, f.Close()) }()
	if err := appendEvent(f, map[string]any{"event": "attempt_reserved", "time": time.Now().UTC(), "attempt_id": attemptID, "group_id": a.groupID, "text": Message, "preflight": info, "cbData": len(payload), "delivery": Unknown}); err != nil {
		return result, err
	}
	// 日志已占用，最终身份检查失败也保留记录；确认等待由命令层处理，模块只核对绑定实例。
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
