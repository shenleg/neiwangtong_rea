// 本文件使用独立loopback原生接收端核对Host正文路由、群成员失效、失败隔离及外部记录失败；不连接真实客户端。
package nativeproto

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"strings"
	"testing"
	"time"
)

type hostSyntheticRun struct {
	host        *Host
	cancel      context.CancelFunc
	done        <-chan residentRunResult
	peers       []<-chan error
	events      <-chan ClientEvent
	discoveries []*net.UDPConn
	nodes       []NodePacket
}

type hostObservedText struct {
	peer  int
	text  string
	group string
}

// hostSyntheticFixture 启动n个独立合成接收端和一对共享Host socket；configure修改本次显式配置，initial/handler提供正常协议消息。
// 返回取消函数和双方完成通道；初始化失败终止测试，所有端点均为127/8临时端口，不读取持久身份或私有目录。
func hostSyntheticFixture(t *testing.T, n int, configure func(*ClientConfig), initial func(int) []Frame, handler func(int, Frame) (*Frame, error)) hostSyntheticRun {
	t.Helper()
	discovery, conn := hostTestSocket(t, "127.0.0.1"), hostTestSocket(t, "127.0.0.1")
	// 合成端固定作为被动方，Host会话顺序必须大于所有合成端。
	node := testClientNode(t, discovery, conn, "host-original-id", 250)
	cfg := hostTestConfig(t, node)
	if configure != nil {
		configure(&cfg)
	}
	peerCtx, stopPeers := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(stopPeers)
	var seeds []netip.AddrPort
	var peers []<-chan error
	var discoveries []*net.UDPConn
	var nodes []NodePacket
	for i := 0; i < n; i++ {
		address := fmt.Sprintf("127.0.0.%d", i+2)
		d, c := hostTestSocket(t, address), hostTestSocket(t, address)
		peerNode := testClientNode(t, d, c, fmt.Sprintf("peer-original-%d", i), byte(i+1))
		seeds = append(seeds, d.LocalAddr().(*net.UDPAddr).AddrPort())
		discoveries = append(discoveries, d)
		nodes = append(nodes, peerNode)
		var frames []Frame
		if initial != nil {
			frames = initial(i)
		}
		done := make(chan error, 1)
		peers = append(peers, done)
		go func(index int) {
			done <- serveSyntheticNativeWithHandler(peerCtx, d, c, peerNode, true, func(frame Frame) (*Frame, error) { return handler(index, frame) }, frames...)
		}(i)
	}
	host, err := NewHost(discovery, conn, HostConfig{Client: cfg, Prefix: netip.MustParsePrefix("127.0.0.0/8"), Seeds: seeds, MaxPeers: n})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(peerCtx)
	t.Cleanup(cancel)
	events := make(chan ClientEvent, 1024)
	done := make(chan residentRunResult, 1)
	go func() {
		result, err := host.Run(ctx, func(event ClientEvent) error { events <- event; return nil })
		done <- residentRunResult{result, err}
	}()
	return hostSyntheticRun{host: host, cancel: cancel, done: done, peers: peers, events: events, discoveries: discoveries, nodes: nodes}
}

// finishHostSynthetic 取消Host并验证所有合成端收到正常退出；完成失败或超过预算终止测试，返回Host统计结果。
func finishHostSynthetic(t *testing.T, run hostSyntheticRun) ClientResult {
	t.Helper()
	run.cancel()
	select {
	case result := <-run.done:
		if result.err != nil || !result.result.GracefulExit {
			t.Fatalf("Host退出: %+v %v", result.result, result.err)
		}
		for _, done := range run.peers {
			select {
			case err := <-done:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("合成端退出超时")
			}
		}
		return result.result
	case <-time.After(3 * time.Second):
		t.Fatal("Host退出超时")
		return ClientResult{}
	}
}

