// Package agent 将联系人/群名称解析与单次 IPC 发送组合为统一入口，供 HTTP 层调用。
// 本包是唯一直接接触客户端窗口（经 internal/ipc）与本地数据快照的层；
// HTTP 层只依赖本包导出的接口，不感知 HWND、WM_COPYDATA 或数据库格式。
package agent

import (
	"errors"
	"fmt"
	"os"
	"sync"
	"time"

	"neiwangtong-gateway/internal/contacts"
	"neiwangtong-gateway/internal/groups"
	"neiwangtong-gateway/internal/ipc"
)

// Sender 是发送与预检能力的最小接口；真实实现为 ipc.Adapter。
type Sender interface {
	Preflight() (ipc.Preflight, error)
	SendGroup(groupID, message string) (ipc.SendResult, error)
	SendUser(userID, message string) (ipc.SendResult, error)
}

// SenderFactory 按次构造绑定当前客户端实例的发送器；每次发送使用新实例，
// 客户端重启后自然重新绑定，不在本层缓存过期句柄。
type SenderFactory func() (Sender, error)

// NewAdapterSender 返回用 exePath 创建 ipc.Adapter 的真实工厂。
func NewAdapterSender(exePath string) SenderFactory {
	return func() (Sender, error) {
		adapter, err := ipc.NewAdapter(exePath)
		if err != nil {
			return nil, err
		}
		return adapter, nil
	}
}

// Options 配置数据文件与口令；两个文件均可省略，省略时对应的名称解析
// 与列表接口返回 ErrNotConfigured，按 ID 发送不受影响。
// 路径可以指向客户端正在使用的实时数据库文件（客户端写盘后下一次请求自动重载），
// 也可以指向退出后取得的离线副本（替换副本文件后同样自动重载）。
type Options struct {
	NRPath     string   // nr 联系人库路径（加密）
	NRPassword []byte   // nr codec 口令的精确字节，仅在内存使用
	QRPPath    string   // qrp 群库路径（明文）
}

// snapshotCache 按文件 mtime 失效地缓存一个已解析的数据快照：
// 文件修改时间未变时直接复用，变化后下次读取重新打开。
type snapshotCache[T any] struct {
	path  string
	load  func(string) (T, error)
	inner T
	mtime time.Time
	valid bool
}

// get 返回缓存快照；文件缺失或重新打开失败时明确报错，不退回过期数据冒充新数据。
// 加载完成后执行读后复核：期间文件被改写或事务边文件出现即作废本次结果，
// 关闭"边文件在读取后才出现"的竞态窗口（Python 审计工具同款防护）。
func (c *snapshotCache[T]) get() (T, error) {
	info, err := os.Stat(c.path)
	if err != nil {
		return c.inner, fmt.Errorf("检查数据文件 %s: %w", c.path, err)
	}
	if c.valid && info.ModTime().Equal(c.mtime) {
		return c.inner, nil
	}
	db, err := c.load(c.path)
	if err != nil {
		return c.inner, err
	}
	after, err := os.Stat(c.path)
	if err != nil {
		return c.inner, fmt.Errorf("%w：读后复核失败 %v", ErrConcurrentWrite, err)
	}
	if !after.ModTime().Equal(info.ModTime()) {
		return c.inner, fmt.Errorf("%w：%s 在读取期间被修改", ErrConcurrentWrite, c.path)
	}
	if err := checkSidecars(c.path); err != nil {
		return c.inner, fmt.Errorf("%w：%v", ErrConcurrentWrite, err)
	}
	c.inner, c.mtime, c.valid = db, after.ModTime(), true
	return db, nil
}

