// 本文件在已就绪的单对端会话中提交独立聊天请求，分别记录传输/业务确认，并有限保存近期回执关联。
package nativeproto

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// ChatRequest 通过互斥的UserID或GroupID选择单聊联系人或本身份已同步的群，并提供完整纯文本正文。
// 不读取原客户端数据库；调用方负责提供有权发送的目标与完整正文。
type ChatRequest struct {
	UserID  string
	GroupID string
	Text    string
}

// ChatResult 分别报告一次请求的提交、传输 ACK 和业务 ACK；任何 ACK 都不表示独立接收确认。
// Submitted 为 true 后发生错误仍保留未知结果；MessageID 不会因超时、取消或断开重新生成。
type ChatResult struct {
	Command               uint32 `json:"command"`
	MessageID             string `json:"message_id"`
	Submitted             bool   `json:"submitted"`
	TransportAcknowledged bool   `json:"transport_acknowledged"`
	BusinessAcknowledged  bool   `json:"business_acknowledged"`
	ReceiptReceived       bool   `json:"receipt_received"`
	Delivery              string `json:"delivery"`
}

type preparedChat struct {
	userID string
	frame  Frame
	wire   []byte
}

// ValidateChatRequest 校验互斥的单聊/群聊目标和纯文本；返回nil表示可构造，不分配消息标识或产生网络操作。
// 目标缺失/冲突、无效标识、空正文、非法XML文本或超过本实现预算时返回错误；不检查成员资格。
func ValidateChatRequest(request ChatRequest) error {
	if (request.UserID == "") == (request.GroupID == "") {
		return fmt.Errorf("必须且只能指定UserID或GroupID之一")
	}
	id := request.UserID
	if request.GroupID != "" {
		id = request.GroupID
	}
	if err := validateMessageID(id); err != nil {
		return fmt.Errorf("聊天目标: %w", err)
	}
	if request.UserID != "" && len(request.UserID) > 64 {
		return fmt.Errorf("聊天对端超出节点身份64字节槽预算")
	}
	return validateChatText(request.Text)
}

func prepareChat(request ChatRequest, maxFrameBytes uint32) (*preparedChat, *ChatResult, error) {
	if err := ValidateChatRequest(request); err != nil {
		return nil, nil, err
	}
	id, err := NewMessageID()
	if err != nil {
		return nil, nil, err
	}
	content, err := EncodeTextContent(request.Text)
	if err != nil {
		return nil, nil, err
	}
	var frame Frame
	if request.GroupID != "" {
		frame, err = EncodeGroupMessage(GroupMessage{GroupID: request.GroupID, MessageID: id, Content: content})
	} else {
		frame, err = EncodeUserMessage(UserMessage{MessageID: id, Content: content})
	}
	if err != nil {
		return nil, nil, err
	}
	wire, err := Encode(frame.Command, frame.Payload)
	if err != nil {
		return nil, nil, err
	}
	if uint64(len(wire)) > uint64(maxFrameBytes) {
		return nil, nil, fmt.Errorf("聊天帧超过传输预算 %d", maxFrameBytes)
	}
	return &preparedChat{userID: request.UserID, frame: frame, wire: wire},
		&ChatResult{Command: frame.Command, MessageID: strconv.FormatUint(id, 10), Delivery: "unknown"}, nil
}

func (c *Client) sendChat(ctx context.Context) error {
	return c.sendPreparedChat(ctx, c.chat, c.result.Chat)
}

func (c *Client) sendPreparedChat(ctx context.Context, chat *preparedChat, result *ChatResult) error {
	if c.peer.Status().State != PeerOnline {
		return fmt.Errorf("聊天提交前双向 READY 尚未齐全")
	}
	versions := c.peer.messageVersions[0]
	if chat.frame.Command == CommandGroupMessage {
		versions = c.peer.groupMessageVersions[0]
	}
	if !supportsTextContent(versions) {
		return fmt.Errorf("对端消息版本不支持当前纯文本正文，不提交聊天")
	}
	c.result.Chat = result
	if err := c.emitChat("chat_send_planned", chat.frame.Command, result.MessageID); err != nil {
		return err
	}
	// 记录回调已成功保存发送意图，才允许进入 Queue；后续任何错误均不重提这条应用请求。
	before := c.transport.sendSubmissions
	err := c.transport.Send(ctx, chat.wire)
	result.Submitted = c.transport.sendSubmissions != before
	if result.Submitted {
		c.result.ChatSends++
		c.trackChat(result)
	}
	if err != nil {
		return fmt.Errorf("聊天消息 %d 发送未完成（submitted=%t）: %w", chat.frame.Command, result.Submitted, err)
	}
	result.TransportAcknowledged = true
	if err := c.peer.Observe(true, chat.wire, time.Since(c.origin)); err != nil {
		return err
	}
	return c.emitChat("transport_acknowledged", chat.frame.Command, result.MessageID)
}

// Contact 返回本次双向就绪后观察到的对端联系人资料及是否可用；不是完整网络名册，也不保证调用时仍在线。
// 无外部I/O，不会读取原客户端数据库或产生错误；Run期间不得并发调用。
func (c *Client) Contact() (Contact, bool) {
	if !c.result.ProtocolReady || c.contact == nil {
		return Contact{}, false
	}
	return *c.contact, true
}

func (c *Client) receiveChatAck(wire []byte, frame Frame) error {
	ack, err := DecodeMessageAck(frame)
	if err != nil {
		return err
	}
	if err := c.peer.Observe(false, wire, time.Since(c.origin)); err != nil {
		return err
	}
	id := strconv.FormatUint(ack.MessageID, 10)
	result, ok := c.chats[id]
	if !ok || result.Command != CommandUserMessage {
		return c.emitChat("unmatched_chat_ack", frame.Command, id)
	}
	stage := "business_acknowledged"
	if frame.Command == CommandMessageAck {
		result.BusinessAcknowledged = true
	} else {
		result.ReceiptReceived = true
		stage = "receipt_received"
	}
	return c.emitChat(stage, frame.Command, id)
}

// recentChatLimit限制常驻进程的内存，只关联最近1024条已提交消息；完整事实由外部observe持久化，不随缓存淘汰删除。
const recentChatLimit = 1024

func (c *Client) trackChat(result *ChatResult) {
	if len(c.chatOrder) < recentChatLimit {
		c.chatOrder = append(c.chatOrder, result.MessageID)
	} else {
		delete(c.chats, c.chatOrder[c.chatCursor])
		c.chatOrder[c.chatCursor] = result.MessageID
		c.chatCursor = (c.chatCursor + 1) % recentChatLimit
	}
	c.chats[result.MessageID] = result
}

func (c *Client) emitChat(stage string, command uint32, id string) error {
	return c.observeEvent(ClientEvent{Stage: stage, Command: command, MessageID: id, Role: c.result.Role,
		State: c.peer.Status().State, ElapsedMS: time.Since(c.origin).Milliseconds()})
}

func supportsTextContent(versions string) bool {
	for _, version := range strings.Split(versions, ":") {
		if version == "1" {
			return true
		}
	}
	return false
}
