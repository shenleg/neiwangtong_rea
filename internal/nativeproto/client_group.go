// 本文件在单对端运行循环中同步本身份群资料并检查成员，再提交3011；不建立额外连接或自动入群。
package nativeproto

import (
	"context"
	"fmt"
	"time"
)

// SendGroup 向本身份已同步的groupID提交一条完整text，与SendUser共用运行循环和消息计数。
// 当前仅支持全部非己成员就是当前已就绪对端的群，返回一次提交/传输确认快照，不代表独立接收。
// 未就绪、未同步、本机不在群中、存在其他成员、正文非法或I/O失败时返回错误；不会只投递部分成员后冒充整群成功。
// 只能从Run以外的协程调用，不可在observe回调同步调用；进入Queue后失败不重连、不重提。
func (c *Client) SendGroup(ctx context.Context, groupID, text string) (ChatResult, error) {
	unsent := ChatResult{Command: CommandGroupMessage, Delivery: "unknown"}
	if err := ctx.Err(); err != nil {
		return unsent, err
	}
	if c.lifecycle.Load() != clientOnline {
		return unsent, fmt.Errorf("客户端尚未就绪或已停止")
	}
	request := ChatRequest{GroupID: groupID, Text: text}
	if err := ValidateChatRequest(request); err != nil {
		return unsent, err
	}
	if !c.config.GroupChat {
		return unsent, fmt.Errorf("客户端未启用群同步与发送")
	}
	return c.submitChatCall(ctx, request, nil)
}

func (c *Client) selectGroup(groupID string) error {
	group, ok := c.groups[groupID]
	if !ok {
		return fmt.Errorf("指定群尚未同步完整成员资料")
	}
	own, remote := false, false
	for _, member := range group.Members {
		switch member {
		case c.config.Node.UserID:
			own = true
		case c.remoteNode.UserID:
			remote = true
		default:
			return fmt.Errorf("群包含当前连接以外的成员；单对端实现不能完成整群投递")
		}
	}
	if !own || !remote {
		return fmt.Errorf("当前独立身份与对端必须都在已同步群成员列表中")
	}
	return nil
}

func (c *Client) receiveGroupPush(ctx context.Context, wire []byte, frame Frame) error {
	if err := c.peer.Observe(false, wire, time.Since(c.origin)); err != nil {
		return err
	}
	group, err := DecodeGroupPush(frame)
	if err != nil {
		return err
	}
	if frame.Command == CommandGroupUserPush {
		delete(c.groupPending, group.GroupID)
		return c.saveGroup(group, frame.Command)
	}
	if previous, ok := c.groups[group.GroupID]; ok && previous.UserVersion == group.UserVersion {
		group.Members = previous.Members
		return c.saveGroup(group, frame.Command)
	}
	// 成员版本变化后旧快照不再可发送；在收到新的完整成员前不能沿用历史成员集合。
	delete(c.groups, group.GroupID)
	if err := c.emit("group_info_received", frame.Command); err != nil {
		return err
	}
	return c.requestGroupUsers(ctx, group.GroupID)
}

func (c *Client) requestGroupUsers(ctx context.Context, groupID string) error {
	if c.groupPending[groupID] {
		return nil
	}
	request, err := EncodeGroupUserRequest(groupID)
	if err != nil {
		return err
	}
	c.groupPending[groupID] = true
	return c.sendFrame(ctx, request)
}

func (c *Client) receiveGroupUserResponse(wire []byte, frame Frame) error {
	if err := c.peer.Observe(false, wire, time.Since(c.origin)); err != nil {
		return err
	}
	response, err := DecodeGroupUserResponse(frame)
	if err != nil {
		return err
	}
	if !c.groupPending[response.GroupID] {
		return c.emit("unmatched_group_response", frame.Command)
	}
	delete(c.groupPending, response.GroupID)
	// 0x4为资料存在，0x10为请求者成员；0x0c虽带成员表，也不能证明本身份已加入。
	if response.Result&0x14 != 0x14 {
		delete(c.groups, response.GroupID)
		return c.emit("group_membership_absent", frame.Command)
	}
	return c.saveGroup(*response.Group, frame.Command)
}

func (c *Client) saveGroup(group GroupInfo, command uint32) error {
	own := false
	for _, member := range group.Members {
		if member == c.config.Node.UserID {
			own = true
			break
		}
	}
	if !own {
		delete(c.groups, group.GroupID)
		return c.emit("group_membership_absent", command)
	}
	if c.config.ObserveGroup != nil {
		snapshot := group
		snapshot.Members = append([]string(nil), group.Members...)
		if err := c.config.ObserveGroup(snapshot); err != nil {
			return err
		}
	}
	c.groups[group.GroupID] = group
	return c.emit("group_synchronized", command)
}
