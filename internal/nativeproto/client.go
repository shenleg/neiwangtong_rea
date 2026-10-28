// 本文件驱动单对端发现、ENet与应用握手；支持限时实验或常驻在线，并由同一调度循环串行发送聊天。
package nativeproto

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"sync/atomic"
	"time"
)

// ClientConfig 明确本机公告、资料、唯一发现对端与时限；Duration=0常驻，正值包含发现/握手，ShutdownTimeout另用于退出。
// Profile 是调用者的本机资料，不会用远端或已有 Windows 身份补齐；RemoteDiscovery 不允许广播/扫描。
type ClientConfig struct {
	Node             NodePacket
	Profile          Handshake
	RemoteDiscovery  netip.AddrPort
	Transport        ENetConfig
	Duration         time.Duration
	ShutdownTimeout  time.Duration
	DiscoveryTimeout time.Duration
	HandshakeTimeout time.Duration
	Contacts         *Directory                  // 单聊时必须提供属于本身份的联系人目录；启用LearnContacts或只上线时可为nil。
	LearnContacts    bool                        // 显式允许双向READY后学习当前已验证对端；默认保留目录必需语义。
	ObserveDirectory func(*Directory) error      // 可选；串行持久化学习后的目录，成功才发布，不能在回调中同步发送。
	Chat             *ChatRequest                // nil 只上线；非 nil 在就绪后向选定联系人提交一条消息，不自动重试。
	GroupChat        bool                        // 显式启用群同步和3011；不自动入群或建立额外对端连接。
	ObserveGroup     func(GroupInfo) error       // 可选；串行保存本身份群同步快照，出错即停止，不应在回调中同步发送。
	ReceiveMessage   func(IncomingMessage) error // 可选；串行接收正文，回调错误终止运行，不可在回调中同步发送。
}

// ClientEvent 是非正文的运行事实；不包含远端姓名、UserID、群信息或聊天内容。
type ClientEvent struct {
	Stage           string    `json:"stage"`
	Command         uint32    `json:"command,omitempty"`
	Role            string    `json:"role,omitempty"`
	State           PeerState `json:"state,omitempty"`
	ElapsedMS       int64     `json:"elapsed_ms"`
	MessageVersions *string   `json:"message_versions,omitempty"`
	DisconnectData  *uint32   `json:"disconnect_data,omitempty"`
	MessageID       string    `json:"message_id,omitempty"`
	PeerIndex       uint16    `json:"peer_index,omitempty"` // Host内部连接序号，不是对端身份或名称。
	Error           string    `json:"error,omitempty"`      // 明确运行错误，不含聊天正文。
}

// ClientResult 区分运行就绪与正常退出；Client的ProtocolReady表示双向1000/1018，Host表示已准备接收发现。
// 各身份真实就绪状态见Snapshot.OnlineUserIDs；这两种就绪都不表示原客户端UI已独立确认。
type ClientResult struct {
	ProtocolReady     bool           `json:"protocol_ready"`
	GracefulExit      bool           `json:"graceful_exit"`
	Role              string         `json:"role"`
	DiscoveryAttempts int            `json:"discovery_attempts"`
	DiscoverySends    int            `json:"discovery_sends"`
	UnhandledCommands map[uint32]int `json:"unhandled_commands"`
	Chat              *ChatResult    `json:"chat,omitempty"` // 最近一次聊天快照，完整事件由observe保存。
	ChatSends         int            `json:"chat_sends"`
}

// Client 拥有一次单对端运行；构造不发包，Run接管socket，SendUser/SendGroup交给Run串行处理，不并发操作UDP。
type Client struct {
	config               ClientConfig
	discovery            packetConn
	conn                 packetConn
	handshake            []byte
	localMessageVersions string
	lifecycle            atomic.Uint32
	directory            atomic.Pointer[Directory]
	snapshot             atomic.Pointer[ClientSnapshot]
	requests             chan chatCall
	done                 chan struct{}
	chats                map[string]*ChatResult
	groups               map[string]GroupInfo
	groupPending         map[string]bool
	incoming             *incomingCache
	incomingGroup        func(string, string) bool
	incomingAcks         []uint64
	chatOrder            []string
	chatCursor           int
	origin               time.Time
	observe              func(ClientEvent) error
	transport            *UDPTransport
	peer                 *Peer
	remoteNode           NodePacket
	chat                 *preparedChat
	contact              *Contact
	result               ClientResult
}

