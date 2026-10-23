// 本文件以公开Adapter方法验证载荷、只读预检、结构化结果和持久化单次控制，不向内网通发送。
package ipc

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"neiwangtong-gateway/internal/windowprobe"
)

const testGroupID = "0123456789abcdef0123456789ABCDEF"
const testAttemptID = "NWT-GW-TEST-REQUEST-1"

// TestConfirmationJournal 保证显式请求标识沿用历史目录，不因Adapter重构覆盖旧任务。
func TestConfirmationJournal(t *testing.T) {
	base := t.TempDir()
	t.Setenv("APPDATA", base)
	t.Setenv("XDG_CONFIG_HOME", base)
	path, err := journalPath("NWT-GW-AUTH-TEST-0001-confirmation-04")
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(path) != "NWT-GW-AUTH-TEST-0001-confirmation-04.jsonl" || filepath.Base(filepath.Dir(path)) != "ipc-experiment" {
		t.Fatalf("历史尝试记录位置发生变化: %q", path)
	}
}

// TestPayload 验证三个NUL均在实际传送区内，且实验缓冲区不含额外正文尾部。
func TestPayload(t *testing.T) {
	if Message != "NWT-GW-AUTH-TEST-0001" {
		t.Fatal("固定正文发生变化")
	}
	for _, selector := range []string{"group_id", "user_id"} {
		for _, id := range []string{"synthetic-target_1", strings.Repeat("A", 199)} {
			b, err := buildPayload(selector, id)
			if err != nil {
				t.Fatal(err)
			}
			if len(b) != 322 {
				t.Fatalf("长度=%d", len(b))
			}
			for offset, text := range map[int]string{0: selector, 0x64: id, 0x12c: Message} {
				end := offset + len(text)
				if end >= len(b) || string(b[offset:end]) != text || b[end] != 0 {
					t.Fatalf("字段偏移=%x 不完整", offset)
				}
			}
			if bytes.Count(b[:0x64], []byte{0}) != 0x64-len(selector) || b[0x141] != 0 {
				t.Fatal("空隙或正文NUL不符固定载荷")
			}
		}
	}
}

// TestPayloadRejectsInvalidID 验证空值、正文覆盖、NUL、非ASCII和列表样式输入在本地被拒绝。
func TestPayloadRejectsInvalidID(t *testing.T) {
	for _, id := range []string{"", strings.Repeat("A", 200), "a\x00b", "a,b", "a;b", "a|b", " a", "a\nb", "群", "*", "[a]"} {
		if _, err := buildPayload("group_id", id); err == nil {
			t.Fatalf("未拒绝 %q", id)
		}
	}
}

// fixture 提供合成定位、测试日志和模拟发送；返回依赖、日志路径与调用计数，错误由测试注入。
func fixture(t *testing.T) (dependencies, string, *int) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "attempt.jsonl")
	calls := new(int)
	d := dependencies{
		check: func(string, *windowprobe.Target) (windowprobe.Target, Preflight, error) {
			return windowprobe.Target{}, Preflight{}, nil
		},
		journalPath: func(string) (string, error) { return path, nil },
		allowSend:   func() error { return nil },
		send: func(_ windowprobe.Target, payload []byte) (CallResult, error) {
			*calls++
			data, err := os.ReadFile(path)
			if err != nil || !bytes.Contains(data, []byte("attempt_reserved")) {
				t.Error("调用前没有持久尝试记录")
			}
			if string(payload[0x12c:len(payload)-1]) != Message {
				t.Error("固定载荷发生变化")
			}
			return classifyCall(1, 1, 0, time.Millisecond), nil
		},
	}
	return d, path, calls
}

