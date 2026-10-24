// 本文件用合成明文 qrp 群库做端到端验证：打开校验、身份表发现、群加载与群名精确匹配。
package groups

import (
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// qrpManifest 对应 tools/gen_test_fixtures.py 输出的 qrp 期望清单。
type qrpManifest struct {
	PageSize        int                   `json:"page_size"`
	SHA256          string                `json:"sha256"`
	UTF16SHA256     string                `json:"utf16_sha256"`
	Tables          []string              `json:"tables"`
	ExpectedGroups  map[string][][2]string `json:"expected_groups"`
}

// loadQRPManifest 读取并解析 qrp 清单；文件缺失或损坏直接让测试失败。
func loadQRPManifest(t *testing.T) qrpManifest {
	t.Helper()
	data, err := os.ReadFile("testdata/qrp-synthetic-manifest.json")
	if err != nil {
		t.Fatalf("读取清单: %v", err)
	}
	var manifest qrpManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		t.Fatalf("解析清单: %v", err)
	}
	return manifest
}

// openQRPVariant 打开指定编码变体的合成群库并核对摘要与页长。
func openQRPVariant(t *testing.T, filename, wantSHA string) (*DB, qrpManifest) {
	t.Helper()
	manifest := loadQRPManifest(t)
	data, err := os.ReadFile("testdata/" + filename)
	if err != nil {
		t.Fatalf("读取测试库: %v", err)
	}
	if got := hex.EncodeToString(sha256Sum(data)); got != wantSHA {
		t.Fatalf("测试库 %s 摘要 %s 与清单 %s 不符，请用生成脚本重建", filename, got, wantSHA)
	}
	db, err := Open("testdata/" + filename)
	if err != nil {
		t.Fatalf("打开测试库 %s: %v", filename, err)
	}
	if db.PageSize() != manifest.PageSize {
		t.Errorf("页长 %d，预期 %d", db.PageSize(), manifest.PageSize)
	}
	if db.SHA256() != wantSHA {
		t.Errorf("SHA256 输出与清单不符")
	}
	return db, manifest
}

// groupsToPairs 把群列表转成清单期望的 [名称, ID] 对。
func groupsToPairs(list []Group) [][2]string {
	pairs := make([][2]string, len(list))
	for i, group := range list {
		pairs[i] = [2]string{group.Name, group.GroupID}
	}
	return pairs
}

// TestQRPTables 验证身份表发现与生成清单一致。
func TestQRPTables(t *testing.T) {
	db, manifest := openQRPVariant(t, "qrp-synthetic.db", loadQRPManifest(t).SHA256)
	if got := db.Tables(); !reflect.DeepEqual(got, manifest.Tables) {
		t.Errorf("身份表 %v，预期 %v", got, manifest.Tables)
	}
}

// TestQRPLoadGroups 验证群加载与期望一致：覆盖中文名、同名、6KB 溢出行与空群名跳过。
func TestQRPLoadGroups(t *testing.T) {
	db, manifest := openQRPVariant(t, "qrp-synthetic.db", loadQRPManifest(t).SHA256)
	users, err := db.LoadGroups("QGroupInfo_TESTID_A")
	if err != nil {
		t.Fatalf("加载表: %v", err)
	}
	if got := groupsToPairs(users); !reflect.DeepEqual(got, manifest.ExpectedGroups["QGroupInfo_TESTID_A"]) {
		t.Errorf("群列表 %v，预期 %v", got, manifest.ExpectedGroups["QGroupInfo_TESTID_A"])
	}
}

// TestQRPLoadGroupsCorruptRow 验证 QGroupId 为空的行使整表加载失败，不跳过坏行。
func TestQRPLoadGroupsCorruptRow(t *testing.T) {
	db, _ := openQRPVariant(t, "qrp-synthetic.db", loadQRPManifest(t).SHA256)
	if _, err := db.LoadGroups("QGroupInfo_TESTID_B"); err == nil {
		t.Fatal("B 表含空 QGroupId 的坏行，应整体失败")
	}
}

