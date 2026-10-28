// 本文件在两个真实UDP socket上管理多个独立Client；集中分派数据，对已知离线端点重新发现，不重提消息。
package nativeproto

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

// HostConfig 指定同一独立身份的网络范围和容量；Broadcast为零时仅使用显式Seeds或被动发现。
// Prefix限制来源；Broadcast非零时必须是该前缀的定向广播及本机发现端口，MaxPeers为1..256。
// Client.RemoteDiscovery由每个发现端点替换，Duration是整个Host的运行预算，不为每个peer重新计时。
type HostConfig struct {
	Client    ClientConfig
	Prefix    netip.Prefix
	Broadcast netip.AddrPort
	Seeds     []netip.AddrPort
	MaxPeers  int
}

// 离线端点从一秒开始指数退避，避免持续离线时频繁发送发现包。
const hostReconnectInterval = time.Second

type hostPeer struct {
	node            NodePacket
	client          *Client
	discovery, conn *hostPacketConn
	cancel          context.CancelFunc
	stopped         bool
	index           uint16
	result          ClientResult
	err             error
	seenGroups      map[string]GroupInfo
	retryAt         time.Time
	retryDelay      time.Duration
	retryPending    bool
}

type hostGroup struct {
	info    GroupInfo
	owner   string
	invalid bool
}

// Host 拥有两个真实socket及至多MaxPeers个单对端Client，所有外部观察/持久化回调串行调用。
// Snapshot可从回调中调用；发送必须从回调之外调用，每个Client仍由自己的Run串行处理网络。
type Host struct {
	config          HostConfig
	discovery, conn *net.UDPConn
	local           netip.Addr
	lifecycle       atomic.Uint32
	mu              sync.Mutex
	accepting       bool
	closing         bool
	peers           map[netip.AddrPort]*hostPeer
	enet            map[netip.AddrPort]*hostPeer
	identities      map[string]*hostPeer
	directory       *Directory
	groups          map[string]hostGroup
	incoming        incomingCache
	groupSends      chan struct{} // 串行群请求以保持各成员消息顺序，等待发送权时仍可响应取消。
	callbackMu      sync.Mutex
	observe         func(ClientEvent) error
	origin          time.Time
	fatal           chan error
	fatalOnce       sync.Once
	peerWG          sync.WaitGroup
	history         ClientResult
	historyFailed   bool
	nextPeerID      uint16
}

