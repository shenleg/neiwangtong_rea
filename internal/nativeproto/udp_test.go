// 本文件仅用 127.0.0.1 临时 UDP socket 验证同步传输，不访问真实客户端或内网节点。
package nativeproto

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"net/netip"
	"testing"
	"time"
)

// udpTestConfig 为合成对端提供独立身份及足以测试分片的显式预算，不读取客户端配置。
// 返回配置不产生 I/O，也不会失败。
func udpTestConfig(peer uint16, id byte) ENetConfig {
	return ENetConfig{LocalPeerID: peer, ConnectID: [4]byte{id, 2, 3, 4}, MTU: 576,
		WindowBytes: 65536, MaxMessageBytes: 32768, MaxQueuedBytes: 65536, MaxPendingCommands: 256,
		RetransmitAfter: 40 * time.Millisecond, MaxAttempts: 20, ReceiveTimeout: 2 * time.Second,
		PingInterval: time.Second}
}

// udpTestSocket 创建 loopback 临时端口并登记清理；创建失败会终止当前测试，不访问外部端点。
func udpTestSocket(t *testing.T) *net.UDPConn {
	t.Helper()
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

// udpTestContext 给当前测试创建两秒截止时间并登记取消；无外部输入，不会失败。
func udpTestContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	t.Cleanup(cancel)
	return ctx
}

// udpTestPair 创建两个串行使用、身份不同的合成传输并完成握手；失败时终止测试。
// 第一端使用 IPv4-mapped remote，验证构造及收包端点归一化；清理只关闭本地 socket。
func udpTestPair(t *testing.T) (*UDPTransport, *UDPTransport, context.Context) {
	t.Helper()
	aConn, bConn := udpTestSocket(t), udpTestSocket(t)
	bAddr := bConn.LocalAddr().(*net.UDPAddr).AddrPort()
	mapped := netip.AddrPortFrom(netip.AddrFrom16(bAddr.Addr().As16()), bAddr.Port())
	a, err := NewUDPTransport(aConn, mapped, udpTestConfig(3, 1))
	if err != nil {
		t.Fatal(err)
	}
	b, err := NewUDPTransport(bConn, aConn.LocalAddr().(*net.UDPAddr).AddrPort(), udpTestConfig(7, 9))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_ = a.Close(ctx)
		_ = b.Close(ctx)
	})
	ctx := udpTestContext(t)
	accepted := make(chan error, 1)
	go func() { accepted <- b.Accept(ctx) }()
	connectErr := a.Connect(ctx)
	acceptErr := <-accepted
	if connectErr != nil || acceptErr != nil {
		t.Fatalf("握手失败: Connect=%v Accept=%v", connectErr, acceptErr)
	}
	if a.runtime.Status().State != ENetConnected || b.runtime.Status().State != ENetConnected {
		t.Fatal("双向握手未进入 connected")
	}
	return a, b, ctx
}

// udpReceiveResult 仅在测试协程之间传递一次完整接收结果。
type udpReceiveResult struct {
	data []byte
	err  error
}

// udpTestTransfer 同时驱动一端 Send 与另一端 Receive，并验证原始消息不变；失败时终止测试。
func udpTestTransfer(t *testing.T, ctx context.Context, sender, receiver *UDPTransport, data []byte) {
	t.Helper()
	received := make(chan udpReceiveResult, 1)
	go func() {
		message, err := receiver.Receive(ctx)
		received <- udpReceiveResult{message, err}
	}()
	sendErr := sender.Send(ctx, data)
	result := <-received
	if sendErr != nil || result.err != nil || !bytes.Equal(data, result.data) {
		t.Fatalf("传输结果: Send=%v Receive=%v bytes=%d/%d", sendErr, result.err, len(result.data), len(data))
	}
}