// adapterFixture 用合成依赖创建实际Adapter，构造错误终止测试；不绕过Adapter方法执行过程。
func adapterFixture(t *testing.T, config Config, d dependencies) *Adapter {
	t.Helper()
	a, err := newAdapter("synthetic.exe", config, d)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

// TestReadOnlyAndUnbound 验证无绑定配置仅可预检，不创建尝试记录或调用发送。
func TestReadOnlyAndUnbound(t *testing.T) {
	d, path, calls := fixture(t)
	a := adapterFixture(t, Config{}, d)
	if _, err := a.Preflight(); err != nil {
		t.Fatal(err)
	}
	if result, err := a.SendGroup(testAttemptID); err == nil || result.AttemptReserved || result.Call.Invoked {
		t.Fatal("未绑定群仍可发送")
	}
	if *calls != 0 {
		t.Fatal("发生发送")
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("只读操作创建了记录: %v", err)
	}
}

// TestBoundReadOnly 验证已绑定群的Adapter预检仍不发送、不占用日志，结构化结果不含群ID。
func TestBoundReadOnly(t *testing.T) {
	d, path, calls := fixture(t)
	a := adapterFixture(t, Config{AuthorizedGroupID: testGroupID}, d)
	info, err := a.Preflight()
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(info)
	if err != nil {
		t.Fatal(err)
	}
	if *calls != 0 || bytes.Contains(data, []byte(testGroupID)) {
		t.Fatal("预检发送或泄露群ID")
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("预检创建了记录: %v", err)
	}
}

// TestOneAttemptPersists 验证同实例、重建实例都被既有标识拒绝；API成功仍只返回Unknown。
func TestOneAttemptPersists(t *testing.T) {
	d, path, calls := fixture(t)
	a := adapterFixture(t, Config{AuthorizedGroupID: testGroupID}, d)
	result, err := a.SendGroup(testAttemptID)
	if err != nil {
		t.Fatal(err)
	}
	if *calls != 1 || !result.AttemptReserved || !result.Call.Completed || result.Delivery != Unknown {
		t.Fatalf("错误的单次结果: %+v", result)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range bytes.Split(bytes.TrimSpace(data), []byte{'\n'}) {
		var event map[string]any
		if err := json.Unmarshal(line, &event); err != nil {
			t.Fatal(err)
		}
	}
	for _, adapter := range []*Adapter{a, adapterFixture(t, Config{AuthorizedGroupID: testGroupID}, d)} {
		if _, err := adapter.SendGroup(testAttemptID); err == nil {
			t.Fatal("已有任务未被拒绝")
		}
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(data, after) || *calls != 1 {
		t.Fatal("重启后改变旧记录或再次发送")
	}
	visible, err := json.Marshal(result)
	if err != nil || bytes.Contains(visible, []byte(testGroupID)) {
		t.Fatal("结构化发送结果泄露目标")
	}
}

// TestFinalIdentityChange 验证落盘后的最终身份变化不会发送且保留记录，结构化结果说明已占用。
func TestFinalIdentityChange(t *testing.T) {
	d, path, calls := fixture(t)
	checks := 0
	d.check = func(string, *windowprobe.Target) (windowprobe.Target, Preflight, error) {
		checks++
		if checks == 3 {
			return windowprobe.Target{}, Preflight{}, errors.New("identity changed")
		}
		return windowprobe.Target{}, Preflight{}, nil
	}
	a := adapterFixture(t, Config{AuthorizedGroupID: testGroupID}, d)
	result, err := a.SendGroup(testAttemptID)
	if err == nil || !result.AttemptReserved || result.Call.Invoked || *calls != 0 {
		t.Fatal("最终身份变化未阻止发送或丢失占用事实")
	}
	data, err := os.ReadFile(path)
	if err != nil || !bytes.Contains(data, []byte("stopped_before_call")) {
		t.Fatalf("停止记录缺失: %v", err)
	}
	if _, err := a.SendGroup(testAttemptID); err == nil || *calls != 0 {
		t.Fatal("最终检查失败后恢复了发送资格")
	}
}

// TestCallFailureAndTimeoutNeverRetry 验证API失败/超时立即返回完整结果和错误，不等待UI或自动重试。
func TestCallFailureAndTimeoutNeverRetry(t *testing.T) {
	for _, code := range []uint32{0, 5, 1460} {
		d, path, calls := fixture(t)
		d.send = func(windowprobe.Target, []byte) (CallResult, error) {
			*calls++
			return classifyCall(0, 0, code, 5*time.Second), nil
		}
		a := adapterFixture(t, Config{AuthorizedGroupID: testGroupID}, d)
		result, err := a.SendGroup(testAttemptID)
		if err == nil || result.Call.LastError != code || !result.Call.Invoked || result.Delivery != Unknown || *calls != 1 {
			t.Fatalf("失败结果不完整: %+v %v", result, err)
		}
		data, err := os.ReadFile(path)
		if err != nil || bytes.Contains(data, []byte("recipient_observed")) {
			t.Fatal("无接收证据却标记送达")
		}
		if _, err := a.SendGroup(testAttemptID); err == nil || *calls != 1 {
			t.Fatal("失败任务被重试")
		}
	}
}

// TestConcurrentAttempt 验证不同Adapter实例争用同一标识时，磁盘独占创建只允许一次调用。
func TestConcurrentAttempt(t *testing.T) {
	d, _, _ := fixture(t)
	var calls atomic.Int32
	d.send = func(windowprobe.Target, []byte) (CallResult, error) {
		calls.Add(1)
		return classifyCall(1, 1, 0, time.Millisecond), nil
	}
	adapters := []*Adapter{adapterFixture(t, Config{AuthorizedGroupID: testGroupID}, d), adapterFixture(t, Config{AuthorizedGroupID: testGroupID}, d)}
	var wg sync.WaitGroup
	for _, a := range adapters {
		wg.Add(1)
		go func(a *Adapter) { defer wg.Done(); _, _ = a.SendGroup(testAttemptID) }(a)
	}
	wg.Wait()
	if calls.Load() != 1 {
		t.Fatalf("并发调用次数=%d", calls.Load())
	}
}

// TestClassifyCall 验证窗口结果1不改变投递语义，错误为0时不伪造超时结论。
func TestClassifyCall(t *testing.T) {
	for _, tc := range []struct {
		api, lr uint64
		code    uint32
		timeout string
		valid   bool
	}{{1, 1, 0, "no", true}, {1, 0, 0, "no", true}, {0, 1, 0, "not_reported", false}, {0, 0, 5, "not_reported", false}, {0, 0, 1460, "yes", false}} {
		r := classifyCall(tc.api, tc.lr, tc.code, time.Second)
		if r.Timeout != tc.timeout || r.LResultValid != tc.valid || r.ElapsedMS != 1000 {
			t.Fatalf("分类错误: %+v", r)
		}
	}
}
