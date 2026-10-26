// 本程序启动即以独立身份单播上线，同步群资料并处理stdin单聊/群聊；信号取消时正常退出，不扫描、不重连。
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

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := runContext(ctx, os.Args[1:], os.Stdin, os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}

type options struct {
	local, broadcast               netip.Addr
	remote                         netip.AddrPort
	name, identityPath, recordPath string
	discoveryPort, enetPort        uint16
	profile                        nativeproto.Handshake
	contacts                       *nativeproto.Directory
	check                          bool
}

// runContext 拥有并关闭input，解析明确配置后立即上线；返回0表示校验/正常取消，1为运行错误，2为参数错误。
// -check仅执行只读检查；运行结束等待输入协程退出后才关闭记录，输入EOF不取消网络运行。
func runContext(ctx context.Context, args []string, input io.ReadCloser, stdout, stderr io.Writer) (code int) {
	transferred := false
	defer func() {
		if !transferred {
			if err := input.Close(); err != nil {
				fmt.Fprintln(stderr, err)
				if code == 0 {
					code = 1
				}
			}
		}
	}()
	config, err := parseOptions(args, stderr)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	if config.check {
		if err := json.NewEncoder(stdout).Encode(map[string]any{"mode": "validation_only", "network_invoked": false, "name": config.name, "local": config.local.String(), "remote": config.remote.String(), "duration_seconds": 0, "chat_requested": false}); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		return 0
	}
	// executeClient负责在Run结束后解除输入阻塞并等待协程；这里转移输入的关闭责任。
	transferred = true
	if err := executeClient(ctx, config, input, stdout); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return 0
}

// parseOptions 在任何文件创建/socket前验证参数、资料、现有身份与联系人归属；缺失身份留待显式运行创建。
func parseOptions(args []string, stderr io.Writer) (options, error) {
	var result options
	flags := flag.NewFlagSet("nwtclient", flag.ContinueOnError)
	flags.SetOutput(stderr)
	localText := flags.String("local", "", "明确本机IPv4")
	prefixText := flags.String("prefix", "", "包含本机的IPv4子网；只计算公告字段，不广播")
	remoteText := flags.String("remote", "", "唯一IPv4单播发现对端")
	name := flags.String("name", "", "独立节点显示名称")
	identityPath := flags.String("identity", "", "自己的32字节身份文件绝对路径；不存在时独占创建")
	recordPath := flags.String("record", "", "私有新记录绝对路径；不覆盖已有记录")
	contactsPath := flags.String("contacts", "", "本身份联系人列表绝对路径；单聊发送时必需")
	discoveryPort := flags.Uint("discovery-port", 9011, "本机发现端口")
	enetPort := flags.Uint("enet-port", 9012, "本机ENet主端口")
	check := flags.Bool("check", false, "只读校验，不创建身份/记录/socket")
	if err := flags.Parse(args); err != nil {
		return result, err
	}
	local, err := netip.ParseAddr(*localText)
	if err != nil || !local.Is4() || local.IsUnspecified() || local.IsMulticast() || local == netip.MustParseAddr("255.255.255.255") {
		return result, fmt.Errorf("须提供明确的本机IPv4单播地址")
	}
	prefix, err := netip.ParsePrefix(*prefixText)
	if err != nil || !prefix.Addr().Is4() || !prefix.Contains(local) || prefix.Bits() > 30 {
		return result, fmt.Errorf("须提供包含本机地址且不窄于/30的IPv4子网")
	}
	broadcast := prefixBroadcast(prefix)
	remote, err := netip.ParseAddrPort(*remoteText)
	if err != nil || !remote.Addr().Is4() || remote.Port() == 0 || remote.Addr().IsUnspecified() || remote.Addr().IsMulticast() || remote.Addr() == local || remote.Addr() == broadcast || remote.Addr() == netip.MustParseAddr("255.255.255.255") {
		return result, fmt.Errorf("须提供不同于本机的单个IPv4单播发现对端")
	}
	if flags.NArg() != 0 || *name == "" || !filepath.IsAbs(*identityPath) || !filepath.IsAbs(*recordPath) || filepath.Clean(*identityPath) == filepath.Clean(*recordPath) || *discoveryPort == 0 || *discoveryPort > 65535 || *enetPort == 0 || *enetPort > 65535 || *discoveryPort == *enetPort {
		return result, fmt.Errorf("名称/不同的绝对私有文件路径/端口无效")
	}
	profile, err := nativeproto.ProbeHandshake(*name)
	if err != nil {
		return result, err
	}
	// 常驻入口已实现群同步与纯文本3011；只声明群资料/消息，不声明文件、远控或主动入群能力。
	for i := range profile.Features {
		if profile.Features[i].Name == "QGROUP" || profile.Features[i].Name == "QGROUP_MSG" {
			profile.Features[i].Value = "2:1"
		}
	}
	var contacts *nativeproto.Directory
	if *contactsPath != "" {
		if !filepath.IsAbs(*contactsPath) || filepath.Clean(*contactsPath) == filepath.Clean(*recordPath) || filepath.Clean(*contactsPath) == filepath.Clean(*identityPath) {
			return result, fmt.Errorf("联系人列表须为不同于身份和记录的绝对路径")
		}
		file, err := os.Open(*contactsPath)
		if err != nil {
			return result, err
		}
		contacts, err = nativeproto.LoadDirectory(file)
		if err := errors.Join(err, file.Close()); err != nil {
			return result, err
		}
	}
	id, exists, err := readIdentity(*identityPath)
	if err != nil {
		return result, err
	}
	if contacts != nil && (!exists || contacts.OwnUserID() != id) {
		return result, fmt.Errorf("联系人列表必须属于现有的当前独立身份")
	}
	return options{local: local, broadcast: broadcast, remote: remote, name: *name, identityPath: *identityPath, recordPath: *recordPath,
		discoveryPort: uint16(*discoveryPort), enetPort: uint16(*enetPort), profile: profile, contacts: contacts, check: *check}, nil
}