// decodeHostText 解析一次1004/3011的合成接收内容并返回原始正文及群ID；坏帧/JSON返回错误，不从发送输入推导期望。
func decodeHostText(index int, frame Frame) (hostObservedText, error) {
	message := hostObservedText{peer: index}
	var payload string
	if frame.Command == CommandGroupMessage {
		group, err := DecodeGroupMessage(frame)
		if err != nil {
			return message, err
		}
		message.group, payload = group.GroupID, group.Content
	} else {
		user, err := DecodeUserMessage(frame)
		if err != nil {
			return message, err
		}
		payload = user.Content
	}
	var content textDocument
	if err := json.Unmarshal([]byte(payload), &content); err != nil {
		return message, err
	}
	message.text = content.Data[0].Text.Value
	return message, nil
}

// TestHostDiscoversSeventyThreeClients 验证一次上线能与超过原64连接上限的合成客户端全部建立连接。
func TestHostDiscoversSeventyThreeClients(t *testing.T) {
	const count = 73
	run := hostSyntheticFixture(t, count, func(cfg *ClientConfig) {
		cfg.Transport.RetransmitAfter, cfg.Transport.MaxAttempts = 100*time.Millisecond, 10
		cfg.HandshakeTimeout = 5 * time.Second
	}, nil, func(int, Frame) (*Frame, error) { return nil, nil })
	deadline := time.After(8 * time.Second)
	failures := make(map[string]int)
	for {
		snapshot := run.host.Snapshot()
		if len(snapshot.OnlineUserIDs) == count && len(snapshot.Contacts) == count {
			break
		}
		select {
		case event := <-run.events:
			if event.Error != "" {
				failures[event.Stage+": "+event.Error]++
			}
		case <-deadline:
			t.Fatalf("在线=%d 联系人=%d 错误=%v", len(snapshot.OnlineUserIDs), len(snapshot.Contacts), failures)
		}
	}
	finishHostSynthetic(t, run)
}

// TestHostRoutesThreeDistinctBodies 验证三个独立原生接收端分别收到目标原始ID对应的完整正文，不只检查传输ACK。
func TestHostRoutesThreeDistinctBodies(t *testing.T) {
	messages := make(chan hostObservedText, 4)
	run := hostSyntheticFixture(t, 3, nil, nil, func(index int, frame Frame) (*Frame, error) {
		message, err := decodeHostText(index, frame)
		if err == nil {
			messages <- message
		}
		return nil, err
	})
	awaitHostSnapshot(t, run.host, func(snapshot ClientSnapshot) bool { return len(snapshot.OnlineUserIDs) == 3 })
	texts := []string{"第一位\n完整正文甲", "Second peer\n独立正文乙", "第三位\r\n原始正文丙"}
	for i, text := range texts {
		result, err := run.host.SendUser(context.Background(), fmt.Sprintf("peer-original-%d", i), text)
		if err != nil || !result.TransportAcknowledged {
			t.Fatalf("发送%d: %+v %v", i, result, err)
		}
		select {
		case message := <-messages:
			if message.peer != i || message.text != text || message.group != "" {
				t.Fatalf("目标%d: %+v", i, message)
			}
		case <-time.After(time.Second):
			t.Fatal("未独立接收正文")
		}
	}
	if result := finishHostSynthetic(t, run); result.ChatSends != 3 {
		t.Fatalf("发送次数=%d", result.ChatSends)
	}
}

