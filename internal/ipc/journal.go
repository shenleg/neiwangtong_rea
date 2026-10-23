// 本文件维护兼容原实验目录的单次日志与外部观察，保留所有已占用或unknown记录，不修改旧事件。
package ipc

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"time"
)

func journalPath(attemptID string) (string, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	// 延用历史目录和原始尝试文件名，包或Adapter重构不得恢复旧任务资格。
	dir := filepath.Join(base, "neiwangtong-gateway", "ipc-experiment")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", err
	}
	return filepath.Join(dir, attemptID+".jsonl"), nil
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
	return errors.Join(cause, appendEvent(f, map[string]any{"event": "stopped_before_call", "error": cause.Error(), "delivery": Unknown}))
}

func errorText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

type storedEvent struct {
	Event     string      `json:"event"`
	AttemptID string      `json:"attempt_id"`
	GroupID   string      `json:"group_id"`
	Text      string      `json:"text"`
	Result    *CallResult `json:"result"`
}

// RecordObservation 为attemptID的既有实际调用追加调用者提供的观察，返回本条观察的投递判定。
// 只接受ClientObservation、SenderObservation、RecipientObservation及各自固定状态；不读取UI或发送消息。
// 无实际调用、配置目标不符、日志无效、状态无效或写入失败时返回错误；客户端异常/unknown、
// 多条或错误目标会先保存观察再返回错误。RecipientObserved只来自独立接收方的one，不推断全群送达。
func (a *Adapter) RecordObservation(attemptID string, observation Observation) (delivery Delivery, err error) {
	delivery = Unknown
	if err := validateAttemptID(attemptID); err != nil {
		return delivery, err
	}
	var states []string
	switch observation.Kind {
	case ClientObservation:
		states = []string{"stable", "crashed", "abnormal", "unknown"}
	case SenderObservation, RecipientObservation:
		states = []string{"one", "none", "multiple", "wrong_target", "unknown"}
	default:
		return delivery, fmt.Errorf("无效的观察来源")
	}
	if !slices.Contains(states, observation.State) {
		return delivery, fmt.Errorf("无效的观察状态")
	}

	a.mu.Lock()
	defer a.mu.Unlock()
	path, err := a.d.journalPath(attemptID)
	if err != nil {
		return delivery, err
	}
	f, err := os.OpenFile(path, os.O_RDWR|os.O_APPEND, 0600)
	if err != nil {
		return delivery, fmt.Errorf("打开既有尝试记录: %w", err)
	}
	defer func() { err = errors.Join(err, f.Close()) }()
	decoder := json.NewDecoder(f)
	reservations, calls := 0, 0
	var reserved storedEvent
	var call CallResult
	for {
		var event storedEvent
		if err := decoder.Decode(&event); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return delivery, fmt.Errorf("读取尝试记录: %w", err)
		}
		switch event.Event {
		case "attempt_reserved":
			reservations++
			reserved = event
		case "call_result":
			calls++
			if event.Result == nil {
				return delivery, fmt.Errorf("调用记录缺少结果")
			}
			call = *event.Result
		}
	}
	if reservations != 1 || calls != 1 || !call.Invoked {
		return delivery, fmt.Errorf("没有可追加观察的唯一实际调用")
	}
	if reserved.AttemptID != attemptID || reserved.GroupID != a.groupID || reserved.Text != Message {
		return delivery, fmt.Errorf("尝试记录与绑定的授权任务不符")
	}
	if observation.Kind == RecipientObservation && observation.State == "one" {
		delivery = RecipientObserved
	}
	if err := appendEvent(f, map[string]any{"event": observation.Kind, "time": time.Now().UTC(), "state": observation.State, "delivery": delivery}); err != nil {
		return delivery, err
	}
	if observation.Kind == ClientObservation && observation.State != "stable" {
		return delivery, fmt.Errorf("客户端状态=%s，停止；送达结果unknown", observation.State)
	}
	if observation.State == "multiple" || observation.State == "wrong_target" {
		return delivery, fmt.Errorf("观察结果=%s，停止；不得重发", observation.State)
	}
	return delivery, nil
}