// NewHost 只验证配置和已绑定socket，返回尚未发包的Host；构造失败不会关闭传入socket。
// discovery可绑定0.0.0.0或公告IP，conn须绑定公告IP；端口、前缀、广播、种子或Client契约错误时返回错误。
func NewHost(discovery, conn *net.UDPConn, cfg HostConfig) (*Host, error) {
	if discovery == nil || conn == nil || discovery == conn || discovery.RemoteAddr() != nil || conn.RemoteAddr() != nil {
		return nil, fmt.Errorf("Host需要两个独立、未连接的UDP socket")
	}
	wire, err := cfg.Client.Node.Encode()
	if err != nil {
		return nil, err
	}
	local := netip.AddrFrom4([4]byte{wire[16], wire[17], wire[18], wire[19]})
	if !cfg.Prefix.IsValid() || !cfg.Prefix.Addr().Is4() || cfg.Prefix != cfg.Prefix.Masked() || cfg.Prefix.Bits() > 30 || !cfg.Prefix.Contains(local) || cfg.MaxPeers < 1 || cfg.MaxPeers > 256 {
		return nil, fmt.Errorf("Host需要包含公告IPv4的规范前缀和1..256个peer容量")
	}
	d := discovery.LocalAddr().(*net.UDPAddr).AddrPort()
	e := conn.LocalAddr().(*net.UDPAddr).AddrPort()
	if !d.Addr().Unmap().Is4() || (!d.Addr().IsUnspecified() && d.Addr().Unmap() != local) || d.Port() != cfg.Client.Node.DiscoveryPort || e.Addr().Unmap() != local || e.Port() != cfg.Client.Node.ENetPort {
		return nil, fmt.Errorf("Host两个socket必须匹配公告IP及发现/ENet端口")
	}
	broadcast := hostBroadcast(cfg.Prefix)
	if cfg.Broadcast.IsValid() && (cfg.Broadcast.Addr() != broadcast || cfg.Broadcast.Port() != cfg.Client.Node.DiscoveryPort) {
		return nil, fmt.Errorf("只允许本前缀定向广播及公告发现端口")
	}
	seen := make(map[netip.AddrPort]bool)
	for _, seed := range cfg.Seeds {
		if !hostUnicast(seed, cfg.Prefix, local) || seen[seed] {
			return nil, fmt.Errorf("种子必须是前缀内非本机、互不重复的明确单播端点")
		}
		seen[seed] = true
	}
	if len(cfg.Seeds) > cfg.MaxPeers || cfg.Client.Chat != nil {
		return nil, fmt.Errorf("种子超出peer容量或配置了单对端启动Chat；Host发送应使用显式SendUser")
	}
	validation := cfg.Client
	validation.RemoteDiscovery = netip.AddrPortFrom(local, cfg.Client.Node.DiscoveryPort)
	validation.Transport.LocalPeerID = 0
	validation.Transport.ConnectID = [4]byte{1}
	vd := newHostPacketConn(discovery, netip.AddrPortFrom(local, d.Port()), validation.RemoteDiscovery)
	ve := newHostPacketConn(conn, e, netip.AddrPortFrom(local, e.Port()))
	if _, err := newClient(vd, ve, validation); err != nil {
		return nil, err
	}
	cfg.Seeds = append([]netip.AddrPort(nil), cfg.Seeds...)
	h := &Host{config: cfg, discovery: discovery, conn: conn, local: local, groupSends: make(chan struct{}, 1), peers: make(map[netip.AddrPort]*hostPeer), enet: make(map[netip.AddrPort]*hostPeer), identities: make(map[string]*hostPeer), groups: make(map[string]hostGroup), fatal: make(chan error, 1), history: ClientResult{UnhandledCommands: make(map[uint32]int)}}
	if cfg.Client.Contacts != nil {
		h.directory = &Directory{ownID: cfg.Client.Node.UserID, contacts: cfg.Client.Contacts.ListContacts()}
	}
	return h, nil
}

func hostBroadcast(prefix netip.Prefix) netip.Addr {
	address := prefix.Addr().As4()
	for bit := prefix.Bits(); bit < 32; bit++ {
		address[bit/8] |= 1 << (7 - uint(bit%8))
	}
	return netip.AddrFrom4(address)
}

func hostUnicast(endpoint netip.AddrPort, prefix netip.Prefix, local netip.Addr) bool {
	address := endpoint.Addr()
	return endpoint.IsValid() && endpoint.Port() != 0 && address.Is4() && prefix.Contains(address) && address != local && address != prefix.Addr() && address != hostBroadcast(prefix) && !address.IsUnspecified() && !address.IsMulticast()
}

