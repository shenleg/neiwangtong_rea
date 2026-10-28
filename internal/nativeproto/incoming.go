// 本文件解码在线入站聊天并限制内存去重；不记录或持久化聊天正文。
package nativeproto

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"
)

// IncomingMessage 是已就绪连接收到的纯文本聊天；SenderID来自连接身份，群聊才包含GroupID。
// Text只交给显式配置的接收回调，不进入运行事件、结果或快照。
type IncomingMessage struct {
	Command    uint32 `json:"command"`
	MessageID  string `json:"message_id"`
	SenderID   string `json:"sender_id"`
	SenderName string `json:"sender_name"`
	GroupID    string `json:"group_id"`
	Text       string `json:"text"`
}

// 入站正文允许比发送入口更大的文本，但限制为64KiB；仅保留最近1024条摘要，避免常驻进程无限增长。
const maxIncomingTextBytes = 64 * 1024
const incomingLimit = 1024

type incomingCache struct {
	mu            sync.Mutex
	seen          map[[32]byte]bool
	order         [incomingLimit][32]byte
	count, cursor int
}

// deliver只保存摘要；持锁串行检查和回调，防止不同peer并发交付相同消息。
func (cache *incomingCache) deliver(message IncomingMessage, content string, callback func(IncomingMessage) error) (bool, error) {
	// 内容JSON包含每条消息自己的GUID；对端重启后即使MSG_ID及可见文字相同，新GUID仍代表新消息。
	key, _ := json.Marshal([]any{message.SenderID, message.Command, message.GroupID, message.MessageID, content})
	digest := sha256.Sum256(key)
	cache.mu.Lock()
	defer cache.mu.Unlock()
	if cache.seen[digest] {
		return false, nil
	}
	if callback(message) != nil {
		return false, fmt.Errorf("接收消息回调失败")
	}
	if cache.seen == nil {
		cache.seen = make(map[[32]byte]bool)
	}
	if cache.count == incomingLimit {
		delete(cache.seen, cache.order[cache.cursor])
	} else {
		cache.count++
	}
	cache.order[cache.cursor] = digest
	cache.cursor = (cache.cursor + 1) % incomingLimit
	cache.seen[digest] = true
	return true, nil
}

// decodeIncomingText只接受有静态证据的normal及lnk片段，XML实体已由外层解析一次。
func decodeIncomingText(content string) (string, error) {
	var root struct {
		Type string                       `json:"type"`
		Data []map[string]json.RawMessage `json:"dt"`
	}
	if err := json.Unmarshal([]byte(content), &root); err != nil || root.Type != "0" || len(root.Data) == 0 {
		return "", fmt.Errorf("不支持的消息内容")
	}
	var result strings.Builder
	for _, item := range root.Data {
		if len(item) != 1 {
			return "", fmt.Errorf("不支持的消息片段")
		}
		var part struct {
			Type  string  `json:"t"`
			Value *string `json:"v"`
		}
		if raw, ok := item["txt"]; ok {
			if json.Unmarshal(raw, &part) != nil || part.Type != "normal" || part.Value == nil {
				return "", fmt.Errorf("不支持的文本片段")
			}
		} else if raw, ok := item["lnk"]; ok {
			if json.Unmarshal(raw, &part) != nil || part.Value == nil {
				return "", fmt.Errorf("不支持的链接片段")
			}
		} else {
			return "", fmt.Errorf("不支持的消息片段")
		}
		if result.Len()+len(*part.Value) > maxIncomingTextBytes {
			return "", fmt.Errorf("入站正文超出预算")
		}
		result.WriteString(*part.Value)
	}
	return result.String(), nil
}

