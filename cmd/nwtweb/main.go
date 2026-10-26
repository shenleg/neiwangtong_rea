// 本程序自动识别独立原生协议节点的本机网络并提供Web/API，默认监听全部IPv4网卡；显式上线后常驻，信号退出时正常下线。
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"neiwangtong-gateway/internal/nativeweb"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := run(ctx, os.Args[1:], os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}

// run 只读识别/校验网络后启动无鉴权HTTP；返回0为正常退出/只读校验，1为运行失败，2为配置或识别错误。
func run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	return runWithNetwork(ctx, args, stdout, stderr, detectLocalNetwork)
}

// runWithNetwork 使用detect只读识别省略的本机参数，便于合成测试；返回码与run相同，不自动协议上线。
func runWithNetwork(ctx context.Context, args []string, stdout, stderr io.Writer, detect func(netip.Addr) (netip.Addr, netip.Prefix, error)) int {
	flags := flag.NewFlagSet("nwtweb", flag.ContinueOnError)
	flags.SetOutput(stderr)
	listenText := flags.String("listen", "0.0.0.0:8788", "网页IPv4监听地址；默认全部IPv4网卡，可指定回环或私有IPv4")
	localText := flags.String("local", "", "默认自动识别本机局域网IPv4及掩码；可手动指定IPv4或IPv4/前缀长度")
	prefixText := flags.String("prefix", "", "IPv4子网；-local已含前缀时可省略，同时指定须为同一子网")
	sessionDuration := flags.Duration("session-duration", 0, "每次协议上线的活动时限；0常驻，到时正常下线且HTTP继续运行")
	name := flags.String("name", "NWT-Go-Web", "独立节点名称")
	dataDir := flags.String("data-dir", "local/nativeclient/web-node", "独立身份、联系人快照与运行记录目录；相对路径以当前工作目录为准")
	discoveryPort := flags.Uint("discovery-port", 9011, "本机发现端口")
	enetPort := flags.Uint("enet-port", 9012, "本机ENet端口")
	check := flags.Bool("check", false, "只读识别及校验，不创建目录、身份或记录，不绑定HTTP/UDP端口")
	online := flags.Bool("online", false, "启动后主动上线；默认等待网页点击上线")
	if err := flags.Parse(args); err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	local, prefix, err := resolveLocalNetwork(*localText, *prefixText, detect)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	absoluteDir, err := filepath.Abs(*dataDir)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	listen, listenErr := netip.ParseAddrPort(*listenText)
	if flags.NArg() != 0 || listenErr != nil || !listen.Addr().Is4() || !(listen.Addr().IsLoopback() || listen.Addr().IsPrivate() || listen.Addr().IsUnspecified()) || *discoveryPort > 65535 || *enetPort > 65535 || *dataDir == "" {
		fmt.Fprintln(stderr, "参数无效：网页须为IPv4回环、私有IPv4或0.0.0.0，并指定非空数据目录与有效端口")
		return 2
	}
	manager, err := nativeweb.New(nativeweb.Config{Local: local, Prefix: prefix, Name: *name,
		DataDir: absoluteDir, DiscoveryPort: uint16(*discoveryPort), ENetPort: uint16(*enetPort), SessionDuration: *sessionDuration})
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	if *check {
		_, err := fmt.Fprintf(stdout, "本机IPv4：%s\n发现子网：%s\n配置校验通过；未启动HTTP、未创建身份、未连接对端、未发送消息。\n", local, prefix)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		return 0
	}
	listener, err := net.Listen("tcp4", listen.String())
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	address := listener.Addr().(*net.TCPAddr).AddrPort()
	handler, err := nativeweb.NewHandler(manager, address)
	if err != nil {
		fmt.Fprintln(stderr, errors.Join(err, listener.Close()))
		return 1
	}
	server := &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second,
		WriteTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16 << 10}
	serveDone := make(chan error, 1)
	go func() { serveDone <- server.Serve(listener) }()
	if *online {
		err = manager.Start(*name)
	}
	if err == nil {
		access := address
		if address.Addr().IsUnspecified() {
			access = netip.AddrPortFrom(local, address.Port())
		}
		_, err = fmt.Fprintf(stdout, "HTTP监听：%s\n本机IPv4：%s\n发现子网：%s\n网页：http://%s\nSwagger：http://%s/docs\nOpenAPI：http://%s/openapi.json\n初版无鉴权；默认不会重连或补发，关闭网页不影响后台在线。\n", address, local, prefix, access, access, access)
	}
	if err == nil {
		select {
		case <-ctx.Done():
		case serveErr := <-serveDone:
			if !errors.Is(serveErr, http.ErrServerClosed) {
				err = serveErr
			}
		}
	}
	// 先停止后台及已受理发送，再等待HTTP处理器结束，避免Shutdown等待一个仍在线的协议请求。
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	err = errors.Join(err, manager.Close(shutdownCtx))
	shutdownErr := server.Shutdown(shutdownCtx)
	if shutdownErr != nil {
		shutdownErr = errors.Join(shutdownErr, server.Close())
	}
	err = errors.Join(err, shutdownErr)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return 0
}

// parseLocalNetwork 解析本机IPv4及子网，保留CIDR中的本机地址；缺失、非法或冲突的网络参数返回错误。
func parseLocalNetwork(localText, prefixText string) (netip.Addr, netip.Prefix, error) {
	if strings.Contains(localText, "/") {
		localPrefix, err := netip.ParsePrefix(localText)
		if err != nil || !localPrefix.Addr().Is4() {
			return netip.Addr{}, netip.Prefix{}, fmt.Errorf("-local须为本机IPv4/前缀长度: %q", localText)
		}
		if prefixText != "" {
			prefix, err := netip.ParsePrefix(prefixText)
			if err != nil || prefix.Masked() != localPrefix.Masked() {
				return netip.Addr{}, netip.Prefix{}, fmt.Errorf("-prefix须与-local中的子网相同")
			}
		}
		return localPrefix.Addr(), localPrefix.Masked(), nil
	}
	local, err := netip.ParseAddr(localText)
	if err != nil || !local.Is4() {
		return netip.Addr{}, netip.Prefix{}, fmt.Errorf("-local须为本机IPv4/前缀长度，或IPv4配合-prefix")
	}
	prefix, err := netip.ParsePrefix(prefixText)
	if err != nil || !prefix.Addr().Is4() {
		return netip.Addr{}, netip.Prefix{}, fmt.Errorf("-local不含前缀长度时须指定IPv4子网-prefix")
	}
	return local, prefix.Masked(), nil
}
