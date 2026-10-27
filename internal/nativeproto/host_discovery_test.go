// 本文件仅使用合成配置与loopback UDP验证有限次广播、发现边界、零peer运行预算及observer失败，不向物理网卡发送。
package nativeproto

import (
	"context"
	"encoding/binary"
	"errors"
	"net"
	"net/netip"
	"testing"
	"time"
)

// TestHostBroadcastDiscoveryLoopback 验证一次上线有限次广播，构造不发包且零peer仍发布Host就绪；广播地址严格限于127/8。
func TestHostBroadcastDiscoveryLoopback(t *testing.T) {
	discovery, conn := hostTestSocket(t, "127.0.0.1"), hostTestSocket(t, "127.0.0.1")
	node := testClientNode(t, discovery, conn, "host-broadcast-id", 3)
	cfg := hostTestConfig(t, node)
	cfg.Duration = 2300 * time.Millisecond
	target := netip.AddrPortFrom(netip.MustParseAddr("127.255.255.255"), node.DiscoveryPort)
	tap, err := net.ListenUDP("udp4", net.UDPAddrFromAddrPort(target))
	if err != nil {
		t.Fatal(err)
	}
	defer tap.Close()
	host, err := NewHost(discovery, conn, HostConfig{Client: cfg, Prefix: netip.MustParsePrefix("127.0.0.0/8"), Broadcast: target, MaxPeers: 3})
	if err != nil {
		t.Fatal(err)
	}
	buffer := make([]byte, 512)
	_ = tap.SetReadDeadline(time.Now().Add(15 * time.Millisecond))
	if _, _, err := tap.ReadFromUDPAddrPort(buffer); !isTimeout(err) {
		t.Fatalf("构造阶段已发送或读取失败: %v", err)
	}
	ready := 0
	result, err := host.Run(context.Background(), func(event ClientEvent) error {
		if event.Stage == "protocol_ready" {
			ready++
			if snapshot := host.Snapshot(); !snapshot.Online || len(snapshot.OnlineUserIDs) != 0 {
				t.Errorf("零peerHost就绪错误: %+v", snapshot)
			}
		}
		return nil
	})
	if err != nil || !result.ProtocolReady || !result.GracefulExit || result.DiscoveryAttempts != 3 || result.DiscoverySends != 3 || ready != 1 {
		t.Fatalf("广播结果=%+v ready=%d err=%v", result, ready, err)
	}
	_ = tap.SetReadDeadline(time.Now().Add(time.Second))
	for i := 0; i < 3; i++ {
		n, source, err := tap.ReadFromUDPAddrPort(buffer)
		if err != nil {
			t.Fatal(err)
		}
		packet, err := DecodeNodePacket(buffer[:n])
		if err != nil || packet.UserID != node.UserID || packet.Type != 1 || source.Port() != node.DiscoveryPort {
			t.Fatalf("公告错误: %+v %s %v", packet, source, err)
		}
	}
	_ = tap.SetReadDeadline(time.Now().Add(20 * time.Millisecond))
	if _, _, err := tap.ReadFromUDPAddrPort(buffer); !isTimeout(err) {
		t.Fatalf("广播重复发送: %v", err)
	}
}

// TestHostDiscoversAfterFirstBroadcastLost 模拟对端错过首个广播，验证同次上线第二次广播后仍能完成双向READY。
func TestHostDiscoversAfterFirstBroadcastLost(t *testing.T) {
	discovery, conn := hostTestSocket(t, "127.0.0.1"), hostTestSocket(t, "127.0.0.1")
	peerDiscovery, peerConn := hostTestSocket(t, "127.0.0.2"), hostTestSocket(t, "127.0.0.2")
	node := testClientNode(t, discovery, conn, "host-broadcast-retry", 9)
	peerNode := testClientNode(t, peerDiscovery, peerConn, "peer-broadcast-retry", 1)
	target := netip.AddrPortFrom(netip.MustParseAddr("127.255.255.255"), node.DiscoveryPort)
	tap, err := net.ListenUDP("udp4", net.UDPAddrFromAddrPort(target))
	if err != nil {
		t.Fatal(err)
	}
	defer tap.Close()
	host, err := NewHost(discovery, conn, HostConfig{Client: hostTestConfig(t, node), Prefix: netip.MustParsePrefix("127.0.0.0/8"), Broadcast: target, MaxPeers: 3})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	hostDone := make(chan residentRunResult, 1)
	go func() {
		result, err := host.Run(ctx, func(ClientEvent) error { return nil })
		hostDone <- residentRunResult{result, err}
	}()
	_ = tap.SetReadDeadline(time.Now().Add(3 * time.Second))
	buffer := make([]byte, 512)
	for i := 0; i < 2; i++ {
		if _, _, err := tap.ReadFromUDPAddrPort(buffer); err != nil {
			t.Fatal(err)
		}
	}
	peerCtx, cancelPeer := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancelPeer()
	peerDone := make(chan error, 1)
	go func() { peerDone <- serveSyntheticNative(peerCtx, peerDiscovery, peerConn, peerNode, true) }()
	wire, err := peerNode.Encode()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := peerDiscovery.WriteToUDPAddrPort(wire, discovery.LocalAddr().(*net.UDPAddr).AddrPort()); err != nil {
		t.Fatal(err)
	}
	awaitHostSnapshot(t, host, func(snapshot ClientSnapshot) bool { return len(snapshot.OnlineUserIDs) == 1 })
	cancel()
	if result := <-hostDone; result.err != nil || !result.result.GracefulExit {
		t.Fatalf("Host退出失败: %+v", result)
	}
	if err := <-peerDone; err != nil && !errors.Is(err, context.Canceled) {
		t.Fatalf("合成对端退出失败: %v", err)
	}
}

