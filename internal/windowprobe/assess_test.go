// 本文件用合成窗口快照验证分类、歧义检测和不完整数据的处理，不接触真实客户端。
package windowprobe

import (
	"encoding/json"
	"strings"
	"testing"
)

// skin 为指定句柄和 owner 构造稳定顶层 SkinDialog 快照，visible 表示系统可见性；不产生错误。
func skin(hwnd, owner Handle, visible bool) Window {
	return Window{HWND: hwnd, PID: 10, TID: 20, Class: "SkinDialog", Visible: visible,
		Owner: owner, Root: hwnd, TopLevel: true, Stable: true, Issues: []string{}}
}

// TestAssessCandidates 验证隐藏主窗口、多实例和普通对话框不能被草率筛除。
func TestAssessCandidates(t *testing.T) {
	tests := []struct {
		name      string
		processes []Process
		windows   []Window
		status    string
		count     int
		roles     []string
	}{
		{"not found", nil, nil, "not_found", 0, nil},
		{"process without windows", []Process{{PID: 10}}, nil, "no_candidate", 0, nil},
		{"owner and main", []Process{{PID: 10}}, []Window{skin(1, 0, false), skin(2, 1, true)},
			"unique_unverified", 1, []string{"hidden_owner_suspect", "main_candidate"}},
		{"hidden main", []Process{{PID: 10}}, []Window{skin(1, 0, false), skin(2, 1, false)},
			"unique_unverified", 1, []string{"hidden_owner_suspect", "main_candidate"}},
		{"two owner siblings", []Process{{PID: 10}}, []Window{skin(1, 0, false), skin(2, 1, true), skin(3, 1, true)},
			"ambiguous", 2, []string{"hidden_owner_suspect", "main_candidate", "main_candidate"}},
		{"ordinary owned dialog", []Process{{PID: 10}}, []Window{skin(1, 0, false), skin(2, 1, true), skin(3, 2, true)},
			"ambiguous", 2, []string{"hidden_owner_suspect", "main_candidate", "owned_dialog_suspect"}},
		{"unowned generic", []Process{{PID: 10}}, []Window{skin(1, 0, true)},
			"unique_unverified", 1, []string{"skin_dialog_unclassified"}},
		{"owner missing", []Process{{PID: 10}}, []Window{skin(2, 99, true)},
			"unique_unverified", 1, []string{"skin_dialog_unclassified"}},
		{"multiple processes one desktop candidate", []Process{{PID: 10}, {PID: 11}}, []Window{skin(1, 0, true)},
			"ambiguous", 1, []string{"skin_dialog_unclassified"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := Report{Processes: tt.processes, Windows: tt.windows}
			Assess(&r)
			if r.Assessment.Status != tt.status || r.Assessment.CandidateCount != tt.count {
				t.Fatalf("assessment = %+v, want status=%s count=%d", r.Assessment, tt.status, tt.count)
			}
			for i, role := range tt.roles {
				if r.Windows[i].Role != role {
					t.Errorf("window %d role=%s, want %s", i, r.Windows[i].Role, role)
				}
			}
			Assess(&r)
			if r.Assessment.CandidateCount != tt.count {
				t.Fatal("重复评估不应累积候选")
			}
		})
	}
}

// TestIncompleteSnapshot 验证读取失败或复查变化不得被“唯一候选”掩盖。
func TestIncompleteSnapshot(t *testing.T) {
	for _, problem := range []string{"global", "process", "window", "unstable"} {
		t.Run(problem, func(t *testing.T) {
			r := Report{Processes: []Process{{PID: 10}}, Windows: []Window{skin(1, 0, true)}}
			switch problem {
			case "global":
				r.Issues = []string{"enum failed"}
			case "process":
				r.Processes[0].Issues = []string{"session failed"}
			case "window":
				r.Windows[0].Issues = []string{"class failed"}
			case "unstable":
				r.Windows[0].Stable = false
			}
			Assess(&r)
			if r.Assessment.Status != "incomplete" {
				t.Fatalf("status=%s", r.Assessment.Status)
			}
		})
	}
}

// TestNonSkinAndChildExcluded 验证非 SkinDialog 和子窗口不因标题而成为候选。
func TestNonSkinAndChildExcluded(t *testing.T) {
	a, b := skin(1, 0, true), skin(2, 0, true)
	a.Class, a.Title = "OtherClass", "内网通"
	b.TopLevel = false
	r := Report{Processes: []Process{{PID: 10}}, Windows: []Window{a, b}}
	Assess(&r)
	if r.Assessment.CandidateCount != 0 {
		t.Fatal("非目标类或子窗口被纳入候选")
	}
}

// TestCrossPIDOwnerNotClassified 验证其他进程窗口不能充当本进程隐藏 owner 的证据。
func TestCrossPIDOwnerNotClassified(t *testing.T) {
	owner := skin(1, 0, false)
	owner.PID = 11
	r := Report{Processes: []Process{{PID: 10}, {PID: 11}}, Windows: []Window{owner, skin(2, 1, true)}}
	Assess(&r)
	if r.Windows[0].Role == "hidden_owner_suspect" || r.Windows[1].Role == "main_candidate" {
		t.Fatal("跨 PID owner 关系被误认为静态创建关系")
	}
}

// TestHandleJSON 验证句柄以十六进制字符串输出，避免 JSON 数字精度丢失 x64 高位。
func TestHandleJSON(t *testing.T) {
	data, err := json.Marshal(struct {
		HWND Handle `json:"hwnd"`
	}{0xFEDCBA9876543210})
	if err != nil || !strings.Contains(string(data), `"0xFEDCBA9876543210"`) {
		t.Fatalf("JSON=%s error=%v", data, err)
	}
}
