// 本文件验证stdin严格JSON、串行发送与发送前落盘；使用可注入发送函数及合成记录，不访问网络。
package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"neiwangtong-gateway/internal/nativeproto"
)

// inputFixture 提供当前合成身份的目录、端点及私有JSONL记录；文件由测试清理时关闭。
func inputFixture(t *testing.T, output io.Writer) (*nativeproto.Directory, netip.AddrPort, *eventLog) {
	t.Helper()
	remote := netip.MustParseAddrPort("127.0.0.2:9011")
	directory, err := nativeproto.NewDirectory("11111111111111111111111111111111", []nativeproto.Contact{
		{UserID: "synthetic-peer", Name: "Synthetic", Discovery: remote},
		{UserID: "other-peer", Name: "Other", Discovery: netip.MustParseAddrPort("127.0.0.3:9011")},
	})
	if err != nil {
		t.Fatal(err)
	}
	file, err := os.OpenFile(filepath.Join(t.TempDir(), "record.jsonl"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := file.Close(); err != nil && !errors.Is(err, os.ErrClosed) {
			t.Error(err)
		}
	})
	return directory, remote, &eventLog{file: file, record: json.NewEncoder(file), output: json.NewEncoder(output)}
}

// TestStrictRequestJSON 验证单聊/群聊目标互斥，拒绝缺键、未知键、重复键、尾随对象、空值及UTF-8错误。
func TestStrictRequestJSON(t *testing.T) {
	for _, line := range []string{
		`null`, `[]`, `{"user_id":"synthetic-peer"}`, `{"user_id":"synthetic-peer","text":null}`, `{"user_id":1,"text":"hi"}`,
		`{"user_id":"synthetic-peer","text":"hi","flags":1}`, `{"user_id":"synthetic-peer","text":"first","text":"second"}`,
		`{"User_ID":"synthetic-peer","text":"hi"}`, `{"user_id":"synthetic-peer","text":"hi"} {}`,
		`{"user_id":"synthetic-peer","text":""}`, `{"user_id":"synthetic-peer","text":"\u0000"}`, "{\"user_id\":\"synthetic-peer\",\"text\":\"\xff\"}",
		`{"group_id":"synthetic-group"}`, `{"text":"hi"}`, `{"group_id":"","text":"hi"}`, `{"group_id":null,"text":"hi"}`,
		`{"group_id":1,"text":"hi"}`, `{"group_id":"synthetic-group","text":null}`, `{"group_id":"synthetic-group","text":""}`,
		`{"group_id":"synthetic-group","text":"hi","unknown":"value"}`, `{"group_id":"synthetic-group","text":"hi","group_id":"other"}`,
		`{"user_id":"synthetic-peer","text":"hi","user_id":"other"}`, `{"group_id":"synthetic-group","text":"first","text":"second"}`,
		`{"group_id":"synthetic-group","user_id":"synthetic-peer","text":"hi"}`,
		`{"group_id":"synthetic-group","user_id":"","text":"hi"}`, `{"group_id":"","user_id":"synthetic-peer","text":"hi"}`,
		`{"group_id":"synthetic-group","text":"hi"} {}`, `{"group_id":"synthetic-group","text":"\u0000"}`,
	} {
		if _, err := decodeRequest([]byte(line)); err == nil {
			t.Errorf("accepted %q", line)
		}
	}
	for _, target := range []struct{ key, id string }{{"user_id", "synthetic-peer"}, {"group_id", "原始同步群ID &<>"}} {
		line := fmt.Sprintf(`{"text":"中文\nline\r\n😀",%q:%q}`, target.key, target.id)
		request, err := decodeRequest([]byte(line))
		if err != nil || request.Text != "中文\nline\r\n😀" {
			t.Fatalf("request=%+v err=%v", request, err)
		}
		if target.key == "user_id" && (request.UserID != target.id || request.GroupID != "") || target.key == "group_id" && (request.GroupID != target.id || request.UserID != "") {
			t.Fatalf("target changed: %+v", request)
		}
	}
}

