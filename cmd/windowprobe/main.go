// windowprobe 只读枚举内网通的顶层窗口，输出元数据与候选可靠性，不提供消息发送功能。
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"time"

	"neiwangtong-gateway/internal/windowprobe"
)

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }

// run 为只读采集注册 Ctrl-C 取消，返回 CLI 退出码；不终止进程，便于调用者处理退出。
func run(args []string, stdout, stderr io.Writer) int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	return runWithObserver(ctx, args, stdout, stderr, windowprobe.Observe)
}

// runWithObserver 校验参数并输出诊断；observe 是采集依赖，测试可用合成样本替代真实扫描。
// 参数错误返回 2，采集、基线、输出错误或不完整诊断返回 1，其余诊断返回 0。
func runWithObserver(ctx context.Context, args []string, stdout, stderr io.Writer,
	observe func(context.Context, int, time.Duration, string) (windowprobe.Series, error)) int {
	flags := flag.NewFlagSet("windowprobe", flag.ContinueOnError)
	flags.SetOutput(stderr)
	format := flags.String("format", "text", "输出格式：text 或 json")
	count := flags.Int("count", 1, "扫描次数：1–1000")
	interval := flags.Duration("interval", time.Second, "扫描间隔：非负，多次扫描至少 1ms")
	label := flags.String("label", "", "操作者声明的状态标签")
	baselinePath := flags.String("baseline", "", "用于首对比的 Report 或 Series JSON 文件")
	if err := flags.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}
	if flags.NArg() != 0 || (*format != "text" && *format != "json") ||
		*count < 1 || *count > 1000 || *interval < 0 || (*count > 1 && *interval < time.Millisecond) {
		fmt.Fprintln(stderr, "用法：windowprobe [-format text|json] [-count 1..1000] [-interval 1s] [-label 状态] [-baseline JSON路径]；多次扫描间隔至少 1ms，不接受 PID、目标或消息参数。")
		return 2
	}
	var baseline *windowprobe.Report
	if *baselinePath != "" {
		data, err := os.ReadFile(*baselinePath)
		if err == nil {
			baseline, err = decodeBaseline(data)
		}
		if err != nil {
			fmt.Fprintf(stderr, "读取 baseline 失败：%v\n", err)
			return 1
		}
	}
	s, scanErr := observe(ctx, *count, *interval, *label)
	if scanErr != nil {
		fmt.Fprintln(stderr, scanErr)
		if len(s.Scans) == 0 {
			return 1
		}
	}
	if baseline != nil {
		first := windowprobe.Compare(*baseline, s.Scans[0])
		s.Comparisons = append([]windowprobe.Comparison{first}, s.Comparisons...)
	}
	// 中断后的部分样本也必须重新计算，不能沿用采集器未完成时的初始稳定性。
	s.Stability = "insufficient_samples"
	if len(s.Comparisons) > 0 {
		s.Stability = "stable_unique_unverified"
		for _, c := range s.Comparisons {
			if !c.Stable {
				s.Stability = "not_stable"
				break
			}
		}
	}
	seriesOutput := *count > 1 || baseline != nil || scanErr != nil
	if err := writeResult(stdout, *format, s, seriesOutput); err != nil {
		fmt.Fprintf(stderr, "输出失败：%v\n", err)
		return 1
	}
	if scanErr != nil {
		return 1
	}
	for _, r := range s.Scans {
		if r.Assessment.Status == "incomplete" {
			return 1
		}
	}
	for _, c := range s.Comparisons {
		if c.Status == "incomplete" {
			return 1
		}
	}
	// 无候选或歧义也是成功执行的只读诊断；调用者应检查 assessment，不能把退出码当成发送许可。
	return 0
}

// decodeBaseline 严格读取单个 Report 或非空 Series，并检查所有样本的采集时间。
// Series 只返回最后一个样本；未知字段、无效句柄、缺失时间或尾随 JSON 均返回错误。
func decodeBaseline(data []byte) (*windowprobe.Report, error) {
	var fields map[string]json.RawMessage
	if err := decodeStrict(data, &fields); err != nil {
		return nil, err
	}
	if fields == nil {
		return nil, fmt.Errorf("baseline 必须是 Report 或 Series 对象")
	}
	var reports []windowprobe.Report
	if _, isSeries := fields["scans"]; isSeries {
		var s windowprobe.Series
		if err := decodeStrict(data, &s); err != nil {
			return nil, err
		}
		if len(s.Scans) == 0 {
			return nil, fmt.Errorf("baseline Series 不得为空")
		}
		reports = s.Scans
	} else {
		var r windowprobe.Report
		if err := decodeStrict(data, &r); err != nil {
			return nil, err
		}
		reports = []windowprobe.Report{r}
	}
	for i, r := range reports {
		if r.StartedAt.IsZero() || r.FinishedAt.IsZero() {
			return nil, fmt.Errorf("baseline 样本 %d 缺少有效 started_at 或 finished_at", i+1)
		}
	}
	return &reports[len(reports)-1], nil
}

// decodeStrict 将唯一 JSON 值解码到 dst，未知字段、格式错误及尾随内容均返回错误。
func decodeStrict(data []byte, dst any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(dst); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err != nil {
			return err
		}
		return fmt.Errorf("baseline 不得包含尾随 JSON")
	}
	return nil
}

