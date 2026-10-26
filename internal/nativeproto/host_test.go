// 本文件仅用loopback验证共享Host与三个实际Client的上线、单聊路由、目录合并及独立正常下线。
package nativeproto

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"reflect"
	"sync"
	"testing"
	"time"
)

// hostTestSocket 在指定loopback地址绑定临时UDP端口；失败终止测试，结束时关闭，不访问物理网卡。
func hostTestSocket(t *testing.T, address string) *net.UDPConn {
	t.Helper()
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.ParseIP(address)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

// hostTestConfig 生成显式常驻预算与支持单聊的合成资料；node由本次loopback socket生成，失败终止测试。
func hostTestConfig(t *testing.T, node NodePacket) ClientConfig {
	t.Helper()
	profile, err := ProbeHandshake(node.UserID)
	if err != nil {
		t.Fatal(err)
	}
	return ClientConfig{Node: node, Profile: profile, Transport: runtimeConfig(0, 1), LearnContacts: true,
		ShutdownTimeout: time.Second, DiscoveryTimeout: time.Second, HandshakeTimeout: 2 * time.Second}
}

// awaitHostSnapshot 等待公共快照满足条件并返回深拷贝；超时终止测试，不接触内部调度状态。
func awaitHostSnapshot(t *testing.T, host *Host, predicate func(ClientSnapshot) bool) ClientSnapshot {
	t.Helper()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(5 * time.Millisecond)
	defer tick.Stop()
	for {
		snapshot := host.Snapshot()
		if predicate(snapshot) {
			return snapshot
		}
		select {
		case <-tick.C:
		case <-deadline.C:
			t.Fatalf("Host快照未达预期: %+v", snapshot)
		}
	}
}

// TestHostThreeClientsSharedSockets 回归共享端口不能被单peer独占关闭，三身份目录不能互相覆盖，单聊不得串发到其他连接。
func TestHostThreeClientsSharedSockets(t *testing.T) {
	discovery, conn := hostTestSocket(t, "127.0.0.1"), hostTestSocket(t, "127.0.0.1")
	node := testClientNode(t, discovery, conn, "host-original-id", 2)
	config := hostTestConfig(t, node)
	var host *Host
	var persisted [][]Contact
	var persistMu sync.Mutex
	config.ObserveDirectory = func(directory *Directory) error {
		_ = host.Snapshot() // 持久化回调可读取Host，不得被Host.mu反向锁住。
		persistMu.Lock()
		persisted = append(persisted, directory.ListContacts())
		persistMu.Unlock()
		return nil
	}
	var seeds []netip.AddrPort
	var clients []*Client
	var cancels []context.CancelFunc
	var dones []chan residentRunResult
	messages := make(chan int, 12)
	ctx, stop := context.WithTimeout(context.Background(), 12*time.Second)
	defer stop()
	for i := 0; i < 3; i++ {
		address := fmt.Sprintf("127.0.0.%d", i+2)
		d, c := hostTestSocket(t, address), hostTestSocket(t, address)
		peerNode := testClientNode(t, d, c, fmt.Sprintf("peer-original-%d", i), byte(i+3))
		peerConfig := hostTestConfig(t, peerNode)
		peerConfig.RemoteDiscovery = discovery.LocalAddr().(*net.UDPAddr).AddrPort()
		client, err := NewClient(d, c, peerConfig)
		if err != nil {
			t.Fatal(err)
		}
		clients = append(clients, client)
		seeds = append(seeds, d.LocalAddr().(*net.UDPAddr).AddrPort())
		peerCtx, cancel := context.WithCancel(ctx)
		cancels = append(cancels, cancel)
		defer cancel()
		done := make(chan residentRunResult, 1)
		dones = append(dones, done)
		go func(index int, client *Client) {
			result, err := client.Run(peerCtx, func(event ClientEvent) error {
				if event.Stage == "unsupported_business_command" && event.Command == CommandUserMessage {
					messages <- index
				}
				return nil
			})
			done <- residentRunResult{result, err}
		}(i, client)
	}
	var err error
	host, err = NewHost(discovery, conn, HostConfig{Client: config, Prefix: netip.MustParsePrefix("127.0.0.0/8"), Seeds: seeds, MaxPeers: 3})
	if err != nil {
		t.Fatal(err)
	}
	hostCtx, cancelHost := context.WithCancel(ctx)
	defer cancelHost()
	hostDone := make(chan residentRunResult, 1)
	var stages []string
	go func() {
		result, err := host.Run(hostCtx, func(event ClientEvent) error {
			_ = host.Snapshot() // Web observer也可同步读取，不得死锁。
			stages = append(stages, event.Stage)
			return nil
		})
		hostDone <- residentRunResult{result, err}
	}()
	snapshot := awaitHostSnapshot(t, host, func(s ClientSnapshot) bool { return len(s.OnlineUserIDs) == 3 && len(s.Contacts) == 3 })
	if !snapshot.Online || snapshot.PeerUserID != "" || !reflect.DeepEqual(snapshot.OnlineUserIDs, []string{"peer-original-0", "peer-original-1", "peer-original-2"}) {
		t.Fatalf("多peer快照错误: %+v", snapshot)
	}
	snapshot.Contacts[0].Name = "changed outside"
	snapshot.OnlineUserIDs[0] = "changed outside"
	if host.Snapshot().Contacts[0].Name == "changed outside" || host.Snapshot().OnlineUserIDs[0] == "changed outside" {
		t.Fatal("Host快照未深拷贝")
	}
	for i, client := range clients {
		result, err := host.SendUser(ctx, client.Snapshot().OwnUserID, fmt.Sprintf("独立正文-%d", i))
		if err != nil || !result.Submitted || !result.TransportAcknowledged {
			t.Fatalf("发送%d: %+v %v", i, result, err)
		}
		select {
		case index := <-messages:
			if index != i {
				t.Fatalf("目标%d收到于%d", i, index)
			}
		case <-time.After(time.Second):
			t.Fatal("目标未收到单聊")
		}
	}
	cancels[0]()
	first := <-dones[0]
	if first.err != nil || !first.result.GracefulExit {
		t.Fatalf("首peer退出: %+v %v", first.result, first.err)
	}
	awaitHostSnapshot(t, host, func(s ClientSnapshot) bool { return len(s.OnlineUserIDs) == 2 && len(s.Contacts) == 3 })
	if _, err := host.SendUser(ctx, "peer-original-0", "不得发送离线身份"); err == nil {
		t.Fatal("已离线目标仍可发送")
	}
	if _, err := host.SendUser(ctx, "peer-original-2", "其余连接保留"); err != nil {
		t.Fatal(err)
	}
	select {
	case index := <-messages:
		if index != 2 {
			t.Fatal("剩余peer路由错误")
		}
	case <-time.After(time.Second):
		t.Fatal("剩余peer丢失")
	}
	cancelHost()
	finished := <-hostDone
	if finished.err != nil || !finished.result.ProtocolReady || !finished.result.GracefulExit {
		t.Fatalf("Host退出: %+v %v", finished.result, finished.err)
	}
	for i := 1; i < 3; i++ {
		finished := <-dones[i]
		if finished.err != nil || !finished.result.GracefulExit {
			t.Fatalf("peer%d退出: %+v %v", i, finished.result, finished.err)
		}
	}
	if host.Snapshot().Online || len(host.Snapshot().OnlineUserIDs) != 0 {
		t.Fatal("停止后仍在线")
	}
	persistMu.Lock()
	defer persistMu.Unlock()
	if len(persisted) != 3 || len(persisted[0]) != 1 || len(persisted[1]) != 2 || len(persisted[2]) != 3 {
		t.Fatalf("目录没有逐次合并: %+v", persisted)
	}
	ready, peerReady := 0, 0
	for _, stage := range stages {
		if stage == "protocol_ready" {
			ready++
		}
		if stage == "peer_ready" {
			peerReady++
		}
	}
	if ready != 1 || peerReady != 3 {
		t.Fatalf("ready=%d peerReady=%d", ready, peerReady)
	}
	if _, err := host.Run(ctx, func(ClientEvent) error { return nil }); err == nil {
		t.Fatal("Host被重复运行")
	}
}
