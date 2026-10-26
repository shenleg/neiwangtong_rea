// 本文件编解码1004在线单聊及其1005/1006回执，并构造样本支持的纯文本JSON内容；不建立网络连接或判断送达。
package nativeproto

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"
	"sync/atomic"
)

const (
	// CommandUserMessage 是在已选择的联系人连接上传输的单聊命令；帧内不包含接收者ID。
	CommandUserMessage uint32 = 1004
	// CommandMessageAck 是单聊应用层确认；不代表正文已在界面显示。
	CommandMessageAck uint32 = 1005
	// CommandMessageReceipt 是单聊回执命令；不替代独立接收观察。
	CommandMessageReceipt uint32 = 1006
	// MaxTextBytes 是本实现纯文本入口的4096 UTF-8字节预算，避免开放未经真实验证的超长正文，不是厂商上限。
	MaxTextBytes = 4096
)

var messageSequence atomic.Uint64

// 六实体规则供1004与3011共用；一次Replace不会重新转义本轮产生的实体。
var chatEntities = strings.NewReplacer("&", "&amp;", `"`, "&quot;", "<", "&lt;", ">", "&gt;", "'", "&apos;", " ", "&nbsp;")

// NewMessageID 分配本进程递增的外层MSG_ID，首次为1；不持久化、不与正文GUID混用，也不产生网络操作。
// 达到uint64上限时返回错误，不回绕复用标识；从0开始是符合已见样本增量模式的本实现选择。
func NewMessageID() (uint64, error) {
	for {
		previous := messageSequence.Load()
		if previous == math.MaxUint64 {
			return 0, fmt.Errorf("消息标识计数器已耗尽")
		}
		if messageSequence.CompareAndSwap(previous, previous+1) {
			return previous + 1, nil
		}
	}
}

// UserMessage 保留单聊外层字段；Content是富文本JSON而非原始文本，Time是原始MSG_TIME值。
// 当前高层只发送在线、留存记录、不请求阅读回执的纯文本消息。
type UserMessage struct {
	MessageID  uint64
	Content    string
	Time       uint64
	Receipt    bool
	Offline    bool
	HideRecord bool
}

// MessageAck 仅携带业务消息标识；与ENet可靠序号、正文JSON的GUID和独立接收记录是不同概念。
type MessageAck struct {
	MessageID uint64
}

func chatFlag(value bool) string {
	if value {
		return "1"
	}
	return "0"
}

// EncodeMessageAck 将1005应用确认或1006回执编码为仅含MSG_ID的帧；返回值不表示独立接收。
// command不受支持或消息标识为零时返回错误，不自动回复收到的消息。
func EncodeMessageAck(command uint32, value MessageAck) (Frame, error) {
	name, err := ackRoot(command)
	if err != nil {
		return Frame{}, err
	}
	if value.MessageID == 0 {
		return Frame{}, fmt.Errorf("MSG_ID不得为0")
	}
	return encodeMessage(command, name, []messageElement{{name: "MSG_ID", text: strconv.FormatUint(value.MessageID, 10)}})
}

// DecodeMessageAck 解析1005应用确认或1006回执，返回其原始消息标识。
// 命令、XML根/版本、字段结构、零值或超出uint64范围时返回错误；不推断正文已显示。
func DecodeMessageAck(frame Frame) (MessageAck, error) {
	name, err := ackRoot(frame.Command)
	if err != nil {
		return MessageAck{}, err
	}
	root, err := decodeMessage(frame, frame.Command, name)
	if err != nil {
		return MessageAck{}, err
	}
	fields, err := messageFields(root, "MSG_ID")
	if err != nil {
		return MessageAck{}, err
	}
	text, err := messageLeafText(fields["MSG_ID"])
	if err != nil {
		return MessageAck{}, err
	}
	id, err := parseMessageUint(text, 64)
	if err != nil || id == 0 {
		return MessageAck{}, fmt.Errorf("无效MSG_ID %q", text)
	}
	return MessageAck{MessageID: id}, nil
}

func ackRoot(command uint32) (string, error) {
	switch command {
	case CommandMessageAck:
		return "X_SEND_MSG_ACK", nil
	case CommandMessageReceipt:
		return "X_SEND_RECEIPT", nil
	default:
		return "", fmt.Errorf("不是单聊应用确认或回执命令: %d", command)
	}
}

type textDocument struct {
	Version string     `json:"ver"`
	App     string     `json:"app"`
	ID      string     `json:"id"`
	Type    string     `json:"type"`
	Data    []textItem `json:"dt"`
	Font    textFont   `json:"ft"`
}

type textItem struct {
	Text textValue `json:"txt"`
}

type textValue struct {
	Type  string `json:"t"`
	Value string `json:"v"`
}

type textFont struct {
	Size string `json:"s"`
}

