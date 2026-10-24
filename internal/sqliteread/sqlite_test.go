// 本文件覆盖 varint 解码与文件头校验的接受/拒绝分支；b-tree 与记录解码由 contacts/groups 的夹具端到端覆盖。
package sqliteread

import (
	"strings"
	"testing"
)

// goodHeader 构造页长 1024、rollback、reserved=0、UTF-8 编码的标准文件头。
func goodHeader() []byte {
	head := make([]byte, 64)
	copy(head, "SQLite format 3\x00")
	head[16], head[17] = 0x04, 0x00 // 页长 1024
	head[18], head[19] = 1, 1
	head[21], head[22], head[23] = 64, 32, 32
	head[59] = 1 // 文本编码 UTF-8（大端 32 位）
	return head
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

// TestParseHeader 用构造的文件头字节覆盖魔数、页长、WAL 头、reserved、
// 负载比例、编码与文件长度的接受和拒绝分支。
func TestParseHeader(t *testing.T) {
	cases := []struct {
		name   string
		mutate func([]byte)
		errMsg string
	}{
		{"正常头部", nil, ""},
		{"魔数错误", func(b []byte) { b[0] = 'X' }, "魔数"},
		{"页长非法", func(b []byte) { b[16], b[17] = 0x03, 0x00 }, "8 种"},
		{"页长 1 表示 65536 但文件过短", func(b []byte) { b[16], b[17] = 0x00, 0x01 }, "整数倍"},
		{"WAL 写版本", func(b []byte) { b[18] = 2 }, "rollback"},
		{"WAL 读版本", func(b []byte) { b[19] = 2 }, "rollback"},
		{"reserved 非零", func(b []byte) { b[20] = 4 }, "reserved"},
		{"负载比例异常", func(b []byte) { b[21] = 63 }, "负载比例"},
		{"编码值越界", func(b []byte) { b[59] = 9 }, "文本编码"},
	}
	for _, testCase := range cases {
		head := goodHeader()
		if testCase.mutate != nil {
			testCase.mutate(head)
		}
		header, err := ParseHeader(head, 17408)
		if testCase.errMsg == "" {
			if err != nil {
				t.Errorf("%s: 不应报错，实际 %v", testCase.name, err)
			} else if header.Encoding != 1 || header.PageSize != 1024 {
				t.Errorf("%s: 头信息 %+v 不符预期", testCase.name, header)
			}
			continue
		}
		if err == nil || !strings.Contains(err.Error(), testCase.errMsg) {
			t.Errorf("%s: 预期包含 %q 的错误，实际 %v", testCase.name, testCase.errMsg, err)
		}
	}
	// UTF-16le 编码头必须放行并回传编码值。
	head := goodHeader()
	head[59] = 2
	header, err := ParseHeader(head, 17408)
	if err != nil || header.Encoding != 2 {
		t.Errorf("UTF-16le 头应放行并回传编码 2，实际 header=%+v err=%v", header, err)
	}
	// 文件长度不是页长整数倍必须拒绝。
	if _, err := ParseHeader(goodHeader(), 4097); err == nil || !strings.Contains(err.Error(), "整数倍") {
		t.Errorf("非整页长度应被页长校验拒绝，实际错误: %v", err)
	}
}
