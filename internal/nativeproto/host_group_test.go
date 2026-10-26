// 本文件使用独立loopback接收端验证多成员群同一消息、整群预检查、并发顺序与部分失败事实。
package nativeproto

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

// enableHostGroup 为合成Host声明群资料与纯文本群消息能力，不操作网络或返回错误。
func enableHostGroup(cfg *ClientConfig) {
	cfg.GroupChat = true
	for i := range cfg.Profile.Features {
		if cfg.Profile.Features[i].Name == "QGROUP" || cfg.Profile.Features[i].Name == "QGROUP_MSG" {
			cfg.Profile.Features[i].Value = "2:1"
		}
	}
}

// hostGroupFixture 仅让一个正常对端提供完整成员快照，返回已就绪的合成Host；其余成员无需重复推送群资料。
// members是完整原始ID集合，handler处理各接收端入站消息；初始化或就绪超时终止测试，全部端点限127/8。
func hostGroupFixture(t *testing.T, n int, members []string, handler func(int, Frame) (*Frame, error)) hostSyntheticRun {
	t.Helper()
	run := hostSyntheticFixture(t, n, enableHostGroup, func(index int) []Frame {
		if index != 0 {
			return nil
		}
		return []Frame{syntheticGroupPush(CommandGroupUserPush, members...)}
	}, handler)
	awaitHostSnapshot(t, run.host, func(s ClientSnapshot) bool {
		return len(s.OnlineUserIDs) == n && len(s.Groups) == 1
	})
	return run
}

// TestHostGroupSameMessageAndOrder 验证多人群各成员恰收一次相同3011、MSG_ID及正文GUID，连续和并发请求在各成员上顺序一致。
func TestHostGroupSameMessageAndOrder(t *testing.T) {
	var mu sync.Mutex
	received := make([][]GroupMessage, 3)
	run := hostGroupFixture(t, 3, []string{"peer-original-2", "host-original-id", "peer-original-0", "peer-original-1"}, func(index int, frame Frame) (*Frame, error) {
		message, err := DecodeGroupMessage(frame)
		if err != nil {
			return nil, err
		}
		mu.Lock()
		received[index] = append(received[index], message)
		mu.Unlock()
		return nil, nil
	})
	texts := []string{"第一条完整正文\n第二行<&>🙂", "第二条\r\n不同正文"}
	results := make([]ChatResult, 2)
	for i, text := range texts {
		result, err := run.host.SendGroup(context.Background(), "synthetic-group", text)
		if err != nil || !result.Submitted || !result.TransportAcknowledged || len(result.Recipients) != 3 || result.BusinessAcknowledged || result.ReceiptReceived || result.Delivery != "unknown" {
			t.Fatalf("群结果：%+v %v", result, err)
		}
		results[i] = result
		for index, recipient := range result.Recipients {
			if recipient.UserID != fmt.Sprintf("peer-original-%d", index) || recipient.PeerIndex == 0 || !recipient.Submitted || !recipient.TransportAcknowledged || recipient.Error != "" {
				t.Fatalf("成员结果不完整：%+v", recipient)
			}
		}
	}
	if results[0].MessageID == results[1].MessageID {
		t.Fatal("独立群请求复用了消息编号")
	}
	var calls sync.WaitGroup
	for i := 0; i < 4; i++ {
		calls.Add(1)
		go func(i int) {
			defer calls.Done()
			if result, err := run.host.SendGroup(context.Background(), "synthetic-group", fmt.Sprintf("并发请求-%d", i)); err != nil || !result.TransportAcknowledged {
				t.Errorf("并发群发送：%+v %v", result, err)
			}
		}(i)
	}
	calls.Wait()
	if summary := finishHostSynthetic(t, run); summary.ChatSends != 18 {
		t.Fatalf("逐成员提交次数=%d", summary.ChatSends)
	}
	mu.Lock()
	defer mu.Unlock()
	for index, messages := range received {
		if len(messages) != 6 {
			t.Fatalf("成员%d收到%d条，应为6条", index, len(messages))
		}
		for i, message := range messages {
			if message != received[0][i] || message.GroupID != "synthetic-group" || message.HideRecord {
				t.Fatalf("成员%d第%d条消息内容/编号/顺序不同：%+v", index, i, message)
			}
			if i < 2 {
				observed, err := decodeHostText(index, mustGroupFrame(t, message))
				if err != nil || observed.text != texts[i] || fmt.Sprint(message.MessageID) != results[i].MessageID {
					t.Fatalf("完整正文不符：%+v %v", observed, err)
				}
			}
		}
	}
	var previousGUID string
	for _, message := range received[0] {
		var content textDocument
		if err := json.Unmarshal([]byte(message.Content), &content); err != nil {
			t.Fatal(err)
		}
		if content.ID == "" || content.ID == previousGUID {
			t.Fatal("独立群请求复用了正文GUID")
		}
		previousGUID = content.ID
	}
}

