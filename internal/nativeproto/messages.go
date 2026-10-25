// 本文件编解码已验证的原生握手、READY、控制与群信息请求 XML；不生成身份、不连接网络，也不修补 3005 正文。
package nativeproto

import (
	"encoding/xml"
	"fmt"
	"io"
	"math/bits"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

const (
	// CommandHandshake 是公布本机资料和能力的握手命令，不代表身份认证成功。
	CommandHandshake uint32 = 1000
	// CommandHeartbeat 是应用层心跳命令。
	CommandHeartbeat uint32 = 1016
	// CommandQuit 是应用层正常退出命令，不是 ENet DISCONNECT。
	CommandQuit uint32 = 1017
	// CommandReady 是公布发送者 USER_ID 的就绪命令。
	CommandReady uint32 = 1018
	// CommandGroupInfoRequest 是按 QGROUP_ID 请求群资料的命令。
	CommandGroupInfoRequest uint32 = 3004
	// CommandGroupInfoResponse 是群资料响应命令；厂商标签错配尚无业务兼容规则，仅保留 Frame 原文。
	CommandGroupInfoResponse uint32 = 3005
)

// XMLField 是 FEATURE 或 INFO 中的叶子；保留字段顺序、正文及 USER_FACE id 等未知属性，不支持命名空间或嵌套。
type XMLField struct {
	Name       string
	Value      string
	Attributes map[string]string
}

// HandshakePorts 保存主机字节序的传输端口；零值可用，XML 中的十进制整数需要交换 16 位字节序。
type HandshakePorts struct {
	FileTCP, FileENet, FolderTCP, FolderENet                             uint16
	FileTCPReverse, FileENetReverse, FolderTCPReverse, FolderENetReverse uint16
	LANUpdateHTTP                                                        uint16
}

// Handshake 是已验证的三个握手 section；调用者负责提供本机合法资料，框架不读取或生成身份，也不臆测必需的厂商叶子集合。
type Handshake struct {
	Features []XMLField
	Info     []XMLField
	Ports    HandshakePorts
}

// Ready 公布发送者的原始 UserID；当前仅验证 Param=0，不解释其他值。
type Ready struct {
	UserID string
	Param  uint32
}

var handshakePortNames = [...]string{
	"FILE_TRAN_TCP_PORT", "FILE_TRAN_ENET_PORT", "FOLDER_TRAN_TCP_PORT", "FOLDER_TRAN_ENET_PORT",
	"FILE_TRAN_TCP_REVERSE_PORT", "FILE_TRAN_ENET_REVERSE_PORT", "FOLDER_TRAN_TCP_REVERSE_PORT", "FOLDER_TRAN_ENET_REVERSE_PORT",
	"LAN_UPDATE_HTTP_PORT",
}

// EncodeHandshake 将调用者资料编码为 1000 帧；返回转义后的 XML，空 section、重复字段、非法 XML 名称或文本返回错误。
func EncodeHandshake(value Handshake) (Frame, error) {
	features, err := handshakeLeaves(value.Features)
	if err != nil {
		return Frame{}, fmt.Errorf("FEATURE: %w", err)
	}
	info, err := handshakeLeaves(value.Info)
	if err != nil {
		return Frame{}, fmt.Errorf("INFO: %w", err)
	}
	ports := [...]uint16{value.Ports.FileTCP, value.Ports.FileENet, value.Ports.FolderTCP, value.Ports.FolderENet,
		value.Ports.FileTCPReverse, value.Ports.FileENetReverse, value.Ports.FolderTCPReverse, value.Ports.FolderENetReverse,
		value.Ports.LANUpdateHTTP}
	net := messageElement{name: "NET"}
	for i, name := range handshakePortNames {
		net.children = append(net.children, messageElement{name: name, text: strconv.FormatUint(uint64(bits.ReverseBytes16(ports[i])), 10)})
	}
	return encodeMessage(CommandHandshake, "X_HANDSHARK", []messageElement{{name: "FEATURE", children: features}, {name: "INFO", children: info}, net})
}

// DecodeHandshake 解析 1000 帧，保留 FEATURE/INFO 叶子及属性并将端口换回主机序；格式、版本、结构或端口不受支持时返回错误。
func DecodeHandshake(frame Frame) (Handshake, error) {
	root, err := decodeMessage(frame, CommandHandshake, "X_HANDSHARK")
	if err != nil {
		return Handshake{}, err
	}
	sections, err := messageFields(root, "FEATURE", "INFO", "NET")
	if err != nil {
		return Handshake{}, err
	}
	features, err := decodeHandshakeLeaves(sections["FEATURE"])
	if err != nil {
		return Handshake{}, err
	}
	info, err := decodeHandshakeLeaves(sections["INFO"])
	if err != nil {
		return Handshake{}, err
	}
	net := sections["NET"]
	if len(net.attrs) != 0 {
		return Handshake{}, fmt.Errorf("NET 不支持属性")
	}
	fields, err := messageFields(net, handshakePortNames[:]...)
	if err != nil {
		return Handshake{}, err
	}
	var ports [9]uint16
	for i, name := range handshakePortNames {
		text, err := messageLeafText(fields[name])
		if err != nil {
			return Handshake{}, err
		}
		port, err := parseMessageUint(text, 16)
		if err != nil {
			return Handshake{}, fmt.Errorf("%s: %w", name, err)
		}
		ports[i] = bits.ReverseBytes16(uint16(port))
	}
	return Handshake{Features: features, Info: info, Ports: HandshakePorts{
		FileTCP: ports[0], FileENet: ports[1], FolderTCP: ports[2], FolderENet: ports[3],
		FileTCPReverse: ports[4], FileENetReverse: ports[5], FolderTCPReverse: ports[6], FolderENetReverse: ports[7], LANUpdateHTTP: ports[8],
	}}, nil
}

// EncodeReady 将发送者 UserID 编码为 1018 帧；标识为空或含非法 XML 文本、Param 非零时返回错误。
func EncodeReady(value Ready) (Frame, error) {
	if err := validateMessageID(value.UserID); err != nil {
		return Frame{}, err
	}
	if value.Param != 0 {
		return Frame{}, fmt.Errorf("不支持 READY PARAM=%d，当前仅验证 0", value.Param)
	}
	return encodeMessage(CommandReady, "X_READY", []messageElement{{name: "USER_ID", text: value.UserID}, {name: "PARAM", text: "0"}})
}

// DecodeReady 返回 1018 帧的发送者标识；命令、XML、必要字段、标识或 PARAM 超出已验证范围时返回错误。
func DecodeReady(frame Frame) (Ready, error) {
	root, err := decodeMessage(frame, CommandReady, "X_READY")
	if err != nil {
		return Ready{}, err
	}
	fields, err := messageFields(root, "USER_ID", "PARAM")
	if err != nil {
		return Ready{}, err
	}
	userID, err := messageLeafText(fields["USER_ID"])
	if err != nil {
		return Ready{}, err
	}
	if err := validateMessageID(userID); err != nil {
		return Ready{}, err
	}
	text, err := messageLeafText(fields["PARAM"])
	if err != nil {
		return Ready{}, err
	}
	param, err := parseMessageUint(text, 32)
	if err != nil {
		return Ready{}, err
	}
	if param != 0 {
		return Ready{}, fmt.Errorf("不支持 READY PARAM=%d，当前仅验证 0", param)
	}
	return Ready{UserID: userID, Param: uint32(param)}, nil
}

// HeartbeatFrame 返回固定 1016 心跳帧；无输入且不会失败，不代表收到对端响应。
func HeartbeatFrame() Frame {
	return Frame{Command: CommandHeartbeat, Payload: `<X_HEARTBEAT docver="1" />`}
}

// QuitFrame 返回固定 1017 应用层退出帧；无输入且不会失败，不执行传输断开。
func QuitFrame() Frame {
	return Frame{Command: CommandQuit, Payload: `<X_QUIT docver="1" />`}
}

// DecodeControl 校验 1016/1017 的命令、根节点、版本和空正文；返回 nil 表示结构有效，其他命令或额外内容返回错误。
func DecodeControl(frame Frame) error {
	var name string
	switch frame.Command {
	case CommandHeartbeat:
		name = "X_HEARTBEAT"
	case CommandQuit:
		name = "X_QUIT"
	default:
		return fmt.Errorf("不支持的控制命令 %d", frame.Command)
	}
	root, err := decodeMessage(frame, frame.Command, name)
	if err != nil {
		return err
	}
	_, err = messageFields(root)
	return err
}

// EncodeGroupInfoRequest 将非空群标识编码为 3004 QGROUP_ID 请求；非法 UTF-8、NUL 或 XML 文本返回错误，不实现 3005 业务语义。
func EncodeGroupInfoRequest(groupID string) (Frame, error) {
	if err := validateMessageID(groupID); err != nil {
		return Frame{}, err
	}
	return encodeMessage(CommandGroupInfoRequest, "X_QGROUP_REQ_INFO", []messageElement{{name: "QGROUP_ID", text: groupID}})
}

// DecodeGroupInfoRequest 返回 3004 帧的原始群标识；命令、版本、XML、必要字段或标识无效时返回错误。
func DecodeGroupInfoRequest(frame Frame) (string, error) {
	root, err := decodeMessage(frame, CommandGroupInfoRequest, "X_QGROUP_REQ_INFO")
	if err != nil {
		return "", err
	}
	fields, err := messageFields(root, "QGROUP_ID")
	if err != nil {
		return "", err
	}
	groupID, err := messageLeafText(fields["QGROUP_ID"])
	if err != nil {
		return "", err
	}
	if err := validateMessageID(groupID); err != nil {
		return "", err
	}
	return groupID, nil
}

type messageElement struct {
	name     string
	attrs    map[string]string
	text     string
	children []messageElement
}

func validateMessageID(value string) error {
	if value == "" {
		return fmt.Errorf("标识不得为空")
	}
	return validateMessageText(value)
}

func validateMessageText(value string) error {
	if !utf8.ValidString(value) {
		return fmt.Errorf("文本必须是有效 UTF-8")
	}
	for _, r := range value {
		if (r < 0x20 && r != '\t' && r != '\n' && r != '\r') || r == 0xfffe || r == 0xffff {
			return fmt.Errorf("文本包含 XML 不允许的字符 U+%04X", r)
		}
	}
	return nil
}

// xmlLocalName 使用 XML 1.0 的 Name 范围，但不接受本协议未定义的命名空间冒号。
func xmlLocalName(name string) bool {
	if name == "" || !utf8.ValidString(name) {
		return false
	}
	for i, r := range name {
		start := r == '_' || r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' ||
			r >= 0xc0 && r <= 0xd6 || r >= 0xd8 && r <= 0xf6 || r >= 0xf8 && r <= 0x2ff ||
			r >= 0x370 && r <= 0x37d || r >= 0x37f && r <= 0x1fff || r >= 0x200c && r <= 0x200d ||
			r >= 0x2070 && r <= 0x218f || r >= 0x2c00 && r <= 0x2fef || r >= 0x3001 && r <= 0xd7ff ||
			r >= 0xf900 && r <= 0xfdcf || r >= 0xfdf0 && r <= 0xfffd || r >= 0x10000 && r <= 0xeffff
		if !start && (i == 0 || !(r == '-' || r == '.' || r >= '0' && r <= '9' || r == 0xb7 || r >= 0x300 && r <= 0x36f || r >= 0x203f && r <= 0x2040)) {
			return false
		}
	}
	return true
}

func handshakeLeaves(fields []XMLField) ([]messageElement, error) {
	if len(fields) == 0 {
		return nil, fmt.Errorf("叶子列表不得为空")
	}
	leaves := make([]messageElement, 0, len(fields))
	seen := make(map[string]bool, len(fields))
	for _, field := range fields {
		if !xmlLocalName(field.Name) || seen[field.Name] {
			return nil, fmt.Errorf("非法或重复字段 %q", field.Name)
		}
		seen[field.Name] = true
		if err := validateMessageText(field.Value); err != nil {
			return nil, fmt.Errorf("%s: %w", field.Name, err)
		}
		for name, value := range field.Attributes {
			if name == "xmlns" || !xmlLocalName(name) {
				return nil, fmt.Errorf("非法属性名 %q", name)
			}
			if err := validateMessageText(value); err != nil {
				return nil, fmt.Errorf("属性 %s: %w", name, err)
			}
		}
		leaves = append(leaves, messageElement{name: field.Name, attrs: field.Attributes, text: field.Value})
	}
	return leaves, nil
}

func decodeHandshakeLeaves(section messageElement) ([]XMLField, error) {
	if len(section.attrs) != 0 || strings.Trim(section.text, " \t\r\n") != "" || len(section.children) == 0 {
		return nil, fmt.Errorf("%s 必须是不带属性、无混合正文的非空叶子列表", section.name)
	}
	fields := make([]XMLField, 0, len(section.children))
	seen := make(map[string]bool, len(section.children))
	for _, child := range section.children {
		if len(child.children) != 0 || seen[child.name] {
			return nil, fmt.Errorf("%s 字段 %s 嵌套或重复", section.name, child.name)
		}
		seen[child.name] = true
		fields = append(fields, XMLField{Name: child.name, Value: child.text, Attributes: child.attrs})
	}
	return fields, nil
}

func encodeMessage(command uint32, name string, children []messageElement) (Frame, error) {
	var output strings.Builder
	encoder := xml.NewEncoder(&output)
	root := messageElement{name: name, attrs: map[string]string{"docver": "1"}, children: children}
	if err := encodeMessageElement(encoder, root); err != nil {
		return Frame{}, err
	}
	if err := encoder.Flush(); err != nil {
		return Frame{}, err
	}
	return Frame{Command: command, Payload: output.String()}, nil
}

func encodeMessageElement(encoder *xml.Encoder, element messageElement) error {
	start := xml.StartElement{Name: xml.Name{Local: element.name}}
	names := make([]string, 0, len(element.attrs))
	for name := range element.attrs {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		start.Attr = append(start.Attr, xml.Attr{Name: xml.Name{Local: name}, Value: element.attrs[name]})
	}
	if err := encoder.EncodeToken(start); err != nil {
		return err
	}
	if err := encoder.EncodeToken(xml.CharData(element.text)); err != nil {
		return err
	}
	for _, child := range element.children {
		if err := encodeMessageElement(encoder, child); err != nil {
			return err
		}
	}
	return encoder.EncodeToken(start.End())
}

func decodeMessage(frame Frame, command uint32, name string) (messageElement, error) {
	if frame.Command != command {
		return messageElement{}, fmt.Errorf("命令 %d 不匹配 %d/%s", frame.Command, command, name)
	}
	if err := validatePayload(frame.Payload); err != nil {
		return messageElement{}, err
	}
	decoder := xml.NewDecoder(strings.NewReader(frame.Payload))
	for {
		token, err := decoder.Token()
		if err != nil {
			return messageElement{}, fmt.Errorf("读取 XML 根节点: %w", err)
		}
		if whitespace, ok := token.(xml.CharData); ok && strings.Trim(string(whitespace), " \t\r\n") == "" {
			continue
		}
		start, ok := token.(xml.StartElement)
		if !ok {
			return messageElement{}, fmt.Errorf("XML 必须以根节点开始")
		}
		root, err := decodeMessageElement(decoder, start, 0)
		if err != nil {
			return messageElement{}, err
		}
		if root.name != name || len(root.attrs) != 1 || root.attrs["docver"] != "1" {
			return messageElement{}, fmt.Errorf("不支持的根节点、属性或 docver，要求 %s docver=1", name)
		}
		for {
			tail, err := decoder.Token()
			if err == io.EOF {
				return root, nil
			}
			if err != nil {
				return messageElement{}, fmt.Errorf("XML 尾随数据: %w", err)
			}
			if whitespace, ok := tail.(xml.CharData); !ok || strings.Trim(string(whitespace), " \t\r\n") != "" {
				return messageElement{}, fmt.Errorf("XML 根节点后存在尾随数据")
			}
		}
	}
}

func decodeMessageElement(decoder *xml.Decoder, start xml.StartElement, depth int) (messageElement, error) {
	// 已验证消息最多是 root/section/leaf，提前拒绝更深结构，不为未知嵌套分配树。
	if depth > 2 || start.Name.Space != "" || !xmlLocalName(start.Name.Local) {
		return messageElement{}, fmt.Errorf("不支持的 XML 嵌套或名称 %s", start.Name.Local)
	}
	element := messageElement{name: start.Name.Local}
	for _, attr := range start.Attr {
		if attr.Name.Space != "" || attr.Name.Local == "xmlns" || !xmlLocalName(attr.Name.Local) {
			return messageElement{}, fmt.Errorf("不支持的 XML 属性 %s", attr.Name.Local)
		}
		if element.attrs == nil {
			element.attrs = make(map[string]string)
		}
		if _, exists := element.attrs[attr.Name.Local]; exists {
			return messageElement{}, fmt.Errorf("重复属性 %s", attr.Name.Local)
		}
		element.attrs[attr.Name.Local] = attr.Value
	}
	var text strings.Builder
	for {
		token, err := decoder.Token()
		if err != nil {
			return messageElement{}, fmt.Errorf("读取 %s: %w", element.name, err)
		}
		switch value := token.(type) {
		case xml.StartElement:
			child, err := decodeMessageElement(decoder, value, depth+1)
			if err != nil {
				return messageElement{}, err
			}
			element.children = append(element.children, child)
		case xml.CharData:
			text.Write(value)
		case xml.EndElement:
			element.text = text.String()
			return element, nil
		case xml.Comment:
			// 注释不是业务正文；不影响字段值或结构校验。
		default:
			return messageElement{}, fmt.Errorf("%s 含不支持的 XML 指令", element.name)
		}
	}
}

func messageFields(parent messageElement, names ...string) (map[string]messageElement, error) {
	if strings.Trim(parent.text, " \t\r\n") != "" {
		return nil, fmt.Errorf("%s 不支持混合正文", parent.name)
	}
	fields := make(map[string]messageElement, len(names))
	for _, child := range parent.children {
		if _, exists := fields[child.name]; exists {
			return nil, fmt.Errorf("%s 重复字段 %s", parent.name, child.name)
		}
		supported := false
		for _, name := range names {
			if child.name == name {
				supported = true
				break
			}
		}
		if !supported {
			return nil, fmt.Errorf("%s 不支持字段 %s", parent.name, child.name)
		}
		fields[child.name] = child
	}
	if len(fields) != len(names) {
		return nil, fmt.Errorf("%s 缺少必要字段", parent.name)
	}
	return fields, nil
}

func messageLeafText(leaf messageElement) (string, error) {
	if len(leaf.attrs) != 0 || len(leaf.children) != 0 {
		return "", fmt.Errorf("%s 必须是无属性的文本叶子", leaf.name)
	}
	return leaf.text, nil
}

func parseMessageUint(text string, size int) (uint64, error) {
	if text == "" || strings.IndexFunc(text, func(r rune) bool { return r < '0' || r > '9' }) >= 0 {
		return 0, fmt.Errorf("%q 不是无符号十进制整数", text)
	}
	return strconv.ParseUint(text, 10, size)
}
