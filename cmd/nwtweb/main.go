// 本程序提供独立原生协议Web客户端：默认仅启动本地网页，显式上线后后台常驻，信号退出时正常下线。
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

// run 校验参数并启动无鉴权HTTP；局域网须显式-lan，返回0为正常退出/只读校验，1为运行失败，2为参数错误。
func run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("nwtweb", flag.ContinueOnError)
	flags.SetOutput(stderr)
	listenText := flags.String("listen", "127.0.0.1:8788", "网页IPv4地址；局域网地址须同时指定-lan")
	lan := flags.Bool("lan", false, "允许明确私有IPv4监听；初版无鉴权和HTTP加密，仅供可信内网")
	localText := flags.String("local", "", "明确本机IPv4")
	prefixText := flags.String("prefix", "", "包含本机地址且不窄于/30的IPv4子网")
	remoteText := flags.String("remote", "", "单播发现对端；-discover模式下可选，作为明确种子")
	discover := flags.Bool("discover", false, "在明确本机子网发送一次标准广播并接受多对端正常上线，不扫描IP")
	sessionDuration := flags.Duration("session-duration", 0, "每次协议上线的活动时限；0常驻，到时正常下线且HTTP继续运行")
	name := flags.String("name", "", "独立节点名称")
	dataDir := flags.String("data-dir", "", "独立身份、联系人快照与运行记录的私有绝对目录")
	discoveryPort := flags.Uint("discovery-port", 9011, "本机发现端口")
	enetPort := flags.Uint("enet-port", 9012, "本机ENet端口")
	check := flags.Bool("check", false, "只读校验，不创建目录、身份、记录或socket")
	online := flags.Bool("online", false, "启动后主动上线；默认等待网页点击上线")
	if err := flags.Parse(args); err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	listen, listenErr := netip.ParseAddrPort(*listenText)
	local, localErr := netip.ParseAddr(*localText)
	prefix, prefixErr := netip.ParsePrefix(*prefixText)
	remote, remoteErr := netip.ParseAddrPort(*remoteText)
	if flags.NArg() != 0 || listenErr != nil || !listen.Addr().Is4() || !(listen.Addr().IsLoopback() || *lan && listen.Addr().IsPrivate()) || localErr != nil || prefixErr != nil || remoteErr != nil && (*remoteText != "" || !*discover) || *discoveryPort > 65535 || *enetPort > 65535 {
		fmt.Fprintln(stderr, "参数无效：网页须为IPv4回环地址或带-lan的明确私有IPv4地址，并指定本机/子网/唯一对端与有效端口")
		return 2
	}
	manager, err := nativeweb.New(nativeweb.Config{Local: local, Prefix: prefix, Remote: remote, Name: *name,
		DataDir: *dataDir, DiscoveryPort: uint16(*discoveryPort), ENetPort: uint16(*enetPort), Discover: *discover, SessionDuration: *sessionDuration})
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	if *check {
		_, err := fmt.Fprintln(stdout, "配置校验通过；未启动HTTP、未创建身份、未连接对端、未发送消息。")
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
	handler, err := nativeweb.NewHandler(manager, listener.Addr().(*net.TCPAddr).AddrPort())
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
		_, err = fmt.Fprintf(stdout, "网页：http://%s\nSwagger：http://%s/docs\nOpenAPI：http://%s/openapi.json\n初版无鉴权；默认不会重连或补发，关闭网页不影响后台在线。\n", listener.Addr(), listener.Addr(), listener.Addr())
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