// TestInvalidGroupInputDoesNotLeakText 验证群请求错误不把正文、未知键或JSON解析原文写入记录及stdout。
func TestInvalidGroupInputDoesNotLeakText(t *testing.T) {
	const secret = "private body must never appear"
	for _, line := range []string{
		`{"group_id":"group","text":"` + secret + `","private body must never appear":true}`,
		`{"group_id":"group","text":"` + secret + `","text":"duplicate"}`,
		`{"group_id":"group","user_id":"synthetic-peer","text":"` + secret + `"}`,
		`{"text":"` + secret + `"}`,
		`{"group_id":"group","text":"` + secret + `" invalid}`,
		`{"group_id":"group","text":{"private body must never appear":true}}`,
		`{"group_id":"group","text":"` + secret + `\u0000"}`,
	} {
		var output bytes.Buffer
		directory, remote, log := inputFixture(t, &output)
		send := func(context.Context, string, string) (nativeproto.ChatResult, error) {
			t.Fatal("invalid group input sent")
			return nativeproto.ChatResult{}, nil
		}
		if err := consumeRequests(context.Background(), strings.NewReader(line), directory, remote, send, send, log); err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(log.file.Name())
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(data, []byte(secret)) || strings.Contains(output.String(), secret) || bytes.Contains(data, []byte(`"event":"chat_request"`)) {
			t.Fatalf("invalid input leaked or recorded send intent: output=%s record=%s", &output, data)
		}
		var result requestResult
		if err := json.Unmarshal(output.Bytes(), &result); err != nil || result.Error == "" {
			t.Fatalf("result=%+v err=%v", result, err)
		}
	}
}

// TestInputSequentialAndRecordedBeforeSend 验证坏行不发送、多行顺序不变、每次调用前已有同步摘要且不保存正文。
func TestInputSequentialAndRecordedBeforeSend(t *testing.T) {
	var output bytes.Buffer
	directory, remote, log := inputFixture(t, &output)
	var sent []string
	send := func(ctx context.Context, userID, text string) (nativeproto.ChatResult, error) {
		data, err := os.ReadFile(log.file.Name())
		if err != nil {
			return nativeproto.ChatResult{}, err
		}
		if bytes.Contains(data, []byte(text)) || !bytes.Contains(data, []byte(`"message_sha256"`)) {
			return nativeproto.ChatResult{}, fmt.Errorf("missing digest or plaintext leaked")
		}
		sent = append(sent, text)
		return nativeproto.ChatResult{Submitted: true, MessageID: fmt.Sprint(len(sent)), Delivery: "unknown"}, nil
	}
	input := `{"user_id":"synthetic-peer","text":"private first text"}` + "\n" + `{"user_id":"missing","text":"no target"}` + "\n" + `{"user_id":"other-peer","text":"wrong endpoint"}` + "\n" + `{"user_id":"synthetic-peer","text":"private second text"}` + "\n" + `{"raw_xml":"private forbidden"}` + "\n"
	if err := consumeRequests(context.Background(), strings.NewReader(input), directory, remote, send, send, log); err != nil {
		t.Fatal(err)
	}
	if strings.Join(sent, ",") != "private first text,private second text" {
		t.Fatalf("sent=%v", sent)
	}
	decoder := json.NewDecoder(&output)
	for sequence := uint64(1); sequence <= 5; sequence++ {
		var result requestResult
		if err := decoder.Decode(&result); err != nil {
			t.Fatal(err)
		}
		if result.Sequence != sequence || result.Event != "request_result" {
			t.Fatalf("result=%+v", result)
		}
		if result.Error == "" != (sequence == 1 || sequence == 4) {
			t.Fatalf("sequence=%d result=%+v", sequence, result)
		}
	}
	data, err := os.ReadFile(log.file.Name())
	if err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{"private first text", "private second text", "private forbidden", "no target", "wrong endpoint"} {
		if bytes.Contains(data, []byte(text)) {
			t.Fatalf("record leaked %q", text)
		}
	}
}

