// 本文件以已有失败阶段记录构造确定性断开回放；只验证错误传播与未达成验收，不猜测 Windows 断开的原因。
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

// TestClientDisconnectAfterLocalReady 保证本机1000/1018获传输ACK而未收远端READY时，不能将EOF误报成上线。
func TestClientDisconnectAfterLocalReady(t *testing.T) {
	discovery, conn := udpTestSocket(t), udpTestSocket(t)
	peerDiscovery, peerConn := udpTestSocket(t), udpTestSocket(t)
	local := testClientNode(t, discovery, conn, "synthetic-client", 1)
	remote := testClientNode(t, peerDiscovery, peerConn, "synthetic-peer", 2)
	profile, err := ProbeHandshake("Synthetic probe")
	if err != nil {
		t.Fatal(err)
	}
	client, err := NewClient(discovery, conn, ClientConfig{Node: local, Profile: profile, RemoteDiscovery: peerDiscovery.LocalAddr().(*net.UDPAddr).AddrPort(),
		Transport: runtimeConfig(0, 1), Duration: time.Second, ShutdownTimeout: time.Second, DiscoveryTimeout: time.Second, HandshakeTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- closeAfterReady(ctx, peerDiscovery, peerConn, remote) }()
	var disconnectData *uint32
	result, err := client.Run(ctx, func(event ClientEvent) error {
		if event.Stage == "peer_disconnected" {
			disconnectData = event.DisconnectData
		}
		return nil
	})
	if err == nil || !errors.Is(err, io.EOF) || result.ProtocolReady || result.GracefulExit {
		t.Fatalf("partial handshake reported success: result=%+v err=%v", result, err)
	}
	if disconnectData == nil || *disconnectData != 0 {
		t.Fatal("missing remote DISCONNECT reason code")
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

// closeAfterReady 复现阶段序列：对端发节点/1000、确认本机1000和1018后断开；不将此fixture当实际拒绝策略。
func closeAfterReady(ctx context.Context, discovery, conn *net.UDPConn, node NodePacket) error {
	if err := discovery.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
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
	node.Type = 2
	wire, err := node.Encode()
	if err != nil {
		return err
	}
	if _, err := discovery.WriteToUDPAddrPort(wire, source); err != nil {
		return err
	}
	transport, err := NewUDPTransport(conn, netip.AddrPortFrom(source.Addr(), remote.ENetPort), runtimeConfig(1, 2))
	if err != nil {
		return err
	}
	defer func() { _ = transport.Close(ctx) }()
	if err := transport.Connect(ctx); err != nil {
		return err
	}
	node.Type = 4
	wire, err = node.Encode()
	if err != nil {
		return err
	}
	if err := transport.Send(ctx, wire); err != nil {
		return err
	}
	frame, err := EncodeHandshake(syntheticHandshake())
	if err != nil {
		return err
	}
	wire, err = Encode(frame.Command, frame.Payload)
	if err != nil {
		return err
	}
	if err := transport.Send(ctx, wire); err != nil {
		return err
	}
	for {
		wire, err := transport.Receive(ctx)
		if err != nil {
			return err
		}
		frame, err := Decode(bytes.NewReader(wire), 1<<20)
		if err != nil {
			return err
		}
		if frame.Command == CommandReady {
			return transport.Close(ctx)
		}
	}
}
