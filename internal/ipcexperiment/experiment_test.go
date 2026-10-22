// 本文件使用合成群 ID 和替代发送函数验证单次控制，不向内网通发送消息。
package ipcexperiment

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"neiwangtong-gateway/internal/windowprobe"
)

// TestPayload 验证三个 NUL 均在实际传送区内，且实验缓冲区不含额外正文尾部。
func TestPayload(t *testing.T) {
	for _, id := range []string{"synthetic-group_1", strings.Repeat("A", 199)} {
		b, err := buildPayload(id)
		if err != nil {
			t.Fatal(err)
		}
		if len(b) != 322 {
			t.Fatalf("长度=%d", len(b))
		}
		for offset, text := range map[int]string{0: "group_id", 0x64: id, 0x12c: Message} {
			end := offset + len(text)
			if end >= len(b) || string(b[offset:end]) != text || b[end] != 0 {
				t.Fatalf("字段偏移=%x 不完整", offset)
			}
		}
		if bytes.Count(b[:0x64], []byte{0}) != 92 {
			t.Fatal("选择器之后的实验空隙未清零")
		}
		if b[0x141] != 0 {
			t.Fatal("正文 NUL 未包含在 cbData 内")
		}
	}
}

// TestPayloadRejectsInvalidID 验证空值、正文覆盖、NUL、非 ASCII 与列表样式输入在本地被拒绝。
func TestPayloadRejectsInvalidID(t *testing.T) {
	for _, id := range []string{"", strings.Repeat("A", 200), "a\x00b", "a,b", "a;b", "a|b", " a", "a\nb", "群", "*", "[a]"} {
		if _, err := buildPayload(id); err == nil {
			t.Fatalf("未拒绝 %q", id)
		}
	}
}

// fixture 为单次流程提供合成定位、同一个临时记录路径及模拟发送；错误由各测试注入。
func fixture(t *testing.T) (dependencies, string, *int) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "attempt.jsonl")
	calls := new(int)
	d := dependencies{
		check: func(string, *windowprobe.Target) (windowprobe.Target, Preflight, error) {
			return windowprobe.Target{}, Preflight{}, nil
		},
		journalPath: func() (string, error) { return path, nil },
		send: func(windowprobe.Target, []byte) (CallResult, error) {
			*calls++
			data, err := os.ReadFile(path)
			if err != nil || !bytes.Contains(data, []byte("attempt_reserved")) {
				t.Error("调用前没有持久尝试记录")
			}
			return classifyCall(1, 1, 0, time.Millisecond), nil
		},
	}
	return d, path, calls
}

// confirmedInput 返回仅用于合成群 ID 的完整确认和后续观察输入，不提供真实群信息。
func confirmedInput(observations string) *strings.Reader {
	return strings.NewReader("SEND ONCE synthetic-group_1 " + Message + "\n" + observations)
}

// TestReadOnlyAndUnbound 验证默认检查与未绑定授权群的发送请求均不创建尝试记录或调用发送。
func TestReadOnlyAndUnbound(t *testing.T) {
	d, path, calls := fixture(t)
	if err := run("synthetic.exe", "", false, strings.NewReader(""), io.Discard, d); err != nil {
		t.Fatal(err)
	}
	if err := run("synthetic.exe", "", true, confirmedInput(""), io.Discard, d); err == nil {
		t.Fatal("未绑定群仍可发送")
	}
	if *calls != 0 {
		t.Fatal("发生发送")
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("只读操作创建了记录: %v", err)
	}
}

// TestConfirmationExact 验证确认不能通过截断、空白修正或只有固定消息获得。
func TestConfirmationExact(t *testing.T) {
	for _, input := range []string{"", "yes\n", "SEND ONCE synthetic-group_1 " + Message, " SEND ONCE synthetic-group_1 " + Message + "\n"} {
		d, _, calls := fixture(t)
		if err := run("synthetic.exe", "synthetic-group_1", true, strings.NewReader(input), io.Discard, d); err == nil {
			t.Fatal("错误确认未被拒绝")
		}
		if *calls != 0 {
			t.Fatal("错误确认后发生发送")
		}
	}
}

