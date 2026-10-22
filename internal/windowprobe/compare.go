// 本文件比较独立扫描，区分进程/窗口身份变化与最小化等外观变化，不缓存选中的 HWND。
package windowprobe

import (
	"fmt"
	"reflect"
	"strings"
	"time"
)

// Change 记录变化类型与句柄，不记录标题或聊天内容；Details 仅含元数据变化说明。
type Change struct {
	Kind    string `json:"kind"`
	HWND    Handle `json:"hwnd,omitempty"`
	Details string `json:"details"`
}

// Comparison 是两次完整扫描的对比；Stable 只表示唯一候选及 owner 的外部身份一致。
type Comparison struct {
	Before  time.Time `json:"before"`
	After   time.Time `json:"after"`
	Status  string    `json:"status"`
	Stable  bool      `json:"stable"`
	Changes []Change  `json:"changes"`
}

// Series 保存有状态标签的重复扫描和相邻比较；不把多次一致提升为已验证 IPC 接收端。
type Series struct {
	Label       string       `json:"label"`
	Scans       []Report     `json:"scans"`
	Comparisons []Comparison `json:"comparisons"`
	Stability   string       `json:"stability"`
}

// Compare 重新评估 before/after 并比较身份、候选集和窗口变化，不修改输入或产生错误。
// 缺失路径/创建时间、时间顺序错误、采集失败、多候选均不能得到稳定结论。
func Compare(before, after Report) Comparison {
	before, after = assessed(before), assessed(after)
	c := Comparison{Before: before.FinishedAt, After: after.FinishedAt, Changes: []Change{}}
	changed := false
	oldProcesses := make(map[uint32]Process, len(before.Processes))
	for _, p := range before.Processes {
		oldProcesses[p.PID] = p
	}
	for _, p := range after.Processes {
		old, exists := oldProcesses[p.PID]
		if !exists || !sameProcess(old, p) {
			c.Changes = append(c.Changes, Change{Kind: "process_identity", Details: fmt.Sprintf("PID=%d 新增或路径、创建时间、session 改变", p.PID)})
			changed = true
		}
		delete(oldProcesses, p.PID)
	}
	for _, p := range before.Processes {
		if _, removed := oldProcesses[p.PID]; removed {
			c.Changes = append(c.Changes, Change{Kind: "process_removed", Details: fmt.Sprintf("PID=%d 不再存在", p.PID)})
			changed = true
		}
	}
	if !reflect.DeepEqual(handleSet(before.Assessment.Candidates), handleSet(after.Assessment.Candidates)) {
		c.Changes = append(c.Changes, Change{Kind: "candidate_set", Details: "候选集合改变，旧 HWND 不可沿用"})
		changed = true
	}
	oldWindows := make(map[Handle]Window, len(before.Windows))
	for _, w := range before.Windows {
		oldWindows[w.HWND] = w
	}
	for _, w := range after.Windows {
		old, exists := oldWindows[w.HWND]
		switch {
		case !exists:
			c.Changes = append(c.Changes, Change{Kind: "window_added", HWND: w.HWND, Details: "新增顶层窗口"})
		case !sameWindow(old, w):
			c.Changes = append(c.Changes, Change{Kind: "window_identity", HWND: w.HWND, Details: "PID/TID、类名或 owner/root/parent/顶层关系改变"})
		case old.Visible != w.Visible || old.Minimized != w.Minimized || old.Style != w.Style || old.ExStyle != w.ExStyle || !reflect.DeepEqual(old.Rect, w.Rect):
			c.Changes = append(c.Changes, Change{Kind: "presentation", HWND: w.HWND, Details: fmt.Sprintf("visible=%t→%t minimized=%t→%t；外观变化不单独作为身份变化", old.Visible, w.Visible, old.Minimized, w.Minimized)})
		}
		if (!exists && relevant(after, w.HWND)) ||
			(exists && !sameWindow(old, w) && (relevant(before, old.HWND) || relevant(after, w.HWND))) {
			changed = true
		}
		delete(oldWindows, w.HWND)
	}
	for _, w := range before.Windows {
		if _, exists := oldWindows[w.HWND]; exists {
			c.Changes = append(c.Changes, Change{Kind: "window_removed", HWND: w.HWND, Details: "顶层窗口消失"})
			if relevant(before, w.HWND) {
				changed = true
			}
		}
	}
	switch {
	case !completeIdentity(before) || !completeIdentity(after) || before.StartedAt.IsZero() ||
		before.FinishedAt.Before(before.StartedAt) || after.StartedAt.Before(before.FinishedAt) || after.FinishedAt.Before(after.StartedAt) ||
		!after.StartedAt.After(before.StartedAt):
		c.Status = "incomplete"
	case before.Assessment.Status == "ambiguous" || after.Assessment.Status == "ambiguous":
		c.Status = "ambiguous"
	case before.Assessment.Status != "unique_unverified" || after.Assessment.Status != "unique_unverified":
		c.Status = "unresolved"
	case changed:
		c.Status = "identity_changed"
	default:
		c.Status, c.Stable = "stable_unique_unverified", true
	}
	return c
}

func assessed(r Report) Report {
	r.Windows = append([]Window(nil), r.Windows...)
	Assess(&r)
	return r
}

func sameProcess(a, b Process) bool {
	return a.PID == b.PID && samePath(a.Path, b.Path) && a.CreationTime == b.CreationTime &&
		(a.SessionID == nil) == (b.SessionID == nil) && (a.SessionID == nil || *a.SessionID == *b.SessionID)
}

func samePath(a, b string) bool {
	return strings.EqualFold(strings.ReplaceAll(a, "/", `\`), strings.ReplaceAll(b, "/", `\`))
}

func sameWindow(a, b Window) bool {
	return a.HWND == b.HWND && a.PID == b.PID && a.TID == b.TID && a.Class == b.Class &&
		a.Owner == b.Owner && a.Parent == b.Parent && a.Root == b.Root && a.RootOwner == b.RootOwner && a.TopLevel == b.TopLevel
}

func handleSet(handles []Handle) map[Handle]bool {
	result := make(map[Handle]bool, len(handles))
	for _, h := range handles {
		result[h] = true
	}
	return result
}

func relevant(r Report, h Handle) bool {
	for _, w := range r.Windows {
		if w.Candidate && (w.HWND == h || w.Owner == h) {
			return true
		}
	}
	return false
}

func completeIdentity(r Report) bool {
	if r.Assessment.Status == "incomplete" {
		return false
	}
	for _, p := range r.Processes {
		if p.Path == "" || p.CreationTime == 0 || p.SessionID == nil || !p.Alive || len(p.Issues) != 0 {
			return false
		}
	}
	return true
}
