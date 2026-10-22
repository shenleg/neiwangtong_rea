// 本文件控制人工确认、调用前记录和单次执行；API 返回与人工观察分开保存。
package ipcexperiment

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"neiwangtong-gateway/internal/windowprobe"
)

// Preflight 是本次只读检查的非内容性结果，不含窗口标题或账户 SID。
type Preflight struct {
	PID                  uint32             `json:"pid"`
	HWND                 windowprobe.Handle `json:"hwnd"`
	CreationTime         uint64             `json:"creation_time_ticks,string"`
	SHA256               string             `json:"sha256"`
	SessionID            uint32             `json:"session_id"`
	SameUserAndIntegrity bool               `json:"same_user_and_integrity"`
	SameDesktop          bool               `json:"same_desktop"`
	Reliability          string             `json:"reliability"`
}

// CallResult 保存一次 Win32 调用结果；LResult 仅在 Completed 时有效，不代表送达。
type CallResult struct {
	Invoked          bool   `json:"invoked"`
	APIReturn        uint64 `json:"api_return"`
	Completed        bool   `json:"completed"`
	LastError        uint32 `json:"last_error"`
	LResult          uint64 `json:"lresult"`
	LResultValid     bool   `json:"lresult_valid"`
	Timeout          string `json:"timeout"`
	ElapsedMS        int64  `json:"elapsed_ms"`
	ClientAliveAfter *bool  `json:"client_alive_after"`
}

type dependencies struct {
	check       func(string, *windowprobe.Target) (windowprobe.Target, Preflight, error)
	send        func(windowprobe.Target, []byte) (CallResult, error)
	journalPath func() (string, error)
}

// Run 默认只检查 exePath 对应实例；send=true 时还要求已绑定的 groupID 和逐字人工确认。
// in/out 用于确认及观察输入/输出；返回定位、身份、输入、持久化或调用错误，不重试。
// 实际消息固定为 Message；本函数只用于隔离实验，不提供正式 Adapter 的送达语义。
func Run(exePath, groupID string, send bool, in io.Reader, out io.Writer) error {
	return run(exePath, groupID, send, in, out, dependencies{checkClient, sendOnce, journalPath})
}

