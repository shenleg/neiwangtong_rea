// 本文件将单对端发现、ENet、节点控制与应用握手串成限时新节点运行，不实现聊天或文件业务。
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
	"time"
)

// ClientConfig 明确本机公告、资料、唯一发现对端与时限；Duration 包含发现和握手，ShutdownTimeout 另用于退出。
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
}

// ClientResult 区分协议就绪与正常退出；ProtocolReady 只表示双向 1000/1018，不表示原客户端 UI 已独立确认。
type ClientResult struct {
	ProtocolReady     bool           `json:"protocol_ready"`
	GracefulExit      bool           `json:"graceful_exit"`
	Role              string         `json:"role"`
	DiscoveryAttempts int            `json:"discovery_attempts"`
	DiscoverySends    int            `json:"discovery_sends"`
	UnhandledCommands map[uint32]int `json:"unhandled_commands"`
}

// Client 拥有一次限时单对端运行；构造不发包，Run 开始后接管两个 socket 的关闭责任，不允许重用。
type Client struct {
	config               ClientConfig
	discovery            *net.UDPConn
	conn                 *net.UDPConn
	handshake            []byte
	localMessageVersions string
	started              bool
	origin               time.Time
	observe              func(ClientEvent) error
	transport            *UDPTransport
	peer                 *Peer
	remoteNode           NodePacket
	result               ClientResult
}

// NewClient 验证 caller 已绑定的两个独立 socket、节点源地址/端口、资料和预算，返回尚未发包的实例。
// 验证失败不关闭传入 socket；成功构造到 Run 开始前仍由调用者保管。无法匹配源地址/端口或广播目标时报错。
func NewClient(discovery, conn *net.UDPConn, config ClientConfig) (*Client, error) {
	if discovery == nil || conn == nil || discovery == conn || discovery.RemoteAddr() != nil || conn.RemoteAddr() != nil {
		return nil, fmt.Errorf("客户端需要两个明确绑定、未连接且互不相同的 UDP socket")
	}
	remote := config.RemoteDiscovery
	if !remote.IsValid() || remote.Port() == 0 || !remote.Addr().Is4() || remote.Addr().IsUnspecified() || remote.Addr().IsMulticast() || remote.Addr() == netip.AddrFrom4([4]byte{255, 255, 255, 255}) {
		return nil, fmt.Errorf("只允许明确指定的单个 IPv4 发现对端")
	}
	if config.Node.Type != 1 || config.Duration <= 0 || config.ShutdownTimeout <= 0 || config.DiscoveryTimeout <= 0 || config.HandshakeTimeout <= 0 {
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
	return &Client{config: config, discovery: discovery, conn: conn, handshake: handshake, localMessageVersions: versions,
		result: ClientResult{UnhandledCommands: make(map[uint32]int)}}, nil
}

// Run 在 ctx 与配置时限内只向明确对端发现一次，按双方会话 GUID 选择主动/被动并完成应用交换。
// observe 必须记录每个阶段且返回错误时停止；所有错误均不重连、不换身份或重发应用请求。
// 返回后 socket 均关闭；控制报文只证明协议运行，UI 在线验收由外部操作者另行确认。
func (c *Client) Run(ctx context.Context, observe func(ClientEvent) error) (result ClientResult, runErr error) {
	if c.started {
		return c.result, fmt.Errorf("客户端运行不能重用")
	}
	c.started = true
	c.origin = time.Now()
	c.observe = observe
	hardEnd := c.origin.Add(c.config.Duration).Add(c.config.ShutdownTimeout)
	runCtx, cancel := context.WithDeadline(ctx, hardEnd)
	defer cancel()
	defer func() {
		shutdownCtx, stop := context.WithDeadline(context.Background(), minTime(hardEnd, time.Now().Add(c.config.ShutdownTimeout)))
		defer stop()
		if c.transport != nil {
			quitOK := c.peer != nil && c.peer.Status().RemoteQuit
			if c.result.ProtocolReady && !c.peer.Status().RemoteQuit && c.transport.failed == nil && c.transport.runtime.Status().State == ENetConnected {
				if err := c.sendFrame(shutdownCtx, QuitFrame()); err != nil {
					runErr = errors.Join(runErr, err)
				} else {
					quitOK = true
				}
			}
			closeErr := c.transport.Close(shutdownCtx)
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
	c.transport, err = NewUDPTransport(c.conn, netip.AddrPortFrom(c.config.RemoteDiscovery.Addr(), remote.ENetPort), c.config.Transport)
	if err != nil {
		return c.result, err
	}
	connectionCtx, stop := context.WithDeadline(runCtx, minTime(c.origin.Add(c.config.Duration), time.Now().Add(c.config.HandshakeTimeout)))
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
	readyBy := minTime(c.origin.Add(c.config.Duration), time.Now().Add(c.config.HandshakeTimeout))
	activeEnd := c.origin.Add(c.config.Duration)
	for time.Now().Before(activeEnd) {
		if err := runCtx.Err(); err != nil {
			return c.result, err
		}
		if !c.result.ProtocolReady && !time.Now().Before(readyBy) {
			return c.result, fmt.Errorf("双向应用握手/READY 超时")
		}
		if err := c.serviceDiscovery(); err != nil {
			return c.result, err
		}
		remaining := time.Until(activeEnd)
		if remaining <= 0 {
			break
		}
		wire, received, err := c.transport.poll(runCtx, min(200*time.Millisecond, remaining))
		if err != nil {
			status := c.transport.runtime.Status()
			if status.RemoteDisconnect {
				value := status.DisconnectData
				if eventErr := c.observe(ClientEvent{Stage: "peer_disconnected", Role: c.result.Role, State: c.peer.Status().State, ElapsedMS: time.Since(c.origin).Milliseconds(), DisconnectData: &value}); eventErr != nil {
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
			c.result.ProtocolReady = true
			if err := c.emit("protocol_ready", 0); err != nil {
				return c.result, err
			}
		}
		if c.result.ProtocolReady {
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
	if err := c.transport.Send(ctx, wire); err != nil {
		return fmt.Errorf("控制消息 %d 的传输结果未确认: %w", command, err)
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
	case CommandGroupInfoRequest:
		// 上线实验没有本机群资料，不伪造成功响应；只记录命令号，不保存群标识或正文。
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
	return c.observe(ClientEvent{Stage: stage, Command: command, Role: c.result.Role, State: state, ElapsedMS: time.Since(c.origin).Milliseconds()})
}

func (c *Client) emitVersions(stage string, versions string) error {
	state := PeerAwaitingNode
	if c.peer != nil {
		state = c.peer.Status().State
	}
	return c.observe(ClientEvent{Stage: stage, Role: c.result.Role, State: state, ElapsedMS: time.Since(c.origin).Milliseconds(), MessageVersions: &versions})
}

func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}