// 验证双向握手和可靠消息，较大消息必须经 MTU 分片及重组。
func TestUDPTransportConnectAcceptMessages(t *testing.T) {
	a, b, ctx := udpTestPair(t)
	udpTestTransfer(t, ctx, a, b, []byte("主动端到被动端"))
	udpTestTransfer(t, ctx, b, a, bytes.Repeat([]byte("fragment-分片"), 600))
	if a.queuedBytes != 0 || b.queuedBytes != 0 {
		t.Fatal("消息出队后未释放字节计数")
	}
}

// 两端各自只进行一个 Send，验证发送循环接收的对向消息没有丢弃。
func TestUDPTransportSimultaneousSendBuffersReceive(t *testing.T) {
	a, b, ctx := udpTestPair(t)
	start := make(chan struct{})
	aDone, bDone := make(chan error, 1), make(chan error, 1)
	aData, bData := bytes.Repeat([]byte("a"), 3000), bytes.Repeat([]byte("b"), 4000)
	go func() { <-start; aDone <- a.Send(ctx, aData) }()
	go func() { <-start; bDone <- b.Send(ctx, bData) }()
	close(start)
	aErr, bErr := <-aDone, <-bDone
	if aErr != nil || bErr != nil {
		t.Fatalf("双向 Send: %v / %v", aErr, bErr)
	}
	for _, test := range []struct {
		transport *UDPTransport
		want      []byte
	}{{a, bData}, {b, aData}} {
		got, err := test.transport.Receive(ctx)
		if err != nil || !bytes.Equal(got, test.want) {
			t.Fatalf("缓存 Receive: %v, bytes=%d/%d", err, len(got), len(test.want))
		}
		if len(test.transport.messages) != 0 || test.transport.queuedBytes != 0 {
			t.Fatal("缓存出队后计数未清空")
		}
	}
}

// 关闭方必须等到对端已发送最终 ACK；被动方 Receive 随后报告 EOF。
func TestUDPTransportNormalDisconnect(t *testing.T) {
	a, b, ctx := udpTestPair(t)
	peerDone := make(chan error, 1)
	go func() {
		_, err := b.Receive(ctx)
		if !errors.Is(err, io.EOF) {
			peerDone <- errors.Join(errors.New("对端 Receive 未报告 EOF"), err)
			return
		}
		peerDone <- b.Close(ctx)
	}()
	aErr := a.Close(ctx)
	bErr := <-peerDone
	if aErr != nil || bErr != nil {
		t.Fatalf("正常断开失败: %v / %v", aErr, bErr)
	}
	if err := a.Close(ctx); err != nil {
		t.Fatalf("重复 Close: %v", err)
	}
	if _, err := a.conn.WriteToUDPAddrPort([]byte("closed"), a.remote); !errors.Is(err, net.ErrClosed) {
		t.Fatalf("Close 未关闭持有的 socket: %v", err)
	}
}

// 同步读取可在截止时间后及时返回，取消读不影响后续新一次接收。
func TestUDPTransportReceiveCancellation(t *testing.T) {
	a, b, ctx := udpTestPair(t)
	readCtx, cancel := context.WithTimeout(ctx, 35*time.Millisecond)
	defer cancel()
	started := time.Now()
	if _, err := b.Receive(readCtx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Receive 取消: %v", err)
	}
	if time.Since(started) > 500*time.Millisecond {
		t.Fatal("取消读取未及时返回")
	}
	udpTestTransfer(t, ctx, a, b, []byte("取消后的新消息"))
}

