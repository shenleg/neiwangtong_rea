// 本文件验证 CLI 参数、元数据文本输出和输出失败行为，不读取真实客户端标题。
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"neiwangtong-gateway/internal/windowprobe"
)

// TestArguments 验证错误格式、额外参数及帮助请求都在探测前处理。
func TestArguments(t *testing.T) {
	for _, tt := range []struct {
		args []string
		code int
	}{
		{[]string{"-h"}, 0},
		{[]string{"-format", "xml"}, 2},
		{[]string{"-pid", "1"}, 2},
		{[]string{"message"}, 2},
		{[]string{"-count", "0"}, 2},
		{[]string{"-count", "1001"}, 2},
		{[]string{"-count", "x"}, 2},
		{[]string{"-interval", "-1s"}, 2},
		{[]string{"-interval", "bad"}, 2},
		{[]string{"-count", "2", "-interval", "0"}, 2},
		{[]string{"-count", "2", "-interval", "999us"}, 2},
	} {
		var out, errOut bytes.Buffer
		if code := run(tt.args, &out, &errOut); code != tt.code {
			t.Errorf("args=%v code=%d, want %d", tt.args, code, tt.code)
		}
		if out.Len() != 0 || errOut.Len() == 0 {
			t.Errorf("参数处理不应触发探测，且应输出帮助或错误: args=%v", tt.args)
		}
	}
}

// TestTextOutput 验证窗口元数据完整输出，并将标题中的控制字符转义。
func TestTextOutput(t *testing.T) {
	r := windowprobe.Report{
		Label:     "合成标签\n\x1b[31m",
		Processes: []windowprobe.Process{{PID: 10, Name: "ShiYeLine.exe", Path: "C:\\合成\n\\ShiYeLine.exe", CreationTime: 12345, Alive: true}},
		Windows: []windowprobe.Window{{HWND: 0x123, PID: 10, TID: 20, Class: "SkinDialog", Title: "合成标题\n\x1b[31m",
			Owner: 0x122, Root: 0x123, RootOwner: 0x122, TopLevel: true, Stable: true,
			Rect: &windowprobe.Rect{Width: 300, Height: 500}}},
	}
	windowprobe.Assess(&r)
	var out bytes.Buffer
	if err := writeText(&out, r); err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{"HWND=0x123", "PID=10", "TID=20", `class="SkinDialog"`, "owner=0x122", "root=0x123", "root_owner=0x122", "size=300x500", "unique_unverified", `\n\x1b[31m`, `label="合成标签\n\x1b[31m"`, `path="C:\\合成\n\\ShiYeLine.exe"`, "creationtime=12345 alive=true"} {
		if !strings.Contains(out.String(), text) {
			t.Errorf("输出缺少 %q", text)
		}
	}
	if strings.ContainsRune(out.String(), '\x1b') {
		t.Fatal("标题中的终端控制字符未转义")
	}
}

// failingWriter 通过 short 选择短写或关闭错误，不访问外部资源。
type failingWriter struct{ short bool }

// Write 模拟 Writer 失败；short 为真时只写入部分数据，否则返回关闭错误。
func (w failingWriter) Write(p []byte) (int, error) {
	if w.short {
		return len(p) / 2, nil
	}
	return 0, io.ErrClosedPipe
}

// TestOutputFailure 验证普通写入错误和短写入都能返回给调用者。
func TestOutputFailure(t *testing.T) {
	for _, tt := range []struct {
		writer failingWriter
		want   error
	}{{failingWriter{}, io.ErrClosedPipe}, {failingWriter{short: true}, io.ErrShortWrite}} {
		if err := writeText(tt.writer, windowprobe.Report{}); !errors.Is(err, tt.want) {
			t.Errorf("error=%v, want %v", err, tt.want)
		}
	}
}

// syntheticReport 按序号构造时间有序且身份完整的唯一候选快照；不读取系统状态，不产生错误。
func syntheticReport(index int) windowprobe.Report {
	start := time.Date(2026, 1, 1, 0, 0, index*2, 0, time.UTC)
	session := uint32(1)
	r := windowprobe.Report{
		StartedAt: start, FinishedAt: start.Add(time.Second),
		Processes: []windowprobe.Process{{PID: 10, Name: "ShiYeLine.exe", SessionID: &session,
			Path: `C:\synthetic\ShiYeLine.exe`, CreationTime: 12345, Alive: true}},
		Windows: []windowprobe.Window{{HWND: 0x123, PID: 10, TID: 20, Class: "SkinDialog",
			Root: 0x123, RootOwner: 0x123, TopLevel: true, Stable: true}},
	}
	windowprobe.Assess(&r)
	return r
}

