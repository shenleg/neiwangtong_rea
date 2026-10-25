// 本文件只向一个显式 IPv4 对端单播发现；不枚举地址、不广播、不扫描、不因超时重发发现请求。
package nativeproto

import (
	"context"
	"fmt"
	"net/netip"
	"time"
)

func (c *Client) discover(ctx context.Context) (NodePacket, error) {
	data, err := c.config.Node.Encode()
	if err != nil {
		return NodePacket{}, err
	}
	deadline := minTime(time.Now().Add(c.config.DiscoveryTimeout), c.origin.Add(c.config.Duration))
	if parent, ok := ctx.Deadline(); ok {
		deadline = minTime(deadline, parent)
	}
	if err := ctx.Err(); err != nil {
		return NodePacket{}, err
	}
	if err := c.emit("discovery_send_planned", 1); err != nil {
		return NodePacket{}, err
	}
	if err := c.discovery.SetWriteDeadline(deadline); err != nil {
		return NodePacket{}, err
	}
	c.result.DiscoveryAttempts++
	if count, err := c.discovery.WriteToUDPAddrPort(data, c.config.RemoteDiscovery); err != nil || count != len(data) {
		return NodePacket{}, fmt.Errorf("单次发现写出失败或短写，不重发: %v", err)
	}
	c.result.DiscoverySends++
	if err := c.emit("discovery_sent", 1); err != nil {
		return NodePacket{}, err
	}
	buffer := make([]byte, enetMaxDatagram)
	for time.Now().Before(deadline) {
		if err := ctx.Err(); err != nil {
			return NodePacket{}, err
		}
		if err := c.discovery.SetReadDeadline(minTime(deadline, time.Now().Add(100*time.Millisecond))); err != nil {
			return NodePacket{}, err
		}
		count, source, err := c.discovery.ReadFromUDPAddrPort(buffer)
		if err != nil {
			if isTimeout(err) {
				continue
			}
			return NodePacket{}, err
		}
		source = netip.AddrPortFrom(source.Addr().Unmap(), source.Port())
		if source != c.config.RemoteDiscovery {
			continue
		}
		node, err := DecodeNodePacket(buffer[:count])
		if err != nil {
			return NodePacket{}, err
		}
		if node.Type != 1 && node.Type != 2 {
			return NodePacket{}, fmt.Errorf("发现口收到非 1/2 节点包")
		}
		if node.DiscoveryPort != source.Port() {
			return NodePacket{}, fmt.Errorf("对端公告发现端口与实际源不符")
		}
		if err := c.emit("discovery_received", node.Type); err != nil {
			return NodePacket{}, err
		}
		return node, nil
	}
	return NodePacket{}, fmt.Errorf("单次单播发现超时，未重发或扫描其他端点")
}

func (c *Client) replyDiscovery(remote netip.AddrPort) error {
	packet := c.config.Node
	packet.Type = 2
	data, err := packet.Encode()
	if err != nil {
		return err
	}
	if err := c.discovery.SetWriteDeadline(time.Now().Add(time.Second)); err != nil {
		return err
	}
	count, err := c.discovery.WriteToUDPAddrPort(data, remote)
	if err != nil || count != len(data) {
		return fmt.Errorf("发现响应写出失败或短写: %v", err)
	}
	return c.emit("discovery_replied", 2)
}

func (c *Client) serviceDiscovery() error {
	if err := c.discovery.SetReadDeadline(time.Now().Add(time.Millisecond)); err != nil {
		return err
	}
	buffer := make([]byte, enetMaxDatagram)
	count, source, err := c.discovery.ReadFromUDPAddrPort(buffer)
	if err != nil {
		if isTimeout(err) {
			return nil
		}
		return err
	}
	source = netip.AddrPortFrom(source.Addr().Unmap(), source.Port())
	if source != c.config.RemoteDiscovery {
		return nil
	}
	node, err := DecodeNodePacket(buffer[:count])
	if err != nil {
		return err
	}
	if node.UserID != c.remoteNode.UserID {
		return fmt.Errorf("运行期间发现对端身份变化，不自动换目标")
	}
	order, err := node.SessionOrder(c.remoteNode)
	if err != nil {
		return err
	}
	if order != 0 {
		return fmt.Errorf("运行期间对端会话变化，不自动重连")
	}
	if node.Type == 1 {
		return c.replyDiscovery(source)
	}
	if node.Type != 2 {
		return fmt.Errorf("发现口收到未知运行期节点类型")
	}
	return nil
}

func isTimeout(err error) bool {
	value, ok := err.(interface{ Timeout() bool })
	return ok && value.Timeout()
}
