// 本文件验证Adapter的显式目标、正文边界、自动标识、结构化错误和外部观察契约，只使用合成数据。
package ipc

import (
	"bytes"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"neiwangtong-gateway/internal/windowprobe"
)

// TestAdapterRejectsInvalidGroupID 验证非法群ID在生成标识、窗口查询及日志操作之前被拒绝。
func TestAdapterRejectsInvalidGroupID(t *testing.T) {
	d, path, calls := fixture(t)
	a := adapterFixture(t, d)
	a.d.check = func(string, *windowprobe.Target) (windowprobe.Target, Preflight, error) {
		t.Fatal("无效目标不应查询窗口")
		return windowprobe.Target{}, Preflight{}, nil
	}
	a.d.newAttemptID = func() (string, error) {
		t.Fatal("无效目标不应生成标识")
		return "", nil
	}
	for _, id := range []string{"", "group", strings.Repeat("a", 31), strings.Repeat("a", 33), strings.Repeat("a", 31) + "g", testGroupID + " ", "a\x00b", "bad\xff"} {
		result, err := a.SendGroup(id, Message)
		if err == nil || result.AttemptID != "" || result.AttemptReserved || result.Call.Invoked {
			t.Fatalf("无效群ID被执行: %q %+v %v", id, result, err)
		}
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) || *calls != 0 {
		t.Fatal("无效群ID产生副作用")
	}
}

// TestAdapterUsesEachTarget 验证同一Adapter连续发送不同群和用户，选择器及原始ID均来自当前参数。
func TestAdapterUsesEachTarget(t *testing.T) {
	d, _, calls := fixture(t)
	base := t.TempDir()
	d.newAttemptID = newAttemptID
	d.journalPath = func(id string) (string, error) { return filepath.Join(base, id+".jsonl"), nil }
	a := adapterFixture(t, d)
	for _, tc := range []struct{ selector, id string }{
		{"group_id", testGroupID},
		{"group_id", strings.Repeat("f", 32)},
		{"user_id", testUserID},
		{"user_id", " 原样 ID ,;| "},
	} {
		a.d.send = func(_ windowprobe.Target, payload []byte) (CallResult, error) {
			*calls++
			for offset, text := range map[int]string{selectorOffset: tc.selector, targetOffset: tc.id, bodyOffset: Message} {
				if string(payload[offset:offset+len(text)]) != text || payload[offset+len(text)] != 0 {
					t.Fatalf("本次目标未原样写入: selector=%s offset=%x", tc.selector, offset)
				}
			}
			return classifyCall(1, 1, 0, time.Millisecond), nil
		}
		send := a.SendGroup
		if tc.selector == "user_id" {
			send = a.SendUser
		}
		result, err := send(tc.id, Message)
		if err != nil || !result.Call.Invoked || result.Delivery != Unknown {
			t.Fatalf("显式目标发送失败: %+v %v", result, err)
		}
		data, err := os.ReadFile(filepath.Join(base, result.AttemptID+".jsonl"))
		if err != nil || !bytes.Contains(data, []byte(`"`+tc.selector+`":"`+tc.id+`"`)) {
			t.Fatalf("日志没有保留本次目标: %v", err)
		}
	}
	if *calls != 4 {
		t.Fatalf("发送次数=%d", *calls)
	}
}

// TestInvalidMessageHasNoSideEffects 验证非法或超预算正文不生成标识、不预检或占用日志。
func TestInvalidMessageHasNoSideEffects(t *testing.T) {
	d, path, calls := fixture(t)
	a := adapterFixture(t, d)
	a.d.check = func(string, *windowprobe.Target) (windowprobe.Target, Preflight, error) {
		t.Fatal("错误正文仍预检")
		return windowprobe.Target{}, Preflight{}, nil
	}
	a.d.allowSend = func() error { t.Fatal("错误正文仍检查发送平台"); return nil }
	a.d.newAttemptID = func() (string, error) { t.Fatal("错误正文仍生成标识"); return "", nil }
	a.d.journalPath = func(string) (string, error) { t.Fatal("错误正文仍请求日志路径"); return "", nil }
	for _, message := range []string{"", Message + "\x00", "bad\xff", strings.Repeat("a.aa ", 334), strings.Repeat("x", escapedJSONBudget)} {
		for _, send := range []func(string, string) (SendResult, error){a.SendGroup, a.SendUser} {
			result, err := send(testGroupID, message)
			if err == nil || result.AttemptID != "" || result.AttemptReserved || result.Call.Invoked || result.Delivery != Unknown {
				t.Fatalf("错误正文产生副作用: %+v %v", result, err)
			}
		}
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) || *calls != 0 {
		t.Fatal("错误正文创建记录或发送")
	}
}

