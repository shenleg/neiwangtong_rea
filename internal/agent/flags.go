// 本文件集中定义托盘宿主的启动参数与构建逻辑：全部参数可留空，留空时自动识别。
package agent

import (
	"flag"
	"fmt"
	"io"
	"os"
)

// Flags 保存注册到某个 FlagSet 的公共启动参数指针。
type Flags struct {
	Exe          *string
	Addr         *string
	NRPath       *string
	PasswordFile *string
	QRPPath      *string
}

// RegisterFlags 在给定 FlagSet 上注册公共参数；两个宿主可再追加自己的参数。
// 除 -addr 外全部可留空：留空时 Build 自动识别，显式传入则优先。
func RegisterFlags(set *flag.FlagSet) Flags {
	return Flags{
		Exe:          set.String("exe", "", "ShiYeLine.exe Windows绝对路径；留空时自动探测 Program Files 下的 nwt* 安装目录"),
		Addr:         set.String("addr", "127.0.0.1:8787", "HTTP 监听地址；默认仅本机回环"),
		NRPath:       set.String("nr", "", "nr 联系人库路径；留空时用 <exe目录>\\Syl\\cache\\db\\nr"),
		PasswordFile: set.String("password-file", "", "nr codec 口令文件路径；留空时从 exe 内嵌字符串自动提取（样本地址 0x9e2e3c）"),
		QRPPath:      set.String("qrp", "", "qrp 群库路径；留空时用 <exe目录>\\Syl\\data\\qrp"),
	}
}

// Build 补全留空参数（exe 探测、数据路径推导、口令提取）、读取口令并构造 Agent；
// 口令只在内存使用。
func Build(flags Flags) (*Agent, error) {
	exePath := *flags.Exe
	if exePath == "" {
		detected, err := DetectExePath()
		if err != nil {
			return nil, err
		}
		exePath = detected
	}
	nrPath := *flags.NRPath
	if nrPath == "" {
		nrPath = DeriveNRPath(exePath)
	}
	qrpPath := *flags.QRPPath
	if qrpPath == "" {
		qrpPath = DeriveQRPPath(exePath)
	}
	if *flags.PasswordFile != "" && nrPath == "" {
		return nil, fmt.Errorf("提供了 -password-file 但未配置 nr 库，口令没有用途")
	}
	var password []byte
	if *flags.PasswordFile != "" {
		file, err := os.Open(*flags.PasswordFile)
		if err != nil {
			return nil, fmt.Errorf("打开口令文件: %w", err)
		}
		defer file.Close()
		// 读至 EOF 且不去除换行：口令文件必须恰好是口令字节本身。
		password, err = io.ReadAll(file)
		if err != nil {
			return nil, fmt.Errorf("读取口令文件: %w", err)
		}
	} else {
		extracted, err := ExtractCodecPassword(exePath)
		if err != nil {
			return nil, fmt.Errorf("%w；请改用 -password-file 显式提供口令", err)
		}
		password = extracted
	}
	return New(NewAdapterSender(exePath), Options{
		NRPath:     nrPath,
		NRPassword: password,
		QRPPath:    qrpPath,
	}), nil
}
