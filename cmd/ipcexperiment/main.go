// 本程序仅用于阶段八固定群聊实验；默认只读检查，未绑定授权群时拒绝发送。
package main

import (
	"flag"
	"fmt"
	"os"
	"runtime"

	"neiwangtong-gateway/internal/ipcexperiment"
)

// 用户选择先完成程序与测试，尚未提供授权群；不得填入猜测值或默认测试群。
// 后续须人工提供并明确授权唯一原生 group_id，修改此常量、重新测试和构建后才可发送。
const authorizedGroupID = ""

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string) error {
	flags := flag.NewFlagSet("ipcexperiment", flag.ContinueOnError)
	exe := flags.String("exe", "", "操作者确认的 ShiYeLine.exe Windows 绝对路径")
	send := flags.Bool("send", false, "请求单次实验；仍要求已绑定授权群和逐字人工确认")
	if err := flags.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return nil
		}
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("不接受位置参数、群 ID 或消息文本")
	}
	seen := make(map[string]bool)
	for _, arg := range args {
		if len(arg) == 0 || arg[0] != '-' {
			continue
		}
		name := arg
		for len(name) > 0 && name[0] == '-' {
			name = name[1:]
		}
		for i, c := range name {
			if c == '=' {
				name = name[:i]
				break
			}
		}
		if seen[name] {
			return fmt.Errorf("参数 %s 不能重复", name)
		}
		seen[name] = true
	}
	if *exe == "" {
		return fmt.Errorf("必须提供 -exe 绝对路径；默认仅执行只读检查")
	}
	if *send && (runtime.GOOS != "windows" || runtime.GOARCH != "386") {
		return fmt.Errorf("本次发送只允许 Windows 386 构建")
	}
	return ipcexperiment.Run(*exe, authorizedGroupID, *send, os.Stdin, os.Stdout)
}