// TestOneAttemptPersists 验证一次调用、独立接收观察与第二次执行被永久记录阻止。
func TestOneAttemptPersists(t *testing.T) {
	d, path, calls := fixture(t)
	input := "stable\none\none\n"
	if err := run("synthetic.exe", "synthetic-group_1", true, confirmedInput(input), io.Discard, d); err != nil {
		t.Fatal(err)
	}
	if *calls != 1 {
		t.Fatalf("调用次数=%d", *calls)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(data, []byte(`"delivery":"recipient_observed"`)) {
		t.Fatal("独立接收结果未保存")
	}
	if err := run("synthetic.exe", "synthetic-group_1", true, confirmedInput(input), io.Discard, d); err == nil {
		t.Fatal("第二次执行未被拒绝")
	}
	if *calls != 1 {
		t.Fatal("第二次执行发生重发")
	}
	for _, line := range bytes.Split(bytes.TrimSpace(data), []byte{'\n'}) {
		var event map[string]any
		if err := json.Unmarshal(line, &event); err != nil {
			t.Fatal(err)
		}
	}
}

// TestFinalIdentityChange 验证记录后最终定位失败时不调用发送，且保留记录阻止再次尝试。
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
	if err := run("synthetic.exe", "synthetic-group_1", true, confirmedInput(""), io.Discard, d); err == nil {
		t.Fatal("最终身份改变未拒绝")
	}
	if *calls != 0 {
		t.Fatal("身份改变仍调用发送")
	}
	data, err := os.ReadFile(path)
	if err != nil || !bytes.Contains(data, []byte("stopped_before_call")) {
		t.Fatalf("停止记录缺失: %v", err)
	}
}

// TestAbnormalObservationStops 验证崩溃、异常发送、错误目标均立即停止，不执行第二次调用。
func TestAbnormalObservationStops(t *testing.T) {
	for _, input := range []string{"crashed\n", "abnormal\n", "stable\nmultiple\n", "stable\nwrong_target\n", "stable\none\nmultiple\n", "stable\none\nwrong_target\n"} {
		d, _, calls := fixture(t)
		if err := run("synthetic.exe", "synthetic-group_1", true, confirmedInput(input), io.Discard, d); err == nil {
			t.Fatalf("异常观察未停止: %q", input)
		}
		if *calls != 1 {
			t.Fatal("调用次数不是一次")
		}
	}
}

// TestCallFailureAndTimeoutNeverRetry 验证 API 失败或超时只保存 unknown，不产生自动重试。
func TestCallFailureAndTimeoutNeverRetry(t *testing.T) {
	for _, code := range []uint32{0, 5, 1460} {
		d, path, calls := fixture(t)
		d.send = func(windowprobe.Target, []byte) (CallResult, error) {
			*calls++
			return classifyCall(0, 0, code, 5*time.Second), nil
		}
		if err := run("synthetic.exe", "synthetic-group_1", true, confirmedInput("stable\nnone\nunknown\n"), io.Discard, d); err == nil {
			t.Fatal("Win32 失败没有返回错误")
		}
		if *calls != 1 {
			t.Fatal("失败调用被重试")
		}
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(data, []byte("recipient_observed")) {
			t.Fatal("无接收证据却标记送达")
		}
	}
}

// TestConcurrentAttempt 验证两个并发进程式调用争用同一路径时只有一个能够进入模拟发送。
func TestConcurrentAttempt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "attempt.jsonl")
	var calls atomic.Int32
	d := dependencies{
		check: func(string, *windowprobe.Target) (windowprobe.Target, Preflight, error) {
			return windowprobe.Target{}, Preflight{}, nil
		},
		journalPath: func() (string, error) { return path, nil },
		send: func(windowprobe.Target, []byte) (CallResult, error) {
			calls.Add(1)
			return classifyCall(1, 1, 0, time.Millisecond), nil
		},
	}
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = run("synthetic.exe", "synthetic-group_1", true, confirmedInput("stable\none\none\n"), io.Discard, d)
		}()
	}
	wg.Wait()
	if calls.Load() != 1 {
		t.Fatalf("并发调用次数=%d", calls.Load())
	}
}

// TestClassifyCall 验证窗口结果 1 不改变投递语义，且错误为 0 时不伪造超时结论。
func TestClassifyCall(t *testing.T) {
	for _, tc := range []struct {
		api, lr uint64
		code    uint32
		timeout string
		valid   bool
	}{
		{1, 1, 0, "no", true}, {1, 0, 0, "no", true}, {0, 1, 0, "not_reported", false}, {0, 0, 5, "not_reported", false}, {0, 0, 1460, "yes", false},
	} {
		r := classifyCall(tc.api, tc.lr, tc.code, time.Second)
		if r.Timeout != tc.timeout || r.LResultValid != tc.valid || r.ElapsedMS != 1000 {
			t.Fatalf("分类错误: %+v", r)
		}
	}
}