// TestGroupInputSequentialAndRecordedBeforeSend 验证群/单聊依输入顺序分派，发送前摘要已落盘且前条结果已记录。
func TestGroupInputSequentialAndRecordedBeforeSend(t *testing.T) {
	var output bytes.Buffer
	directory, remote, log := inputFixture(t, &output)
	targets := []string{"原始同步群ID &<>", "synthetic-peer", "second-group"}
	texts := []string{"private group first", "private user second", "private group third"}
	sequences := []uint64{1, 3, 4}
	commands := []uint32{nativeproto.CommandGroupMessage, nativeproto.CommandUserMessage, nativeproto.CommandGroupMessage}
	calls := 0
	send := func(command uint32, key string) func(context.Context, string, string) (nativeproto.ChatResult, error) {
		return func(_ context.Context, targetID, text string) (nativeproto.ChatResult, error) {
			if calls >= len(targets) || targetID != targets[calls] || text != texts[calls] || command != commands[calls] {
				t.Fatalf("call=%d command=%d target=%q text=%q", calls, command, targetID, text)
			}
			data, err := os.ReadFile(log.file.Name())
			if err != nil {
				t.Fatal(err)
			}
			decoder := json.NewDecoder(bytes.NewReader(data))
			var last map[string]any
			var results uint64
			for decoder.More() {
				var value map[string]any
				if err := decoder.Decode(&value); err != nil {
					t.Fatal(err)
				}
				last = value
				if last["event"] == "request_result" {
					results++
				}
			}
			if last["event"] != "chat_request" || last["sequence"] != float64(sequences[calls]) || results != sequences[calls]-1 || last[key] != fmt.Sprintf("%x", sha256.Sum256([]byte(targetID))) || last["message_sha256"] != fmt.Sprintf("%x", sha256.Sum256([]byte(text))) || last["message_bytes"] != float64(len(text)) {
				t.Fatalf("summary=%v preceding results=%d", last, results)
			}
			otherKey := "target_user_id_sha256"
			if key == otherKey {
				otherKey = "target_group_id_sha256"
			}
			if _, exists := last[otherKey]; exists {
				t.Fatalf("summary contains both targets: %v", last)
			}
			calls++
			return nativeproto.ChatResult{Command: command, Submitted: true, Delivery: "unknown"}, nil
		}
	}
	input := `{"group_id":"原始同步群ID &<>","text":"private group first"}` + "\n" +
		`{"group_id":"bad-group","user_id":"synthetic-peer","text":"private invalid text"}` + "\n" +
		`{"user_id":"synthetic-peer","text":"private user second"}` + "\n" +
		`{"group_id":"second-group","text":"private group third"}` + "\n"
	if err := consumeRequests(context.Background(), strings.NewReader(input), directory, remote, send(nativeproto.CommandUserMessage, "target_user_id_sha256"), send(nativeproto.CommandGroupMessage, "target_group_id_sha256"), log); err != nil {
		t.Fatal(err)
	}
	if calls != 3 {
		t.Fatalf("calls=%d", calls)
	}
	stdout := output.String()
	decoder := json.NewDecoder(&output)
	for sequence, command := range []uint32{nativeproto.CommandGroupMessage, nativeproto.CommandGroupMessage, nativeproto.CommandUserMessage, nativeproto.CommandGroupMessage} {
		var result requestResult
		if err := decoder.Decode(&result); err != nil {
			t.Fatal(err)
		}
		if result.Sequence != uint64(sequence+1) || result.Result.Command != command || (result.Error != "") != (sequence == 1) {
			t.Fatalf("result=%+v", result)
		}
	}
	data, err := os.ReadFile(log.file.Name())
	if err != nil {
		t.Fatal(err)
	}
	for _, text := range append(texts, "private invalid text", targets[0], targets[2]) {
		if bytes.Contains(data, []byte(text)) || strings.Contains(stdout, text) {
			t.Fatalf("plaintext leaked: %q", text)
		}
	}
}

