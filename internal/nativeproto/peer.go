// 本文件跟踪单个对端的离线上线/退出时序和心跳计时；不认证身份、不打开网络，也不自动提交消息。
package nativeproto

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"time"
)

// PeerState 表示本次会话已观察到的协议阶段；online 不是认证成功或独立接收确认。
type PeerState string

const (
	// PeerAwaitingNode 表示尚未观察到可靠连接中的 type 4 节点包。
	PeerAwaitingNode PeerState = "awaiting_node"
	// PeerHandshaking 表示已有节点包，双向 1000 尚未齐全。
	PeerHandshaking PeerState = "handshaking"
	// PeerReadying 表示双向 1000 已观察到，双向 1018 尚未齐全。
	PeerReadying PeerState = "readying"
	// PeerOnline 表示双向 1000/1018 交换齐全，不表示账号真实性已验证。
	PeerOnline PeerState = "online"
	// PeerClosing 表示任一方向已发送 1017；还需外部传输实现处理 ACK 和断开。
	PeerClosing PeerState = "closing"
	// PeerClosed 表示调用者已报告传输连接关闭。
	PeerClosed PeerState = "closed"
)

// PeerStatus 不含用户标识或资料，只报告本次会话的双向交换事实。
type PeerStatus struct {
	State           PeerState `json:"state"`
	NodeSeen        bool      `json:"node_seen"`
	LocalHandshake  bool      `json:"local_handshake"`
	RemoteHandshake bool      `json:"remote_handshake"`
	LocalReady      bool      `json:"local_ready"`
	RemoteReady     bool      `json:"remote_ready"`
	LocalQuit       bool      `json:"local_quit"`
	RemoteQuit      bool      `json:"remote_quit"`
}

// PeerTimers 是对显式观察时间的判断，不会发送心跳、断开连接或安排后台重试。
type PeerTimers struct {
	HeartbeatDue bool `json:"heartbeat_due"`
	TimedOut     bool `json:"timed_out"`
}

// Peer 跟踪调用者明确选定的一个对端；串行使用，重新建连须创建新实例。
// Observe 的方向必须由调用者按端点提供，不能从 XML UserID 自动挑选目标。
type Peer struct {
	localID              string
	remoteID             string
	maxFrameBytes        uint32
	status               PeerStatus
	handshakes           [2]string
	messageVersions      [2]string
	groupMessageVersions [2]string
	lastAt               time.Duration
	lastInbound          time.Duration
	lastOutbound         time.Duration
}

// NewPeer 使用显式本机/对端标识和应用帧读取预算创建空的离线会话。
// 身份不适合已验证节点槽、两端标识相同或预算不足时返回错误；不读取客户端身份，不生成凭据。
func NewPeer(localUserID, remoteUserID string, maxFrameBytes uint32) (*Peer, error) {
	for _, id := range []string{localUserID, remoteUserID} {
		if err := validateMessageID(id); err != nil {
			return nil, err
		}
		if len(id) > 64 {
			return nil, fmt.Errorf("会话身份超出节点包 64 字节槽预算")
		}
	}
	if localUserID == remoteUserID || maxFrameBytes <= headerSize {
		return nil, fmt.Errorf("会话两端须有不同标识，且应用帧预算须大于 8")
	}
	return &Peer{localID: localUserID, remoteID: remoteUserID, maxFrameBytes: maxFrameBytes,
		status: PeerStatus{State: PeerAwaitingNode}}, nil
}

// Observe 记录一条完整、已由 ENet 解包/重组的消息；outbound=true 表示本机发出，at 为会话起点后的单调时间。
// 依已验证控制时序限制初始顺序：type 4、双向1000、双向1018，再接受单聊/群聊/回执、心跳、群资料及QUIT。
// 截断、尾随字节、身份冲突、时间倒退或未支持的消息/顺序返回错误且不推进状态。
// 本方法只记录发送事实，不实际发送，也不把 ACK 或状态 online 转成 recipient_observed。
func (p *Peer) Observe(outbound bool, data []byte, at time.Duration) error {
	if at < p.lastAt || p.status.State == PeerClosed {
		return fmt.Errorf("观察时间倒退或会话已关闭")
	}
	if len(data) == nodePacketSize && nodeTypeSupported(binary.BigEndian.Uint32(data[4:])) {
		node, err := DecodeNodePacket(data)
		if err != nil {
			return err
		}
		if node.Type != 4 || p.status.State == PeerClosing {
			return fmt.Errorf("会话内只接受未关闭连接的 type 4 节点包")
		}
		if node.UserID != p.senderID(outbound) {
			return fmt.Errorf("节点身份与显式会话发送者不一致")
		}
		p.status.NodeSeen = true
		p.progress()
		p.recordTime(outbound, at)
		return nil
	}
	reader := bytes.NewReader(data)
	frame, err := Decode(reader, p.maxFrameBytes)
	if err != nil {
		return err
	}
	if reader.Len() != 0 {
		return fmt.Errorf("会话输入必须恰好一条应用帧")
	}
	if err := p.observeFrame(outbound, frame); err != nil {
		return err
	}
	p.progress()
	p.recordTime(outbound, at)
	return nil
}

// Status 返回不含资料的当前观察快照；无输入且不会失败，不证明对端已处理业务。
func (p *Peer) Status() PeerStatus { return p.status }

