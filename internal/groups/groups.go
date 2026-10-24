// Package groups 从内网通客户端的离线群数据库（qrp）只读解析群名称与群 ID 的对应。
//
// 依据 docs/research/reports/qgroup-id/qgroup-id-report.md 的静态结论：qrp 是
// <数据根目录>\data\qrp 的明文 SQLite（打开路径不装 codec、空密钥）；
// QGroupInfo_<本机身份> 表中 Name 列是群列表显示名，QGroupId 列是 IPC SendGroup
// 的目标 group_id（创建时由 CoCreateGuid 生成 32 个小写十六进制字符，读取保留原值）。
// 群名没有唯一约束；本包多命中返回全部候选，由调用方人工选择，不发送、不写文件。
package groups

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strings"
	"unicode/utf8"

	"neiwangtong-gateway/internal/sqliteread"
)

// Group 是一条原生群记录：Name 为群列表显示名，GroupID 为 QGroupId 列原值。
type Group struct {
	Name    string `json:"name"`
	GroupID string `json:"group_id"`
}

// tableInfo 是 sqlite_master 中一张 QGroupInfo_* 身份表的登记信息。
type tableInfo struct {
	name string
	root int
}

// DB 是已通过文件头与表结构校验的离线 qrp 群库；零值不可用，必须经 Open 创建。
type DB struct {
	file     *sqliteread.Image
	tables   []tableInfo
	checksum string
}

// Open 只读打开 path 指定的完整离线 qrp 副本。qrp 是明文 SQLite，不需要口令；
// 拒绝同名 -wal/-shm/-journal 边文件与任何文件头校验不通过的文件（不是明文
// SQLite 时明确报错，不寻找密钥），未找到结构相符的 QGroupInfo_* 身份表时同样返回错误。
func Open(path string) (*DB, error) {
	for _, suffix := range []string{"-wal", "-shm", "-journal"} {
		if _, err := os.Stat(path + suffix); err == nil {
			return nil, fmt.Errorf("存在边文件 %s：须在客户端正常退出后取得完整副本，不删除边文件", path+suffix)
		} else if !errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("检查边文件 %s: %w", path+suffix, err)
		}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("读取副本: %w", err)
	}
	image, err := sqliteread.NewImage(data)
	if err != nil {
		return nil, err
	}
	db := &DB{file: image}
	tables, err := db.discoverTables()
	if err != nil {
		return nil, err
	}
	db.tables = tables
	db.checksum = hex.EncodeToString(sha256Sum(data))
	return db, nil
}

// Tables 返回库中全部 QGroupInfo_* 身份表名，按名称排序。
// 多张表时须由操作者核实本机原始身份与表名后缀的对应关系，不能挑第一张。
func (db *DB) Tables() []string {
	names := make([]string, len(db.tables))
	for i, table := range db.tables {
		names[i] = table.name
	}
	return names
}

// PageSize 返回从文件头恢复的实际页长。
func (db *DB) PageSize() int {
	return db.file.PageSize()
}

// SHA256 返回源文件的 SHA-256 十六进制摘要，供输出与副本核对。
func (db *DB) SHA256() string {
	return db.checksum
}

// LoadGroups 扫描 table 指定的身份表，按 rowid 升序返回全部群记录。
// 群名为空的行不可能匹配任何查询名，直接跳过；除此之外，任一行 QGroupId 非文本/
// 为空/含 NUL/非 UTF-8、Name 非文本或表名不存在都返回错误，不跳过坏行后继续。
func (db *DB) LoadGroups(table string) ([]Group, error) {
	info, err := db.findTable(table)
	if err != nil {
		return nil, err
	}
	rows, err := db.file.TableRows(info.root)
	if err != nil {
		return nil, fmt.Errorf("读取表 %s: %w", table, err)
	}
	list := make([]Group, 0, len(rows))
	for _, row := range rows {
		values, err := sqliteread.DecodeRecord(row.Payload, db.file.Encoding())
		if err != nil {
			return nil, fmt.Errorf("表 %s rowid %d: %w", table, row.Rowid, err)
		}
		if len(values) < 3 {
			return nil, fmt.Errorf("表 %s rowid %d 列数 %d 不足 3", table, row.Rowid, len(values))
		}
		if values[1].Kind != sqliteread.KindText {
			return nil, fmt.Errorf("表 %s rowid %d 的 QGroupId 不是文本", table, row.Rowid)
		}
		id := values[1].Text
		if id == "" || strings.ContainsRune(id, 0) || !utf8.ValidString(id) {
			return nil, fmt.Errorf("表 %s rowid %d 的 QGroupId 为空、含 NUL 或非有效 UTF-8", table, row.Rowid)
		}
		if values[2].Kind != sqliteread.KindText {
			return nil, fmt.Errorf("表 %s rowid %d 的 Name 不是文本", table, row.Rowid)
		}
		if values[2].Text == "" {
			continue
		}
		list = append(list, Group{Name: values[2].Text, GroupID: id})
	}
	return list, nil
}

