// 本文件将Host共享UDP口分成独立队列和读写时限的虚拟端点，复用单对端Client而不改变协议。
package nativeproto

import (
	"fmt"
	"net"
	"net/netip"
	"os"
	"sync"
	"time"
)

// packetConn 是Client和UDPTransport需要的最小数据报接口。
type packetConn interface {
	LocalAddr() net.Addr
	RemoteAddr() net.Addr
	ReadFromUDPAddrPort([]byte) (int, netip.AddrPort, error)
	WriteToUDPAddrPort([]byte, netip.AddrPort) (int, error)
	SetReadDeadline(time.Time) error
	SetWriteDeadline(time.Time) error
	Close() error
}

// hostPacketQueueSize限制单peer积压，防止慢会话阻塞Host的公共接收循环。
const hostPacketQueueSize = 256

type hostPacket struct {
	data   []byte
	source netip.AddrPort
}

// hostPacketConn不读取真实socket，也不设置其deadline；Host负责接收、分派和最终关闭。
// 每端点最多有一次实际UDP写；Go UDPConn支持不同端点并发写，无需Host共享写锁。
// 虚拟写超时或Close会立即返回，但已开始的系统写可能随后完成，其结果未知，不能重试。
type hostPacketConn struct {
	conn          *net.UDPConn
	local         netip.AddrPort
	remote        netip.AddrPort
	packets       chan hostPacket
	writing       chan struct{}
	mu            sync.Mutex
	readDeadline  time.Time
	writeDeadline time.Time
	changed       chan struct{}
	closed        bool
}

// newHostPacketConn 创建独立虚拟端点；地址来自Host已验证的绑定与显式对端，不接管conn。
func newHostPacketConn(conn *net.UDPConn, local, remote netip.AddrPort) *hostPacketConn {
	return &hostPacketConn{conn: conn, local: local, remote: netip.AddrPortFrom(remote.Addr().Unmap(), remote.Port()),
		packets: make(chan hostPacket, hostPacketQueueSize), writing: make(chan struct{}, 1), changed: make(chan struct{})}
}

// LocalAddr 返回虚拟绑定地址，保持原Client的UDPAddr类型和公告校验。
func (c *hostPacketConn) LocalAddr() net.Addr { return net.UDPAddrFromAddrPort(c.local) }

// RemoteAddr 返回nil，保持未连接UDP socket语义；写入仍限制为构造时的remote。
func (c *hostPacketConn) RemoteAddr() net.Addr { return nil }

// deliver复制Host接收缓冲区后入队；队列满或端点已关闭时明确报错，不阻塞其他peer。
func (c *hostPacketConn) deliver(data []byte, source netip.AddrPort) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return net.ErrClosed
	}
	select {
	case c.packets <- hostPacket{data: append([]byte(nil), data...), source: source}:
		return nil
	default:
		return fmt.Errorf("虚拟UDP端点入站队列超过%d包", hostPacketQueueSize)
	}
}

// ReadFromUDPAddrPort读取本端点队列；时限变化会唤醒已有读取，关闭或超时返回标准网络错误。
func (c *hostPacketConn) ReadFromUDPAddrPort(buffer []byte) (int, netip.AddrPort, error) {
	for {
		c.mu.Lock()
		deadline, changed, closed := c.readDeadline, c.changed, c.closed
		c.mu.Unlock()
		if closed {
			return 0, netip.AddrPort{}, net.ErrClosed
		}
		if !deadline.IsZero() && !time.Now().Before(deadline) {
			return 0, netip.AddrPort{}, os.ErrDeadlineExceeded
		}
		var timer *time.Timer
		var expired <-chan time.Time
		if !deadline.IsZero() {
			timer = time.NewTimer(time.Until(deadline))
			expired = timer.C
		}
		select {
		case packet := <-c.packets:
			if timer != nil {
				timer.Stop()
			}
			return copy(buffer, packet.data), packet.source, nil
		case <-changed:
		case <-expired:
		}
		if timer != nil {
			timer.Stop()
		}
	}
}

// WriteToUDPAddrPort仅向固定remote写；截止时间和Close不修改共享socket，也不取消已开始的系统写。
func (c *hostPacketConn) WriteToUDPAddrPort(data []byte, remote netip.AddrPort) (int, error) {
	remote = netip.AddrPortFrom(remote.Addr().Unmap(), remote.Port())
	if remote != c.remote {
		return 0, fmt.Errorf("虚拟UDP端点只允许写入固定对端%s", c.remote)
	}
	type writeResult struct {
		n   int
		err error
	}
	var result <-chan writeResult
	gate := c.writing
	for {
		c.mu.Lock()
		deadline, changed, closed := c.writeDeadline, c.changed, c.closed
		c.mu.Unlock()
		if closed {
			return 0, net.ErrClosed
		}
		if !deadline.IsZero() && !time.Now().Before(deadline) {
			return 0, os.ErrDeadlineExceeded
		}
		var timer *time.Timer
		var expired <-chan time.Time
		if !deadline.IsZero() {
			timer = time.NewTimer(time.Until(deadline))
			expired = timer.C
		}
		select {
		case gate <- struct{}{}:
			// 等待写位置期间可能已经关闭或改为过期时限，不能在这两者之后开始新写。
			c.mu.Lock()
			var startErr error
			if c.closed {
				startErr = net.ErrClosed
			} else if !c.writeDeadline.IsZero() && !time.Now().Before(c.writeDeadline) {
				startErr = os.ErrDeadlineExceeded
			}
			c.mu.Unlock()
			if startErr != nil {
				<-c.writing
				if timer != nil {
					timer.Stop()
				}
				return 0, startErr
			}
			// 调用可能先因虚拟deadline返回，系统写必须持有自己的字节，且完成前不释放写位置。
			payload := append([]byte(nil), data...)
			completed := make(chan writeResult, 1)
			go func() {
				n, err := c.conn.WriteToUDPAddrPort(payload, c.remote)
				<-c.writing
				completed <- writeResult{n, err}
			}()
			result, gate = completed, nil
		case written := <-result:
			if timer != nil {
				timer.Stop()
			}
			return written.n, written.err
		case <-changed:
		case <-expired:
		}
		if timer != nil {
			timer.Stop()
		}
	}
}

// SetReadDeadline只修改本端点读时限；零值清除时限，关闭后返回net.ErrClosed。
func (c *hostPacketConn) SetReadDeadline(deadline time.Time) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return net.ErrClosed
	}
	c.readDeadline = deadline
	close(c.changed)
	c.changed = make(chan struct{})
	return nil
}

// SetWriteDeadline只修改本端点写时限并唤醒等待中的写；零值清除时限，关闭后返回net.ErrClosed。
func (c *hostPacketConn) SetWriteDeadline(deadline time.Time) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return net.ErrClosed
	}
	c.writeDeadline = deadline
	close(c.changed)
	c.changed = make(chan struct{})
	return nil
}

// Close只关闭虚拟端点并唤醒读写等待，不关闭Host的真实socket；重复调用返回nil。
func (c *hostPacketConn) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.closed {
		c.closed = true
		close(c.changed)
	}
	return nil
}
