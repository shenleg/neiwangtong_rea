// 本程序提供IPC Adapter的实验命令入口：默认只读，显式发送需要单群配置及实验确认，记录标识由Adapter生成。
package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"runtime"

	"neiwangtong-gateway/internal/ipc"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string) error {
	return runWith(args, runExperiment)
}

func runWith(args []string, execute func(string, ipc.Config, bool, io.Reader, io.Writer) error) error {
	flags := flag.NewFlagSet("ipcexperiment", flag.ContinueOnError)
	exe := flags.String("exe", "", "操作者确认的ShiYeLine.exe Windows绝对路径")
	configPath := flags.String("config", "", "私有目标JSON配置路径；不在命令行填写群ID")
	send := flags.Bool("send", false, "请求单次实验；仍要求单群配置和完整确认，不自动重试")
	if err := flags.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return nil
		}
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("不接受位置参数、群ID或消息文本")
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
			return fmt.Errorf("参数%s不能重复", name)
		}
		seen[name] = true
	}
	if *exe == "" {
		return fmt.Errorf("必须提供-exe绝对路径；默认仅执行只读检查")
	}
	if *send && (runtime.GOOS != "windows" || runtime.GOARCH != "386") {
		return fmt.Errorf("本次发送只允许Windows 386构建")
	}
	var config ipc.Config
	if *configPath != "" {
		var err error
		config, err = ipc.LoadConfig(*configPath)
		if err != nil {
			return err
		}
	}
	if *send && config.AuthorizedGroupID == "" {
		return fmt.Errorf("实验发送必须通过-config显式加载群目标；未发送")
	}
	return execute(*exe, config, *send, os.Stdin, os.Stdout)
}