// Run 接管socket且只能调用一次；在可接受发现时发protocol_ready，表示Host就绪，不表示有peer或接收端UI已确认。
// 每个peer的双向READY改发peer_ready；peer停止后重新发现已知端点，不重发消息；observer/持久化错误停止整个Host。
// ctx取消或Duration到期时同时停止所有peer，等待各自QUIT/断开后关闭真实socket；回调与I/O失败返回错误。
func (h *Host) Run(ctx context.Context, observe func(ClientEvent) error) (ClientResult, error) {
	if !h.lifecycle.CompareAndSwap(clientIdle, clientRunning) {
		return ClientResult{}, fmt.Errorf("Host运行不能重用")
	}
	h.origin = time.Now()
	h.observe = observe
	result := ClientResult{Role: "host", UnhandledCommands: make(map[uint32]int)}
	peerCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	var timer *time.Timer
	if h.config.Client.Duration > 0 {
		timer = time.AfterFunc(h.config.Client.Duration, cancel)
		defer timer.Stop()
	}
	var readers sync.WaitGroup
	var runErr error
	if observe == nil {
		runErr = fmt.Errorf("Host运行须显式提供阶段记录回调")
	} else {
		h.mu.Lock()
		h.accepting = true
		h.mu.Unlock()
		if runErr = h.emit(ClientEvent{Stage: "protocol_ready", Role: "host"}); runErr == nil {
			result.ProtocolReady = true
			readers.Add(3)
			go func() { defer readers.Done(); h.readPackets(peerCtx, h.discovery, true) }()
			go func() { defer readers.Done(); h.readPackets(peerCtx, h.conn, false) }()
			go func() { defer readers.Done(); h.reconnectPeers(peerCtx) }()
			wire, _ := h.config.Client.Node.Encode()
			targets := append([]netip.AddrPort(nil), h.config.Seeds...)
			if h.config.Broadcast.IsValid() {
				targets = append(targets, h.config.Broadcast)
			}
			for _, target := range targets {
				if peerCtx.Err() != nil {
					break
				}
				result.DiscoveryAttempts++
				// 使用临时虚拟端点，不能把Host发现时限留在真实socket上影响各peer后续响应。
				out := newHostPacketConn(h.discovery, netip.AddrPortFrom(h.local, h.config.Client.Node.DiscoveryPort), target)
				_ = out.SetWriteDeadline(time.Now().Add(h.config.Client.DiscoveryTimeout))
				n, err := out.WriteToUDPAddrPort(wire, target)
				_ = out.Close()
				if err != nil || n != len(wire) {
					if err == nil {
						err = io.ErrShortWrite
					}
					runErr = fmt.Errorf("Host单次发现写出失败，不重发: %w", err)
					break
				}
				result.DiscoverySends++
			}
			if runErr == nil && h.config.Broadcast.IsValid() {
				// 上线时连续三次间隔一秒公告，减少单次UDP广播或响应丢失造成的遗漏。
				ticker := time.NewTicker(time.Second)
				broadcasting := true
				for broadcasting && result.DiscoverySends < len(targets)+2 && runErr == nil {
					select {
					case <-peerCtx.Done():
						broadcasting = false
					case runErr = <-h.fatal:
					case <-ticker.C:
						out := newHostPacketConn(h.discovery, netip.AddrPortFrom(h.local, h.config.Client.Node.DiscoveryPort), h.config.Broadcast)
						_ = out.SetWriteDeadline(time.Now().Add(h.config.Client.DiscoveryTimeout))
						result.DiscoveryAttempts++
						n, err := out.WriteToUDPAddrPort(wire, h.config.Broadcast)
						_ = out.Close()
						if err != nil || n != len(wire) {
							if err == nil {
								err = io.ErrShortWrite
							}
							runErr = fmt.Errorf("Host发现广播写出失败: %w", err)
						} else {
							result.DiscoverySends++
						}
					}
				}
				ticker.Stop()
			}
			if runErr == nil {
				select {
				case <-peerCtx.Done():
				case runErr = <-h.fatal:
				}
			}
		}
	}
	h.mu.Lock()
	h.accepting = false
	cancel()
	h.mu.Unlock()
	h.peerWG.Wait() // 读循环仍运行，退出握手所需ACK不能因Host取消而提前丢失。
	h.mu.Lock()
	h.closing = true
	h.mu.Unlock()
	closeErr := errors.Join(h.discovery.Close(), h.conn.Close())
	readers.Wait()
	h.lifecycle.Store(clientStopped)
	result.GracefulExit = result.ProtocolReady
	h.mu.Lock()
	result.DiscoveryAttempts += h.history.DiscoveryAttempts
	result.DiscoverySends += h.history.DiscoverySends
	result.ChatSends += h.history.ChatSends
	for command, count := range h.history.UnhandledCommands {
		result.UnhandledCommands[command] += count
	}
	if h.historyFailed {
		result.GracefulExit = false
	}
	for _, peer := range h.peers {
		result.DiscoveryAttempts += peer.result.DiscoveryAttempts
		result.DiscoverySends += peer.result.DiscoverySends
		result.ChatSends += peer.result.ChatSends
		for command, count := range peer.result.UnhandledCommands {
			result.UnhandledCommands[command] += count
		}
		if !peer.result.GracefulExit || peer.err != nil {
			result.GracefulExit = false
		}
	}
	h.mu.Unlock()
	if observe != nil {
		runErr = errors.Join(runErr, h.emit(ClientEvent{Stage: "stopped", Role: "host"}))
	}
	select {
	case err := <-h.fatal:
		runErr = errors.Join(runErr, err)
	default:
	}
	if ctx.Err() != nil && !errors.Is(ctx.Err(), context.Canceled) {
		runErr = errors.Join(runErr, ctx.Err())
	}
	return result, errors.Join(runErr, closeErr)
}

