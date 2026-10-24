// 本文件提供联系人库的公开入口：副本校验与整库解密、身份表发现、姓名→user_id 精确匹配。
package contacts

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"strings"
	"unicode/utf8"
)

// Contact 是一条 Type=1 的原生用户联系人：Name 为 Data XML 的原始名称，UserID 为 Id 列原值。
type Contact struct {
	Name   string `json:"name"`
	UserID string `json:"user_id"`
}

// tableInfo 是 sqlite_master 中一张 OrgStruct_* 身份表的登记信息。
type tableInfo struct {
	name string
	root int
}

// DB 是已解密并通过文件头与表结构校验的离线 nr 联系人库；零值不可用，必须经 Open 创建。
type DB struct {
	file     dbImage
	tables   []tableInfo
	checksum string
}

// Open 只读打开 path 指定的完整离线 nr 副本，用 password 的精确字节派生密钥并整库解密。
// 拒绝空口令、同名 -wal/-shm/-journal 边文件、WAL 头、非零 reserved、非 UTF-8 文本编码
// 及页长/长度校验不通过的文件；未找到结构相符的 OrgStruct_* 身份表时同样返回错误。
// 口令须为客户端使用的原始窄字符串字节，不做 trim 或编码转换。
func Open(path string, password []byte) (*DB, error) {
	if len(password) == 0 {
		return nil, errors.New("口令不能为空；须传入客户端使用的原始窄字符串字节")
	}
	for _, suffix := range []string{"-wal", "-shm", "-journal"} {
		if _, err := os.Stat(path + suffix); err == nil {
			return nil, fmt.Errorf("存在边文件 %s：须在客户端正常退出后取得完整副本，不删除边文件", path+suffix)
		} else if !errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("检查边文件 %s: %w", path+suffix, err)
		}
	}
	ciphertext, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("读取副本: %w", err)
	}
	if len(ciphertext) < 100 {
		return nil, errors.New("文件小于 SQLite 文件头，不是有效的 nr 副本")
	}
	baseKey := deriveBaseKey(password)
	// CBC 前四块与整页长度无关：先只解密第一页前 64 字节取文件头，确认页长后再整库解密。
	head, err := decryptPage(baseKey, 1, ciphertext[:64])
	if err != nil {
		return nil, err
	}
	info, err := validateHeader(head, len(ciphertext))
	if err != nil {
		return nil, err
	}
	pageCount := len(ciphertext) / info.pageSize
	pages := make([][]byte, pageCount)
	for i := range pages {
		pages[i], err = decryptPage(baseKey, i+1, ciphertext[i*info.pageSize:(i+1)*info.pageSize])
		if err != nil {
			return nil, err
		}
	}
	db := &DB{file: dbImage{pages: pages, pageSize: info.pageSize, encoding: info.encoding}}
	tables, err := db.discoverTables()
	if err != nil {
		return nil, err
	}
	db.tables = tables
	db.checksum = hex.EncodeToString(sha256Sum(ciphertext))
	return db, nil
}

// Tables 返回库中全部 OrgStruct_* 身份表名，按名称排序。
// 多张表时须由操作者核实本机原始身份与表名后缀的对应关系，不能挑第一张。
func (db *DB) Tables() []string {
	names := make([]string, len(db.tables))
	for i, table := range db.tables {
		names[i] = table.name
	}
	return names
}

// PageSize 返回从解密文件头恢复的实际页长。
func (db *DB) PageSize() int {
	return db.file.pageSize
}

// SHA256 返回源密文的 SHA-256 十六进制摘要，供输出与副本核对。
func (db *DB) SHA256() string {
	return db.checksum
}

