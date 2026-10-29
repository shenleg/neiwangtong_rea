// 本文件记录本进程收发消息涉及的最近会话；只保存目标资料，不保存正文，也不写入磁盘。
package nativeweb

import (
	"slices"
	"time"

	"neiwangtong-gateway/internal/nativeproto"
)

// RecentConversation 表示发生过消息收发的单聊或群聊；Kind为user/group，ID保留原始目标，LastActivityAt为本机最近收发时间。
// 列表按最近活动倒序排列，重新上线保留，进程重启清空；不表示目标当前在线或可发送。
type RecentConversation struct {
	Kind           string    `json:"kind"`
	ID             string    `json:"id"`
	Name           string    `json:"name"`
	LastActivityAt time.Time `json:"last_activity_at"`
}

// recordRecentLocked在持有管理器锁时更新唯一会话，重复请求不会再次调用；群消息只更新所在群，不额外更新发送者的单聊。
func (m *Manager) recordRecentLocked(kind, id, name string, at time.Time) {
	if currentName := conversationName(m.current.client.Snapshot(), kind, id); currentName != "" {
		name = currentName
	}
	entry := RecentConversation{Kind: kind, ID: id, Name: name, LastActivityAt: at}
	index := slices.IndexFunc(m.recent, func(item RecentConversation) bool { return item.Kind == kind && item.ID == id })
	if index >= 0 {
		if entry.Name == "" {
			entry.Name = m.recent[index].Name
		}
		m.recent = slices.Delete(m.recent, index, index+1)
	}
	m.recent = append([]RecentConversation{entry}, m.recent...)
}

// recentSnapshotLocked复制会话并使用当前目录名称，暂未同步的群仍保留最后已知名称；调用者持有管理器锁。
func (m *Manager) recentSnapshotLocked(snapshot nativeproto.ClientSnapshot) []RecentConversation {
	recent := append([]RecentConversation{}, m.recent...)
	for i := range recent {
		if name := conversationName(snapshot, recent[i].Kind, recent[i].ID); name != "" {
			recent[i].Name = name
		}
		if recent[i].Name == "" {
			recent[i].Name = recent[i].ID
		}
	}
	return recent
}

func conversationName(snapshot nativeproto.ClientSnapshot, kind, id string) string {
	if kind == "user" {
		for _, contact := range snapshot.Contacts {
			if contact.UserID == id {
				return contact.Name
			}
		}
	} else {
		for _, group := range snapshot.Groups {
			if group.GroupID == id {
				return group.Name
			}
		}
	}
	return ""
}
