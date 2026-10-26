// 本文件验证上线前自定义显示名称的HTTP契约、联网前校验及在线改名拒绝；仅使用合成会话和临时目录。
package nativeweb

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
)

// TestSessionStartsWithCustomName 验证自定义名称进入本次配置与运行记录，保留原身份/联系人，省略参数后继续使用当前名称。
func TestSessionStartsWithCustomName(t *testing.T) {
	manager, fake := testManager(t)
	configs := make(chan Config, 2)
	manager.factory = func(config Config, _ string, _ *journal) (session, error) {
		configs <- config
		fake.mu.Lock()
		fake.snapshot.Online = true
		fake.mu.Unlock()
		return fake, nil
	}
	handler := testHandler(t, manager)
	const name = "  我的 Go 网关 & <节点> 🙂  "
	body, err := json.Marshal(map[string]string{"name": name})
	if err != nil {
		t.Fatal(err)
	}
	response := performRequest(handler, http.MethodPost, "/api/session/start", string(body))
	if response.Code != http.StatusAccepted {
		t.Fatalf("自定义名称未受理：%d %s", response.Code, response.Body.String())
	}
	state := awaitPhase(t, manager, "online")
	if state.Name != name || state.OwnUserID != "synthetic-own" || len(state.Contacts) != 2 {
		t.Fatalf("改名错误改变身份或联系人：%+v", state)
	}
	if config := <-configs; config.Name != name {
		t.Fatalf("实际会话未使用所填名称：%q", config.Name)
	}
	response = performRequest(handler, http.MethodPost, "/api/session/start", `{"name":"在线不能换名"}`)
	if response.Code != http.StatusConflict || manager.State().Name != name {
		t.Fatal("在线改名应拒绝且不修改当前名称")
	}
	manager.Stop()
	awaitPhase(t, manager, "offline")
	files, err := filepath.Glob(filepath.Join(manager.config.DataDir, "session-*.jsonl"))
	if err != nil || len(files) != 1 {
		t.Fatalf("运行记录异常：%v %v", files, err)
	}
	data, err := os.ReadFile(files[0])
	if err != nil {
		t.Fatal(err)
	}
	var record struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(bytes.NewReader(data)).Decode(&record); err != nil {
		t.Fatal(err)
	}
	if record.Name != name {
		t.Fatalf("记录没有保存实际名称：%q", record.Name)
	}
	response = performRequest(handler, http.MethodPost, "/api/session/start", `{}`)
	if response.Code != http.StatusAccepted {
		t.Fatalf("旧的空对象上线请求不兼容：%d %s", response.Code, response.Body.String())
	}
	awaitPhase(t, manager, "online")
	if config := <-configs; config.Name != name {
		t.Fatal("省略名称时没有沿用本进程当前名称")
	}
}

// TestInvalidSessionNamesFailBeforeIO 验证空白、控制字符和非法结构在创建记录或启动会话前拒绝，不静默改用旧名称。
func TestInvalidSessionNamesFailBeforeIO(t *testing.T) {
	manager, _ := testManager(t)
	manager.config.DataDir = filepath.Join(t.TempDir(), "not-created")
	original := manager.State().Name
	var calls atomic.Int32
	manager.factory = func(Config, string, *journal) (session, error) {
		calls.Add(1)
		t.Error("非法名称不应执行会话工厂")
		return nil, errors.New("非法名称进入了会话工厂")
	}
	handler := testHandler(t, manager)
	for _, body := range []string{
		`{"name":""}`, `{"name":"  \t  "}`, `{"name":"第一行\n第二行"}`, `{"name":"name\u0000"}`,
		`{"name":null}`, `{"name":123}`, `{"name":"甲","name":"乙"}`, `{"name":"甲","extra":"value"}`,
	} {
		response := performRequest(handler, http.MethodPost, "/api/session/start", body)
		if response.Code != http.StatusBadRequest {
			t.Fatalf("非法名称未拒绝：%s %d %s", body, response.Code, response.Body.String())
		}
	}
	if err := manager.Start("\xff"); !errors.Is(err, ErrInvalidName) {
		t.Fatalf("Go调用方非法UTF-8名称未返回明确错误：%v", err)
	}
	if _, err := os.Stat(manager.config.DataDir); !os.IsNotExist(err) {
		t.Fatalf("非法名称创建了目录或记录：%v", err)
	}
	if manager.State().Name != original || manager.State().Phase != "offline" || calls.Load() != 0 {
		t.Fatal("非法输入改变了后台状态")
	}
}
