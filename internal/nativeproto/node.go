// 本文件编解码已验证的 304 字节发现/节点控制包，原样保存未知字段，不生成或推断本机身份。
package nativeproto

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"net/netip"
	"strings"
	"unicode/utf8"
)

const nodePacketSize = 304

// NodePacket 保存已验证的节点字段及原始模板；由 DecodeNodePacket 或已恢复默认路径的 NewNodePacket 创建。
// Type 为发现广播 1、发现响应 2 或 ENet 节点控制 4；UserID 是发送者标识，不是认证凭据。
type NodePacket struct {
	Type            uint32 `json:"type"`
	UserID          string `json:"user_id"`
	DiscoveryPort   uint16 `json:"discovery_port"`
	ENetPort        uint16 `json:"enet_port"`
	TCPPort         uint16 `json:"tcp_port"`
	ReverseENetPort uint16 `json:"reverse_enet_port"`
	raw             [nodePacketSize]byte
}

// DecodeNodePacket 从完整 data 读取已验证字段并复制原始字节，返回可修改、重新编码的节点包。
// 长度、类型、版本或 NUL 终止的 UTF-8 身份字段无效时返回错误；不读取配置，也不验证身份真实性。
func DecodeNodePacket(data []byte) (NodePacket, error) {
	if len(data) != nodePacketSize {
		return NodePacket{}, fmt.Errorf("节点包须为 %d 字节，实际 %d", nodePacketSize, len(data))
	}
	if binary.BigEndian.Uint32(data) != nodePacketSize || binary.BigEndian.Uint32(data[8:]) != 1 {
		return NodePacket{}, fmt.Errorf("节点包总长度或版本不受支持")
	}
	kind := binary.BigEndian.Uint32(data[4:])
	if !nodeTypeSupported(kind) {
		return NodePacket{}, fmt.Errorf("节点包类型 %d 不受支持", kind)
	}
	slot := data[0x1e : 0x1e+65]
	end := bytes.IndexByte(slot, 0)
	if end <= 0 || !utf8.Valid(slot[:end]) {
		return NodePacket{}, fmt.Errorf("节点身份须为非空、NUL 终止的有效 UTF-8")
	}
	packet := NodePacket{
		Type: kind, UserID: string(slot[:end]),
		DiscoveryPort:   binary.BigEndian.Uint16(data[0x18:]),
		ENetPort:        binary.BigEndian.Uint16(data[0x1a:]),
		TCPPort:         binary.BigEndian.Uint16(data[0x1c:]),
		ReverseENetPort: binary.BigEndian.Uint16(data[0xa5:]),
	}
	copy(packet.raw[:], data)
	return packet, nil
}

// Encode 返回新分配的节点包，更新已知字段并保留模板中的其他字节。
// 零值、未验证类型或不适合 65 字节 NUL 槽的 UserID 返回错误；不会默认生成未知字段或身份。
func (p NodePacket) Encode() ([]byte, error) {
	if binary.BigEndian.Uint32(p.raw[:]) != nodePacketSize || binary.BigEndian.Uint32(p.raw[8:]) != 1 {
		return nil, fmt.Errorf("节点包必须由 DecodeNodePacket 或 NewNodePacket 创建")
	}
	if !nodeTypeSupported(p.Type) {
		return nil, fmt.Errorf("节点包类型 %d 不受支持", p.Type)
	}
	if p.UserID == "" || len(p.UserID) > 64 || !utf8.ValidString(p.UserID) || strings.IndexByte(p.UserID, 0) >= 0 {
		return nil, fmt.Errorf("节点身份须为 1..64 字节有效 UTF-8，且不含 NUL")
	}
	data := append([]byte(nil), p.raw[:]...)
	binary.BigEndian.PutUint32(data[4:], p.Type)
	binary.BigEndian.PutUint16(data[0x18:], p.DiscoveryPort)
	binary.BigEndian.PutUint16(data[0x1a:], p.ENetPort)
	binary.BigEndian.PutUint16(data[0x1c:], p.TCPPort)
	binary.BigEndian.PutUint16(data[0xa5:], p.ReverseENetPort)
	slot := data[0x1e : 0x1e+65]
	originalID := string(slot[:bytes.IndexByte(slot, 0)])
	if originalID != p.UserID {
		clear(slot)
		copy(slot, p.UserID)
	}
	return data, nil
}