// LoadUsers 扫描 table 指定的身份表，按 rowid 升序返回全部 Type=1 的原生用户联系人。
// 原始名称为空的行（IPC 误输入在缓存留下的空名对象）不可能匹配任何姓名，直接跳过；
// 除此之外，任一行 Id 非文本/空/含 NUL/非 UTF-8、Data XML 损坏或表名不存在都返回错误，
// 不跳过坏行后继续。
func (db *DB) LoadUsers(table string) ([]Contact, error) {
	info, err := db.findTable(table)
	if err != nil {
		return nil, err
	}
	rows, err := db.file.tableRows(info.root)
	if err != nil {
		return nil, fmt.Errorf("读取表 %s: %w", table, err)
	}
	users := make([]Contact, 0, len(rows))
	for _, row := range rows {
		values, err := decodeRecord(row.payload, db.file.encoding)
		if err != nil {
			return nil, fmt.Errorf("表 %s rowid %d: %w", table, row.rowid, err)
		}
		if len(values) < 6 {
			return nil, fmt.Errorf("表 %s rowid %d 列数 %d 不足 6", table, row.rowid, len(values))
		}
		if values[3].kind != valueInt || values[3].int != 1 {
			continue // 非原生用户条目（部门等），不参与联系人映射
		}
		if values[4].kind != valueText {
			return nil, fmt.Errorf("表 %s rowid %d 的 Id 不是文本", table, row.rowid)
		}
		id := values[4].text
		if id == "" || strings.ContainsRune(id, 0) || !utf8.ValidString(id) {
			return nil, fmt.Errorf("表 %s rowid %d 的 Id 为空、含 NUL 或非有效 UTF-8", table, row.rowid)
		}
		if values[5].kind != valueText {
			return nil, fmt.Errorf("表 %s rowid %d 的 Data 不是文本", table, row.rowid)
		}
		name, err := xmlUserName(values[5].text)
		if err != nil {
			return nil, fmt.Errorf("表 %s rowid %d 的 Data XML: %w", table, row.rowid, err)
		}
		if name == "" {
			continue
		}
		users = append(users, Contact{Name: name, UserID: id})
	}
	return users, nil
}