// writeResult 按格式输出 Series 或兼容单次调用的 Report；编码或写入失败时返回错误。
func writeResult(out io.Writer, format string, s windowprobe.Series, seriesOutput bool) error {
	if format == "json" {
		var buffer bytes.Buffer
		encoder := json.NewEncoder(&buffer)
		encoder.SetIndent("", "  ")
		encoder.SetEscapeHTML(false)
		var value any = s
		if !seriesOutput {
			value = s.Scans[0]
		}
		if err := encoder.Encode(value); err != nil {
			return err
		}
		return writeBuffer(out, buffer.Bytes())
	}
	if !seriesOutput {
		return writeText(out, s.Scans[0])
	}
	for _, r := range s.Scans {
		if err := writeText(out, r); err != nil {
			return err
		}
	}
	buffer := fmt.Appendf(nil, "\n重复扫描 label=%q samples=%d stability=%s\n", s.Label, len(s.Scans), s.Stability)
	for _, c := range s.Comparisons {
		buffer = fmt.Appendf(buffer, "比较 %s — %s status=%s stable=%t\n",
			c.Before.Format(time.RFC3339Nano), c.After.Format(time.RFC3339Nano), c.Status, c.Stable)
		for _, change := range c.Changes {
			buffer = fmt.Appendf(buffer, "  change kind=%s HWND=%s details=%q\n", change.Kind, change.HWND, change.Details)
		}
	}
	return writeBuffer(out, buffer)
}

// writeText 输出单个快照及转义后的标签、路径和标题；写入失败或短写时返回错误。
func writeText(out io.Writer, r windowprobe.Report) error {
	// 先生成完整文本再写出，统一报告输出错误，避免多处遗漏 Writer 错误。
	var buffer []byte
	buffer = fmt.Appendf(buffer, "只读窗口快照 %s — %s\n进程数=%d 窗口数=%d 候选数=%d 评估=%s\n",
		r.StartedAt.Format("2006-01-02T15:04:05Z07:00"), r.FinishedAt.Format("2006-01-02T15:04:05Z07:00"),
		len(r.Processes), len(r.Windows), r.Assessment.CandidateCount, r.Assessment.Status)
	if r.Label != "" {
		buffer = fmt.Appendf(buffer, "label=%q\n", r.Label)
	}
	for _, p := range r.Processes {
		session := "unknown"
		if p.SessionID != nil {
			session = fmt.Sprint(*p.SessionID)
		}
		buffer = fmt.Appendf(buffer, "\n进程 PID=%d name=%q session=%s path=%q creationtime=%d alive=%t\n",
			p.PID, p.Name, session, p.Path, p.CreationTime, p.Alive)
		for _, issue := range p.Issues {
			buffer = fmt.Appendf(buffer, "  读取问题: %s\n", issue)
		}
		for _, w := range r.Windows {
			if w.PID != p.PID {
				continue
			}
			// 标题以带引号转义的形式输出，避免窗口 caption 中的控制字符影响终端。
			buffer = fmt.Appendf(buffer, "  HWND=%s PID=%d TID=%d class=%q title=%q\n", w.HWND, w.PID, w.TID, w.Class, w.Title)
			buffer = fmt.Appendf(buffer, "    visible=%t minimized=%t top_level=%t stable=%t role=%s candidate=%t\n",
				w.Visible, w.Minimized, w.TopLevel, w.Stable, w.Role, w.Candidate)
			buffer = fmt.Appendf(buffer, "    owner=%s parent=%s root=%s root_owner=%s style=0x%08X ex_style=0x%08X\n",
				w.Owner, w.Parent, w.Root, w.RootOwner, w.Style, w.ExStyle)
			if w.Rect != nil {
				buffer = fmt.Appendf(buffer, "    rect=(%d,%d)-(%d,%d) size=%dx%d\n",
					w.Rect.Left, w.Rect.Top, w.Rect.Right, w.Rect.Bottom, w.Rect.Width, w.Rect.Height)
			} else {
				buffer = append(buffer, "    rect=unknown\n"...)
			}
			for _, reason := range w.Reasons {
				buffer = fmt.Appendf(buffer, "    判断依据: %s\n", reason)
			}
			for _, issue := range w.Issues {
				buffer = fmt.Appendf(buffer, "    读取问题: %s\n", issue)
			}
		}
	}
	for _, issue := range r.Issues {
		buffer = fmt.Appendf(buffer, "\n全局读取问题: %s\n", issue)
	}
	buffer = append(buffer, "\n可靠性评估:\n"...)
	for _, reason := range r.Assessment.Reasons {
		buffer = fmt.Appendf(buffer, "  - %s\n", reason)
	}
	for _, limitation := range r.Assessment.Limitations {
		buffer = fmt.Appendf(buffer, "  - %s\n", limitation)
	}
	return writeBuffer(out, buffer)
}

// writeBuffer 一次写出完整缓冲区，保留底层错误，并将无错误的短写转为 io.ErrShortWrite。
func writeBuffer(out io.Writer, buffer []byte) error {
	n, err := out.Write(buffer)
	if err == nil && n != len(buffer) {
		return io.ErrShortWrite
	}
	return err
}