// 主动与被动握手在没有对端驱动时均须响应取消，且失败关闭仍释放 socket。
func TestUDPTransportHandshakeCancellation(t *testing.T) {
	for _, active := range []bool{true, false} {
		t.Run(map[bool]string{true: "connect", false: "accept"}[active], func(t *testing.T) {
			conn, silent := udpTestSocket(t), udpTestSocket(t)
			transport, err := NewUDPTransport(conn, silent.LocalAddr().(*net.UDPAddr).AddrPort(), udpTestConfig(3, 1))
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 35*time.Millisecond)
			defer cancel()
			started := time.Now()
			if active {
				err = transport.Connect(ctx)
			} else {
				err = transport.Accept(ctx)
			}
			if !errors.Is(err, context.DeadlineExceeded) || time.Since(started) > 500*time.Millisecond {
				t.Fatalf("握手取消未及时返回截止错误: %v", err)
			}
			if err := transport.Close(context.Background()); err == nil {
				t.Fatal("失败握手被假称为正常协议关闭")
			}
			if _, err := conn.WriteToUDPAddrPort([]byte("closed"), transport.remote); !errors.Is(err, net.ErrClosed) {
				t.Fatalf("失败 Close 未释放 socket: %v", err)
			}
		})
	}
}

// 同一 IP 的陌生端口先发无效包，驱动必须忽略它而不失败或更换显式对端。
func TestUDPTransportIgnoreUnknownSource(t *testing.T) {
	a, b, ctx := udpTestPair(t)
	stranger := udpTestSocket(t)
	if _, err := stranger.WriteToUDPAddrPort([]byte{0xff}, b.conn.LocalAddr().(*net.UDPAddr).AddrPort()); err != nil {
		t.Fatal(err)
	}
	udpTestTransfer(t, ctx, a, b, []byte("仅接受显式来源"))
	if b.remote != a.conn.LocalAddr().(*net.UDPAddr).AddrPort() {
		t.Fatal("陌生来源改变了目标端点")
	}
}

// 对端不再读包时，已提交 Send 的取消保留未知结果，禁止下一次 Send 重提或假称成功。
func TestUDPTransportSendCancellationUnknownResult(t *testing.T) {
	a, _, ctx := udpTestPair(t)
	sendCtx, cancel := context.WithTimeout(ctx, 35*time.Millisecond)
	defer cancel()
	if err := a.Send(sendCtx, []byte("结果未知")); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Send 取消: %v", err)
	}
	pending := a.runtime.Status().PendingCommands
	if pending == 0 {
		t.Fatal("没有对端驱动却清空了可靠待确认命令")
	}
	if err := a.Send(ctx, []byte("不得提交")); err == nil {
		t.Fatal("未知结果后仍允许 Send")
	}
	if a.runtime.Status().PendingCommands != pending {
		t.Fatal("第二次 Send 修改了旧任务")
	}
	if err := a.Close(ctx); err == nil {
		t.Fatal("未知发送结果被假称为正常协议关闭")
	}
	if !a.closed {
		t.Fatal("错误 Close 未关闭 socket")
	}
}

// 构造失败不接管 socket；成功后的空闲 Close 应报状态错误，但仍关闭并支持重复调用。
func TestUDPTransportConstructorOwnership(t *testing.T) {
	conn, remote := udpTestSocket(t), udpTestSocket(t)
	addr := remote.LocalAddr().(*net.UDPAddr).AddrPort()
	invalid := udpTestConfig(3, 1)
	invalid.MTU = 1
	if _, err := NewUDPTransport(conn, addr, invalid); err == nil {
		t.Fatal("非法核心配置通过构造")
	}
	if _, err := NewUDPTransport(conn, netip.AddrPort{}, udpTestConfig(3, 1)); err == nil {
		t.Fatal("未指定远端通过构造")
	}
	for _, forbidden := range []string{"255.255.255.255:9012", "224.0.0.1:9012", "0.0.0.0:9012"} {
		if _, err := NewUDPTransport(conn, netip.MustParseAddrPort(forbidden), udpTestConfig(3, 1)); err == nil {
			t.Fatal("非单对端地址通过构造")
		}
	}
	if _, err := conn.WriteToUDPAddrPort([]byte("still owned by caller"), addr); err != nil {
		t.Fatalf("构造失败关闭了调用方 socket: %v", err)
	}
	transport, err := NewUDPTransport(conn, addr, udpTestConfig(3, 1))
	if err != nil {
		t.Fatal(err)
	}
	first := transport.Close(context.Background())
	if first == nil {
		t.Fatal("空闲状态无协议握手却报告正常断开")
	}
	if second := transport.Close(context.Background()); second != first {
		t.Fatalf("重复 Close 未保留首个结果: %v / %v", first, second)
	}
	if _, err := conn.WriteToUDPAddrPort([]byte("closed"), addr); !errors.Is(err, net.ErrClosed) {
		t.Fatalf("成功构造后未接管 socket: %v", err)
	}
}

