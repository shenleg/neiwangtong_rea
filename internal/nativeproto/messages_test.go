// 本文件用合成身份与资料验证业务 XML 的转义、结构边界和端口字节序；不读取采集文件、不访问客户端或网络。
package nativeproto

import (
	"bytes"
	"reflect"
	"strings"
	"testing"
)

// syntheticHandshake 返回无真实身份的握手资料和九个不同端口，供测试验证顺序、未知属性及字节序；无外部输入或错误。
func syntheticHandshake() Handshake {
	return Handshake{
		Features: []XMLField{{Name: "BUILD", Value: "3055"}, {Name: "FUTURE_FEATURE", Value: "开<&>\"关", Attributes: map[string]string{"mode": "a<&\"b"}}},
		Info: []XMLField{
			{Name: "NAME", Value: "合成<&>\"资料\r\n第二行"},
			{Name: "USER_FACE", Value: "合成头像<&>", Attributes: map[string]string{"id": "123", "future": "未知<&>\"属性"}},
			{Name: "EMPTY", Value: ""},
		},
		Ports: HandshakePorts{
			FileTCP: 9013, FileENet: 0, FolderTCP: 9014, FolderENet: 1,
			FileTCPReverse: 60294, FileENetReverse: 256, FolderTCPReverse: 60295, FolderENetReverse: 65535, LANUpdateHTTP: 60296,
		},
	}
}

// mustHandshakeFrame 编码合成握手并返回帧；编码失败时立即终止当前测试，不访问外部资料。
func mustHandshakeFrame(t *testing.T) Frame {
	t.Helper()
	frame, err := EncodeHandshake(syntheticHandshake())
	if err != nil {
		t.Fatal(err)
	}
	return frame
}

// TestHandshakeRoundTrip 验证字段顺序、未知叶子及属性、文本转义和全部主机序端口经过业务帧后仍保留。
func TestHandshakeRoundTrip(t *testing.T) {
	want := syntheticHandshake()
	frame := mustHandshakeFrame(t)
	if frame.Command != CommandHandshake {
		t.Fatalf("command = %d", frame.Command)
	}
	if strings.Contains(frame.Payload, "合成<&>") || !strings.Contains(frame.Payload, "&lt;&amp;&gt;") {
		t.Fatalf("unescaped payload = %q", frame.Payload)
	}
	for _, field := range []string{
		"<FILE_TRAN_TCP_PORT>13603</FILE_TRAN_TCP_PORT>",
		"<FILE_TRAN_ENET_PORT>0</FILE_TRAN_ENET_PORT>",
		"<FOLDER_TRAN_TCP_PORT>13859</FOLDER_TRAN_TCP_PORT>",
		"<FOLDER_TRAN_ENET_PORT>256</FOLDER_TRAN_ENET_PORT>",
		"<FILE_TRAN_TCP_REVERSE_PORT>34539</FILE_TRAN_TCP_REVERSE_PORT>",
		"<FILE_TRAN_ENET_REVERSE_PORT>1</FILE_TRAN_ENET_REVERSE_PORT>",
		"<FOLDER_TRAN_TCP_REVERSE_PORT>34795</FOLDER_TRAN_TCP_REVERSE_PORT>",
		"<FOLDER_TRAN_ENET_REVERSE_PORT>65535</FOLDER_TRAN_ENET_REVERSE_PORT>",
		"<LAN_UPDATE_HTTP_PORT>35051</LAN_UPDATE_HTTP_PORT>",
	} {
		if !strings.Contains(frame.Payload, field) {
			t.Fatalf("missing %s in %s", field, frame.Payload)
		}
	}
	wire, err := Encode(frame.Command, frame.Payload)
	if err != nil {
		t.Fatal(err)
	}
	frame, err = Decode(bytes.NewReader(wire), uint32(len(wire)))
	if err != nil {
		t.Fatal(err)
	}
	got, err := DecodeHandshake(frame)
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("got = %#v, err = %v; want %#v", got, err, want)
	}
}

// TestHandshakeAllowsMinimalFieldsAndZeroPorts 验证不臆测厂商必需叶子集合，且全部零端口可以往返。
func TestHandshakeAllowsMinimalFieldsAndZeroPorts(t *testing.T) {
	want := Handshake{Features: []XMLField{{Name: "CUSTOM", Value: "0"}}, Info: []XMLField{{Name: "CUSTOM_INFO", Value: ""}}}
	frame, err := EncodeHandshake(want)
	if err != nil {
		t.Fatal(err)
	}
	got, err := DecodeHandshake(frame)
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("got = %#v, err = %v", got, err)
	}
}

