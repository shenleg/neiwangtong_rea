// 本程序以独立新身份执行一次限时单播上线实验；默认只校验参数，真实运行必须显式 -run，不发送聊天消息。
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/netip"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"neiwangtong-gateway/internal/nativeproto"
)

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }

// run 处理单次上线参数并返回退出码：0为只读校验/运行完成，1为运行错误，2为参数错误。
// 默认不创建身份、不绑定端口、不发包；显式运行独占保存记录，失败不换标识或自动再试。
func run(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("nwtnode", flag.ContinueOnError)
	flags.SetOutput(stderr)
	localText := flags.String("local", "", "明确本机 IPv4 地址")
	prefixText := flags.String("prefix", "", "本机 IPv4 子网，如10.0.0.0/24；只用于公告广播字段，不实际广播")
	remoteText := flags.String("remote", "", "唯一对端 IPv4:发现端口")
	name := flags.String("name", "", "独立新节点显示名称")
	identityPath := flags.String("identity", "", "自己的独立身份文件绝对路径；不传Windows客户端acc")
	recordPath := flags.String("record", "", "本次运行独占记录文件绝对路径")
	discoveryPort := flags.Uint("discovery-port", 9011, "本机实际绑定的发现端口")
	enetPort := flags.Uint("enet-port", 9012, "本机实际绑定的 ENet 主端口")
	duration := flags.Duration("duration", 90*time.Second, "包含发现、握手和退出的总时限，最多90秒")
	execute := flags.Bool("run", false, "执行一次显式单播上线；不聊天、不广播、不自动重连")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	local, err := netip.ParseAddr(*localText)
	if err != nil || !local.Is4() || local.IsUnspecified() || local.IsMulticast() {
		fmt.Fprintln(stderr, "须提供明确的本机IPv4")
		return 2
	}
	prefix, err := netip.ParsePrefix(*prefixText)
	if err != nil || !prefix.Addr().Is4() || !prefix.Contains(local) || prefix.Bits() > 30 {
		fmt.Fprintln(stderr, "须提供包含本机地址且不窄于/30的IPv4子网")
		return 2
	}
	remote, err := netip.ParseAddrPort(*remoteText)
	if err != nil || !remote.Addr().Is4() || remote.Port() == 0 || remote.Addr().IsUnspecified() || remote.Addr().IsMulticast() || remote.Addr() == local {
		fmt.Fprintln(stderr, "须提供不同于本机的单个IPv4发现对端")
		return 2
	}
	broadcast := prefixBroadcast(prefix)
	if remote.Addr() == broadcast || remote.Addr() == netip.AddrFrom4([4]byte{255, 255, 255, 255}) {
		fmt.Fprintln(stderr, "不允许广播发现目的地址")
		return 2
	}
	if flags.NArg() != 0 || *name == "" || !filepath.IsAbs(*identityPath) || !filepath.IsAbs(*recordPath) || *identityPath == *recordPath || *discoveryPort == 0 || *discoveryPort > 65535 || *enetPort == 0 || *enetPort > 65535 || *enetPort == *discoveryPort || *duration <= 5*time.Second || *duration > 90*time.Second {
		fmt.Fprintln(stderr, "名称/绝对私有文件路径/端口/时限无效；总时限须大于5秒且不超过90秒")
		return 2
	}
	profile, err := nativeproto.ProbeHandshake(*name)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	if !*execute {
		if err := json.NewEncoder(stdout).Encode(map[string]any{"mode": "validation_only", "network_invoked": false, "name": *name, "local": local.String(), "remote": remote.String(), "duration_seconds": duration.Seconds()}); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		return 0
	}
	if err := executeNode(local, broadcast, remote, uint16(*discoveryPort), uint16(*enetPort), profile, *name, *identityPath, *recordPath, *duration, stdout); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return 0
}

