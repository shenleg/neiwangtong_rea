// 本文件以固定合成向量验证单聊JSON、原生六实体、uint64编号和回执边界；不依赖真实客户端或聊天资料。
package nativeproto

import (
	"bytes"
	"encoding/json"
	"math"
	"reflect"
	"strings"
	"sync"
	"testing"
)

// TestTextContentStaticVector 验证已恢复的普通文本结构、字体字符串和独立GUID内存字节，保留空格、换行、URL及字面实体。
func TestTextContentStaticVector(t *testing.T) {
	const text = "中文🙂 空格\r\n\t<&>\"' &nbsp; &amp; \\n https://example.invalid/a"
	content, err := encodeTextContent(text, bytes.NewReader(make([]byte, 16)))
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(content), &got); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"ver": "6.0", "app": "shiyeline", "id": "00000000000000408000000000000000", "type": "0",
		"ft": map[string]any{"s": "10"},
		"dt": []any{map[string]any{"txt": map[string]any{"t": "normal", "v": text}}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got=%#v want=%#v", got, want)
	}
	if strings.Contains(content, "\\u003c") || strings.Contains(content, "\\u0026") {
		t.Fatalf("HTML escaping replaced native MSG entity stage: %s", content)
	}
}

// TestTextContentBudgetAndRandomFailure 验证预算按UTF-8字节而不是字符计算；坏文本与随机失败不生成替代GUID。
func TestTextContentBudgetAndRandomFailure(t *testing.T) {
	for _, text := range []string{"", "\xff", "bad\x00text", "bad\x01text", strings.Repeat("a", MaxTextBytes+1), strings.Repeat("中", MaxTextBytes/3+1)} {
		if _, err := EncodeTextContent(text); err == nil {
			t.Fatalf("accepted invalid text of %d bytes", len(text))
		}
	}
	if _, err := EncodeTextContent(strings.Repeat("a", MaxTextBytes)); err != nil {
		t.Fatal(err)
	}
	if _, err := encodeTextContent("synthetic", bytes.NewReader(nil)); err == nil {
		t.Fatal("random failure generated fallback GUID")
	}
}

// TestUserMessageStaticXMLVector 验证三个flag输出0/1、线上时间显式为0，以及六实体顺序和一轮还原不损坏字面实体。
func TestUserMessageStaticXMLVector(t *testing.T) {
	const content = `{"v":"a & \" < > ' &nbsp; &amp;"}`
	value := UserMessage{MessageID: 1, Content: content}
	frame, err := EncodeUserMessage(value)
	if err != nil {
		t.Fatal(err)
	}
	const want = `<X_SEND_MSG docver="1"><MSG_ID>1</MSG_ID><RECEIPT>0</RECEIPT><MSG>{&quot;v&quot;:&quot;a&nbsp;&amp;&nbsp;\&quot;&nbsp;&lt;&nbsp;&gt;&nbsp;&apos;&nbsp;&amp;nbsp;&nbsp;&amp;amp;&quot;}</MSG><MSG_TIME>0</MSG_TIME><OFFLINE>0</OFFLINE><HIDE_RECORD>0</HIDE_RECORD></X_SEND_MSG>`
	if frame.Command != CommandUserMessage || frame.Payload != want {
		t.Fatalf("frame=%s\nwant=%s", frame.Payload, want)
	}
	got, err := DecodeUserMessage(frame)
	if err != nil || got != value {
		t.Fatalf("got=%+v err=%v", got, err)
	}
}

// TestUserMessageFrameRoundTrip 验证完整加密帧往返保留uint64上界和原始JSON；不把反斜杠换行或特殊字符改写成其他正文。
func TestUserMessageFrameRoundTrip(t *testing.T) {
	for _, text := range []string{"合成正文", "中文🙂 空格\n第二行\r\n第三行", `<X_SEND_MSG> &nbsp; &amp; &#32; \\n`, strings.Repeat("a", MaxTextBytes)} {
		content, err := EncodeTextContent(text)
		if err != nil {
			t.Fatal(err)
		}
		want := UserMessage{MessageID: math.MaxUint64, Content: content, Time: math.MaxUint64, Receipt: true, Offline: true, HideRecord: true}
		frame, err := EncodeUserMessage(want)
		if err != nil {
			t.Fatal(err)
		}
		wire, err := Encode(frame.Command, frame.Payload)
		if err != nil {
			t.Fatal(err)
		}
		frame, err = Decode(bytes.NewReader(wire), uint32(len(wire)))
		if err != nil {
			t.Fatal(err)
		}
		got, err := DecodeUserMessage(frame)
		if err != nil || got != want {
			t.Fatalf("got=%+v err=%v", got, err)
		}
	}
}

