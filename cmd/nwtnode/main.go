// 本程序以独立身份执行一次限时单播实验；默认只校验，-run 才上线，额外 -send 才提交一条明确目标的聊天消息。
package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
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
	execute := flags.Bool("run", false, "执行一次显式单播上线；不广播、不自动重连")
	send := flags.Bool("send", false, "与-run同时使用，向指定对端提交一条聊天消息；不自动重试")
	userID := flags.String("user-id", "", "明确聊天对端UserID；必须与发现节点一致")
	message := flags.String("message", "", "完整UTF-8纯文本正文；只有-send才发送")
	contactsPath := flags.String("contacts", "", "本独立身份自己的联系人JSON列表绝对路径")
	listContacts := flags.Bool("list-contacts", false, "只读输出联系人列表，不生成身份或建立网络连接")
	contactsOut := flags.String("contacts-out", "", "上线后将已验证对端资料独占写入新联系人列表文件；不覆盖已有文件")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	contacts, err := readContacts(*contactsPath)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	if *listContacts {
		if contacts == nil || *execute || *send || *userID != "" || *message != "" || *contactsOut != "" || flags.NArg() != 0 {
			fmt.Fprintln(stderr, "-list-contacts需要-contacts且不能与运行或发送参数混用")
			return 2
		}
		if err := json.NewEncoder(stdout).Encode(contacts.ListContacts()); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		return 0
	}
	chat, err := chatOptions(*userID, *message, *send, *execute, contacts)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	if chat != nil {
		contact, _ := contacts.ContactByID(chat.UserID) // chatOptions已验证唯一目标。
		if *remoteText == "" {
			*remoteText = contact.Discovery.String()
		} else if *remoteText != contact.Discovery.String() {
			fmt.Fprintln(stderr, "-remote必须与选定联系人的发现端点一致")
			return 2
		}
	}
	if *contactsOut != "" && (!filepath.IsAbs(*contactsOut) || *contactsOut == *identityPath || *contactsOut == *recordPath || *contactsOut == *contactsPath) {
		fmt.Fprintln(stderr, "联系人输出必须是不同于身份、记录和旧列表的绝对新路径")
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
		if err := json.NewEncoder(stdout).Encode(map[string]any{"mode": "validation_only", "network_invoked": false, "name": *name, "local": local.String(), "remote": remote.String(), "duration_seconds": duration.Seconds(), "chat_requested": chat != nil}); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		return 0
	}
	if err := executeNode(local, broadcast, remote, uint16(*discoveryPort), uint16(*enetPort), profile, *name, *identityPath, *recordPath, *duration, contacts, *contactsOut, chat, stdout); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return 0
}

func executeNode(local, broadcast netip.Addr, remote netip.AddrPort, discoveryPort, enetPort uint16, profile nativeproto.Handshake, name, identityPath, recordPath string, duration time.Duration, contacts *nativeproto.Directory, contactsOut string, chat *nativeproto.ChatRequest, stdout io.Writer) (runErr error) {
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
	if err := record(map[string]any{"event": "reserved", "started_at": time.Now().UTC(), "local": local.String(), "remote": remote.String(), "name": name, "duration_seconds": duration.Seconds(), "ui_observation": "unknown", "chat_sends": 0, "chat_requested": chat != nil}); err != nil {
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
	if contacts != nil && contacts.OwnUserID() != id {
		return fmt.Errorf("联系人列表不属于当前独立身份")
	}
	if chat != nil {
		if err := record(map[string]any{"event": "chat_request", "target_user_id_sha256": fmt.Sprintf("%x", sha256.Sum256([]byte(chat.UserID))), "message_sha256": fmt.Sprintf("%x", sha256.Sum256([]byte(chat.Text))), "message_bytes": len(chat.Text), "delivery": "unknown"}); err != nil {
			return err
		}
	}
	var contactFile *os.File
	if contactsOut != "" {
		contactFile, err = os.OpenFile(contactsOut, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			return fmt.Errorf("独占创建联系人输出失败，不覆盖或改名重试: %w", err)
		}
		defer func() { runErr = errors.Join(runErr, contactFile.Close()) }()
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
		Duration: duration - shutdown, ShutdownTimeout: shutdown, DiscoveryTimeout: 10 * time.Second, HandshakeTimeout: 15 * time.Second, Contacts: contacts, Chat: chat})
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
	if contactFile != nil {
		if contact, ok := client.Contact(); ok {
			err = errors.Join(err, saveContacts(contactFile, id, contacts, contact))
		} else {
			err = errors.Join(err, fmt.Errorf("未完成联系人身份/握手确认，保留联系人输出但不写入未验证资料"))
		}
	}
	chatSends := 0
	if result.Chat != nil && result.Chat.Submitted {
		chatSends = 1
	}
	if resultErr := record(map[string]any{"event": "result", "result": result, "error": errorText(err), "ui_observation": "unknown", "chat_sends": chatSends}); resultErr != nil {
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