func executeNode(local, broadcast netip.Addr, remote netip.AddrPort, discoveryPort, enetPort uint16, profile nativeproto.Handshake, name, identityPath, recordPath string, duration time.Duration, stdout io.Writer) (runErr error) {
	// 记录先独占创建并Sync，再创建新身份/绑定socket；记录碰撞时本次没有任何网络副作用。
	file, err := os.OpenFile(recordPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return fmt.Errorf("独占创建记录失败，不换记录重试: %w", err)
	}
	defer func() { runErr = errors.Join(runErr, file.Close()) }()
	encoder := json.NewEncoder(file)
	record := func(value any) error {
		if err := encoder.Encode(value); err != nil {
			return err
		}
		return file.Sync()
	}
	if err := record(map[string]any{"event": "reserved", "started_at": time.Now().UTC(), "local": local.String(), "remote": remote.String(), "name": name, "duration_seconds": duration.Seconds(), "ui_observation": "unknown", "chat_sends": 0}); err != nil {
		return err
	}
	defer func() {
		if runErr != nil {
			runErr = errors.Join(runErr, record(map[string]any{"event": "error", "error": runErr.Error(), "ui_observation": "unknown"}))
		}
	}()
	id, err := nativeproto.LoadOrCreateIdentity(identityPath, rand.Reader)
	if err != nil {
		return err
	}
	session, err := nativeproto.NewSessionGUID(rand.Reader)
	if err != nil {
		return err
	}
	var nonce [4]byte
	if _, err := io.ReadFull(rand.Reader, nonce[:]); err != nil {
		return err
	}
	if nonce == ([4]byte{}) {
		return fmt.Errorf("本次 ConnectID 为协议未分配值，不重抽重试")
	}
	if err := record(map[string]any{"event": "own_identity", "user_id": id, "session_guid_hex": hex.EncodeToString(session[:]), "name": name}); err != nil {
		return err
	}
	discovery, err := net.ListenUDP("udp4", net.UDPAddrFromAddrPort(netip.AddrPortFrom(local, discoveryPort)))
	if err != nil {
		return err
	}
	defer discovery.Close()
	conn, err := net.ListenUDP("udp4", net.UDPAddrFromAddrPort(netip.AddrPortFrom(local, enetPort)))
	if err != nil {
		return err
	}
	defer conn.Close()
	node, err := nativeproto.NewNodePacket(nativeproto.NodeRegistration{UserID: id, SessionGUID: session, Address: local, Broadcast: broadcast, DiscoveryPort: discoveryPort, ENetPort: enetPort}, 1)
	if err != nil {
		return err
	}
	shutdown := 5 * time.Second
	client, err := nativeproto.NewClient(discovery, conn, nativeproto.ClientConfig{Node: node, Profile: profile, RemoteDiscovery: remote,
		Transport: nativeproto.ENetConfig{LocalPeerID: 0, ConnectID: nonce, MTU: 1400, WindowBytes: 65536, MaxMessageBytes: 1 << 20, MaxQueuedBytes: 4 << 20, MaxPendingCommands: 1024,
			RetransmitAfter: 500 * time.Millisecond, MaxAttempts: 6, ReceiveTimeout: 120 * time.Second, PingInterval: 30 * time.Second},
		Duration: duration - shutdown, ShutdownTimeout: shutdown, DiscoveryTimeout: 10 * time.Second, HandshakeTimeout: 15 * time.Second})
	if err != nil {
		return err
	}
	if err := json.NewEncoder(stdout).Encode(map[string]any{"event": "starting", "name": name, "record": recordPath, "identity_file": identityPath, "total_seconds": duration.Seconds()}); err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	result, err := client.Run(ctx, func(event nativeproto.ClientEvent) error {
		if err := record(event); err != nil {
			return err
		}
		if event.Stage == "protocol_ready" {
			return json.NewEncoder(stdout).Encode(event)
		}
		return nil
	})
	if resultErr := record(map[string]any{"event": "result", "result": result, "error": errorText(err), "ui_observation": "unknown", "chat_sends": 0}); resultErr != nil {
		return errors.Join(err, resultErr)
	}
	if outputErr := json.NewEncoder(stdout).Encode(map[string]any{"event": "result", "result": result, "ui_observation": "unknown"}); outputErr != nil {
		return errors.Join(err, outputErr)
	}
	return err
}

func prefixBroadcast(prefix netip.Prefix) netip.Addr {
	value := prefix.Masked().Addr().As4()
	for bit := prefix.Bits(); bit < 32; bit++ {
		value[bit/8] |= 1 << uint(7-bit%8)
	}
	return netip.AddrFrom4(value)
}

func errorText(err error) string {
	if err != nil {
		return err.Error()
	}
	return ""
}