func (h *Host) fail(err error) { h.fatalOnce.Do(func() { h.fatal <- err }) }

func (h *Host) emit(event ClientEvent) error {
	h.callbackMu.Lock()
	defer h.callbackMu.Unlock()
	return h.emitLocked(event)
}

func (h *Host) emitLocked(event ClientEvent) error {
	event.ElapsedMS = time.Since(h.origin).Milliseconds()
	if err := h.observe(event); err != nil {
		h.fail(err)
		return err
	}
	return nil
}

func (h *Host) readPackets(ctx context.Context, conn *net.UDPConn, discovery bool) {
	buffer := make([]byte, enetMaxDatagram)
	for {
		n, source, err := conn.ReadFromUDPAddrPort(buffer)
		if err != nil {
			h.mu.Lock()
			closing := h.closing
			h.mu.Unlock()
			if !closing {
				h.fail(err)
			}
			return
		}
		source = netip.AddrPortFrom(source.Addr().Unmap(), source.Port())
		if discovery {
			h.distributeDiscovery(ctx, buffer[:n], source)
			continue
		}
		h.mu.Lock()
		peer := h.enet[source]
		h.mu.Unlock()
		if peer != nil {
			if err := peer.conn.deliver(buffer[:n], source); err != nil {
				h.stopPeer(peer, err)
			}
		}
	}
}

// reconnectPeers只向本次会话已经发现的离线端点单播；指数间隔限制对长期离线节点的请求频率。
func (h *Host) reconnectPeers(ctx context.Context) {
	wire, _ := h.config.Client.Node.Encode()
	ticker := time.NewTicker(hostReconnectInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			type retry struct {
				endpoint netip.AddrPort
				index    uint16
			}
			var due []retry
			h.mu.Lock()
			for endpoint, peer := range h.peers {
				if !h.accepting || !peer.stopped || now.Before(peer.retryAt) {
					continue
				}
				peer.retryPending = true
				peer.retryAt = now.Add(peer.retryDelay)
				peer.retryDelay = min(peer.retryDelay*2, time.Minute)
				due = append(due, retry{endpoint, peer.index})
			}
			h.mu.Unlock()
			for _, item := range due {
				if ctx.Err() != nil {
					return
				}
				if err := h.emit(ClientEvent{Stage: "reconnect_discovery", Role: "host", PeerIndex: item.index, Command: 1}); err != nil {
					return
				}
				out := newHostPacketConn(h.discovery, netip.AddrPortFrom(h.local, h.config.Client.Node.DiscoveryPort), item.endpoint)
				_ = out.SetWriteDeadline(time.Now().Add(time.Second))
				h.mu.Lock()
				h.history.DiscoveryAttempts++
				h.mu.Unlock()
				n, err := out.WriteToUDPAddrPort(wire, item.endpoint)
				_ = out.Close()
				if err == nil && n != len(wire) {
					err = io.ErrShortWrite
				}
				if err != nil {
					_ = h.emit(ClientEvent{Stage: "reconnect_discovery_failed", Role: "host", PeerIndex: item.index, Error: err.Error()})
				} else {
					h.mu.Lock()
					h.history.DiscoverySends++
					h.mu.Unlock()
				}
			}
		}
	}
}

