// 本文件仅根据快照中的窗口关系分类，不调用 Windows API 或依赖固定标题。
package windowprobe

// Assess 更新 report 的窗口分类和可靠性结论；输入须为一次完整采集的快照，无错误返回。
// 类名和 owner 只提供启发式证据，绝不生成一个默认选中的发送目标。
func Assess(report *Report) {
	byHandle := make(map[Handle]*Window, len(report.Windows))
	for i := range report.Windows {
		w := &report.Windows[i]
		w.Role, w.Candidate, w.Reasons = "other_window", false, []string{}
		byHandle[w.HWND] = w
	}

	// 0x4029d0 创建隐藏的通用 SkinDialog owner，再让主窗口以其为 owner。
	// 只标记“疑似”，因为其他隐藏对话框也可能形成同样的外部关系。
	hiddenOwners := make(map[Handle]bool)
	for _, w := range report.Windows {
		o := byHandle[w.Owner]
		if usableSkin(w) && o != nil && usableSkin(*o) && o.PID == w.PID && !o.Visible && o.Owner == 0 {
			hiddenOwners[o.HWND] = true
		}
	}

	a := Assessment{
		ProcessCount: len(report.Processes),
		Candidates:   []Handle{},
		Reasons:      []string{},
		Limitations: []string{
			"仅枚举调用线程所在 desktop 的顶层窗口（包含隐藏窗口）；不枚举子控件、message-only 窗口或其他 desktop。",
			"SkinDialog 是通用类；隐藏 owner 与主窗口的分类是基于 0x4029d0 / 0x524890 的启发式推断，不证明 CMainDialog 身份。",
			"标题、可见性和尺寸不作为唯一身份依据；最小化或隐藏不直接排除主窗口。",
			"扫描已查询进程路径、创建时间与检查时存活状态；未核实版本、哈希、登录状态、用户身份、完整性级别或 IPC 可达性。",
			"窗口采集不是原子快照；PID/TID 和关系复查不能阻止复查后销毁或句柄复用，不能缓存为发送许可。",
			"未发送 WM_COPYDATA 或其他探测消息，未验证业务接收与送达。",
		},
	}
	incomplete := len(report.Issues) != 0
	for _, p := range report.Processes {
		incomplete = incomplete || len(p.Issues) != 0
	}
	for i := range report.Windows {
		w := &report.Windows[i]
		incomplete = incomplete || len(w.Issues) != 0 || !w.Stable
		if !usableSkin(*w) {
			continue
		}
		if hiddenOwners[w.HWND] {
			w.Role = "hidden_owner_suspect"
			w.Reasons = append(w.Reasons, "隐藏、无 owner，且被同 PID 的另一个 SkinDialog 直接拥有关系引用。")
			continue
		}
		w.Candidate = true
		w.Role = "skin_dialog_unclassified"
		w.Reasons = append(w.Reasons, "同名进程的稳定顶层 SkinDialog；仅凭通用类无法确认主窗口。")
		if hiddenOwners[w.Owner] {
			w.Role = "main_candidate"
			w.Reasons = append(w.Reasons, "直接 owner 符合隐藏 owner 特征，与 0x4029d0 的创建关系一致。")
		} else if owner := byHandle[w.Owner]; owner != nil && owner.PID == w.PID && owner.Class == "SkinDialog" {
			w.Role = "owned_dialog_suspect"
			w.Reasons = append(w.Reasons, "由另一 SkinDialog 拥有，疑似普通对话框；证据不足以排除，仍保留为候选。")
		}
		a.Candidates = append(a.Candidates, w.HWND)
	}
	a.CandidateCount = len(a.Candidates)
	switch {
	case len(report.Processes) == 0:
		a.Status = "not_found"
		a.Reasons = append(a.Reasons, "进程快照中未找到 ShiYeLine.exe。")
	case len(report.Processes) > 1:
		a.Status = "ambiguous"
		a.Reasons = append(a.Reasons, "发现多个同名进程；不自动选择实例，即使当前 desktop 只有一个窗口候选。")
	case a.CandidateCount == 0:
		a.Status = "no_candidate"
		a.Reasons = append(a.Reasons, "没有可用的 SkinDialog 候选；可能位于其他 desktop、尚未创建或采集失败。")
	case a.CandidateCount > 1:
		a.Status = "ambiguous"
		a.Reasons = append(a.Reasons, "存在多个 SkinDialog 候选；不按标题、面积、可见性或枚举顺序选第一个。")
	default:
		a.Status = "unique_unverified"
		a.Reasons = append(a.Reasons, "仅有一个候选，但未验证其 CMainDialog 身份；仍不可据此发送消息。")
	}
	if incomplete {
		a.Status = "incomplete"
		a.Reasons = append(a.Reasons, "存在 API 读取失败、进程集合变化或窗口复查失败；不能作唯一性结论。")
	}
	report.Assessment = a
}

func usableSkin(w Window) bool {
	return w.Class == "SkinDialog" && w.TopLevel && w.Stable && len(w.Issues) == 0
}