func run(exePath, groupID string, send bool, in io.Reader, out io.Writer, d dependencies) error {
	if send && groupID == "" {
		return fmt.Errorf("尚未绑定唯一授权 group_id；本轮禁止实际发送")
	}
	var payload []byte
	var err error
	if send {
		payload, err = buildPayload(groupID)
		if err != nil {
			return err
		}
	}
	target, info, err := d.check(exePath, nil)
	if err != nil {
		return err
	}
	if err := json.NewEncoder(out).Encode(info); err != nil {
		return err
	}
	if !send {
		return nil
	}

	path, err := d.journalPath()
	if err != nil {
		return err
	}
	phrase := "SEND ONCE " + groupID + " " + Message
	if _, err := fmt.Fprintf(out, "实验仅限隔离环境、正常登录的授权账号和仅含授权成员的测试群。\n请确认当前实例及独立授权接收方已就绪；不得重试。\ngroup_id=%s\ntext=%s\ncbData=%d（实验长度，非厂商协议）\n记录=%s\n输入以下完整一行才会调用一次：\n%s\n", groupID, Message, len(payload), path, phrase); err != nil {
		return err
	}
	reader := bufio.NewReader(in)
	answer, err := readLine(reader)
	if err != nil {
		return fmt.Errorf("发送确认: %w", err)
	}
	if answer != phrase {
		return fmt.Errorf("未收到逐字确认，未发送")
	}

	// 确认等待期间允许状态变化，但不能自动换实例或在出现歧义后继续。
	target, info, err = d.check(exePath, &target)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return fmt.Errorf("拒绝创建单次尝试记录（已存在则禁止重发）: %w", err)
	}
	defer f.Close()
	if err := appendEvent(f, map[string]any{"event": "attempt_reserved", "time": time.Now().UTC(), "group_id": groupID, "text": Message, "preflight": info, "cbData": len(payload), "delivery": "unknown"}); err != nil {
		return err
	}
	// 记录落盘后再做最终定位；失败同样保留已占用记录，不自动恢复发送资格。
	target, _, err = d.check(exePath, &target)
	if err != nil {
		return recordFailure(f, err)
	}
	result, callErr := d.send(target, payload)
	if err := appendEvent(f, map[string]any{"event": "call_result", "time": time.Now().UTC(), "result": result, "delivery": "unknown", "error": errorText(callErr)}); err != nil {
		return err
	}
	if err := json.NewEncoder(out).Encode(result); err != nil {
		return err
	}
	if callErr != nil {
		return callErr
	}
	// 任何返回都不能触发第二次发送；失败/超时也只允许人工观察。
	if _, err := fmt.Fprintln(out, "调用结束，不会重试。请仅观察本条测试消息，不读取历史。\n客户端状态（stable/crashed/abnormal/unknown）："); err != nil {
		return err
	}
	client, err := observation(reader, "stable", "crashed", "abnormal", "unknown")
	if err != nil {
		return err
	}
	if err := appendEvent(f, map[string]any{"event": "client_observation", "state": client, "delivery": "unknown"}); err != nil {
		return err
	}
	if client != "stable" {
		return fmt.Errorf("客户端状态=%s，停止；送达结果 unknown", client)
	}
	if _, err := fmt.Fprintln(out, "发送方当前授权测试群是否恰好出现一条固定文本（one/none/multiple/wrong_target/unknown）："); err != nil {
		return err
	}
	local, err := observation(reader, "one", "none", "multiple", "wrong_target", "unknown")
	if err != nil {
		return err
	}
	if err := appendEvent(f, map[string]any{"event": "sender_observation", "state": local, "delivery": "unknown"}); err != nil {
		return err
	}
	if local == "multiple" || local == "wrong_target" {
		return fmt.Errorf("发送方观察异常=%s，立即停止", local)
	}
	if _, err := fmt.Fprintln(out, "独立授权接收账号是否在正确原生群看见正确发送者的一条固定文本（one/none/multiple/wrong_target/unknown）："); err != nil {
		return err
	}
	recipient, err := observation(reader, "one", "none", "multiple", "wrong_target", "unknown")
	if err != nil {
		return err
	}
	delivery := "unknown"
	if recipient == "one" {
		delivery = "recipient_observed"
	}
	if err := appendEvent(f, map[string]any{"event": "recipient_observation", "state": recipient, "delivery": delivery}); err != nil {
		return err
	}
	if recipient == "multiple" || recipient == "wrong_target" {
		return fmt.Errorf("接收方观察异常=%s，立即停止", recipient)
	}
	if _, err := fmt.Fprintf(out, "独立接收观察=%s；不代表全群送达。正式 Adapter 是否开发须另行评估，程序不自动授权。\n", delivery); err != nil {
		return err
	}
	if !result.Completed {
		return fmt.Errorf("Win32 调用未成功完成: error=%d timeout=%s；接收观察=%s，不重试", result.LastError, result.Timeout, delivery)
	}
	return nil
}

func readLine(r *bufio.Reader) (string, error) {
	line, err := r.ReadString('\n')
	if err != nil {
		return "", err
	}
	return strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r"), nil
}

func observation(r *bufio.Reader, allowed ...string) (string, error) {
	line, err := readLine(r)
	if err != nil {
		return "", fmt.Errorf("人工观察未完成，结果仍为 unknown: %w", err)
	}
	for _, value := range allowed {
		if line == value {
			return line, nil
		}
	}
	return "", fmt.Errorf("无效的观察值 %q，停止；不得重新发送", line)
}

func journalPath() (string, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(base, "neiwangtong-gateway", "ipc-experiment")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", err
	}
	return filepath.Join(dir, Message+".jsonl"), nil
}

func appendEvent(f *os.File, event any) error {
	if err := json.NewEncoder(f).Encode(event); err != nil {
		return fmt.Errorf("写入尝试记录: %w", err)
	}
	if err := f.Sync(); err != nil {
		return fmt.Errorf("尝试记录落盘: %w", err)
	}
	return nil
}

func recordFailure(f *os.File, cause error) error {
	if err := appendEvent(f, map[string]any{"event": "stopped_before_call", "error": cause.Error(), "delivery": "unknown"}); err != nil {
		return fmt.Errorf("%v; %w", cause, err)
	}
	return cause
}

func errorText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
