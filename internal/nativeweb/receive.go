// 本文件维护本进程的临时入站消息和网页已读标记，与已发记录共享内存预算，不写磁盘或发送协议已读回执。
package nativeweb

import (
	"fmt"
	"time"

	"neiwangtong-gateway/internal/nativeproto"
)

// 收发记录共同限制条数和正文总字节数，避免长时间在线后无限保留聊天内容。
const (
	maxReceivedMessages = 128
	maxReceivedBytes    = 1 << 20
)

// ReceivedMessage 是本进程临时保留的入站消息；Sequence用于轮询去重，ReceivedAt是本机接收时间而非对端声明的发送时间。
// Read仅表示网页已展示，不是协议已读回执；消息正文和已读标记都不落盘。
type ReceivedMessage struct {
	nativeproto.IncomingMessage
	Sequence   uint64    `json:"sequence"`
	ReceivedAt time.Time `json:"received_at"`
	Read       bool      `json:"read"`
}

// receive接收已由协议层核验的消息，在锁内发布有界副本；旧会话回调明确失败，退出前仍接收在途消息，不把正文拼入错误。
func (m *Manager) receive(run *running, message nativeproto.IncomingMessage) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.current != run {
		return fmt.Errorf("入站消息所属后台会话已停止")
	}
	now := time.Now().UTC()
	kind, id, name := "user", message.SenderID, message.SenderName
	if message.Command == nativeproto.CommandGroupMessage {
		kind, id, name = "group", message.GroupID, ""
	}
	m.recordRecentLocked(kind, id, name, now)
	m.receivedTotal++
	m.messages = append(m.messages, ReceivedMessage{IncomingMessage: message, Sequence: m.receivedTotal, ReceivedAt: now})
	m.receivedBytes += len(message.Text)
	m.trimHistoryLocked()
	return nil
}

// MarkRead 把当前网页会话中sequence所指会话截至该序号的消息标为已展示；不影响其他目标或较新的消息。
// startedAt须匹配当前协议会话，旧页面或未知序号报错；标记仅保存于内存，不生成网络回执。
func (m *Manager) MarkRead(startedAt time.Time, sequence uint64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.started == nil || !m.started.Equal(startedAt) || sequence == 0 || sequence > m.receivedTotal {
		return fmt.Errorf("已读标记不属于当前消息状态，请刷新页面")
	}
	var target *ReceivedMessage
	for i := range m.messages {
		if m.messages[i].Sequence == sequence {
			target = &m.messages[i]
			break
		}
	}
	if target == nil {
		return nil // 已因预算淘汰的消息不再产生未读提示，无需恢复或保存额外标记。
	}
	for i := range m.messages {
		message := &m.messages[i]
		if message.Sequence > sequence || message.Command != target.Command || message.GroupID != target.GroupID {
			continue
		}
		if message.GroupID != "" || message.SenderID == target.SenderID {
			message.Read = true
		}
	}
	return nil
}
