// 本文件为每次明确上线创建独立协议会话；默认单对端，显式Discover时使用共享Host正常发现并连接本子网节点。
package nativeweb

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"time"

	"neiwangtong-gateway/internal/nativeproto"
)

// session 是HTTP管理器与真实协议/合成测试之间的内部接口；socket始终仅由Run持有。
type session interface {
	Run(context.Context, func(nativeproto.ClientEvent) error) (nativeproto.ClientResult, error)
	Snapshot() nativeproto.ClientSnapshot
	SendUser(context.Context, string, string) (nativeproto.ChatResult, error)
	SendGroup(context.Context, string, string) (nativeproto.ChatResult, error)
}

type sessionFactory func(Config, string, *journal) (session, error)

func openNative(config Config, runID string, log *journal) (session, error) {
	ownID, err := nativeproto.LoadOrCreateIdentity(filepath.Join(config.DataDir, "identity"), rand.Reader)
	if err != nil {
		return nil, err
	}
	directory, err := loadDirectory(config.DataDir, ownID)
	if err != nil {
		return nil, err
	}
	guid, err := nativeproto.NewSessionGUID(rand.Reader)
	if err != nil {
		return nil, err
	}
	var nonce [4]byte
	if _, err := io.ReadFull(rand.Reader, nonce[:]); err != nil {
		return nil, err
	}
	if nonce == ([4]byte{}) {
		return nil, fmt.Errorf("ConnectID为未分配值，不重抽重试")
	}
	node, err := nativeproto.NewNodePacket(nativeproto.NodeRegistration{UserID: ownID, SessionGUID: guid,
		Address: config.Local, Broadcast: broadcast(config.Prefix), DiscoveryPort: config.DiscoveryPort, ENetPort: config.ENetPort}, 1)
	if err != nil {
		return nil, err
	}
	profile, err := nativeproto.ProbeHandshake(config.Name)
	if err != nil {
		return nil, err
	}
	for i := range profile.Features {
		if profile.Features[i].Name == "QGROUP" || profile.Features[i].Name == "QGROUP_MSG" {
			profile.Features[i].Value = "2:1"
		}
	}
	if err := log.write(map[string]any{"event": "own_identity", "user_id": ownID, "session_guid_hex": hex.EncodeToString(guid[:])}); err != nil {
		return nil, err
	}
	discoveryAddress := config.Local
	if config.Discover {
		discoveryAddress = netip.IPv4Unspecified() // Linux接收定向广播须绑定发现口的通配地址；Host仍按本机子网过滤来源。
	}
	discovery, err := net.ListenUDP("udp4", net.UDPAddrFromAddrPort(netip.AddrPortFrom(discoveryAddress, config.DiscoveryPort)))
	if err != nil {
		return nil, err
	}
	conn, err := net.ListenUDP("udp4", net.UDPAddrFromAddrPort(netip.AddrPortFrom(config.Local, config.ENetPort)))
	if err != nil {
		return nil, errors.Join(err, discovery.Close())
	}
	var directorySequence uint64
	cfg := nativeproto.ClientConfig{
		Node: node, Profile: profile, RemoteDiscovery: config.Remote, Contacts: directory, LearnContacts: true, GroupChat: true,
		ObserveDirectory: func(directory *nativeproto.Directory) error {
			directorySequence++ // Client/Host契约保证目录回调串行；同一次上线可学到多个联系人，逐份保留新快照。
			return saveDirectory(filepath.Join(config.DataDir, fmt.Sprintf("directory-%s-%08d.json", runID, directorySequence)), directory)
		},
		ObserveGroup: func(group nativeproto.GroupInfo) error {
			return log.write(map[string]any{"event": "group_snapshot", "group": group})
		},
		Transport: nativeproto.ENetConfig{LocalPeerID: 0, ConnectID: nonce, MTU: 1400, WindowBytes: 65536,
			MaxMessageBytes: 1 << 20, MaxQueuedBytes: 4 << 20, MaxPendingCommands: 1024,
			RetransmitAfter: 500 * time.Millisecond, MaxAttempts: 6, ReceiveTimeout: 120 * time.Second, PingInterval: 30 * time.Second},
		Duration: config.SessionDuration, ShutdownTimeout: 5 * time.Second, DiscoveryTimeout: 10 * time.Second, HandshakeTimeout: 15 * time.Second,
	}
	var client session
	if config.Discover {
		hostConfig := nativeproto.HostConfig{Client: cfg, Prefix: config.Prefix.Masked(),
			Broadcast: netip.AddrPortFrom(broadcast(config.Prefix), config.DiscoveryPort), MaxPeers: 32}
		if config.Remote.IsValid() {
			hostConfig.Seeds = []netip.AddrPort{config.Remote}
		}
		client, err = nativeproto.NewHost(discovery, conn, hostConfig)
	} else {
		client, err = nativeproto.NewClient(discovery, conn, cfg)
	}
	if err != nil {
		return nil, errors.Join(err, discovery.Close(), conn.Close())
	}
	return client, nil
}

// readExistingIdentity 不存在时返回空身份；已有损坏身份不自动重建，且只读模式不使用随机源。
func readExistingIdentity(path string) (string, error) {
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return "", nil
	} else if err != nil {
		return "", err
	}
	// 如果检查后文件消失，空随机源令LoadOrCreateIdentity在创建文件前明确失败。
	return nativeproto.LoadOrCreateIdentity(path, bytes.NewReader(nil))
}