// NewClient 验证 caller 已绑定的两个独立 socket、节点源地址/端口、资料和预算，返回尚未发包的实例。
// 验证失败不关闭传入 socket；成功构造到 Run 开始前仍由调用者保管。无法匹配源地址/端口或广播目标时报错。
func NewClient(discovery, conn *net.UDPConn, config ClientConfig) (*Client, error) {
	if discovery == nil || conn == nil {
		return nil, fmt.Errorf("客户端需要两个明确绑定、未连接且互不相同的 UDP socket")
	}
	return newClient(discovery, conn, config)
}

// newClient 复用单对端协议循环；Host传入独立虚拟端点时仍验证其公告地址和端口。
func newClient(discovery, conn packetConn, config ClientConfig) (*Client, error) {
	if discovery == conn || discovery.RemoteAddr() != nil || conn.RemoteAddr() != nil {
		return nil, fmt.Errorf("客户端需要两个明确绑定、未连接且互不相同的 UDP socket")
	}
	remote := config.RemoteDiscovery
	if !remote.IsValid() || remote.Port() == 0 || !remote.Addr().Is4() || remote.Addr().IsUnspecified() || remote.Addr().IsMulticast() || remote.Addr() == netip.AddrFrom4([4]byte{255, 255, 255, 255}) {
		return nil, fmt.Errorf("只允许明确指定的单个 IPv4 发现对端")
	}
	if config.Node.Type != 1 || config.Duration < 0 || config.ShutdownTimeout <= 0 || config.DiscoveryTimeout <= 0 || config.HandshakeTimeout <= 0 {
		return nil, fmt.Errorf("客户端节点类型或显式时限无效")
	}
	wire, err := config.Node.Encode()
	if err != nil {
		return nil, err
	}
	var address [4]byte
	copy(address[:], wire[16:20])
	local := netip.AddrFrom4(address)
	discoveryAddr := discovery.LocalAddr().(*net.UDPAddr).AddrPort()
	peerAddr := conn.LocalAddr().(*net.UDPAddr).AddrPort()
	if discoveryAddr.Addr().Unmap() != local || peerAddr.Addr().Unmap() != local || discoveryAddr.Port() != config.Node.DiscoveryPort || peerAddr.Port() != config.Node.ENetPort || config.Node.TCPPort != 0 || config.Node.ReverseENetPort != 0 {
		return nil, fmt.Errorf("公告须匹配两个 socket 的实际 IPv4/端口，且未实现的 TCP/反向 listener 须不公布")
	}
	if _, err := NewENetRuntime(config.Transport); err != nil {
		return nil, err
	}
	if err := validateClientProfile(config.Profile); err != nil {
		return nil, err
	}
	frame, err := EncodeHandshake(config.Profile)
	if err != nil {
		return nil, err
	}
	handshake, err := Encode(frame.Command, frame.Payload)
	if err != nil {
		return nil, err
	}
	if uint64(len(handshake)) > uint64(config.Transport.MaxMessageBytes) || uint64(len(wire)) > uint64(config.Transport.MaxMessageBytes) {
		return nil, fmt.Errorf("客户端握手或节点包超过传输预算")
	}
	versions := ""
	for _, field := range config.Profile.Features {
		if field.Name == "MSG" {
			versions = field.Value
			break
		}
	}
	if config.GroupChat {
		for _, name := range []string{"QGROUP", "QGROUP_MSG"} {
			supported := false
			for _, field := range config.Profile.Features {
				if field.Name == name && supportsTextContent(field.Value) {
					supported = true
					break
				}
			}
			if !supported {
				return nil, fmt.Errorf("启用群同步与发送须声明兼容的%s版本", name)
			}
		}
	}
	if config.Contacts != nil && config.Contacts.OwnUserID() != config.Node.UserID {
		return nil, fmt.Errorf("联系人列表不属于当前独立身份")
	}
	var chat *preparedChat
	var chatResult *ChatResult
	if config.Chat != nil {
		if config.Chat.GroupID != "" {
			return nil, fmt.Errorf("群聊须在同步成员资料后调用SendGroup，不接受启动Chat配置")
		}
		if !supportsTextContent(versions) {
			return nil, fmt.Errorf("本机MSG声明不支持当前单聊正文版本")
		}
		if !config.LearnContacts {
			if config.Contacts == nil {
				return nil, fmt.Errorf("单聊需要当前独立身份自己的联系人列表")
			}
			contact, err := config.Contacts.ContactByID(config.Chat.UserID)
			if err != nil {
				return nil, err
			}
			if contact.Discovery != config.RemoteDiscovery {
				return nil, fmt.Errorf("选定联系人的发现端点与运行目标不一致")
			}
		}
		chat, chatResult, err = prepareChat(*config.Chat, config.Transport.MaxMessageBytes)
		if err != nil {
			return nil, err
		}
	}
	client := &Client{config: config, discovery: discovery, conn: conn, handshake: handshake, localMessageVersions: versions, chat: chat,
		requests: make(chan chatCall), done: make(chan struct{}), chats: make(map[string]*ChatResult), groups: make(map[string]GroupInfo), groupPending: make(map[string]bool), incoming: &incomingCache{},
		result: ClientResult{UnhandledCommands: make(map[uint32]int), Chat: chatResult}}
	if config.Contacts != nil {
		client.directory.Store(&Directory{ownID: config.Node.UserID, contacts: config.Contacts.ListContacts()})
	}
	client.publishSnapshot()
	return client, nil
}

