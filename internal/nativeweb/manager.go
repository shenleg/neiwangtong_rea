// 本文件管理网页控制的单次常驻会话、有限状态快照和请求去重；网页断开不会取消后台会话。
package nativeweb

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"

	"neiwangtong-gateway/internal/nativeproto"
)

// ContactView 是可在网页展示的联系人；Online仅表示该身份的当前连接完成协议握手，Reason说明不可发送原因。
type ContactView struct {
	nativeproto.Contact
	Online   bool   `json:"online"`
	Sendable bool   `json:"sendable"`
	Reason   string `json:"reason"`
}

// GroupView 是本身份已同步的完整群快照；Sendable还要求所有非己成员均被当前连接覆盖。
type GroupView struct {
	nativeproto.GroupInfo
	Sendable bool   `json:"sendable"`
	Reason   string `json:"reason"`
}

// State 返回后台状态及列表的独立副本；群列表仅含本次正常同步的群，不保证穷尽。
type State struct {
	Phase         string                    `json:"phase"`
	Name          string                    `json:"name"`
	OwnUserID     string                    `json:"own_user_id"`
	Remote        string                    `json:"remote"`
	DiscoveryMode string                    `json:"discovery_mode"`
	StartedAt     *time.Time                `json:"started_at"`
	Error         string                    `json:"error"`
	Stage         string                    `json:"stage"`
	Contacts      []ContactView             `json:"contacts"`
	Groups        []GroupView               `json:"groups"`
	Events        []nativeproto.ClientEvent `json:"events"`
	MaxTextBytes  int                       `json:"max_text_bytes"`
}

// MessageResponse 保留一次网页请求的协议提交事实；Error非空时也不能丢弃已经分配的MessageID或Submitted。
type MessageResponse struct {
	RequestID string                 `json:"request_id"`
	Result    nativeproto.ChatResult `json:"result"`
	Error     string                 `json:"error"`
}

type operation struct {
	hash     [32]byte
	done     chan struct{}
	response MessageResponse
	status   int
}

type running struct {
	ctx    context.Context
	fatal  error
	cancel context.CancelFunc
	done   chan struct{}
	client session
	log    *journal
	sends  sync.WaitGroup
	ops    map[string]*operation
}

// Manager 拥有一个后台会话；构造不联网，上线必须显式Start，失败不会自动重连或补发。
type Manager struct {
	mu      sync.Mutex
	config  Config
	factory sessionFactory
	phase   string
	stage   string
	lastErr string
	started *time.Time
	saved   nativeproto.ClientSnapshot
	events  []nativeproto.ClientEvent
	current *running
	closed  bool
}

// New 验证config并只读加载自己的身份与最近联系人；返回离线管理器，配置或已有文件损坏时返回错误。
func New(config Config) (*Manager, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	ownID, err := readExistingIdentity(filepath.Join(config.DataDir, "identity"))
	if err != nil {
		return nil, err
	}
	snapshot := nativeproto.ClientSnapshot{OwnUserID: ownID}
	if ownID != "" {
		directory, err := loadDirectory(config.DataDir, ownID)
		if err != nil {
			return nil, err
		}
		snapshot.Contacts = directory.ListContacts()
	} else {
		paths, err := filepath.Glob(filepath.Join(config.DataDir, "directory-*.json"))
		if err != nil || len(paths) != 0 {
			return nil, fmt.Errorf("联系人快照存在但所属身份缺失: %v", err)
		}
	}
	return &Manager{config: config, factory: openNative, phase: "offline", saved: snapshot}, nil
}