func (h *Host) stopPeer(peer *hostPeer, err error) {
	if errors.Is(err, net.ErrClosed) {
		return
	} // Client正常退出先关虚拟口，晚到的包不改写退出结果。
	h.mu.Lock()
	if peer.stopped {
		h.mu.Unlock()
		return
	}
	if peer.err == nil {
		peer.err = err
	}
	h.mu.Unlock()
	peer.cancel()
}

func (h *Host) distributeDiscovery(ctx context.Context, data []byte, source netip.AddrPort) {
	if !hostUnicast(source, h.config.Prefix, h.local) {
		return
	}
	node, err := DecodeNodePacket(data)
	if err == nil {
		announced := netip.AddrFrom4([4]byte{node.raw[16], node.raw[17], node.raw[18], node.raw[19]})
		switch {
		case node.Type != 1 && node.Type != 2:
			err = fmt.Errorf("发现包类型%d不受支持", node.Type)
		case node.DiscoveryPort != source.Port():
			err = fmt.Errorf("发现公告端口%d与实际源端口%d不符", node.DiscoveryPort, source.Port())
		case node.ENetPort == 0:
			err = fmt.Errorf("发现包未公告ENet端口")
		// 原厂广播响应可能公告0.0.0.0，实际连接仍使用受Prefix限制的UDP来源，不向零地址连接。
		case !announced.IsUnspecified() && announced != source.Addr():
			err = fmt.Errorf("发现公告地址%s与实际源地址%s不符", announced, source.Addr())
		case node.UserID == h.config.Client.Node.UserID:
			err = fmt.Errorf("发现包使用了本机身份")
		case !sharedNodeFeature(h.config.Client.Node, node):
			err = fmt.Errorf("发现FEATURE与本机不符")
		}
	}
	if err == nil {
		var order int
		order, err = node.SessionOrder(h.config.Client.Node)
		if order == 0 && err == nil {
			err = fmt.Errorf("发现包反射本机会话")
		}
	}
	if err != nil {
		_ = h.emit(ClientEvent{Stage: "discovery_rejected", Role: "host", Error: err.Error()})
		return
	}
	h.mu.Lock()
	if !h.accepting {
		h.mu.Unlock()
		return
	}
	previousSource := source
	previous := h.peers[source]
	if previous == nil {
		if known := h.identities[node.UserID]; known != nil && known.stopped {
			previous = known
			previousSource = known.discovery.remote
		}
	}
	if previous != nil && previous.stopped && !previous.retryPending && time.Now().Before(previous.retryAt) {
		h.mu.Unlock()
		return
	}
	if previous != nil && !previous.stopped {
		order, _ := previous.node.SessionOrder(node)
		if previous.node.UserID != node.UserID || previous.node.ENetPort != node.ENetPort || order != 0 {
			h.mu.Unlock()
			_ = h.emit(ClientEvent{Stage: "discovery_rejected", Role: "host"})
			return
		}
		h.mu.Unlock()
		if err := previous.discovery.deliver(data, source); err != nil {
			h.stopPeer(previous, err)
		}
		return
	}
	remote := netip.AddrPortFrom(source.Addr(), node.ENetPort)
	if previous != nil && previous.node.UserID != node.UserID ||
		previous == nil && len(h.peers) >= h.config.MaxPeers ||
		h.identities[node.UserID] != nil && h.identities[node.UserID] != previous ||
		h.enet[remote] != nil && h.enet[remote] != previous {
		h.mu.Unlock()
		_ = h.emit(ClientEvent{Stage: "discovery_rejected", Role: "host"})
		return
	}
	cfg := h.config.Client
	cfg.RemoteDiscovery = source
	cfg.Duration = 0
	index := uint16(len(h.peers) + 1)
	if previous != nil {
		index = previous.index
	}
	// 新一代使用不同的本地peer ID；ConnectID只出现在握手中，不能单独阻止旧数据包进入新会话。
	for {
		cfg.Transport.LocalPeerID = h.nextPeerID
		h.nextPeerID = (h.nextPeerID + 1) % 0x0fff
		used := false
		for _, candidate := range h.peers {
			if candidate.client.config.Transport.LocalPeerID == cfg.Transport.LocalPeerID {
				used = true
				break
			}
		}
		if !used {
			break
		}
	}
	if _, err := rand.Read(cfg.Transport.ConnectID[:]); err != nil {
		h.mu.Unlock()
		h.fail(err)
		return
	}
	if cfg.Transport.ConnectID == ([4]byte{}) {
		h.mu.Unlock()
		h.fail(fmt.Errorf("Host生成了未分配ConnectID，不重抽重试"))
		return
	}
	if h.directory != nil {
		cfg.Contacts = &Directory{ownID: h.directory.ownID, contacts: h.directory.ListContacts()}
	}
	peerCtx, cancel := context.WithCancel(ctx)
	peer := &hostPeer{node: node, index: index, cancel: cancel, seenGroups: make(map[string]GroupInfo)}
	if previous != nil {
		peer.retryAt, peer.retryDelay = previous.retryAt, previous.retryDelay
	}
	peer.discovery = newHostPacketConn(h.discovery, netip.AddrPortFrom(h.local, cfg.Node.DiscoveryPort), source)
	peer.conn = newHostPacketConn(h.conn, netip.AddrPortFrom(h.local, cfg.Node.ENetPort), remote)
	cfg.ObserveDirectory = func(directory *Directory) error { return h.learnDirectory(peer.node.UserID, directory) }
	cfg.ObserveGroup = func(group GroupInfo) error { return h.learnGroup(peer, group) }
	if cfg.ReceiveMessage != nil {
		cfg.ReceiveMessage = func(message IncomingMessage) error {
			h.callbackMu.Lock()
			defer h.callbackMu.Unlock()
			if h.config.Client.ReceiveMessage(message) != nil {
				err := fmt.Errorf("接收消息回调失败")
				h.fail(err)
				return err
			}
			return nil
		}
	}
	peer.client, err = newClient(peer.discovery, peer.conn, cfg)
	if err == nil {
		peer.client.incoming = &h.incoming
		peer.client.incomingGroup = h.incomingMember
		err = peer.discovery.deliver(data, source)
	}
	if err != nil {
		cancel()
		h.mu.Unlock()
		h.fail(err)
		return
	}
	if previous != nil {
		h.history.DiscoveryAttempts += previous.result.DiscoveryAttempts
		h.history.DiscoverySends += previous.result.DiscoverySends
		h.history.ChatSends += previous.result.ChatSends
		for command, count := range previous.result.UnhandledCommands {
			h.history.UnhandledCommands[command] += count
		}
		if !previous.result.GracefulExit || previous.err != nil {
			h.historyFailed = true
		}
		delete(h.peers, previousSource)
		delete(h.enet, netip.AddrPortFrom(previousSource.Addr(), previous.node.ENetPort))
	}
	h.peers[source], h.enet[remote], h.identities[node.UserID] = peer, peer, peer
	h.peerWG.Add(1)
	h.mu.Unlock()
	go func() {
		defer h.peerWG.Done()
		defer cancel()
		result, err := peer.client.Run(peerCtx, func(event ClientEvent) error { return h.peerEvent(peer, event) })
		h.mu.Lock()
		err = errors.Join(peer.err, err)
		peer.result, peer.err = result, err
		h.mu.Unlock()
		if err != nil {
			_ = h.emit(ClientEvent{Stage: "peer_failed", Role: result.Role, PeerIndex: peer.index, Error: err.Error()})
		}
		h.mu.Lock()
		peer.stopped = true
		peer.retryPending = false
		if peer.retryDelay == 0 {
			peer.retryDelay = hostReconnectInterval
		}
		if !peer.retryAt.After(time.Now()) {
			peer.retryAt = time.Now().Add(peer.retryDelay)
		}
		h.mu.Unlock()
	}()
}

