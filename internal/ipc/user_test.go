// 本文件验证配置限定的原生user_id入口、群/用户隔离及共用单次控制，仅使用合成目标与模拟发送。
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

// TestUserConfig 验证单用户、双目标类型和原单群配置均原样读取，字段顺序不影响绑定。
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
		d, _, _ := fixture(t)
		d.check = func(string, *windowprobe.Target) (windowprobe.Target, Preflight, error) {
			t.Fatal("无效用户配置仍查询窗口")
			return windowprobe.Target{}, Preflight{}, nil
		}
		// 空Config是已定义的只读模式；显式空JSON字段仍由LoadConfig拒绝。
		if id != "" {
			if _, err := newAdapter("synthetic.exe", Config{AuthorizedUserID: id}, d); err == nil {
				t.Fatal("构造接受无效用户配置")
			}
		}
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
	for _, id := range []string{" 原样 ID-🙂 ,;| ", strings.Repeat("界", 66) + "A", `literal\ud800`, "replacement-�"} {
		d, _, _ := fixture(t)
		a := adapterFixture(t, Config{AuthorizedUserID: id}, d)
		a.d.send = func(_ windowprobe.Target, payload []byte) (CallResult, error) {
			if string(payload[targetOffset:targetOffset+len(id)]) != id || payload[targetOffset+len(id)] != 0 {
				t.Fatal("原始ID被改写或NUL不在传送区内")
			}
			return classifyCall(1, 1, 0, time.Millisecond), nil
		}
		if _, err := a.SendUser(id, testAttemptID); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := buildPayload("user_id", "bad\xff"); err == nil {
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

// TestSendUserPayloadAndObservation 验证配置快照、user_id偏移与NUL、调用前落盘以及独立接收观察。
func TestSendUserPayloadAndObservation(t *testing.T) {
	d, path, calls := fixture(t)
	config := Config{AuthorizedGroupID: testGroupID, AuthorizedUserID: testUserID}
	a := adapterFixture(t, config, d)
	config.AuthorizedUserID = "other"
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
	result, err := a.SendUser(testUserID, testAttemptID)
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
	if _, err := a.SendUser(testUserID, testAttemptID); !errors.Is(err, os.ErrExist) || *calls != 1 {
		t.Fatal("已占用单聊被重发")
	}
}

// TestMissingTargetDoesNotSwitchKind 验证缺少对应授权时不改用另一种目标，也不创建记录。
func TestMissingTargetDoesNotSwitchKind(t *testing.T) {
	for _, config := range []Config{{}, {AuthorizedGroupID: testGroupID}, {AuthorizedUserID: testUserID}} {
		d, path, calls := fixture(t)
		a := adapterFixture(t, config, d)
		send := func(attemptID string) (SendResult, error) { return a.SendUser(testUserID, attemptID) }
		if config.AuthorizedUserID != "" {
			send = a.SendGroup
		}
		result, err := send(testAttemptID)
		if err == nil || result.AttemptReserved || result.Call.Invoked || *calls != 0 {
			t.Fatal("缺少授权仍发送或切换目标类型")
		}
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("缺少授权仍占用记录")
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
	config := Config{AuthorizedGroupID: testGroupID, AuthorizedUserID: testUserID}
	group, user := adapterFixture(t, config, d), adapterFixture(t, config, d)
	var wg sync.WaitGroup
	sendUser := func(attemptID string) (SendResult, error) { return user.SendUser(testUserID, attemptID) }
	for _, send := range []func(string) (SendResult, error){group.SendGroup, sendUser} {
		wg.Add(1)
		go func(send func(string) (SendResult, error)) {
			defer wg.Done()
			_, _ = send(testAttemptID)
		}(send)
	}
	wg.Wait()
	if calls.Load() != 1 {
		t.Fatalf("群/用户共用标识发生%d次调用", calls.Load())
	}
}

// TestUserObservationChecksTargetKind 验证同字符串但不同目标类型、不同用户以及含双目标的损坏记录均拒绝观察。
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
			config := Config{AuthorizedUserID: testGroupID}
			if selector == "user_id" {
				config = Config{AuthorizedGroupID: testGroupID, AuthorizedUserID: testUserID}
			}
			a := adapterFixture(t, config, d)
			if delivery, err := a.RecordObservation(testAttemptID, Observation{RecipientObservation, "one"}); err == nil || delivery != Unknown {
				t.Fatal("错误目标类型或ID被记录为送达")
			}
			after, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(data, after) {
				t.Fatal("错误目标记录被修改")
			}
		})
	}
}

