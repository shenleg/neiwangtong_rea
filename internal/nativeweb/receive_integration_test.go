// 本文件用独立loopback协议端验证默认Web会话接收群资料和3011，经认证HTTP读取全文且正文不写入任何数据文件。
package nativeweb

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"neiwangtong-gateway/internal/nativeproto"
)

// serveIncomingGroupPeer向target公告合成身份，完成正常握手后推送完整成员及一条群文本；返回解析或网络错误。
// socket仅由测试提供的127/8端点组成，ctx限制整个服务，不访问真实身份或聊天记录。
func serveIncomingGroupPeer(ctx context.Context, discovery, conn *net.UDPConn, target netip.AddrPort, text string) error {
	d := discovery.LocalAddr().(*net.UDPAddr).AddrPort()
	node, err := nativeproto.NewNodePacket(nativeproto.NodeRegistration{
		UserID: "synthetic-incoming-group-peer", SessionGUID: [16]byte{1}, Address: d.Addr(),
		Broadcast: netip.MustParseAddr("127.255.255.255"), DiscoveryPort: d.Port(), ENetPort: conn.LocalAddr().(*net.UDPAddr).AddrPort().Port(),
	}, 1)
	if err != nil {
		return err
	}
	wire, err := node.Encode()
	if err != nil {
		return err
	}
	if _, err := discovery.WriteToUDPAddrPort(wire, target); err != nil {
		return err
	}
	if err := discovery.SetReadDeadline(time.Now().Add(4 * time.Second)); err != nil {
		return err
	}
	buffer := make([]byte, 4096)
	n, source, err := discovery.ReadFromUDPAddrPort(buffer)
	if err != nil {
		return err
	}
	other, err := nativeproto.DecodeNodePacket(buffer[:n])
	if err != nil {
		return err
	}
	node.Type = 2
	wire, err = node.Encode()
	if err != nil {
		return err
	}
	if _, err := discovery.WriteToUDPAddrPort(wire, source); err != nil {
		return err
	}
	transport, err := nativeproto.NewUDPTransport(conn, netip.AddrPortFrom(source.Addr(), other.ENetPort), nativeproto.ENetConfig{
		LocalPeerID: 1, ConnectID: [4]byte{1, 2, 3, 4}, MTU: 1400, WindowBytes: 65536,
		MaxMessageBytes: 1 << 20, MaxQueuedBytes: 2 << 20, MaxPendingCommands: 1024,
		RetransmitAfter: 100 * time.Millisecond, MaxAttempts: 6, ReceiveTimeout: 6 * time.Second, PingInterval: time.Second,
	})
	if err != nil {
		return err
	}
	defer func() { _ = transport.Close(ctx) }()
	order, err := node.SessionOrder(other)
	if err != nil {
		return err
	}
	if order > 0 {
		err = transport.Connect(ctx)
	} else {
		err = transport.Accept(ctx)
	}
	if err != nil {
		return err
	}
	profile, err := nativeproto.ProbeHandshake("合成群消息发送者")
	if err != nil {
		return err
	}
	for i := range profile.Features {
		if profile.Features[i].Name == "QGROUP" || profile.Features[i].Name == "QGROUP_MSG" {
			profile.Features[i].Value = "2:1"
		}
	}
	handshake, err := nativeproto.EncodeHandshake(profile)
	if err != nil {
		return err
	}
	sendFrame := func(frame nativeproto.Frame) error {
		wire, err := nativeproto.Encode(frame.Command, frame.Payload)
		if err != nil {
			return err
		}
		return transport.Send(ctx, wire)
	}
	sentHandshake := false
	if order > 0 {
		node.Type = 4
		wire, err := node.Encode()
		if err != nil {
			return err
		}
		if err := transport.Send(ctx, wire); err != nil {
			return err
		}
		if err := sendFrame(handshake); err != nil {
			return err
		}
		sentHandshake = true
	}
	for {
		wire, err := transport.Receive(ctx)
		if err != nil {
			return err
		}
		if len(wire) == 304 {
			if _, err := nativeproto.DecodeNodePacket(wire); err != nil {
				return err
			}
			continue
		}
		frame, err := nativeproto.Decode(bytes.NewReader(wire), 1<<20)
		if err != nil {
			return err
		}
		if frame.Command == nativeproto.CommandHandshake {
			if _, err := nativeproto.DecodeHandshake(frame); err != nil {
				return err
			}
			if !sentHandshake {
				if err := sendFrame(handshake); err != nil {
					return err
				}
				sentHandshake = true
			}
			ready, err := nativeproto.EncodeReady(nativeproto.Ready{UserID: node.UserID})
			if err != nil {
				return err
			}
			if err := sendFrame(ready); err != nil {
				return err
			}
		} else if frame.Command == nativeproto.CommandReady {
			ready, err := nativeproto.DecodeReady(frame)
			if err != nil || ready.UserID != other.UserID {
				return fmt.Errorf("合成群对端READY身份不符")
			}
			break
		} else {
			return fmt.Errorf("合成群对端收到非握手命令%d", frame.Command)
		}
	}
	// 输入通知只含TYPE，身份必须来自此已READY的独立连接，不能靠XML自报联系人。
	if err := sendFrame(nativeproto.Frame{Command: nativeproto.CommandTyping, Payload: `<X_SEND_WRITTING docver="1"><TYPE>0</TYPE></X_SEND_WRITTING>`}); err != nil {
		return err
	}
	// 资料与成员使用既有3003模板，成员身份来自这次实际握手，不从Manager快照反向构造接收结果。
	group := fmt.Sprintf(`<X_QGROUP_PUSH_USER docver="1"><INFO><QGROUP_ID>synthetic-incoming-group</QGROUP_ID><QGROUP_NAME>合成接收群</QGROUP_NAME><QGROUP_MASTER>%s</QGROUP_MASTER><QGROUP_INTR></QGROUP_INTR><QGROUP_ANN></QGROUP_ANN><QGROUP_INFO_VER>1</QGROUP_INFO_VER><QGROUP_USER_VER>1</QGROUP_USER_VER><QGROUP_ALLOW_OTHER_ADD>0</QGROUP_ALLOW_OTHER_ADD><QGROUP_PHOTO_MD5></QGROUP_PHOTO_MD5></INFO><USER><ID name="合成发送者">%s</ID><ID name="合成网页">%s</ID></USER></X_QGROUP_PUSH_USER>`, node.UserID, node.UserID, other.UserID)
	if err := sendFrame(nativeproto.Frame{Command: nativeproto.CommandGroupUserPush, Payload: group}); err != nil {
		return err
	}
	content, err := nativeproto.EncodeTextContent(text)
	if err != nil {
		return err
	}
	message, err := nativeproto.EncodeGroupMessage(nativeproto.GroupMessage{GroupID: "synthetic-incoming-group", MessageID: 73, Content: content})
	if err != nil {
		return err
	}
	if err := sendFrame(message); err != nil {
		return err
	}
	for {
		wire, err := transport.Receive(ctx)
		if err != nil {
			return err
		}
		frame, err := nativeproto.Decode(bytes.NewReader(wire), 1<<20)
		if err != nil {
			return err
		}
		if frame.Command == nativeproto.CommandQuit {
			if _, err := transport.Receive(ctx); err != io.EOF {
				return fmt.Errorf("合成对端等待正常断开: %w", err)
			}
			return nil
		}
		if frame.Command != nativeproto.CommandHeartbeat {
			return fmt.Errorf("群消息不应触发未知应用确认%d", frame.Command)
		}
	}
}

