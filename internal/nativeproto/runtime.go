// 本文件实现单对端、单可靠 channel 的 ENet 调度核心；显式输入时间与 datagram，不持有 socket 或账户身份。
package nativeproto

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"time"
)

// ENetState 是传输会话阶段，与应用层 PeerState、身份认证及 recipient_observed 无关。
type ENetState string

const (
	ENetIdle          ENetState = "idle"
	ENetConnecting    ENetState = "connecting"
	ENetAccepting     ENetState = "accepting"
	ENetConnected     ENetState = "connected"
	ENetDisconnecting ENetState = "disconnecting"
	ENetClosed        ENetState = "closed"
	ENetFailed        ENetState = "failed"
)

// ENetConfig 是单对端运行预算；不提供隐藏默认值，MTU/window 需落在 ENet 1.3 的有效范围。
// ConnectID 为本次主动建连的新鲜非零 4 字节不透明标识，不是身份凭据；被动方使用收到的连接标识。
// MaxAttempts 包含第一次发送；可靠重发保留同一序号及内容，不重新提交应用消息。
type ENetConfig struct {
	LocalPeerID        uint16
	ConnectID          [4]byte
	MTU                uint32
	WindowBytes        uint32
	MaxMessageBytes    uint32
	MaxQueuedBytes     uint64
	MaxPendingCommands uint32
	RetransmitAfter    time.Duration
	MaxAttempts        uint8
	ReceiveTimeout     time.Duration
	PingInterval       time.Duration
}

// ENetEvents 是调用者本轮应写出的 UDP payload 和首次按序交付的数据；不含 UDP/IP 头。
// 调用者必须先处理 Datagrams，再根据 Status 判断建连/关闭完成，不能漏掉最后的 ACK。
type ENetEvents struct {
	Datagrams [][]byte
	Messages  [][]byte
}

// ENetStatus 报告本次传输状态和预算占用；可靠 ACK 不等于应用处理或送达确认。
type ENetStatus struct {
	State            ENetState
	PendingCommands  int
	QueuedBytes      uint64
	InFlightBytes    uint32
	ReceiveBytes     uint64
	RemoteDisconnect bool
	DisconnectData   uint32
}

type pendingKey struct {
	channel  uint8
	sequence uint16
}

type pendingCommand struct {
	command  ENetCommand
	serial   uint64
	attempts uint8
	lastSent time.Duration
	rto      time.Duration
	stamps   []uint16
}

type receivedMessage struct {
	span       uint32
	total      uint32
	complete   bool
	fragmented bool
	data       []byte
}

// ENetRuntime 是可重放的传输调度状态；调用者串行调用，并按本次 UDP 端点单独创建实例。
// 仅实现可靠 channel 0、控制 channel 255、无压缩/校验和、零带宽限制及固定 throttle 参数。
// 不实现多 peer 的 socket 分派、拥塞自适应、不可靠消息或自动重新建连。
type ENetRuntime struct {
	config           ENetConfig
	state            ENetState
	failure          error
	lastAt           time.Duration
	lastReceive      time.Duration
	lastTransmit     time.Duration
	remotePeer       uint16
	incomingSession  uint8
	outgoingSession  uint8
	connectID        [4]byte
	connectBody      []byte
	verifyBody       []byte
	mtu              uint32
	remoteWindow     uint32
	nextControl      uint64
	nextData         uint64
	lastData         uint16
	pending          map[pendingKey]*pendingCommand
	order            []pendingKey
	queued           uint64
	inFlight         uint32
	receive          map[uint16]*receivedMessage
	owners           map[uint16]uint16
	receiveBytes     uint64
	assembler        *Reassembler
	remoteDisconnect bool
	disconnectData   uint32
}