// TestHostConfigurationDoesNotOwnSocketsOnFailure 验证非法广播、种子和容量在边界拒绝，构造失败不关闭socket；发现口允许0.0.0.0。
func TestHostConfigurationDoesNotOwnSocketsOnFailure(t *testing.T) {
	discovery, conn := hostTestSocket(t, "0.0.0.0"), hostTestSocket(t, "127.0.0.1")
	var guid [16]byte
	guid[0] = 1
	node, err := NewNodePacket(NodeRegistration{UserID: "host-wildcard-id", SessionGUID: guid, Address: netip.MustParseAddr("127.0.0.1"), Broadcast: netip.MustParseAddr("127.255.255.255"), DiscoveryPort: discovery.LocalAddr().(*net.UDPAddr).AddrPort().Port(), ENetPort: conn.LocalAddr().(*net.UDPAddr).AddrPort().Port()}, 1)
	if err != nil {
		t.Fatal(err)
	}
	base := HostConfig{Client: hostTestConfig(t, node), Prefix: netip.MustParsePrefix("127.0.0.0/8"), MaxPeers: 3}
	for _, change := range []func(*HostConfig){
		func(cfg *HostConfig) { cfg.MaxPeers = 257 },
		func(cfg *HostConfig) { cfg.MaxPeers = 0 },
		func(cfg *HostConfig) { cfg.Broadcast = netip.MustParseAddrPort("255.255.255.255:9011") },
		func(cfg *HostConfig) { cfg.Broadcast = netip.MustParseAddrPort("127.255.255.255:1") },
		func(cfg *HostConfig) { cfg.Seeds = []netip.AddrPort{netip.MustParseAddrPort("192.0.2.10:9011")} },
		func(cfg *HostConfig) { cfg.Seeds = []netip.AddrPort{netip.MustParseAddrPort("127.0.0.1:9011")} },
	} {
		cfg := base
		change(&cfg)
		if _, err := NewHost(discovery, conn, cfg); err == nil {
			t.Fatalf("接受非法Host配置: %+v", cfg)
		}
		if err := discovery.SetReadDeadline(time.Time{}); err != nil {
			t.Fatalf("错误关闭发现socket: %v", err)
		}
		if err := conn.SetReadDeadline(time.Time{}); err != nil {
			t.Fatalf("错误关闭ENet socket: %v", err)
		}
	}
	if _, err := NewHost(discovery, conn, base); err != nil {
		t.Fatalf("拒绝wildcard发现绑定: %v", err)
	}
}

// TestHostRejectsDiscoveryNoise 验证错误长度/类型/字段/FEATURE不会启动peer或杀死Host，未知ENet来源直接忽略。
func TestHostRejectsDiscoveryNoise(t *testing.T) {
	discovery, conn := hostTestSocket(t, "127.0.0.1"), hostTestSocket(t, "127.0.0.1")
	d, c := hostTestSocket(t, "127.0.0.2"), hostTestSocket(t, "127.0.0.2")
	local := testClientNode(t, discovery, conn, "host-noise-id", 4)
	remote := testClientNode(t, d, c, "noise-original-id", 2)
	host, err := NewHost(discovery, conn, HostConfig{Client: hostTestConfig(t, local), Prefix: netip.MustParsePrefix("127.0.0.0/8"), MaxPeers: 3})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	events := make(chan ClientEvent, 32)
	done := make(chan residentRunResult, 1)
	go func() {
		result, err := host.Run(ctx, func(event ClientEvent) error { events <- event; return nil })
		done <- residentRunResult{result, err}
	}()
	awaitResidentEvent(t, events, "protocol_ready", "")
	wire, _ := remote.Encode()
	packets := [][]byte{{1}, append([]byte(nil), wire...), append([]byte(nil), wire...), append([]byte(nil), wire...), append([]byte(nil), wire...)}
	binary.BigEndian.PutUint32(packets[1][4:], 4)
	packets[2][16] = 126
	binary.BigEndian.PutUint16(packets[3][0x18:], 1)
	clear(packets[4][12:16])
	for _, packet := range packets {
		if _, err := d.WriteToUDPAddrPort(packet, discovery.LocalAddr().(*net.UDPAddr).AddrPort()); err != nil {
			t.Fatal(err)
		}
		awaitResidentEvent(t, events, "discovery_rejected", "")
	}
	if _, err := c.WriteToUDPAddrPort([]byte("unknown ENet noise"), conn.LocalAddr().(*net.UDPAddr).AddrPort()); err != nil {
		t.Fatal(err)
	}
	if snapshot := host.Snapshot(); !snapshot.Online || len(snapshot.OnlineUserIDs) != 0 || len(snapshot.Contacts) != 0 {
		t.Fatalf("噪声改变Host状态: %+v", snapshot)
	}
	cancel()
	result := <-done
	if result.err != nil || !result.result.GracefulExit {
		t.Fatalf("噪声杀死Host: %+v %v", result.result, result.err)
	}
}