// Timers 在 at 判断 60 秒发送检查和 120 秒接收超时，仅 online 阶段产生 true。
// 时间倒退时报错且不改状态；到期也不自动发送或断开，调用者须按运行授权处理。
func (p *Peer) Timers(at time.Duration) (PeerTimers, error) {
	if at < p.lastAt {
		return PeerTimers{}, fmt.Errorf("会话计时时间倒退")
	}
	p.lastAt = at
	if p.status.State != PeerOnline {
		return PeerTimers{}, nil
	}
	return PeerTimers{HeartbeatDue: at-p.lastOutbound >= 60*time.Second, TimedOut: at-p.lastInbound >= 120*time.Second}, nil
}

// Disconnect 记录外部传输关闭，可发生在 QUIT 之后或异常断开时；不自行生成 1017/ACK。
// 时间倒退时报错；重复关闭可用，重新上线必须新建 Peer，不能复用旧交换状态。
func (p *Peer) Disconnect(at time.Duration) error {
	if at < p.lastAt {
		return fmt.Errorf("断开时间倒退")
	}
	p.lastAt = at
	p.status.State = PeerClosed
	return nil
}

func (p *Peer) observeFrame(outbound bool, frame Frame) error {
	if p.status.State == PeerClosing {
		if frame.Command != CommandQuit {
			return fmt.Errorf("QUIT 后不再接受其他应用消息")
		}
	}
	switch frame.Command {
	case CommandHandshake:
		if !p.status.NodeSeen {
			return fmt.Errorf("1000 前尚未观察到 type 4 节点包")
		}
		profile, err := DecodeHandshake(frame)
		if err != nil {
			return err
		}
		index := 0
		if outbound {
			index = 1
		}
		if previous := p.handshakes[index]; previous != "" && previous != frame.Payload {
			return fmt.Errorf("同一会话的初始握手变化，须显式重建会话")
		}
		for _, field := range profile.Features {
			switch field.Name {
			case "MSG":
				p.messageVersions[index] = field.Value
			case "QGROUP_MSG":
				p.groupMessageVersions[index] = field.Value
			}
		}
		p.handshakes[index] = frame.Payload
		p.status.LocalHandshake = p.handshakes[1] != ""
		p.status.RemoteHandshake = p.handshakes[0] != ""
	case CommandReady:
		if !p.status.LocalHandshake || !p.status.RemoteHandshake {
			return fmt.Errorf("1018 前双向 1000 尚未齐全")
		}
		ready, err := DecodeReady(frame)
		if err != nil {
			return err
		}
		if ready.UserID != p.senderID(outbound) {
			return fmt.Errorf("READY 身份与显式会话发送者不一致")
		}
		if outbound {
			p.status.LocalReady = true
		} else {
			p.status.RemoteReady = true
		}
	case CommandUserMessage, CommandGroupMessage, CommandMessageAck, CommandMessageReceipt, CommandHeartbeat, CommandQuit, CommandGroupInfoPush, CommandGroupUserPush, CommandGroupInfoRequest, CommandGroupInfoResponse, CommandGroupUserRequest, CommandGroupUserResponse:
		if p.status.State != PeerOnline && p.status.State != PeerClosing {
			return fmt.Errorf("命令 %d 前双向 READY 尚未齐全", frame.Command)
		}
		switch frame.Command {
		case CommandUserMessage:
			if _, err := DecodeUserMessage(frame); err != nil {
				return err
			}
		case CommandGroupMessage:
			if _, err := DecodeGroupMessage(frame); err != nil {
				return err
			}
		case CommandMessageAck, CommandMessageReceipt:
			if _, err := DecodeMessageAck(frame); err != nil {
				return err
			}
		case CommandHeartbeat, CommandQuit:
			if err := DecodeControl(frame); err != nil {
				return err
			}
		case CommandGroupInfoPush, CommandGroupUserPush:
			if _, err := DecodeGroupPush(frame); err != nil {
				return err
			}
		case CommandGroupUserResponse:
			if _, err := DecodeGroupUserResponse(frame); err != nil {
				return err
			}
		case CommandGroupUserRequest:
			if _, err := DecodeGroupUserRequest(frame); err != nil {
				return err
			}
		case CommandGroupInfoRequest:
			if _, err := DecodeGroupInfoRequest(frame); err != nil {
				return err
			}
		case CommandGroupInfoResponse:
			// 3005 已有厂商错配标签的实包；只计入传输活动，不将原文视为可解析或可信群资料。
		}
		if frame.Command == CommandQuit {
			if outbound {
				p.status.LocalQuit = true
			} else {
				p.status.RemoteQuit = true
			}
		}
	default:
		return fmt.Errorf("离线会话尚不支持命令 %d", frame.Command)
	}
	return nil
}

// progress 仅由已校验的消息更新派生阶段；不把线上顺序推断为认证结果。
func (p *Peer) progress() {
	if p.status.LocalQuit || p.status.RemoteQuit {
		p.status.State = PeerClosing
	} else if p.status.LocalReady && p.status.RemoteReady {
		p.status.State = PeerOnline
	} else if p.status.LocalHandshake && p.status.RemoteHandshake {
		p.status.State = PeerReadying
	} else if p.status.NodeSeen {
		p.status.State = PeerHandshaking
	}
}

func (p *Peer) senderID(outbound bool) string {
	if outbound {
		return p.localID
	}
	return p.remoteID
}

func (p *Peer) recordTime(outbound bool, at time.Duration) {
	p.lastAt = at
	if outbound {
		p.lastOutbound = at
	} else {
		p.lastInbound = at
	}
}
