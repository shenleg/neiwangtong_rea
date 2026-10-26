// 本文件通过请求通道为常驻Client提供连续单聊/群聊；只有Run调度循环可以读写传输、准备消息及更新回执状态。
package nativeproto

import (
	"context"
	"errors"
	"fmt"
)

const (
	clientIdle uint32 = iota
	clientRunning
	clientOnline
	clientStopped
)

type chatCall struct {
	ctx     context.Context
	request ChatRequest
	group   *hostGroupDispatch
	reply   chan chatReply
}

type chatReply struct {
	result ChatResult
	err    error
}

// SendUser 从本身份联系人列表选择userID并提交一条text；可在Run以外的协程连续或并发调用，同一连接仍串行发送。
// 成功返回本条消息的传输确认快照；1005/1006可能稍后由observe通知，不修改已返回值，delivery始终unknown。
// 未就绪、目标不在列表/不是当前单对端、正文或预算无效、取消及I/O失败返回错误；已提交后的错误不自动重发。
// observe回调不能同步调用本方法；Run结束后拒绝新消息，不能通过再次Run重用旧连接。
func (c *Client) SendUser(ctx context.Context, userID, text string) (ChatResult, error) {
	unsent := ChatResult{Command: CommandUserMessage, Delivery: "unknown"}
	if err := ctx.Err(); err != nil {
		return unsent, err
	}
	if c.lifecycle.Load() != clientOnline {
		return unsent, fmt.Errorf("客户端尚未就绪或已停止")
	}
	request := ChatRequest{UserID: userID, Text: text}
	if err := ValidateChatRequest(request); err != nil {
		return unsent, err
	}
	if !supportsTextContent(c.localMessageVersions) {
		return unsent, fmt.Errorf("本机MSG声明不支持当前单聊正文版本")
	}
	directory := c.directory.Load()
	if directory == nil {
		return unsent, fmt.Errorf("单聊需要当前独立身份自己的联系人列表")
	}
	contact, err := directory.ContactByID(userID)
	if err != nil {
		return unsent, err
	}
	if contact.Discovery != c.config.RemoteDiscovery {
		return unsent, fmt.Errorf("选定联系人不是当前单对端连接，不自动建立其他连接")
	}
	return c.submitChatCall(ctx, request, nil)
}

func chatCommand(request ChatRequest) uint32 {
	if request.GroupID != "" {
		return CommandGroupMessage
	}
	return CommandUserMessage
}

func (c *Client) submitChatCall(ctx context.Context, request ChatRequest, group *hostGroupDispatch) (ChatResult, error) {
	unsent := ChatResult{Command: chatCommand(request), Delivery: "unknown"}
	call := chatCall{ctx: ctx, request: request, group: group, reply: make(chan chatReply, 1)}
	select {
	case c.requests <- call:
		// 交给Run后由它返回最终提交状态；不能因调用ctx先取消就丢掉MSG_ID和可能已经Queue的事实。
		response := <-call.reply
		return response.result, response.err
	case <-ctx.Done():
		return unsent, ctx.Err()
	case <-c.done:
		return unsent, fmt.Errorf("客户端已停止")
	}
}

func (c *Client) handleChatCall(runCtx context.Context, call chatCall) error {
	unsent := ChatResult{Command: chatCommand(call.request), Delivery: "unknown"}
	if err := call.ctx.Err(); err != nil {
		call.reply <- chatReply{result: unsent, err: err}
		return nil
	}
	var chat *preparedChat
	var result *ChatResult
	if call.group != nil {
		// Host已检查完整路由；此处只在当前连接的Run中复核计划仍有效，不另生成消息ID或正文GUID。
		if err := call.group.validate(c); err != nil {
			call.reply <- chatReply{result: unsent, err: err}
			return nil
		}
		chat = call.group.chat
		result = &ChatResult{Command: CommandGroupMessage, MessageID: call.group.messageID, Delivery: "unknown"}
	} else {
		if call.request.GroupID != "" {
			if err := c.selectGroup(call.request.GroupID); err != nil {
				call.reply <- chatReply{result: unsent, err: err}
				return nil
			}
		} else if call.request.UserID != c.remoteNode.UserID {
			call.reply <- chatReply{result: unsent, err: fmt.Errorf("当前连接身份与指定联系人不一致")}
			return nil
		}
		var err error
		chat, result, err = prepareChat(call.request, c.config.Transport.MaxMessageBytes)
		if err != nil {
			call.reply <- chatReply{result: unsent, err: err}
			return nil
		}
	}
	ctx, cancel := context.WithCancel(runCtx)
	stop := context.AfterFunc(call.ctx, cancel)
	defer stop()
	defer cancel()
	err := c.sendPreparedChat(ctx, chat, result)
	call.reply <- chatReply{result: *result, err: err}
	if err != nil && !result.Submitted && ctx.Err() != nil && errors.Is(err, ctx.Err()) {
		return nil // 请求在Queue前取消不会损坏会话；已Queue后的取消必须停止，保留未知结果。
	}
	return err
}