func (h *Host) peerEvent(peer *hostPeer, event ClientEvent) error {
	event.PeerIndex = peer.index // 1..256为Host内部序号，不记录远端原始身份；0保留给Host事件。
	h.callbackMu.Lock()
	defer h.callbackMu.Unlock()
	snapshot := peer.client.Snapshot()
	h.mu.Lock()
	current := make(map[string]GroupInfo, len(snapshot.Groups))
	for _, group := range snapshot.Groups {
		current[group.GroupID] = group
	}
	for id, previous := range peer.seenGroups {
		if _, ok := current[id]; !ok {
			group := h.groups[id]
			if previous.UserVersion >= group.info.UserVersion {
				group.info, group.owner, group.invalid = previous, peer.node.UserID, true
				h.groups[id] = group
			}
		}
	}
	peer.seenGroups = current
	if event.Stage == "protocol_ready" {
		peer.retryAt, peer.retryDelay = time.Time{}, 0
	}
	h.mu.Unlock()
	if event.Stage == "protocol_ready" {
		event.Stage = "peer_ready"
	}
	if event.Stage == "stopped" {
		event.Stage = "peer_stopped"
	}
	return h.emitLocked(event)
}

func (h *Host) learnDirectory(peerID string, incoming *Directory) error {
	h.callbackMu.Lock()
	defer h.callbackMu.Unlock()
	learned, err := incoming.ContactByID(peerID)
	if err != nil {
		h.fail(err)
		return err
	}
	h.mu.Lock()
	merged := make(map[string]Contact)
	if h.directory != nil {
		for _, contact := range h.directory.ListContacts() {
			merged[contact.UserID] = contact
		}
	}
	// 单Client只新学当前peer；其余条目可能是构造时旧目录，不能覆盖其他Client刚学到的资料。
	merged[peerID] = learned
	contacts := make([]Contact, 0, len(merged))
	for _, contact := range merged {
		contacts = append(contacts, contact)
	}
	sort.Slice(contacts, func(i, j int) bool { return contacts[i].UserID < contacts[j].UserID })
	h.mu.Unlock()
	directory, err := NewDirectory(h.config.Client.Node.UserID, contacts)
	if err == nil && h.config.Client.ObserveDirectory != nil {
		err = h.config.Client.ObserveDirectory(directory)
	}
	if err != nil {
		h.fail(err)
		return err
	}
	h.mu.Lock()
	h.directory = directory
	h.mu.Unlock()
	return nil
}