func (c *Client) receiveIncoming(wire []byte, frame Frame) error {
	reject := func() error {
		return c.observeEvent(ClientEvent{Stage: "incoming_message_rejected", Command: frame.Command, Role: c.result.Role, State: c.peer.Status().State, ElapsedMS: time.Since(c.origin).Milliseconds()})
	}
	if c.peer.Status().State != PeerOnline {
		return reject()
	}
	versions := c.peer.messageVersions
	if frame.Command == CommandGroupMessage {
		versions = c.peer.groupMessageVersions
		if !c.config.GroupChat {
			return reject()
		}
	}
	if !supportsTextContent(versions[0]) || !supportsTextContent(versions[1]) {
		return reject()
	}
	var id uint64
	var content, groupID string
	if frame.Command == CommandUserMessage {
		message, err := DecodeUserMessage(frame)
		if err != nil {
			return reject()
		}
		id, content = message.MessageID, message.Content
	} else {
		message, err := DecodeGroupMessage(frame)
		if err != nil {
			return reject()
		}
		id, content, groupID = message.MessageID, message.Content, message.GroupID
		if c.incomingGroup != nil {
			if !c.incomingGroup(groupID, c.remoteNode.UserID) {
				return reject()
			}
		} else if !groupHasMembers(c.groups[groupID], c.config.Node.UserID, c.remoteNode.UserID) {
			return reject()
		}
	}
	if err := c.peer.Observe(false, wire, time.Since(c.origin)); err != nil {
		return reject()
	}
	text, err := decodeIncomingText(content)
	if err != nil {
		return reject()
	}
	name := ""
	if c.contact != nil {
		name = c.contact.Name
	}
	message := IncomingMessage{Command: frame.Command, MessageID: strconv.FormatUint(id, 10), SenderID: c.remoteNode.UserID, SenderName: name, GroupID: groupID, Text: text}
	delivered, err := c.incoming.deliver(message, content, c.config.ReceiveMessage)
	if err != nil {
		return err
	}
	if frame.Command == CommandUserMessage {
		// 原生0x5be0b0在已连接状态分发1004后调用0x60dca0返回1005；重复提交也应确认，但不重复显示。
		// 只排队标识，交给Run串行写出，避免在入站回调中递归驱动传输。
		c.incomingAcks = append(c.incomingAcks, id)
	}
	if delivered {
		return c.emitChat("message_received", frame.Command, message.MessageID)
	}
	return nil
}

// flushIncomingAcks只确认本次已接受的单聊；断开后不另建连接发送ACK，也不发送1006或群回执。
func (c *Client) flushIncomingAcks(ctx context.Context) error {
	if c.transport.failed != nil || c.transport.runtime.Status().State != ENetConnected || c.peer.Status().State != PeerOnline {
		c.incomingAcks = nil
		return nil
	}
	for len(c.incomingAcks) > 0 {
		id := c.incomingAcks[0]
		c.incomingAcks = c.incomingAcks[1:]
		frame, err := EncodeMessageAck(CommandMessageAck, MessageAck{MessageID: id})
		if err != nil {
			return err
		}
		if err := c.sendFrame(ctx, frame); err != nil {
			return err
		}
	}
	return nil
}

func groupHasMembers(group GroupInfo, ownID, remoteID string) bool {
	own, remote := false, false
	for _, id := range group.Members {
		own = own || id == ownID
		remote = remote || id == remoteID
	}
	return own && remote
}

func (h *Host) incomingMember(groupID, remoteID string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	group, ok := h.groups[groupID]
	return ok && !group.invalid && groupHasMembers(group.info, h.config.Client.Node.UserID, remoteID)
}

// drainIncoming按原始顺序处理发送或关闭期间缓存的帧，群同步等控制帧不能被后续聊天越过。
func (c *Client) drainIncoming() error {
	if c.config.ReceiveMessage == nil || c.peer == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), c.config.ShutdownTimeout)
	defer cancel()
	for len(c.transport.messages) > 0 {
		wire := c.transport.messages[0]
		c.transport.messages[0] = nil
		c.transport.messages = c.transport.messages[1:]
		c.transport.queuedBytes -= uint64(len(wire))
		if err := c.receive(ctx, wire); err != nil {
			return err
		}
	}
	return nil
}