// Run 只向明确对端发现一次并交换握手；Duration=0时持续到ctx取消、对端退出或协议失败，正值保留限时实验。
// observe串行保存阶段和逐条消息事件，返回错误即停止；可在其他协程调用SendUser/SendGroup，不可在回调中同步发送。
// ctx主动取消时正常发送1017并断开；已提交中的发送被取消仍返回未知结果错误，不重连或重发；退出后socket关闭。
func (c *Client) Run(ctx context.Context, observe func(ClientEvent) error) (result ClientResult, runErr error) {
	if !c.lifecycle.CompareAndSwap(clientIdle, clientRunning) {
		return ClientResult{}, fmt.Errorf("客户端运行不能重用")
	}
	c.origin = time.Now()
	c.observe = observe
	var activeEnd time.Time
	runCtx, cancel := context.WithCancel(ctx)
	if c.config.Duration > 0 {
		activeEnd = c.origin.Add(c.config.Duration)
		cancel()
		runCtx, cancel = context.WithDeadline(ctx, activeEnd.Add(c.config.ShutdownTimeout))
	}
	defer cancel()
	defer func() {
		c.lifecycle.Store(clientStopped)
		c.publishSnapshot()
		defer close(c.done)
		if ctx.Err() == context.Canceled && runErr == context.Canceled && c.result.ProtocolReady && c.transport.failed == nil {
			runErr = nil // 空闲等待被调用方停止时执行正常退出；已提交失败仍由transport.failed保留。
		}
		shutdownBy := time.Now().Add(c.config.ShutdownTimeout)
		if !activeEnd.IsZero() {
			shutdownBy = minTime(shutdownBy, activeEnd.Add(c.config.ShutdownTimeout))
		}
		shutdownCtx, stop := context.WithDeadline(context.Background(), shutdownBy)
		defer stop()
		if c.transport != nil {
			runErr = errors.Join(runErr, c.drainIncoming())
			runErr = errors.Join(runErr, c.flushIncomingAcks(shutdownCtx))
			quitOK := c.peer != nil && c.peer.Status().RemoteQuit
			if c.result.ProtocolReady && !c.peer.Status().RemoteQuit && c.transport.failed == nil && c.transport.runtime.Status().State == ENetConnected {
				if err := c.sendFrame(shutdownCtx, QuitFrame()); err != nil {
					runErr = errors.Join(runErr, err)
				} else {
					quitOK = true
				}
			}
			closeErr := c.transport.Close(shutdownCtx)
			runErr = errors.Join(runErr, c.drainIncoming())
			if closeErr == nil {
				closeErr = c.peer.Disconnect(time.Since(c.origin))
			}
			c.result.GracefulExit = closeErr == nil && c.result.ProtocolReady && quitOK
			runErr = errors.Join(runErr, closeErr)
		} else {
			runErr = errors.Join(runErr, c.conn.Close())
		}
		runErr = errors.Join(runErr, c.discovery.Close())
		if observe != nil {
			runErr = errors.Join(runErr, c.emit("stopped", 0))
		}
		result = c.result
	}()
	if observe == nil {
		return c.result, fmt.Errorf("客户端运行须显式提供阶段记录回调")
	}
	if err := c.emit("initialized", 0); err != nil {
		return c.result, err
	}
	if err := c.emitVersions("local_message_versions", c.localMessageVersions); err != nil {
		return c.result, err
	}
	remote, err := c.discover(runCtx)
	if err != nil {
		return c.result, err
	}
	c.remoteNode = remote
	if c.chat != nil && c.chat.userID != remote.UserID {
		return c.result, fmt.Errorf("发现的对端身份与明确聊天目标不一致，不建立聊天连接")
	}
	order, err := c.config.Node.SessionOrder(remote)
	if err != nil {
		return c.result, err
	}
	if order == 0 || remote.UserID == c.config.Node.UserID {
		return c.result, fmt.Errorf("发现对端与本机会话/身份冲突")
	}
	if remote.ENetPort == 0 {
		return c.result, fmt.Errorf("对端未公布 ENet 主端口，不回退 TCP")
	}
	if !sharedNodeFeature(c.config.Node, remote) {
		return c.result, fmt.Errorf("节点产品 FEATURE 不匹配，不尝试其他目标")
	}
	if remote.Type == 1 {
		if err := c.replyDiscovery(c.config.RemoteDiscovery); err != nil {
			return c.result, err
		}
	}
	c.peer, err = NewPeer(c.config.Node.UserID, remote.UserID, c.config.Transport.MaxMessageBytes)
	if err != nil {
		return c.result, err
	}
	c.transport, err = newUDPTransport(c.conn, netip.AddrPortFrom(c.config.RemoteDiscovery.Addr(), remote.ENetPort), c.config.Transport)
	if err != nil {
		return c.result, err
	}
	connectionCtx, stop := context.WithDeadline(runCtx, clientDeadline(activeEnd, c.config.HandshakeTimeout))
	if order > 0 {
		c.result.Role = "active"
		err = c.transport.Connect(connectionCtx)
	} else {
		c.result.Role = "passive"
		err = c.transport.Accept(connectionCtx)
	}
	stop()
	if err != nil {
		return c.result, err
	}
	if err := c.emit("enet_connected", 0); err != nil {
		return c.result, err
	}
	if order > 0 {
		node := c.config.Node
		node.Type = 4
		wire, err := node.Encode()
		if err != nil {
			return c.result, err
		}
		if err := c.send(runCtx, wire, 4); err != nil {
			return c.result, err
		}
		if err := c.send(runCtx, c.handshake, CommandHandshake); err != nil {
			return c.result, err
		}
	}
	readyBy := clientDeadline(activeEnd, c.config.HandshakeTimeout)
	for activeEnd.IsZero() || time.Now().Before(activeEnd) {
		if err := runCtx.Err(); err != nil {
			return c.result, err
		}
		if !c.result.ProtocolReady && !time.Now().Before(readyBy) {
			return c.result, fmt.Errorf("双向应用握手/READY 超时")
		}
		if err := c.serviceDiscovery(); err != nil {
			return c.result, err
		}
		wait := 50 * time.Millisecond // 同一循环同时处理网络和请求；限制空闲提交延迟，不并发读取UDP。
		if !activeEnd.IsZero() {
			wait = min(wait, time.Until(activeEnd))
			if wait <= 0 {
				break
			}
		}
		wire, received, err := c.transport.poll(runCtx, wait)
		if err != nil {
			status := c.transport.runtime.Status()
			if status.RemoteDisconnect {
				c.lifecycle.Store(clientStopped)
				value := status.DisconnectData
				if eventErr := c.observeEvent(ClientEvent{Stage: "peer_disconnected", Role: c.result.Role, State: c.peer.Status().State, ElapsedMS: time.Since(c.origin).Milliseconds(), DisconnectData: &value}); eventErr != nil {
					return c.result, errors.Join(err, eventErr)
				}
			}
			if errors.Is(err, io.EOF) && c.peer.Status().RemoteQuit {
				return c.result, nil
			}
			return c.result, err
		}
		if received {
			if err := c.receive(runCtx, wire); err != nil {
				return c.result, err
			}
		}
		if c.peer.Status().RemoteQuit {
			return c.result, nil
		}
		if c.peer.Status().State == PeerOnline && !c.result.ProtocolReady {
			if err := c.learnDirectory(); err != nil {
				return c.result, err
			}
			c.result.ProtocolReady = true
			c.lifecycle.Store(clientOnline)
			if err := c.emit("protocol_ready", 0); err != nil {
				return c.result, err
			}
			if c.chat != nil {
				if err := c.sendChat(runCtx); err != nil {
					return c.result, err
				}
			}
		}
		if c.result.ProtocolReady {
			if err := c.flushIncomingAcks(runCtx); err != nil {
				return c.result, err
			}
			timers, err := c.peer.Timers(time.Since(c.origin))
			if err != nil {
				return c.result, err
			}
			if timers.TimedOut {
				return c.result, fmt.Errorf("应用心跳接收超时")
			}
			if timers.HeartbeatDue {
				if err := c.sendFrame(runCtx, HeartbeatFrame()); err != nil {
					return c.result, err
				}
			}
			select {
			case call := <-c.requests:
				if err := c.handleChatCall(runCtx, call); err != nil {
					return c.result, err
				}
			default:
			}
		}
	}
	if !c.result.ProtocolReady {
		return c.result, fmt.Errorf("运行时间内未完成应用就绪")
	}
	return c.result, nil
}