func (h *Host) learnGroup(peer *hostPeer, incoming GroupInfo) error {
	h.callbackMu.Lock()
	defer h.callbackMu.Unlock()
	incoming.Members = append([]string(nil), incoming.Members...)
	if h.config.Client.ObserveGroup != nil {
		snapshot := incoming
		snapshot.Members = append([]string(nil), incoming.Members...)
		if err := h.config.Client.ObserveGroup(snapshot); err != nil {
			h.fail(err)
			return err
		}
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	previous, exists := h.groups[incoming.GroupID]
	// 已失效的成员版本必须被更高成员版本替换，其他peer的旧完整快照不能重新开放发送。
	if exists && previous.invalid && incoming.UserVersion <= previous.info.UserVersion {
		return nil
	}
	newer := incoming.UserVersion > previous.info.UserVersion || incoming.UserVersion == previous.info.UserVersion && incoming.InfoVersion > previous.info.InfoVersion
	equal := incoming.UserVersion == previous.info.UserVersion && incoming.InfoVersion == previous.info.InfoVersion
	if !exists || newer || equal && ((!previous.invalid && peer.node.UserID <= previous.owner) || peer.node.UserID == previous.owner) {
		h.groups[incoming.GroupID] = hostGroup{info: incoming, owner: peer.node.UserID}
	}
	return nil
}
