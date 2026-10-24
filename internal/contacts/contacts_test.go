// 本文件用合成加密测试库做端到端验证：打开校验、身份表发现、联系人加载与姓名精确匹配。
package contacts

import (
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// fixtureManifest 对应 tools/gen_contacts_fixture.py 输出的期望清单。
type fixtureManifest struct {
	Password            string                `json:"password"`
	PageSize            int                   `json:"page_size"`
	CiphertextSHA256    string                `json:"ciphertext_sha256"`
	UTF16CiphertextSHA256 string              `json:"utf16_ciphertext_sha256"`
	Tables              []string              `json:"tables"`
	ExpectedUsers       map[string][][2]string `json:"expected_users"`
}

// loadManifest 读取并解析生成清单；文件缺失或损坏直接让测试失败。
func loadManifest(t *testing.T) fixtureManifest {
	t.Helper()
	manifestBytes, err := os.ReadFile("testdata/nr-synthetic-manifest.json")
	if err != nil {
		t.Fatalf("读取清单: %v", err)
	}
	var manifest fixtureManifest
	if err := json.Unmarshal(manifestBytes, &manifest); err != nil {
		t.Fatalf("解析清单: %v", err)
	}
	return manifest
}

// openFixtureVariant 打开指定编码变体的合成库并核对密文摘要与页长。
func openFixtureVariant(t *testing.T, filename, wantSHA string) (*DB, fixtureManifest) {
	t.Helper()
	manifest := loadManifest(t)
	ciphertext, err := os.ReadFile("testdata/" + filename)
	if err != nil {
		t.Fatalf("读取测试库: %v", err)
	}
	sum := sha256Sum(ciphertext)
	if got := hex.EncodeToString(sum); got != wantSHA {
		t.Fatalf("测试库 %s 密文摘要 %s 与清单 %s 不符，请用生成脚本重建", filename, got, wantSHA)
	}
	db, err := Open("testdata/"+filename, []byte(manifest.Password))
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

// openFixture 打开默认 UTF-8 合成库。
func openFixture(t *testing.T) (*DB, fixtureManifest) {
	t.Helper()
	manifest := loadManifest(t)
	return openFixtureVariant(t, "nr-synthetic.db", manifest.CiphertextSHA256)
}

// contactsToPairs 把联系人列表转成清单期望的 [名称, ID] 对，便于整体比较。
func contactsToPairs(users []Contact) [][2]string {
	pairs := make([][2]string, len(users))
	for i, user := range users {
		pairs[i] = [2]string{user.Name, user.UserID}
	}
	return pairs
}

// TestFixtureTables 验证身份表发现与生成清单一致。
func TestFixtureTables(t *testing.T) {
	db, manifest := openFixture(t)
	if got := db.Tables(); !reflect.DeepEqual(got, manifest.Tables) {
		t.Errorf("身份表 %v，预期 %v", got, manifest.Tables)
	}
}

// TestFixtureLoadUsers 逐表比对期望联系人：覆盖 Unicode 名称、同名、6KB 溢出行、
// 实体还原、空名跳过与 Type=2 类型过滤。
func TestFixtureLoadUsers(t *testing.T) {
	db, manifest := openFixture(t)
	for _, table := range manifest.Tables {
		if _, ok := manifest.ExpectedUsers[table]; !ok {
			continue
		}
		users, err := db.LoadUsers(table)
		if err != nil {
			t.Fatalf("加载表 %s: %v", table, err)
		}
		want := manifest.ExpectedUsers[table]
		if got := contactsToPairs(users); !reflect.DeepEqual(got, want) {
			t.Errorf("表 %s 联系人 %v，预期 %v", table, got, want)
		}
	}
}

// TestFixtureLookup 覆盖唯一命中、同名歧义与未命中三种查询结果。
func TestFixtureLookup(t *testing.T) {
	db, _ := openFixture(t)
	unique, err := db.LookupName("OrgStruct_TESTID_A", "Alice 张三")
	if err != nil || len(unique) != 1 || unique[0].UserID != "u-alice-001" {
		t.Errorf("唯一命中结果 %+v（错误 %v）不符合预期", unique, err)
	}
	ambiguous, err := db.LookupName("OrgStruct_TESTID_A", "Carol")
	if err != nil || len(ambiguous) != 2 {
		t.Errorf("同名歧义应命中 2 条，实际 %+v（错误 %v）", ambiguous, err)
	}
	entity, err := db.LookupName("OrgStruct_TESTID_A", "A&B")
	if err != nil || len(entity) != 1 || entity[0].UserID != "u-entity-007" {
		t.Errorf("实体还原名称结果 %+v（错误 %v）不符合预期", entity, err)
	}
	none, err := db.LookupName("OrgStruct_TESTID_A", "Zed")
	if err != nil || len(none) != 0 {
		t.Errorf("未命中应返回空，实际 %+v（错误 %v）", none, err)
	}
	isolated, err := db.LookupName("OrgStruct_TESTID_C", "Carol")
	if err != nil || len(isolated) != 0 {
		t.Errorf("其他身份表的联系人不应被查出，实际 %+v（错误 %v）", isolated, err)
	}
}

// TestFixtureUTF16 在与真实客户端库一致的 UTF-16le 编码合成库上重复端到端验证：
// 对应真实库首查被拒的回归场景。
func TestFixtureUTF16(t *testing.T) {
	manifest := loadManifest(t)
	db, _ := openFixtureVariant(t, "nr-synthetic-utf16.db", manifest.UTF16CiphertextSHA256)
	if got := db.Tables(); !reflect.DeepEqual(got, manifest.Tables) {
		t.Errorf("身份表 %v，预期 %v", got, manifest.Tables)
	}
	for _, table := range manifest.Tables {
		if _, ok := manifest.ExpectedUsers[table]; !ok {
			continue
		}
		users, err := db.LoadUsers(table)
		if err != nil {
			t.Fatalf("加载表 %s: %v", table, err)
		}
		if got := contactsToPairs(users); !reflect.DeepEqual(got, manifest.ExpectedUsers[table]) {
			t.Errorf("表 %s 联系人 %v，预期 %v", table, got, manifest.ExpectedUsers[table])
		}
	}
	unique, err := db.LookupName("OrgStruct_TESTID_A", "Alice 张三")
	if err != nil || len(unique) != 1 || unique[0].UserID != "u-alice-001" {
		t.Errorf("唯一命中结果 %+v（错误 %v）不符合预期", unique, err)
	}
	ambiguous, err := db.LookupName("OrgStruct_TESTID_A", "Carol")
	if err != nil || len(ambiguous) != 2 {
		t.Errorf("同名歧义应命中 2 条，实际 %+v（错误 %v）", ambiguous, err)
	}
	if _, err := db.LoadUsers("OrgStruct_TESTID_B"); err == nil {
		t.Error("B 表损坏行在 UTF-16 库中同样应整体失败")
	}
}

// TestLoadUsersCorruptRow 验证 Data 根元素不是 U 时整表加载失败，不跳过坏行。
func TestLoadUsersCorruptRow(t *testing.T) {
	db, _ := openFixture(t)
	if _, err := db.LoadUsers("OrgStruct_TESTID_B"); err == nil {
		t.Fatal("B 表含根元素为 V 的损坏行，应整体失败")
	}
}

// TestOpenRejectsWrongPassword 验证错误口令在文件头魔数校验处失败。
func TestOpenRejectsWrongPassword(t *testing.T) {
	if _, err := Open("testdata/nr-synthetic.db", []byte("wrong-password")); err == nil {
		t.Fatal("错误口令应被拒绝")
	}
}

// TestOpenRejectsEmptyPassword 验证空口令在进入 codec 前被拒绝。
func TestOpenRejectsEmptyPassword(t *testing.T) {
	if _, err := Open("testdata/nr-synthetic.db", nil); err == nil {
		t.Fatal("空口令应被拒绝")
	}
}

// fixturePassword 从清单读取合成口令，测试内不硬编码口令值。
func fixturePassword(t *testing.T) string {
	t.Helper()
	return loadManifest(t).Password
}

// TestOpenRejectsSidecar 验证存在同名边文件时拒绝打开，对应活动库防护。
func TestOpenRejectsSidecar(t *testing.T) {
	ciphertext, err := os.ReadFile("testdata/nr-synthetic.db")
	if err != nil {
		t.Fatal(err)
	}
	password := []byte(fixturePassword(t))
	for _, suffix := range []string{"-wal", "-shm", "-journal"} {
		dir := t.TempDir()
		path := filepath.Join(dir, "nr-copy")
		if err := os.WriteFile(path, ciphertext, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path+suffix, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := Open(path, password); err == nil {
			t.Errorf("存在 %s 边文件时应被拒绝", suffix)
		}
	}
}

// TestOpenRejectsBadLength 验证文件长度不是页长整数倍时被拒绝。
func TestOpenRejectsBadLength(t *testing.T) {
	ciphertext, err := os.ReadFile("testdata/nr-synthetic.db")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "nr-truncated")
	if err := os.WriteFile(path, ciphertext[:4097], 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = Open(path, []byte(fixturePassword(t)))
	if err == nil || !strings.Contains(err.Error(), "整数倍") {
		t.Errorf("非整页长度应被页长校验拒绝，实际错误: %v", err)
	}
}

// TestValidateHeader 用构造的文件头字节覆盖魔数、页长、WAL 头、reserved、
// 负载比例与文本编码的拒绝分支。
func TestValidateHeader(t *testing.T) {
	db, _ := openFixture(t)
	good := db.file.pages[0][:64]
	cases := []struct {
		name   string
		mutate func([]byte)
		errMsg string
	}{
		{"正常头部", nil, ""},
		{"魔数错误", func(b []byte) { b[0] = 'X' }, "魔数"},
		{"页长非法", func(b []byte) { b[16], b[17] = 0x03, 0x00 }, "8 种"},
		{"页长 1 表示 65536", func(b []byte) { b[16], b[17] = 0x00, 0x01 }, "整数倍"},
		{"WAL 写版本", func(b []byte) { b[18] = 2 }, "rollback"},
		{"WAL 读版本", func(b []byte) { b[19] = 2 }, "rollback"},
		{"reserved 非零", func(b []byte) { b[20] = 4 }, "reserved"},
		{"负载比例异常", func(b []byte) { b[21] = 63 }, "负载比例"},
		{"编码值越界", func(b []byte) { b[56], b[57], b[58], b[59] = 0, 0, 0, 9 }, "文本编码"},
	}
	for _, testCase := range cases {
		head := make([]byte, 64)
		copy(head, good)
		if testCase.mutate != nil {
			testCase.mutate(head)
		}
		info, err := validateHeader(head, 17408)
		if testCase.errMsg == "" {
			if err != nil {
				t.Errorf("%s: 不应报错，实际 %v", testCase.name, err)
			} else if info.encoding != 1 || info.pageSize != 1024 {
				t.Errorf("%s: 头信息 %+v 不符预期", testCase.name, info)
			}
			continue
		}
		if err == nil || !strings.Contains(err.Error(), testCase.errMsg) {
			t.Errorf("%s: 预期包含 %q 的错误，实际 %v", testCase.name, testCase.errMsg, err)
		}
	}
	// UTF-16le 编码头必须放行并回传编码值。
	head := make([]byte, 64)
	copy(head, good)
	head[59] = 2
	info, err := validateHeader(head, 17408)
	if err != nil || info.encoding != 2 {
		t.Errorf("UTF-16le 头应放行并回传编码 2，实际 info=%+v err=%v", info, err)
	}
}

// TestReadVarint 覆盖单字节、多字节与 9 字节上限的 varint 解码。
func TestReadVarint(t *testing.T) {
	cases := []struct {
		bytes []byte
		value int64
		size  int
	}{
		{[]byte{0x00}, 0, 1},
		{[]byte{0x7f}, 127, 1},
		{[]byte{0x81, 0x00}, 128, 2},
		{[]byte{0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff}, -1, 9},
		{[]byte{0x80}, 0, 0},
	}
	for _, testCase := range cases {
		value, size := readVarint(testCase.bytes)
		if value != testCase.value || size != testCase.size {
			t.Errorf("readVarint(% x) = (%d, %d)，预期 (%d, %d)",
				testCase.bytes, value, size, testCase.value, testCase.size)
		}
	}
}
