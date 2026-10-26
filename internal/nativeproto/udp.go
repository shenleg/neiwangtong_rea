// 本文件将纯 ENetRuntime 驱动为显式 UDP socket 上的同步单对端传输，不处理应用登录或业务送达。
package nativeproto

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"time"
)

// udpPollInterval 限制无背景读协程时的取消延迟，并让核心定期处理可靠命令重传。
const udpPollInterval = 20 * time.Millisecond

// UDPTransport 独占一个真实或虚拟 socket，只与构造时的端点通信；所有方法必须串行调用。
// 不启动后台协程，不自动建连、重连或更换 ConnectID；可靠命令重传仅由 ENetRuntime.Tick 执行。
type UDPTransport struct {
	conn               packetConn
	remote             netip.AddrPort
	runtime            *ENetRuntime
	origin             time.Time
	messages           [][]byte
	queuedBytes        uint64
	maxQueuedBytes     uint64
	maxPendingCommands uint32
	failed             error
	closed             bool
	closeErr           error
	sendSubmissions    uint64 // 仅在Queue接受整条应用消息后递增，供上层区分未提交与结果未知。
}

// NewUDPTransport 使用 conn 和显式 remote 创建空闲传输驱动，不执行 socket bind 或 connect。
// 成功后接管 conn 的关闭责任；端点、socket 或核心配置无效时报错且不关闭 conn。
// conn 必须是未连接且未被其他调用者使用的 UDP socket；不会从包内身份推断或更换端点。
func NewUDPTransport(conn *net.UDPConn, remote netip.AddrPort, config ENetConfig) (*UDPTransport, error) {
	if conn == nil {
		return nil, fmt.Errorf("UDP 传输需要显式、未连接的 socket")
	}
	return newUDPTransport(conn, remote, config)
}

// newUDPTransport 允许Host传入独占虚拟端点，关闭只影响该端点，不影响共享socket。
func newUDPTransport(conn packetConn, remote netip.AddrPort, config ENetConfig) (*UDPTransport, error) {
	if conn.RemoteAddr() != nil {
		return nil, fmt.Errorf("UDP 传输需要显式、未连接的 socket")
	}
	if !remote.IsValid() || remote.Port() == 0 || remote.Addr().IsUnspecified() || remote.Addr().IsMulticast() || remote.Addr().Unmap() == netip.AddrFrom4([4]byte{255, 255, 255, 255}) {
		return nil, fmt.Errorf("UDP 对端必须有明确的单播 IP 和非零端口")
	}
	remote = netip.AddrPortFrom(remote.Addr().Unmap(), remote.Port())
	runtime, err := NewENetRuntime(config)
	if err != nil {
		return nil, err
	}
	return &UDPTransport{conn: conn, remote: remote, runtime: runtime, origin: time.Now(),
		maxQueuedBytes: config.MaxQueuedBytes, maxPendingCommands: config.MaxPendingCommands}, nil
}

// Connect 使用 ctx 的取消或截止时间完成主动 ENet 握手，成功只表示传输已连接。
// 仅允许空闲实例调用；不发送 1000/READY、不读取用户资料，握手失败后只能 Close。
func (t *UDPTransport) Connect(ctx context.Context) error {
	if err := t.usable(ctx); err != nil {
		return err
	}
	if t.runtime.Status().State != ENetIdle {
		return t.stateError("主动握手")
	}
	events, err := t.runtime.Start(t.now())
	if err = t.applyResult(ctx, events, err); err == nil {
		err = t.pump(ctx, func(s ENetStatus) bool { return s.State == ENetConnected })
	}
	return t.fail(err)
}

// Accept 使用 ctx 等待显式对端完成 ENet 握手；成功不表示业务认证或 READY 已完成。
// 仅允许空闲实例调用，不响应其他来源；握手失败后只能 Close。
func (t *UDPTransport) Accept(ctx context.Context) error {
	if err := t.usable(ctx); err != nil {
		return err
	}
	if t.runtime.Status().State != ENetIdle {
		return t.stateError("被动握手")
	}
	return t.fail(t.pump(ctx, func(s ENetStatus) bool { return s.State == ENetConnected }))
}

