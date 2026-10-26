// 本文件以静态模板合成群同步帧，验证成员字段与厂商错闭合的精确兼容；不读取真实资料。
package nativeproto

import (
	"fmt"
	"strings"
	"testing"
)

// syntheticGroupInfo 返回脱敏合成INFO模板，保留样本QGROUP_INFO_VER错闭合；无I/O或错误。
func syntheticGroupInfo() string {
	return `<INFO><QGROUP_ID>synthetic-group</QGROUP_ID><QGROUP_NAME>Synthetic 群</QGROUP_NAME><QGROUP_MASTER>synthetic-peer</QGROUP_MASTER><QGROUP_INTR></QGROUP_INTR><QGROUP_ANN></QGROUP_ANN><QGROUP_INFO_VER>9</GROUP_INFO_VER><QGROUP_USER_VER>10</QGROUP_USER_VER><QGROUP_ALLOW_OTHER_ADD>0</QGROUP_ALLOW_OTHER_ADD><QGROUP_PHOTO_MD5></QGROUP_PHOTO_MD5></INFO>`
}

// syntheticGroupPush 返回3002或3003合成帧，members仅用于构造3003；不进行成员推断，不产生I/O错误。
func syntheticGroupPush(command uint32, members ...string) Frame {
	root := "X_QGROUP_PUSH_INFO"
	body := syntheticGroupInfo()
	if command == CommandGroupUserPush {
		root = "X_QGROUP_PUSH_USER"
		body += "<USER>"
		for _, member := range members {
			body += fmt.Sprintf(`<ID name="Synthetic">%s</ID>`, member)
		}
		body += "</USER>"
	}
	return Frame{Command: command, Payload: fmt.Sprintf(`<%s docver="1">%s</%s>`, root, body, root)}
}

// TestGroupPushContract 验证3002不假造成员，3003按ID文本读取完整成员，局部兼容不改原始帧。
func TestGroupPushContract(t *testing.T) {
	for _, command := range []uint32{3002, 3003} {
		frame := syntheticGroupPush(command, "synthetic-local", "synthetic-peer")
		before := frame
		group, err := DecodeGroupPush(frame)
		if err != nil {
			t.Fatal(err)
		}
		if group.GroupID != "synthetic-group" || group.Name != "Synthetic 群" || group.MasterID != "synthetic-peer" || group.InfoVersion != 9 || group.UserVersion != 10 {
			t.Fatalf("group=%+v", group)
		}
		if (command == 3002 && group.Members != nil) || (command == 3003 && strings.Join(group.Members, ",") != "synthetic-local,synthetic-peer") {
			t.Fatalf("members=%v", group.Members)
		}
		if frame != before || !strings.Contains(frame.Payload, "</GROUP_INFO_VER>") {
			t.Fatal("改写原始帧")
		}
		corrected := frame
		corrected.Payload = strings.ReplaceAll(corrected.Payload, "</GROUP_INFO_VER>", "</QGROUP_INFO_VER>")
		if _, err := DecodeGroupPush(corrected); err != nil {
			t.Fatal(err)
		}
	}
}

// TestGroupPushRejectsMalformed 验证除已证明的版本标签外，其他错闭合、成员属性、重复ID与未知字段仍严格失败。
func TestGroupPushRejectsMalformed(t *testing.T) {
	frame := syntheticGroupPush(3003, "synthetic-local", "synthetic-peer")
	for _, body := range []string{
		strings.Replace(frame.Payload, "</QGROUP_NAME>", "</GROUP_NAME>", 1),
		strings.Replace(frame.Payload, "<QGROUP_USER_VER>10", "<QGROUP_USER_VER>-1", 1),
		strings.Replace(frame.Payload, `name="Synthetic"`, `other="Synthetic"`, 1),
		strings.Replace(frame.Payload, "synthetic-local</ID>", "synthetic-peer</ID>", 1),
		strings.Replace(frame.Payload, "</USER>", "<OTHER/></USER>", 1),
		strings.Replace(frame.Payload, "</INFO>", "<UNKNOWN/></INFO>", 1),
	} {
		if _, err := DecodeGroupPush(Frame{Command: 3003, Payload: body}); err == nil {
			t.Fatalf("接受非法群同步: %s", body)
		}
	}
}

// syntheticGroupUserResponse 根据RET资料位构造3007，只用合成成员；不声明所有返回值都是成功。
func syntheticGroupUserResponse(result uint32, members ...string) Frame {
	body := fmt.Sprintf("<RET>%d</RET><QGROUP_ID>synthetic-group</QGROUP_ID>", result)
	if result&0x4 != 0 {
		body += syntheticGroupInfo() + "<USER>"
		for _, member := range members {
			body += fmt.Sprintf(`<ID name="Synthetic">%s</ID>`, member)
		}
		body += "</USER>"
	}
	return Frame{Command: CommandGroupUserResponse, Payload: `<X_QGROUP_REQ_USER_RSP docver="1">` + body + `</X_QGROUP_REQ_USER_RSP>`}
}