// EncodeTextContent 将非空纯文本编码为ver=6.0的shiyeline内容JSON，生成独立正文GUID；返回新字符串。
// 超过MaxTextBytes、UTF-8/XML文本非法或随机源失败时返回错误；不规范化换行、不把字面反斜杠转成换行。
func EncodeTextContent(text string) (string, error) {
	return encodeTextContent(text, rand.Reader)
}

func validateChatText(text string) error {
	if text == "" || len(text) > MaxTextBytes {
		return fmt.Errorf("纯文本正文须为1..%d个UTF-8字节；这是本实现预算而非厂商上限", MaxTextBytes)
	}
	return validateMessageText(text)
}

func encodeTextContent(text string, random io.Reader) (string, error) {
	if err := validateChatText(text); err != nil {
		return "", err
	}
	guid, err := NewSessionGUID(random)
	if err != nil {
		return "", err
	}
	// 版本、普通文本类型和10号字体均按已恢复的IPC内容构造；协议把这些值存为JSON字符串。
	document := textDocument{Version: "6.0", App: "shiyeline", ID: hex.EncodeToString(guid[:]), Type: "0",
		Data: []textItem{{Text: textValue{Type: "normal", Value: text}}}, Font: textFont{Size: "10"}}
	var output bytes.Buffer
	encoder := json.NewEncoder(&output)
	// XML转义在外层统一执行；JSON不另把中文、&和尖括号编码成HTML转义。
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(document); err != nil {
		return "", err
	}
	return string(bytes.TrimSuffix(output.Bytes(), []byte{'\n'})), nil
}

// EncodeUserMessage 将调用者提供的1004字段编码为XML帧，Content按文本转义，不允许注入外层元素。
// 消息ID为零、正文不是有效内容JSON或存在非法文本时返回错误，不生成消息ID或推算时间。
func EncodeUserMessage(value UserMessage) (Frame, error) {
	if value.MessageID == 0 {
		return Frame{}, fmt.Errorf("MSG_ID不得为0")
	}
	if err := validateMessageText(value.Content); err != nil {
		return Frame{}, err
	}
	if !json.Valid([]byte(value.Content)) {
		return Frame{}, fmt.Errorf("MSG必须是有效内容JSON，不是原始文本")
	}
	// 样本对MSG使用自定义实体替换，尤其把普通空格写成&nbsp;；标准XML编码器不能复现这一格式。
	content := chatEntities.Replace(value.Content)
	payload := fmt.Sprintf(`<X_SEND_MSG docver="1"><MSG_ID>%d</MSG_ID><RECEIPT>%s</RECEIPT><MSG>%s</MSG><MSG_TIME>%d</MSG_TIME><OFFLINE>%s</OFFLINE><HIDE_RECORD>%s</HIDE_RECORD></X_SEND_MSG>`,
		value.MessageID, chatFlag(value.Receipt), content, value.Time, chatFlag(value.Offline), chatFlag(value.HideRecord))
	return Frame{Command: CommandUserMessage, Payload: payload}, nil
}

// DecodeUserMessage 返回1004外层字段并保留Content原文；不执行内容、读取文件或保存聊天。
// 命令、版本、XML结构、消息标识、布尔字段或内容JSON非法时返回错误。
func DecodeUserMessage(frame Frame) (UserMessage, error) {
	root, err := decodeMessageEntities(frame, CommandUserMessage, "X_SEND_MSG", map[string]string{"nbsp": " "})
	if err != nil {
		return UserMessage{}, err
	}
	fields, err := messageFields(root, "MSG_ID", "RECEIPT", "MSG", "MSG_TIME", "OFFLINE", "HIDE_RECORD")
	if err != nil {
		return UserMessage{}, err
	}
	values := make(map[string]string, len(fields))
	for name, field := range fields {
		values[name], err = messageLeafText(field)
		if err != nil {
			return UserMessage{}, err
		}
	}
	id, err := parseMessageUint(values["MSG_ID"], 64)
	if err != nil || id == 0 {
		return UserMessage{}, fmt.Errorf("无效MSG_ID %q", values["MSG_ID"])
	}
	at, err := parseMessageUint(values["MSG_TIME"], 64)
	if err != nil {
		return UserMessage{}, err
	}
	var flags [3]bool
	for i, name := range []string{"RECEIPT", "OFFLINE", "HIDE_RECORD"} {
		switch values[name] {
		case "1":
			flags[i] = true
		case "0":
		default:
			return UserMessage{}, fmt.Errorf("%s必须是0或1", name)
		}
	}
	if !json.Valid([]byte(values["MSG"])) {
		return UserMessage{}, fmt.Errorf("MSG不是有效内容JSON")
	}
	return UserMessage{MessageID: id, Content: values["MSG"], Time: at, Receipt: flags[0], Offline: flags[1], HideRecord: flags[2]}, nil
}