// mustGroupFrame 将已接收群消息重新编码供正文解析断言；编码失败终止测试，无网络操作。
func mustGroupFrame(t *testing.T, message GroupMessage) Frame {
	t.Helper()
	frame, err := EncodeGroupMessage(message)
	if err != nil {
		t.Fatal(err)
	}
	return frame
}

// TestHostGroupOfflineMemberDoesNotBlock 验证离线成员不阻断在线路由，各在线成员恰收一次，离线成员明确标为未投递。
func TestHostGroupOfflineMemberDoesNotBlock(t *testing.T) {
	messages := make(chan hostObservedText, 4)
	run := hostGroupFixture(t, 2, []string{"host-original-id", "peer-original-0", "peer-original-1", "offline-member"}, func(index int, frame Frame) (*Frame, error) {
		message, err := decodeHostText(index, frame)
		if err == nil {
			messages <- message
		}
		return nil, err
	})
	result, err := run.host.SendGroup(context.Background(), "synthetic-group", "向在线成员投递")
	if err != nil || !result.Submitted || result.TransportAcknowledged || result.MessageID == "" || len(result.Recipients) != 3 {
		t.Fatalf("离线成员阻断了在线路由或伪造全群确认：%+v %v", result, err)
	}
	for _, recipient := range result.Recipients {
		if recipient.UserID == "offline-member" {
			if !recipient.Skipped || recipient.Submitted || recipient.TransportAcknowledged || recipient.PeerIndex != 0 || recipient.Error == "" {
				t.Fatalf("离线成员事实错误：%+v", recipient)
			}
		} else if recipient.Skipped || !recipient.Submitted || !recipient.TransportAcknowledged || recipient.Error != "" {
			t.Fatalf("在线路由未正常投递：%+v", recipient)
		}
	}
	if summary := finishHostSynthetic(t, run); summary.ChatSends != 2 || len(messages) != 2 {
		t.Fatalf("在线提交次数错误：%+v messages=%d", summary, len(messages))
	}
	seen := make(map[int]bool)
	for i := 0; i < 2; i++ {
		message := <-messages
		if seen[message.peer] || message.text != "向在线成员投递" || message.group != "synthetic-group" {
			t.Fatalf("在线成员收到了重复或错误正文：%+v", message)
		}
		seen[message.peer] = true
	}
}