// Send 将 data 仅提交一次，然后使用 ctx 驱动至全部可靠命令获得传输 ACK。
// nil 只表示传输确认，不证明业务处理或独立接收；同时收到的消息保留供 Receive 获取。
// 未连接、提交失败、取消、断开或 I/O 失败时返回错误；提交后的未知结果禁止再次 Send。
func (t *UDPTransport) Send(ctx context.Context, data []byte) error {
	if err := t.usable(ctx); err != nil {
		return err
	}
	if t.runtime.Status().State != ENetConnected {
		return t.stateError("发送")
	}
	events, err := t.runtime.Queue(data, t.now())
	if err != nil {
		return err
	}
	t.sendSubmissions++
	if err = t.apply(ctx, events); err == nil {
		err = t.pump(ctx, func(s ENetStatus) bool { return s.State == ENetConnected && s.PendingCommands == 0 })
	}
	return t.fail(err)
}

// Receive 使用 ctx 等待一条完整消息并移出缓存；出队会释放消息数和字节预算。
// 对端正常断开后先交付已缓存消息，再返回 io.EOF；调用前或纯读等待取消可重试。
// 协议事件写出失败或取消后只允许 Close，不能继续使用已推进的接收状态。
func (t *UDPTransport) Receive(ctx context.Context) ([]byte, error) {
	if err := t.usable(ctx); err != nil {
		return nil, err
	}
	if len(t.messages) == 0 {
		state := t.runtime.Status().State
		if state != ENetConnected && state != ENetClosed {
			return nil, t.stateError("接收")
		}
		if err := t.pump(ctx, func(s ENetStatus) bool { return len(t.messages) != 0 || s.State == ENetClosed }); err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return nil, err
			}
			return nil, t.fail(err)
		}
	}
	if len(t.messages) == 0 {
		return nil, io.EOF
	}
	message := t.messages[0]
	t.messages[0] = nil
	t.messages = t.messages[1:]
	t.queuedBytes -= uint64(len(message))
	return message, nil
}

// Close 在 ctx 内等待已有可靠命令确认，再发送 ENet DISCONNECT 并等待 ACK，不发送应用 1017。
// 无论协议关闭成功与否都会关闭 socket；不安全的状态、取消和 I/O 故障返回错误。
// 对端已正常断开时直接关闭 socket；重复调用返回第一次 Close 的结果，不重复发包。
func (t *UDPTransport) Close(ctx context.Context) (err error) {
	if t.closed {
		return t.closeErr
	}
	defer func() {
		t.closed = true
		err = errors.Join(err, t.conn.Close())
		t.closeErr = err
	}()
	if err = t.usable(ctx); err != nil {
		return err
	}
	switch t.runtime.Status().State {
	case ENetClosed:
		return nil
	case ENetConnected:
		if err = t.pump(ctx, func(s ENetStatus) bool {
			return s.State == ENetClosed || s.State == ENetConnected && s.PendingCommands == 0
		}); err != nil {
			return err
		}
		if t.runtime.Status().State == ENetClosed {
			return nil
		}
	default:
		return t.stateError("协议关闭")
	}
	events, coreErr := t.runtime.Disconnect(t.now())
	if err = t.applyResult(ctx, events, coreErr); err != nil {
		return err
	}
	return t.pump(ctx, func(s ENetStatus) bool { return s.State == ENetClosed })
}

// pump 在当前调用内读包并推进核心；先实际发送事件，再检查完成状态，不能漏掉最后一个 ACK。
func (t *UDPTransport) pump(ctx context.Context, done func(ENetStatus) bool) error {
	buffer := make([]byte, enetMaxDatagram)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		status := t.runtime.Status()
		if status.State == ENetFailed {
			return t.stateError("驱动")
		}
		if done(status) {
			return nil
		}
		if status.State == ENetClosed {
			return io.EOF
		}
		events, err := t.runtime.Tick(t.now())
		if err := t.applyResult(ctx, events, err); err != nil {
			return err
		}
		status = t.runtime.Status()
		if status.State == ENetFailed {
			return t.stateError("驱动")
		}
		if done(status) {
			return nil
		}
		if status.State == ENetClosed {
			return io.EOF
		}
		if err := t.conn.SetReadDeadline(udpDeadline(ctx)); err != nil {
			return err
		}
		n, source, err := t.conn.ReadFromUDPAddrPort(buffer)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if timeout, ok := err.(net.Error); ok && timeout.Timeout() {
				continue
			}
			return err
		}
		source = netip.AddrPortFrom(source.Addr().Unmap(), source.Port())
		if source != t.remote {
			continue
		}
		events, err = t.runtime.Handle(buffer[:n], t.now())
		if err := t.applyResult(ctx, events, err); err != nil {
			return err
		}
	}
}

