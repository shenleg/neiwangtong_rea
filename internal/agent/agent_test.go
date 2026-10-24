// 本文件用合成单表快照与替代 Sender 验证统一入口：懒加载缓存、名称解析语义与发送委托。
package agent

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"time"
	"testing"

	"neiwangtong-gateway/internal/ipc"
)

// fakeSender 记录发送委托的参数与次数，可注入错误；不接触真实客户端。
type fakeSender struct {
	mu         sync.Mutex
	groupCalls []struct{ id, text string }
	userCalls  []struct{ id, text string }
	sendErr    error
}

func (f *fakeSender) Preflight() (ipc.Preflight, error) { return ipc.Preflight{}, nil }

func (f *fakeSender) SendGroup(groupID, text string) (ipc.SendResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.groupCalls = append(f.groupCalls, struct{ id, text string }{groupID, text})
	return ipc.SendResult{AttemptID: "NWT-GW-AGENT-GROUP", AttemptReserved: true, Delivery: ipc.Unknown}, f.sendErr
}

func (f *fakeSender) SendUser(userID, text string) (ipc.SendResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.userCalls = append(f.userCalls, struct{ id, text string }{userID, text})
	return ipc.SendResult{AttemptID: "NWT-GW-AGENT-USER", AttemptReserved: true, Delivery: ipc.Unknown}, f.sendErr
}

// agentManifest 对应 tools/gen_test_fixtures.py 输出的 agent 夹具清单。
type agentManifest struct {
	Password          string       `json:"password"`
	NRSHA256          string       `json:"nr_sha256"`
	QRPSHA256         string       `json:"qrp_sha256"`
	ExpectedContacts  [][2]string  `json:"expected_contacts"`
	ExpectedGroups    [][2]string  `json:"expected_groups"`
}

// loadAgentManifest 读取夹具清单；缺失即失败。
func loadAgentManifest(t *testing.T) agentManifest {
	t.Helper()
	data, err := os.ReadFile("testdata/agent-manifest.json")
	if err != nil {
		t.Fatalf("读取清单: %v", err)
	}
	var manifest agentManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		t.Fatalf("解析清单: %v", err)
	}
	return manifest
}

// newFixtureAgent 用真实单表快照与替代 Sender 构造被测入口。
func newFixtureAgent(t *testing.T) (*Agent, *fakeSender) {
	t.Helper()
	manifest := loadAgentManifest(t)
	sender := &fakeSender{}
	unified := New(func() (Sender, error) { return sender, nil }, Options{
		NRPath:     "testdata/nr-agent.db",
		NRPassword: []byte(manifest.Password),
		QRPPath:    "testdata/qrp-agent.db",
	})
	return unified, sender
}

// TestListContactsAndGroups 验证单表快照的列表结果与清单一致。
func TestListContactsAndGroups(t *testing.T) {
	unified, _ := newFixtureAgent(t)
	manifest := loadAgentManifest(t)

	snapshot, err := unified.ListContacts()
	if err != nil {
		t.Fatalf("读取联系人快照: %v", err)
	}
	if snapshot.Table != "OrgStruct_AGENT" || len(snapshot.Users) != len(manifest.ExpectedContacts) {
		t.Fatalf("联系人快照 %+v 不符预期", snapshot.Table)
	}
	groupSnapshot, err := unified.ListGroups()
	if err != nil {
		t.Fatalf("读取群快照: %v", err)
	}
	if groupSnapshot.Table != "QGroupInfo_AGENT" || len(groupSnapshot.Groups) != len(manifest.ExpectedGroups) {
		t.Fatalf("群快照表名 %s 不符预期", groupSnapshot.Table)
	}
}

// TestResolveUserName 验证联系人名称解析：唯一命中、同名歧义与未命中。
func TestResolveUserName(t *testing.T) {
	unified, _ := newFixtureAgent(t)
	unique, err := unified.ResolveUserName("Alice")
	if err != nil || unique.UserID != "agent-alice-01" {
		t.Errorf("唯一命中 %+v（错误 %v）不符预期", unique, err)
	}
	_, err = unified.ResolveUserName("Carol")
	var ambiguous *AmbiguousContactsError
	if !errors.As(err, &ambiguous) || len(ambiguous.Matches) != 2 {
		t.Errorf("同名应返回两候选的歧义错误，实际 %v", err)
	}
	_, err = unified.ResolveUserName("Nobody")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("未命中应返回 ErrNotFound，实际 %v", err)
	}
}

// TestResolveGroupName 验证群名解析：唯一命中、同名歧义与未命中。
func TestResolveGroupName(t *testing.T) {
	unified, _ := newFixtureAgent(t)
	unique, err := unified.ResolveGroupName("研发群")
	if err != nil || unique.GroupID != "agent-group-01" {
		t.Errorf("唯一命中 %+v（错误 %v）不符预期", unique, err)
	}
	_, err = unified.ResolveGroupName("值班群")
	var ambiguous *AmbiguousGroupsError
	if !errors.As(err, &ambiguous) || len(ambiguous.Matches) != 2 {
		t.Errorf("同名应返回两候选的歧义错误，实际 %v", err)
	}
	_, err = unified.ResolveGroupName("不存在的群")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("未命中应返回 ErrNotFound，实际 %v", err)
	}
}

// TestSendDelegation 验证发送按参数委托且每次调用创建新发送器。
func TestSendDelegation(t *testing.T) {
	unified, sender := newFixtureAgent(t)
	unified.SendToUser("agent-alice-01", "hi user")
	unified.SendToGroup("agent-group-01", "hi group")
	if len(sender.userCalls) != 1 || sender.userCalls[0].id != "agent-alice-01" || sender.userCalls[0].text != "hi user" {
		t.Errorf("用户发送委托参数不符: %+v", sender.userCalls)
	}
	if len(sender.groupCalls) != 1 || sender.groupCalls[0].id != "agent-group-01" || sender.groupCalls[0].text != "hi group" {
		t.Errorf("群发送委托参数不符: %+v", sender.groupCalls)
	}
}

