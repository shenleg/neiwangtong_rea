// 本程序提供IPC Adapter的实验命令入口：默认只读；显式发送需要单一群或用户目标配置及实验确认，
// -observe可单独追加外部观察；记录标识由Adapter生成。
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

func runWith(args []string, execute func(string, ipc.Config, bool, string, io.Reader, io.Writer) error) error {
	flags := flag.NewFlagSet("ipcexperiment", flag.ContinueOnError)
	exe := flags.String("exe", "", "操作者确认的ShiYeLine.exe Windows绝对路径")
	configPath := flags.String("config", "", "私有目标JSON配置路径；不在命令行填写目标ID")
	send := flags.Bool("send", false, "请求单次实验；仍要求单目标配置和完整确认，不自动重试")
	text := flags.String("text", "", "自定义单行正文；确认短语将包含该正文，缺省用固定实验正文NWT-GW-AUTH-TEST-0001")
	observe := flags.String("observe", "", "只追加一条外部观察的已占用发送记录标识；不发送")
	observeKind := flags.String("observe-kind", "", "观察类别：client、sender 或 recipient")
	observeState := flags.String("observe-state", "", "观察值，如 stable、one、none、unknown")
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
	if *observe != "" {
		if *send {
			return fmt.Errorf("-observe只记录观察，不能与-send同时使用")
		}
		if *observeKind == "" || *observeState == "" {
			return fmt.Errorf("-observe必须同时提供-observe-kind与-observe-state")
		}
		kind, err := observationKind(*observeKind)
		if err != nil {
			return err
		}
		adapter, err := ipc.NewAdapter(*exe)
		if err != nil {
			return err
		}
		return recordObservation(adapter, *observe, kind, *observeState, os.Stdout)
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
	if *send {
		switch {
		case config.AuthorizedGroupID != "" && config.AuthorizedUserID != "":
			return fmt.Errorf("配置同时包含群与用户目标，发送实验须明确其一；未发送")
		case config.AuthorizedGroupID == "" && config.AuthorizedUserID == "":
			return fmt.Errorf("实验发送必须通过-config显式加载群或用户目标；未发送")
		}
	}
	message := *text
	if message == "" {
		message = ipc.Message
	}
	return execute(*exe, config, *send, message, os.Stdin, os.Stdout)
}
