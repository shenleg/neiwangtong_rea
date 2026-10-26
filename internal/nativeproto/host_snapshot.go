// 本文件提供共享Host的深拷贝快照与单聊原始身份路由；目录条目不是在线证明。
package nativeproto

import (
	"context"
	"fmt"
	"sort"
)

// Snapshot 返回本身份整份已学习目录、在线身份和最新有效群资料的深拷贝，无外部I/O或错误。
// Online表示Host正在接受发现，允许零peer；OnlineUserIDs才是双向READY的对端，PeerUserID仅在恰有一个在线peer时填写。
func (h *Host) Snapshot() ClientSnapshot {
	h.mu.Lock()
	defer h.mu.Unlock()
	snapshot := ClientSnapshot{OwnUserID: h.config.Client.Node.UserID, Online: h.accepting}
	if h.directory != nil {
		snapshot.Contacts = h.directory.ListContacts()
	}
	for _, peer := range h.peers {
		if peer.stopped {
			continue
		}
		remote := peer.client.Snapshot()
		if remote.Online && remote.PeerUserID == peer.node.UserID {
			snapshot.OnlineUserIDs = append(snapshot.OnlineUserIDs, peer.node.UserID)
		}
	}
	sort.Strings(snapshot.OnlineUserIDs)
	if len(snapshot.OnlineUserIDs) == 1 {
		snapshot.PeerUserID = snapshot.OnlineUserIDs[0]
	}
	for _, group := range h.groups {
		if group.invalid {
			continue
		}
		info := group.info
		info.Members = append([]string(nil), info.Members...)
		snapshot.Groups = append(snapshot.Groups, info)
	}
	sort.Slice(snapshot.Groups, func(i, j int) bool { return snapshot.Groups[i].GroupID < snapshot.Groups[j].GroupID })
	return snapshot
}

// SendUser 按原始id选择已双向READY的唯一peer并提交text，返回该Client的一次传输结果，不代表独立接收。
// 未运行、身份未在线、正文无效、取消或I/O错误时返回错误；提交未知或日志失败均不重新提交，也不自动建立其他连接。
func (h *Host) SendUser(ctx context.Context, id, text string) (ChatResult, error) {
	unsent := ChatResult{Command: CommandUserMessage, Delivery: "unknown"}
	if err := ctx.Err(); err != nil {
		return unsent, err
	}
	h.mu.Lock()
	peer, err := h.onlinePeer(id)
	h.mu.Unlock()
	if err != nil {
		return unsent, err
	}
	return peer.client.SendUser(ctx, id, text)
}

// onlinePeer在Host.mu保护下查找已验证原始身份；调用方不能把目录条目或发现包当作READY证据。
func (h *Host) onlinePeer(id string) (*hostPeer, error) {
	peer := h.identities[id]
	if !h.accepting || peer == nil || peer.stopped {
		return nil, fmt.Errorf("指定原始身份尚未在线或Host已停止")
	}
	snapshot := peer.client.Snapshot()
	if !snapshot.Online || snapshot.PeerUserID != id {
		return nil, fmt.Errorf("指定原始身份尚未完成双向READY")
	}
	return peer, nil
}

func hostSameMembers(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	a, b := append([]string(nil), left...), append([]string(nil), right...)
	sort.Strings(a)
	sort.Strings(b)
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
