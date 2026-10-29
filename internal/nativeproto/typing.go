// 本文件解析原生1008输入通知并按已握手连接发布短期状态；不发送输入通知、不改变未读消息或消息记录。
package nativeproto

import (
	"fmt"
	"time"
)

// CommandTyping 是原版CSendWrittingPacker的输入通知命令，原XML名称保留WRITTING拼写。
const CommandTyping uint32 = 1008

// TypingDuration 沿用原版0x514d10的timer2=5000ms；重复通知重新计时，0x514d70到期撤销提示。
const TypingDuration = 5 * time.Second

// TypingStatus 是已就绪对端最近一次输入通知的有效期；UserID来自连接身份，不来自XML或浏览器输入。
type TypingStatus struct {
	UserID string    `json:"user_id"`
	Until  time.Time `json:"until"`
}

// DecodeTyping 验证1008的X_SEND_WRITTING/docver=1和TYPE=0；返回格式或未验证TYPE的错误，不推断停止值。
// 原模板0x9ec404、pack0x668b80、unpack0x668c90，正常生产者0x60dfb0传入TYPE=0。
func DecodeTyping(frame Frame) error {
	root, err := decodeMessage(frame, CommandTyping, "X_SEND_WRITTING")
	if err != nil {
		return err
	}
	fields, err := messageFields(root, "TYPE")
	if err != nil {
		return err
	}
	text, err := messageLeafText(fields["TYPE"])
	if err != nil {
		return err
	}
	value, err := parseMessageUint(text, 32)
	if err != nil || value != 0 {
		return fmt.Errorf("不支持的输入通知TYPE")
	}
	return nil
}

func (c *Client) receiveTyping(wire []byte, frame Frame) error {
	if c.peer.Status().State != PeerOnline {
		return c.emit("typing_notification_rejected", frame.Command)
	}
	if err := c.peer.Observe(false, wire, time.Since(c.origin)); err != nil {
		return c.emit("typing_notification_rejected", frame.Command)
	}
	c.typingUntil = time.Now().Add(TypingDuration)
	c.publishSnapshot()
	return nil
}