// TestRequestIDValidation 验证观察入口在读取日志前拒绝空标识、路径穿越、设备名及非法文件名。
func TestRequestIDValidation(t *testing.T) {
	d, path, calls := fixture(t)
	a := adapterFixture(t, d)
	a.d.journalPath = func(string) (string, error) { t.Fatal("无效标识仍读取日志"); return "", nil }
	for _, id := range []string{"", "NWT-GW-", "../NWT-GW-a", "NWT-GW-a/..", `NWT-GW-a\b`, "NWT-GW-a.jsonl", "NWT-GW- a", "NWT-GW-群", "CON", "NUL", "NWT-GW-" + strings.Repeat("a", 122)} {
		if delivery, err := a.RecordObservation(id, Observation{RecipientObservation, "one"}); err == nil || delivery != Unknown {
			t.Fatalf("无效标识被接受: %q", id)
		}
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) || *calls != 0 {
		t.Fatal("无效观察标识产生副作用")
	}
}

// TestPlatformGuardDoesNotReserve 验证不支持的发送平台在生成标识、日志占用或实际调用前停止。
func TestPlatformGuardDoesNotReserve(t *testing.T) {
	d, path, calls := fixture(t)
	d.allowSend = func() error { return errors.New("unsupported platform") }
	d.newAttemptID = func() (string, error) { t.Fatal("平台拒绝后仍生成标识"); return "", nil }
	a := adapterFixture(t, d)
	result, err := a.SendGroup(testGroupID, Message)
	if err == nil || result.AttemptID != "" || result.AttemptReserved || *calls != 0 {
		t.Fatal("不支持的平台继续执行")
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("平台拒绝时占用了日志")
	}
}

// TestAttemptIDGenerationFailure 验证随机标识生成失败保留原错误，且不预检、写日志或发送。
func TestAttemptIDGenerationFailure(t *testing.T) {
	for _, selector := range []string{"group", "user"} {
		t.Run(selector, func(t *testing.T) {
			d, path, calls := fixture(t)
			want := errors.New("synthetic random source failure")
			generated := 0
			d.newAttemptID = func() (string, error) { generated++; return "", want }
			a := adapterFixture(t, d)
			a.d.check = func(string, *windowprobe.Target) (windowprobe.Target, Preflight, error) {
				t.Fatal("标识生成失败仍预检")
				return windowprobe.Target{}, Preflight{}, nil
			}
			send, id := a.SendGroup, testGroupID
			if selector == "user" {
				send, id = a.SendUser, testUserID
			}
			result, err := send(id, Message)
			if !errors.Is(err, want) || result.AttemptReserved || result.Call.Invoked || result.AttemptID != "" || generated != 1 || *calls != 0 {
				t.Fatalf("标识生成失败处理错误: %+v %v", result, err)
			}
			if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("标识生成失败仍创建日志")
			}
		})
	}
}

// TestAdapterPreservesBinding 验证每次预检都比较已绑定Target，不因新查询失败而自动换实例。
func TestAdapterPreservesBinding(t *testing.T) {
	d, _, _ := fixture(t)
	checks := 0
	d.check = func(_ string, previous *windowprobe.Target) (windowprobe.Target, Preflight, error) {
		checks++
		if checks == 1 {
			if previous != nil {
				t.Fatal("首次绑定已有旧Target")
			}
			return windowprobe.Target{Process: windowprobe.Process{PID: 42}}, Preflight{PID: 42}, nil
		}
		if previous == nil || previous.Process.PID != 42 {
			t.Fatal("未复核已绑定实例")
		}
		return windowprobe.Target{}, Preflight{}, errors.New("identity changed")
	}
	a := adapterFixture(t, d)
	if _, err := a.Preflight(); err == nil {
		t.Fatal("身份变化未报错")
	}
	if _, err := a.SendGroup(testGroupID, Message); err == nil {
		t.Fatal("身份变化后自动换实例发送")
	}
}

// TestIndependentAttemptIDs 验证真实生成器返回128位十六进制随机标识，独立调用与重建实例均使用不同标识。
func TestIndependentAttemptIDs(t *testing.T) {
	d, _, calls := fixture(t)
	base := t.TempDir()
	d.newAttemptID = newAttemptID
	d.journalPath = func(id string) (string, error) { return filepath.Join(base, id+".jsonl"), nil }
	d.send = func(windowprobe.Target, []byte) (CallResult, error) {
		*calls++
		return classifyCall(1, 1, 0, time.Millisecond), nil
	}
	a := adapterFixture(t, d)
	seen := make(map[string]bool)
	for _, adapter := range []*Adapter{a, a, adapterFixture(t, d)} {
		result, err := adapter.SendGroup(testGroupID, Message)
		if err != nil || !strings.HasPrefix(result.AttemptID, "NWT-GW-") || len(result.AttemptID) != len("NWT-GW-")+32 {
			t.Fatalf("自动标识格式错误: %+v %v", result, err)
		}
		random, err := hex.DecodeString(strings.TrimPrefix(result.AttemptID, "NWT-GW-"))
		if err != nil || len(random) != 16 || seen[result.AttemptID] {
			t.Fatalf("自动标识不是独立128位hex: %q %v", result.AttemptID, err)
		}
		seen[result.AttemptID] = true
		data, err := os.ReadFile(filepath.Join(base, result.AttemptID+".jsonl"))
		if err != nil || !bytes.Contains(data, []byte(`"attempt_id":"`+result.AttemptID+`"`)) {
			t.Fatalf("返回标识与日志不符: %v", err)
		}
	}
	if *calls != 3 {
		t.Fatalf("发送次数=%d", *calls)
	}
}