// LookupName 在 table 指定的身份表中按原始名称精确匹配（保留大小写与前后空格），
// 返回全部命中联系人。零个表示未命中；多于一个表示同名歧义，由调用方人工选择，
// 不自动挑第一条。名称须为 Data 中的原始 Name，不是界面备注。
func (db *DB) LookupName(table, name string) ([]Contact, error) {
	users, err := db.LoadUsers(table)
	if err != nil {
		return nil, err
	}
	var matches []Contact
	for _, user := range users {
		if user.Name == name {
			matches = append(matches, user)
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

// discoverTables 从 sqlite_master 收集 OrgStruct_* 表并做轻量 DDL 核对。
func (db *DB) discoverTables() ([]tableInfo, error) {
	rows, err := db.file.tableRows(1)
	if err != nil {
		return nil, fmt.Errorf("读取 sqlite_master: %w", err)
	}
	var tables []tableInfo
	for _, row := range rows {
		values, err := decodeRecord(row.payload, db.file.encoding)
		if err != nil {
			return nil, fmt.Errorf("sqlite_master rowid %d: %w", row.rowid, err)
		}
		if len(values) < 5 || values[0].kind != valueText || values[1].kind != valueText {
			return nil, fmt.Errorf("sqlite_master rowid %d 结构异常", row.rowid)
		}
		if values[0].text != "table" || !strings.HasPrefix(values[1].text, "OrgStruct_") {
			continue
		}
		if values[3].kind != valueInt || values[3].int <= 0 {
			return nil, fmt.Errorf("身份表 %s 的根页号不是正整数", values[1].text)
		}
		if values[4].kind != valueText {
			return nil, fmt.Errorf("身份表 %s 缺少建表语句", values[1].text)
		}
		if err := checkOrgStructDDL(values[4].text); err != nil {
			return nil, fmt.Errorf("身份表 %s 结构不符: %w", values[1].text, err)
		}
		tables = append(tables, tableInfo{name: values[1].text, root: int(values[3].int)})
	}
	if len(tables) == 0 {
		return nil, errors.New("库中没有 OrgStruct_* 身份表，不是本实例的联系人数据库")
	}
	return tables, nil
}

// orgColumnSequence 是 OrgStruct 表的已知列名序列（大写），用于按序核对建表语句。
var orgColumnSequence = [6]string{"PRIKEY", "UPDATETIME", "ONLINETIME", "TYPE", "ID", "DATA"}

// checkOrgStructDDL 核对建表语句在列定义区包含按序的六个已知列名及 UNIQUE(Id) 唯一约束。
// 这是对已恢复 schema 的轻量核对；列级类型与索引细节以研究报告的完整审计为准。
func checkOrgStructDDL(sql string) error {
	upper := strings.ToUpper(sql)
	open := strings.IndexByte(upper, '(')
	if open < 0 {
		return errors.New("建表语句缺少列定义")
	}
	body := upper[open:]
	position := 0
	for _, column := range orgColumnSequence {
		index := strings.Index(body[position:], column)
		if index < 0 {
			return fmt.Errorf("建表语句缺少列 %s", column)
		}
		position += index + len(column)
	}
	if !strings.Contains(body, "UNIQUE(ID)") {
		return errors.New("建表语句缺少 UNIQUE(Id) 唯一约束")
	}
	return nil
}

// headerInfo 是从解密文件头恢复的库参数。
type headerInfo struct {
	pageSize int
	encoding int // SQLite 文本编码：1=UTF-8，2=UTF-16le，3=UTF-16be
}

// validateHeader 校验解密后的第一页文件头（前 64 字节）并返回页长与文本编码。
// 只接受已验证的 8 种页长、rollback 读写版本、reserved=0、标准负载比例与
// SQLite 定义的三种文本编码；真实客户端库实测为 UTF-16le。
func validateHeader(head []byte, fileSize int) (headerInfo, error) {
	if len(head) < 64 {
		return headerInfo{}, errors.New("文件头不足 64 字节")
	}
	if string(head[:16]) != "SQLite format 3\x00" {
		return headerInfo{}, errors.New("解密后未出现 SQLite 魔数：口令错误或文件不是 nr 数据库")
	}
	pageSize := int(head[16])<<8 | int(head[17])
	if pageSize == 1 {
		pageSize = 65536
	}
	switch pageSize {
	case 512, 1024, 2048, 4096, 8192, 16384, 32768, 65536:
	default:
		return headerInfo{}, fmt.Errorf("页长 %d 不在已验证的 8 种取值内", pageSize)
	}
	if head[18] != 1 || head[19] != 1 {
		return headerInfo{}, errors.New("不是 rollback 日志模式的数据库头；WAL 模式副本不支持")
	}
	if head[20] != 0 {
		return headerInfo{}, errors.New("reserved 字节非零，超出已验证的 codec 契约")
	}
	if head[21] != 64 || head[22] != 32 || head[23] != 32 {
		return headerInfo{}, errors.New("负载比例字节与标准 SQLite 值不符")
	}
	encoding := int(binary.BigEndian.Uint32(head[56:60]))
	if encoding < 1 || encoding > 3 {
		return headerInfo{}, fmt.Errorf("文本编码值 %d 不是 SQLite 定义的 1/2/3", encoding)
	}
	if fileSize < pageSize || fileSize%pageSize != 0 {
		return headerInfo{}, errors.New("文件长度不是页长的非零整数倍")
	}
	return headerInfo{pageSize: pageSize, encoding: encoding}, nil
}

// xmlUserName 提取 U 根下唯一直接 Name 子元素的完整文本，标准实体由解码器还原。
// 根不是 U、Name 缺失或重复、Name 含嵌套元素或 XML 非法都返回错误。
func xmlUserName(data string) (string, error) {
	decoder := xml.NewDecoder(strings.NewReader(data))
	depth := 0
	seenRoot := false
	rootClosed := false
	inName := false
	nameCount := 0
	var name strings.Builder
	for {
		token, err := decoder.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return "", err
		}
		switch element := token.(type) {
		case xml.StartElement:
			if rootClosed {
				return "", errors.New("根元素闭合后还有内容")
			}
			depth++
			switch {
			case depth == 1:
				if element.Name.Local != "U" {
					return "", fmt.Errorf("根元素是 %s 不是 U", element.Name.Local)
				}
				seenRoot = true
			case depth == 2 && element.Name.Local == "Name":
				nameCount++
				inName = true
			case depth >= 3 && inName:
				return "", errors.New("Name 含嵌套元素")
			}
		case xml.CharData:
			if inName {
				name.Write(element)
			}
		case xml.EndElement:
			if inName && depth == 2 && element.Name.Local == "Name" {
				inName = false
			}
			depth--
			if depth == 0 {
				rootClosed = true
			}
		}
	}
	if !seenRoot {
		return "", errors.New("缺少根元素")
	}
	if !rootClosed {
		return "", errors.New("根元素未闭合")
	}
	if nameCount == 0 {
		return "", errors.New("U 根下缺少直接 Name 子元素")
	}
	if nameCount > 1 {
		return "", errors.New("U 根下有多个直接 Name 子元素")
	}
	return name.String(), nil
}

func sha256Sum(data []byte) []byte {
	sum := sha256.Sum256(data)
	return sum[:]
}
