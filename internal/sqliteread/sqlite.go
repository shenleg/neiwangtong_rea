// Package sqliteread 提供内网通本地 SQLite 库（明文或已解密映像）的最小只读解析：
// 文件头校验、表 b-tree 遍历、溢出负载拼接与记录列解码。遇到越界或非法结构立即报错，
// 不修复、不跳过。解密是调用方的职责；本包只处理明文字节。
package sqliteread

import (
	"encoding/binary"
	"errors"
	"fmt"
	"unicode/utf16"
)

const (
	btreeInteriorTable = 0x05
	btreeLeafTable     = 0x0d
)

// errCorrupt 表示页面或记录结构越界、类型非法；调用方不应继续解释该库。
var errCorrupt = errors.New("页面结构损坏")

// readVarint 解码 SQLite 变长整数：前 8 字节各贡献低 7 位，第 9 字节贡献全部 8 位。
// 返回值与占用字节数；输入不完整时字节数为 0。
func readVarint(b []byte) (int64, int) {
	var value uint64
	for i := 0; i < 8; i++ {
		if i >= len(b) {
			return 0, 0
		}
		value = value<<7 | uint64(b[i]&0x7f)
		if b[i]&0x80 == 0 {
			return int64(value), i + 1
		}
	}
	if len(b) < 9 {
		return 0, 0
	}
	return int64(value<<8 | uint64(b[8])), 9
}

// Header 是从文件头恢复的库参数。
type Header struct {
	PageSize int
	Encoding int // SQLite 文本编码：1=UTF-8，2=UTF-16le，3=UTF-16be
}

// ParseHeader 校验文件头（前 64 字节）并返回页长与文本编码。
// 只接受标准 SQLite 魔数、512..65536 的 8 种二次幂页长、rollback 读写版本、
// reserved=0、标准负载比例与 SQLite 定义的三种文本编码。
func ParseHeader(head []byte, fileSize int) (Header, error) {
	if len(head) < 64 {
		return Header{}, errors.New("文件头不足 64 字节")
	}
	if string(head[:16]) != "SQLite format 3\x00" {
		return Header{}, errors.New("未出现 SQLite 魔数：不是明文 SQLite 文件或口令错误")
	}
	pageSize := int(head[16])<<8 | int(head[17])
	if pageSize == 1 {
		pageSize = 65536
	}
	switch pageSize {
	case 512, 1024, 2048, 4096, 8192, 16384, 32768, 65536:
	default:
		return Header{}, fmt.Errorf("页长 %d 不在已验证的 8 种取值内", pageSize)
	}
	if head[18] != 1 || head[19] != 1 {
		return Header{}, errors.New("不是 rollback 日志模式的数据库头；WAL 模式副本不支持")
	}
	if head[20] != 0 {
		return Header{}, errors.New("reserved 字节非零，超出已验证的读取契约")
	}
	if head[21] != 64 || head[22] != 32 || head[23] != 32 {
		return Header{}, errors.New("负载比例字节与标准 SQLite 值不符")
	}
	encoding := int(binary.BigEndian.Uint32(head[56:60]))
	if encoding < 1 || encoding > 3 {
		return Header{}, fmt.Errorf("文本编码值 %d 不是 SQLite 定义的 1/2/3", encoding)
	}
	if fileSize < pageSize || fileSize%pageSize != 0 {
		return Header{}, errors.New("文件长度不是页长的非零整数倍")
	}
	return Header{PageSize: pageSize, Encoding: encoding}, nil
}

// Image 是整库的明文页映像；pages 按页号从 1 开始索引。
type Image struct {
	pages    [][]byte
	pageSize int
	encoding int
}

// NewImage 校验整库数据并按页长切分；data 必须是完整明文库（明文文件或已整体解密）。
func NewImage(data []byte) (*Image, error) {
	if len(data) < 64 {
		return nil, errors.New("文件小于 SQLite 文件头")
	}
	header, err := ParseHeader(data[:64], len(data))
	if err != nil {
		return nil, err
	}
	pageCount := len(data) / header.PageSize
	pages := make([][]byte, pageCount)
	for i := range pages {
		pages[i] = data[i*header.PageSize : (i+1)*header.PageSize]
	}
	return &Image{pages: pages, pageSize: header.PageSize, encoding: header.Encoding}, nil
}

// PageSize 返回从文件头恢复的实际页长。
func (db *Image) PageSize() int { return db.pageSize }

// Encoding 返回库文本编码：1=UTF-8，2=UTF-16le，3=UTF-16be。
func (db *Image) Encoding() int { return db.encoding }

func (db *Image) page(num int) ([]byte, error) {
	if num < 1 || num > len(db.pages) {
		return nil, fmt.Errorf("%w：页号 %d 超出 1..%d", errCorrupt, num, len(db.pages))
	}
	return db.pages[num-1], nil
}

// Row 是表 b-tree 的一条完整记录，Payload 已含溢出页拼接结果。
type Row struct {
	Rowid   int64
	Payload []byte
}

