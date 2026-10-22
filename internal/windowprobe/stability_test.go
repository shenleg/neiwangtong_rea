// 本文件用合成快照测试跨扫描身份比较与定位接口，不操作真实客户端。
package windowprobe

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

// stableReport 构造第 index 次完整扫描；调用方可修改独立快照模拟状态，不产生错误。
func stableReport(index int) Report {
	session := uint32(2)
	r := Report{
		StartedAt:  time.Date(2026, 10, 23, 0, 0, index*2, 0, time.UTC),
		FinishedAt: time.Date(2026, 10, 23, 0, 0, index*2+1, 0, time.UTC),
		Processes: []Process{{PID: 10, Name: "ShiYeLine.exe", Path: `C:\NWT\ShiYeLine.exe`,
			CreationTime: 100, SessionID: &session, Alive: true}},
		Windows: []Window{skin(1, 0, false), skin(2, 1, true)},
	}
	r.Windows[0].RootOwner, r.Windows[1].RootOwner = 1, 1
	Assess(&r)
	return r
}

// TestCompareStates 验证最小化/恢复保持身份，而重启、owner变化和多候选不能通过。
func TestCompareStates(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*Report)
		status string
		change string
	}{
		{"unchanged", func(*Report) {}, "stable_unique_unverified", ""},
		{"minimize", func(r *Report) { r.Windows[1].Minimized = true; r.Windows[1].Style = 0x20000000 }, "stable_unique_unverified", "presentation"},
		{"hidden", func(r *Report) { r.Windows[1].Visible = false }, "stable_unique_unverified", "presentation"},
		{"caption not identity", func(r *Report) { r.Windows[1].Title = "不同的合成标题" }, "stable_unique_unverified", ""},
		{"new pid", func(r *Report) {
			r.Processes[0].PID = 11
			for i := range r.Windows {
				r.Windows[i].PID = 11
			}
		}, "identity_changed", "process_identity"},
		{"pid reused", func(r *Report) { r.Processes[0].CreationTime++ }, "identity_changed", "process_identity"},
		{"path changed", func(r *Report) { r.Processes[0].Path = `C:\other\ShiYeLine.exe` }, "identity_changed", "process_identity"},
		{"session changed", func(r *Report) { s := uint32(3); r.Processes[0].SessionID = &s }, "identity_changed", "process_identity"},
		{"tid changed", func(r *Report) { r.Windows[1].TID++ }, "identity_changed", "window_identity"},
		{"new main hwnd", func(r *Report) { r.Windows[1].HWND = 3; r.Windows[1].Root = 3 }, "identity_changed", "candidate_set"},
		{"owner changed", func(r *Report) {
			r.Windows[0].HWND = 3
			r.Windows[0].Root = 3
			r.Windows[0].RootOwner = 3
			r.Windows[1].Owner = 3
			r.Windows[1].RootOwner = 3
		}, "identity_changed", "window_identity"},
		{"owner disappeared", func(r *Report) { r.Windows = r.Windows[1:] }, "identity_changed", "window_removed"},
		{"other skin dialog", func(r *Report) { r.Windows = append(r.Windows, skin(3, 2, true)) }, "ambiguous", "window_added"},
		{"sibling skin dialog", func(r *Report) { r.Windows = append(r.Windows, skin(3, 1, true)) }, "ambiguous", "candidate_set"},
		{"non skin helper", func(r *Report) { w := skin(3, 2, false); w.Class = "Helper"; r.Windows = append(r.Windows, w) }, "stable_unique_unverified", "window_added"},
		{"multiple processes", func(r *Report) { p := r.Processes[0]; p.PID = 11; r.Processes = append(r.Processes, p) }, "ambiguous", "process_identity"},
		{"exit", func(r *Report) { r.Processes = nil; r.Windows = nil }, "unresolved", "process_removed"},
		{"missing path", func(r *Report) { r.Processes[0].Path = "" }, "incomplete", "process_identity"},
		{"missing creation", func(r *Report) { r.Processes[0].CreationTime = 0 }, "incomplete", "process_identity"},
		{"not alive", func(r *Report) { r.Processes[0].Alive = false }, "incomplete", ""},
		{"partial scan", func(r *Report) { r.Issues = []string{"failed"} }, "incomplete", ""},
		{"stale timestamp", func(r *Report) { r.StartedAt = stableReport(0).StartedAt }, "incomplete", ""},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			before, after := stableReport(0), stableReport(1)
			tt.mutate(&after)
			c := Compare(before, after)
			if c.Status != tt.status || c.Stable != (tt.status == "stable_unique_unverified") {
				t.Fatalf("comparison=%+v, want %s", c, tt.status)
			}
			if tt.change != "" {
				found := false
				for _, change := range c.Changes {
					found = found || change.Kind == tt.change
				}
				if !found {
					t.Fatalf("changes=%+v, missing %s", c.Changes, tt.change)
				}
			}
		})
	}
	minimized, restored := stableReport(0), stableReport(1)
	minimized.Windows[1].Minimized = true
	if !Compare(minimized, restored).Stable {
		t.Fatal("恢复窗口不应改变身份")
	}
}

