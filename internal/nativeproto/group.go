// 本文件编解码正常群资料同步与成员请求，并表示本身份群快照；不读取客户端数据库或从群名推测ID。
package nativeproto

import (
	"encoding/xml"
	"fmt"
	"io"
	"strings"
)

const (
	// CommandGroupInfoPush 是对端推送群基本资料的命令，不包含完整成员。
	CommandGroupInfoPush uint32 = 3002
	// CommandGroupUserPush 是同时推送群资料与完整成员的命令。
	CommandGroupUserPush uint32 = 3003
	// CommandGroupUserRequest 是按原始群ID请求完整成员的命令。
	CommandGroupUserRequest uint32 = 3006
	// CommandGroupUserResponse 是完整成员请求的回复，不是群消息回执。
	CommandGroupUserResponse uint32 = 3007
)

// GroupInfo 保存正常协议同步的群标识、资料版本与完整成员身份列表，不保证成员一直在线。
// Members为nil表示仅观察到基本资料，不能作为可发送的完整成员快照。
type GroupInfo struct {
	GroupID     string   `json:"group_id"`
	Name        string   `json:"name"`
	MasterID    string   `json:"master_id"`
	InfoVersion uint64   `json:"info_version"`
	UserVersion uint64   `json:"user_version"`
	Members     []string `json:"members"`
}

// EncodeGroupUserRequest 将原始groupID编码为3006，不加入群或推测成员；标识或XML编码非法时返回错误。
func EncodeGroupUserRequest(groupID string) (Frame, error) {
	if err := validateMessageID(groupID); err != nil {
		return Frame{}, err
	}
	return encodeMessage(CommandGroupUserRequest, "X_QGROUP_REQ_USER", []messageElement{{name: "QGROUP_ID", text: groupID}})
}

// DecodeGroupUserRequest 解析3006原始群ID；命令、XML结构或标识非法时返回错误，不执行网络请求。
func DecodeGroupUserRequest(frame Frame) (string, error) {
	root, err := decodeMessage(frame, CommandGroupUserRequest, "X_QGROUP_REQ_USER")
	if err != nil {
		return "", err
	}
	fields, err := messageFields(root, "QGROUP_ID")
	if err != nil {
		return "", err
	}
	id, err := messageLeafText(fields["QGROUP_ID"])
	if err != nil {
		return "", err
	}
	if err := validateMessageID(id); err != nil {
		return "", err
	}
	return id, nil
}

// DecodeGroupPush 解析3002基本资料或3003完整成员快照，不保存聊天数据。
// 非支持命令、结构、版本、成员或资料字段非法时返回错误；仅在局部解析副本中兼容已证实的厂商错闭合标签。
func DecodeGroupPush(frame Frame) (GroupInfo, error) {
	name := "X_QGROUP_PUSH_INFO"
	if frame.Command == CommandGroupUserPush {
		name = "X_QGROUP_PUSH_USER"
	} else if frame.Command != CommandGroupInfoPush {
		return GroupInfo{}, fmt.Errorf("不是群资料推送命令: %d", frame.Command)
	}
	root, err := decodeGroupXML(frame, frame.Command, name)
	if err != nil {
		return GroupInfo{}, err
	}
	names := []string{"INFO"}
	if frame.Command == CommandGroupUserPush {
		names = append(names, "USER")
	}
	fields, err := messageFields(root, names...)
	if err != nil {
		return GroupInfo{}, err
	}
	group, err := decodeGroupInfo(fields["INFO"])
	if err != nil {
		return GroupInfo{}, err
	}
	if frame.Command == CommandGroupUserPush {
		group.Members, err = decodeGroupMembers(fields["USER"])
	}
	return group, err
}

// GroupUserResponse 保留3007原始RET与请求群ID；Group仅在RET声明包含资料时非nil，存在资料不等于本机是成员。
type GroupUserResponse struct {
	Result  uint32
	GroupID string
	Group   *GroupInfo
}

// DecodeGroupUserResponse 解析3007返回值及可选群资料/成员，不把RET=0误作成功。
// RET的0x4位控制INFO/USER存在；结构、编号或外层/INFO群ID不一致时返回错误。
func DecodeGroupUserResponse(frame Frame) (GroupUserResponse, error) {
	root, err := decodeGroupXML(frame, CommandGroupUserResponse, "X_QGROUP_REQ_USER_RSP")
	if err != nil {
		return GroupUserResponse{}, err
	}
	var resultText string
	for _, child := range root.children {
		if child.name == "RET" {
			resultText, err = messageLeafText(child)
			if err != nil {
				return GroupUserResponse{}, err
			}
		}
	}
	result, err := parseMessageUint(resultText, 32)
	if err != nil {
		return GroupUserResponse{}, err
	}
	names := []string{"RET", "QGROUP_ID"}
	// 样本0x65e784以RET bit 0x4决定是否携带INFO+USER，0不是通用成功码。
	if result&0x4 != 0 {
		names = append(names, "INFO", "USER")
	}
	fields, err := messageFields(root, names...)
	if err != nil {
		return GroupUserResponse{}, err
	}
	id, err := messageLeafText(fields["QGROUP_ID"])
	if err != nil {
		return GroupUserResponse{}, err
	}
	if err := validateMessageID(id); err != nil {
		return GroupUserResponse{}, err
	}
	response := GroupUserResponse{Result: uint32(result), GroupID: id}
	if result&0x4 == 0 {
		return response, nil
	}
	group, err := decodeGroupInfo(fields["INFO"])
	if err != nil {
		return GroupUserResponse{}, err
	}
	if group.GroupID != id {
		return GroupUserResponse{}, fmt.Errorf("3007外层与INFO群ID不一致")
	}
	group.Members, err = decodeGroupMembers(fields["USER"])
	if err != nil {
		return GroupUserResponse{}, err
	}
	response.Group = &group
	return response, nil
}

