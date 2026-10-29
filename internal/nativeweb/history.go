// 本文件保存本进程的有界已发记录，与接收记录共享正文预算；刷新网页不会丢失记录，不写入磁盘或自动补发。
package nativeweb

import (
	"time"

	"neiwangtong-gateway/internal/nativeproto"
)

// SentMessage 是一次已受理发送的临时记录；StartedAt区分不同协议会话中可复用的请求标识。
// Pending表示后台尚未完成；Response保留提交及未知结果，不作为送达或已读证明。
type SentMessage struct {
	StartedAt  time.Time       `json:"started_at"`
	SentAt     time.Time       `json:"sent_at"`
	TargetKind string          `json:"target_kind"`
	TargetID   string          `json:"target_id"`
	TargetName string          `json:"target_name"`
	Text       string          `json:"text"`
	Pending    bool            `json:"pending"`
	Response   MessageResponse `json:"response"`
}

// recordSentLocked在持锁时为新受理请求保存一条记录；重复查询原请求不进入此处。
func (m *Manager) recordSentLocked(request nativeproto.ChatRequest, response MessageResponse) *SentMessage {
	kind, id := "user", request.UserID
	if request.GroupID != "" {
		kind, id = "group", request.GroupID
	}
	name := conversationName(m.current.client.Snapshot(), kind, id)
	if name == "" {
		name = id
	}
	message := &SentMessage{StartedAt: *m.started, SentAt: time.Now().UTC(), TargetKind: kind, TargetID: id, TargetName: name,
		Text: request.Text, Pending: true, Response: response}
	m.sent = append(m.sent, message)
	m.sentBytes += len(message.Text)
	m.trimHistoryLocked()
	return message
}

// acknowledgeHistoryLocked只更新本次会话的单聊协议事实，群事件不能改变逐成员聚合结果。
func (m *Manager) acknowledgeHistoryLocked(event nativeproto.ClientEvent) {
	for _, message := range m.sent {
		result := &message.Response.Result
		if message.StartedAt != *m.started || result.Command != nativeproto.CommandUserMessage || result.MessageID == "" || result.MessageID != event.MessageID {
			continue
		}
		switch {
		case event.Stage == "transport_acknowledged" && event.Command == nativeproto.CommandUserMessage:
			result.TransportAcknowledged = true
		case event.Stage == "business_acknowledged" && event.Command == nativeproto.CommandMessageAck:
			result.BusinessAcknowledged = true
		case event.Stage == "receipt_received" && event.Command == nativeproto.CommandMessageReceipt:
			result.ReceiptReceived = true
		}
	}
}

func (m *Manager) sentSnapshotLocked() []SentMessage {
	messages := make([]SentMessage, len(m.sent))
	for i, message := range m.sent {
		messages[i] = *message
		messages[i].Response = message.Response.clone()
	}
	return messages
}

// trimHistoryLocked按本机时间淘汰最旧收发记录，共享条数和字节预算；淘汰不取消已经受理的发送。
func (m *Manager) trimHistoryLocked() {
	for len(m.messages)+len(m.sent) > maxReceivedMessages || m.receivedBytes+m.sentBytes > maxReceivedBytes {
		if len(m.messages) > 0 && (len(m.sent) == 0 || !m.messages[0].ReceivedAt.After(m.sent[0].SentAt)) {
			m.receivedBytes -= len(m.messages[0].Text)
			copy(m.messages, m.messages[1:])
			m.messages[len(m.messages)-1] = ReceivedMessage{}
			m.messages = m.messages[:len(m.messages)-1]
			m.receivedDropped++
			continue
		}
		m.sentBytes -= len(m.sent[0].Text)
		copy(m.sent, m.sent[1:])
		m.sent[len(m.sent)-1] = nil
		m.sent = m.sent[:len(m.sent)-1]
	}
}

func (m *Manager) clearHistoryLocked() {
	m.messages, m.sent = nil, nil
	m.receivedBytes, m.sentBytes, m.receivedTotal, m.receivedDropped = 0, 0, 0, 0
}