// TestEncodeHandshakeRejectsInvalidFields 验证外部字段名、属性和值不能注入 XML，也不会被编码器静默替换成有效字符。
func TestEncodeHandshakeRejectsInvalidFields(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*Handshake)
	}{
		{"empty-feature", func(h *Handshake) { h.Features = nil }},
		{"empty-info", func(h *Handshake) { h.Info = nil }},
		{"duplicate-feature", func(h *Handshake) { h.Features = append(h.Features, h.Features[0]) }},
		{"duplicate-info", func(h *Handshake) { h.Info = append(h.Info, h.Info[0]) }},
		{"empty-name", func(h *Handshake) { h.Info[0].Name = "" }},
		{"name-injection", func(h *Handshake) { h.Info[0].Name = `NAME/><EVIL` }},
		{"name-prefix", func(h *Handshake) { h.Info[0].Name = "1NAME" }},
		{"name-namespace", func(h *Handshake) { h.Info[0].Name = "p:NAME" }},
		{"invalid-name-utf8", func(h *Handshake) { h.Info[0].Name = "\xff" }},
		{"invalid-value-utf8", func(h *Handshake) { h.Info[0].Value = "\xff" }},
		{"nul-value", func(h *Handshake) { h.Info[0].Value = "a\x00b" }},
		{"xml-control-value", func(h *Handshake) { h.Info[0].Value = "a\x01b" }},
		{"xml-noncharacter", func(h *Handshake) { h.Info[0].Value = "￿" }},
		{"attribute-name-injection", func(h *Handshake) { h.Info[0].Attributes = map[string]string{`id="x"`: "x"} }},
		{"attribute-namespace", func(h *Handshake) { h.Info[0].Attributes = map[string]string{"xmlns": "x"} }},
		{"attribute-nul", func(h *Handshake) { h.Info[0].Attributes = map[string]string{"id": "\x00"} }},
		{"attribute-utf8", func(h *Handshake) { h.Info[0].Attributes = map[string]string{"id": "\xff"} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			value := syntheticHandshake()
			tc.edit(&value)
			if _, err := EncodeHandshake(value); err == nil {
				t.Fatal("accepted invalid handshake")
			}
		})
	}
}

