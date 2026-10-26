// 本文件以合成目录和loopback会话验证快照复制、联系人学习的发布顺序、并发读取发送及群成员撤销。
package nativeproto

import (
	"context"
	"errors"
	"net"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

// snapshotFixture 构造尚未运行的合成客户端，configure仅在构造前修改配置；start随后启动单个loopback对端。
// 返回客户端和启动函数；初始化错误终止测试，启动函数的结果通道分别报告双方退出，不加载私有资料。
func snapshotFixture(t *testing.T, configure func(*ClientConfig)) (*Client, func(func(ClientEvent) error) (context.CancelFunc, <-chan residentRunResult, <-chan error)) {
	t.Helper()
	discovery, conn := udpTestSocket(t), udpTestSocket(t)
	peerDiscovery, peerConn := udpTestSocket(t), udpTestSocket(t)
	local := testClientNode(t, discovery, conn, "synthetic-local", 2)
	remote := testClientNode(t, peerDiscovery, peerConn, "synthetic-peer", 1)
	profile, err := ProbeHandshake("Synthetic snapshot")
	if err != nil {
		t.Fatal(err)
	}
	config := ClientConfig{Node: local, Profile: profile, RemoteDiscovery: peerDiscovery.LocalAddr().(*net.UDPAddr).AddrPort(),
		Transport: runtimeConfig(0, 1), ShutdownTimeout: time.Second, DiscoveryTimeout: time.Second, HandshakeTimeout: time.Second}
	configure(&config)
	client, err := NewClient(discovery, conn, config)
	if err != nil {
		t.Fatal(err)
	}
	return client, func(observe func(ClientEvent) error) (context.CancelFunc, <-chan residentRunResult, <-chan error) {
		peerCtx, peerCancel := context.WithTimeout(context.Background(), 5*time.Second)
		ctx, cancel := context.WithCancel(peerCtx)
		t.Cleanup(func() { cancel(); peerCancel() })
		done, peerDone := make(chan residentRunResult, 1), make(chan error, 1)
		go func() {
			peerDone <- serveSyntheticNativeWithHandler(peerCtx, peerDiscovery, peerConn, remote, true, func(frame Frame) (*Frame, error) {
				message, err := DecodeUserMessage(frame)
				if err != nil {
					return nil, err
				}
				ack, err := EncodeMessageAck(CommandMessageAck, MessageAck{MessageID: message.MessageID})
				return &ack, err
			})
		}()
		go func() {
			result, err := client.Run(ctx, observe)
			done <- residentRunResult{result, err}
		}()
		return cancel, done, peerDone
	}
}

// TestSnapshotInitialDirectoryCopy 验证启动目录立即可读但不代表在线，输入目录和返回切片都不与内部共享。
func TestSnapshotInitialDirectoryCopy(t *testing.T) {
	var directory *Directory
	client, _ := snapshotFixture(t, func(config *ClientConfig) {
		var err error
		directory, err = NewDirectory(config.Node.UserID, []Contact{{UserID: "synthetic-peer", Name: "Original", Discovery: config.RemoteDiscovery}})
		if err != nil {
			t.Fatal(err)
		}
		config.Contacts = directory
	})
	directory.contacts[0].Name = "Changed input"
	snapshot := client.Snapshot()
	if snapshot.OwnUserID != "synthetic-local" || snapshot.Online || snapshot.PeerUserID != "" || len(snapshot.OnlineUserIDs) != 0 || len(snapshot.Contacts) != 1 || snapshot.Contacts[0].Name != "Original" {
		t.Fatalf("snapshot=%+v", snapshot)
	}
	snapshot.Contacts[0].Name = "Changed output"
	if client.Snapshot().Contacts[0].Name != "Original" {
		t.Fatal("快照修改影响了内部目录")
	}
}

// TestSnapshotLearnContactsConcurrent 验证首次上线无需目录，持久化完成前不发布联系人或在线状态，并发快照/发送无共享可变目录。
func TestSnapshotLearnContactsConcurrent(t *testing.T) {
	for _, activeDirectory := range []bool{false, true} {
		t.Run(map[bool]string{false: "first-login", true: "merge"}[activeDirectory], func(t *testing.T) {
			persisting, release := make(chan struct{}), make(chan struct{})
			var releaseOnce sync.Once
			t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })
			var saved *Directory
			client, start := snapshotFixture(t, func(config *ClientConfig) {
				config.LearnContacts = true
				if activeDirectory {
					var err error
					config.Contacts, err = NewDirectory(config.Node.UserID, []Contact{
						{UserID: "other", Name: "Other", Discovery: config.RemoteDiscovery},
						{UserID: "synthetic-peer", Name: "Old", Discovery: config.RemoteDiscovery},
					})
					if err != nil {
						t.Fatal(err)
					}
				}
				config.ObserveDirectory = func(directory *Directory) error {
					saved = directory
					close(persisting)
					<-release
					return nil
				}
			})
			initial := client.Snapshot()
			events := make(chan ClientEvent, 128)
			cancel, done, peerDone := start(func(event ClientEvent) error {
				snapshot := client.Snapshot()
				if event.Stage == "protocol_ready" && (!snapshot.Online || snapshot.PeerUserID != "synthetic-peer" || !reflect.DeepEqual(snapshot.OnlineUserIDs, []string{"synthetic-peer"})) {
					return errors.New("READY事件早于在线快照")
				}
				if event.Stage == "stopped" && (snapshot.Online || snapshot.PeerUserID != "" || len(snapshot.OnlineUserIDs) != 0) {
					return errors.New("停止事件仍公布在线对端")
				}
				events <- event
				return nil
			})
			select {
			case <-persisting:
			case <-time.After(2 * time.Second):
				t.Fatal("未进入目录持久化")
			}
			if snapshot := client.Snapshot(); !reflect.DeepEqual(initial, snapshot) {
				t.Fatalf("持久化前已发布: %+v", snapshot)
			}
			if result, err := client.SendUser(context.Background(), "synthetic-peer", "before-save"); err == nil || result.Submitted {
				t.Fatal("目录持久化前允许发送")
			}
			readCtx, stopReads := context.WithCancel(context.Background())
			var readers sync.WaitGroup
			for i := 0; i < 4; i++ {
				readers.Add(1)
				go func() {
					defer readers.Done()
					for readCtx.Err() == nil {
						snapshot := client.Snapshot()
						if len(snapshot.Contacts) > 0 {
							snapshot.Contacts[0].Name = "reader-owned"
						}
						runtime.Gosched()
					}
				}()
			}
			t.Cleanup(func() { stopReads(); readers.Wait() })
			learningSend := make(chan chatReply, 1)
			attempted := make(chan struct{})
			go func() {
				result, err := client.SendUser(readCtx, "synthetic-peer", "concurrent learning")
				close(attempted)
				for err != nil && readCtx.Err() == nil {
					runtime.Gosched()
					result, err = client.SendUser(readCtx, "synthetic-peer", "concurrent learning")
				}
				learningSend <- chatReply{result, err}
			}()
			<-attempted
			releaseOnce.Do(func() { close(release) })
			awaitResidentEvent(t, events, "protocol_ready", "")
			// 在线ID和目录一样属于调用方副本，不能让读者修改当前对端身份。
			ownedSnapshot := client.Snapshot()
			ownedSnapshot.OnlineUserIDs[0] = "reader-owned"
			if got := client.Snapshot().OnlineUserIDs; !reflect.DeepEqual(got, []string{"synthetic-peer"}) {
				t.Fatalf("在线ID未深拷贝: %v", got)
			}
			if response := <-learningSend; response.err != nil || !response.result.TransportAcknowledged {
				t.Fatalf("concurrent learning result=%+v err=%v", response.result, response.err)
			}
			contact, err := saved.ContactByID("synthetic-peer")
			if err != nil || contact.Name == "" || contact.Name == "Old" || saved.OwnUserID() != initial.OwnUserID {
				t.Fatalf("learned=%+v err=%v", contact, err)
			}
			if activeDirectory && len(saved.ListContacts()) != 2 {
				t.Fatal("学习未保留其他联系人或重复添加同一ID")
			}
			if initialDirectory := client.config.Contacts; initialDirectory != nil && initialDirectory.ListContacts()[1].Name != "Old" {
				t.Fatal("运行中修改了config.Contacts")
			}
			var callers sync.WaitGroup
			for i := 0; i < 4; i++ {
				callers.Add(1)
				go func() {
					defer callers.Done()
					result, err := client.SendUser(context.Background(), "synthetic-peer", "synthetic learned message")
					if err != nil || !result.TransportAcknowledged {
						t.Errorf("result=%+v err=%v", result, err)
					}
				}()
			}
			callers.Wait()
			finishResident(t, cancel, done, peerDone)
			stopReads()
			readers.Wait()
			if snapshot := client.Snapshot(); snapshot.Online || snapshot.PeerUserID != "" || len(snapshot.Contacts) != len(saved.ListContacts()) {
				t.Fatalf("stopped=%+v", snapshot)
			}
		})
	}
}