// TestNativeIncomingGroupIsEphemeral 验证默认openNative从独立协议端收到群全文，HTTP认证读取后所有资料文件仍不含正文。
func TestNativeIncomingGroupIsEphemeral(t *testing.T) {
	d, e := nativeIntegrationSocket(t, "127.0.0.1"), nativeIntegrationSocket(t, "127.0.0.1")
	config := Config{Local: netip.MustParseAddr("127.0.0.1"), Prefix: netip.MustParsePrefix("127.0.0.0/8"), Name: "合成接收网页", DataDir: t.TempDir(),
		DiscoveryPort: d.LocalAddr().(*net.UDPAddr).AddrPort().Port(), ENetPort: e.LocalAddr().(*net.UDPAddr).AddrPort().Port()}
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
	if err := e.Close(); err != nil {
		t.Fatal(err)
	}
	manager, err := New(config)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	defer func() {
		if err := manager.Close(ctx); err != nil {
			t.Error(err)
		}
	}()
	if err := manager.Start(config.Name); err != nil {
		t.Fatal(err)
	}
	awaitNativeIntegrationStage(t, manager, "protocol_ready")
	const text = "群聊接收回环唯一正文\n<&nbsp;>🙂\r\n群末行"
	peerDiscovery, peerENet := nativeIntegrationSocket(t, "127.0.0.2"), nativeIntegrationSocket(t, "127.0.0.2")
	peerDone := make(chan error, 1)
	go func() {
		peerDone <- serveIncomingGroupPeer(ctx, peerDiscovery, peerENet, netip.AddrPortFrom(config.Local, config.DiscoveryPort), text)
	}()
	awaitNativeIntegrationStage(t, manager, "message_received")
	response := performRequest(testHandler(t, manager), http.MethodGet, "/api/state", "")
	var state State
	if err := json.Unmarshal(response.Body.Bytes(), &state); err != nil {
		t.Fatal(err)
	}
	if response.Code != http.StatusOK || len(state.Messages) != 1 {
		t.Fatalf("Web未收到群消息: %d %+v", response.Code, state)
	}
	if len(state.Typing) != 1 || state.Typing[0].UserID != "synthetic-incoming-group-peer" || state.Typing[0].RemainingMS <= 0 || state.Typing[0].RemainingMS > nativeproto.TypingDuration.Milliseconds() {
		t.Fatalf("1008未经过默认Host和认证HTTP发布正确输入状态: %+v", state.Typing)
	}
	message := state.Messages[0]
	if message.Command != nativeproto.CommandGroupMessage || message.GroupID != "synthetic-incoming-group" || message.SenderID != "synthetic-incoming-group-peer" || message.SenderName != "合成群消息发送者" || message.Text != text {
		t.Fatalf("群消息正文或身份错误: %+v", message)
	}
	manager.Stop()
	awaitPhase(t, manager, "offline")
	select {
	case err := <-peerDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("独立群消息对端未正常退出")
	}
	entries, err := os.ReadDir(config.DataDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		data, err := os.ReadFile(filepath.Join(config.DataDir, entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(data), "群聊接收回环唯一正文") {
			t.Fatalf("群正文写入了文件%s", entry.Name())
		}
	}
}