// readIdentity 只读检查已有身份，缺失返回exists=false；不调用可能创建文件的LoadOrCreateIdentity。
func readIdentity(path string) (id string, exists bool, err error) {
	file, err := os.Open(path)
	if os.IsNotExist(err) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	defer func() { err = errors.Join(err, file.Close()) }()
	data, err := io.ReadAll(io.LimitReader(file, 33))
	if err != nil {
		return "", true, err
	}
	if len(data) != 32 {
		return "", true, fmt.Errorf("独立身份文件须恰好为32字节")
	}
	for _, b := range data {
		if !(b >= '0' && b <= '9' || b >= 'a' && b <= 'f') {
			return "", true, fmt.Errorf("独立身份文件须为小写hex")
		}
	}
	return string(data), true, nil
}

// executeClient 独占记录并Sync后才创建身份与socket；任何失败保留记录，不换端口、不重连。
func executeClient(ctx context.Context, config options, input io.ReadCloser, stdout io.Writer) (runErr error) {
	inputClosed := false
	defer func() {
		if !inputClosed {
			runErr = errors.Join(runErr, input.Close())
		}
	}()
	file, err := os.OpenFile(config.recordPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return fmt.Errorf("独占创建记录失败，不换记录重试: %w", err)
	}
	defer func() { runErr = errors.Join(runErr, file.Close()) }()
	log := &eventLog{file: file, record: json.NewEncoder(file), output: json.NewEncoder(stdout)}
	if err := log.write(map[string]any{"event": "reserved", "started_at": time.Now().UTC(), "local": config.local.String(), "remote": config.remote.String(), "name": config.name, "duration_seconds": 0, "chat_sends": 0, "ui_observation": "unknown"}, false); err != nil {
		return err
	}
	defer func() {
		if runErr != nil {
			runErr = errors.Join(runErr, log.write(map[string]any{"event": "error", "error": runErr.Error()}, false))
		}
	}()
	id, err := nativeproto.LoadOrCreateIdentity(config.identityPath, rand.Reader)
	if err != nil {
		return err
	}
	if config.contacts != nil && config.contacts.OwnUserID() != id {
		return fmt.Errorf("联系人列表不属于当前独立身份")
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
		return fmt.Errorf("ConnectID为协议未分配值，不重抽重试")
	}
	node, err := nativeproto.NewNodePacket(nativeproto.NodeRegistration{UserID: id, SessionGUID: session, Address: config.local, Broadcast: config.broadcast, DiscoveryPort: config.discoveryPort, ENetPort: config.enetPort}, 1)
	if err != nil {
		return err
	}
	if err := log.write(map[string]any{"event": "own_identity", "user_id": id, "session_guid_hex": hex.EncodeToString(session[:])}, false); err != nil {
		return err
	}
	discovery, err := net.ListenUDP("udp4", net.UDPAddrFromAddrPort(netip.AddrPortFrom(config.local, config.discoveryPort)))
	if err != nil {
		return err
	}
	defer func() {
		if discovery != nil {
			runErr = errors.Join(runErr, discovery.Close())
		}
	}()
	conn, err := net.ListenUDP("udp4", net.UDPAddrFromAddrPort(netip.AddrPortFrom(config.local, config.enetPort)))
	if err != nil {
		return err
	}
	defer func() {
		if conn != nil {
			runErr = errors.Join(runErr, conn.Close())
		}
	}()
	cfg := clientConfig(config, node, nonce)
	cfg.ObserveGroup = func(group nativeproto.GroupInfo) error {
		return log.write(map[string]any{"event": "group_snapshot", "group": group}, false)
	}
	client, err := nativeproto.NewClient(discovery, conn, cfg)
	if err != nil {
		return err
	}
	if err := log.write(map[string]any{"event": "starting", "name": config.name, "record": config.recordPath, "identity_file": config.identityPath}, true); err != nil {
		return err
	}
	// Run开始后接管socket；输入协程只调用SendUser/SendGroup，不直接读写网络。
	discovery, conn = nil, nil
	runCtx, cancelRun := context.WithCancel(ctx)
	defer cancelRun()
	ready := make(chan struct{})
	inputDone := make(chan error, 1)
	go func() {
		select {
		case <-ready:
			err := consumeRequests(runCtx, input, config.contacts, config.remote, client.SendUser, client.SendGroup, log)
			if err != nil {
				cancelRun()
			}
			inputDone <- err
		case <-runCtx.Done():
			inputDone <- nil
		}
	}()
	result, err := client.Run(runCtx, func(event nativeproto.ClientEvent) error {
		if err := log.write(event, true); err != nil {
			return err
		}
		if event.Stage == "protocol_ready" {
			close(ready) // Run只在双向握手首次完成时发送一次protocol_ready。
		}
		return nil
	})
	cancelRun()
	closeErr := input.Close()
	inputClosed = true
	inputErr := <-inputDone
	err = errors.Join(err, closeErr, inputErr)
	resultErr := log.write(map[string]any{"event": "result", "result": result, "error": errorText(err), "chat_sends": result.ChatSends, "ui_observation": "unknown"}, true)
	return errors.Join(err, resultErr)
}

