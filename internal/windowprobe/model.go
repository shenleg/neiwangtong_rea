// Package windowprobe 记录只读窗口快照，并对 SkinDialog 主窗口候选作保守评估。
package windowprobe

import (
	"fmt"
	"strconv"
	"time"
)

// Handle 是本次快照中的窗口标识；不是可持久化或可直接发送消息的授权。
type Handle uint64

// String 将窗口标识格式化为十六进制；不产生错误。
func (h Handle) String() string { return fmt.Sprintf("0x%X", uint64(h)) }

// MarshalText 为 JSON 等文本编码提供十六进制窗口标识；不产生错误。
func (h Handle) MarshalText() ([]byte, error) { return []byte(h.String()), nil }

// UnmarshalText 读取快照中的十六进制句柄；格式错误或超出 uint64 时返回错误。
func (h *Handle) UnmarshalText(text []byte) error {
	if len(text) < 3 || string(text[:2]) != "0x" {
		return fmt.Errorf("HWND 必须是 0x 开头的十六进制字符串")
	}
	value, err := strconv.ParseUint(string(text[2:]), 16, 64)
	if err != nil {
		return fmt.Errorf("无效 HWND: %w", err)
	}
	*h = Handle(value)
	return nil
}

// Process 是按文件名匹配并查询身份的进程，不代表已验证版本、用户身份或登录状态。
type Process struct {
	PID          uint32   `json:"pid"`
	Name         string   `json:"name"`
	SessionID    *uint32  `json:"session_id"`
	Path         string   `json:"path"`
	CreationTime uint64   `json:"creation_time_ticks,string"` // FILETIME 原始值；字符串避免 JSON 精度丢失。
	Alive        bool     `json:"alive_at_check"`
	Issues       []string `json:"issues"`
}

// Rect 是 GetWindowRect 返回的屏幕坐标；尺寸可能受 DPI 虚拟化影响。
type Rect struct {
	Left   int32 `json:"left"`
	Top    int32 `json:"top"`
	Right  int32 `json:"right"`
	Bottom int32 `json:"bottom"`
	Width  int64 `json:"width"`
	Height int64 `json:"height"`
}

// Window 是 EnumWindows 返回的目标进程顶层窗口元数据，不包含子控件内容。
type Window struct {
	HWND      Handle   `json:"hwnd"`
	PID       uint32   `json:"pid"`
	TID       uint32   `json:"tid"`
	Class     string   `json:"class"`
	Title     string   `json:"title"`
	Visible   bool     `json:"visible"`
	Minimized bool     `json:"minimized"`
	Owner     Handle   `json:"owner"`
	Parent    Handle   `json:"parent"`
	Root      Handle   `json:"root"`
	RootOwner Handle   `json:"root_owner"`
	Style     uint32   `json:"style"`
	ExStyle   uint32   `json:"ex_style"`
	TopLevel  bool     `json:"top_level"`
	Rect      *Rect    `json:"rect"`
	Stable    bool     `json:"stable_at_recheck"`
	Role      string   `json:"role"`
	Candidate bool     `json:"main_candidate"`
	Reasons   []string `json:"reasons"`
	Issues    []string `json:"issues"`
}

// Assessment 描述候选唯一性，不确认消息接收能力或登录状态。
type Assessment struct {
	Status         string   `json:"status"`
	ProcessCount   int      `json:"process_count"`
	CandidateCount int      `json:"candidate_count"`
	Candidates     []Handle `json:"candidates"`
	Reasons        []string `json:"reasons"`
	Limitations    []string `json:"limitations"`
}

// Report 是一次非原子的只读探测结果；Issues 表示全局不完整性。
type Report struct {
	Label      string     `json:"label,omitempty"`
	StartedAt  time.Time  `json:"started_at"`
	FinishedAt time.Time  `json:"finished_at"`
	Processes  []Process  `json:"processes"`
	Windows    []Window   `json:"windows"`
	Issues     []string   `json:"issues"`
	Assessment Assessment `json:"assessment"`
}