// 使用直接合成事件检查完整消息缓存的两类预算及出队计数，不创建外部网络连接。
func TestUDPTransportQueueBudget(t *testing.T) {
	for _, countLimit := range []bool{false, true} {
		t.Run(map[bool]string{false: "bytes", true: "messages"}[countLimit], func(t *testing.T) {
			a, _, ctx := udpTestPair(t)
			if countLimit {
				a.maxPendingCommands = 1
			} else {
				a.maxQueuedBytes = 3
			}
			if err := a.apply(ctx, ENetEvents{Messages: [][]byte{[]byte("one")}}); err != nil {
				t.Fatal(err)
			}
			if err := a.apply(ctx, ENetEvents{Messages: [][]byte{[]byte("two")}}); err == nil {
				t.Fatal("完整消息缓存未执行预算")
			}
			got, err := a.Receive(ctx)
			if err != nil || string(got) != "one" || a.queuedBytes != 0 {
				t.Fatalf("出队未释放预算: %q / %v / %d", got, err, a.queuedBytes)
			}
			if err := a.apply(ctx, ENetEvents{Messages: [][]byte{[]byte("two")}}); err != nil {
				t.Fatalf("出队后预算未恢复: %v", err)
			}
		})
	}
}

// 验证没有 ctx deadline 时，主动取消仍能通过读轮询及时结束正在等待的握手。
func TestUDPTransportExplicitHandshakeCancel(t *testing.T) {
	conn, remote := udpTestSocket(t), udpTestSocket(t)
	transport, err := NewUDPTransport(conn, remote.LocalAddr().(*net.UDPAddr).AddrPort(), udpTestConfig(3, 1))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- transport.Connect(ctx) }()
	if err := remote.SetReadDeadline(time.Now().Add(500 * time.Millisecond)); err != nil {
		cancel()
		<-done
		t.Fatal(err)
	}
	buffer := make([]byte, enetMaxDatagram)
	_, _, readErr := remote.ReadFromUDPAddrPort(buffer)
	started := time.Now()
	cancel()
	connectErr := <-done
	if readErr != nil || !errors.Is(connectErr, context.Canceled) || time.Since(started) > 500*time.Millisecond {
		t.Fatalf("取消已发送 CONNECT 的等待: read=%v Connect=%v", readErr, connectErr)
	}
	if err := transport.Close(context.Background()); !errors.Is(err, context.Canceled) {
		t.Fatalf("关闭未保留握手取消错误: %v", err)
	}
}

// 验证远端断开后先返回已有缓存再 EOF，不能因可靠队列为空而允许 Send 报成功。
func TestUDPTransportClosedPeerDrainsMessages(t *testing.T) {
	a, b, ctx := udpTestPair(t)
	if err := a.apply(ctx, ENetEvents{Messages: [][]byte{[]byte("cached")}}); err != nil {
		t.Fatal(err)
	}
	peerDone := make(chan error, 1)
	go func() { peerDone <- b.Close(ctx) }()
	pumpErr := a.pump(ctx, func(s ENetStatus) bool { return s.State == ENetClosed })
	peerErr := <-peerDone
	if pumpErr != nil || peerErr != nil {
		t.Fatalf("被动断开失败: %v / %v", pumpErr, peerErr)
	}
	if err := a.Send(ctx, []byte("不得发送")); err == nil {
		t.Fatal("远端已断开且 pending=0 却返回发送成功")
	}
	if got, err := a.Receive(ctx); err != nil || string(got) != "cached" {
		t.Fatalf("断开后未保留缓存: %q / %v", got, err)
	}
	if _, err := a.Receive(ctx); !errors.Is(err, io.EOF) {
		t.Fatalf("缓存耗尽后未返回 EOF: %v", err)
	}
	if err := a.Close(ctx); err != nil {
		t.Fatalf("被动关闭 socket: %v", err)
	}
}