// clientConfig 复用限时nwtnode的协议预算及超时；Duration=0且Chat=nil只在外部取消时退出，不发送启动聊天。
func clientConfig(config options, node nativeproto.NodePacket, nonce [4]byte) nativeproto.ClientConfig {
	return nativeproto.ClientConfig{Node: node, Profile: config.profile, RemoteDiscovery: config.remote,
		Transport: nativeproto.ENetConfig{LocalPeerID: 0, ConnectID: nonce, MTU: 1400, WindowBytes: 65536, MaxMessageBytes: 1 << 20, MaxQueuedBytes: 4 << 20, MaxPendingCommands: 1024,
			RetransmitAfter: 500 * time.Millisecond, MaxAttempts: 6, ReceiveTimeout: 120 * time.Second, PingInterval: 30 * time.Second},
		Duration: 0, ShutdownTimeout: 5 * time.Second, DiscoveryTimeout: 10 * time.Second, HandshakeTimeout: 15 * time.Second, Contacts: config.contacts, GroupChat: true, Chat: nil}
}

// prefixBroadcast 仅计算公告所需的子网广播字段，不执行广播I/O。
func prefixBroadcast(prefix netip.Prefix) netip.Addr {
	value := prefix.Masked().Addr().As4()
	for bit := prefix.Bits(); bit < 32; bit++ {
		value[bit/8] |= 1 << uint(7-bit%8)
	}
	return netip.AddrFrom4(value)
}

// errorText 将错误转换为日志字段，成功时为空字符串。
func errorText(err error) string {
	if err != nil {
		return err.Error()
	}
	return ""
}