// TestHostAcceptsZeroDiscoveryAddress 回归真实Windows发现响应的公告IP为0.0.0.0；连接地址必须使用已限定子网的UDP实际来源。
func TestHostAcceptsZeroDiscoveryAddress(t *testing.T) {
	discovery, conn := hostTestSocket(t, "127.0.0.1"), hostTestSocket(t, "127.0.0.1")
	remoteD, remoteE := hostTestSocket(t, "127.0.0.2"), hostTestSocket(t, "127.0.0.2")
	node := testClientNode(t, discovery, conn, "host-zero-address", 4)
	remote := testClientNode(t, remoteD, remoteE, "peer-zero-address", 2)
	host, err := NewHost(discovery, conn, HostConfig{Client: hostTestConfig(t, node), Prefix: netip.MustParsePrefix("127.0.0.0/8"), MaxPeers: 1})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	events := make(chan ClientEvent, 32)
	done := make(chan residentRunResult, 1)
	go func() {
		result, err := host.Run(ctx, func(event ClientEvent) error { events <- event; return nil })
		done <- residentRunResult{result, err}
	}()
	defer func() { cancel(); <-done }()
	awaitResidentEvent(t, events, "protocol_ready", "")
	wire, err := remote.Encode()
	if err != nil {
		t.Fatal(err)
	}
	clear(wire[16:20])
	if _, err := remoteD.WriteToUDPAddrPort(wire, discovery.LocalAddr().(*net.UDPAddr).AddrPort()); err != nil {
		t.Fatal(err)
	}
	deadline := time.After(2 * time.Second)
	for {
		select {
		case event := <-events:
			if event.Stage == "discovery_rejected" {
				t.Fatalf("正常零公告IP发现响应被拒绝：%s", event.Error)
			}
			if event.Stage == "initialized" {
				host.mu.Lock()
				peer := host.peers[remoteD.LocalAddr().(*net.UDPAddr).AddrPort()]
				actual := peer.client.config.RemoteDiscovery
				host.mu.Unlock()
				if actual != remoteD.LocalAddr().(*net.UDPAddr).AddrPort() {
					t.Fatal("未使用实际UDP来源建立连接")
				}
				return
			}
		case <-deadline:
			t.Fatal("正常发现响应未创建连接")
		}
	}
}

// TestHostObserverFailureStops 验证就绪记录失败向调用者返回原错误并关闭真实socket，不把错误吞掉继续运行。
func TestHostObserverFailureStops(t *testing.T) {
	discovery, conn := hostTestSocket(t, "127.0.0.1"), hostTestSocket(t, "127.0.0.1")
	node := testClientNode(t, discovery, conn, "host-observer-id", 3)
	host, err := NewHost(discovery, conn, HostConfig{Client: hostTestConfig(t, node), Prefix: netip.MustParsePrefix("127.0.0.0/8"), MaxPeers: 1})
	if err != nil {
		t.Fatal(err)
	}
	recordErr := errors.New("synthetic Host observer failure")
	_, err = host.Run(context.Background(), func(ClientEvent) error { _ = host.Snapshot(); return recordErr })
	if !errors.Is(err, recordErr) || host.Snapshot().Online {
		t.Fatalf("observer错误未终止Host: %v", err)
	}
	if err := discovery.SetReadDeadline(time.Time{}); !errors.Is(err, net.ErrClosed) {
		t.Fatalf("发现口未关闭: %v", err)
	}
	if err := conn.SetReadDeadline(time.Time{}); !errors.Is(err, net.ErrClosed) {
		t.Fatalf("ENet口未关闭: %v", err)
	}
}