// udpTestACK 生成供事件写出测试使用的合成 ACK；编码失败时终止当前测试，不进行网络 I/O。
func udpTestACK(t *testing.T) []byte {
	t.Helper()
	ack, err := EncodeENet(ENetPacket{PeerID: 7, Commands: []ENetCommand{{Kind: 1, Channel: 0, Control: make([]byte, 4)}}})
	if err != nil {
		t.Fatal(err)
	}
	return ack
}

// 核心已返回消息与 ACK 时，写出取消必须使传输失败，不能把已经去重的消息当作可重试读取。
func TestUDPTransportApplyCancellationFailsTransport(t *testing.T) {
	a, _, ctx := udpTestPair(t)
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	events := ENetEvents{Datagrams: [][]byte{udpTestACK(t)}, Messages: [][]byte{[]byte("不能丢失后继续")}}
	if err := a.applyResult(canceled, events, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("事件写出取消: %v", err)
	}
	if a.failed == nil {
		t.Fatal("事件写出取消后未标记失败")
	}
	if err := a.Send(ctx, []byte("不得再次提交")); err == nil {
		t.Fatal("事件写出失败后仍允许 Send")
	}
	if _, err := a.Receive(ctx); err == nil {
		t.Fatal("事件写出失败后仍允许 Receive")
	}
	if err := a.Close(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Close 未保留事件写出错误: %v", err)
	}
	if _, err := a.conn.WriteToUDPAddrPort([]byte("closed"), a.remote); !errors.Is(err, net.ErrClosed) {
		t.Fatalf("事件失败后的 Close 未关闭 socket: %v", err)
	}
}

// 缓存预算不足时不能先发 ACK 确认无法保存的消息，并且必须使已推进的传输失败。
func TestUDPTransportBudgetFailureDoesNotACK(t *testing.T) {
	for _, countLimit := range []bool{false, true} {
		t.Run(map[bool]string{false: "bytes", true: "messages"}[countLimit], func(t *testing.T) {
			conn, remote := udpTestSocket(t), udpTestSocket(t)
			a, err := NewUDPTransport(conn, remote.LocalAddr().(*net.UDPAddr).AddrPort(), udpTestConfig(3, 1))
			if err != nil {
				t.Fatal(err)
			}
			ctx := udpTestContext(t)
			if countLimit {
				a.maxPendingCommands = 1
			} else {
				a.maxQueuedBytes = 3
			}
			if err := a.apply(ctx, ENetEvents{Messages: [][]byte{[]byte("one")}}); err != nil {
				t.Fatal(err)
			}
			events := ENetEvents{Datagrams: [][]byte{udpTestACK(t)}, Messages: [][]byte{[]byte("two")}}
			if err := a.applyResult(ctx, events, nil); err == nil || a.failed == nil {
				t.Fatalf("预算不足未使事件处理失败: %v", err)
			}
			if err := remote.SetReadDeadline(time.Now().Add(20 * time.Millisecond)); err != nil {
				t.Fatal(err)
			}
			buffer := make([]byte, enetMaxDatagram)
			_, _, readErr := remote.ReadFromUDPAddrPort(buffer)
			if timeout, ok := readErr.(net.Error); !ok || !timeout.Timeout() {
				t.Fatalf("预算不足仍发出了 ACK，或出现非预期读取错误: %v", readErr)
			}
			if _, err := a.Receive(ctx); err == nil {
				t.Fatal("预算失败后仍允许交付缓存并继续使用传输")
			}
			if err := a.Close(ctx); err == nil {
				t.Fatal("预算失败后 Close 未返回失败原因")
			}
		})
	}
}
