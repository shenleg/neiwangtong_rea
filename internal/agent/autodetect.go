// 本文件实现零参数启动的自动识别：客户端 exe 探测、数据文件路径推导与
// nr codec 口令的 PE 内嵌提取；识别结果均可被显式参数覆盖。
package agent

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"

	"debug/pe"
)

// exeSearchRoots 是客户端安装目录的探测根；匹配 <根>\nwt*\ShiYeLine.exe。
var exeSearchRoots = []string{
	`C:\Program Files (x86)`,
	`C:\Program Files`,
}

// codecPasswordVA 是客户端 exe 内 nr codec 口令窄字符串的虚拟地址，
// 来源：已提交证据 docs/research/reports/user-id/0055-read_function_instructions.json
// （安装 codec 路径 0x6f9915 处两次 MOV EBX, 0x9e2e3c 后调用派生函数）。
// 该地址绑定已验证的 3.4.3055 样本；客户端升级后若提取失败，用 -password-file 显式覆盖。
const codecPasswordVA = 0x9e2e3c

// passwordMaxBytes 是口令字符串的读取上限，防止地址失效后把无关数据当口令。
const passwordMaxBytes = 64

// DetectExePath 在标准安装根下探测唯一的 ShiYeLine.exe；零命中或多命中均报错，
// 提示用 -exe 显式指定，不猜测。
func DetectExePath() (string, error) {
	var candidates []string
	for _, root := range exeSearchRoots {
		matches, err := filepath.Glob(filepath.Join(root, "nwt*", "ShiYeLine.exe"))
		if err != nil {
			return "", fmt.Errorf("探测客户端安装目录 %s: %w", root, err)
		}
		candidates = append(candidates, matches...)
	}
	switch len(candidates) {
	case 1:
		return candidates[0], nil
	case 0:
		return "", fmt.Errorf("在 %v 下未找到 nwt*\\ShiYeLine.exe，请用 -exe 指定客户端路径", exeSearchRoots)
	default:
		return "", fmt.Errorf("发现多个客户端候选 %v，请用 -exe 指定其中一个", candidates)
	}
}

// DeriveNRPath 返回客户端实时 nr 联系人库的固定位置（<exe目录>\Syl\cache\db\nr）。
func DeriveNRPath(exePath string) string {
	return filepath.Join(filepath.Dir(exePath), "Syl", "cache", "db", "nr")
}

// DeriveQRPPath 返回客户端实时 qrp 群库的固定位置（<exe目录>\Syl\data\qrp）。
func DeriveQRPPath(exePath string) string {
	return filepath.Join(filepath.Dir(exePath), "Syl", "data", "qrp")
}

// ExtractCodecPassword 从客户端 exe 的 PE 映射中读取 codec 口令窄字符串
// （读至 NUL，上限 64 字节）。地址不落在任何节、字符串为空或超限时明确报错。
func ExtractCodecPassword(exePath string) ([]byte, error) {
	file, err := os.Open(exePath)
	if err != nil {
		return nil, fmt.Errorf("打开客户端 exe 提取口令: %w", err)
	}
	defer file.Close()
	peFile, err := pe.NewFile(file)
	if err != nil {
		return nil, fmt.Errorf("解析客户端 PE: %w", err)
	}
	defer peFile.Close()
	optional, ok := peFile.OptionalHeader.(*pe.OptionalHeader32)
	if !ok {
		return nil, fmt.Errorf("客户端 exe 不是 32 位 PE，无法按样本地址提取口令")
	}
	rva := uint32(codecPasswordVA) - optional.ImageBase
	buf := make([]byte, passwordMaxBytes)
	for _, section := range peFile.Sections {
		start := section.VirtualAddress
		size := section.VirtualSize
		if size < section.Size {
			size = section.Size
		}
		if rva < start || rva >= start+size {
			continue
		}
		if _, err := section.ReadAt(buf, int64(rva-start)); err != nil {
			return nil, fmt.Errorf("读取口令字符串: %w", err)
		}
		if i := bytes.IndexByte(buf, 0); i >= 0 {
			if i == 0 {
				return nil, fmt.Errorf("口令地址 %#x 处为空字符串", codecPasswordVA)
			}
			return buf[:i], nil
		}
		return nil, fmt.Errorf("口令字符串超过 %d 字节上限，样本地址 %#x 可能已失效", passwordMaxBytes, codecPasswordVA)
	}
	return nil, fmt.Errorf("口令地址 %#x 不落在客户端 exe 的任何节内，样本地址可能已失效", codecPasswordVA)
}