// TestCompareOwnerAppeared 验证候选句柄没变但 owner 新增时，不能把两次元数据视为同一关系快照。
func TestCompareOwnerAppeared(t *testing.T) {
	before, after := stableReport(0), stableReport(1)
	before.Windows = before.Windows[1:]
	c := Compare(before, after)
	if c.Stable || c.Status != "identity_changed" {
		t.Fatalf("owner 新增未被识别: %+v", c)
	}
}

// TestCompareDoesNotMutate 验证比较重算分类但不修改输入，枚举顺序也不影响结论。
func TestCompareDoesNotMutate(t *testing.T) {
	a, b := stableReport(0), stableReport(1)
	b.Windows[0], b.Windows[1] = b.Windows[1], b.Windows[0]
	b.Windows[0].Role, b.Windows[0].Candidate = "stale", false
	if !Compare(a, b).Stable || b.Windows[0].Role != "stale" || b.Windows[0].Candidate {
		t.Fatal("比较不应信任旧分类、修改输入或依赖枚举顺序")
	}
}

// TestResolve 验证每次定位均扫描并执行最终复核，任一拒绝均返回零值 HWND。
func TestResolve(t *testing.T) {
	cases := []struct {
		name      string
		path      string
		mutate    func(*Report)
		previous  *Target
		verifyErr error
		ok        bool
	}{
		{"valid", `C:\NWT\ShiYeLine.exe`, func(*Report) {}, nil, nil, true},
		{"case and slash", `c:/nwt/SHIYELINE.EXE`, func(*Report) {}, nil, nil, true},
		{"relative", `ShiYeLine.exe`, func(*Report) {}, nil, nil, false},
		{"different path", `C:\other\ShiYeLine.exe`, func(*Report) {}, nil, nil, false},
		{"ambiguous", `C:\NWT\ShiYeLine.exe`, func(r *Report) { r.Windows = append(r.Windows, skin(3, 2, true)) }, nil, nil, false},
		{"missing owner", `C:\NWT\ShiYeLine.exe`, func(r *Report) { r.Windows = r.Windows[1:] }, nil, nil, false},
		{"root owner mismatch", `C:\NWT\ShiYeLine.exe`, func(r *Report) { r.Windows[1].RootOwner = 2 }, nil, nil, false},
		{"dead", `C:\NWT\ShiYeLine.exe`, func(r *Report) { r.Processes[0].Alive = false }, nil, nil, false},
		{"process access failed", `C:\NWT\ShiYeLine.exe`, func(r *Report) { r.Processes[0].Issues = []string{"access denied"} }, nil, nil, false},
		{"last check failed", `C:\NWT\ShiYeLine.exe`, func(*Report) {}, nil, errors.New("destroyed"), false},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			r := stableReport(0)
			tt.mutate(&r)
			verified := 0
			got, err := resolve(tt.path, tt.previous, func() (Report, error) { return r, nil }, func(Target) error { verified++; return tt.verifyErr })
			if (err == nil) != tt.ok {
				t.Fatalf("target=%+v error=%v", got, err)
			}
			if tt.ok && (got.Window.HWND != 2 || got.CheckedAt.IsZero() || got.Reliability != "unique_unverified" || verified != 1) {
				t.Fatalf("target=%+v verified=%d", got, verified)
			}
			if !tt.ok && got.Window.HWND != 0 {
				t.Fatal("失败时泄露旧 HWND")
			}
		})
	}
	calls, verifies := 0, 0
	scan := func() (Report, error) { calls++; return stableReport(calls), nil }
	verify := func(Target) error { verifies++; return nil }
	first, err := resolve(`C:\NWT\ShiYeLine.exe`, nil, scan, verify)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := resolve(first.Process.Path, &first, scan, verify); err != nil {
		t.Fatal(err)
	}
	if calls != 2 || verifies != 2 {
		t.Fatal("重复定位复用了缓存而非重新扫描和复核")
	}
	for _, what := range []string{"creation", "hwnd", "owner", "tid"} {
		r := stableReport(3)
		switch what {
		case "creation":
			r.Processes[0].CreationTime++
		case "hwnd":
			r.Windows[1].HWND = 3
			r.Windows[1].Root = 3
		case "owner":
			r.Windows[0].TID++
		case "tid":
			r.Windows[1].TID++
		}
		got, err := resolve(first.Process.Path, &first, func() (Report, error) { return r, nil }, func(Target) error { t.Fatal("变化后不应进入最终复核"); return nil })
		if err == nil || got.Window.HWND != 0 {
			t.Fatalf("未拒绝变化: %s", what)
		}
	}
	if _, err := resolve(first.Process.Path, nil, func() (Report, error) { return Report{}, errors.New("scan failed") }, verify); err == nil {
		t.Fatal("扫描错误被吞掉")
	}
}