// NewENetRuntime 按 config 创建空传输核心；无效范围、零预算、无效计时或本机分配范围溢出时报错。
// LocalPeerID 不得使用预留的 4095；最多 4096 个未完成序号用于约束本实现的有限窗口。
func NewENetRuntime(config ENetConfig) (*ENetRuntime, error) {
	if config.LocalPeerID >= 0x0fff || config.ConnectID == ([4]byte{}) || config.MTU < 576 || config.MTU > 4096 ||
		config.WindowBytes < 4096 || config.WindowBytes > 65536 || config.MaxMessageBytes == 0 ||
		config.MaxQueuedBytes < uint64(config.MaxMessageBytes) || config.MaxQueuedBytes < uint64(config.WindowBytes) || config.MaxPendingCommands == 0 || config.MaxPendingCommands > 4096 ||
		config.RetransmitAfter < time.Millisecond || config.MaxAttempts == 0 || config.ReceiveTimeout < config.RetransmitAfter ||
		config.PingInterval <= 0 || config.PingInterval >= config.ReceiveTimeout {
		return nil, fmt.Errorf("ENet 单对端配置的标识、范围、预算或计时无效")
	}
	assembler, err := NewReassembler(ReassemblyLimits{MaxMessageBytes: config.MaxMessageBytes,
		MaxBufferedBytes: config.MaxQueuedBytes, MaxAssemblies: config.MaxPendingCommands,
		MaxFragments: config.MaxPendingCommands, TTL: config.ReceiveTimeout})
	if err != nil {
		return nil, err
	}
	return &ENetRuntime{config: config, state: ENetIdle, remotePeer: 0x0fff,
		incomingSession: 0xff, outgoingSession: 0xff, connectID: config.ConnectID,
		mtu: config.MTU, remoteWindow: config.WindowBytes, pending: make(map[pendingKey]*pendingCommand),
		receive: make(map[uint16]*receivedMessage), owners: make(map[uint16]uint16), assembler: assembler}, nil
}

// Status 返回传输阶段和仍占用的可靠/接收预算；无输入且不会失败，不表示账户上线成功。
func (r *ENetRuntime) Status() ENetStatus {
	return ENetStatus{State: r.state, PendingCommands: len(r.pending), QueuedBytes: r.queued,
		InFlightBytes: r.inFlight, ReceiveBytes: r.receiveBytes, RemoteDisconnect: r.remoteDisconnect, DisconnectData: r.disconnectData}
}

// Start 在 at 主动发起一次 CONNECT；仅 idle 可用，时间倒退或重复调用返回错误且不隐式重连。
func (r *ENetRuntime) Start(at time.Duration) (ENetEvents, error) {
	if err := r.clock(at); err != nil {
		return ENetEvents{}, err
	}
	if r.state != ENetIdle {
		return ENetEvents{}, fmt.Errorf("只能从 idle 主动建连")
	}
	r.state = ENetConnecting
	r.lastReceive = at
	r.connectBody = r.connectionBody(r.config.LocalPeerID, 0xff, 0xff, true)
	r.enqueueControl(2, r.connectBody)
	return r.flush(ENetEvents{}, at), nil
}

// Queue 将 data 作为一次应用提交排入可靠 channel 0；过大、预算不足或未建连时报错，且不部分排队。
// 返回 datagram 仅表示传输动作；收到 ACK 前数据仍占用预算，应用不得凭超时自动再次 Queue。
func (r *ENetRuntime) Queue(data []byte, at time.Duration) (ENetEvents, error) {
	if err := r.clock(at); err != nil {
		return ENetEvents{}, err
	}
	if r.state != ENetConnected {
		return ENetEvents{}, fmt.Errorf("可靠提交要求 connected 状态")
	}
	if at-r.lastReceive >= r.config.ReceiveTimeout {
		return r.fail(fmt.Errorf("ENet 接收超时，本次消息未排队"))
	}
	if len(data) == 0 || uint64(len(data)) > uint64(r.config.MaxMessageBytes) {
		return ENetEvents{}, fmt.Errorf("可靠消息为空或超过消息预算")
	}
	// ENet 1.3 peer_send 以 fragment 头计算切片阈值，即使单条 reliable 头更小，也不能改用另一阈值。
	pieceSize := int(r.mtu) - 28
	count := (len(data)-1)/pieceSize + 1
	if err := r.capacity(0, uint32(count), uint64(len(data))); err != nil {
		return ENetEvents{}, err
	}
	start := uint16(r.nextData + 1)
	for number, offset := 0, 0; offset < len(data); number++ {
		end := min(offset+pieceSize, len(data))
		r.nextData++
		command := ENetCommand{Kind: 6, Channel: 0, Sequence: uint16(r.nextData), AckRequired: true,
			Data: append([]byte(nil), data[offset:end]...)}
		if count > 1 {
			command.Kind = 8
			command.Fragment = &ENetFragment{StartSequence: start, Count: uint32(count), Number: uint32(number),
				TotalLength: uint32(len(data)), Offset: uint32(offset)}
		}
		r.addPending(command, r.nextData)
		r.queued += uint64(len(command.Data))
		offset = end
	}
	return r.flush(ENetEvents{}, at), nil
}

// Disconnect 在可靠队列已排空后发送控制层 DISCONNECT，不生成应用 1017。
// 未建连、仍有待确认命令或时间倒退时报错；只有相应 ACK 到达后才转 closed。
func (r *ENetRuntime) Disconnect(at time.Duration) (ENetEvents, error) {
	if err := r.clock(at); err != nil {
		return ENetEvents{}, err
	}
	if r.state != ENetConnected || len(r.pending) != 0 {
		return ENetEvents{}, fmt.Errorf("断开要求 connected 且可靠队列已排空")
	}
	r.state = ENetDisconnecting
	r.enqueueControl(4, make([]byte, 4))
	return r.flush(ENetEvents{}, at), nil
}