// TestGroupUserResponseContract 验证RET的资料位与成员位独立，20/12均带成员，但10/0不带。
func TestGroupUserResponseContract(t *testing.T) {
	for _, result := range []uint32{0, 10, 12, 20} {
		response, err := DecodeGroupUserResponse(syntheticGroupUserResponse(result, "synthetic-local", "synthetic-peer"))
		if err != nil || response.Result != result || response.GroupID != "synthetic-group" || (response.Group != nil) != (result&0x4 != 0) {
			t.Fatalf("result=%d response=%+v err=%v", result, response, err)
		}
	}
	frame := syntheticGroupUserResponse(20, "synthetic-local", "synthetic-peer")
	frame.Payload = strings.Replace(frame.Payload, "<QGROUP_ID>synthetic-group</QGROUP_ID>", "<QGROUP_ID>other</QGROUP_ID>", 1)
	if _, err := DecodeGroupUserResponse(frame); err == nil {
		t.Fatal("接受外层与INFO不同群")
	}
	frame = syntheticGroupUserResponse(10)
	frame.Payload = strings.Replace(frame.Payload, "</X_QGROUP_REQ_USER_RSP>", syntheticGroupInfo()+"</X_QGROUP_REQ_USER_RSP>", 1)
	if _, err := DecodeGroupUserResponse(frame); err == nil {
		t.Fatal("RET无资料却接受INFO")
	}
}

// FuzzGroupSync 验证群同步XML的局部兼容与严格解析面对外部字节不会panic。
func FuzzGroupSync(f *testing.F) {
	f.Add(uint8(0), syntheticGroupPush(3002).Payload)
	f.Add(uint8(1), syntheticGroupPush(3003, "synthetic-local", "synthetic-peer").Payload)
	f.Add(uint8(2), syntheticGroupUserResponse(20, "synthetic-local", "synthetic-peer").Payload)
	f.Fuzz(func(t *testing.T, kind uint8, payload string) {
		commands := [...]uint32{3002, 3003, 3007}
		frame := Frame{Command: commands[kind%3], Payload: payload}
		if frame.Command == CommandGroupUserResponse {
			_, _ = DecodeGroupUserResponse(frame)
		} else {
			_, _ = DecodeGroupPush(frame)
		}
	})
}

// TestGroupVersionNormalizationScope 验证已知错闭合只修正真实版本元素，群名中的相同CDATA原文不被改写。
func TestGroupVersionNormalizationScope(t *testing.T) {
	frame := syntheticGroupPush(3003, "synthetic-local", "synthetic-peer")
	const literal = `<QGROUP_INFO_VER>123</GROUP_INFO_VER>`
	frame.Payload = strings.Replace(frame.Payload, "Synthetic 群", "<![CDATA["+literal+"]]>", 1)
	group, err := DecodeGroupPush(frame)
	if err != nil || group.Name != literal {
		t.Fatalf("group=%+v err=%v", group, err)
	}
	frame.Payload = strings.Replace(frame.Payload, "<QGROUP_ALLOW_OTHER_ADD>0", "<QGROUP_ALLOW_OTHER_ADD>18446744073709551615", 1)
	if _, err := DecodeGroupPush(frame); err != nil {
		t.Fatal(err)
	}
}

// TestGroupUserRequestContract 验证3006精确模板与原始群ID选择，不能将其他群命令当成员请求。
func TestGroupUserRequestContract(t *testing.T) {
	frame, err := EncodeGroupUserRequest("synthetic-group")
	if err != nil {
		t.Fatal(err)
	}
	const want = `<X_QGROUP_REQ_USER docver="1"><QGROUP_ID>synthetic-group</QGROUP_ID></X_QGROUP_REQ_USER>`
	if frame.Command != 3006 || frame.Payload != want {
		t.Fatalf("frame=%+v", frame)
	}
	if id, err := DecodeGroupUserRequest(frame); err != nil || id != "synthetic-group" {
		t.Fatalf("id=%q err=%v", id, err)
	}
	if _, err := EncodeGroupUserRequest(""); err == nil {
		t.Fatal("接受空群ID")
	}
	frame.Command = 3004
	if _, err := DecodeGroupUserRequest(frame); err == nil {
		t.Fatal("将3004当成员请求")
	}
}
