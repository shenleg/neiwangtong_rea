// 本文件实现整库解密后 SQLite 映像的最小只读解析：表 b-tree 遍历、溢出负载拼接与记录列解码。
// 只支持联系人提取需要的表页结构，遇到越界或非法结构立即报错，不修复、不跳过。
package contacts

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

// errCorruptPage 表示页面或记录结构越界、类型非法；调用方不应继续解释该库。
var errCorruptPage = errors.New("页面结构损坏")

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

// dbImage 是整库解密后的页映像；pages 按页号从 1 开始索引，encoding 为库文本编码
//（1=UTF-8，2=UTF-16le，3=UTF-16be）。
type dbImage struct {
	pages    [][]byte
	pageSize int
	encoding int
}

func (db *dbImage) page(num int) ([]byte, error) {
	if num < 1 || num > len(db.pages) {
		return nil, fmt.Errorf("%w：页号 %d 超出 1..%d", errCorruptPage, num, len(db.pages))
	}
	return db.pages[num-1], nil
}

// tableRow 是表 b-tree 的一条完整记录，payload 已含溢出页拼接结果。
type tableRow struct {
	rowid   int64
	payload []byte
}

// tableRows 遍历以 root 为根的表 b-tree，按 rowid 升序返回全部记录。
func (db *dbImage) tableRows(root int) ([]tableRow, error) {
	var rows []tableRow
	if err := db.walk(root, &rows); err != nil {
		return nil, err
	}
	return rows, nil
}

