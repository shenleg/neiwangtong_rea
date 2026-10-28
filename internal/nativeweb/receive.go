// 本文件维护当前后台会话的有界入站消息缓冲，仅供认证后的网页轮询，不写入磁盘或运行日志。
package nativeweb

import (
	"fmt"
	"time"

	"neiwangtong-gateway/internal/nativeproto"
)

// 同时限制条数和正文总字节数，避免长时间在线后无限保留聊天内容。
const (
	maxReceivedMessages = 128
	maxReceivedBytes    = 1 << 20
)

// ReceivedMessage 是当前会话内临时保留的入站消息；Sequence用于轮询去重，ReceivedAt是本机接收时间而非对端声明的发送时间。
// 消息标识和发送者来自协议接收回调，正文不写入阶段记录；本结构不表示人工已读。
type ReceivedMessage struct {
	nativeproto.IncomingMessage
	Sequence   uint64    `json:"sequence"`
	ReceivedAt time.Time `json:"received_at"`
}

// receive接收已由协议层核验的消息，在锁内发布有界副本；旧会话回调明确失败，退出前仍接收在途消息，不把正文拼入错误。
func (m *Manager) receive(run *running, message nativeproto.IncomingMessage) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.current != run {
		return fmt.Errorf("入站消息所属后台会话已停止")
	}
	m.receivedTotal++
	m.messages = append(m.messages, ReceivedMessage{IncomingMessage: message, Sequence: m.receivedTotal, ReceivedAt: time.Now().UTC()})
	m.receivedBytes += len(message.Text)
	for len(m.messages) > maxReceivedMessages || m.receivedBytes > maxReceivedBytes {
		m.receivedBytes -= len(m.messages[0].Text)
		copy(m.messages, m.messages[1:])
		m.messages[len(m.messages)-1] = ReceivedMessage{} // 释放淘汰正文的引用，避免底层切片继续保留它。
		m.messages = m.messages[:len(m.messages)-1]
		m.receivedDropped++
	}
	return nil
}
