// 本文件用真实Host和合成Client验证Web默认子网发现、联系人持久化、单聊传输确认及正常停止；仅使用临时目录和loopback。
package nativeweb

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"testing"
	"time"

	"neiwangtong-gateway/internal/nativeproto"
)

// nativeIntegrationSocket 在指定loopback地址绑定临时UDP端口，返回未连接的socket；绑定失败终止测试，测试结束时关闭。
func nativeIntegrationSocket(t *testing.T, address string) *net.UDPConn {
	t.Helper()
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.ParseIP(address)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

// awaitNativeIntegrationStage 等待真实管理器记录指定协议阶段；失败或三秒内未到达时报告状态并终止测试。
func awaitNativeIntegrationStage(t *testing.T, manager *Manager, stage string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		state := manager.State()
		for _, event := range state.Events {
			if event.Stage == stage {
				return
			}
		}
		if state.Phase == "failed" {
			t.Fatalf("等待%s时会话失败：%+v", stage, state)
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("未记录%s：%+v", stage, manager.State())
}

// TestNativeStopWhileWaitingForContacts 验证Host已就绪但没有联系人时可正常下线，不把主动取消显示为失败。
func TestNativeStopWhileWaitingForContacts(t *testing.T) {
	localD := nativeIntegrationSocket(t, "127.0.0.1")
	localE := nativeIntegrationSocket(t, "127.0.0.1")
	config := Config{Local: netip.MustParseAddr("127.0.0.1"), Prefix: netip.MustParsePrefix("127.0.0.0/8"),
		Name: "合成停止测试", DataDir: t.TempDir(),
		DiscoveryPort: localD.LocalAddr().(*net.UDPAddr).AddrPort().Port(), ENetPort: localE.LocalAddr().(*net.UDPAddr).AddrPort().Port()}
	if err := localD.Close(); err != nil {
		t.Fatal(err)
	}
	if err := localE.Close(); err != nil {
		t.Fatal(err)
	}
	manager, err := New(config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Close(context.Background()) })
	if err := manager.Start(manager.State().Name); err != nil {
		t.Fatal(err)
	}
	awaitNativeIntegrationStage(t, manager, "protocol_ready")
	manager.Stop()
	state := awaitPhase(t, manager, "offline")
	if state.Error != "" {
		t.Fatalf("主动取消仍显示为失败：%+v", state)
	}
}

// TestNativeIntegrationManyOnlineClients 验证Web一次上线能同时显示57个合成在线客户端，不依赖历史联系人。
func TestNativeIntegrationManyOnlineClients(t *testing.T) {
	const count = 57
	localD := nativeIntegrationSocket(t, "127.0.0.1")
	localE := nativeIntegrationSocket(t, "127.0.0.1")
	config := Config{Local: netip.MustParseAddr("127.0.0.1"), Prefix: netip.MustParsePrefix("127.0.0.0/8"),
		Name: "合成Web节点", DataDir: t.TempDir(),
		DiscoveryPort: localD.LocalAddr().(*net.UDPAddr).AddrPort().Port(), ENetPort: localE.LocalAddr().(*net.UDPAddr).AddrPort().Port()}
	if err := localD.Close(); err != nil {
		t.Fatal(err)
	}
	if err := localE.Close(); err != nil {
		t.Fatal(err)
	}
	manager, err := New(config)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, count)
	started := 0
	defer func() {
		closeCtx, stop := context.WithTimeout(context.Background(), 10*time.Second)
		defer stop()
		if err := manager.Close(closeCtx); err != nil {
			t.Errorf("关闭Web: %v", err)
		}
		cancel()
		for i := 0; i < started; i++ {
			select {
			case <-done:
			case <-closeCtx.Done():
				t.Error("合成对端未退出")
				return
			}
		}
	}()
	if err := manager.Start(config.Name); err != nil {
		t.Fatal(err)
	}
	awaitNativeIntegrationStage(t, manager, "protocol_ready")
	for i := 0; i < count; i++ {
		address := fmt.Sprintf("127.0.0.%d", i+2)
		d := nativeIntegrationSocket(t, address)
		e := nativeIntegrationSocket(t, address)
		dAddr := d.LocalAddr().(*net.UDPAddr).AddrPort()
		node, err := nativeproto.NewNodePacket(nativeproto.NodeRegistration{UserID: fmt.Sprintf("synthetic-%d", i), SessionGUID: [16]byte{byte(i + 1)},
			Address: dAddr.Addr(), Broadcast: netip.MustParseAddr("127.255.255.255"),
			DiscoveryPort: dAddr.Port(), ENetPort: e.LocalAddr().(*net.UDPAddr).AddrPort().Port()}, 1)
		if err != nil {
			t.Fatal(err)
		}
		profile, err := nativeproto.ProbeHandshake(node.UserID)
		if err != nil {
			t.Fatal(err)
		}
		peer, err := nativeproto.NewClient(d, e, nativeproto.ClientConfig{Node: node, Profile: profile,
			RemoteDiscovery: netip.AddrPortFrom(config.Local, config.DiscoveryPort), LearnContacts: true,
			Transport: nativeproto.ENetConfig{LocalPeerID: 1, ConnectID: [4]byte{byte(i + 1)}, MTU: 1400, WindowBytes: 65536,
				MaxMessageBytes: 1 << 20, MaxQueuedBytes: 2 << 20, MaxPendingCommands: 1024,
				RetransmitAfter: 100 * time.Millisecond, MaxAttempts: 6, ReceiveTimeout: 10 * time.Second, PingInterval: time.Second},
			ShutdownTimeout: 3 * time.Second, DiscoveryTimeout: 3 * time.Second, HandshakeTimeout: 5 * time.Second})
		if err != nil {
			t.Fatal(err)
		}
		started++
		go func() {
			_, err := peer.Run(ctx, func(nativeproto.ClientEvent) error { return nil })
			done <- err
		}()
	}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		state := manager.State()
		online := 0
		for _, contact := range state.Contacts {
			if contact.Online {
				online++
			}
		}
		if online == count && len(state.Contacts) == count {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("未发现全部合成客户端: %+v", manager.State())
}

// TestNativeIntegrationFirstStartSendAndRestore 验证默认openNative首次生成身份、READY后学习联系人、真实1004传输及下线后只读恢复。
func TestNativeIntegrationFirstStartSendAndRestore(t *testing.T) {
	for _, mode := range []string{"manager", "public_api"} {
		t.Run(mode, func(t *testing.T) { nativeFirstStartSendAndRestore(t, mode == "public_api") })
	}
}

// nativeFirstStartSendAndRestore 以临时loopback身份验证完整运行；viaHTTP选择公开名称发送接口，否则直接调用Manager。
// 初始化、就绪、提交、退出或身份恢复不符立即终止；没有真实对端或持久私有资料。
func nativeFirstStartSendAndRestore(t *testing.T, viaHTTP bool) {
	t.Helper()
	localDiscovery := nativeIntegrationSocket(t, "127.0.0.1")
	localENet := nativeIntegrationSocket(t, "127.0.0.1")
	peerDiscovery := nativeIntegrationSocket(t, "127.0.0.2")
	peerENet := nativeIntegrationSocket(t, "127.0.0.2")
	localDiscoveryAddr := localDiscovery.LocalAddr().(*net.UDPAddr).AddrPort()
	peerDiscoveryAddr := peerDiscovery.LocalAddr().(*net.UDPAddr).AddrPort()
	config := Config{Local: localDiscoveryAddr.Addr(), Prefix: netip.MustParsePrefix("127.0.0.0/8"),
		Name: "合成Web集成节点", DataDir: t.TempDir(),
		DiscoveryPort: localDiscoveryAddr.Port(), ENetPort: localENet.LocalAddr().(*net.UDPAddr).AddrPort().Port()}
	manager, err := New(config)
	if err != nil {
		t.Fatal(err)
	}
	if state := manager.State(); state.Phase != "offline" || state.OwnUserID != "" || len(state.Contacts) != 0 {
		t.Fatalf("首次构造不应生成身份或联系人：%+v", state)
	}
	identityPath := filepath.Join(config.DataDir, "identity")
	if _, err := os.Stat(identityPath); !os.IsNotExist(err) {
		t.Fatalf("只读构造创建了身份或检查失败：%v", err)
	}

	const peerID = "synthetic-nativeweb-peer"
	const peerName = "合成独立对端"
	node, err := nativeproto.NewNodePacket(nativeproto.NodeRegistration{UserID: peerID, SessionGUID: [16]byte{1},
		Address: peerDiscoveryAddr.Addr(), Broadcast: netip.MustParseAddr("127.255.255.255"),
		DiscoveryPort: peerDiscoveryAddr.Port(), ENetPort: peerENet.LocalAddr().(*net.UDPAddr).AddrPort().Port()}, 1)
	if err != nil {
		t.Fatal(err)
	}
	profile, err := nativeproto.ProbeHandshake(peerName)
	if err != nil {
		t.Fatal(err)
	}
	peer, err := nativeproto.NewClient(peerDiscovery, peerENet, nativeproto.ClientConfig{
		Node: node, Profile: profile, RemoteDiscovery: localDiscoveryAddr, LearnContacts: true,
		Transport: nativeproto.ENetConfig{LocalPeerID: 1, ConnectID: [4]byte{1, 2, 3, 4}, MTU: 1400, WindowBytes: 65536,
			MaxMessageBytes: 1 << 20, MaxQueuedBytes: 2 << 20, MaxPendingCommands: 1024,
			RetransmitAfter: 100 * time.Millisecond, MaxAttempts: 6, ReceiveTimeout: 10 * time.Second, PingInterval: time.Second},
		ShutdownTimeout: 3 * time.Second, DiscoveryTimeout: 3 * time.Second, HandshakeTimeout: 3 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancelPeer := context.WithTimeout(context.Background(), 15*time.Second)
	peerDone, peerReady := make(chan struct{}), make(chan struct{})
	received := make(chan nativeproto.ClientEvent, 1)
	var peerResult nativeproto.ClientResult
	var peerErr error
	peerStarted := false
	defer func() {
		closeCtx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cancel()
		// 先让管理器退出，保留对端运行循环处理QUIT和断开ACK；失败时也等待对端结束，避免泄漏协程。
		if err := manager.Close(closeCtx); err != nil {
			t.Errorf("关闭真实管理器：%v", err)
		}
		cancelPeer()
		if peerStarted {
			select {
			case <-peerDone:
			case <-closeCtx.Done():
				t.Error("合成对端未退出")
			}
		}
	}()

	// 临时占用两端口以保证它们不同，交还后由默认openNative自行绑定；不替换session factory。
	if err := localDiscovery.Close(); err != nil {
		t.Fatal(err)
	}
	if err := localENet.Close(); err != nil {
		t.Fatal(err)
	}
	startName := config.Name
	if viaHTTP {
		startName = "自定义上线名称 <&>🙂"
		body, err := json.Marshal(map[string]string{"name": startName})
		if err != nil {
			t.Fatal(err)
		}
		response := performRequest(testHandler(t, manager), http.MethodPost, "/api/session/start", string(body))
		if response.Code != http.StatusAccepted {
			t.Fatalf("自定义名称上线未受理：%d %s", response.Code, response.Body.String())
		}
	} else if err := manager.Start(startName); err != nil {
		t.Fatal(err)
	}
	// Host就绪后由合成对端主动公告，验证默认子网接收，不配置单播种子或替换session factory。
	awaitNativeIntegrationStage(t, manager, "protocol_ready")
	peerStarted = true
	go func() {
		peerResult, peerErr = peer.Run(ctx, func(event nativeproto.ClientEvent) error {
			if event.Stage == "protocol_ready" {
				close(peerReady)
			}
			if event.Stage == "unsupported_business_command" && event.Command == nativeproto.CommandUserMessage {
				received <- event
			}
			return nil
		})
		close(peerDone)
	}()
	awaitNativeIntegrationStage(t, manager, "peer_ready")
	state := manager.State()
	select {
	case <-peerReady:
	case <-peerDone:
		t.Fatalf("对端未就绪即退出：%+v %v", peerResult, peerErr)
	case <-ctx.Done():
		t.Fatal("对端未完成READY")
	}
	if state.Name != startName || state.OwnUserID == "" || state.OwnUserID == peerID || state.Error != "" || state.StartedAt == nil || len(state.Contacts) != 1 {
		t.Fatalf("真实上线状态错误：%+v", state)
	}
	contact := state.Contacts[0]
	if contact.UserID != peerID || contact.Name != peerName || contact.Discovery != peerDiscoveryAddr || !contact.Online || !contact.Sendable || contact.Reason != "" {
		t.Fatalf("READY后未学习可发送联系人：%+v", contact)
	}
	identity, err := os.ReadFile(identityPath)
	if err != nil || string(identity) != state.OwnUserID {
		t.Fatalf("生成的身份未持久化：%q %v", identity, err)
	}
	peerSnapshot := peer.Snapshot()
	if !peerSnapshot.Online || peerSnapshot.PeerUserID != state.OwnUserID || len(peerSnapshot.Contacts) != 1 || peerSnapshot.Contacts[0].Name != startName {
		t.Fatalf("独立对端未确认Web节点身份及资料：%+v", peerSnapshot)
	}
	// 通过New只读恢复，检查真实ObserveDirectory已在发布online前保存了可用目录，而非只更新内存。
	restored, err := New(config)
	if err != nil {
		t.Fatal(err)
	}
	if saved := restored.State(); saved.Phase != "offline" || saved.OwnUserID != state.OwnUserID || len(saved.Contacts) != 1 || saved.Contacts[0].Contact != contact.Contact || saved.Contacts[0].Sendable || saved.Contacts[0].Online {
		t.Fatalf("在线时持久化的目录无法离线恢复：%+v", saved)
	}

	text := "Web真实模块合成单聊：第一行\n第二行\r\n<&>\"'\\🙂"
	var response MessageResponse
	var status int
	if viaHTTP {
		body, err := json.Marshal(map[string]string{"name": peerName, "text": text})
		if err != nil {
			t.Fatal(err)
		}
		recorded := performRequest(testHandler(t, manager), http.MethodPost, "/send/user", string(body))
		var public publicSendResponse
		if err := json.Unmarshal(recorded.Body.Bytes(), &public); err != nil {
			t.Fatal(err)
		}
		status = recorded.Code
		response = MessageResponse{RequestID: public.RequestID, Result: public.ChatResult, Error: public.Error}
	} else {
		response, status = manager.Send(ctx, "native-integration-user-1", nativeproto.ChatRequest{UserID: peerID, Text: text})
	}
	if status != http.StatusOK || response.Error != "" || response.Result.Command != nativeproto.CommandUserMessage || response.Result.MessageID == "" || !response.Result.Submitted || !response.Result.TransportAcknowledged || response.Result.Delivery != "unknown" {
		t.Fatalf("真实SendUser结果：%d %+v", status, response)
	}
	// Client公开接口不暴露入站正文，也不提供任意群推送；此处验证对端确实收到1004，全文解析及3003/3011留在nativeproto测试。
	select {
	case <-received:
	case <-peerDone:
		t.Fatalf("对端收到1004前退出：%+v %v", peerResult, peerErr)
	case <-ctx.Done():
		t.Fatal("独立对端未观察到1004")
	}
	manager.Stop()
	stopped := awaitPhase(t, manager, "offline")
	if stopped.Error != "" || stopped.OwnUserID != state.OwnUserID || len(stopped.Contacts) != 1 || stopped.Contacts[0].Contact != contact.Contact || stopped.Contacts[0].Sendable || stopped.Contacts[0].Online {
		t.Fatalf("停止后应保留目录且撤销在线状态：%+v", stopped)
	}
	select {
	case <-peerDone:
		if peerErr != nil || !peerResult.ProtocolReady || !peerResult.GracefulExit || peerResult.UnhandledCommands[nativeproto.CommandUserMessage] != 1 {
			t.Fatalf("对端退出或1004次数错误：%+v %v", peerResult, peerErr)
		}
	case <-ctx.Done():
		t.Fatal("管理器下线后对端未正常退出")
	}
	restored, err = New(config)
	if err != nil {
		t.Fatal(err)
	}
	if saved := restored.State(); saved.Phase != "offline" || saved.OwnUserID != state.OwnUserID || len(saved.Contacts) != 1 || saved.Contacts[0].Contact != contact.Contact || saved.Contacts[0].Sendable || saved.Contacts[0].Online {
		t.Fatalf("停止后无法恢复相同身份及目录：%+v", saved)
	}
}