// TestQRPLookup 覆盖唯一命中、同名歧义、未命中与未知表。
func TestQRPLookup(t *testing.T) {
	db, _ := openQRPVariant(t, "qrp-synthetic.db", loadQRPManifest(t).SHA256)
	unique, err := db.LookupName("QGroupInfo_TESTID_A", "测试群 Alpha")
	if err != nil || len(unique) != 1 || unique[0].GroupID != "g-alpha-001" {
		t.Errorf("唯一命中结果 %+v（错误 %v）不符合预期", unique, err)
	}
	ambiguous, err := db.LookupName("QGroupInfo_TESTID_A", "Beta 群")
	if err != nil || len(ambiguous) != 2 {
		t.Errorf("同名歧义应命中 2 条，实际 %+v（错误 %v）", ambiguous, err)
	}
	none, err := db.LookupName("QGroupInfo_TESTID_A", "不存在的群")
	if err != nil || len(none) != 0 {
		t.Errorf("未命中应返回空，实际 %+v（错误 %v）", none, err)
	}
	if _, err := db.LookupName("QGroupInfo_MISSING", "x"); err == nil {
		t.Error("未知表应报错")
	}
}

// TestQRPUTF16 在 UTF-16le 编码群库上重复端到端验证，与真实客户端库编码一致。
func TestQRPUTF16(t *testing.T) {
	manifest := loadQRPManifest(t)
	db, _ := openQRPVariant(t, "qrp-synthetic-utf16.db", manifest.UTF16SHA256)
	if got := db.Tables(); !reflect.DeepEqual(got, manifest.Tables) {
		t.Errorf("身份表 %v，预期 %v", got, manifest.Tables)
	}
	users, err := db.LoadGroups("QGroupInfo_TESTID_A")
	if err != nil {
		t.Fatalf("加载表: %v", err)
	}
	if got := groupsToPairs(users); !reflect.DeepEqual(got, manifest.ExpectedGroups["QGroupInfo_TESTID_A"]) {
		t.Errorf("群列表 %v，预期 %v", got, manifest.ExpectedGroups["QGroupInfo_TESTID_A"])
	}
	ambiguous, err := db.LookupName("QGroupInfo_TESTID_A", "Beta 群")
	if err != nil || len(ambiguous) != 2 {
		t.Errorf("同名歧义应命中 2 条，实际 %+v（错误 %v）", ambiguous, err)
	}
	if _, err := db.LoadGroups("QGroupInfo_TESTID_B"); err == nil {
		t.Error("坏 ID 行在 UTF-16 库中同样应整体失败")
	}
}

// TestOpenRejectsSidecar 验证存在同名边文件时拒绝打开。
func TestOpenRejectsSidecar(t *testing.T) {
	data, err := os.ReadFile("testdata/qrp-synthetic.db")
	if err != nil {
		t.Fatal(err)
	}
	for _, suffix := range []string{"-wal", "-shm", "-journal"} {
		path := filepath.Join(t.TempDir(), "qrp-copy")
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path+suffix, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := Open(path); err == nil {
			t.Errorf("存在 %s 边文件时应被拒绝", suffix)
		}
	}
}

// TestOpenRejectsGarbage 验证非 SQLite 内容按魔数校验明确失败，不寻找密钥。
func TestOpenRejectsGarbage(t *testing.T) {
	path := filepath.Join(t.TempDir(), "not-qrp")
	if err := os.WriteFile(path, []byte(strings.Repeat("garbage!", 64)), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(path); err == nil || !strings.Contains(err.Error(), "魔数") {
		t.Errorf("非 SQLite 内容应被魔数校验拒绝，实际错误: %v", err)
	}
}

// TestOpenRejectsBadLength 验证文件长度不是页长整数倍时被拒绝。
func TestOpenRejectsBadLength(t *testing.T) {
	data, err := os.ReadFile("testdata/qrp-synthetic.db")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "qrp-truncated")
	if err := os.WriteFile(path, data[:4097], 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(path); err == nil || !strings.Contains(err.Error(), "整数倍") {
		t.Errorf("非整页长度应被拒绝，实际错误: %v", err)
	}
}