// TableRows 遍历以 root 为根的表 b-tree，按 rowid 升序返回全部记录。
func (db *Image) TableRows(root int) ([]Row, error) {
	var rows []Row
	if err := db.walk(root, &rows); err != nil {
		return nil, err
	}
	return rows, nil
}

func (db *Image) walk(num int, rows *[]Row) error {
	data, err := db.page(num)
	if err != nil {
		return err
	}
	header := 0
	if num == 1 {
		header = 100 // 第一页开头是 100 字节 SQLite 文件头
	}
	if header+8 > len(data) {
		return fmt.Errorf("%w：页 %d 头部不完整", errCorrupt, num)
	}
	pageType := data[header]
	cellCount := int(binary.BigEndian.Uint16(data[header+3 : header+5]))
	switch pageType {
	case btreeInteriorTable:
		pointerArray := header + 12
		if pointerArray+2*cellCount > len(data) {
			return fmt.Errorf("%w：页 %d 单元格指针数组越界", errCorrupt, num)
		}
		for i := 0; i < cellCount; i++ {
			offset := int(binary.BigEndian.Uint16(data[pointerArray+2*i : pointerArray+2*i+2]))
			if offset < 0 || offset+4 > len(data) {
				return fmt.Errorf("%w：页 %d 单元格偏移越界", errCorrupt, num)
			}
			if err := db.walk(int(binary.BigEndian.Uint32(data[offset:offset+4])), rows); err != nil {
				return err
			}
		}
		return db.walk(int(binary.BigEndian.Uint32(data[header+8:header+12])), rows)
	case btreeLeafTable:
		pointerArray := header + 8
		if pointerArray+2*cellCount > len(data) {
			return fmt.Errorf("%w：页 %d 单元格指针数组越界", errCorrupt, num)
		}
		for i := 0; i < cellCount; i++ {
			offset := int(binary.BigEndian.Uint16(data[pointerArray+2*i : pointerArray+2*i+2]))
			row, err := db.leafCell(data, offset)
			if err != nil {
				return fmt.Errorf("页 %d 第 %d 个单元格: %w", num, i, err)
			}
			*rows = append(*rows, row)
		}
		return nil
	default:
		return fmt.Errorf("%w：页 %d 类型 0x%02x 不是表页", errCorrupt, num, pageType)
	}
}

// leafCell 解码表叶子单元格并拼接溢出链；reserved 已在头校验中限定为 0，可用字节数等于页长。
func (db *Image) leafCell(pageData []byte, offset int) (Row, error) {
	var row Row
	if offset < 0 || offset >= len(pageData) {
		return row, fmt.Errorf("%w：单元格偏移越界", errCorrupt)
	}
	payloadLength, sizeLen := readVarint(pageData[offset:])
	if sizeLen == 0 {
		return row, fmt.Errorf("%w：负载长度 varint 不完整", errCorrupt)
	}
	rowid, rowidLen := readVarint(pageData[offset+sizeLen:])
	if rowidLen == 0 {
		return row, fmt.Errorf("%w：rowid varint 不完整", errCorrupt)
	}
	// 负载不可能超过整库大小；先挡住会在 32 位平台截断的损坏值。
	if payloadLength < 0 || payloadLength > int64(len(db.pages))*int64(db.pageSize) {
		return row, fmt.Errorf("%w：负载长度 %d 超出整库大小", errCorrupt, payloadLength)
	}
	row.Rowid = rowid
	payload, err := db.readPayload(pageData, offset+sizeLen+rowidLen, int(payloadLength))
	if err != nil {
		return row, err
	}
	row.Payload = payload
	return row, nil
}

// readPayload 按 SQLite 本地/溢出规则取完整负载：小负载整体在本页，
// 大负载按 minLocal/最大本地规则截断后接 4 字节页号的溢出页链。
func (db *Image) readPayload(pageData []byte, offset, length int) ([]byte, error) {
	usable := db.pageSize
	maxLocal := usable - 35
	if length <= maxLocal {
		if offset < 0 || offset+length > len(pageData) {
			return nil, fmt.Errorf("%w：本地负载越界", errCorrupt)
		}
		return pageData[offset : offset+length], nil
	}
	minLocal := ((usable-12)*32)/255 - 23
	local := minLocal
	if k := minLocal + (length-minLocal)%(usable-4); k <= maxLocal {
		local = k
	}
	if offset+local+4 > len(pageData) {
		return nil, fmt.Errorf("%w：溢出单元格头部越界", errCorrupt)
	}
	payload := make([]byte, 0, length)
	payload = append(payload, pageData[offset:offset+local]...)
	next := int(binary.BigEndian.Uint32(pageData[offset+local : offset+local+4]))
	remaining := length - local
	for remaining > 0 {
		if next == 0 {
			return nil, fmt.Errorf("%w：溢出链在剩余 %d 字节前中断", errCorrupt, remaining)
		}
		overflow, err := db.page(next)
		if err != nil {
			return nil, err
		}
		next = int(binary.BigEndian.Uint32(overflow[:4]))
		chunk := min(remaining, usable-4)
		if 4+chunk > len(overflow) {
			return nil, fmt.Errorf("%w：溢出页内容越界", errCorrupt)
		}
		payload = append(payload, overflow[4:4+chunk]...)
		remaining -= chunk
	}
	return payload, nil
}