// TestSnapshotDirectoryCallbackFailure 验证目录持久化失败不会公布学习结果或READY，运行返回原错误且不提交聊天。
func TestSnapshotDirectoryCallbackFailure(t *testing.T) {
	recordErr := errors.New("synthetic directory persistence failure")
	client, start := snapshotFixture(t, func(config *ClientConfig) {
		config.LearnContacts = true
		config.ObserveDirectory = func(*Directory) error { return recordErr }
	})
	_, done, peerDone := start(func(event ClientEvent) error {
		if event.Stage == "protocol_ready" {
			return errors.New("持久化失败却公布READY")
		}
		return nil
	})
	select {
	case outcome := <-done:
		if !errors.Is(outcome.err, recordErr) || outcome.result.ProtocolReady || outcome.result.ChatSends != 0 {
			t.Fatalf("result=%+v err=%v", outcome.result, outcome.err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("目录保存失败后未退出")
	}
	<-peerDone // 对端看到未发送QUIT的失败断开，不将其伪装为健康会话退出。
	if snapshot := client.Snapshot(); snapshot.Online || snapshot.PeerUserID != "" || len(snapshot.Contacts) != 0 {
		t.Fatalf("failed=%+v", snapshot)
	}
	if client.directory.Load() != nil {
		t.Fatal("失败目录被发布到发送路径")
	}
}

// TestSnapshotDefaultStillRequiresDirectory 验证未启用学习时不调用持久化回调、不自动获取联系人，旧单聊目录必需语义保留。
func TestSnapshotDefaultStillRequiresDirectory(t *testing.T) {
	client, start := snapshotFixture(t, func(config *ClientConfig) {
		config.ObserveDirectory = func(*Directory) error { return errors.New("默认模式不应学习") }
	})
	events := make(chan ClientEvent, 64)
	cancel, done, peerDone := start(func(event ClientEvent) error { events <- event; return nil })
	awaitResidentEvent(t, events, "protocol_ready", "")
	if snapshot := client.Snapshot(); !snapshot.Online || snapshot.PeerUserID != "synthetic-peer" || len(snapshot.Contacts) != 0 {
		t.Fatalf("snapshot=%+v", snapshot)
	}
	if result, err := client.SendUser(context.Background(), "synthetic-peer", "synthetic"); err == nil || result.Submitted {
		t.Fatal("默认模式无目录仍允许单聊")
	}
	finishResident(t, cancel, done, peerDone)
}

// TestSnapshotLearningWithoutObserver 验证显式学习允许仅维护内存目录，不要求调用方安装持久化回调。
func TestSnapshotLearningWithoutObserver(t *testing.T) {
	client, start := snapshotFixture(t, func(config *ClientConfig) { config.LearnContacts = true })
	events := make(chan ClientEvent, 64)
	cancel, done, peerDone := start(func(event ClientEvent) error { events <- event; return nil })
	awaitResidentEvent(t, events, "protocol_ready", "")
	if snapshot := client.Snapshot(); !snapshot.Online || len(snapshot.Contacts) != 1 || snapshot.Contacts[0].UserID != snapshot.PeerUserID {
		t.Fatalf("snapshot=%+v", snapshot)
	}
	finishResident(t, cancel, done, peerDone)
}

// TestSnapshotGroupRevocation 验证快照深拷贝成员，版本变化或本机退群时在阶段事件之前撤销旧完整成员。
func TestSnapshotGroupRevocation(t *testing.T) {
	client := &Client{config: ClientConfig{Node: NodePacket{UserID: "synthetic-local"}}, peer: newSyntheticPeer(t),
		origin: time.Now(), groups: map[string]GroupInfo{}, groupPending: map[string]bool{}}
	var stages []string
	client.observe = func(event ClientEvent) error {
		stages = append(stages, event.Stage)
		groups := client.Snapshot().Groups
		if event.Stage == "group_synchronized" && len(groups) != 1 {
			return errors.New("群同步事件早于快照")
		}
		if (event.Stage == "group_info_received" || event.Stage == "group_membership_absent") && len(groups) != 0 {
			return errors.New("成员撤销事件仍公布旧快照")
		}
		return nil
	}
	group := GroupInfo{GroupID: "synthetic-group", UserVersion: 10, Members: []string{"synthetic-local", "synthetic-peer"}}
	if err := client.saveGroup(group, CommandGroupUserPush); err != nil {
		t.Fatal(err)
	}
	snapshot := client.Snapshot()
	snapshot.Groups[0].Name = "reader-owned"
	snapshot.Groups[0].Members[0] = "reader-owned"
	if copied := client.Snapshot().Groups[0]; copied.Name != "" || copied.Members[0] != "synthetic-local" {
		t.Fatalf("copied=%+v", copied)
	}
	push := syntheticGroupPush(CommandGroupInfoPush)
	push.Payload = strings.Replace(push.Payload, "<QGROUP_USER_VER>10</QGROUP_USER_VER>", "<QGROUP_USER_VER>11</QGROUP_USER_VER>", 1)
	wire, err := Encode(push.Command, push.Payload)
	if err != nil {
		t.Fatal(err)
	}
	// 标记请求已发，单独检验撤销分支而不为此单元测试创建网络传输。
	client.groupPending[group.GroupID] = true
	if err := client.receiveGroupPush(context.Background(), wire, push); err != nil {
		t.Fatal(err)
	}
	if len(client.Snapshot().Groups) != 0 {
		t.Fatal("版本变化后仍保留旧群")
	}
	if err := client.saveGroup(group, CommandGroupUserPush); err != nil {
		t.Fatal(err)
	}
	group.Members = []string{"synthetic-peer"}
	if err := client.saveGroup(group, CommandGroupUserPush); err != nil {
		t.Fatal(err)
	}
	if len(client.Snapshot().Groups) != 0 || len(stages) != 4 {
		t.Fatalf("snapshot=%+v stages=%v", client.Snapshot(), stages)
	}
}
