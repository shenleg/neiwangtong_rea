// 本程序是统一发送 API 的命令行入口：HTTP 层在 internal/api，只依赖 internal/agent，
// 不直接接触 HWND、WM_COPYDATA 或数据库文件。默认只监听本机回环地址。
//
// 构建：GOOS=windows GOARCH=386 go build ./cmd/nwtagent（发送只允许 Windows 386）。
package main

import (
	"flag"
	"fmt"
	"net/http"
	"os"

	"neiwangtong-gateway/internal/agent"
	"neiwangtong-gateway/internal/api"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string) error {
	flags := flag.NewFlagSet("nwtagent", flag.ContinueOnError)
	options := agent.RegisterFlags(flags)
	if err := flags.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return nil
		}
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("不接受位置参数")
	}
	unified, err := agent.Build(options)
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "nwtagent 监听 %s（nr=%t qrp=%t；HTTP 层不接触 HWND）\n",
		*options.Addr, *options.NRPath != "", *options.QRPPath != "")
	return http.ListenAndServe(*options.Addr, api.NewMux(unified))
}