// Tick 在 at 处理接收超时、有限指数退避重发和空闲 PING；不重建连接或重提应用数据。
// 重试耗尽、接收超时、缺片超时或时间倒退返回错误；前三者使本次会话进入 failed。
func (r *ENetRuntime) Tick(at time.Duration) (ENetEvents, error) {
	if err := r.clock(at); err != nil {
		return ENetEvents{}, err
	}
	if r.state == ENetIdle || r.state == ENetClosed {
		return ENetEvents{}, nil
	}
	if at-r.lastReceive >= r.config.ReceiveTimeout {
		return r.fail(fmt.Errorf("ENet 接收超时，未确认发送结果未知"))
	}
	pending := r.assembler.Pending()
	if _, err := r.assembler.Expire(at); err != nil {
		return r.fail(err)
	}
	if r.assembler.Pending() < pending {
		return r.fail(fmt.Errorf("ENet 分片超时，未交付缺失数据"))
	}
	for _, key := range r.order {
		command := r.pending[key]
		if command.attempts != 0 && at-command.lastSent >= command.rto && command.attempts >= r.config.MaxAttempts {
			return r.fail(fmt.Errorf("ENet 可靠发送尝试耗尽，发送结果未知"))
		}
	}
	var events ENetEvents
	for _, key := range r.order {
		command := r.pending[key]
		if command.attempts != 0 && at-command.lastSent >= command.rto {
			r.transmit(command, &events, at)
			if command.rto > r.config.ReceiveTimeout/2 {
				command.rto = r.config.ReceiveTimeout
			} else {
				command.rto *= 2
			}
		}
	}
	if r.state == ENetConnected && at-r.lastTransmit >= r.config.PingInterval && !r.pendingKind(5) {
		if err := r.capacity(255, 1, 0); err != nil {
			return r.fail(err)
		}
		r.enqueueControl(5, nil)
	}
	return r.flush(events, at), nil
}

// Handle 处理来自已由调用者核实 UDP 端点的 datagram，返回 ACK/重组消息及后续可写动作。
// 不属于本 peer/session 的旧包不回复；非法或未支持的有效会话命令会使会话 failed，不静默降级。
// 返回错误时应停止使用该会话；包内先前命令可能已经被处理，不能重新提交应用消息来补偿。
func (r *ENetRuntime) Handle(data []byte, at time.Duration) (ENetEvents, error) {
	if err := r.clock(at); err != nil {
		return ENetEvents{}, err
	}
	if r.state == ENetClosed {
		return ENetEvents{}, nil
	}
	packet, err := DecodeENet(data)
	if err != nil {
		return r.fail(err)
	}
	if r.state == ENetIdle {
		if packet.PeerID != 0x0fff || len(packet.Commands) != 1 || packet.Commands[0].Kind != 2 {
			return ENetEvents{}, nil
		}
	} else if packet.PeerID == 0x0fff && len(packet.Commands) == 1 && packet.Commands[0].Kind == 2 {
		if r.state == ENetAccepting && bytes.Equal(packet.Commands[0].Control, r.connectBody) {
			return ENetEvents{}, nil
		}
		return ENetEvents{}, nil
	} else if packet.PeerID != r.config.LocalPeerID || r.state != ENetConnecting && packet.SessionID != r.incomingSession {
		return ENetEvents{}, nil
	}
	var events ENetEvents
	accepted := false
	for _, command := range packet.Commands {
		if command.AckRequired && packet.SentTime == nil {
			return r.fail(fmt.Errorf("要求 ACK 的 ENet 命令缺少 sent-time"))
		}
		ok, err := r.handleCommand(packet, command, &events, at)
		if err != nil {
			return r.fail(err)
		}
		accepted = accepted || ok
		if r.state == ENetClosed {
			break
		}
	}
	if accepted {
		r.lastReceive = at
	}
	return r.flush(events, at), nil
}

func (r *ENetRuntime) clock(at time.Duration) error {
	if r.state == ENetFailed {
		return r.failure
	}
	if at < r.lastAt {
		return fmt.Errorf("ENet 时间不能倒退")
	}
	r.lastAt = at
	return nil
}

func (r *ENetRuntime) fail(err error) (ENetEvents, error) {
	r.state, r.failure = ENetFailed, err
	return ENetEvents{}, err
}