// TestClientUnavailable 验证发送器构造失败包装为 ErrClientUnavailable。
func TestClientUnavailable(t *testing.T) {
	unified := New(func() (Sender, error) { return nil, errors.New("找不到窗口") }, Options{})
	if _, err := unified.SendToUser("agent-alice-01", "hi"); !errors.Is(err, ErrClientUnavailable) {
		t.Errorf("发送器构造失败应包装 ErrClientUnavailable，实际 %v", err)
	}
	if _, err := unified.Preflight(); !errors.Is(err, ErrClientUnavailable) {
		t.Errorf("预检失败应包装 ErrClientUnavailable，实际 %v", err)
	}
}

// TestNotConfigured 验证省略快照时解析与列表返回 ErrNotConfigured，按 ID 发送不受影响。
func TestNotConfigured(t *testing.T) {
	sender := &fakeSender{}
	unified := New(func() (Sender, error) { return sender, nil }, Options{})
	if _, err := unified.ListContacts(); !errors.Is(err, ErrNotConfigured) {
		t.Errorf("未配置 nr 应返回 ErrNotConfigured，实际 %v", err)
	}
	if _, err := unified.ListGroups(); !errors.Is(err, ErrNotConfigured) {
		t.Errorf("未配置 qrp 应返回 ErrNotConfigured，实际 %v", err)
	}
	if _, err := unified.SendToUser("agent-alice-01", "hi"); err != nil {
		t.Errorf("按 ID 发送不应依赖快照，实际错误 %v", err)
	}
}

// TestSnapshotAutoReload 验证 mtime 变化触发真实重载：
// 换成垃圾内容后读取必须失败（证明没有沿用缓存），恢复原文件后又成功。
// 对应把 -qrp 指向客户端实时数据库（或替换副本文件）的使用方式。
func TestSnapshotAutoReload(t *testing.T) {
	source, err := os.ReadFile("testdata/qrp-agent.db")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "qrp-live")
	if err := os.WriteFile(path, source, 0o600); err != nil {
		t.Fatal(err)
	}
	unified := New(func() (Sender, error) { return &fakeSender{}, nil }, Options{QRPPath: path})
	if _, err := unified.ListGroups(); err != nil {
		t.Fatal(err)
	}
	// 覆盖为垃圾并推进 mtime：下一次读取必须因重载失败而报错。
	if err := os.WriteFile(path, []byte("not a sqlite file anymore"), 0o600); err != nil {
		t.Fatal(err)
	}
	future := time.Now().Add(time.Minute)
	if err := os.Chtimes(path, future, future); err != nil {
		t.Fatal(err)
	}
	if _, err := unified.ListGroups(); err == nil {
		t.Fatal("mtime 变化后应重新加载并发现文件损坏，而不是沿用缓存")
	}
	// 恢复原文件并再次推进 mtime：读取恢复成功。
	if err := os.WriteFile(path, source, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, future.Add(time.Minute), future.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	snapshot, err := unified.ListGroups()
	if err != nil || len(snapshot.Groups) == 0 {
		t.Fatalf("恢复后应重新加载成功，实际 err=%v groups=%d", err, len(snapshot.Groups))
	}
}

// TestSnapshotConcurrentWrite 用可注入的 load 函数确定性模拟读后复核要拦的两种竞态：
// 加载期间 mtime 前进（客户端完成了一次写事务）与边文件出现（事务进行中）。
// 同时验证可疑结果不会被缓存——失败后下一次 get 重新执行加载。
func TestSnapshotConcurrentWrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "live")
	if err := os.WriteFile(path, []byte("v1"), 0o600); err != nil {
		t.Fatal(err)
	}

	// 场景一：load 期间文件被改写，mtime 前进。
	loads := 0
	cache := &snapshotCache[bool]{
		path: path,
		load: func(p string) (bool, error) {
			loads++
			if loads == 1 {
				future := time.Now().Add(2 * time.Minute)
				if err := os.WriteFile(p, []byte("v2"), 0o600); err != nil {
					t.Fatal(err)
				}
				if err := os.Chtimes(p, future, future); err != nil {
					t.Fatal(err)
				}
			}
			return true, nil
		},
	}
	if _, err := cache.get(); !errors.Is(err, ErrConcurrentWrite) {
		t.Fatalf("加载期间 mtime 变化应返回 ErrConcurrentWrite，实际 %v", err)
	}
	// 失败后未缓存：下一次 get 重新加载并成功。
	if v, err := cache.get(); err != nil || !v || loads != 2 {
		t.Fatalf("复核失败后应重新加载，实际 err=%v loads=%d", err, loads)
	}

	// 场景二：load 期间出现事务边文件。
	journalLoads := 0
	journalCache := &snapshotCache[bool]{
		path: path,
		load: func(p string) (bool, error) {
			journalLoads++
			if journalLoads == 1 {
				if err := os.WriteFile(p+"-journal", nil, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			return true, nil
		},
	}
	if _, err := journalCache.get(); !errors.Is(err, ErrConcurrentWrite) {
		t.Fatalf("加载期间边文件出现应返回 ErrConcurrentWrite，实际 %v", err)
	}
	if err := os.Remove(path + "-journal"); err != nil {
		t.Fatal(err)
	}
	if _, err := journalCache.get(); err != nil {
		t.Fatalf("边文件消失后应加载成功，实际 %v", err)
	}
}