// TestHostTwoMemberGroupAndInvalidation 验证双人/多人群按完整成员路由，以及一peer失效后不能从另一peer旧快照复活群。
func TestHostTwoMemberGroupAndInvalidation(t *testing.T) {
	messages := make(chan hostObservedText, 4)
	configure := func(cfg *ClientConfig) {
		cfg.GroupChat = true
		for i := range cfg.Profile.Features {
			if cfg.Profile.Features[i].Name == "QGROUP" || cfg.Profile.Features[i].Name == "QGROUP_MSG" {
				cfg.Profile.Features[i].Value = "2:1"
			}
		}
	}
	initial := func(index int) []Frame {
		// 两端报告同一二成员群，确定性选择版本；只有peer0是真实成员与可发送目标。
		pair := syntheticGroupPush(CommandGroupUserPush, "host-original-id", "peer-original-0")
		many := syntheticGroupPush(CommandGroupUserPush, "host-original-id", "peer-original-0", "peer-original-1")
		many.Payload = strings.ReplaceAll(many.Payload, "synthetic-group", "multi-member-group")
		return []Frame{pair, many}
	}
	run := hostSyntheticFixture(t, 2, configure, initial, func(index int, frame Frame) (*Frame, error) {
		if frame.Command == CommandGroupUserRequest {
			response := syntheticGroupUserResponse(10)
			return &response, nil
		}
		message, err := decodeHostText(index, frame)
		if err != nil {
			return nil, err
		}
		messages <- message
		if message.text == "stale" {
			push := syntheticGroupPush(CommandGroupUserPush, "host-original-id", "peer-original-0")
			return &push, nil
		}
		if message.text == "invalidate" {
			push := syntheticGroupPush(CommandGroupInfoPush)
			push.Payload = strings.ReplaceAll(push.Payload, "<QGROUP_USER_VER>10", "<QGROUP_USER_VER>11")
			return &push, nil
		}
		return nil, nil
	})
	awaitHostSnapshot(t, run.host, func(snapshot ClientSnapshot) bool {
		return len(snapshot.OnlineUserIDs) == 2 && len(snapshot.Groups) == 2
	})
	for i := 0; i < 4; i++ {
		awaitResidentEvent(t, run.events, "group_synchronized", "")
	}
	if result, err := run.host.SendGroup(context.Background(), "multi-member-group", "多人群完整正文"); err != nil || !result.TransportAcknowledged || len(result.Recipients) != 2 {
		t.Fatalf("多人群投递失败：%+v %v", result, err)
	}
	seen := make(map[int]bool)
	for i := 0; i < 2; i++ {
		message := <-messages
		if seen[message.peer] || message.group != "multi-member-group" || message.text != "多人群完整正文" {
			t.Fatalf("多人群目标或正文错误：%+v", message)
		}
		seen[message.peer] = true
	}
	text := "二成员群完整正文\n仅peer0接收"
	if result, err := run.host.SendGroup(context.Background(), "synthetic-group", text); err != nil || !result.TransportAcknowledged {
		t.Fatalf("二成员群: %+v %v", result, err)
	}
	if message := <-messages; message.peer != 0 || message.group != "synthetic-group" || message.text != text {
		t.Fatalf("群路由错误: %+v", message)
	}
	snapshot := run.host.Snapshot()
	snapshot.Groups[0].Members[0] = "outside-change"
	if run.host.Snapshot().Groups[0].Members[0] == "outside-change" {
		t.Fatal("群成员未深拷贝")
	}
	if _, err := run.host.SendUser(context.Background(), "peer-original-0", "invalidate"); err != nil {
		t.Fatal(err)
	}
	<-messages
	awaitResidentEvent(t, run.events, "group_membership_absent", "")
	awaitHostSnapshot(t, run.host, func(snapshot ClientSnapshot) bool { return len(snapshot.Groups) == 1 })
	if _, err := run.host.SendUser(context.Background(), "peer-original-1", "stale"); err != nil {
		t.Fatal(err)
	}
	<-messages
	awaitResidentEvent(t, run.events, "group_synchronized", "")
	if len(run.host.Snapshot().Groups) != 1 {
		t.Fatal("其他peer再次推送旧成员版本复活了失效群")
	}
	if result, err := run.host.SendGroup(context.Background(), "synthetic-group", "旧成员不可发送"); err == nil || result.Submitted {
		t.Fatal("旧peer快照恢复了失效群")
	}
	finishHostSynthetic(t, run)
}

// TestHostDirectoryFailureStopsAll 验证目录持久化错误停止整个Host且不发布失败目录，不把外部记录失败当成普通peer故障忽略。
func TestHostDirectoryFailureStopsAll(t *testing.T) {
	recordErr := errors.New("synthetic directory write failure")
	run := hostSyntheticFixture(t, 3, func(cfg *ClientConfig) { cfg.ObserveDirectory = func(*Directory) error { return recordErr } }, nil, func(int, Frame) (*Frame, error) { return nil, nil })
	select {
	case result := <-run.done:
		if !errors.Is(result.err, recordErr) {
			t.Fatalf("未返回持久化错误: %v", result.err)
		}
		if run.host.Snapshot().Online || len(run.host.Snapshot().Contacts) != 0 {
			t.Fatal("失败目录已发布或Host未停止")
		}
	case <-time.After(4 * time.Second):
		t.Fatal("目录失败未停止Host")
	}
}

