// 本文件验证3011字段、正文实体保真与错误拒绝；只使用合成群和内容，不访问真实客户端。
package nativeproto

import (
	"strings"
	"testing"
)

// TestGroupMessageRoundTrip 验证群消息复用纯文本内容格式，原样保留换行和字面实体。
func TestGroupMessageRoundTrip(t *testing.T) {
	content, err := EncodeTextContent("合成群聊 <>&\"' &nbsp; 🙂\n第二行\r\n第三行")
	if err != nil {
		t.Fatal(err)
	}
	for _, hidden := range []bool{false, true} {
		want := GroupMessage{GroupID: "synthetic<&group", MessageID: 18446744073709551615, Content: content, HideRecord: hidden}
		frame, err := EncodeGroupMessage(want)
		if err != nil {
			t.Fatal(err)
		}
		if frame.Command != 3011 || !strings.Contains(frame.Payload, "&nbsp;") || !strings.Contains(frame.Payload, "&amp;nbsp;") {
			t.Fatalf("群消息字段/实体错误: %s", frame.Payload)
		}
		got, err := DecodeGroupMessage(frame)
		if err != nil || got != want {
			t.Fatalf("roundtrip = %#v, %v", got, err)
		}
		wire, err := Encode(frame.Command, frame.Payload)
		if err != nil {
			t.Fatal(err)
		}
		decoded, err := Decode(strings.NewReader(string(wire)), 1<<20)
		if err != nil || decoded != frame {
			t.Fatalf("frame roundtrip = %#v, %v", decoded, err)
		}
	}
}

// TestGroupMessageRejectsInvalidFields 验证坏群ID、原始正文、零编号与错误外层结构不能作为3011提交。
func TestGroupMessageRejectsInvalidFields(t *testing.T) {
	for _, value := range []GroupMessage{
		{GroupID: "", MessageID: 1, Content: `{}`},
		{GroupID: "g\x00", MessageID: 1, Content: `{}`},
		{GroupID: "g", Content: `{}`},
		{GroupID: "g", MessageID: 1, Content: "原始文本"},
	} {
		if _, err := EncodeGroupMessage(value); err == nil {
			t.Fatalf("接受非法字段: %#v", value)
		}
	}
	frame, err := EncodeGroupMessage(GroupMessage{GroupID: "synthetic-group", MessageID: 1, Content: `{}`})
	if err != nil {
		t.Fatal(err)
	}
	const expected = `<X_QGROUP_SEND_MSG docver="1"><QGROUP_ID>synthetic-group</QGROUP_ID><MSG_ID>1</MSG_ID><MSG>{}</MSG><HIDE_RECORD>0</HIDE_RECORD></X_QGROUP_SEND_MSG>`
	if frame.Command != 3011 || frame.Payload != expected {
		t.Fatalf("3011与静态模板不同: %+v", frame)
	}
	for _, payload := range []string{
		strings.Replace(frame.Payload, "<MSG_ID>1</MSG_ID>", "<MSG_ID>0</MSG_ID>", 1),
		strings.Replace(frame.Payload, "<HIDE_RECORD>0</HIDE_RECORD>", "<HIDE_RECORD>false</HIDE_RECORD>", 1),
		strings.Replace(frame.Payload, "<MSG>{}</MSG>", "<MSG>raw</MSG>", 1),
		strings.Replace(frame.Payload, "</X_QGROUP_SEND_MSG>", "<MSG_ID>2</MSG_ID></X_QGROUP_SEND_MSG>", 1),
		strings.Replace(frame.Payload, `docver="1"`, `docver="2"`, 1),
	} {
		if _, err := DecodeGroupMessage(Frame{Command: 3011, Payload: payload}); err == nil {
			t.Fatalf("接受坏XML: %s", payload)
		}
	}
	if _, err := DecodeGroupMessage(Frame{Command: 1004, Payload: frame.Payload}); err == nil {
		t.Fatal("接受单聊命令")
	}
}

// FuzzGroupMessage 检查外部3011 XML解析无panic，成功解析后再编码不改变群、正文或消息标识。
func FuzzGroupMessage(f *testing.F) {
	f.Add(`<X_QGROUP_SEND_MSG docver="1"><QGROUP_ID>synthetic-group</QGROUP_ID><MSG_ID>1</MSG_ID><MSG>{}</MSG><HIDE_RECORD>0</HIDE_RECORD></X_QGROUP_SEND_MSG>`)
	f.Add("")
	f.Fuzz(func(t *testing.T, payload string) {
		message, err := DecodeGroupMessage(Frame{Command: CommandGroupMessage, Payload: payload})
		if err != nil {
			return
		}
		frame, err := EncodeGroupMessage(message)
		if err != nil {
			t.Fatal(err)
		}
		got, err := DecodeGroupMessage(frame)
		if err != nil || got != message {
			t.Fatalf("group roundtrip changed fields: %+v, %v", got, err)
		}
	})
}
