// 本文件验证历史尝试兼容与异常日志拒绝，只创建测试临时目录，不访问真实APPDATA记录。
package ipc

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"neiwangtong-gateway/internal/windowprobe"
)

// TestLegacyAttemptFilesRemainReserved 验证正式Adapter仍拒绝首次及03/04的同名旧文件，不改写其内容。
func TestLegacyAttemptFilesRemainReserved(t *testing.T) {
	base := t.TempDir()
	t.Setenv("APPDATA", base)
	t.Setenv("XDG_CONFIG_HOME", base)
	d, _, calls := fixture(t)
	d.journalPath = journalPath
	a := adapterFixture(t, Config{AuthorizedGroupID: testGroupID}, d)
	for _, id := range []string{"NWT-GW-AUTH-TEST-0001", "NWT-GW-AUTH-TEST-0001-confirmation-03", "NWT-GW-AUTH-TEST-0001-confirmation-04"} {
		path, err := journalPath(id)
		if err != nil {
			t.Fatal(err)
		}
		original := []byte("existing historical attempt; preserve even if incomplete\n")
		if err := os.WriteFile(path, original, 0600); err != nil {
			t.Fatal(err)
		}
		result, err := a.SendGroup(id)
		if !errors.Is(err, os.ErrExist) || result.AttemptReserved || result.Call.Invoked {
			t.Fatalf("旧标识恢复了发送资格: %+v %v", result, err)
		}
		after, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(original, after) {
			t.Fatal("旧文件被改写")
		}
	}
	if *calls != 0 {
		t.Fatal("触发了旧任务发送")
	}
}

// TestUninvokedCallCannotBecomeObserved 验证未进入Win32的失败任务不能追加独立接收成功。
func TestUninvokedCallCannotBecomeObserved(t *testing.T) {
	d, path, _ := fixture(t)
	d.send = func(windowprobe.Target, []byte) (CallResult, error) {
		return CallResult{}, errors.New("sender window failed before API")
	}
	a := adapterFixture(t, Config{AuthorizedGroupID: testGroupID}, d)
	if result, err := a.SendGroup(testAttemptID); err == nil || result.Call.Invoked {
		t.Fatal("测试未产生调用前失败")
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if delivery, err := a.RecordObservation(testAttemptID, Observation{RecipientObservation, "one"}); err == nil || delivery != Unknown {
		t.Fatal("未调用任务被标记为送达")
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("无实际调用仍追加观察")
	}
}

// TestMalformedJournalRejectsObservation 验证损坏、无调用、重复调用及空结果的日志均原样保留。
func TestMalformedJournalRejectsObservation(t *testing.T) {
	reserved, err := json.Marshal(map[string]any{"event": "attempt_reserved", "attempt_id": testAttemptID, "group_id": testGroupID, "text": Message})
	if err != nil {
		t.Fatal(err)
	}
	call := []byte(`{"event":"call_result","result":{"invoked":true,"completed":true}}`)
	for name, data := range map[string][]byte{
		"invalid-json":     []byte("not-json\n"),
		"no-call":          append(append([]byte{}, reserved...), '\n'),
		"null-result":      append(append(append([]byte{}, reserved...), '\n'), []byte("{\"event\":\"call_result\",\"result\":null}\n")...),
		"duplicate-result": bytes.Join([][]byte{reserved, call, call, nil}, []byte{'\n'}),
	} {
		t.Run(name, func(t *testing.T) {
			d, path, _ := fixture(t)
			if err := os.WriteFile(path, data, 0600); err != nil {
				t.Fatal(err)
			}
			a := adapterFixture(t, Config{AuthorizedGroupID: testGroupID}, d)
			if _, err := a.RecordObservation(testAttemptID, Observation{RecipientObservation, "one"}); err == nil {
				t.Fatal("无效日志被当作实际发送")
			}
			after, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(data, after) {
				t.Fatal("无效日志被修改")
			}
		})
	}
}

// TestConfigurationIsReusable 验证模块公开读取配置而不依赖cmd包，也不泄露无效ID。
func TestConfigurationIsReusable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	valid := []byte(`{"authorized_group_id":"` + testGroupID + `"}`)
	if err := os.WriteFile(path, valid, 0600); err != nil {
		t.Fatal(err)
	}
	config, err := LoadConfig(path)
	if err != nil || config.AuthorizedGroupID != testGroupID {
		t.Fatalf("共享配置未原样读取: %v", err)
	}
	if err := os.WriteFile(path, append(valid, valid...), 0600); err != nil {
		t.Fatal(err)
	}
	config, err = LoadConfig(path)
	if err == nil || config != (Config{}) || bytes.Contains([]byte(err.Error()), []byte(testGroupID)) {
		t.Fatal("歧义配置被接受或泄露")
	}
}