// ValueKind 标记列值的语义类别；联系人/群表只出现 NULL、整数与文本。
type ValueKind uint8

const (
	KindNull  ValueKind = iota
	KindInt
	KindText
	KindOther // float 与 blob：目标表列契约之外的类型
)

// Value 是一列解码值。
type Value struct {
	Kind ValueKind
	Int  int64
	Text string
}

// DecodeRecord 按库文本编码解码记录：先读变长序列类型头，再逐列取值。
func DecodeRecord(payload []byte, encoding int) ([]Value, error) {
	headerLength, n := readVarint(payload)
	if n == 0 || headerLength < int64(n) || int(headerLength) > len(payload) {
		return nil, fmt.Errorf("%w：记录头长度非法", errCorrupt)
	}
	var serialTypes []int64
	for offset := n; offset < int(headerLength); {
		code, size := readVarint(payload[offset:])
		if size == 0 || offset+size > int(headerLength) {
			return nil, fmt.Errorf("%w：记录头 varint 越界", errCorrupt)
		}
		serialTypes = append(serialTypes, code)
		offset += size
	}
	values := make([]Value, len(serialTypes))
	body := int(headerLength)
	for i, code := range serialTypes {
		value, size, err := decodeValue(payload, body, code, encoding)
		if err != nil {
			return nil, err
		}
		values[i] = value
		body += size
	}
	return values, nil
}

// decodeValue 按 serial type 取一个列值，返回值、占用字节数。
func decodeValue(payload []byte, offset int, code int64, encoding int) (Value, int, error) {
	var value Value
	switch {
	case code == 0:
		value.Kind = KindNull
		return value, 0, nil
	case code >= 1 && code <= 6:
		sizes := map[int64]int{1: 1, 2: 2, 3: 3, 4: 4, 5: 6, 6: 8}
		size := sizes[code]
		if offset+size > len(payload) {
			return value, 0, fmt.Errorf("%w：整数值越界", errCorrupt)
		}
		value.Kind = KindInt
		value.Int = bigEndianInt(payload[offset : offset+size])
		return value, size, nil
	case code == 8:
		value.Kind = KindInt
		return value, 0, nil
	case code == 9:
		value.Kind = KindInt
		value.Int = 1
		return value, 0, nil
	case code == 7 || (code >= 12 && code%2 == 0):
		size := 8
		if code >= 12 {
			size = int((code - 12) / 2)
		}
		if offset+size > len(payload) {
			return value, 0, fmt.Errorf("%w：float/blob 值越界", errCorrupt)
		}
		value.Kind = KindOther
		return value, size, nil
	case code >= 13:
		size := int((code - 13) / 2)
		if offset < 0 || offset+size > len(payload) {
			return value, 0, fmt.Errorf("%w：文本值越界", errCorrupt)
		}
		text, err := decodeDBText(payload[offset:offset+size], encoding)
		if err != nil {
			return value, 0, err
		}
		value.Kind = KindText
		value.Text = text
		return value, size, nil
	default:
		return value, 0, fmt.Errorf("%w：serial type %d 保留或非法", errCorrupt, code)
	}
}

// decodeDBText 按库文本编码把列字节转成 Go 字符串。真实客户端库为 UTF-16le，
// 孤立代理项视为损坏而不是静默替换，避免改写 ID 与名称。
func decodeDBText(raw []byte, encoding int) (string, error) {
	switch encoding {
	case 1:
		return string(raw), nil
	case 2, 3:
		if len(raw)%2 != 0 {
			return "", fmt.Errorf("%w：UTF-16 文本字节数为奇数", errCorrupt)
		}
		units := make([]uint16, len(raw)/2)
		for i := range units {
			if encoding == 2 {
				units[i] = binary.LittleEndian.Uint16(raw[2*i:])
			} else {
				units[i] = binary.BigEndian.Uint16(raw[2*i:])
			}
		}
		for i := 0; i < len(units); i++ {
			switch {
			case units[i] >= 0xd800 && units[i] <= 0xdbff:
				if i+1 >= len(units) || units[i+1] < 0xdc00 || units[i+1] > 0xdfff {
					return "", fmt.Errorf("%w：UTF-16 孤立高位代理项", errCorrupt)
				}
				i++
			case units[i] >= 0xdc00 && units[i] <= 0xdfff:
				return "", fmt.Errorf("%w：UTF-16 孤立低位代理项", errCorrupt)
			}
		}
		return string(utf16.Decode(units)), nil
	default:
		return "", fmt.Errorf("%w：不支持的文本编码 %d", errCorrupt, encoding)
	}
}

// bigEndianInt 读取大端有符号整数并做符号扩展。
func bigEndianInt(b []byte) int64 {
	var value int64
	if len(b) > 0 && b[0]&0x80 != 0 {
		value = -1
	}
	for _, one := range b {
		value = value<<8 | int64(one)
	}
	return value
}