// TestDecodeHandshakeRejectsStructure 验证 section、叶子、端口和 XML 文档边界错误明确失败，不忽略未知端口或嵌套。
func TestDecodeHandshakeRejectsStructure(t *testing.T) {
	valid := mustHandshakeFrame(t).Payload
	for _, tc := range []struct{ name, old, replacement string }{
		{"duplicate-section", "</FEATURE>", "</FEATURE><FEATURE><X>1</X></FEATURE>"},
		{"missing-section", "<INFO>", "<OTHER>"},
		{"unknown-section", "</NET>", "</NET><OTHER/>"},
		{"section-attribute", "<INFO>", `<INFO id="x">`},
		{"empty-info", "<INFO>", "<INFO>"},
		{"empty-feature", `<BUILD>3055</BUILD>`, ""},
		{"duplicate-feature", `<BUILD>3055</BUILD>`, `<BUILD>3055</BUILD><BUILD>1</BUILD>`},
		{"duplicate-info", "<NAME>", "<NAME>first</NAME><NAME>"},
		{"nested-info", "<EMPTY></EMPTY>", "<EMPTY><INNER/></EMPTY>"},
		{"mixed-info", "<INFO>", "<INFO>unexpected"},
		{"nested-feature", "<BUILD>3055</BUILD>", "<BUILD><INNER/></BUILD>"},
		{"net-attribute", "<NET>", `<NET x="1">`},
		{"net-text", "<NET>", "<NET>unexpected"},
		{"duplicate-port", "<FILE_TRAN_TCP_PORT>13603</FILE_TRAN_TCP_PORT>", "<FILE_TRAN_TCP_PORT>1</FILE_TRAN_TCP_PORT><FILE_TRAN_TCP_PORT>2</FILE_TRAN_TCP_PORT>"},
		{"missing-port", "<FILE_TRAN_TCP_PORT>13603</FILE_TRAN_TCP_PORT>", ""},
		{"unknown-port", "</NET>", "<UNKNOWN_PORT>0</UNKNOWN_PORT></NET>"},
		{"overflow-port", ">13603<", ">65536<"},
		{"negative-port", ">13603<", ">-1<"},
		{"hex-port", ">13603<", ">0x35<"},
		{"empty-port", ">13603<", "><"},
		{"space-port", ">13603<", "> 13603<"},
		{"attribute-port", "<FILE_TRAN_TCP_PORT>", `<FILE_TRAN_TCP_PORT id="x">`},
		{"nested-port", ">13603<", "><PORT>1</PORT><"},
		{"duplicate-attribute", `id="123"`, `id="123" id="456"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			payload := strings.Replace(valid, tc.old, tc.replacement, 1)
			// 保留 section 本身并移除全部叶子，验证非空约束。
			if tc.name == "empty-feature" || tc.name == "empty-info" {
				section := "FEATURE"
				if tc.name == "empty-info" {
					section = "INFO"
				}
				start := strings.Index(payload, "<"+section+">") + len(section) + 2
				end := strings.Index(payload, "</"+section+">")
				payload = payload[:start] + payload[end:]
			}
			if payload == valid {
				t.Fatal("mutation did not change payload")
			}
			if _, err := DecodeHandshake(Frame{Command: CommandHandshake, Payload: payload}); err == nil {
				t.Fatalf("accepted %s", payload)
			}
		})
	}
}

// TestReadyAndGroupRequestRoundTrip 验证标识按原文保留，包含闭合标签的标识仅是文本，不能追加第二个根节点。
func TestReadyAndGroupRequestRoundTrip(t *testing.T) {
	const id = `synthetic-</USER_ID></X_READY><EVIL/>&"中文`
	ready, err := EncodeReady(Ready{UserID: id})
	if err != nil {
		t.Fatal(err)
	}
	got, err := DecodeReady(ready)
	if err != nil || got != (Ready{UserID: id}) || ready.Command != CommandReady {
		t.Fatalf("ready = %+v, err = %v", got, err)
	}
	request, err := EncodeGroupInfoRequest(id)
	if err != nil {
		t.Fatal(err)
	}
	group, err := DecodeGroupInfoRequest(request)
	if err != nil || group != id || request.Command != CommandGroupInfoRequest {
		t.Fatalf("group = %q, err = %v", group, err)
	}
}

// TestReadyAndRequestRejectInvalidFields 验证必要叶子不得缺失、重复、嵌套或带属性，且非零 PARAM 没有被擅自解释。
func TestReadyAndRequestRejectInvalidFields(t *testing.T) {
	for _, id := range []string{"", "\xff", "a\x00b", "\x01"} {
		if _, err := EncodeReady(Ready{UserID: id}); err == nil {
			t.Fatalf("accepted ready id %q", id)
		}
		if _, err := EncodeGroupInfoRequest(id); err == nil {
			t.Fatalf("accepted group id %q", id)
		}
	}
	if _, err := EncodeReady(Ready{UserID: "synthetic-user", Param: 1}); err == nil {
		t.Fatal("accepted unsupported PARAM")
	}
	for _, body := range []string{
		`<USER_ID>synthetic-user</USER_ID>`,
		`<PARAM>0</PARAM>`,
		`<USER_ID></USER_ID><PARAM>0</PARAM>`,
		`<USER_ID>u</USER_ID><USER_ID>v</USER_ID><PARAM>0</PARAM>`,
		`<USER_ID>u</USER_ID><PARAM>0</PARAM><PARAM>0</PARAM>`,
		`<USER_ID>u</USER_ID><PARAM>1</PARAM>`,
		`<USER_ID>u</USER_ID><PARAM>4294967296</PARAM>`,
		`<USER_ID>u</USER_ID><PARAM>-1</PARAM>`,
		`<USER_ID>u</USER_ID><PARAM></PARAM>`,
		`<USER_ID id="x">u</USER_ID><PARAM>0</PARAM>`,
		`<USER_ID><NESTED/></USER_ID><PARAM>0</PARAM>`,
		`<USER_ID>u</USER_ID><PARAM id="x">0</PARAM>`,
		`<USER_ID>u</USER_ID><PARAM>0</PARAM><UNKNOWN/>`,
		`unexpected<USER_ID>u</USER_ID><PARAM>0</PARAM>`,
	} {
		if _, err := DecodeReady(Frame{Command: CommandReady, Payload: `<X_READY docver="1">` + body + `</X_READY>`}); err == nil {
			t.Fatalf("accepted ready body %s", body)
		}
	}
	for _, body := range []string{"", `<QGROUP_ID/>`, `<QGROUP_ID>g</QGROUP_ID><QGROUP_ID>h</QGROUP_ID>`, `<QGROUP_ID id="1">g</QGROUP_ID>`, `<QGROUP_ID><NESTED/></QGROUP_ID>`, `<OTHER>g</OTHER>`} {
		if _, err := DecodeGroupInfoRequest(Frame{Command: CommandGroupInfoRequest, Payload: `<X_QGROUP_REQ_INFO docver="1">` + body + `</X_QGROUP_REQ_INFO>`}); err == nil {
			t.Fatalf("accepted group body %s", body)
		}
	}
}

// TestControlFrames 验证控制命令仅接受对应的空根节点，保留已验证固定正文而不生成附加业务字段。
func TestControlFrames(t *testing.T) {
	for _, frame := range []Frame{HeartbeatFrame(), QuitFrame()} {
		if err := DecodeControl(frame); err != nil {
			t.Fatal(err)
		}
		name := "X_HEARTBEAT"
		if frame.Command == CommandQuit {
			name = "X_QUIT"
		}
		for _, body := range []string{" \t\r\n", "<!--无业务正文-->"} {
			if err := DecodeControl(Frame{Command: frame.Command, Payload: `<` + name + ` docver="1">` + body + `</` + name + `>`}); err != nil {
				t.Fatal(err)
			}
		}
		for _, body := range []string{"unexpected", "<EXTRA/>", "&#160;"} {
			if err := DecodeControl(Frame{Command: frame.Command, Payload: `<` + name + ` docver="1">` + body + `</` + name + `>`}); err == nil {
				t.Fatalf("accepted control body %q", body)
			}
		}
	}
	if HeartbeatFrame().Payload != `<X_HEARTBEAT docver="1" />` || QuitFrame().Payload != `<X_QUIT docver="1" />` {
		t.Fatal("fixed control payload changed")
	}
	if err := DecodeControl(Frame{Command: CommandGroupInfoResponse, Payload: `<X_QGROUP_REQ_INFO_RSP/>`}); err == nil {
		t.Fatal("accepted unsupported control command")
	}
}

// TestMessageDocumentBoundaries 验证所有业务解码入口拒绝错误命令、版本、重复属性、尾随第二根、坏编码及命令/根不一致。
func TestMessageDocumentBoundaries(t *testing.T) {
	ready, err := EncodeReady(Ready{UserID: "synthetic-user"})
	if err != nil {
		t.Fatal(err)
	}
	request, err := EncodeGroupInfoRequest("synthetic-group")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name   string
		frame  Frame
		decode func(Frame) error
	}{
		{"handshake", mustHandshakeFrame(t), func(f Frame) error { _, err := DecodeHandshake(f); return err }},
		{"ready", ready, func(f Frame) error { _, err := DecodeReady(f); return err }},
		{"request", request, func(f Frame) error { _, err := DecodeGroupInfoRequest(f); return err }},
		{"heartbeat", HeartbeatFrame(), DecodeControl},
		{"quit", QuitFrame(), DecodeControl},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, payload := range []string{
				"", "\xff", tc.frame.Payload + "\x00", tc.frame.Payload + `<SECOND/>`, tc.frame.Payload + "garbage", tc.frame.Payload + "<!--tail-->",
				strings.Replace(tc.frame.Payload, `docver="1"`, `docver="2"`, 1),
				strings.Replace(tc.frame.Payload, `docver="1"`, `docver="1" docver="1"`, 1),
				strings.Replace(tc.frame.Payload, `docver="1"`, `other="1"`, 1),
				strings.Replace(tc.frame.Payload, `docver="1"`, `docver="1" other="1"`, 1),
				strings.Replace(tc.frame.Payload, "X_", "OTHER_", 1),
				strings.Replace(tc.frame.Payload, `docver="1"`, `xmlns="urn:unsupported" docver="1"`, 1),
				" " + tc.frame.Payload,
			} {
				if err := tc.decode(Frame{Command: tc.frame.Command, Payload: payload}); err == nil {
					t.Fatalf("accepted %q", payload)
				}
			}
			wrongCommand := tc.frame
			wrongCommand.Command = CommandGroupInfoResponse
			if err := tc.decode(wrongCommand); err == nil {
				t.Fatal("accepted wrong command")
			}
			if err := tc.decode(Frame{Command: tc.frame.Command, Payload: " \t\n" + tc.frame.Payload + "\r\n"}); err != nil {
				t.Fatalf("rejected XML whitespace: %v", err)
			}
		})
	}
}
