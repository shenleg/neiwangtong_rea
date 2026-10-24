// 本文件验证显式原始user_id、配置读取兼容、群/用户单次控制及观察日志，仅使用合成目标与模拟发送。
package ipc

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"neiwangtong-gateway/internal/windowprobe"
)

const testUserID = "synthetic-user_1"

// TestUserConfig 验证实验命令仍可原样读取单用户、双目标类型和原单群配置，字段顺序不影响结果。
func TestUserConfig(t *testing.T) {
	for name, config := range map[string]Config{
		"user":     {AuthorizedUserID: testUserID},
		"group":    {AuthorizedGroupID: testGroupID},
		"both":     {AuthorizedGroupID: testGroupID, AuthorizedUserID: testUserID},
		"max-user": {AuthorizedUserID: strings.Repeat("A", 199)},
		"unicode":  {AuthorizedUserID: " 原样 ID-🙂 ,;| "},
		"max-utf8": {AuthorizedUserID: strings.Repeat("界", 66) + "A"},
	} {
		t.Run(name, func(t *testing.T) {
			data, err := json.Marshal(config)
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(t.TempDir(), "config.json")
			if err := os.WriteFile(path, data, 0600); err != nil {
				t.Fatal(err)
			}
			got, err := LoadConfig(path)
			if err != nil || got != config {
				t.Fatalf("配置未原样读取: %v", err)
			}
		})
	}
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"authorized_user_id":"`+testUserID+`","authorized_group_id":"`+testGroupID+`"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if got, err := LoadConfig(path); err != nil || got != (Config{AuthorizedGroupID: testGroupID, AuthorizedUserID: testUserID}) {
		t.Fatalf("字段顺序影响配置: %v", err)
	}
}

