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
const syntheticUserID = "synthetic-user_1"
const syntheticAttemptID = "NWT-GW-CLI-SYNTHETIC-1"

// groupTarget 返回合成群发送目标。
func groupTarget() sendTarget { return sendTarget{targetGroup, syntheticGroupID} }

// userTarget 返回合成用户发送目标。
func userTarget() sendTarget { return sendTarget{targetUser, syntheticUserID} }

type fakeAdapter struct {
	calls          int
	groupID        string
	userID         string
	message        string
	observationIDs []string
	observations   []ipc.Observation
	sendErr        error
	observationErr error
}

// Preflight 返回合成身份信息，不执行窗口查询或输出，且不会报错。
func (a *fakeAdapter) Preflight() (ipc.Preflight, error) { return ipc.Preflight{}, nil }

// SendGroup 保存命令传入的群目标和正文，返回合成记录标识、调用事实及注入错误，不执行IPC。
func (a *fakeAdapter) SendGroup(groupID, message string) (ipc.SendResult, error) {
	a.calls++
	a.groupID, a.message = groupID, message
	return ipc.SendResult{AttemptID: syntheticAttemptID, AttemptReserved: true, Call: ipc.CallResult{Invoked: true, Completed: a.sendErr == nil, APIReturn: 1, LResult: 1, LResultValid: true}, Delivery: ipc.Unknown}, a.sendErr
}