// Start 使用name作为本次显示名称启动协议会话；保留原身份和联系人，先独占Sync记录再创建socket。
// 只允许离线/失败后启动，运行或退出中报错；名称非法返回ErrInvalidName，文件/随机源错误不重试。
// 成功受理后本进程记住该名称，进程重启仍使用CLI配置；会话时限沿用Config.SessionDuration。
func (m *Manager) Start(name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed || m.current != nil {
		return fmt.Errorf("后台会话正在运行、退出或程序已关闭")
	}
	if err := validateDisplayName(name); err != nil {
		return err
	}
	config := m.config
	config.Name = name
	if err := os.MkdirAll(config.DataDir, 0700); err != nil {
		return err
	}
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return err
	}
	now := time.Now().UTC()
	runID := now.Format("20060102T150405.000000000Z") + "-" + hex.EncodeToString(random[:])
	log, err := newJournal(filepath.Join(m.config.DataDir, "session-"+runID+".jsonl"))
	if err != nil {
		return err
	}
	if err := log.write(map[string]any{"event": "reserved", "started_at": now, "name": config.Name,
		"local": m.config.Local.String(), "remote": m.config.Remote.String(), "discover": m.config.Discover, "prefix": m.config.Prefix.String(), "session_duration_ms": m.config.SessionDuration.Milliseconds(), "delivery": "unknown"}); err != nil {
		return errors.Join(err, log.close())
	}
	ctx, cancel := context.WithCancel(context.Background())
	run := &running{ctx: ctx, cancel: cancel, done: make(chan struct{}), log: log, ops: make(map[string]*operation)}
	m.config = config
	m.current, m.started, m.phase, m.stage, m.lastErr = run, &now, "starting", "starting", ""
	m.events = nil
	m.saved.Groups = nil // 新会话必须重新同步，不能拿历史成员向变化后的群发送。
	go m.run(ctx, runID, run, config)
	return nil
}

func (m *Manager) run(ctx context.Context, runID string, run *running, config Config) {
	client, runErr := m.factory(config, runID, run.log)
	var result nativeproto.ClientResult
	if runErr == nil {
		m.mu.Lock()
		run.client = client
		m.mu.Unlock()
		result, runErr = client.Run(ctx, func(event nativeproto.ClientEvent) error {
			if err := run.log.write(event); err != nil {
				return err
			}
			m.mu.Lock()
			defer m.mu.Unlock()
			m.stage = event.Stage
			if event.Stage == "protocol_ready" && m.phase == "starting" {
				m.phase = "online"
			}
			if len(m.events) == 128 { // 页面只保留近期阶段；完整记录始终留在私有JSONL中。
				m.events = m.events[1:]
			}
			m.events = append(m.events, event)
			return nil
		})
	}
	// 先禁止新请求，再等已受理请求保存最终事实，避免关闭记录时丢失未知结果。
	m.mu.Lock()
	m.phase = "stopping"
	if client != nil {
		m.saved = client.Snapshot()
		m.saved.Online, m.saved.PeerUserID = false, ""
	}
	m.mu.Unlock()
	run.sends.Wait()
	if ctx.Err() == context.Canceled && cancellationOnly(runErr) {
		runErr = nil // 尚未握手时人工下线也属于正常停止，不伪造ProtocolReady或GracefulExit。
	}
	m.mu.Lock()
	runErr = errors.Join(runErr, run.fatal)
	m.mu.Unlock()
	runErr = errors.Join(runErr, run.log.write(map[string]any{"event": "result", "result": result, "error": errorText(runErr)}))
	runErr = errors.Join(runErr, run.log.close())
	run.cancel()
	m.mu.Lock()
	m.phase, m.lastErr = "offline", ""
	if runErr != nil {
		m.phase, m.lastErr = "failed", runErr.Error()
	}
	m.current = nil
	close(run.done)
	m.mu.Unlock()
}

// Stop 请求当前会话正常下线，不等待网络清理；已经离线时无操作，无I/O错误返回。
func (m *Manager) Stop() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.current != nil {
		m.phase = "stopping"
		m.current.cancel()
	}
}