// TestUserPreCallGuards 验证单聊的请求、平台、预检、落盘与最终身份失败均阻止实际调用。
func TestUserPreCallGuards(t *testing.T) {
	for _, stage := range []string{"attempt-id", "platform", "preflight", "journal", "final-identity"} {
		t.Run(stage, func(t *testing.T) {
			d, path, calls := fixture(t)
			stop := errors.New("synthetic pre-call failure")
			attemptID := testAttemptID
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
				attemptID = "../invalid"
			case "platform":
				d.allowSend = func() error { return stop }
			case "journal":
				d.journalPath = func(string) (string, error) { return "", stop }
			}
			a := adapterFixture(t, Config{AuthorizedUserID: testUserID}, d)
			result, err := a.SendUser(testUserID, attemptID)
			if err == nil || result.Call.Invoked || *calls != 0 || result.Delivery != Unknown || result.AttemptReserved != (stage == "final-identity") {
				t.Fatalf("单聊调用前失败处理错误: %+v %v", result, err)
			}
			if stage == "final-identity" {
				data, err := os.ReadFile(path)
				if err != nil || !bytes.Contains(data, []byte("stopped_before_call")) {
					t.Fatal("最终身份失败未保留占用记录")
				}
				if _, err := a.SendUser(testUserID, testAttemptID); !errors.Is(err, os.ErrExist) || *calls != 0 {
					t.Fatal("最终身份失败恢复了单聊发送资格")
				}
			} else if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("调用前拒绝仍创建记录")
			}
		})
	}
}

// TestSendUserRejectsUnconfiguredID 验证运行时目标必须原样匹配授权配置，拒绝时不预检、不占用日志或调用发送。
func TestSendUserRejectsUnconfiguredID(t *testing.T) {
	for _, id := range []string{"", "other", strings.ToUpper(testUserID), " " + testUserID, testUserID + " ", testUserID + ",other", "bad\x00id", "bad\xff", strings.Repeat("a", 200)} {
		d, path, calls := fixture(t)
		a := adapterFixture(t, Config{AuthorizedUserID: testUserID}, d)
		a.d.check = func(string, *windowprobe.Target) (windowprobe.Target, Preflight, error) {
			t.Fatal("不匹配的用户ID仍执行预检")
			return windowprobe.Target{}, Preflight{}, nil
		}
		result, err := a.SendUser(id, testAttemptID)
		if err == nil || result.AttemptID != testAttemptID || result.AttemptReserved || result.Call.Invoked || result.Delivery != Unknown || *calls != 0 {
			t.Fatalf("未授权目标仍产生副作用: %+v %v", result, err)
		}
		if strings.Contains(err.Error(), testUserID) {
			t.Fatal("错误信息泄露授权ID")
		}
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("未授权目标仍占用记录")
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
	config := Config{AuthorizedUserID: testUserID}
	a := adapterFixture(t, config, d)
	result, err := a.SendUser(testUserID, testAttemptID)
	if err == nil || !result.AttemptReserved || !result.Call.Invoked || result.Call.Timeout != "yes" || result.Delivery != Unknown {
		t.Fatalf("单聊超时结果错误: %+v %v", result, err)
	}
	if _, err := adapterFixture(t, config, d).SendUser(testUserID, testAttemptID); !errors.Is(err, os.ErrExist) || *calls != 1 {
		t.Fatal("单聊超时后重发")
	}
}