// SendUser 保存命令传入的用户目标和正文，行为与SendGroup一致，不执行IPC。
func (a *fakeAdapter) SendUser(userID, message string) (ipc.SendResult, error) {
	a.calls++
	a.userID, a.message = userID, message
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

// confirmedInput 返回合成群目标的完整确认及观察输入；不读取真实配置或提供真实群ID。
func confirmedInput(observations string) *strings.Reader {
	return strings.NewReader("SEND ONCE " + syntheticGroupID + " " + ipc.Message + "\n" + observations)
}

// confirmedUserInput 返回合成用户目标的完整确认及观察输入。
func confirmedUserInput(observations string) *strings.Reader {
	return strings.NewReader("SEND ONCE " + syntheticUserID + " " + ipc.Message + "\n" + observations)
}

// TestFixedMessageConfirmation 验证确认使用原固定正文，而非接受任意消息文本。
func TestFixedMessageConfirmation(t *testing.T) {
	a := &fakeAdapter{}
	input := strings.NewReader("SEND ONCE " + syntheticGroupID + " other-message\n")
	if err := runWithAdapter(groupTarget(), ipc.Message, true, input, io.Discard, a); err == nil || a.calls != 0 {
		t.Fatal("其他正文的确认触发发送")
	}
}

// TestConfirmationExact 验证确认不能通过截断、空白修正或只有yes获得，检查留在命令层。
func TestConfirmationExact(t *testing.T) {
	for _, input := range []string{"", "yes\n", "SEND ONCE " + syntheticGroupID + " " + ipc.Message, " SEND ONCE " + syntheticGroupID + " " + ipc.Message + "\n"} {
		a := &fakeAdapter{}
		if err := runWithAdapter(groupTarget(), ipc.Message, true, strings.NewReader(input), io.Discard, a); err == nil {
			t.Fatal("错误确认被接受")
		}
		if a.calls != 0 || len(a.observations) != 0 {
			t.Fatal("未确认就调用Adapter发送")
		}
	}
}

// TestCLIReadOnly 验证只读命令不需要确认、不输出目标ID，也不调用发送或观察写入。
func TestCLIReadOnly(t *testing.T) {
	a := &fakeAdapter{}
	var out bytes.Buffer
	if err := runWithAdapter(groupTarget(), ipc.Message, false, strings.NewReader(""), &out, a); err != nil {
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
	if err := runWithAdapter(groupTarget(), ipc.Message, true, confirmedInput("stable\none\none\n"), &out, a); err != nil {
		t.Fatal(err)
	}
	if a.calls != 1 || a.groupID != syntheticGroupID || a.userID != "" || a.message != ipc.Message || len(a.observations) != 3 {
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

// TestCLIUserTargetUsesSendUser 验证用户目标确认后调用SendUser而非SendGroup，观察流程与群共用。
func TestCLIUserTargetUsesSendUser(t *testing.T) {
	a := &fakeAdapter{}
	var out bytes.Buffer
	if err := runWithAdapter(userTarget(), ipc.Message, true, confirmedUserInput("stable\none\none\n"), &out, a); err != nil {
		t.Fatal(err)
	}
	if a.calls != 1 || a.userID != syntheticUserID || a.groupID != "" || a.message != ipc.Message || len(a.observations) != 3 {
		t.Fatal("用户目标未正确委托给SendUser")
	}
	if !strings.Contains(out.String(), "user_id="+syntheticUserID) {
		t.Fatal("确认提示未标明 user_id 选择器")
	}
}

// TestCLIUserTargetRejectsGroupPhrase 验证群目标确认短语不能触发用户目标发送。
func TestCLIUserTargetRejectsGroupPhrase(t *testing.T) {
	a := &fakeAdapter{}
	if err := runWithAdapter(userTarget(), ipc.Message, true, confirmedInput("stable\none\none\n"), io.Discard, a); err == nil || a.calls != 0 {
		t.Fatal("群短语被用户目标接受")
	}
}

// TestCLICustomText 验证自定义正文的确认短语包含该正文，且原样传递给发送方法。
func TestCLICustomText(t *testing.T) {
	a := &fakeAdapter{}
	var out bytes.Buffer
	input := strings.NewReader("SEND ONCE " + syntheticUserID + " hello world\nstable\none\none\n")
	if err := runWithAdapter(userTarget(), "hello world", true, input, &out, a); err != nil {
		t.Fatal(err)
	}
	if a.calls != 1 || a.userID != syntheticUserID || a.message != "hello world" || len(a.observations) != 3 {
		t.Fatal("自定义正文未按确认短语发送")
	}
}

// TestCLICustomTextRejectsFixedPhrase 验证固定正文确认短语不能触发自定义正文发送。
func TestCLICustomTextRejectsFixedPhrase(t *testing.T) {
	a := &fakeAdapter{}
	if err := runWithAdapter(userTarget(), "hello world", true, confirmedUserInput(""), io.Discard, a); err == nil || a.calls != 0 {
		t.Fatal("固定正文短语被自定义正文接受")
	}
}

// TestCLIAdapterFailureStops 验证模块失败立即返回，不继续索取UI观察或重复调用。
func TestCLIAdapterFailureStops(t *testing.T) {
	want := errors.New("timeout")
	a := &fakeAdapter{sendErr: want}
	err := runWithAdapter(groupTarget(), ipc.Message, true, confirmedInput(""), io.Discard, a)
	if !errors.Is(err, want) || a.calls != 1 || len(a.observations) != 0 {
		t.Fatal("模块错误未直接返回")
	}
}

// TestCLIObservationFailureStops 验证模块观察错误令命令立即停止，不询问后续项目或再次发送。
func TestCLIObservationFailureStops(t *testing.T) {
	want := errors.New("client unknown")
	a := &fakeAdapter{observationErr: want}
	err := runWithAdapter(groupTarget(), ipc.Message, true, confirmedInput("unknown\none\none\n"), io.Discard, a)
	if !errors.Is(err, want) || a.calls != 1 || len(a.observations) != 1 {
		t.Fatal("观察错误后仍继续")
	}
}

// TestCLIInterruptedObservation 验证调用后输入结束只报告缺少观察，不伪造结果或再发消息。
func TestCLIInterruptedObservation(t *testing.T) {
	a := &fakeAdapter{}
	if err := runWithAdapter(groupTarget(), ipc.Message, true, confirmedInput(""), io.Discard, a); err == nil {
		t.Fatal("缺少观察却成功")
	}
	if a.calls != 1 || len(a.observations) != 0 {
		t.Fatal("中断后产生伪观察或重发")
	}
}

// TestRecordObservation 验证独立观察入口按标识与类别委托记录并输出送达判定。
func TestRecordObservation(t *testing.T) {
	a := &fakeAdapter{}
	var out bytes.Buffer
	if err := recordObservation(a, syntheticAttemptID, ipc.RecipientObservation, "one", &out); err != nil {
		t.Fatal(err)
	}
	if len(a.observationIDs) != 1 || a.observations[0].Kind != ipc.RecipientObservation ||
		a.observations[0].State != "one" || !strings.Contains(out.String(), "recipient_observed") {
		t.Fatal("观察未被正确记录或输出")
	}
}

// TestObservationKind 验证观察类别参数映射与未知类别拒绝。
func TestObservationKind(t *testing.T) {
	for name, want := range map[string]ipc.ObservationKind{
		"client":    ipc.ClientObservation,
		"sender":    ipc.SenderObservation,
		"recipient": ipc.RecipientObservation,
	} {
		got, err := observationKind(name)
		if err != nil || got != want {
			t.Errorf("类别 %q 映射错误: %v, %v", name, got, err)
		}
	}
	if _, err := observationKind("bogus"); err == nil {
		t.Error("未知类别未被拒绝")
	}
}