// Close 禁止后续上线并请求正常退出；等待已受理发送及记录关闭，ctx到期返回取消/超时错误。
func (m *Manager) Close(ctx context.Context) error {
	m.mu.Lock()
	m.closed = true
	run := m.current
	if run != nil {
		m.phase = "stopping"
		run.cancel()
	}
	m.mu.Unlock()
	if run == nil {
		return nil
	}
	select {
	case <-run.done:
		m.mu.Lock()
		defer m.mu.Unlock()
		if m.lastErr != "" {
			return errors.New(m.lastErr)
		}
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// State 返回并发安全的展示快照；不打开网络、不修改已发布数据，也不把列表条目当成在线证明。
func (m *Manager) State() State {
	m.mu.Lock()
	defer m.mu.Unlock()
	snapshot := m.saved
	if m.current != nil && m.current.client != nil {
		snapshot = m.current.client.Snapshot()
	}
	online := m.phase == "online" && snapshot.Online
	state := State{Phase: m.phase, Name: m.config.Name, OwnUserID: snapshot.OwnUserID, Remote: m.config.Remote.String(),
		Error: m.lastErr, Stage: m.stage, MaxTextBytes: nativeproto.MaxTextBytes,
		Contacts: []ContactView{}, Groups: []GroupView{}, Events: append([]nativeproto.ClientEvent{}, m.events...)}
	state.DiscoveryMode = "single_peer"
	if m.config.Discover {
		state.DiscoveryMode, state.Remote = "subnet", m.config.Prefix.String()
	}
	for i := range state.Events {
		if value := state.Events[i].MessageVersions; value != nil {
			copy := *value
			state.Events[i].MessageVersions = &copy
		}
		if value := state.Events[i].DisconnectData; value != nil {
			copy := *value
			state.Events[i].DisconnectData = &copy
		}
	}
	if m.started != nil {
		started := *m.started
		state.StartedAt = &started
	}
	for _, contact := range snapshot.Contacts {
		active := online && slices.Contains(snapshot.OnlineUserIDs, contact.UserID)
		reason := "该联系人当前没有已就绪连接"
		if !online {
			reason = "后台尚未上线"
		} else if active {
			reason = ""
		}
		state.Contacts = append(state.Contacts, ContactView{Contact: contact, Online: active, Sendable: active, Reason: reason})
	}
	for _, group := range snapshot.Groups {
		group.Members = append([]string{}, group.Members...)
		var groupPeer string
		for _, member := range group.Members {
			if member != snapshot.OwnUserID && slices.Contains(snapshot.OnlineUserIDs, member) {
				groupPeer = member
			}
		}
		reason := groupReason(snapshot.OwnUserID, groupPeer, group.Members)
		if !online {
			reason = "后台尚未上线"
		}
		state.Groups = append(state.Groups, GroupView{GroupInfo: group, Sendable: reason == "", Reason: reason})
	}
	return state
}

func groupReason(ownID, peerID string, members []string) string {
	if len(members) != 2 {
		return "当前群聊仅支持本机与一个已连接成员，尚未实现多成员群投递"
	}
	own, peer := false, false
	for _, member := range members {
		switch member {
		case ownID:
			own = true
		case peerID:
			peer = true
		default:
			return "群成员的协议连接尚未就绪"
		}
	}
	if !own || !peer {
		return "群成员未包含本机及当前对端"
	}
	return ""
}

// Send 提交一条完整请求，返回结果与HTTP状态；同一会话同requestID、目标和正文只调用协议一次。
// requestID复用但内容不同、缓存已满、未就绪或正文无效均拒绝；受理后不因浏览器断开丢弃最终事实，不自动重提。
func (m *Manager) Send(ctx context.Context, requestID string, request nativeproto.ChatRequest) (MessageResponse, int) {
	command := nativeproto.CommandUserMessage
	if request.GroupID != "" {
		command = nativeproto.CommandGroupMessage
	}
	response := MessageResponse{RequestID: requestID, Result: nativeproto.ChatResult{Command: command, Delivery: "unknown"}}
	if err := nativeproto.ValidateChatRequest(request); err != nil {
		response.Error = err.Error()
		return response, http.StatusUnprocessableEntity
	}
	if !validRequestID(requestID) {
		response.Error = "request_id须为8..128个ASCII字母、数字、连字符或下划线"
		return response, http.StatusBadRequest
	}
	if err := ctx.Err(); err != nil {
		response.Error = "请求在受理前已取消，未发送"
		return response, http.StatusRequestTimeout
	}
	hash := sha256.Sum256([]byte(fmt.Sprintf("%d\x00%s\x00%s\x00%s", command, request.UserID, request.GroupID, request.Text)))
	m.mu.Lock()
	run := m.current
	if run != nil {
		if previous, ok := run.ops[requestID]; ok {
			m.mu.Unlock()
			if previous.hash != hash {
				response.Error = "request_id已用于不同请求，不发送"
				return response, http.StatusConflict
			}
			select {
			case <-previous.done:
				return previous.response, previous.status
			case <-ctx.Done():
				response.Error = "请求仍由后台处理，请用相同request_id查询结果，不要生成新请求补发"
				return response, http.StatusRequestTimeout
			}
		}
	}
	if run == nil || run.client == nil || m.phase != "online" {
		m.mu.Unlock()
		response.Error = "后台尚未就绪或正在退出"
		return response, http.StatusConflict
	}
	if len(run.ops) == 1024 { // 保留已用标识，不淘汰后把重复请求误当成新发送；下一次明确上线才开启新范围。
		m.mu.Unlock()
		response.Error = "本会话已达到1024条网页请求预算，请先核实结果再正常下线上线"
		return response, http.StatusConflict
	}
	op := &operation{hash: hash, done: make(chan struct{})}
	run.ops[requestID] = op
	run.sends.Add(1)
	m.mu.Unlock()
	defer run.sends.Done()
	// 受理后使用后台时限，而非HTTP请求上下文；关闭网页不应中断一次已经开始的协议发送。
	sendCtx, cancel := context.WithTimeout(run.ctx, 20*time.Second)
	defer cancel()
	status := http.StatusOK
	target := request.UserID
	if request.GroupID != "" {
		target = request.GroupID
	}
	err := run.log.write(map[string]any{"event": "chat_request", "request_id": requestID, "command": command,
		"target_sha256":  fmt.Sprintf("%x", sha256.Sum256([]byte(target))),
		"message_sha256": fmt.Sprintf("%x", sha256.Sum256([]byte(request.Text))), "message_bytes": len(request.Text), "delivery": "unknown"})
	if err == nil {
		if request.GroupID != "" {
			response.Result, err = run.client.SendGroup(sendCtx, request.GroupID, request.Text)
		} else {
			response.Result, err = run.client.SendUser(sendCtx, request.UserID, request.Text)
		}
		if err != nil {
			status = http.StatusBadGateway
		}
	} else {
		status = http.StatusInternalServerError
		m.fail(run, err)
	}
	response.Error = errorText(err)
	if logErr := run.log.write(map[string]any{"event": "request_result", "response": response}); logErr != nil {
		response.Error = errorText(errors.Join(err, logErr))
		status = http.StatusInternalServerError
		m.fail(run, logErr)
	}
	op.response, op.status = response, status
	close(op.done)
	return response, status
}

func (m *Manager) fail(run *running, err error) {
	m.mu.Lock()
	run.fatal = errors.Join(run.fatal, err)
	m.phase = "stopping"
	run.cancel()
	m.mu.Unlock()
}

func validRequestID(id string) bool {
	if len(id) < 8 || len(id) > 128 {
		return false
	}
	for _, c := range id {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			return false
		}
	}
	return true
}

// cancellationOnly只忽略主动停止产生的纯取消；errors.Join附带的文件或socket关闭错误必须继续报告。
func cancellationOnly(err error) bool {
	if err == context.Canceled {
		return true
	}
	joined, ok := err.(interface{ Unwrap() []error })
	if !ok {
		return false
	}
	for _, cause := range joined.Unwrap() {
		if !cancellationOnly(cause) {
			return false
		}
	}
	return true
}

func errorText(err error) string {
	if err != nil {
		return err.Error()
	}
	return ""
}
