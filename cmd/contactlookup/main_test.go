// 本文件验证命令入口的参数处理、退出码与 JSON 输出，全部使用合成测试库。
package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

const fixturePath = "../../internal/contacts/testdata/nr-synthetic.db"

// fixturePassword 从清单读取合成口令；清单缺失直接让测试失败。
func fixturePassword(t *testing.T) []byte {
	t.Helper()
	data, err := os.ReadFile("../../internal/contacts/testdata/nr-synthetic-manifest.json")
	if err != nil {
		t.Fatalf("读取清单: %v", err)
	}
	var manifest struct {
		Password string `json:"password"`
	}
	if err := json.Unmarshal(data, &manifest); err != nil {
		t.Fatalf("解析清单: %v", err)
	}
	return []byte(manifest.Password)
}

// runFixture 对合成库执行一次命令并返回退出码与 stdout JSON。
func runFixture(t *testing.T, args []string, stdin []byte) (int, result) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code := run(args, bytes.NewReader(stdin), &stdout, &stderr)
	var output result
	if stdout.Len() > 0 {
		if err := json.Unmarshal(stdout.Bytes(), &output); err != nil {
			t.Fatalf("解析输出 JSON %q: %v", stdout.String(), err)
		}
	}
	return code, output
}

// TestRunUnique 验证唯一命中的退出码与候选输出。
func TestRunUnique(t *testing.T) {
	code, output := runFixture(t,
		[]string{"-nr", fixturePath, "-table", "OrgStruct_TESTID_A", "-name", "Bob"},
		fixturePassword(t))
	if code != 0 || output.Status != "unique" || len(output.Candidates) != 1 ||
		output.Candidates[0].UserID != "u-bob-002" {
		t.Errorf("退出码 %d，输出 %+v，不符合唯一命中预期", code, output)
	}
}

// TestRunAmbiguous 验证同名歧义返回退出码 4 与全部候选。
func TestRunAmbiguous(t *testing.T) {
	code, output := runFixture(t,
		[]string{"-nr", fixturePath, "-table", "OrgStruct_TESTID_A", "-name", "Carol"},
		fixturePassword(t))
	if code != 4 || output.Status != "ambiguous" || len(output.Candidates) != 2 {
		t.Errorf("退出码 %d，输出 %+v，不符合同名歧义预期", code, output)
	}
}

// TestRunNotFound 验证未命中返回退出码 3。
func TestRunNotFound(t *testing.T) {
	code, output := runFixture(t,
		[]string{"-nr", fixturePath, "-table", "OrgStruct_TESTID_A", "-name", "Zed"},
		fixturePassword(t))
	if code != 3 || output.Status != "not_found" {
		t.Errorf("退出码 %d，输出 %+v，不符合未命中预期", code, output)
	}
}

// TestRunListAll 验证缺省 -name 时输出全部用户联系人。
func TestRunListAll(t *testing.T) {
	code, output := runFixture(t,
		[]string{"-nr", fixturePath, "-table", "OrgStruct_TESTID_A"},
		fixturePassword(t))
	if code != 0 || output.Status != "listed" || len(output.Candidates) != 6 {
		t.Errorf("退出码 %d，候选 %d 个，不符合列表预期", code, len(output.Candidates))
	}
}

// TestRunRequiresTableForMulti 验证多身份表未指定 -table 时报错。
func TestRunRequiresTableForMulti(t *testing.T) {
	code, _ := runFixture(t, []string{"-nr", fixturePath}, fixturePassword(t))
	if code != 1 {
		t.Errorf("多身份表未指定 -table 应退出码 1，实际 %d", code)
	}
}

// TestRunUnknownTable 验证未知表名报错。
func TestRunUnknownTable(t *testing.T) {
	code, _ := runFixture(t,
		[]string{"-nr", fixturePath, "-table", "OrgStruct_MISSING", "-name", "Bob"},
		fixturePassword(t))
	if code != 1 {
		t.Errorf("未知表应退出码 1，实际 %d", code)
	}
}

// TestRunWrongPassword 验证错误口令报错；口令从 stdin 读精确字节。
func TestRunWrongPassword(t *testing.T) {
	code, _ := runFixture(t,
		[]string{"-nr", fixturePath, "-table", "OrgStruct_TESTID_A", "-name", "Bob"},
		[]byte("wrong-password"))
	if code != 1 {
		t.Errorf("错误口令应退出码 1，实际 %d", code)
	}
}

// TestRunPasswordExactBytes 验证口令末尾多一个换行即不匹配，文档化精确字节契约。
func TestRunPasswordExactBytes(t *testing.T) {
	code, _ := runFixture(t,
		[]string{"-nr", fixturePath, "-table", "OrgStruct_TESTID_A", "-name", "Bob"},
		append(fixturePassword(t), '\n'))
	if code != 1 {
		t.Errorf("带尾随换行的口令应被拒绝，实际退出码 %d", code)
	}
}

// TestRunUTF16Fixture 验证命令入口在与真实库一致的 UTF-16le 合成库上正常工作。
func TestRunUTF16Fixture(t *testing.T) {
	code, output := runFixture(t,
		[]string{"-nr", "../../internal/contacts/testdata/nr-synthetic-utf16.db",
			"-table", "OrgStruct_TESTID_A", "-name", "Bob"},
		fixturePassword(t))
	if code != 0 || output.Status != "unique" || len(output.Candidates) != 1 ||
		output.Candidates[0].UserID != "u-bob-002" {
		t.Errorf("退出码 %d，输出 %+v，不符合 UTF-16 库唯一命中预期", code, output)
	}
}

// TestRunUsageErrors 验证缺 -nr 与多余位置参数返回退出码 2。
func TestRunUsageErrors(t *testing.T) {
	if code, _ := runFixture(t, nil, fixturePassword(t)); code != 2 {
		t.Errorf("缺少 -nr 应退出码 2，实际 %d", code)
	}
	if code, _ := runFixture(t, []string{"-nr", fixturePath, "extra"}, fixturePassword(t)); code != 2 {
		t.Errorf("位置参数应退出码 2，实际 %d", code)
	}
}

// TestRunSidecarRejected 验证存在边文件的副本在命令入口同样被拒绝。
func TestRunSidecarRejected(t *testing.T) {
	ciphertext, err := os.ReadFile(fixturePath)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "nr-copy")
	if err := os.WriteFile(path, ciphertext, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path+"-wal", nil, 0o600); err != nil {
		t.Fatal(err)
	}
	code, _ := runFixture(t,
		[]string{"-nr", path, "-table", "OrgStruct_TESTID_A", "-name", "Bob"},
		fixturePassword(t))
	if code != 1 {
		t.Errorf("带边文件的副本应退出码 1，实际 %d", code)
	}
}
