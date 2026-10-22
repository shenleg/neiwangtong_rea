// 本文件提供后续 IPC Adapter 可复用的候选定位接口；不包含 IPC 调用或长期 HWND 缓存。
package windowprobe

import (
	"fmt"
	"strings"
	"time"
)

// Target 记录一次复核通过的候选和 owner；不证明业务处理器身份，也不保证返回后仍有效。
type Target struct {
	Process     Process   `json:"process"`
	Window      Window    `json:"window"`
	Owner       Window    `json:"owner"`
	CheckedAt   time.Time `json:"checked_at"`
	Reliability string    `json:"reliability"`
}

// Resolve 每次重新扫描并在返回前复核唯一候选、进程存活/路径/创建时间和窗口 owner 关系。
// expectedPath 必须是调用方确认的 Windows EXE 绝对路径；previous 可为空，非空则拒绝跨进程或窗口身份变化。
// 多实例、多候选、路径不符、缺失隐藏 owner、采集或最终复核失败均返回零值和错误；绝不返回默认候选。
// 返回的 unique_unverified 仅供研究定位，不是发送许可；未来 Adapter 每次调用前必须重新 Resolve。
func Resolve(expectedPath string, previous *Target) (Target, error) {
	return resolve(expectedPath, previous, Scan, verifyTarget)
}

func resolve(expectedPath string, previous *Target, scan func() (Report, error), verify func(Target) error) (Target, error) {
	// 不接受相对路径、设备路径、NUL 或仅文件名；不展开环境变量或猜测安装位置。
	if !absoluteEXEPath(expectedPath) {
		return Target{}, fmt.Errorf("必须提供以 ShiYeLine.exe 结尾的 Windows 绝对路径")
	}
	r, err := scan()
	if err != nil {
		return Target{}, fmt.Errorf("重新扫描: %w", err)
	}
	r = assessed(r)
	if r.Assessment.Status != "unique_unverified" || !completeIdentity(r) {
		return Target{}, fmt.Errorf("拒绝选择 HWND: assessment=%s candidates=%d", r.Assessment.Status, r.Assessment.CandidateCount)
	}
	p := r.Processes[0]
	if !samePath(p.Path, expectedPath) {
		return Target{}, fmt.Errorf("PID=%d 的可执行路径与指定路径不符", p.PID)
	}
	var main, owner Window
	for _, w := range r.Windows {
		if w.Candidate {
			main = w
		}
	}
	for _, w := range r.Windows {
		if w.HWND == main.Owner {
			owner = w
		}
	}
	if main.PID != p.PID || main.Role != "main_candidate" || owner.Role != "hidden_owner_suspect" || owner.PID != p.PID ||
		main.RootOwner != owner.HWND || main.Owner != owner.HWND || owner.RootOwner != owner.HWND {
		return Target{}, fmt.Errorf("唯一 SkinDialog 未形成已观察到的同 PID 隐藏 owner 关系")
	}
	target := Target{Process: p, Window: main, Owner: owner, Reliability: "unique_unverified"}
	if previous != nil && (!sameProcess(previous.Process, p) || !sameWindow(previous.Window, main) || !sameWindow(previous.Owner, owner)) {
		return Target{}, fmt.Errorf("进程或窗口身份已改变；拒绝沿用旧 HWND，需明确重新获取候选")
	}
	if err := verify(target); err != nil {
		return Target{}, fmt.Errorf("返回 HWND 前复核失败: %w", err)
	}
	target.CheckedAt = time.Now().UTC()
	return target, nil
}

func absoluteEXEPath(path string) bool {
	path = strings.ReplaceAll(path, "/", `\`)
	if strings.ContainsRune(path, 0) || strings.HasPrefix(path, `\\?\`) || strings.HasPrefix(path, `\\.\`) ||
		!strings.HasSuffix(strings.ToLower(path), `\shiyeline.exe`) {
		return false
	}
	if len(path) >= 3 && ((path[0] >= 'A' && path[0] <= 'Z') || (path[0] >= 'a' && path[0] <= 'z')) && path[1:3] == `:\` {
		return true
	}
	parts := strings.Split(path, `\`)
	return len(parts) >= 5 && parts[0] == "" && parts[1] == "" && parts[2] != "" && parts[3] != ""
}