// checkSidecars 检查事务边文件是否出现；读取期间出现即代表结果可能落在写事务窗口内。
func checkSidecars(path string) error {
	for _, suffix := range []string{"-wal", "-shm", "-journal"} {
		if _, err := os.Stat(path + suffix); err == nil {
			return fmt.Errorf("存在边文件 %s", path+suffix)
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}

// Agent 是统一入口：按 mtime 缓存数据文件、按次创建发送器；全部方法可并发调用，
// 发送与快照加载在实例内串行执行。
type Agent struct {
	factory     SenderFactory
	options     Options
	mu          sync.Mutex
	contacts    *snapshotCache[*contacts.DB]
	groups      *snapshotCache[*groups.DB]
}

var (
	// ErrNotConfigured 表示对应数据快照未配置，名称解析与列表不可用。
	ErrNotConfigured = errors.New("对应数据快照未配置")
	// ErrNotFound 表示名称精确匹配零命中。
	ErrNotFound = errors.New("名称未命中")
	// ErrClientUnavailable 表示无法绑定客户端实例（未运行、多实例或窗口异常）。
	ErrClientUnavailable = errors.New("客户端实例不可用")
	// ErrConcurrentWrite 表示读取实时数据文件期间检测到客户端写事务（mtime 变化或
	// 边文件出现），本次结果不可信已作废；立即重试即可，不需要换配置或重启。
	ErrConcurrentWrite = errors.New("数据文件正被客户端写事务修改，请重试")
)

// AmbiguousContactsError 表示联系人同名多候选，携带全部候选供调用方人工选择。
type AmbiguousContactsError struct {
	Matches []contacts.Contact
}

func (e *AmbiguousContactsError) Error() string {
	return fmt.Sprintf("联系人同名命中 %d 条，需人工选择", len(e.Matches))
}

// AmbiguousGroupsError 表示群同名多候选，携带全部候选供调用方人工选择。
type AmbiguousGroupsError struct {
	Matches []groups.Group
}

func (e *AmbiguousGroupsError) Error() string {
	return fmt.Sprintf("群同名命中 %d 条，需人工选择", len(e.Matches))
}

// New 创建统一入口；factory 为真实 ipc.Adapter 工厂，测试可注入替代实现。
func New(factory SenderFactory, options Options) *Agent {
	password := options.NRPassword
	return &Agent{
		factory: factory,
		options: options,
		contacts: &snapshotCache[*contacts.DB]{
			path: options.NRPath,
			load: func(path string) (*contacts.DB, error) { return contacts.Open(path, password) },
		},
		groups: &snapshotCache[*groups.DB]{
			path: options.QRPPath,
			load: func(path string) (*groups.DB, error) { return groups.Open(path) },
		},
	}
}

// Preflight 重新核对并返回当前客户端实例身份。
func (a *Agent) Preflight() (ipc.Preflight, error) {
	sender, err := a.newSender()
	if err != nil {
		return ipc.Preflight{}, err
	}
	return sender.Preflight()
}

// ContactsSnapshot 是一次联系人快照读取结果；快照在首次调用后缓存，
// 重新加载需重启进程或更换快照文件。
type ContactsSnapshot struct {
	Table    string            `json:"table"`
	PageSize int               `json:"page_size"`
	SHA256   string            `json:"source_sha256"`
	Users    []contacts.Contact `json:"contacts"`
}

// GroupsSnapshot 是一次群快照读取结果；缓存策略与联系人快照相同。
type GroupsSnapshot struct {
	Table    string         `json:"table"`
	PageSize int            `json:"page_size"`
	SHA256   string         `json:"source_sha256"`
	Groups   []groups.Group `json:"groups"`
}

// ListContacts 读取并缓存联系人快照，返回全部原生用户联系人。
func (a *Agent) ListContacts() (ContactsSnapshot, error) {
	db, err := a.contactsDB()
	if err != nil {
		return ContactsSnapshot{}, err
	}
	table := db.Tables()
	if len(table) != 1 {
		// 多身份表无法自动归属，保持既定纪律：不挑第一张，须人工核实快照来源。
		return ContactsSnapshot{}, fmt.Errorf("联系人快照含 %d 张身份表，须人工核实后使用单一身份的副本", len(table))
	}
	users, err := db.LoadUsers(table[0])
	if err != nil {
		return ContactsSnapshot{}, err
	}
	return ContactsSnapshot{Table: table[0], PageSize: db.PageSize(), SHA256: db.SHA256(), Users: users}, nil
}

// ResolveUserName 按原始名称精确匹配唯一联系人；零命中返回 ErrNotFound，
// 多命中返回 *AmbiguousContactsError。
func (a *Agent) ResolveUserName(name string) (contacts.Contact, error) {
	db, err := a.contactsDB()
	if err != nil {
		return contacts.Contact{}, err
	}
	table := db.Tables()
	if len(table) != 1 {
		return contacts.Contact{}, fmt.Errorf("联系人快照含 %d 张身份表，须人工核实后使用单一身份的副本", len(table))
	}
	matches, err := db.LookupName(table[0], name)
	if err != nil {
		return contacts.Contact{}, err
	}
	switch len(matches) {
	case 0:
		return contacts.Contact{}, fmt.Errorf("%w：%s", ErrNotFound, name)
	case 1:
		return matches[0], nil
	default:
		return contacts.Contact{}, &AmbiguousContactsError{Matches: matches}
	}
}

// ListGroups 读取并缓存群快照，返回全部群名称与 group_id。
func (a *Agent) ListGroups() (GroupsSnapshot, error) {
	db, err := a.groupsDB()
	if err != nil {
		return GroupsSnapshot{}, err
	}
	table := db.Tables()
	if len(table) != 1 {
		return GroupsSnapshot{}, fmt.Errorf("群快照含 %d 张身份表，须人工核实后使用单一身份的副本", len(table))
	}
	list, err := db.LoadGroups(table[0])
	if err != nil {
		return GroupsSnapshot{}, err
	}
	return GroupsSnapshot{Table: table[0], PageSize: db.PageSize(), SHA256: db.SHA256(), Groups: list}, nil
}

// ResolveGroupName 按群名精确匹配唯一群；零命中返回 ErrNotFound，
// 多命中返回 *AmbiguousGroupsError。
func (a *Agent) ResolveGroupName(name string) (groups.Group, error) {
	db, err := a.groupsDB()
	if err != nil {
		return groups.Group{}, err
	}
	table := db.Tables()
	if len(table) != 1 {
		return groups.Group{}, fmt.Errorf("群快照含 %d 张身份表，须人工核实后使用单一身份的副本", len(table))
	}
	matches, err := db.LookupName(table[0], name)
	if err != nil {
		return groups.Group{}, err
	}
	switch len(matches) {
	case 0:
		return groups.Group{}, fmt.Errorf("%w：%s", ErrNotFound, name)
	case 1:
		return matches[0], nil
	default:
		return groups.Group{}, &AmbiguousGroupsError{Matches: matches}
	}
}

// SendToUser 向 userID 指定的原始用户发送一次 text；调用事实与错误语义同 ipc.Adapter。
// 客户端绑定失败以 ErrClientUnavailable 包装返回，其余错误原样透传。
func (a *Agent) SendToUser(userID, text string) (ipc.SendResult, error) {
	return a.send(func(sender Sender) (ipc.SendResult, error) {
		return sender.SendUser(userID, text)
	})
}

// SendToGroup 向 groupID 指定的原生群发送一次 text；调用事实与错误语义同 ipc.Adapter。
func (a *Agent) SendToGroup(groupID, text string) (ipc.SendResult, error) {
	return a.send(func(sender Sender) (ipc.SendResult, error) {
		return sender.SendGroup(groupID, text)
	})
}

func (a *Agent) send(call func(Sender) (ipc.SendResult, error)) (ipc.SendResult, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	sender, err := a.factory()
	if err != nil {
		return ipc.SendResult{}, fmt.Errorf("%w: %v", ErrClientUnavailable, err)
	}
	return call(sender)
}

func (a *Agent) newSender() (Sender, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	sender, err := a.factory()
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrClientUnavailable, err)
	}
	return sender, nil
}

func (a *Agent) contactsDB() (*contacts.DB, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.options.NRPath == "" {
		return nil, ErrNotConfigured
	}
	return a.contacts.get()
}

func (a *Agent) groupsDB() (*groups.DB, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.options.QRPPath == "" {
		return nil, ErrNotConfigured
	}
	return a.groups.get()
}