// TestAbsoluteEXEPath 验证拒绝设备路径、相对路径及 NUL，允许盘符和 UNC 的明确路径。
func TestAbsoluteEXEPath(t *testing.T) {
	for _, path := range []string{`ShiYeLine.exe`, `C:ShiYeLine.exe`, `\ShiYeLine.exe`, `\\?\C:\NWT\ShiYeLine.exe`, `\\.\C:\NWT\ShiYeLine.exe`, "C:\\NWT\\ShiYeLine.exe\x00", `C:\NWT\other.exe`, `\\host\ShiYeLine.exe`} {
		if absoluteEXEPath(path) {
			t.Errorf("accepted %q", path)
		}
	}
	for _, path := range []string{`C:\NWT\ShiYeLine.exe`, `c:/NWT/ShiYeLine.exe`, `\\host\share\ShiYeLine.exe`} {
		if !absoluteEXEPath(path) {
			t.Errorf("rejected %q", path)
		}
	}
}

// TestObserve 验证连续采集、取消和失败样本保留，避免只保留成功结果造成虚假稳定结论。
func TestObserve(t *testing.T) {
	calls := 0
	scan := func() (Report, error) { r := stableReport(calls); calls++; return r, nil }
	s, err := observe(context.Background(), 3, time.Millisecond, "minimized", scan)
	if err != nil || calls != 3 || len(s.Comparisons) != 2 || s.Stability != "stable_unique_unverified" || s.Scans[0].Label != "minimized" {
		t.Fatalf("series=%+v err=%v calls=%d", s, err, calls)
	}
	calls = 0
	s, err = observe(context.Background(), 2, time.Millisecond, "dialog", func() (Report, error) {
		r, err := scan()
		if calls == 2 {
			r.Windows = append(r.Windows, skin(3, 2, true))
		}
		return r, err
	})
	if err != nil || s.Stability != "not_stable" || len(s.Scans) != 2 {
		t.Fatalf("丢弃歧义样本: %+v %v", s, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	s, err = observe(ctx, 3, time.Hour, "cancel", func() (Report, error) { cancel(); return stableReport(0), nil })
	if !errors.Is(err, context.Canceled) || len(s.Scans) != 1 {
		t.Fatalf("取消丢失记录或未及时退出: %+v %v", s, err)
	}
	calls = 0
	s, err = observe(context.Background(), 2, time.Millisecond, "failed", func() (Report, error) {
		calls++
		if calls == 2 {
			return Report{}, errors.New("failed")
		}
		return stableReport(0), nil
	})
	if err == nil || len(s.Scans) != 1 {
		t.Fatal("失败样本未报告或先前样本丢失")
	}
	for _, count := range []int{0, 1001} {
		if _, err := observe(context.Background(), count, time.Second, "", scan); err == nil {
			t.Fatal("接受非法 count")
		}
	}
	if _, err := observe(context.Background(), 2, 0, "", scan); err == nil {
		t.Fatal("接受无间隔连续扫描")
	}
}

// TestObservePartialStability 验证部分结果按已完成的比较评级，后续稳定样本不能抹掉早先歧义。
func TestObservePartialStability(t *testing.T) {
	calls := 0
	s, err := observe(context.Background(), 3, time.Millisecond, "partial", func() (Report, error) {
		calls++
		if calls == 3 {
			return Report{}, errors.New("stopped")
		}
		return stableReport(calls), nil
	})
	if err == nil || len(s.Scans) != 2 || s.Stability != "stable_unique_unverified" {
		t.Fatalf("partial=%+v err=%v", s, err)
	}
	calls = 0
	s, err = observe(context.Background(), 4, time.Millisecond, "recover", func() (Report, error) {
		r := stableReport(calls)
		calls++
		if calls == 2 {
			r.Windows = append(r.Windows, skin(3, 2, true))
		}
		return r, nil
	})
	if err != nil || s.Stability != "not_stable" || !s.Comparisons[2].Stable {
		t.Fatalf("历史歧义未保留: %+v %v", s, err)
	}
}

// TestReportRoundTrip 验证 baseline 可保真读取句柄与 FILETIME，非法句柄明确失败。
func TestReportRoundTrip(t *testing.T) {
	r := stableReport(0)
	r.Processes[0].CreationTime = 0xFEDCBA9876543210
	data, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	var decoded Report
	if err := json.Unmarshal(data, &decoded); err != nil || decoded.Processes[0].CreationTime != r.Processes[0].CreationTime || decoded.Windows[1].HWND != 2 {
		t.Fatalf("error=%v report=%+v", err, decoded)
	}
	for _, value := range []string{`"123"`, `"0x"`, `"0x10000000000000000"`, `"0xZZ"`, `12`} {
		var h Handle
		if err := json.Unmarshal([]byte(value), &h); err == nil {
			t.Errorf("accepted %s", value)
		}
	}
	if strings.Contains(string(data), `"creation_time_ticks":183647`) {
		t.Fatal("FILETIME 被编码为 JSON number")
	}
}