// TestHostPeerFailureIsolated 验证单peer业务协议错误只停止该连接，记录明确原因，不重连该身份，其余peer仍可发送。
func TestHostPeerFailureIsolated(t *testing.T) {
	messages := make(chan hostObservedText, 8)
	run := hostSyntheticFixture(t, 3, nil, nil, func(index int, frame Frame) (*Frame, error) {
		message, err := decodeHostText(index, frame)
		if err != nil {
			return nil, err
		}
		messages <- message
		if message.text == "break-peer" {
			bad := Frame{Command: CommandReady, Payload: "<broken/>"}
			return &bad, nil
		}
		return nil, nil
	})
	awaitHostSnapshot(t, run.host, func(snapshot ClientSnapshot) bool { return len(snapshot.OnlineUserIDs) == 3 })
	if _, err := run.host.SendUser(context.Background(), "peer-original-0", "break-peer"); err != nil {
		t.Fatal(err)
	}
	<-messages
	failed := awaitResidentEvent(t, run.events, "peer_failed", "")
	if failed.Error == "" || failed.PeerIndex == 0 {
		t.Fatalf("peer失败缺少原因和序号: %+v", failed)
	}
	awaitHostSnapshot(t, run.host, func(snapshot ClientSnapshot) bool { return snapshot.Online && len(snapshot.OnlineUserIDs) == 2 })
	for i := 1; i < 3; i++ {
		if _, err := run.host.SendUser(context.Background(), fmt.Sprintf("peer-original-%d", i), "survives"); err != nil {
			t.Fatal(err)
		}
		if message := <-messages; message.peer != i || message.text != "survives" {
			t.Fatalf("健康peer路由错误: %+v", message)
		}
	}
	// 重新公告完全相同的失败节点，Host仍保留失败记录，不换nonce偷偷重连。
	wire, _ := run.nodes[0].Encode()
	target := run.host.discovery.LocalAddr().(*net.UDPAddr).AddrPort()
	if _, err := run.discoveries[0].WriteToUDPAddrPort(wire, target); err != nil {
		t.Fatal(err)
	}
	if result, err := run.host.SendUser(context.Background(), "peer-original-0", "不得重连"); err == nil || result.Submitted {
		t.Fatal("失败身份可继续提交")
	}
	run.cancel()
	select {
	case result := <-run.done:
		if result.err != nil || result.result.GracefulExit {
			t.Fatalf("故障历史被掩盖或拖垮Host: %+v %v", result.result, result.err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("故障隔离Host未退出")
	}
	for i := 1; i < 3; i++ {
		if err := <-run.peers[i]; err != nil {
			t.Fatal(err)
		}
	}
}

// TestHostDurationStopsAllPeers 验证活动预算属于Host而不是每次新连接，各peer在预算到期后仍交换QUIT和传输断开。
func TestHostDurationStopsAllPeers(t *testing.T) {
	run := hostSyntheticFixture(t, 3, func(cfg *ClientConfig) { cfg.Duration = 400 * time.Millisecond }, nil, func(int, Frame) (*Frame, error) { return nil, nil })
	awaitHostSnapshot(t, run.host, func(snapshot ClientSnapshot) bool { return len(snapshot.OnlineUserIDs) == 3 })
	select {
	case result := <-run.done:
		if result.err != nil || !result.result.GracefulExit {
			t.Fatalf("预算退出: %+v %v", result.result, result.err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Host未按预算正常退出")
	}
	for _, done := range run.peers {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
	if run.host.Snapshot().Online || len(run.host.Snapshot().OnlineUserIDs) != 0 {
		t.Fatal("预算退出后仍发布在线")
	}
}