// TestUserConfigRejectsInvalidTargets 验证不接受用户列表、名称/IP选择器、重复键、空值及非法字符，不泄露ID。
func TestUserConfigRejectsInvalidTargets(t *testing.T) {
	invalid := []string{
		`{"authorized_user_id":null}`, `{"authorized_user_id":123}`,
		`{"authorized_user_id":["` + testUserID + `"]}`,
		`{"authorized_user_id":"` + testUserID + `","authorized_user_id":"other"}`,
		`{"authorized_user_id":"` + testUserID + `","authorized_group_id":""}`,
		`{"authorized_user_id":"` + testUserID + `","user_name":"name"}`,
		`{"user_ip":"127.0.0.1"}`, `{"user_id":"` + testUserID + `"}`,
		`{"authorized_user_id":"` + testUserID + `"}junk`,
		`{"authorized_user_id":"bad\ud800"}`,
		`{"authorized_user_id":"bad\udc00"}`,
		`{"authorized_user_id":"bad\ud800A"}`,
		"{\"authorized_user_id\":\"bad\xff\"}",
	}
	for _, id := range []string{"", strings.Repeat("a", 200), strings.Repeat("界", 67), "a\x00b"} {
		data, err := json.Marshal(map[string]string{"authorized_user_id": id})
		if err != nil {
			t.Fatal(err)
		}
		invalid = append(invalid, string(data))
	}
	for _, content := range invalid {
		path := filepath.Join(t.TempDir(), "config.json")
		if err := os.WriteFile(path, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
		got, err := LoadConfig(path)
		if err == nil || got != (Config{}) || strings.Contains(err.Error(), testUserID) {
			t.Fatal("无效用户配置被接受或泄露目标")
		}
	}
}

// TestUserIDPreserved 验证联系人原始ID不被格式假设、空白处理或Unicode损坏改变。
func TestUserIDPreserved(t *testing.T) {
	for _, id := range []string{"a", " ", "other", strings.ToUpper(testUserID), " 原样 ID-🙂 ,;| ", strings.Repeat("A", 199), strings.Repeat("界", 66) + "A", `literal\ud800`, "replacement-�"} {
		d, _, _ := fixture(t)
		a := adapterFixture(t, d)
		a.d.send = func(_ windowprobe.Target, payload []byte) (CallResult, error) {
			if string(payload[targetOffset:targetOffset+len(id)]) != id || payload[targetOffset+len(id)] != 0 {
				t.Fatal("原始ID被改写或NUL不在传送区内")
			}
			return classifyCall(1, 1, 0, time.Millisecond), nil
		}
		if _, err := a.SendUser(id, Message); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := buildPayload("user_id", "bad\xff", Message); err == nil {
		t.Fatal("非法UTF-8被接受")
	}
	path := filepath.Join(t.TempDir(), "config.json")
	for _, content := range []string{`{"authorized_user_id":"🙂"}`, `{"authorized_user_id":"literal\\ud800"}`} {
		if err := os.WriteFile(path, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadConfig(path); err != nil {
			t.Fatalf("合法Unicode代理对或转义反斜线被拒绝: %v", err)
		}
	}
}

// TestSendUserPayloadAndObservation 验证显式user_id偏移与NUL、调用前落盘以及独立接收观察。
func TestSendUserPayloadAndObservation(t *testing.T) {
	d, path, calls := fixture(t)
	a := adapterFixture(t, d)
	originalSend := a.d.send
	a.d.send = func(target windowprobe.Target, payload []byte) (CallResult, error) {
		if len(payload) != 322 {
			t.Fatal("单聊改变了固定载荷长度")
		}
		for offset, text := range map[int]string{0: "user_id", 0x64: testUserID, 0x12c: Message} {
			if string(payload[offset:offset+len(text)]) != text || payload[offset+len(text)] != 0 {
				t.Fatalf("单聊字段或NUL错误: offset=%x", offset)
			}
		}
		data, err := os.ReadFile(path)
		if err != nil || !bytes.Contains(data, []byte(`"user_id":"`+testUserID+`"`)) || bytes.Contains(data, []byte(`"group_id"`)) {
			t.Fatal("调用前没有保存正确的用户目标类型和ID")
		}
		return originalSend(target, payload)
	}
	result, err := a.SendUser(testUserID, Message)
	if err != nil || !result.AttemptReserved || !result.Call.Completed || result.Delivery != Unknown || *calls != 1 {
		t.Fatalf("单聊调用事实错误: %+v %v", result, err)
	}
	visible, err := json.Marshal(result)
	if err != nil || bytes.Contains(visible, []byte(testUserID)) || bytes.Contains(visible, []byte(testGroupID)) {
		t.Fatal("结构化结果泄露目标")
	}
	for _, observation := range []Observation{{SenderObservation, "one"}, {RecipientObservation, "one"}} {
		delivery, err := a.RecordObservation(testAttemptID, observation)
		if err != nil || (delivery == RecipientObserved) != (observation.Kind == RecipientObservation) {
			t.Fatalf("单聊观察结果错误: %s %v", delivery, err)
		}
	}
	if _, err := a.SendUser(testUserID, Message); !errors.Is(err, os.ErrExist) || *calls != 1 {
		t.Fatal("已占用单聊被重发")
	}
}

// TestMissingTargetDoesNotSwitchKind 验证空目标不改用另一种目标类型，也不创建记录。
func TestMissingTargetDoesNotSwitchKind(t *testing.T) {
	d, path, calls := fixture(t)
	a := adapterFixture(t, d)
	for _, send := range []func(string, string) (SendResult, error){a.SendGroup, a.SendUser} {
		result, err := send("", Message)
		if err == nil || result.AttemptReserved || result.Call.Invoked || *calls != 0 {
			t.Fatal("空目标仍发送或切换目标类型")
		}
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("空目标仍占用记录")
		}
	}
}

// TestGroupAndUserShareAttemptReservation 验证群与用户并发争用同名请求时只有一次调用，不新增日志命名空间。
func TestGroupAndUserShareAttemptReservation(t *testing.T) {
	d, _, _ := fixture(t)
	var calls atomic.Int32
	d.send = func(windowprobe.Target, []byte) (CallResult, error) {
		calls.Add(1)
		return classifyCall(1, 1, 0, time.Millisecond), nil
	}
	group, user := adapterFixture(t, d), adapterFixture(t, d)
	var wg sync.WaitGroup
	for _, send := range []func() (SendResult, error){
		func() (SendResult, error) { return group.SendGroup(testGroupID, Message) },
		func() (SendResult, error) { return user.SendUser(testUserID, Message) },
	} {
		wg.Add(1)
		go func(send func() (SendResult, error)) {
			defer wg.Done()
			_, _ = send()
		}(send)
	}
	wg.Wait()
	if calls.Load() != 1 {
		t.Fatalf("群/用户共用标识发生%d次调用", calls.Load())
	}
}

// TestUserObservationChecksTargetKind 验证同一合法ID可分别作为群或用户观察，双目标及无目标记录拒绝追加。
func TestUserObservationChecksTargetKind(t *testing.T) {
	for _, selector := range []string{"group_id", "user_id", "both", "neither"} {
		t.Run(selector, func(t *testing.T) {
			d, path, _ := fixture(t)
			reservation := map[string]any{"event": "attempt_reserved", "attempt_id": testAttemptID, "text": Message}
			if selector == "group_id" || selector == "both" {
				reservation["group_id"] = testGroupID
			}
			if selector == "user_id" || selector == "both" {
				reservation["user_id"] = testGroupID
			}
			reserved, err := json.Marshal(reservation)
			if err != nil {
				t.Fatal(err)
			}
			data := append(reserved, []byte("\n{\"event\":\"call_result\",\"result\":{\"invoked\":true}}\n")...)
			if err := os.WriteFile(path, data, 0600); err != nil {
				t.Fatal(err)
			}
			a := adapterFixture(t, d)
			delivery, err := a.RecordObservation(testAttemptID, Observation{RecipientObservation, "one"})
			valid := selector == "group_id" || selector == "user_id"
			if valid && (err != nil || delivery != RecipientObserved) || !valid && (err == nil || delivery != Unknown) {
				t.Fatalf("目标类型校验错误: delivery=%s err=%v", delivery, err)
			}
			after, err := os.ReadFile(path)
			if err != nil || valid && (!bytes.HasPrefix(after, data) || bytes.Equal(data, after)) || !valid && !bytes.Equal(data, after) {
				t.Fatal("观察未追加或错误目标记录被修改")
			}
		})
	}
}

// TestUserPreCallGuards 验证单聊的标识生成、平台、预检、落盘与最终身份失败均阻止实际调用。
func TestUserPreCallGuards(t *testing.T) {
	for _, stage := range []string{"attempt-id", "platform", "preflight", "journal", "final-identity"} {
		t.Run(stage, func(t *testing.T) {
			d, path, calls := fixture(t)
			stop := errors.New("synthetic pre-call failure")
			checks := 0
			d.check = func(string, *windowprobe.Target) (windowprobe.Target, Preflight, error) {
				checks++
				if stage == "preflight" && checks == 2 || stage == "final-identity" && checks == 3 {
					return windowprobe.Target{}, Preflight{}, stop
				}
				return windowprobe.Target{}, Preflight{}, nil
			}
			switch stage {
			case "attempt-id":
				d.newAttemptID = func() (string, error) { return "", stop }
			case "platform":
				d.allowSend = func() error { return stop }
			case "journal":
				d.journalPath = func(string) (string, error) { return "", stop }
			}
			a := adapterFixture(t, d)
			result, err := a.SendUser(testUserID, Message)
			if err == nil || result.Call.Invoked || *calls != 0 || result.Delivery != Unknown || result.AttemptReserved != (stage == "final-identity") {
				t.Fatalf("单聊调用前失败处理错误: %+v %v", result, err)
			}
			if stage == "final-identity" {
				data, err := os.ReadFile(path)
				if err != nil || !bytes.Contains(data, []byte("stopped_before_call")) {
					t.Fatal("最终身份失败未保留占用记录")
				}
				if _, err := a.SendUser(testUserID, Message); !errors.Is(err, os.ErrExist) || *calls != 0 {
					t.Fatal("最终身份失败恢复了单聊发送资格")
				}
			} else if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("调用前拒绝仍创建记录")
			}
		})
	}
}

// TestSendUserRejectsInvalidID 验证空值、NUL、非法UTF-8及超过199字节的用户ID不生成标识、不预检或占用日志。
func TestSendUserRejectsInvalidID(t *testing.T) {
	for _, id := range []string{"", "bad\x00id", "bad\xff", strings.Repeat("a", 200), strings.Repeat("界", 67)} {
		d, path, calls := fixture(t)
		a := adapterFixture(t, d)
		a.d.check = func(string, *windowprobe.Target) (windowprobe.Target, Preflight, error) {
			t.Fatal("非法用户ID仍执行预检")
			return windowprobe.Target{}, Preflight{}, nil
		}
		a.d.newAttemptID = func() (string, error) { t.Fatal("非法用户ID仍生成标识"); return "", nil }
		result, err := a.SendUser(id, Message)
		if err == nil || result.AttemptID != "" || result.AttemptReserved || result.Call.Invoked || result.Delivery != Unknown || *calls != 0 {
			t.Fatalf("非法目标仍产生副作用: %+v %v", result, err)
		}
		if strings.Contains(err.Error(), testUserID) {
			t.Fatal("错误信息泄露用户ID")
		}
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("非法目标仍占用记录")
		}
	}
}

// TestUserTimeoutRemainsReserved 验证单聊超时不重试，重建Adapter也不能复用标识。
func TestUserTimeoutRemainsReserved(t *testing.T) {
	d, _, calls := fixture(t)
	d.send = func(windowprobe.Target, []byte) (CallResult, error) {
		*calls++
		return classifyCall(0, 0, 1460, 5*time.Second), nil
	}
	a := adapterFixture(t, d)
	result, err := a.SendUser(testUserID, Message)
	if err == nil || !result.AttemptReserved || !result.Call.Invoked || result.Call.Timeout != "yes" || result.Delivery != Unknown {
		t.Fatalf("单聊超时结果错误: %+v %v", result, err)
	}
	if _, err := adapterFixture(t, d).SendUser(testUserID, Message); !errors.Is(err, os.ErrExist) || *calls != 1 {
		t.Fatal("单聊超时后重发")
	}
}