// TestSendErrorRetainsCallFacts 验证调用后错误不丢失API返回或持久化记录，不将成功API判为送达。
func TestSendErrorRetainsCallFacts(t *testing.T) {
	d, path, _ := fixture(t)
	want := errors.New("post-call check failed")
	d.send = func(windowprobe.Target, []byte) (CallResult, error) {
		return classifyCall(1, 1, 0, time.Millisecond), want
	}
	a := adapterFixture(t, d)
	result, err := a.SendGroup(testGroupID, Message)
	if !errors.Is(err, want) || result.AttemptID != testAttemptID || !result.Call.Completed || !result.AttemptReserved || result.Delivery != Unknown {
		t.Fatalf("丢失调用事实: %+v %v", result, err)
	}
	data, err := os.ReadFile(path)
	if err != nil || !bytes.Contains(data, []byte("call_result")) {
		t.Fatal("调用结果未持久化")
	}
}

// TestRecordRecipientObservation 验证只有独立接收one将结果变为RecipientObserved，追加观察不再发送。
func TestRecordRecipientObservation(t *testing.T) {
	d, path, calls := fixture(t)
	a := adapterFixture(t, d)
	result, err := a.SendGroup(testGroupID, Message)
	if err != nil || result.Delivery != Unknown {
		t.Fatal("发送时提前标记送达")
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, observation := range []Observation{{ClientObservation, "stable"}, {SenderObservation, "one"}, {RecipientObservation, "one"}} {
		delivery, err := a.RecordObservation(result.AttemptID, observation)
		if err != nil {
			t.Fatal(err)
		}
		if (delivery == RecipientObserved) != (observation.Kind == RecipientObservation) {
			t.Fatal("从非独立接收观察推断送达")
		}
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.HasPrefix(after, before) || !bytes.Contains(after, []byte(`"delivery":"recipient_observed"`)) || *calls != 1 {
		t.Fatal("观察改写旧事件、丢失证据或再次发送")
	}
}

// TestAbnormalObservationStops 验证客户端异常/unknown、多条或错误目标先保存观察再返回错误。
func TestAbnormalObservationStops(t *testing.T) {
	for _, observation := range []Observation{{ClientObservation, "crashed"}, {ClientObservation, "abnormal"}, {ClientObservation, "unknown"}, {SenderObservation, "multiple"}, {SenderObservation, "wrong_target"}, {RecipientObservation, "multiple"}, {RecipientObservation, "wrong_target"}} {
		d, path, calls := fixture(t)
		a := adapterFixture(t, d)
		if _, err := a.SendGroup(testGroupID, Message); err != nil {
			t.Fatal(err)
		}
		if delivery, err := a.RecordObservation(testAttemptID, observation); err == nil || delivery != Unknown {
			t.Fatalf("异常观察没有停止: %+v", observation)
		}
		data, err := os.ReadFile(path)
		if err != nil || !bytes.Contains(data, []byte(observation.State)) || *calls != 1 {
			t.Fatal("未保存观察或重复发送")
		}
	}
}

// TestObservationRejectsInvalidEvidence 验证无效状态、错误请求及不存在的实际调用均不追加证据。
func TestObservationRejectsInvalidEvidence(t *testing.T) {
	d, path, _ := fixture(t)
	a := adapterFixture(t, d)
	if _, err := a.RecordObservation(testAttemptID, Observation{RecipientObservation, "one"}); err == nil {
		t.Fatal("没有尝试日志仍记录送达")
	}
	if _, err := a.SendGroup(testGroupID, Message); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, observation := range []Observation{{"other", "one"}, {RecipientObservation, "yes"}, {ClientObservation, "one"}} {
		if _, err := a.RecordObservation(testAttemptID, observation); err == nil {
			t.Fatal("无效观察被记录")
		}
	}
	if _, err := a.RecordObservation("NWT-GW-OTHER-REQUEST", Observation{RecipientObservation, "one"}); err == nil {
		t.Fatal("请求标识不匹配仍记录观察")
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("无效证据改变了旧日志")
	}
}
