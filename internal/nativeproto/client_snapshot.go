// 本文件发布运行循环的只读快照，并在显式学习模式下持久化已验证对端；读取不接触网络或运行中的可变表。
package nativeproto

import (
	"fmt"
	"slices"
	"sort"
	"time"
)

// ClientSnapshot 是当前连接和本身份目录、完整群成员的值快照；Contacts不代表在线，Groups不保证穷尽所有群。
// OnlineUserIDs仅包含双向READY且尚未停止的对端；单Client在线时恰有一个ID，PeerUserID保留单对端兼容语义。
type ClientSnapshot struct {
	OwnUserID          string         `json:"own_user_id"`
	Online             bool           `json:"online"`
	PeerUserID         string         `json:"peer_user_id"`
	OnlineUserIDs      []string       `json:"online_user_ids"`
	GroupTextSupported bool           `json:"group_text_supported"` // 当前已就绪连接已协商可发送纯文本群消息。
	Contacts           []Contact      `json:"contacts"`
	Groups             []GroupInfo    `json:"groups"`
	Typing             []TypingStatus `json:"typing"`
}

// Snapshot 返回并发安全的深拷贝，包括在线ID、联系人、群成员和未过期输入状态；调用方修改返回值不会影响后续读取。
// 无外部I/O或错误；启动目录立即可见，但只有完成双向READY且尚未停止的连接才标为在线。
func (c *Client) Snapshot() ClientSnapshot {
	snapshot := *c.snapshot.Load()
	snapshot.OnlineUserIDs = append([]string(nil), snapshot.OnlineUserIDs...)
	snapshot.Contacts = append([]Contact(nil), snapshot.Contacts...)
	snapshot.Groups = append([]GroupInfo(nil), snapshot.Groups...)
	snapshot.Typing = slices.DeleteFunc(append([]TypingStatus(nil), snapshot.Typing...), func(status TypingStatus) bool { return !status.Until.After(time.Now()) })
	for i := range snapshot.Groups {
		snapshot.Groups[i].Members = append([]string(nil), snapshot.Groups[i].Members...)
	}
	return snapshot
}

// publishSnapshot只由构造函数或运行循环调用；原子替换后不再修改已发布切片。
func (c *Client) publishSnapshot() {
	snapshot := &ClientSnapshot{OwnUserID: c.config.Node.UserID}
	if directory := c.directory.Load(); directory != nil {
		snapshot.Contacts = directory.ListContacts()
	}
	if c.lifecycle.Load() == clientOnline && c.peer.Status().State == PeerOnline {
		snapshot.Online = true
		snapshot.PeerUserID = c.remoteNode.UserID
		snapshot.OnlineUserIDs = []string{c.remoteNode.UserID}
		snapshot.GroupTextSupported = c.config.GroupChat && supportsTextContent(c.peer.groupMessageVersions[0])
		if c.typingUntil.After(time.Now()) {
			snapshot.Typing = []TypingStatus{{UserID: c.remoteNode.UserID, Until: c.typingUntil}}
		}
	}
	for _, group := range c.groups {
		group.Members = append([]string(nil), group.Members...)
		snapshot.Groups = append(snapshot.Groups, group)
	}
	sort.Slice(snapshot.Groups, func(i, j int) bool { return snapshot.Groups[i].GroupID < snapshot.Groups[j].GroupID })
	c.snapshot.Store(snapshot)
}

func (c *Client) observeEvent(event ClientEvent) error {
	c.publishSnapshot()
	return c.observe(event)
}

func (c *Client) learnDirectory() error {
	if !c.config.LearnContacts {
		return nil
	}
	// NAME来自已经验证身份的1000；没有有效资料时不能把发现包当作联系人写入目录。
	if c.contact == nil {
		return fmt.Errorf("已就绪对端缺少可学习的联系人资料")
	}
	var contacts []Contact
	if directory := c.directory.Load(); directory != nil {
		contacts = directory.ListContacts()
	}
	found := false
	for i := range contacts {
		if contacts[i].UserID == c.contact.UserID {
			contacts[i] = *c.contact
			found = true
			break
		}
	}
	if !found {
		contacts = append(contacts, *c.contact)
	}
	directory, err := NewDirectory(c.config.Node.UserID, contacts)
	if err != nil {
		return err
	}
	if c.config.ObserveDirectory != nil {
		if err := c.config.ObserveDirectory(directory); err != nil {
			return err
		}
	}
	c.directory.Store(directory)
	return nil
}