func (db *dbImage) walk(num int, rows *[]tableRow) error {
	data, err := db.page(num)
	if err != nil {
		return err
	}
	header := 0
	if num == 1 {
		header = 100 // 第一页开头是 100 字节 SQLite 文件头
	}
	if header+8 > len(data) {
		return fmt.Errorf("%w：页 %d 头部不完整", errCorruptPage, num)
	}
	pageType := data[header]
	cellCount := int(binary.BigEndian.Uint16(data[header+3 : header+5]))
	switch pageType {
	case btreeInteriorTable:
		pointerArray := header + 12
		if pointerArray+2*cellCount > len(data) {
			return fmt.Errorf("%w：页 %d 单元格指针数组越界", errCorruptPage, num)
		}
		for i := 0; i < cellCount; i++ {
			offset := int(binary.BigEndian.Uint16(data[pointerArray+2*i : pointerArray+2*i+2]))
			if offset < 0 || offset+4 > len(data) {
				return fmt.Errorf("%w：页 %d 单元格偏移越界", errCorruptPage, num)
			}
			if err := db.walk(int(binary.BigEndian.Uint32(data[offset:offset+4])), rows); err != nil {
				return err
			}
		}
		return db.walk(int(binary.BigEndian.Uint32(data[header+8:header+12])), rows)
	case btreeLeafTable:
		pointerArray := header + 8
		if pointerArray+2*cellCount > len(data) {
			return fmt.Errorf("%w：页 %d 单元格指针数组越界", errCorruptPage, num)
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
		return fmt.Errorf("%w：页 %d 类型 0x%02x 不是表页", errCorruptPage, num, pageType)
	}
}

// leafCell 解码表叶子单元格并拼接溢出链；reserved 已在打开时校验为 0，可用字节数等于页长。
func (db *dbImage) leafCell(pageData []byte, offset int) (tableRow, error) {
	var row tableRow
	if offset < 0 || offset >= len(pageData) {
		return row, fmt.Errorf("%w：单元格偏移越界", errCorruptPage)
	}
	payloadLength, sizeLen := readVarint(pageData[offset:])
	if sizeLen == 0 {
		return row, fmt.Errorf("%w：负载长度 varint 不完整", errCorruptPage)
	}
	rowid, rowidLen := readVarint(pageData[offset+sizeLen:])
	if rowidLen == 0 {
		return row, fmt.Errorf("%w：rowid varint 不完整", errCorruptPage)
	}
	// 负载不可能超过整库大小；先挡住会在 32 位平台截断的损坏值。
	if payloadLength < 0 || payloadLength > int64(len(db.pages))*int64(db.pageSize) {
		return row, fmt.Errorf("%w：负载长度 %d 超出整库大小", errCorruptPage, payloadLength)
	}
	row.rowid = rowid
	payload, err := db.readPayload(pageData, offset+sizeLen+rowidLen, int(payloadLength))
	if err != nil {
		return row, err
	}
	row.payload = payload
	return row, nil
}

// readPayload 按 SQLite 本地/溢出规则取完整负载：小负载整体在本页，
// 大负载按 minLocal/最大本地规则截断后接 4 字节页号的溢出页链。
func (db *dbImage) readPayload(pageData []byte, offset, length int) ([]byte, error) {
	usable := db.pageSize
	maxLocal := usable - 35
	if length <= maxLocal {
		if offset < 0 || offset+length > len(pageData) {
			return nil, fmt.Errorf("%w：本地负载越界", errCorruptPage)
		}
		return pageData[offset : offset+length], nil
	}
	minLocal := ((usable-12)*32)/255 - 23
	local := minLocal
	if k := minLocal + (length-minLocal)%(usable-4); k <= maxLocal {
		local = k
	}
	if offset+local+4 > len(pageData) {
		return nil, fmt.Errorf("%w：溢出单元格头部越界", errCorruptPage)
	}
	payload := make([]byte, 0, length)
	payload = append(payload, pageData[offset:offset+local]...)
	next := int(binary.BigEndian.Uint32(pageData[offset+local : offset+local+4]))
	remaining := length - local
	for remaining > 0 {
		if next == 0 {
			return nil, fmt.Errorf("%w：溢出链在剩余 %d 字节前中断", errCorruptPage, remaining)
		}
		overflow, err := db.page(next)
		if err != nil {
			return nil, err
		}
		next = int(binary.BigEndian.Uint32(overflow[:4]))
		chunk := min(remaining, usable-4)
		if 4+chunk > len(overflow) {
			return nil, fmt.Errorf("%w：溢出页内容越界", errCorruptPage)
		}
		payload = append(payload, overflow[4:4+chunk]...)
		remaining -= chunk
	}
	return payload, nil
}

type valueKind uint8

const (
	valueNull valueKind = iota
	valueInt
	valueText
	valueOther // float 与 blob：OrgStruct 列契约之外的类型
)

// columnValue 是一列解码值，只物化联系人表用到的 NULL/整数/文本三类语义。
type columnValue struct {
	kind valueKind
	int  int64
	text string
}

// decodeRecord 解码记录：先读变长序列类型头，再按类型与库文本编码取列值。
func decodeRecord(payload []byte, encoding int) ([]columnValue, error) {
	headerLength, n := readVarint(payload)
	if n == 0 || headerLength < int64(n) || int(headerLength) > len(payload) {
		return nil, fmt.Errorf("%w：记录头长度非法", errCorruptPage)
	}
	var serialTypes []int64
	for offset := n; offset < int(headerLength); {
		code, size := readVarint(payload[offset:])
		if size == 0 || offset+size > int(headerLength) {
			return nil, fmt.Errorf("%w：记录头 varint 越界", errCorruptPage)
		}
		serialTypes = append(serialTypes, code)
		offset += size
	}
	values := make([]columnValue, len(serialTypes))
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
func decodeValue(payload []byte, offset int, code int64, encoding int) (columnValue, int, error) {
	var value columnValue
	switch {
	case code == 0:
		value.kind = valueNull
		return value, 0, nil
	case code >= 1 && code <= 6:
		sizes := map[int64]int{1: 1, 2: 2, 3: 3, 4: 4, 5: 6, 6: 8}
		size := sizes[code]
		if offset+size > len(payload) {
			return value, 0, fmt.Errorf("%w：整数值越界", errCorruptPage)
		}
		value.kind = valueInt
		value.int = bigEndianInt(payload[offset : offset+size])
		return value, size, nil
	case code == 8:
		value.kind = valueInt
		return value, 0, nil
	case code == 9:
		value.kind = valueInt
		value.int = 1
		return value, 0, nil
	case code == 7 || (code >= 12 && code%2 == 0):
		size := 8
		if code >= 12 {
			size = int((code - 12) / 2)
		}
		if offset+size > len(payload) {
			return value, 0, fmt.Errorf("%w：float/blob 值越界", errCorruptPage)
		}
		value.kind = valueOther
		return value, size, nil
	case code >= 13:
		size := int((code - 13) / 2)
		if offset < 0 || offset+size > len(payload) {
			return value, 0, fmt.Errorf("%w：文本值越界", errCorruptPage)
		}
		text, err := decodeDBText(payload[offset:offset+size], encoding)
		if err != nil {
			return value, 0, err
		}
		value.kind = valueText
		value.text = text
		return value, size, nil
	default:
		return value, 0, fmt.Errorf("%w：serial type %d 保留或非法", errCorruptPage, code)
	}
}

// decodeDBText 按库文本编码把列字节转成 Go 字符串。真实客户端库为 UTF-16le，
// 孤立代理项视为损坏而不是静默替换，避免改写 Id 与名称。
func decodeDBText(raw []byte, encoding int) (string, error) {
	switch encoding {
	case 1:
		return string(raw), nil
	case 2, 3:
		if len(raw)%2 != 0 {
			return "", fmt.Errorf("%w：UTF-16 文本字节数为奇数", errCorruptPage)
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
					return "", fmt.Errorf("%w：UTF-16 孤立高位代理项", errCorruptPage)
				}
				i++
			case units[i] >= 0xdc00 && units[i] <= 0xdfff:
				return "", fmt.Errorf("%w：UTF-16 孤立低位代理项", errCorruptPage)
			}
		}
		return string(utf16.Decode(units)), nil
	default:
		return "", fmt.Errorf("%w：不支持的文本编码 %d", errCorruptPage, encoding)
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