// TestHostGroupCancellationAndVersionChange 验证取消和发送意图记录后的成员版本变化均阻止旧计划，不生成应用重试。
func TestHostGroupCancellationAndVersionChange(t *testing.T) {
	messages := make(chan Frame, 4)
	run := hostGroupFixture(t, 2, []string{"host-original-id", "peer-original-0", "peer-original-1"}, func(_ int, frame Frame) (*Frame, error) {
		messages <- frame
		return nil, nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if result, err := run.host.SendGroup(ctx, "synthetic-group", "已取消"); !errors.Is(err, context.Canceled) || result.Submitted || result.MessageID != "" {
		t.Fatalf("已取消请求提交：%+v %v", result, err)
	}
	run.host.groupSends <- struct{}{}
	waiting, stop := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer stop()
	if result, err := run.host.SendGroup(waiting, "synthetic-group", "等待中取消"); !errors.Is(err, context.DeadlineExceeded) || result.Submitted {
		t.Fatalf("等待发送权不响应取消：%+v %v", result, err)
	}
	<-run.host.groupSends
	previous := run.host.observe
	run.host.callbackMu.Lock()
	run.host.observe = func(event ClientEvent) error {
		if event.Stage == "group_send_planned" {
			run.host.mu.Lock()
			group := run.host.groups["synthetic-group"]
			group.info.UserVersion++
			run.host.groups["synthetic-group"] = group
			run.host.mu.Unlock()
		}
		return previous(event)
	}
	run.host.callbackMu.Unlock()
	result, err := run.host.SendGroup(context.Background(), "synthetic-group", "旧版本不可投递")
	if err == nil || result.Submitted || result.TransportAcknowledged || len(result.Recipients) != 2 {
		t.Fatalf("旧版本计划仍提交：%+v %v", result, err)
	}
	for _, recipient := range result.Recipients {
		if !strings.Contains(recipient.Error, "已变化") {
			t.Fatalf("缺失版本变化原因：%+v", recipient)
		}
	}
	if summary := finishHostSynthetic(t, run); summary.ChatSends != 0 || len(messages) != 0 {
		t.Fatal("取消或成员更新仍有消息提交")
	}
}

// TestHostGroupJournalFailureRejectsAll 验证整群发送意图落盘失败时没有任何成员进入Queue，记录错误停止Host。
func TestHostGroupJournalFailureRejectsAll(t *testing.T) {
	messages := make(chan Frame, 4)
	run := hostGroupFixture(t, 2, []string{"host-original-id", "peer-original-0", "peer-original-1"}, func(_ int, frame Frame) (*Frame, error) {
		messages <- frame
		return nil, nil
	})
	recordErr := errors.New("synthetic group journal failure")
	run.host.callbackMu.Lock()
	previous := run.host.observe
	run.host.observe = func(event ClientEvent) error {
		if event.Stage == "group_send_planned" {
			return recordErr
		}
		return previous(event)
	}
	run.host.callbackMu.Unlock()
	result, err := run.host.SendGroup(context.Background(), "synthetic-group", "记录失败不可投递")
	if !errors.Is(err, recordErr) || result.Submitted || result.TransportAcknowledged || len(result.Recipients) != 2 {
		t.Fatalf("记录失败未保留未提交事实：%+v %v", result, err)
	}
	for _, recipient := range result.Recipients {
		if recipient.Submitted || recipient.TransportAcknowledged || recipient.Error == "" {
			t.Fatalf("记录失败成员结果错误：%+v", recipient)
		}
	}
	select {
	case summary := <-run.done:
		if !errors.Is(summary.err, recordErr) || summary.result.ChatSends != 0 || len(messages) != 0 {
			t.Fatalf("记录失败仍有提交：%+v %v", summary.result, summary.err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("记录失败未停止Host")
	}
}

// TestHostGroupAllMembersOffline 验证没有可投递成员时不生成消息编号、不Queue，并保留全部离线成员的未投递原因。
func TestHostGroupAllMembersOffline(t *testing.T) {
	run := hostGroupFixture(t, 1, []string{"host-original-id", "offline-member"}, func(_ int, _ Frame) (*Frame, error) {
		t.Error("无可用成员不应收到聊天")
		return nil, nil
	})
	result, err := run.host.SendGroup(context.Background(), "synthetic-group", "无在线成员")
	if err == nil || result.Submitted || result.MessageID != "" || len(result.Recipients) != 1 || !result.Recipients[0].Skipped {
		t.Fatalf("无在线成员时结果错误：%+v %v", result, err)
	}
	if summary := finishHostSynthetic(t, run); summary.ChatSends != 0 {
		t.Fatalf("无在线成员仍发生提交：%+v", summary)
	}
}

// TestHostGroupPreflightCapabilities 验证不兼容成员记录为Skipped，无可用成员、本机不在群或无其他成员时返回错误。
func TestHostGroupPreflightCapabilities(t *testing.T) {
	for _, tc := range []struct {
		name    string
		members []string
		capable bool
	}{
		{"unsupported", []string{"own", "peer"}, false},
		{"own-absent", []string{"peer"}, true},
		{"own-only", []string{"own"}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := &Client{}
			client.snapshot.Store(&ClientSnapshot{Online: true, PeerUserID: "peer", GroupTextSupported: tc.capable})
			host := &Host{config: HostConfig{Client: ClientConfig{Node: NodePacket{UserID: "own"}, GroupChat: true}}, accepting: true,
				identities: map[string]*hostPeer{"peer": {node: NodePacket{UserID: "peer"}, client: client}},
				groups:     map[string]hostGroup{"g": {info: GroupInfo{GroupID: "g", Members: tc.members}}}}
			host.mu.Lock()
			_, peers, err := host.groupRecipients("g")
			host.mu.Unlock()
			if err == nil {
				t.Fatalf("无可用成员的群计划被接受：%v", peers)
			}
			if tc.name == "unsupported" {
				if len(peers) != 1 || !peers[0].result.Skipped || peers[0].result.Error == "" || peers[0].peer != nil {
					t.Fatalf("不兼容成员未保留原因：%+v", peers)
				}
			} else if peers != nil {
				t.Fatalf("无效成员集合产生了路由：%+v", peers)
			}
		})
	}
}

// TestHostGroupUnsupportedMemberDoesNotBlock 验证群文本能力不兼容的成员不会阻断其他在线成员，且不向其安排提交。
func TestHostGroupUnsupportedMemberDoesNotBlock(t *testing.T) {
	host := &Host{config: HostConfig{Client: ClientConfig{Node: NodePacket{UserID: "own"}, GroupChat: true}}, accepting: true,
		identities: make(map[string]*hostPeer), groups: map[string]hostGroup{"g": {info: GroupInfo{GroupID: "g", Members: []string{"own", "capable", "unsupported"}}}}}
	for _, id := range []string{"capable", "unsupported"} {
		client := &Client{}
		client.snapshot.Store(&ClientSnapshot{Online: true, PeerUserID: id, GroupTextSupported: id == "capable"})
		host.identities[id] = &hostPeer{node: NodePacket{UserID: id}, client: client, index: 1}
	}
	host.mu.Lock()
	_, routes, err := host.groupRecipients("g")
	host.mu.Unlock()
	if err != nil || len(routes) != 2 || routes[0].peer == nil || routes[0].result.Skipped || routes[1].peer != nil || !routes[1].result.Skipped || routes[1].result.Error == "" {
		t.Fatalf("能力不兼容成员阻断了正常路由：%+v %v", routes, err)
	}
}

// TestHostGroupPartialFailurePreservesResults 验证一个路由Queue后断网时保留该成员未知及健康成员确认，不把局部ACK当成整群成功。
func TestHostGroupPartialFailurePreservesResults(t *testing.T) {
	messages := make(chan hostObservedText, 4)
	run := hostGroupFixture(t, 2, []string{"host-original-id", "peer-original-0", "peer-original-1"}, func(index int, frame Frame) (*Frame, error) {
		message, err := decodeHostText(index, frame)
		if err == nil {
			messages <- message
		}
		return nil, err
	})
	// 该路由保存发送意图后关闭自己的虚拟端点；Queue仍接受一次，实际写出失败，其他路由继续正常确认。
	previous := run.host.observe
	run.host.callbackMu.Lock()
	run.host.observe = func(event ClientEvent) error {
		if event.Stage == "chat_send_planned" && event.Command == CommandGroupMessage {
			run.host.mu.Lock()
			broken := run.host.identities["peer-original-1"]
			run.host.mu.Unlock()
			if event.PeerIndex == broken.index {
				if err := broken.conn.Close(); err != nil {
					return err
				}
			}
		}
		return previous(event)
	}
	run.host.callbackMu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	result, err := run.host.SendGroup(ctx, "synthetic-group", "一次群请求部分失败")
	if err == nil || !result.Submitted || result.TransportAcknowledged || result.MessageID == "" || len(result.Recipients) != 2 {
		t.Fatalf("部分失败被改写：%+v %v", result, err)
	}
	if first, second := result.Recipients[0], result.Recipients[1]; !first.TransportAcknowledged || !first.Submitted || first.Error != "" || !second.Submitted || second.TransportAcknowledged || second.Error == "" {
		t.Fatalf("逐成员事实错误：%+v", result.Recipients)
	}
	run.cancel()
	select {
	case summary := <-run.done:
		if summary.result.ChatSends != 2 {
			t.Fatalf("部分失败触发应用重试或丢失提交：%+v %v", summary.result, summary.err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("部分失败后Host未退出")
	}
}
