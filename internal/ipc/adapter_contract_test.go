// 本文件验证正式Adapter的配置绑定、请求标识、结构化错误和外部观察契约，只使用合成数据。
package ipc

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"neiwangtong-gateway/internal/windowprobe"
)

// TestAdapterRejectsInvalidConfig 验证构造时拒绝非32位原始群ID，不进行窗口查询。
func TestAdapterRejectsInvalidConfig(t *testing.T) {
	d, _, _ := fixture(t)
	d.check = func(string, *windowprobe.Target) (windowprobe.Target, Preflight, error) {
		t.Fatal("无效配置不应查询窗口")
		return windowprobe.Target{}, Preflight{}, nil
	}
	for _, id := range []string{"group", strings.Repeat("a", 31), strings.Repeat("a", 33), strings.Repeat("a", 31) + "g", testGroupID + " "} {
		if _, err := newAdapter("synthetic.exe", Config{AuthorizedGroupID: id}, d); err == nil {
			t.Fatal("无效授权配置被接受")
		}
	}
}

// TestAdapterSnapshotsAuthorization 验证调用者修改原Config不会改变已绑定群，发送结果不依赖控制台。
func TestAdapterSnapshotsAuthorization(t *testing.T) {
	d, _, calls := fixture(t)
	config := Config{AuthorizedGroupID: testGroupID}
	a := adapterFixture(t, config, d)
	config.AuthorizedGroupID = strings.Repeat("f", 32)
	a.d.send = func(_ windowprobe.Target, payload []byte) (CallResult, error) {
		*calls++
		if string(payload[0x64:0x64+32]) != testGroupID {
			t.Fatal("授权目标被调用者修改")
		}
		return classifyCall(1, 1, 0, time.Millisecond), nil
	}
	result, err := a.SendGroup(testAttemptID)
	if err != nil || !result.Call.Invoked || result.Delivery != Unknown {
		t.Fatalf("非交互式发送失败: %+v %v", result, err)
	}
}

// TestRequestIDValidation 验证无任务标识、路径穿越、设备名、非ASCII和过长文件名均在落盘前拒绝。
func TestRequestIDValidation(t *testing.T) {
	d, path, calls := fixture(t)
	a := adapterFixture(t, Config{AuthorizedGroupID: testGroupID}, d)
	for _, id := range []string{"", "NWT-GW-", "../NWT-GW-a", "NWT-GW-a/..", `NWT-GW-a\b`, "NWT-GW-a.jsonl", "NWT-GW- a", "NWT-GW-群", "CON", "NUL", "NWT-GW-" + strings.Repeat("a", 122)} {
		result, err := a.SendGroup(id)
		if err == nil || result.AttemptReserved || result.Call.Invoked {
			t.Fatalf("无效标识被执行: %q", id)
		}
	}
	if *calls != 0 {
		t.Fatal("无效标识触发发送")
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("无效标识占用记录")
	}
}

// TestPlatformGuardDoesNotReserve 验证不支持的发送平台在任何日志占用或实际调用前停止。
func TestPlatformGuardDoesNotReserve(t *testing.T) {
	d, path, calls := fixture(t)
	d.allowSend = func() error { return errors.New("unsupported platform") }
	a := adapterFixture(t, Config{AuthorizedGroupID: testGroupID}, d)
	result, err := a.SendGroup(testAttemptID)
	if err == nil || result.AttemptReserved || *calls != 0 {
		t.Fatal("不支持的平台继续执行")
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("平台拒绝时占用了日志")
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
	a := adapterFixture(t, Config{AuthorizedGroupID: testGroupID}, d)
	if _, err := a.Preflight(); err == nil {
		t.Fatal("身份变化未报错")
	}
	if _, err := a.SendGroup(testAttemptID); err == nil {
		t.Fatal("身份变化后自动换实例发送")
	}
}

// TestIndependentAttemptIDs 验证任务标识由调用者明确提供，不再修改源码常量或自动生成新任务。
func TestIndependentAttemptIDs(t *testing.T) {
	d, _, calls := fixture(t)
	base := t.TempDir()
	d.journalPath = func(id string) (string, error) { return filepath.Join(base, id+".jsonl"), nil }
	d.send = func(windowprobe.Target, []byte) (CallResult, error) {
		*calls++
		return classifyCall(1, 1, 0, time.Millisecond), nil
	}
	a := adapterFixture(t, Config{AuthorizedGroupID: testGroupID}, d)
	for _, id := range []string{"NWT-GW-SYNTHETIC-A", "NWT-GW-SYNTHETIC-B"} {
		result, err := a.SendGroup(id)
		if err != nil || result.AttemptID != id {
			t.Fatalf("请求标识未保留: %+v %v", result, err)
		}
		if _, err := os.Stat(filepath.Join(base, id+".jsonl")); err != nil {
			t.Fatal(err)
		}
	}
	if *calls != 2 {
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
	a := adapterFixture(t, Config{AuthorizedGroupID: testGroupID}, d)
	result, err := a.SendGroup(testAttemptID)
	if !errors.Is(err, want) || !result.Call.Completed || !result.AttemptReserved || result.Delivery != Unknown {
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
	a := adapterFixture(t, Config{AuthorizedGroupID: testGroupID}, d)
	result, err := a.SendGroup(testAttemptID)
	if err != nil || result.Delivery != Unknown {
		t.Fatal("发送时提前标记送达")
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, observation := range []Observation{{ClientObservation, "stable"}, {SenderObservation, "one"}, {RecipientObservation, "one"}} {
		delivery, err := a.RecordObservation(testAttemptID, observation)
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
		a := adapterFixture(t, Config{AuthorizedGroupID: testGroupID}, d)
		if _, err := a.SendGroup(testAttemptID); err != nil {
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

// TestObservationRejectsInvalidEvidence 验证无效状态、错误请求、错误群及无真实调用均不追加送达证据。
func TestObservationRejectsInvalidEvidence(t *testing.T) {
	d, path, _ := fixture(t)
	a := adapterFixture(t, Config{AuthorizedGroupID: testGroupID}, d)
	if _, err := a.RecordObservation(testAttemptID, Observation{RecipientObservation, "one"}); err == nil {
		t.Fatal("没有尝试日志仍记录送达")
	}
	if _, err := a.SendGroup(testAttemptID); err != nil {
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
	other := adapterFixture(t, Config{AuthorizedGroupID: strings.Repeat("f", 32)}, d)
	if _, err := other.RecordObservation(testAttemptID, Observation{RecipientObservation, "one"}); err == nil {
		t.Fatal("其他群记录被接受")
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("无效证据改变了旧日志")
	}
}