// LookupName 在 table 指定的身份表中按群名精确匹配（保留大小写与前后空格），
// 返回全部命中群。零个表示未命中；多于一个表示同名群歧义，由调用方结合群主、
// 创建时间等元数据人工选择，不自动挑第一条。
func (db *DB) LookupName(table, name string) ([]Group, error) {
	list, err := db.LoadGroups(table)
	if err != nil {
		return nil, err
	}
	var matches []Group
	for _, group := range list {
		if group.Name == name {
			matches = append(matches, group)
		}
	}
	return matches, nil
}

func (db *DB) findTable(name string) (tableInfo, error) {
	for _, table := range db.tables {
		if table.name == name {
			return table, nil
		}
	}
	return tableInfo{}, fmt.Errorf("身份表 %s 不在本库中", name)
}

// discoverTables 从 sqlite_master 收集 QGroupInfo_* 表并做轻量 DDL 核对。
func (db *DB) discoverTables() ([]tableInfo, error) {
	rows, err := db.file.TableRows(1)
	if err != nil {
		return nil, fmt.Errorf("读取 sqlite_master: %w", err)
	}
	var tables []tableInfo
	for _, row := range rows {
		values, err := sqliteread.DecodeRecord(row.Payload, db.file.Encoding())
		if err != nil {
			return nil, fmt.Errorf("sqlite_master rowid %d: %w", row.Rowid, err)
		}
		if len(values) < 5 || values[0].Kind != sqliteread.KindText || values[1].Kind != sqliteread.KindText {
			return nil, fmt.Errorf("sqlite_master rowid %d 结构异常", row.Rowid)
		}
		if values[0].Text != "table" || !strings.HasPrefix(values[1].Text, "QGroupInfo_") {
			continue
		}
		if values[3].Kind != sqliteread.KindInt || values[3].Int <= 0 {
			return nil, fmt.Errorf("身份表 %s 的根页号不是正整数", values[1].Text)
		}
		if values[4].Kind != sqliteread.KindText {
			return nil, fmt.Errorf("身份表 %s 缺少建表语句", values[1].Text)
		}
		if err := checkQGroupInfoDDL(values[4].Text); err != nil {
			return nil, fmt.Errorf("身份表 %s 结构不符: %w", values[1].Text, err)
		}
		tables = append(tables, tableInfo{name: values[1].Text, root: int(values[3].Int)})
	}
	if len(tables) == 0 {
		return nil, errors.New("库中没有 QGroupInfo_* 身份表，不是本实例的群数据库")
	}
	return tables, nil
}

// qgroupColumnSequence 是 QGroupInfo 表已知列名序列的前缀（大写），用于按序核对建表语句。
var qgroupColumnSequence = [3]string{"PRIKEY", "QGROUPID", "NAME"}

// checkQGroupInfoDDL 核对建表语句在列定义区按序包含 PriKey、QGroupId、Name。
// 这是轻量核对；完整列集与索引以 qgroup-id 报告恢复的建表结构为准。
func checkQGroupInfoDDL(sql string) error {
	upper := strings.ToUpper(sql)
	open := strings.IndexByte(upper, '(')
	if open < 0 {
		return errors.New("建表语句缺少列定义")
	}
	body := upper[open:]
	position := 0
	for _, column := range qgroupColumnSequence {
		index := strings.Index(body[position:], column)
		if index < 0 {
			return fmt.Errorf("建表语句缺少列 %s", column)
		}
		position += index + len(column)
	}
	return nil
}

func sha256Sum(data []byte) []byte {
	sum := sha256.Sum256(data)
	return sum[:]
}