// syntheticObserver 用 count 和 label 构造有序样本及比较，忽略真实等待；ctx 已取消时返回取消错误。
func syntheticObserver(ctx context.Context, count int, _ time.Duration, label string) (windowprobe.Series, error) {
	s := windowprobe.Series{Label: label, Scans: []windowprobe.Report{}, Comparisons: []windowprobe.Comparison{}}
	if err := ctx.Err(); err != nil {
		return s, err
	}
	for i := 0; i < count; i++ {
		r := syntheticReport(i + 1)
		r.Label = label
		s.Scans = append(s.Scans, r)
		if i > 0 {
			s.Comparisons = append(s.Comparisons, windowprobe.Compare(s.Scans[i-1], r))
		}
	}
	return s, nil
}

// baselineJSON 将合成值编码成 JSON；编码失败时终止当前测试，返回可用于 baseline 的字节。
func baselineJSON(t *testing.T, value any) []byte {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// baselineFile 将合成 baseline 写入测试临时目录并返回路径；写入错误时终止测试。
func baselineFile(t *testing.T, data []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "baseline.json")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestDecodeBaseline 验证 Report/Series 往返、严格解码、非法句柄和各样本时间必填边界。
func TestDecodeBaseline(t *testing.T) {
	r := syntheticReport(0)
	data := baselineJSON(t, r)
	s := windowprobe.Series{Scans: []windowprobe.Report{r, syntheticReport(1)}}
	for _, value := range []any{r, s} {
		got, err := decodeBaseline(append(baselineJSON(t, value), '\n'))
		if err != nil {
			t.Fatal(err)
		}
		want := r.FinishedAt
		if _, ok := value.(windowprobe.Series); ok {
			want = s.Scans[1].FinishedAt
		}
		if !got.FinishedAt.Equal(want) || got.Windows[0].HWND != 0x123 || got.Processes[0].CreationTime != 12345 {
			t.Errorf("baseline roundtrip=%+v", got)
		}
	}
	for name, input := range map[string]string{
		"empty": "", "null": "null", "array": "[]", "empty object": "{}",
		"empty series": `{"scans":[]}`, "null scans": `{"scans":null}`,
		"unknown":        strings.Replace(string(data), `"started_at"`, `"unknown"`, 1),
		"nested unknown": strings.Replace(string(data), `"pid":10`, `"pid":10,"unknown":true`, 1),
		"trailing json":  string(data) + `{}`, "trailing null": string(data) + ` null`,
		"trailing garbage":   string(data) + ` x`,
		"missing start":      `{"finished_at":"2026-01-01T00:00:01Z"}`,
		"missing finish":     `{"started_at":"2026-01-01T00:00:00Z"}`,
		"null time":          `{"started_at":null,"finished_at":"2026-01-01T00:00:01Z"}`,
		"zero time":          `{"started_at":"0001-01-01T00:00:00Z","finished_at":"2026-01-01T00:00:01Z"}`,
		"bad time":           `{"started_at":"bad","finished_at":"2026-01-01T00:00:01Z"}`,
		"bad handle":         strings.Replace(string(data), `"hwnd":"0x123"`, `"hwnd":"123"`, 1),
		"overflow handle":    strings.Replace(string(data), `"hwnd":"0x123"`, `"hwnd":"0x10000000000000000"`, 1),
		"numeric handle":     strings.Replace(string(data), `"hwnd":"0x123"`, `"hwnd":291`, 1),
		"bad earlier scan":   string(baselineJSON(t, windowprobe.Series{Scans: []windowprobe.Report{{}, r}})),
		"series unknown":     `{"scans":[` + string(data) + `],"unknown":1}`,
		"comparison unknown": `{"scans":[` + string(data) + `],"comparisons":[{"unknown":1}]}`,
		"comparison handle":  `{"scans":[` + string(data) + `],"comparisons":[{"changes":[{"hwnd":"bad"}]}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := decodeBaseline([]byte(input)); err == nil {
				t.Fatalf("非法 baseline 未被拒绝: %s", input)
			}
		})
	}
}

// TestCollectionArguments 验证默认与边界参数传递、单次 Report 兼容以及重复采集 Series 输出。
func TestCollectionArguments(t *testing.T) {
	for _, tt := range []struct {
		args     []string
		count    int
		interval time.Duration
		label    string
	}{
		{nil, 1, time.Second, ""},
		{[]string{"-count", "1", "-interval", "0", "-label", "合成\n状态"}, 1, 0, "合成\n状态"},
		{[]string{"-count", "2", "-interval", "1ms"}, 2, time.Millisecond, ""},
		{[]string{"-count", "1000", "-interval", "1ms"}, 1000, time.Millisecond, ""},
	} {
		var out, errOut bytes.Buffer
		called := false
		observer := func(ctx context.Context, count int, interval time.Duration, label string) (windowprobe.Series, error) {
			called = true
			if count != tt.count || interval != tt.interval || label != tt.label {
				t.Errorf("采集参数=%d %s %q", count, interval, label)
			}
			return syntheticObserver(ctx, count, interval, label)
		}
		args := append(append([]string{}, tt.args...), "-format", "json")
		if code := runWithObserver(context.Background(), args, &out, &errOut, observer); code != 0 || !called {
			t.Fatalf("code=%d called=%t stderr=%s", code, called, errOut.String())
		}
		if tt.count == 1 {
			var r windowprobe.Report
			if err := decodeStrict(out.Bytes(), &r); err != nil || r.Label != tt.label || r.StartedAt.IsZero() {
				t.Fatalf("单次 Report 不兼容: %s err=%v", out.String(), err)
			}
		} else {
			var s windowprobe.Series
			if err := decodeStrict(out.Bytes(), &s); err != nil || len(s.Scans) != tt.count ||
				len(s.Comparisons) != tt.count-1 || s.Stability != "stable_unique_unverified" {
				t.Fatalf("Series 数量或稳定性错误: scans=%d comparisons=%d stability=%s err=%v", len(s.Scans), len(s.Comparisons), s.Stability, err)
			}
		}
	}
}

// TestBaselineBeforeCollection 验证文件和内容错误均在采集前失败，避免无效输入触发扫描。
func TestBaselineBeforeCollection(t *testing.T) {
	for _, path := range []string{filepath.Join(t.TempDir(), "missing.json"), baselineFile(t, []byte(`{"scans":[]}`))} {
		var out, errOut bytes.Buffer
		observer := func(context.Context, int, time.Duration, string) (windowprobe.Series, error) {
			t.Fatal("无效 baseline 不应触发采集")
			return windowprobe.Series{}, nil
		}
		if code := runWithObserver(context.Background(), []string{"-baseline", path}, &out, &errOut, observer); code != 1 || out.Len() != 0 || errOut.Len() == 0 {
			t.Fatalf("code=%d stdout=%s stderr=%s", code, out.String(), errOut.String())
		}
	}
}

// TestBaselineComparison 验证 baseline 的末样本作为首比较，所有比较共同决定稳定性。
func TestBaselineComparison(t *testing.T) {
	for _, count := range []int{1, 2} {
		for _, unstable := range []bool{false, true} {
			before := syntheticReport(0)
			if unstable {
				before.Processes[0].CreationTime++
			}
			path := baselineFile(t, baselineJSON(t, windowprobe.Series{Scans: []windowprobe.Report{syntheticReport(-1), before}}))
			var out, errOut bytes.Buffer
			args := []string{"-format", "json", "-count", fmt.Sprint(count), "-baseline", path, "-label", "合成状态"}
			if code := runWithObserver(context.Background(), args, &out, &errOut, syntheticObserver); code != 0 {
				t.Fatalf("code=%d stderr=%s", code, errOut.String())
			}
			var s windowprobe.Series
			if err := decodeStrict(out.Bytes(), &s); err != nil {
				t.Fatal(err)
			}
			if s.Label != "合成状态" || len(s.Comparisons) != count || !s.Comparisons[0].Before.Equal(before.FinishedAt) {
				t.Fatalf("baseline 比较位置错误: %+v", s)
			}
			want := "stable_unique_unverified"
			if unstable {
				want = "not_stable"
			}
			if s.Stability != want || s.Comparisons[0].Stable == unstable {
				t.Errorf("stability=%s comparison=%+v", s.Stability, s.Comparisons[0])
			}
		}
	}
}

// TestPartialCollection 验证取消和扫描错误输出已完成的 Series，无样本时只输出错误并返回 1。
func TestPartialCollection(t *testing.T) {
	for _, scanErr := range []error{context.Canceled, errors.New("合成扫描错误")} {
		for _, completed := range []int{0, 1, 2} {
			for _, withBaseline := range []bool{false, true} {
				observer := func(ctx context.Context, _ int, interval time.Duration, label string) (windowprobe.Series, error) {
					s, _ := syntheticObserver(ctx, completed, interval, label)
					return s, scanErr
				}
				args := []string{"-count", "3", "-format", "json"}
				if withBaseline {
					args = append(args, "-baseline", baselineFile(t, baselineJSON(t, syntheticReport(0))))
				}
				var out, errOut bytes.Buffer
				if code := runWithObserver(context.Background(), args, &out, &errOut, observer); code != 1 || !strings.Contains(errOut.String(), scanErr.Error()) {
					t.Fatalf("code=%d stderr=%s", code, errOut.String())
				}
				if completed == 0 {
					if out.Len() != 0 {
						t.Fatal("无样本时不应伪造结果")
					}
					continue
				}
				var s windowprobe.Series
				if err := decodeStrict(out.Bytes(), &s); err != nil || len(s.Scans) != completed {
					t.Fatalf("部分样本未保留: %s err=%v", out.String(), err)
				}
				want := "insufficient_samples"
				if completed > 1 || withBaseline {
					want = "stable_unique_unverified"
				}
				if s.Stability != want {
					t.Errorf("stability=%s want=%s", s.Stability, want)
				}
			}
		}
	}
}

// TestCancelledContext 验证已取消的采集上下文不会输出伪造样本，返回失败退出码。
func TestCancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var out, errOut bytes.Buffer
	if code := runWithObserver(ctx, nil, &out, &errOut, syntheticObserver); code != 1 || out.Len() != 0 || !strings.Contains(errOut.String(), "canceled") {
		t.Fatalf("code=%d stdout=%s stderr=%s", code, out.String(), errOut.String())
	}
}

// TestDiagnosticExitCodes 验证任意不完整样本和 baseline 比较失败退出，其他诊断不提升为 CLI 错误。
func TestDiagnosticExitCodes(t *testing.T) {
	for _, status := range []string{"incomplete", "not_found", "ambiguous", "unique_unverified"} {
		observer := func(ctx context.Context, count int, interval time.Duration, label string) (windowprobe.Series, error) {
			s, err := syntheticObserver(ctx, count, interval, label)
			s.Scans[0].Assessment.Status = status
			return s, err
		}
		var out, errOut bytes.Buffer
		want := 0
		if status == "incomplete" {
			want = 1
		}
		if code := runWithObserver(context.Background(), []string{"-count", "2"}, &out, &errOut, observer); code != want {
			t.Errorf("status=%s code=%d want=%d", status, code, want)
		}
	}
	before := syntheticReport(0)
	before.Processes[0].Path = ""
	path := baselineFile(t, baselineJSON(t, before))
	var out, errOut bytes.Buffer
	if code := runWithObserver(context.Background(), []string{"-baseline", path}, &out, &errOut, syntheticObserver); code != 1 || !strings.Contains(out.String(), "status=incomplete") {
		t.Fatalf("不完整 baseline 比较 code=%d stdout=%s stderr=%s", code, out.String(), errOut.String())
	}
}

// TestSeriesTextOutput 验证每次快照、比较变化、状态、稳定性及标签转义均输出。
func TestSeriesTextOutput(t *testing.T) {
	s, err := syntheticObserver(context.Background(), 2, time.Millisecond, "合成\n\x1b")
	if err != nil {
		t.Fatal(err)
	}
	s.Stability = "not_stable"
	s.Comparisons[0].Status = "identity_changed"
	s.Comparisons[0].Changes = []windowprobe.Change{{Kind: "window_identity", HWND: 0x123, Details: "合成\n变化"}}
	var out bytes.Buffer
	if err := writeResult(&out, "text", s, true); err != nil {
		t.Fatal(err)
	}
	if strings.Count(out.String(), "只读窗口快照") != 2 || strings.ContainsRune(out.String(), '\x1b') {
		t.Fatalf("快照遗漏或标签未转义: %s", out.String())
	}
	for _, text := range []string{`label="合成\n\x1b"`, "stability=not_stable", "status=identity_changed", "change kind=window_identity HWND=0x123", `details="合成\n变化"`} {
		if !strings.Contains(out.String(), text) {
			t.Errorf("输出缺少 %q", text)
		}
	}
}

// TestCLIOutputFailure 验证单次与 Series 的文本/JSON 输出在错误和短写时均返回 1。
func TestCLIOutputFailure(t *testing.T) {
	for _, format := range []string{"text", "json"} {
		for _, count := range []string{"1", "2"} {
			for _, writer := range []failingWriter{{}, {short: true}} {
				var errOut bytes.Buffer
				args := []string{"-format", format, "-count", count}
				if code := runWithObserver(context.Background(), args, writer, &errOut, syntheticObserver); code != 1 || !strings.Contains(errOut.String(), "输出失败") {
					t.Errorf("format=%s count=%s short=%t code=%d stderr=%s", format, count, writer.short, code, errOut.String())
				}
			}
		}
	}
}
