// 本文件集中定义两个宿主（命令行与托盘）共用的启动参数与构建逻辑，避免入口间复制漂移。
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
func RegisterFlags(set *flag.FlagSet) Flags {
	return Flags{
		Exe:          set.String("exe", "", "操作者确认的ShiYeLine.exe Windows绝对路径"),
		Addr:         set.String("addr", "127.0.0.1:8787", "HTTP 监听地址；默认仅本机回环"),
		NRPath:       set.String("nr", "", "完整离线nr联系人副本路径（可选，名称解析用）"),
		PasswordFile: set.String("password-file", "", "nr codec 口令文件的绝对路径（配置-nr时必须提供）"),
		QRPPath:      set.String("qrp", "", "完整离线qrp群副本路径（可选，群名称解析用）"),
	}
}

// Build 校验参数、读取口令文件并构造 Agent；口令只在内存使用。
func Build(flags Flags) (*Agent, error) {
	if *flags.Exe == "" {
		return nil, fmt.Errorf("必须提供-exe绝对路径")
	}
	if (*flags.NRPath == "") != (*flags.PasswordFile == "") {
		return nil, fmt.Errorf("-nr 与 -password-file 必须同时提供或同时省略")
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
	}
	return New(NewAdapterSender(*flags.Exe), Options{
		NRPath:     *flags.NRPath,
		NRPassword: password,
		QRPPath:    *flags.QRPPath,
	}), nil
}
