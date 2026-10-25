// 本文件用合成 32 位 PE 夹具与临时目录验证零参数启动的自动识别：
// 口令提取的地址映射与边界、exe 探测的唯一性判定、数据路径推导。
package agent

import (
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fixturePassword 是合成口令；长度与真实样本一致（8 字节），内容无真实含义。
var fixturePassword = []byte("s3cr3t!!")

// writePEFixture 生成包含一个 .text 节的最小 32 位 PE：ImageBase 与节起始 RVA
// 可调，口令窄字符串固定写在样本地址 codecPasswordVA 对应的节内偏移处。
// password 为 nil 时写入 64+ 字节非 NUL 数据（触发超限错误）。
func writePEFixture(t *testing.T, imageBase, sectionVA uint32, password []byte) string {
	t.Helper()
	const rawOffset = 0x200
	sectionData := make([]byte, 0x1000)
	rva := uint32(codecPasswordVA) - imageBase
	// 节外地址变体（rva-sectionVA 越界）只测映射失败，不写数据。
	if off := int(rva - sectionVA); off >= 0 && off+passwordMaxBytes+1 <= len(sectionData) {
		switch {
		case password == nil:
			for i := 0; i < passwordMaxBytes+1; i++ {
				sectionData[off+i] = 'A'
			}
		case len(password) == 0:
			// 只留 NUL，即地址处为空字符串。
		default:
			copy(sectionData[off:], password)
		}
	}
	var buf bytes.Buffer
	dos := make([]byte, 0x80)
	dos[0], dos[1] = 'M', 'Z'
	binary.LittleEndian.PutUint32(dos[0x3c:], 0x80)
	buf.Write(dos)
	buf.WriteString("PE\x00\x00")
	// COFF 头：I386、1 个节、224 字节 OptionalHeader32。
	coff := make([]byte, 20)
	binary.LittleEndian.PutUint16(coff[0:], 0x14c)
	binary.LittleEndian.PutUint16(coff[2:], 1)
	binary.LittleEndian.PutUint16(coff[16:], 224)
	binary.LittleEndian.PutUint16(coff[18:], 0x102)
	buf.Write(coff)
	// OptionalHeader32：Magic 0x10b，ImageBase 在偏移 28，NumberOfRvaAndSizes
	// 在偏移 92——必须为 16，否则 debug/pe 判定头长度与目录数不一致。
	opt := make([]byte, 224)
	binary.LittleEndian.PutUint16(opt[0:], 0x10b)
	binary.LittleEndian.PutUint32(opt[28:], imageBase)
	binary.LittleEndian.PutUint32(opt[92:], 16)
	buf.Write(opt)
	// .text 节表：VirtualAddress=sectionVA，原始数据在 rawOffset。
	sh := make([]byte, 40)
	copy(sh, ".text")
	binary.LittleEndian.PutUint32(sh[8:], uint32(len(sectionData)))
	binary.LittleEndian.PutUint32(sh[12:], sectionVA)
	binary.LittleEndian.PutUint32(sh[16:], uint32(len(sectionData)))
	binary.LittleEndian.PutUint32(sh[20:], rawOffset)
	buf.Write(sh)
	buf.Write(bytes.Repeat([]byte{0}, rawOffset-buf.Len()))
	buf.Write(sectionData)
	path := filepath.Join(t.TempDir(), "ShiYeLine.exe")
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatalf("写入 PE 夹具: %v", err)
	}
	return path
}

// 验证口令提取：样本地址经 ImageBase 与节表映射到文件偏移，读出 NUL 前的字节。
func TestExtractCodecPassword(t *testing.T) {
	path := writePEFixture(t, 0x400000, 0x5e2000, fixturePassword)
	password, err := ExtractCodecPassword(path)
	if err != nil {
		t.Fatalf("提取口令: %v", err)
	}
	if !bytes.Equal(password, fixturePassword) {
		t.Fatalf("口令不匹配：got %q want %q", password, fixturePassword)
	}
}

// 回归边界：地址处为空字符串、64 字节内无 NUL、地址不落在任何节内均须明确报错。
func TestExtractCodecPasswordRejects(t *testing.T) {
	cases := []struct {
		name     string
		password []byte
		wantErr  string
	}{
		{"空字符串", []byte{}, "空字符串"},
		{"超限无NUL", nil, "超过"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := writePEFixture(t, 0x400000, 0x5e2000, tc.password)
			if _, err := ExtractCodecPassword(path); err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("错误不匹配：got %v want 含 %q", err, tc.wantErr)
			}
		})
	}
	t.Run("地址不在节内", func(t *testing.T) {
		// 节起始挪到 0x1000，样本 RVA 0x5e2e3c 不在其中。
		path := writePEFixture(t, 0x400000, 0x1000, fixturePassword)
		if _, err := ExtractCodecPassword(path); err == nil || !strings.Contains(err.Error(), "不落在") {
			t.Fatalf("错误不匹配：got %v want 含 %q", err, "不落在")
		}
	})
	t.Run("文件缺失", func(t *testing.T) {
		if _, err := ExtractCodecPassword(filepath.Join(t.TempDir(), "no.exe")); err == nil {
			t.Fatal("缺失文件应报错")
		}
	})
}

// 验证 exe 探测：标准根下唯一命中返回路径，零命中与多命中明确报错不猜测。
func TestDetectExePath(t *testing.T) {
	origRoots := exeSearchRoots
	defer func() { exeSearchRoots = origRoots }()
	root := t.TempDir()
	exeSearchRoots = []string{root}
	touch := func(rel string) {
		path := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Run("唯一命中", func(t *testing.T) {
		touch("nwt3055/ShiYeLine.exe")
		got, err := DetectExePath()
		if err != nil {
			t.Fatalf("探测: %v", err)
		}
		if want := filepath.Join(root, "nwt3055", "ShiYeLine.exe"); got != want {
			t.Fatalf("路径不匹配：got %q want %q", got, want)
		}
	})
	t.Run("多命中", func(t *testing.T) {
		touch("nwt3060/ShiYeLine.exe")
		if _, err := DetectExePath(); err == nil || !strings.Contains(err.Error(), "多个客户端候选") {
			t.Fatalf("错误不匹配：got %v", err)
		}
	})
	t.Run("零命中", func(t *testing.T) {
		if err := os.RemoveAll(root); err != nil {
			t.Fatal(err)
		}
		if _, err := DetectExePath(); err == nil || !strings.Contains(err.Error(), "未找到") {
			t.Fatalf("错误不匹配：got %v", err)
		}
	})
}

// 验证数据文件路径固定推导自 exe 目录，与文档声明的实时库位置一致。
func TestDeriveDataPaths(t *testing.T) {
	exe := `C:\Program Files (x86)\nwt3055\ShiYeLine.exe`
	if got, want := DeriveNRPath(exe), `C:\Program Files (x86)\nwt3055\Syl\cache\db\nr`; got != want {
		t.Fatalf("nr 路径不匹配：got %q want %q", got, want)
	}
	if got, want := DeriveQRPPath(exe), `C:\Program Files (x86)\nwt3055\Syl\data\qrp`; got != want {
		t.Fatalf("qrp 路径不匹配：got %q want %q", got, want)
	}
}
