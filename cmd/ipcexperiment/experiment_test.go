// 本文件验证命令只负责确认和观察，所有发送与记录委托给替代Adapter，不接触真实客户端。
package main

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"

	"neiwangtong-gateway/internal/ipc"
)

const syntheticGroupID = "0123456789abcdef0123456789ABCDEF"
const syntheticAttemptID = "NWT-GW-CLI-SYNTHETIC-1"

type fakeAdapter struct {
	calls          int
	groupID        string
	message        string
	observationIDs []string
	observations   []ipc.Observation
	sendErr        error
	observationErr error
}

// Preflight 返回合成身份信息，不执行窗口查询或输出，且不会报错。
func (a *fakeAdapter) Preflight() (ipc.Preflight, error) { return ipc.Preflight{}, nil }

// SendGroup 保存命令传入的目标和正文，返回合成记录标识、调用事实及注入错误，不执行IPC。
func (a *fakeAdapter) SendGroup(groupID, message string) (ipc.SendResult, error) {
	a.calls++
	a.groupID, a.message = groupID, message
	return ipc.SendResult{AttemptID: syntheticAttemptID, AttemptReserved: true, Call: ipc.CallResult{Invoked: true, Completed: a.sendErr == nil, APIReturn: 1, LResult: 1, LResultValid: true}, Delivery: ipc.Unknown}, a.sendErr
}

// RecordObservation 记录命令委托的观察，模拟独立接收判定或注入错误，不访问日志。
func (a *fakeAdapter) RecordObservation(attemptID string, observation ipc.Observation) (ipc.Delivery, error) {
	a.observationIDs = append(a.observationIDs, attemptID)
	a.observations = append(a.observations, observation)
	if a.observationErr != nil {
		return ipc.Unknown, a.observationErr
	}
	if observation.Kind == ipc.RecipientObservation && observation.State == "one" {
		return ipc.RecipientObserved, nil
	}
	return ipc.Unknown, nil
}

// confirmedInput 返回合成群的完整确认及观察输入；不读取真实配置或提供真实群ID。
func confirmedInput(observations string) *strings.Reader {
	return strings.NewReader("SEND ONCE " + syntheticGroupID + " " + ipc.Message + "\n" + observations)
}

// TestFixedMessageConfirmation 验证确认使用原固定正文，而非接受任意消息文本。
func TestFixedMessageConfirmation(t *testing.T) {
	a := &fakeAdapter{}
	input := strings.NewReader("SEND ONCE " + syntheticGroupID + " other-message\n")
	if err := runWithAdapter(syntheticGroupID, true, input, io.Discard, a); err == nil || a.calls != 0 {
		t.Fatal("其他正文的确认触发发送")
	}
}

// TestConfirmationExact 验证确认不能通过截断、空白修正或只有yes获得，检查留在命令层。
func TestConfirmationExact(t *testing.T) {
	for _, input := range []string{"", "yes\n", "SEND ONCE " + syntheticGroupID + " " + ipc.Message, " SEND ONCE " + syntheticGroupID + " " + ipc.Message + "\n"} {
		a := &fakeAdapter{}
		if err := runWithAdapter(syntheticGroupID, true, strings.NewReader(input), io.Discard, a); err == nil {
			t.Fatal("错误确认被接受")
		}
		if a.calls != 0 || len(a.observations) != 0 {
			t.Fatal("未确认就调用Adapter发送")
		}
	}
}

// TestCLIReadOnly 验证只读命令不需要确认、不输出群ID，也不调用发送或观察写入。
func TestCLIReadOnly(t *testing.T) {
	a := &fakeAdapter{}
	var out bytes.Buffer
	if err := runWithAdapter(syntheticGroupID, false, strings.NewReader(""), &out, a); err != nil {
		t.Fatal(err)
	}
	if a.calls != 0 || len(a.observations) != 0 || strings.Contains(out.String(), syntheticGroupID) {
		t.Fatal("只读命令触发副作用或泄露群ID")
	}
}

// TestCLIUsesAdapter 验证确认后只调用一次Adapter，并将三类外部观察原样交给模块记录。
func TestCLIUsesAdapter(t *testing.T) {
	a := &fakeAdapter{}
	var out bytes.Buffer
	if err := runWithAdapter(syntheticGroupID, true, confirmedInput("stable\none\none\n"), &out, a); err != nil {
		t.Fatal(err)
	}
	if a.calls != 1 || a.groupID != syntheticGroupID || a.message != ipc.Message || len(a.observations) != 3 {
		t.Fatal("命令未完整委托给Adapter")
	}
	for i, kind := range []ipc.ObservationKind{ipc.ClientObservation, ipc.SenderObservation, ipc.RecipientObservation} {
		if a.observations[i].Kind != kind || a.observationIDs[i] != syntheticAttemptID {
			t.Fatal("观察来源顺序错误或未使用Adapter返回的记录标识")
		}
	}
	if !strings.Contains(out.String(), "recipient_observed") {
		t.Fatal("未显示模块返回的接收观察")
	}
}

// TestCLIAdapterFailureStops 验证模块失败立即返回，不继续索取UI观察或重复调用。
func TestCLIAdapterFailureStops(t *testing.T) {
	want := errors.New("timeout")
	a := &fakeAdapter{sendErr: want}
	err := runWithAdapter(syntheticGroupID, true, confirmedInput(""), io.Discard, a)
	if !errors.Is(err, want) || a.calls != 1 || len(a.observations) != 0 {
		t.Fatal("模块错误未直接返回")
	}
}

// TestCLIObservationFailureStops 验证模块观察错误令命令立即停止，不询问后续项目或再次发送。
func TestCLIObservationFailureStops(t *testing.T) {
	want := errors.New("client unknown")
	a := &fakeAdapter{observationErr: want}
	err := runWithAdapter(syntheticGroupID, true, confirmedInput("unknown\none\none\n"), io.Discard, a)
	if !errors.Is(err, want) || a.calls != 1 || len(a.observations) != 1 {
		t.Fatal("观察错误后仍继续")
	}
}

// TestCLIInterruptedObservation 验证调用后输入结束只报告缺少观察，不伪造结果或再发消息。
func TestCLIInterruptedObservation(t *testing.T) {
	a := &fakeAdapter{}
	if err := runWithAdapter(syntheticGroupID, true, confirmedInput(""), io.Discard, a); err == nil {
		t.Fatal("缺少观察却成功")
	}
	if a.calls != 1 || len(a.observations) != 0 {
		t.Fatal("中断后产生伪观察或重发")
	}
}
