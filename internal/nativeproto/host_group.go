// 本文件按Host完整群快照向当前可用成员投递同一3011消息，明确记录未投递成员，失败不重提。
package nativeproto

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
)

type hostGroupDispatch struct {
	host      *Host
	group     GroupInfo
	chat      *preparedChat
	messageID string
}

type hostGroupRoute struct {
	peer   *hostPeer
	result GroupRecipientResult
}

// validate 在成员Client的运行循环中复核群计划；版本或连接变化返回错误，不沿用过时成员投递。
func (d *hostGroupDispatch) validate(client *Client) error {
	d.host.mu.Lock()
	defer d.host.mu.Unlock()
	current, ok := d.host.groups[d.group.GroupID]
	if !ok || current.invalid || current.info.UserVersion != d.group.UserVersion || current.info.InfoVersion != d.group.InfoVersion || !hostSameMembers(current.info.Members, d.group.Members) {
		return fmt.Errorf("群成员或资料已变化，停止旧计划的投递，不自动补发")
	}
	peer, err := d.host.onlinePeer(client.remoteNode.UserID)
	if err != nil {
		return err
	}
	if peer.client != client {
		return fmt.Errorf("群成员的当前连接已变化")
	}
	return nil
}

// SendGroup 使用最新完整群快照，向当前可用成员提交相同text、MSG_ID与正文GUID，未就绪成员记录为Skipped而不阻断发送。
// 返回逐成员事实；Submitted表示至少一人已提交，TransportAcknowledged仅完整名单全部成员确认时为true，送达仍unknown。
// 未同步、本机不在群或没有可用成员时报错；在线路由失败可能部分提交，保留结果，不存储离线消息、补发或自动重试。
// 不能在Host观察回调中同步调用；同一Host的群请求串行，各成员的网络发送由各自Client运行循环执行。
func (h *Host) SendGroup(ctx context.Context, id, text string) (ChatResult, error) {
	unsent := ChatResult{Command: CommandGroupMessage, Delivery: "unknown"}
	if err := ctx.Err(); err != nil {
		return unsent, err
	}
	select {
	case h.groupSends <- struct{}{}:
		defer func() { <-h.groupSends }()
	case <-ctx.Done():
		return unsent, ctx.Err()
	}
	h.mu.Lock()
	group, routes, err := h.groupRecipients(id)
	h.mu.Unlock()
	if err != nil {
		for _, route := range routes {
			unsent.Recipients = append(unsent.Recipients, route.result)
		}
		return unsent, err
	}
	request := ChatRequest{GroupID: id, Text: text}
	chat, result, err := prepareChat(request, h.config.Client.Transport.MaxMessageBytes)
	if err != nil {
		return unsent, err
	}
	dispatch := &hostGroupDispatch{host: h, group: group, chat: chat, messageID: result.MessageID}
	result.Recipients = make([]GroupRecipientResult, len(routes))
	for i, route := range routes {
		result.Recipients[i] = route.result
	}
	// 整条群请求的发送意图先持久化，避免其他成员已提交后才发现记录不可写。
	if err := h.emit(ClientEvent{Stage: "group_send_planned", Command: CommandGroupMessage, MessageID: result.MessageID, Role: "host"}); err != nil {
		for i := range result.Recipients {
			if !result.Recipients[i].Skipped {
				result.Recipients[i].Error = "群发送意图记录失败，未提交"
			}
		}
		return *result, err
	}
	failures := make([]error, len(routes))
	var pending sync.WaitGroup
	for i, route := range routes {
		if route.result.Skipped {
			continue
		}
		pending.Add(1)
		go func(i int, route hostGroupRoute) {
			defer pending.Done()
			part, err := route.peer.client.submitChatCall(ctx, request, dispatch)
			recipient := route.result
			recipient.Submitted, recipient.TransportAcknowledged = part.Submitted, part.TransportAcknowledged
			if err != nil {
				recipient.Error = err.Error()
				failures[i] = fmt.Errorf("群成员 %q 投递失败: %w", recipient.UserID, err)
			}
			result.Recipients[i] = recipient
		}(i, route)
	}
	pending.Wait()
	result.TransportAcknowledged = true
	for _, recipient := range result.Recipients {
		result.Submitted = result.Submitted || recipient.Submitted
		result.TransportAcknowledged = result.TransportAcknowledged && recipient.TransportAcknowledged
	}
	return *result, errors.Join(failures...)
}

// groupRecipients 在Host.mu保护下选择可用路由，并保留完整名单中每个非己成员的结果；离线或能力不兼容不阻断其他人。
func (h *Host) groupRecipients(id string) (GroupInfo, []hostGroupRoute, error) {
	group, ok := h.groups[id]
	if !h.config.Client.GroupChat || !ok || group.invalid {
		return GroupInfo{}, nil, fmt.Errorf("群未同步完整有效的成员资料")
	}
	own, available := false, 0
	var routes []hostGroupRoute
	for _, member := range group.info.Members {
		if member == h.config.Client.Node.UserID {
			own = true
			continue
		}
		route := hostGroupRoute{result: GroupRecipientResult{UserID: member}}
		peer, err := h.onlinePeer(member)
		if err != nil {
			route.result.Skipped, route.result.Error = true, "协议连接尚未就绪，本次未投递，不自动补发"
		} else if !peer.client.Snapshot().GroupTextSupported {
			route.result.PeerIndex = peer.index
			route.result.Skipped, route.result.Error = true, "不支持当前纯文本群消息，本次未投递"
		} else {
			route.peer, route.result.PeerIndex = peer, peer.index
			available++
		}
		routes = append(routes, route)
	}
	if !own || len(routes) == 0 {
		return GroupInfo{}, nil, fmt.Errorf("群须包含本机及至少一个其他成员")
	}
	sort.Slice(routes, func(i, j int) bool { return routes[i].result.UserID < routes[j].result.UserID })
	if available == 0 {
		return GroupInfo{}, routes, fmt.Errorf("群内没有可投递的在线成员，未提交")
	}
	return group.info, routes, nil
}