func (c *Client) sendFrame(ctx context.Context, frame Frame) error {
	wire, err := Encode(frame.Command, frame.Payload)
	if err != nil {
		return err
	}
	return c.send(ctx, wire, frame.Command)
}

func (c *Client) send(ctx context.Context, wire []byte, command uint32) error {
	if err := c.emit("send_planned", command); err != nil {
		return err
	}
	sendErr := c.transport.Send(ctx, wire)
	if command == CommandQuit || sendErr != nil && c.peer.Status().State == PeerOnline {
		sendErr = errors.Join(sendErr, c.drainIncoming())
	}
	if sendErr != nil {
		return fmt.Errorf("控制消息 %d 的传输结果未确认: %w", command, sendErr)
	}
	if err := c.peer.Observe(true, wire, time.Since(c.origin)); err != nil {
		return err
	}
	return c.emit("transport_acknowledged", command)
}

func (c *Client) receive(ctx context.Context, wire []byte) error {
	if len(wire) == nodePacketSize && nodeTypeSupported(binary.BigEndian.Uint32(wire[4:])) {
		if err := c.peer.Observe(false, wire, time.Since(c.origin)); err != nil {
			return err
		}
		return c.emit("node_control_received", 4)
	}
	reader := bytes.NewReader(wire)
	frame, err := Decode(reader, c.config.Transport.MaxMessageBytes)
	if err != nil {
		return err
	}
	if reader.Len() != 0 {
		return fmt.Errorf("应用数据单元含多余字节")
	}
	switch frame.Command {
	case CommandHandshake, CommandReady, CommandHeartbeat, CommandQuit, CommandGroupInfoResponse:
		if err := c.peer.Observe(false, wire, time.Since(c.origin)); err != nil {
			return err
		}
	case CommandUserMessage, CommandGroupMessage:
		if c.config.ReceiveMessage != nil {
			return c.receiveIncoming(wire, frame)
		}
		c.result.UnhandledCommands[frame.Command]++
		return c.emit("unsupported_business_command", frame.Command)
	case CommandMessageAck, CommandMessageReceipt:
		return c.receiveChatAck(wire, frame)
	case CommandGroupInfoPush, CommandGroupUserPush, CommandGroupUserResponse:
		if c.config.GroupChat {
			if frame.Command == CommandGroupUserResponse {
				return c.receiveGroupUserResponse(wire, frame)
			}
			return c.receiveGroupPush(ctx, wire, frame)
		}
		c.result.UnhandledCommands[frame.Command]++
		return c.emit("unsupported_business_command", frame.Command)
	case CommandGroupInfoRequest:
		if c.config.GroupChat {
			if err := c.peer.Observe(false, wire, time.Since(c.origin)); err != nil {
				return err
			}
			groupID, err := DecodeGroupInfoRequest(frame)
			if err != nil {
				return err
			}
			// 本机没有群资料时不伪造3005；正常入站请求提供原始ID，再向同一对端请求成员。
			if _, ok := c.groups[groupID]; ok {
				return c.emit("group_info_request_received", frame.Command)
			}
			return c.requestGroupUsers(ctx, groupID)
		}
		c.result.UnhandledCommands[frame.Command]++
		return c.emit("unsupported_business_command", frame.Command)
	default:
		c.result.UnhandledCommands[frame.Command]++
		return c.emit("unsupported_business_command", frame.Command)
	}
	if err := c.emit("control_received", frame.Command); err != nil {
		return err
	}
	if frame.Command == CommandHandshake {
		profile, err := DecodeHandshake(frame)
		if err != nil {
			return err
		}
		for _, field := range profile.Info {
			if field.Name == "NAME" && field.Value != "" {
				c.contact = &Contact{UserID: c.remoteNode.UserID, Name: field.Value, Discovery: c.config.RemoteDiscovery}
				break
			}
		}
		if err := c.emitVersions("remote_message_versions", c.peer.messageVersions[0]); err != nil {
			return err
		}
		if !c.peer.Status().LocalHandshake {
			if err := c.send(ctx, c.handshake, CommandHandshake); err != nil {
				return err
			}
		}
		if !c.peer.Status().LocalReady {
			ready, err := EncodeReady(Ready{UserID: c.config.Node.UserID})
			if err != nil {
				return err
			}
			return c.sendFrame(ctx, ready)
		}
	}
	return nil
}

func (c *Client) emit(stage string, command uint32) error {
	state := PeerAwaitingNode
	if c.peer != nil {
		state = c.peer.Status().State
	}
	event := ClientEvent{Stage: stage, Command: command, Role: c.result.Role, State: state, ElapsedMS: time.Since(c.origin).Milliseconds()}
	if c.result.Chat != nil && command == c.result.Chat.Command {
		event.MessageID = c.result.Chat.MessageID
	}
	return c.observeEvent(event)
}

func (c *Client) emitVersions(stage string, versions string) error {
	state := PeerAwaitingNode
	if c.peer != nil {
		state = c.peer.Status().State
	}
	return c.observeEvent(ClientEvent{Stage: stage, Role: c.result.Role, State: state, ElapsedMS: time.Since(c.origin).Milliseconds(), MessageVersions: &versions})
}

func clientDeadline(activeEnd time.Time, timeout time.Duration) time.Time {
	deadline := time.Now().Add(timeout)
	if activeEnd.IsZero() {
		return deadline
	}
	return minTime(activeEnd, deadline)
}

func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}