// TestUserMessageRejectsInvalidFields 验证XML/数字边界、未知字段、错误文档和实体不能被当作有效1004。
func TestUserMessageRejectsInvalidFields(t *testing.T) {
	frame, err := EncodeUserMessage(UserMessage{MessageID: 1, Content: `{"v":"synthetic"}`})
	if err != nil {
		t.Fatal(err)
	}
	for _, mutation := range []struct{ old, new string }{
		{"<MSG_ID>1</MSG_ID>", "<MSG_ID>0</MSG_ID>"},
		{"<MSG_ID>1</MSG_ID>", "<MSG_ID>18446744073709551616</MSG_ID>"},
		{"<MSG_ID>1</MSG_ID>", "<MSG_ID>-1</MSG_ID>"},
		{"<MSG_ID>1</MSG_ID>", "<MSG_ID>1</MSG_ID><MSG_ID>2</MSG_ID>"},
		{"<MSG_ID>1</MSG_ID>", ""},
		{"<MSG_TIME>0</MSG_TIME>", "<MSG_TIME>18446744073709551616</MSG_TIME>"},
		{"<RECEIPT>0</RECEIPT>", "<RECEIPT>false</RECEIPT>"},
		{"<OFFLINE>0</OFFLINE>", "<OFFLINE>2</OFFLINE>"},
		{"<HIDE_RECORD>0</HIDE_RECORD>", "<HIDE_RECORD/>"},
		{"<MSG>", `<MSG injected="x">`},
		{"&quot;v&quot;", "&unknown;"},
		{"&quot;v&quot;", "<NESTED/>"},
		{`docver="1"`, `docver="2"`},
		{"</X_SEND_MSG>", "<UNKNOWN/></X_SEND_MSG>"},
		{"</X_SEND_MSG>", "</X_SEND_MSG><SECOND/>"},
	} {
		invalid := frame
		invalid.Payload = strings.Replace(frame.Payload, mutation.old, mutation.new, 1)
		if invalid.Payload == frame.Payload {
			t.Fatal("mutation did not change test input")
		}
		if _, err := DecodeUserMessage(invalid); err == nil {
			t.Fatalf("accepted %s", invalid.Payload)
		}
	}
	for _, value := range []UserMessage{{Content: `{}`}, {MessageID: 1, Content: "plain text"}, {MessageID: 1, Content: "\xff"}} {
		if _, err := EncodeUserMessage(value); err == nil {
			t.Fatalf("accepted %+v", value)
		}
	}
}

// TestMessageAckVectors 验证1005和1006模板准确分开，仅包含uint64 MSG_ID，不把二者当作同一种确认。
func TestMessageAckVectors(t *testing.T) {
	for _, tc := range []struct {
		command uint32
		root    string
	}{{CommandMessageAck, "X_SEND_MSG_ACK"}, {CommandMessageReceipt, "X_SEND_RECEIPT"}} {
		want := MessageAck{MessageID: math.MaxUint64}
		frame, err := EncodeMessageAck(tc.command, want)
		if err != nil {
			t.Fatal(err)
		}
		expected := `<` + tc.root + ` docver="1"><MSG_ID>18446744073709551615</MSG_ID></` + tc.root + `>`
		if frame.Payload != expected {
			t.Fatalf("got=%s want=%s", frame.Payload, expected)
		}
		got, err := DecodeMessageAck(frame)
		if err != nil || got != want {
			t.Fatalf("got=%+v err=%v", got, err)
		}
		for _, payload := range []string{
			strings.Replace(expected, "18446744073709551615", "0", 1),
			strings.Replace(expected, "18446744073709551615", "18446744073709551616", 1),
			strings.Replace(expected, "18446744073709551615", "-1", 1),
			strings.Replace(expected, "<MSG_ID>", "<MSG_ID>2</MSG_ID><MSG_ID>", 1),
			strings.Replace(expected, "<MSG_ID>", `<MSG_ID id="x">`, 1),
			expected + `<SECOND/>`,
		} {
			if _, err := DecodeMessageAck(Frame{Command: tc.command, Payload: payload}); err == nil {
				t.Fatalf("accepted %s", payload)
			}
		}
	}
	if _, err := EncodeMessageAck(CommandUserMessage, MessageAck{MessageID: 1}); err == nil {
		t.Fatal("accepted unsupported ACK command")
	}
}

// TestMessageIDConcurrentUniqueness 验证同一进程的并发消息编号不重复；不假定测试进程已经分配了多少消息。
func TestMessageIDConcurrentUniqueness(t *testing.T) {
	const count = 64
	ids := make(chan uint64, count)
	var workers sync.WaitGroup
	for i := 0; i < count; i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			id, err := NewMessageID()
			if err != nil {
				t.Error(err)
				return
			}
			ids <- id
		}()
	}
	workers.Wait()
	close(ids)
	seen := make(map[uint64]bool)
	for id := range ids {
		if id == 0 || seen[id] {
			t.Fatalf("duplicate or zero id=%d", id)
		}
		seen[id] = true
	}
	if len(seen) != count {
		t.Fatalf("IDs=%d want=%d", len(seen), count)
	}
}

// FuzzUserMessageDecode 验证任意外部XML不会使单聊/回执解码器崩溃；不生成网络操作。
func FuzzUserMessageDecode(f *testing.F) {
	f.Add(uint32(1004), `<X_SEND_MSG docver="1"/>`)
	f.Add(uint32(1005), `<X_SEND_MSG_ACK docver="1"><MSG_ID>1</MSG_ID></X_SEND_MSG_ACK>`)
	f.Add(uint32(1006), `<X_SEND_RECEIPT docver="1"><MSG_ID>1</MSG_ID></X_SEND_RECEIPT>`)
	f.Fuzz(func(t *testing.T, command uint32, payload string) {
		frame := Frame{Command: command, Payload: payload}
		_, _ = DecodeUserMessage(frame)
		_, _ = DecodeMessageAck(frame)
	})
}
