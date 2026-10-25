// 本程序是统一发送 API 的 Windows 托盘宿主：托盘图标随客户端连接状态切换，
// 菜单提供 API 服务启动/停止与程序退出。HTTP 层在 internal/api、窗口与数据库
// 访问全部在 internal/agent，本文件只做托盘壳。启动参数全部可留空，留空时自动
// 识别客户端安装位置、数据文件与 codec 口令，双击即可启动。
//
// 发布构建（无控制台窗口）：
// GOOS=windows GOARCH=386 go build -ldflags "-H=windowsgui" -o nwttray.exe ./cmd/nwttray
// 调试构建（保留 stderr 输出）：GOOS=windows GOARCH=386 go build ./cmd/nwttray
package main

import (
	_ "embed"
	"flag"
	"fmt"
	"net"
	"os"
	"os/exec"
	"syscall"
	"time"
	"unsafe"

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
		// windowsgui 构建没有控制台，stderr 无人可见，用消息框呈现致命错误。
		fatalBox(err)
		os.Exit(1)
	}
}

// fatalBox 弹出系统错误消息框；仅在启动失败等致命路径调用。
func fatalBox(err error) {
	messageBox := syscall.NewLazyDLL("user32.dll").NewProc("MessageBoxW")
	title, _ := syscall.UTF16PtrFromString("nwttray 启动失败")
	text, _ := syscall.UTF16PtrFromString(err.Error())
	// 0x10 = MB_ICONERROR。
	messageBox.Call(0, uintptr(unsafe.Pointer(text)), uintptr(unsafe.Pointer(title)), 0x10)
}

// confirmBox 弹出带警告图标的确认框，返回用户是否点击了"确定"。
func confirmBox(text string) bool {
	messageBox := syscall.NewLazyDLL("user32.dll").NewProc("MessageBoxW")
	title, _ := syscall.UTF16PtrFromString("确认外网监听")
	content, _ := syscall.UTF16PtrFromString(text)
	// 0x31 = MB_OKCANCEL | MB_ICONWARNING；返回 1 = IDOK。
	ret, _, _ := messageBox.Call(0, uintptr(unsafe.Pointer(content)), uintptr(unsafe.Pointer(title)), 0x31)
	return ret == 1
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
	listen := newListenConfig(*options.Addr)
	systray.Run(func() { onReady(unified, server, *autostart, listen) }, func() { _ = server.Stop() })
	return nil
}

// listenConfig 描述本机与外网两个监听形态，供"启用外网监听"勾选项切换。
type listenConfig struct {
	local      string
	external   string
	externalOn bool
}

// newListenConfig 由 -addr 推导两个形态：端口沿用 -addr，外网形态固定通配地址。
// -addr 本身就是通配地址时勾选项初始为已勾选。
func newListenConfig(addr string) listenConfig {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		host, port = "127.0.0.1", "8787"
	}
	switch host {
	case "", "0.0.0.0", "::":
		return listenConfig{local: "127.0.0.1:" + port, external: net.JoinHostPort(host, port), externalOn: true}
	}
	return listenConfig{local: net.JoinHostPort(host, port), external: "0.0.0.0:" + port}
}

// onReady 装配托盘图标与菜单，并启动状态轮询；systray 保证只在就绪后调用一次。
func onReady(unified *agent.Agent, server *api.Server, autostart bool, listen listenConfig) {
	systray.SetIcon(iconDisconnected)
	systray.SetTitle("内网通Agent")
	systray.SetTooltip("内网通 Agent — 检测客户端中")
	toggleItem := systray.AddMenuItem("启动 API 服务", "启动或停止统一发送 API")
	externalItem := systray.AddMenuItem("启用外网监听（0.0.0.0）",
		"点击后改监听 0.0.0.0，供局域网其他机器调用；API 无鉴权，仅限可信局域网")
	docsItem := systray.AddMenuItem("打开 API 文档", "在浏览器中打开内置接口说明")
	systray.AddSeparator()
	statusItem := systray.AddMenuItem("客户端状态：检测中", "内网通客户端连接状态")
	statusItem.Disable()
	systray.AddSeparator()
	quitItem := systray.AddMenuItem("退出", "退出托盘程序并停止 API 服务")
	externalOn := listen.externalOn
	if externalOn {
		externalItem.SetTitle(fmt.Sprintf("停止外网监听（%s）", listen.external))
	}
	if autostart {
		applyToggle(server, toggleItem)
	}
	go func() {
		for {
			select {
			case <-toggleItem.ClickedCh:
				applyToggle(server, toggleItem)
			case <-externalItem.ClickedCh:
				externalOn = applyExternal(server, externalItem, toggleItem, listen, externalOn)
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
func applyToggle(server *api.Server, toggleItem *systray.MenuItem) {
	if server.Running() {
		if err := server.Stop(); err != nil {
			systray.SetTooltip(fmt.Sprintf("内网通 Agent — 停止 API 失败：%v", err))
			return
		}
	} else if err := server.Start(); err != nil {
		systray.SetTooltip(fmt.Sprintf("内网通 Agent — 启动 API 失败：%v", err))
		return
	}
	refreshServerUI(server, toggleItem)
}

// applyExternal 处理「启用/停止外网监听」两态切换：未启用时先确认（API 无鉴权，
// 暴露即信任局域网），确认后切到通配地址；已启用时切回 -addr 形态。服务未运行时
// 只记录地址，下次启动生效。返回最新启用状态；切换失败时状态不变，标题不改。
func applyExternal(server *api.Server, externalItem, toggleItem *systray.MenuItem, listen listenConfig, enabled bool) bool {
	if !enabled {
		if !confirmBox(fmt.Sprintf(
			"将监听 %s，供局域网其他机器调用 API。\n\nAPI 目前无鉴权，任何能访问本机的机器都可发送消息。\n确认该局域网可信？",
			listen.external)) {
			return false
		}
		if err := server.SetAddr(listen.external); err != nil {
			systray.SetTooltip(fmt.Sprintf("内网通 Agent — 启用外网监听失败：%v", err))
			return false
		}
		externalItem.SetTitle(fmt.Sprintf("停止外网监听（%s）", listen.external))
		refreshServerUI(server, toggleItem)
		return true
	}
	if err := server.SetAddr(listen.local); err != nil {
		systray.SetTooltip(fmt.Sprintf("内网通 Agent — 停止外网监听失败：%v", err))
		return true
	}
	externalItem.SetTitle("启用外网监听（0.0.0.0）")
	refreshServerUI(server, toggleItem)
	return false
}

// refreshServerUI 按服务当前状态刷新启停菜单标题与托盘提示。
func refreshServerUI(server *api.Server, toggleItem *systray.MenuItem) {
	if !server.Running() {
		toggleItem.SetTitle("启动 API 服务")
		systray.SetTooltip("内网通 Agent — API 已停止")
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