// capacity 同时限制字节/命令数及最早未确认序号的跨度，避免小消息跨过半周后复用未确认序号。
func (r *ENetRuntime) capacity(channel uint8, count uint32, size uint64) error {
	if uint64(len(r.pending))+uint64(count) > uint64(r.config.MaxPendingCommands) || size > r.config.MaxQueuedBytes-r.queued {
		return fmt.Errorf("ENet 可靠队列预算不足，消息未排队")
	}
	next := r.nextData
	if channel == 255 {
		next = r.nextControl
	}
	for _, key := range r.order {
		if key.channel == channel && next+uint64(count)-r.pending[key].serial >= 4096 {
			return fmt.Errorf("ENet 序号窗口未释放，消息未排队")
		}
	}
	return nil
}

func (r *ENetRuntime) enqueueControl(kind uint8, body []byte) {
	r.nextControl++
	r.addPending(ENetCommand{Kind: kind, Channel: 255, Sequence: uint16(r.nextControl), AckRequired: true,
		Control: append([]byte(nil), body...)}, r.nextControl)
}

func (r *ENetRuntime) addPending(command ENetCommand, serial uint64) {
	key := pendingKey{command.Channel, command.Sequence}
	r.pending[key] = &pendingCommand{command: command, serial: serial, rto: r.config.RetransmitAfter}
	r.order = append(r.order, key)
}

func (r *ENetRuntime) pendingKind(kind uint8) bool {
	for _, command := range r.pending {
		if command.command.Kind == kind {
			return true
		}
	}
	return false
}

func (r *ENetRuntime) flush(events ENetEvents, at time.Duration) ENetEvents {
	if r.state == ENetClosed {
		return events
	}
	for _, key := range r.order {
		command := r.pending[key]
		if command.attempts != 0 {
			continue
		}
		cost := uint32(len(command.command.Data))
		if cost != 0 && uint64(r.inFlight)+uint64(cost) > uint64(r.remoteWindow) {
			continue
		}
		r.inFlight += cost
		r.transmit(command, &events, at)
	}
	return events
}

func (r *ENetRuntime) transmit(command *pendingCommand, events *ENetEvents, at time.Duration) {
	stamp := uint16(at / time.Millisecond)
	packet := ENetPacket{PeerID: r.remotePeer, Commands: []ENetCommand{command.command}, SentTime: &stamp}
	if r.remotePeer != 0x0fff {
		packet.SessionID = r.outgoingSession
	}
	// 命令全部由本模块按已校验的 MTU/长度创建，EncodeENet 的错误在这里属于内部契约破坏。
	data, err := EncodeENet(packet)
	if err != nil {
		panic(err)
	}
	events.Datagrams = append(events.Datagrams, data)
	command.attempts++
	command.lastSent = at
	command.stamps = append(command.stamps, stamp)
	r.lastTransmit = at
}

func (r *ENetRuntime) removePending(key pendingKey) *pendingCommand {
	command := r.pending[key]
	delete(r.pending, key)
	for index, item := range r.order {
		if item == key {
			r.order = append(r.order[:index], r.order[index+1:]...)
			break
		}
	}
	r.queued -= uint64(len(command.command.Data))
	if command.attempts != 0 {
		r.inFlight -= uint32(len(command.command.Data))
	}
	return command
}

func (r *ENetRuntime) acknowledge(packet ENetPacket, command ENetCommand, events *ENetEvents, at time.Duration) {
	if !command.AckRequired {
		return
	}
	body := make([]byte, 4)
	binary.BigEndian.PutUint16(body, command.Sequence)
	binary.BigEndian.PutUint16(body[2:], *packet.SentTime)
	ack := ENetCommand{Kind: 1, Channel: command.Channel, Sequence: command.Sequence, Control: body}
	data, err := EncodeENet(ENetPacket{PeerID: r.remotePeer, SessionID: r.outgoingSession, Commands: []ENetCommand{ack}})
	if err != nil {
		panic(err)
	}
	events.Datagrams = append(events.Datagrams, data)
	r.lastTransmit = at
}

// connectionBody 保持 connectID 为原始 4 字节；其余网络整数按协议显式转换，不能把随机标识当整数交换。
func (r *ENetRuntime) connectionBody(peer uint16, incoming, outgoing uint8, connect bool) []byte {
	size := 40
	if connect {
		size = 44
	}
	body := make([]byte, size)
	binary.BigEndian.PutUint16(body, peer)
	body[2], body[3] = incoming, outgoing
	for index, value := range []uint32{r.mtu, r.config.WindowBytes, 1, 0, 0, 5000, 2, 2} {
		binary.BigEndian.PutUint32(body[4+index*4:], value)
	}
	copy(body[36:40], r.connectID[:])
	return body
}
