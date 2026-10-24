// 本文件验证群查询命令的参数处理、退出码与 JSON 输出，全部使用合成明文测试库。
package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

const fixturePath = "../../internal/groups/testdata/qrp-synthetic.db"

// runFixture 对合成群库执行一次命令并返回退出码与 stdout JSON。
func runFixture(t *testing.T, args []string) (int, result) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code := run(args, &stdout, &stderr)
	var output result
	if stdout.Len() > 0 {
		if err := json.Unmarshal(stdout.Bytes(), &output); err != nil {
			t.Fatalf("解析输出 JSON %q: %v", stdout.String(), err)
		}
	}
	return code, output
}

// TestRunUnique 验证唯一群名命中的退出码与候选输出。
func TestRunUnique(t *testing.T) {
	code, output := runFixture(t,
		[]string{"-qrp", fixturePath, "-table", "QGroupInfo_TESTID_A", "-name", "测试群 Alpha"})
	if code != 0 || output.Status != "unique" || len(output.Candidates) != 1 ||
		output.Candidates[0].GroupID != "g-alpha-001" {
		t.Errorf("退出码 %d，输出 %+v，不符合唯一命中预期", code, output)
	}
}

// TestRunAmbiguous 验证同名群歧义返回退出码 4 与全部候选。
func TestRunAmbiguous(t *testing.T) {
	code, output := runFixture(t,
		[]string{"-qrp", fixturePath, "-table", "QGroupInfo_TESTID_A", "-name", "Beta 群"})
	if code != 4 || output.Status != "ambiguous" || len(output.Candidates) != 2 {
		t.Errorf("退出码 %d，输出 %+v，不符合同名歧义预期", code, output)
	}
}

// TestRunNotFound 验证未命中返回退出码 3。
func TestRunNotFound(t *testing.T) {
	code, output := runFixture(t,
		[]string{"-qrp", fixturePath, "-table", "QGroupInfo_TESTID_A", "-name", "不存在的群"})
	if code != 3 || output.Status != "not_found" {
		t.Errorf("退出码 %d，输出 %+v，不符合未命中预期", code, output)
	}
}

// TestRunListAll 验证缺省 -name 时输出全部群（含空名跳过后的 4 条）。
func TestRunListAll(t *testing.T) {
	code, output := runFixture(t,
		[]string{"-qrp", fixturePath, "-table", "QGroupInfo_TESTID_A"})
	if code != 0 || output.Status != "listed" || len(output.Candidates) != 4 {
		t.Errorf("退出码 %d，候选 %d 个，不符合列表预期", code, len(output.Candidates))
	}
}

// TestRunRequiresTableForMulti 验证多身份表未指定 -table 时报错。
func TestRunRequiresTableForMulti(t *testing.T) {
	code, _ := runFixture(t, []string{"-qrp", fixturePath})
	if code != 1 {
		t.Errorf("多身份表未指定 -table 应退出码 1，实际 %d", code)
	}
}

// TestRunCorruptTableRejected 验证坏行表的整体失败以命令错误退出。
func TestRunCorruptTableRejected(t *testing.T) {
	code, _ := runFixture(t,
		[]string{"-qrp", fixturePath, "-table", "QGroupInfo_TESTID_B", "-name", "坏 ID 群"})
	if code != 1 {
		t.Errorf("坏行表应退出码 1，实际 %d", code)
	}
}

// TestRunUTF16Fixture 验证 UTF-16le 群库上命令正常工作。
func TestRunUTF16Fixture(t *testing.T) {
	code, output := runFixture(t,
		[]string{"-qrp", "../../internal/groups/testdata/qrp-synthetic-utf16.db",
			"-table", "QGroupInfo_TESTID_A", "-name", "测试群 Alpha"})
	if code != 0 || output.Status != "unique" || len(output.Candidates) != 1 {
		t.Errorf("退出码 %d，输出 %+v，不符合 UTF-16 库唯一命中预期", code, output)
	}
}

// TestRunUsageErrors 验证缺 -qrp 与多余位置参数返回退出码 2。
func TestRunUsageErrors(t *testing.T) {
	if code, _ := runFixture(t, nil); code != 2 {
		t.Errorf("缺少 -qrp 应退出码 2，实际 %d", code)
	}
	if code, _ := runFixture(t, []string{"-qrp", fixturePath, "extra"}); code != 2 {
		t.Errorf("位置参数应退出码 2，实际 %d", code)
	}
}

// TestRunGarbageRejected 验证非 SQLite 文件被明确拒绝，不寻找密钥。
func TestRunGarbageRejected(t *testing.T) {
	path := filepath.Join(t.TempDir(), "not-qrp")
	if err := os.WriteFile(path, []byte("not a sqlite file at all, definitely not"), 0o600); err != nil {
		t.Fatal(err)
	}
	code, _ := runFixture(t, []string{"-qrp", path})
	if code != 1 {
		t.Errorf("非 SQLite 文件应退出码 1，实际 %d", code)
	}
}

// TestRunSidecarRejected 验证存在边文件的副本被拒绝。
func TestRunSidecarRejected(t *testing.T) {
	data, err := os.ReadFile(fixturePath)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "qrp-copy")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path+"-journal", nil, 0o600); err != nil {
		t.Fatal(err)
	}
	code, _ := runFixture(t, []string{"-qrp", path})
	if code != 1 {
		t.Errorf("带边文件的副本应退出码 1，实际 %d", code)
	}
}