// TestGroupInputWithoutContacts 验证群请求无需-contacts，仅群发送函数会被调用。
func TestGroupInputWithoutContacts(t *testing.T) {
	var output bytes.Buffer
	_, remote, log := inputFixture(t, &output)
	calls := 0
	sendUser := func(context.Context, string, string) (nativeproto.ChatResult, error) {
		t.Fatal("group input called SendUser")
		return nativeproto.ChatResult{}, nil
	}
	sendGroup := func(_ context.Context, groupID, text string) (nativeproto.ChatResult, error) {
		calls++
		if groupID != "原始同步群ID" || text != "private group body" {
			t.Fatalf("group=%q text=%q", groupID, text)
		}
		return nativeproto.ChatResult{Command: nativeproto.CommandGroupMessage, Submitted: true, Delivery: "unknown"}, nil
	}
	err := consumeRequests(context.Background(), strings.NewReader(`{"group_id":"原始同步群ID","text":"private group body"}`), nil, remote, sendUser, sendGroup, log)
	if err != nil || calls != 1 {
		t.Fatalf("err=%v calls=%d output=%s", err, calls, &output)
	}
	var result requestResult
	if err := json.Unmarshal(output.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Error != "" || result.Result.Command != nativeproto.CommandGroupMessage || !result.Result.Submitted {
		t.Fatalf("result=%+v", result)
	}
}

// TestMissingContactsRejectsInput 验证只上线时可不配目录，但stdin单聊必须明确拒绝且不调用发送。
func TestMissingContactsRejectsInput(t *testing.T) {
	var output bytes.Buffer
	_, remote, log := inputFixture(t, &output)
	calls := 0
	send := func(context.Context, string, string) (nativeproto.ChatResult, error) {
		calls++
		return nativeproto.ChatResult{}, nil
	}
	err := consumeRequests(context.Background(), strings.NewReader(`{"user_id":"synthetic-peer","text":"synthetic"}`), nil, remote, send, send, log)
	if err != nil || calls != 0 || !strings.Contains(output.String(), "发送需要-contacts") {
		t.Fatalf("err=%v calls=%d output=%s", err, calls, &output)
	}
}

// TestSendFailureIsNotRetried 验证提交后的传输错误只产生一次调用，下一输入仍为新请求。
func TestSendFailureIsNotRetried(t *testing.T) {
	var output bytes.Buffer
	directory, remote, log := inputFixture(t, &output)
	calls := 0
	send := func(context.Context, string, string) (nativeproto.ChatResult, error) {
		calls++
		return nativeproto.ChatResult{Submitted: true, MessageID: fmt.Sprint(calls), Delivery: "unknown"}, fmt.Errorf("synthetic transport failure")
	}
	line := `{"user_id":"synthetic-peer","text":"synthetic message"}` + "\n"
	if err := consumeRequests(context.Background(), strings.NewReader(line+line), directory, remote, send, send, log); err != nil {
		t.Fatal(err)
	}
	if calls != 2 || strings.Count(output.String(), "synthetic transport failure") != 2 {
		t.Fatalf("calls=%d output=%s", calls, &output)
	}
}

// TestRecordFailurePreventsSend 验证单聊/群聊在记录写入或Sync失败后均不调用发送；编码成功不能替代Sync。
func TestRecordFailurePreventsSend(t *testing.T) {
	for _, target := range []string{"user_id", "group_id"} {
		for _, stage := range []string{"write", "sync"} {
			t.Run(target+"/"+stage, func(t *testing.T) {
				directory, remote, log := inputFixture(t, io.Discard)
				if err := log.file.Close(); err != nil {
					t.Fatal(err)
				}
				var encoded bytes.Buffer
				if stage == "sync" {
					// 编码写入内存成功，随后对已关闭文件Sync必须失败。
					log.record = json.NewEncoder(&encoded)
				}
				calls := 0
				send := func(context.Context, string, string) (nativeproto.ChatResult, error) {
					calls++
					return nativeproto.ChatResult{}, nil
				}
				line := fmt.Sprintf(`{%q:"synthetic-peer","text":"private failed record body"}`, target)
				err := consumeRequests(context.Background(), strings.NewReader(line), directory, remote, send, send, log)
				if err == nil || calls != 0 {
					t.Fatalf("err=%v calls=%d", err, calls)
				}
				if stage == "sync" && !strings.Contains(err.Error(), "同步私有记录") {
					t.Fatalf("did not reach Sync: %v", err)
				}
			})
		}
	}
}

// TestInputEOFAndBudget 验证EOF返回但不取消上下文，超长行不发送，最大转义正文仍可解析。
func TestInputEOFAndBudget(t *testing.T) {
	var output bytes.Buffer
	directory, remote, log := inputFixture(t, &output)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	calls := 0
	send := func(context.Context, string, string) (nativeproto.ChatResult, error) {
		calls++
		return nativeproto.ChatResult{}, nil
	}
	if err := consumeRequests(ctx, strings.NewReader(""), directory, remote, send, send, log); err != nil || ctx.Err() != nil {
		t.Fatalf("EOF err=%v ctx=%v", err, ctx.Err())
	}
	if err := consumeRequests(ctx, strings.NewReader(strings.Repeat("x", maxInputLineBytes+1)), directory, remote, send, send, log); err != nil {
		t.Fatal(err)
	}
	if calls != 0 || !strings.Contains(output.String(), "停止接受stdin") || ctx.Err() != nil {
		t.Fatalf("calls=%d output=%s", calls, &output)
	}
	output.Reset()
	line := `{"user_id":"synthetic-peer","text":"` + strings.Repeat(`a`, nativeproto.MaxTextBytes) + `"}` + "\n"
	if err := consumeRequests(ctx, strings.NewReader(line), directory, remote, send, send, log); err != nil || calls != 1 {
		t.Fatalf("err=%v calls=%d", err, calls)
	}
}

// brokenReader 提供明确的外部读取故障，用于验证I/O异常不会被当成EOF吞掉。
type brokenReader struct{}

// Read 不读取缓冲区，始终返回合成输入故障。
func (brokenReader) Read([]byte) (int, error) { return 0, fmt.Errorf("synthetic input I/O failure") }

// TestInputIOErrors 验证读取或stdout写入错误显式返回。
func TestInputIOErrors(t *testing.T) {
	directory, remote, log := inputFixture(t, io.Discard)
	send := func(context.Context, string, string) (nativeproto.ChatResult, error) {
		return nativeproto.ChatResult{}, nil
	}
	if err := consumeRequests(context.Background(), brokenReader{}, directory, remote, send, send, log); err == nil {
		t.Fatal("ignored read error")
	}
	_, _, failedLog := inputFixture(t, brokenWriter{})
	if err := consumeRequests(context.Background(), strings.NewReader("bad JSON\n"), directory, remote, send, send, failedLog); err == nil {
		t.Fatal("ignored stdout error")
	}
}

// brokenWriter 提供明确的stdout写入故障，Write始终返回测试错误。
type brokenWriter struct{}

// Write 不保存数据，始终返回合成输出故障。
func (brokenWriter) Write([]byte) (int, error) { return 0, fmt.Errorf("synthetic output I/O failure") }

// TestEventLogConcurrentLines 验证异步事件和请求结果并行输出时每行仍为完整JSON。
func TestEventLogConcurrentLines(t *testing.T) {
	var output bytes.Buffer
	_, _, log := inputFixture(t, &output)
	var workers sync.WaitGroup
	for worker := 0; worker < 4; worker++ {
		workers.Add(1)
		go func(id int) {
			defer workers.Done()
			for index := 0; index < 10; index++ {
				if err := log.write(map[string]int{"worker": id, "index": index}, true); err != nil {
					t.Error(err)
					return
				}
			}
		}(worker)
	}
	workers.Wait()
	decoder := json.NewDecoder(&output)
	for index := 0; index < 40; index++ {
		var value map[string]int
		if err := decoder.Decode(&value); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := decoder.Token(); err != io.EOF {
		t.Fatalf("extra output: %v", err)
	}
}