func decodeGroupXML(frame Frame, command uint32, name string) (messageElement, error) {
	payload, err := normalizeGroupInfoVersion(frame.Payload, name)
	if err != nil {
		return messageElement{}, err
	}
	frame.Payload = payload
	return decodeMessageEntities(frame, command, name, map[string]string{"nbsp": " "})
}

// normalizeGroupInfoVersion只更正root/INFO中十进制QGROUP_INFO_VER的已知错闭合，不改CDATA、属性或其他字段。
// RawToken用于定位原字节范围，不负责容忍其他结构；返回值随后必须经严格XML和业务字段校验。
func normalizeGroupInfoVersion(payload, root string) (string, error) {
	decoder := xml.NewDecoder(strings.NewReader(payload))
	decoder.Entity = map[string]string{"nbsp": " "}
	var stack []xml.Name
	var output strings.Builder
	copied, valueStart := 0, -1
	for {
		before := int(decoder.InputOffset())
		token, err := decoder.RawToken()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", fmt.Errorf("读取群XML: %w", err)
		}
		switch token := token.(type) {
		case xml.StartElement:
			stack = append(stack, token.Name)
			if len(stack) > 3 {
				return "", fmt.Errorf("群XML超出root/section/leaf嵌套结构")
			}
			if len(stack) == 3 && stack[0] == (xml.Name{Local: root}) && stack[1] == (xml.Name{Local: "INFO"}) && token.Name == (xml.Name{Local: "QGROUP_INFO_VER"}) {
				valueStart = int(decoder.InputOffset())
			}
		case xml.EndElement:
			if len(stack) == 3 && stack[0] == (xml.Name{Local: root}) && stack[1] == (xml.Name{Local: "INFO"}) && stack[2] == (xml.Name{Local: "QGROUP_INFO_VER"}) && token.Name == (xml.Name{Local: "GROUP_INFO_VER"}) && valueStart >= 0 && payload[before:int(decoder.InputOffset())] == "</GROUP_INFO_VER>" {
				if _, err := parseMessageUint(payload[valueStart:before], 64); err == nil {
					output.WriteString(payload[copied:before])
					output.WriteString("</QGROUP_INFO_VER>")
					copied = int(decoder.InputOffset())
				}
			}
			if len(stack) != 0 {
				stack = stack[:len(stack)-1]
			}
		}
	}
	if copied == 0 {
		return payload, nil
	}
	output.WriteString(payload[copied:])
	return output.String(), nil
}

func decodeGroupInfo(info messageElement) (GroupInfo, error) {
	if len(info.attrs) != 0 {
		return GroupInfo{}, fmt.Errorf("INFO不得有属性")
	}
	fields, err := messageFields(info, "QGROUP_ID", "QGROUP_NAME", "QGROUP_MASTER", "QGROUP_INTR", "QGROUP_ANN", "QGROUP_INFO_VER", "QGROUP_USER_VER", "QGROUP_ALLOW_OTHER_ADD", "QGROUP_PHOTO_MD5")
	if err != nil {
		return GroupInfo{}, err
	}
	values := make(map[string]string, len(fields))
	for name, field := range fields {
		values[name], err = messageLeafText(field)
		if err != nil {
			return GroupInfo{}, err
		}
	}
	if err := validateMessageID(values["QGROUP_ID"]); err != nil {
		return GroupInfo{}, err
	}
	if err := validateMessageID(values["QGROUP_MASTER"]); err != nil {
		return GroupInfo{}, err
	}
	if err := validateMessageText(values["QGROUP_NAME"]); err != nil {
		return GroupInfo{}, err
	}
	infoVersion, err := parseMessageUint(values["QGROUP_INFO_VER"], 64)
	if err != nil {
		return GroupInfo{}, err
	}
	userVersion, err := parseMessageUint(values["QGROUP_USER_VER"], 64)
	if err != nil {
		return GroupInfo{}, err
	}
	// 样本使用%llu，尚未证明该管理字段仅有0/1，不额外猜测布尔约束。
	if _, err := parseMessageUint(values["QGROUP_ALLOW_OTHER_ADD"], 64); err != nil {
		return GroupInfo{}, err
	}
	return GroupInfo{GroupID: values["QGROUP_ID"], Name: values["QGROUP_NAME"], MasterID: values["QGROUP_MASTER"], InfoVersion: infoVersion, UserVersion: userVersion}, nil
}

func decodeGroupMembers(users messageElement) ([]string, error) {
	if len(users.attrs) != 0 || strings.TrimSpace(users.text) != "" {
		return nil, fmt.Errorf("USER须为无属性的成员列表")
	}
	members := make([]string, 0, len(users.children))
	seen := make(map[string]bool, len(users.children))
	for _, member := range users.children {
		if member.name != "ID" || len(member.children) != 0 || len(member.attrs) != 1 {
			return nil, fmt.Errorf("成员须为带name属性的ID文本")
		}
		if _, ok := member.attrs["name"]; !ok {
			return nil, fmt.Errorf("成员缺少name属性")
		}
		if err := validateMessageID(member.text); err != nil {
			return nil, err
		}
		if seen[member.text] {
			return nil, fmt.Errorf("群成员ID重复")
		}
		seen[member.text] = true
		members = append(members, member.text)
	}
	return members, nil
}
