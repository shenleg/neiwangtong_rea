// 本文件用 loopback 原生协议模拟端验证限时发现、双方角色、应用上线和退出；不触及真实客户端。
package nativeproto

import (
	"bytes"
	"context"
	"encoding/binary"
	"io"
	"net"
	"net/netip"
	"strings"
	"testing"
	"time"
)

// testClientNode 生成具有显式会话排序的合成节点；socket 均由 t 的本机 fixture 提供，失败终止测试。
func testClientNode(t *testing.T, discovery, conn *net.UDPConn, id string, order byte) NodePacket {
	t.Helper()
	var guid [16]byte
	guid[0] = order
	d := discovery.LocalAddr().(*net.UDPAddr).AddrPort()
	e := conn.LocalAddr().(*net.UDPAddr).AddrPort()
	node, err := NewNodePacket(NodeRegistration{UserID: id, SessionGUID: guid, Address: d.Addr(), Broadcast: netip.MustParseAddr("127.255.255.255"), DiscoveryPort: d.Port(), ENetPort: e.Port()}, 1)
	if err != nil {
		t.Fatal(err)
	}
	return node
}

// TestClientBothRoles 验证一次单播发现按会话 GUID 而非 IP 决定角色，真实 loopback 交换1000/1018/1017。
func TestClientBothRoles(t *testing.T) {
	for _, active := range []bool{true, false} {
		t.Run(map[bool]string{true: "active", false: "passive"}[active], func(t *testing.T) {
			localDiscovery, localConn := udpTestSocket(t), udpTestSocket(t)
			remoteDiscovery, remoteConn := udpTestSocket(t), udpTestSocket(t)
			localOrder, remoteOrder := byte(1), byte(2)
			if active {
				localOrder, remoteOrder = 2, 1
			}
			localNode := testClientNode(t, localDiscovery, localConn, "synthetic-client", localOrder)
			remoteNode := testClientNode(t, remoteDiscovery, remoteConn, "synthetic-peer", remoteOrder)
			profile, err := ProbeHandshake("Synthetic Go node")
			if err != nil {
				t.Fatal(err)
			}
			client, err := NewClient(localDiscovery, localConn, ClientConfig{Node: localNode, Profile: profile, RemoteDiscovery: remoteDiscovery.LocalAddr().(*net.UDPAddr).AddrPort(),
				Transport: runtimeConfig(0, 1), Duration: 600 * time.Millisecond, ShutdownTimeout: 500 * time.Millisecond, DiscoveryTimeout: time.Second, HandshakeTimeout: time.Second})
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			peerDone := make(chan error, 1)
			go func() { peerDone <- serveSyntheticNative(ctx, remoteDiscovery, remoteConn, remoteNode, active) }()
			var stages []ClientEvent
			result, err := client.Run(ctx, func(event ClientEvent) error { stages = append(stages, event); return nil })
			if err != nil {
				t.Fatal(err)
			}
			if err := <-peerDone; err != nil {
				t.Fatal(err)
			}
			if !result.ProtocolReady || !result.GracefulExit || result.DiscoverySends != 1 {
				t.Fatalf("result=%+v", result)
			}
			if result.Role != map[bool]string{true: "active", false: "passive"}[active] {
				t.Fatal("wrong role")
			}
			ready := false
			for _, event := range stages {
				if event.Stage == "protocol_ready" {
					ready = true
				}
			}
			if !ready {
				t.Fatal("missing ready event")
			}
			if _, err := client.Run(ctx, func(ClientEvent) error { return nil }); err == nil {
				t.Fatal("reused live attempt")
			}
		})
	}
}

// serveSyntheticNative 作为单个本机协议对端响应一次发现与握手，最后验证 QUIT 并正常关闭传输。
// 返回第一处解析/I/O错误；不会自动重试应用控制消息，不创建额外连接。
func serveSyntheticNative(ctx context.Context, discovery, conn *net.UDPConn, node NodePacket, localActive bool) error {
	if err := discovery.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		return err
	}
	buffer := make([]byte, 4096)
	n, source, err := discovery.ReadFromUDPAddrPort(buffer)
	if err != nil {
		return err
	}
	remote, err := DecodeNodePacket(buffer[:n])
	if err != nil {
		return err
	}
	if remote.Type != 1 {
		return io.ErrUnexpectedEOF
	}
	node.Type = 2
	reply, err := node.Encode()
	if err != nil {
		return err
	}
	if _, err := discovery.WriteToUDPAddrPort(reply, source); err != nil {
		return err
	}
	transport, err := NewUDPTransport(conn, netip.AddrPortFrom(source.Addr(), remote.ENetPort), runtimeConfig(1, 2))
	if err != nil {
		return err
	}
	defer func() { _ = transport.Close(ctx) }()
	if localActive {
		err = transport.Accept(ctx)
	} else {
		err = transport.Connect(ctx)
	}
	if err != nil {
		return err
	}
	profile := syntheticHandshake()
	profile.Features = append(profile.Features, XMLField{Name: "MSG", Value: "2:1"})
	if !localActive {
		node.Type = 4
		data, err := node.Encode()
		if err != nil {
			return err
		}
		if err := transport.Send(ctx, data); err != nil {
			return err
		}
		frame, err := EncodeHandshake(profile)
		if err != nil {
			return err
		}
		data, err = Encode(frame.Command, frame.Payload)
		if err != nil {
			return err
		}
		if err := transport.Send(ctx, data); err != nil {
			return err
		}
	}
	sentHandshake := !localActive
	sentReady := false
	messageCompatible := false
	sendReady := func() error {
		response, err := EncodeReady(Ready{UserID: node.UserID})
		if err != nil {
			return err
		}
		wire, err := Encode(response.Command, response.Payload)
		if err != nil {
			return err
		}
		if err := transport.Send(ctx, wire); err != nil {
			return err
		}
		sentReady = true
		return nil
	}
	for {
		data, err := transport.Receive(ctx)
		if err != nil {
			return err
		}
		if len(data) == 304 && binary.BigEndian.Uint32(data[4:]) == 4 {
			continue
		}
		frame, err := Decode(bytes.NewReader(data), 1<<20)
		if err != nil {
			return err
		}
		if frame.Command == CommandHandshake {
			other, err := DecodeHandshake(frame)
			if err != nil {
				return err
			}
			for _, field := range other.Features {
				if field.Name == "MSG" {
					for _, version := range strings.Split(field.Value, ":") {
						if version == "1" || version == "2" {
							messageCompatible = true
						}
					}
				}
			}
			if !sentHandshake {
				response, err := EncodeHandshake(profile)
				if err != nil {
					return err
				}
				wire, err := Encode(response.Command, response.Payload)
				if err != nil {
					return err
				}
				if err := transport.Send(ctx, wire); err != nil {
					return err
				}
				sentHandshake = true
			}
			if !sentReady && messageCompatible {
				if err := sendReady(); err != nil {
					return err
				}
			}
		} else if frame.Command == CommandReady {
			ready, err := DecodeReady(frame)
			if err != nil || ready.UserID != remote.UserID {
				return io.ErrUnexpectedEOF
			}
			if !messageCompatible {
				return transport.Close(ctx)
			}
		} else if frame.Command == CommandQuit {
			if err := DecodeControl(frame); err != nil {
				return err
			}
			_, err := transport.Receive(ctx)
			if err != io.EOF {
				return err
			}
			return transport.Close(ctx)
		} else if frame.Command != CommandHeartbeat {
			return io.ErrUnexpectedEOF
		}
	}
}
