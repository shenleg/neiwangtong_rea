// 本程序是统一发送 API 的 Windows 托盘宿主：托盘图标随客户端连接状态切换，
// 菜单提供 API 服务启动/停止与程序退出。HTTP 层在 internal/api、窗口与数据库
// 访问全部在 internal/agent，本文件只做托盘壳。
//
// 构建：GOOS=windows GOARCH=386 go build ./cmd/nwttray（发送只允许 Windows 386）。
package main

import (
	_ "embed"
	"flag"
	"fmt"
	"net"
	"os"
	"os/exec"
	"time"

	"github.com/getlantern/systray"

	"neiwangtong-gateway/internal/agent"
	"neiwangtong-gateway/internal/api"
)

//go:embed icons/connected.ico
var iconConnected []byte

//go:embed icons/disconnected.ico
var iconDisconnected []byte

// statusInterval 是客户端连接状态的轮询周期。
const statusInterval = 10 * time.Second

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string) error {
	flags := flag.NewFlagSet("nwttray", flag.ContinueOnError)
	options := agent.RegisterFlags(flags)
	autostart := flags.Bool("autostart", false, "启动后立即开始提供 API 服务，不等待托盘菜单")
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
	server := api.NewServer(*options.Addr, unified)
	systray.Run(func() { onReady(unified, server, *autostart) }, func() { _ = server.Stop() })
	return nil
}

// onReady 装配托盘图标与菜单，并启动状态轮询；systray 保证只在就绪后调用一次。
func onReady(unified *agent.Agent, server *api.Server, autostart bool) {
	systray.SetIcon(iconDisconnected)
	systray.SetTitle("内网通Agent")
	systray.SetTooltip("内网通 Agent — 检测客户端中")
	toggleItem := systray.AddMenuItem("启动 API 服务", "启动或停止统一发送 API")
	docsItem := systray.AddMenuItem("打开 API 文档", "在浏览器中打开内置接口说明")
	systray.AddSeparator()
	statusItem := systray.AddMenuItem("客户端状态：检测中", "内网通客户端连接状态")
	statusItem.Disable()
	systray.AddSeparator()
	quitItem := systray.AddMenuItem("退出", "退出托盘程序并停止 API 服务")
	if autostart {
		applyToggle(unified, server, toggleItem)
	}
	go func() {
		for {
			select {
			case <-toggleItem.ClickedCh:
				applyToggle(unified, server, toggleItem)
			case <-docsItem.ClickedCh:
				openDocs(server)
			case <-quitItem.ClickedCh:
				systray.Quit()
				return
			}
		}
	}()
	go pollClientStatus(unified, statusItem)
}

// openDocs 保证服务在监听后用默认浏览器打开内置文档页。
func openDocs(server *api.Server) {
	if !server.Running() {
		if err := server.Start(); err != nil {
			systray.SetTooltip(fmt.Sprintf("内网通 Agent — 打开文档前启动 API 失败：%v", err))
			return
		}
	}
	addr := server.Addr()
	// 绑定 0.0.0.0 等通配地址时浏览器仍走回环。
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		systray.SetTooltip(fmt.Sprintf("内网通 Agent — 文档地址解析失败：%v", err))
		return
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	url := fmt.Sprintf("http://%s:%s/docs", host, port)
	if err := exec.Command("cmd", "/c", "start", "", url).Start(); err != nil {
		systray.SetTooltip(fmt.Sprintf("内网通 Agent — 打开浏览器失败：%v", err))
		return
	}
	systray.SetTooltip(fmt.Sprintf("内网通 Agent — API 文档 %s", url))
}

// applyToggle 在运行/停止之间切换 API 服务，并把结果反映到菜单标题。
func applyToggle(unified *agent.Agent, server *api.Server, toggleItem *systray.MenuItem) {
	if server.Running() {
		if err := server.Stop(); err != nil {
			systray.SetTooltip(fmt.Sprintf("内网通 Agent — 停止 API 失败：%v", err))
			return
		}
		toggleItem.SetTitle("启动 API 服务")
		systray.SetTooltip("内网通 Agent — API 已停止")
		return
	}
	if err := server.Start(); err != nil {
		systray.SetTooltip(fmt.Sprintf("内网通 Agent — 启动 API 失败：%v", err))
		return
	}
	toggleItem.SetTitle(fmt.Sprintf("停止 API 服务（%s）", server.Addr()))
	systray.SetTooltip(fmt.Sprintf("内网通 Agent — API 监听 %s", server.Addr()))
}

// pollClientStatus 周期核对客户端实例并更新状态菜单、图标与提示。
func pollClientStatus(unified *agent.Agent, statusItem *systray.MenuItem) {
	for {
		info, err := unified.Preflight()
		if err == nil {
			statusItem.SetTitle(fmt.Sprintf("客户端状态：已连接（PID %d）", info.PID))
			systray.SetIcon(iconConnected)
		} else {
			statusItem.SetTitle("客户端状态：未连接")
			systray.SetIcon(iconDisconnected)
		}
		time.Sleep(statusInterval)
	}
}
