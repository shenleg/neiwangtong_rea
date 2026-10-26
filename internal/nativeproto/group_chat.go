// 本文件编解码3011原生群消息，复用单聊的纯文本JSON与六实体规则；不推断成员或建立网络连接。
package nativeproto

import (
	"encoding/json"
	"encoding/xml"
	"fmt"
	"strings"
)

// CommandGroupMessage 是按群成员连接分别投递的原生群消息命令，不是服务器广播。
const CommandGroupMessage uint32 = 3011

// GroupMessage 保留3011外层群标识、消息编号、内容JSON及记录选项；不包含成员路由。
type GroupMessage struct {
	GroupID    string
	MessageID  uint64
	Content    string
	HideRecord bool
}

// EncodeGroupMessage 将非空群标识及内容JSON编码为3011帧，不分配标识、不执行投递。
// 群标识/文本非法、消息编号为零或内容不是JSON时返回错误。
func EncodeGroupMessage(value GroupMessage) (Frame, error) {
	if err := validateMessageID(value.GroupID); err != nil {
		return Frame{}, err
	}
	if value.MessageID == 0 {
		return Frame{}, fmt.Errorf("MSG_ID不得为0")
	}
	if err := validateMessageText(value.Content); err != nil {
		return Frame{}, err
	}
	if !json.Valid([]byte(value.Content)) {
		return Frame{}, fmt.Errorf("MSG必须是有效内容JSON，不是原始文本")
	}
	var group strings.Builder
	if err := xml.EscapeText(&group, []byte(value.GroupID)); err != nil {
		return Frame{}, err
	}
	payload := fmt.Sprintf(`<X_QGROUP_SEND_MSG docver="1"><QGROUP_ID>%s</QGROUP_ID><MSG_ID>%d</MSG_ID><MSG>%s</MSG><HIDE_RECORD>%s</HIDE_RECORD></X_QGROUP_SEND_MSG>`, group.String(), value.MessageID, chatEntities.Replace(value.Content), chatFlag(value.HideRecord))
	return Frame{Command: CommandGroupMessage, Payload: payload}, nil
}

// DecodeGroupMessage 解析3011并保留内容JSON，不保存正文或把群标识当作成员证明。
// 命令、XML结构/版本、群标识、消息编号、记录标志或JSON非法时返回错误。
func DecodeGroupMessage(frame Frame) (GroupMessage, error) {
	root, err := decodeMessageEntities(frame, CommandGroupMessage, "X_QGROUP_SEND_MSG", map[string]string{"nbsp": " "})
	if err != nil {
		return GroupMessage{}, err
	}
	fields, err := messageFields(root, "QGROUP_ID", "MSG_ID", "MSG", "HIDE_RECORD")
	if err != nil {
		return GroupMessage{}, err
	}
	values := make(map[string]string, len(fields))
	for name, field := range fields {
		values[name], err = messageLeafText(field)
		if err != nil {
			return GroupMessage{}, err
		}
	}
	if err := validateMessageID(values["QGROUP_ID"]); err != nil {
		return GroupMessage{}, err
	}
	id, err := parseMessageUint(values["MSG_ID"], 64)
	if err != nil || id == 0 {
		return GroupMessage{}, fmt.Errorf("无效MSG_ID %q", values["MSG_ID"])
	}
	if values["HIDE_RECORD"] != "0" && values["HIDE_RECORD"] != "1" {
		return GroupMessage{}, fmt.Errorf("HIDE_RECORD必须是0或1")
	}
	if !json.Valid([]byte(values["MSG"])) {
		return GroupMessage{}, fmt.Errorf("MSG不是有效内容JSON")
	}
	return GroupMessage{GroupID: values["QGROUP_ID"], MessageID: id, Content: values["MSG"], HideRecord: values["HIDE_RECORD"] == "1"}, nil
}