// NodeRegistration 是本机新节点的显式初始化字段；SessionGUID 是本次生命周期的新 Windows GUID 内存字节。
// UserID 与 SessionGUID 必须独立生成；默认 CorpNumber 为空，不复制对端身份或组织配置。
type NodeRegistration struct {
	UserID          string
	SessionGUID     [16]byte
	Address         netip.Addr
	Broadcast       netip.Addr
	DiscoveryPort   uint16
	ENetPort        uint16
	ReverseENetPort uint16
}

// NewNodePacket 按已恢复的默认新节点路径创建完整 304 字节包；注册字段无效或种类未知时返回错误。
// TCP/reverse TCP 端口为零，因为本实现未提供 TCP listener；尾部零字节来自原客户端显式初始化规则。
func NewNodePacket(registration NodeRegistration, kind uint32) (NodePacket, error) {
	if !registration.Address.Is4() || !registration.Broadcast.Is4() || registration.Address.IsUnspecified() ||
		registration.DiscoveryPort == 0 || registration.ENetPort == 0 || registration.SessionGUID == ([16]byte{}) {
		return NodePacket{}, fmt.Errorf("新节点须有 IPv4、实际发现/ENet 端口和非零独立会话 GUID")
	}
	data := make([]byte, nodePacketSize)
	binary.BigEndian.PutUint32(data, nodePacketSize)
	binary.BigEndian.PutUint32(data[4:], kind)
	binary.BigEndian.PutUint32(data[8:], 1)
	// 空 CorpNumber 集合的原生默认 FEATURE，见 0x657960；不是自行拼出的功能位。
	binary.BigEndian.PutUint32(data[12:], 0x5fc1d8ec)
	address, broadcast := registration.Address.As4(), registration.Broadcast.As4()
	copy(data[16:20], address[:])
	copy(data[20:24], broadcast[:])
	binary.BigEndian.PutUint16(data[0x18:], registration.DiscoveryPort)
	binary.BigEndian.PutUint16(data[0x1a:], registration.ENetPort)
	copy(data[0x5f:0x5f+16], registration.SessionGUID[:])
	// 原客户端 0x651cb1 明确初始化为 500；业务名称未恢复，按已有值编码而不虚构含义。
	binary.BigEndian.PutUint32(data[0xa0:], 500)
	binary.BigEndian.PutUint16(data[0xa5:], registration.ReverseENetPort)
	if registration.UserID == "" || len(registration.UserID) > 64 || !utf8.ValidString(registration.UserID) || strings.IndexByte(registration.UserID, 0) >= 0 {
		return NodePacket{}, fmt.Errorf("新节点身份须为 1..64 字节有效 UTF-8，且不含 NUL")
	}
	copy(data[0x1e:0x1e+65], registration.UserID)
	return DecodeNodePacket(data)
}

// SessionOrder 按原客户端 0x6503c0 比较完整会话字段，返回 -1/0/1；大的一方连接对方主端口。
// equal 表示同一会话，应过滤；端点或模板无效时返回错误，不按 IP 大小猜测角色。
func (p NodePacket) SessionOrder(other NodePacket) (int, error) {
	if _, err := p.Encode(); err != nil {
		return 0, err
	}
	if _, err := other.Encode(); err != nil {
		return 0, err
	}
	return bytes.Compare(p.raw[0x5f:0xa0], other.raw[0x5f:0xa0]), nil
}

// nodeTypeSupported 限制为已有采集依据的发现广播、响应和可靠节点控制，不猜测其他类型。
func nodeTypeSupported(kind uint32) bool {
	return kind == 1 || kind == 2 || kind == 4
}