// poll 为应用心跳提供正常空闲返回，避免以短 ctx 取消已推进接收状态的 ACK 写出。
func (t *UDPTransport) poll(ctx context.Context, wait time.Duration) ([]byte, bool, error) {
	if err := t.usable(ctx); err != nil {
		return nil, false, err
	}
	if wait <= 0 {
		return nil, false, fmt.Errorf("应用轮询间隔必须为正")
	}
	until := time.Now().Add(wait)
	if err := t.pump(ctx, func(s ENetStatus) bool {
		return len(t.messages) != 0 || s.State == ENetClosed || !time.Now().Before(until)
	}); err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil, false, err
		}
		return nil, false, t.fail(err)
	}
	if len(t.messages) == 0 && t.runtime.Status().State != ENetClosed {
		return nil, false, nil
	}
	data, err := t.Receive(ctx)
	return data, err == nil, err
}

// apply 在发送 ACK 前校验完整消息缓存预算，避免确认无法保存的数据；实际发包完成后再追加消息。
func (t *UDPTransport) apply(ctx context.Context, events ENetEvents) error {
	bytes := t.queuedBytes
	for _, message := range events.Messages {
		if uint64(len(message)) > t.maxQueuedBytes-bytes {
			return fmt.Errorf("UDP 入站消息缓存超过 %d 字节", t.maxQueuedBytes)
		}
		bytes += uint64(len(message))
	}
	if uint64(len(t.messages))+uint64(len(events.Messages)) > uint64(t.maxPendingCommands) {
		return fmt.Errorf("UDP 入站消息缓存超过 %d 条", t.maxPendingCommands)
	}
	for _, datagram := range events.Datagrams {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := t.conn.SetWriteDeadline(udpDeadline(ctx)); err != nil {
			return err
		}
		n, err := t.conn.WriteToUDPAddrPort(datagram, t.remote)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return err
		}
		if n != len(datagram) {
			return io.ErrShortWrite
		}
	}
	t.messages = append(t.messages, events.Messages...)
	t.queuedBytes = bytes
	return nil
}

func (t *UDPTransport) applyResult(ctx context.Context, events ENetEvents, coreErr error) error {
	if err := t.apply(ctx, events); err != nil {
		// 核心已消费接收序号，事件未成功写出不能按普通读取消恢复，否则重传会被去重而丢消息。
		return t.fail(err)
	}
	return coreErr
}

func (t *UDPTransport) usable(ctx context.Context) error {
	if t.closed {
		return net.ErrClosed
	}
	if t.failed != nil {
		return fmt.Errorf("UDP 传输已失败，只允许关闭: %w", t.failed)
	}
	return ctx.Err()
}

func (t *UDPTransport) fail(err error) error {
	// 正常远端断开仍允许 Receive 交付已有缓存；发送者依然收到 EOF 而非成功。
	if err != nil && !(errors.Is(err, io.EOF) && t.runtime.Status().State == ENetClosed) {
		t.failed = err
	}
	return err
}

func (t *UDPTransport) stateError(action string) error {
	return fmt.Errorf("UDP %s要求合适的 ENet 状态，当前为 %s", action, t.runtime.Status().State)
}

func (t *UDPTransport) now() time.Duration { return time.Since(t.origin) }

// udpDeadline 同时考虑调用截止时间与固定读写轮询间隔，取消不依赖后台协程。
func udpDeadline(ctx context.Context) time.Time {
	deadline := time.Now().Add(udpPollInterval)
	if ctxDeadline, ok := ctx.Deadline(); ok && ctxDeadline.Before(deadline) {
		return ctxDeadline
	}
	return deadline
}
