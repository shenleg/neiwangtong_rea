// 本文件编解码无压缩、无校验和扩展的 ENet datagram，保留命令控制字段，不建立 socket。
package nativeproto

import (
	"encoding/binary"
	"fmt"
)

// ENetPacket 是一个方向上的 ENet 包；SentTime 为 nil 时不写 sent-time 头，不包含 UDP/IP 头。
// PeerID、SessionID、Sequence 均属于本次传输会话，不能当作用户标识或认证结果。
type ENetPacket struct {
	PeerID    uint16        `json:"peer_id"`
	SessionID uint8         `json:"session_id"`
	SentTime  *uint16       `json:"sent_time,omitempty"`
	Commands  []ENetCommand `json:"commands"`
}

// ENetCommand 保存公共命令头；Kind 使用 ENet 的 1..12 编号。
// 数据命令的 Data 不含长度字段；Control 仅保存 ACK/建连/断开/带宽等命令的固定控制正文。
// SecondarySequence 仅用于 7/9，Fragment 仅用于 8/12；其他组合在编码时拒绝，避免静默丢字段。
type ENetCommand struct {
	Kind              uint8         `json:"kind"`
	AckRequired       bool          `json:"ack_required"`
	Unsequenced       bool          `json:"unsequenced"`
	Channel           uint8         `json:"channel"`
	Sequence          uint16        `json:"sequence"`
	Control           []byte        `json:"control,omitempty"`
	Data              []byte        `json:"data,omitempty"`
	SecondarySequence uint16        `json:"secondary_sequence,omitempty"`
	Fragment          *ENetFragment `json:"fragment,omitempty"`
}

// ENetFragment 是可靠或不可靠分片的定位字段；Data 的长度由所属 ENetCommand 提供。
// Offset 按整个重组数据的字节定位，StartSequence 不是业务 MSG_ID。
type ENetFragment struct {
	StartSequence uint16 `json:"start_sequence"`
	Count         uint32 `json:"count"`
	Number        uint32 `json:"number"`
	TotalLength   uint32 `json:"total_length"`
	Offset        uint32 `json:"offset"`
}

const (
	// 本离线模块的输入预算，不是业务正文或厂商产品长度上限。
	enetMaxDatagram = 65535
	enetMaxCommands = 32
)

// DecodeENet 解析完整 UDP payload 并复制命令数据，返回一个无压缩 ENet 包。
// 截断、未知命令、保留标志、压缩标志、错误分片或超过本模块解析预算时返回错误。
// 当前格式不包含 ENet checksum 扩展；不会通过扫描偏移或忽略尾字节猜测包格式。
func DecodeENet(data []byte) (ENetPacket, error) {
	if len(data) < 2 || len(data) > enetMaxDatagram {
		return ENetPacket{}, fmt.Errorf("ENet datagram 长度须为 2..%d", enetMaxDatagram)
	}
	header := binary.BigEndian.Uint16(data)
	if header&0x4000 != 0 {
		return ENetPacket{}, fmt.Errorf("不支持压缩 ENet 包")
	}
	packet := ENetPacket{PeerID: header & 0x0fff, SessionID: uint8(header >> 12 & 3)}
	offset := 2
	if header&0x8000 != 0 {
		if len(data) < 4 {
			return ENetPacket{}, fmt.Errorf("ENet sent-time 头截断")
		}
		time := binary.BigEndian.Uint16(data[2:])
		packet.SentTime = &time
		offset = 4
	}
	for offset < len(data) {
		if len(packet.Commands) == enetMaxCommands || len(data)-offset < 4 {
			return ENetPacket{}, fmt.Errorf("ENet 命令头截断或命令数超过 %d", enetMaxCommands)
		}
		flags := data[offset]
		if flags&0x30 != 0 {
			return ENetPacket{}, fmt.Errorf("ENet 命令含未支持的标志 %#x", flags)
		}
		command := ENetCommand{Kind: flags & 15, AckRequired: flags&0x80 != 0, Unsequenced: flags&0x40 != 0,
			Channel: data[offset+1], Sequence: binary.BigEndian.Uint16(data[offset+2:])}
		offset += 4
		fixed, err := enetBodyLength(command.Kind)
		if err != nil {
			return ENetPacket{}, err
		}
		if len(data)-offset < fixed {
			return ENetPacket{}, fmt.Errorf("ENet 命令 %d 正文截断", command.Kind)
		}
		body := data[offset : offset+fixed]
		offset += fixed
		length := 0
		switch command.Kind {
		case 6:
			length = int(binary.BigEndian.Uint16(body))
		case 7, 9:
			command.SecondarySequence = binary.BigEndian.Uint16(body)
			length = int(binary.BigEndian.Uint16(body[2:]))
		case 8, 12:
			length = int(binary.BigEndian.Uint16(body[2:]))
			command.Fragment = &ENetFragment{StartSequence: binary.BigEndian.Uint16(body),
				Count: binary.BigEndian.Uint32(body[4:]), Number: binary.BigEndian.Uint32(body[8:]),
				TotalLength: binary.BigEndian.Uint32(body[12:]), Offset: binary.BigEndian.Uint32(body[16:])}
		default:
			command.Control = append([]byte(nil), body...)
		}
		if length > len(data)-offset {
			return ENetPacket{}, fmt.Errorf("ENet 命令 %d 数据截断", command.Kind)
		}
		if command.Kind == 6 || command.Kind == 7 || command.Kind == 8 || command.Kind == 9 || command.Kind == 12 {
			command.Data = append([]byte(nil), data[offset:offset+length]...)
			offset += length
			if err := validateFragment(command); err != nil {
				return ENetPacket{}, err
			}
		}
		packet.Commands = append(packet.Commands, command)
	}
	if len(packet.Commands) == 0 {
		return ENetPacket{}, fmt.Errorf("ENet 包没有命令")
	}
	return packet, nil
}

// EncodeENet 将 packet 编为新 datagram，不包含 UDP 头；保留控制正文和各命令顺序。
// 无效字段组合、截断控制正文、分片越界或本模块不支持的包大小/标志返回错误；不发送数据。
func EncodeENet(packet ENetPacket) ([]byte, error) {
	if packet.PeerID > 0x0fff || packet.SessionID > 3 || len(packet.Commands) == 0 || len(packet.Commands) > enetMaxCommands {
		return nil, fmt.Errorf("ENet peer/session 或命令数无效")
	}
	header := packet.PeerID | uint16(packet.SessionID)<<12
	data := make([]byte, 2)
	if packet.SentTime != nil {
		header |= 0x8000
		data = append(data, byte(*packet.SentTime>>8), byte(*packet.SentTime))
	}
	binary.BigEndian.PutUint16(data, header)
	for _, command := range packet.Commands {
		body, err := encodeENetCommand(command)
		if err != nil {
			return nil, err
		}
		if len(body) > enetMaxDatagram-len(data) {
			return nil, fmt.Errorf("ENet datagram 超过 %d 字节解析预算", enetMaxDatagram)
		}
		data = append(data, body...)
	}
	return data, nil
}

// enetBodyLength 返回 ENet 命令公共 4 字节头以外的固定部分；数据长度另行计入。
func enetBodyLength(kind uint8) (int, error) {
	switch kind {
	case 1, 4:
		return 4, nil
	case 2:
		return 44, nil
	case 3:
		return 40, nil
	case 5:
		return 0, nil
	case 6:
		return 2, nil
	case 7, 9:
		return 4, nil
	case 8, 12:
		return 20, nil
	case 10:
		return 8, nil
	case 11:
		return 12, nil
	default:
		return 0, fmt.Errorf("ENet 命令 %d 不受支持", kind)
	}
}

// validateFragment 校验来自外部字节或编码参数的分片位置，不推断缺失片段或填补间隙。
func validateFragment(command ENetCommand) error {
	if command.Fragment == nil {
		return nil
	}
	f := command.Fragment
	if len(command.Data) == 0 || f.Count == 0 || f.Number >= f.Count || f.TotalLength == 0 || f.Count > f.TotalLength ||
		uint64(f.Offset)+uint64(len(command.Data)) > uint64(f.TotalLength) {
		return fmt.Errorf("ENet 分片计数、长度或偏移无效")
	}
	return nil
}

// encodeENetCommand 编码单条命令；控制与数据格式互斥，拒绝会导致字段被忽略的组合。
func encodeENetCommand(c ENetCommand) ([]byte, error) {
	fixed, err := enetBodyLength(c.Kind)
	if err != nil {
		return nil, err
	}
	isData := c.Kind == 6 || c.Kind == 7 || c.Kind == 8 || c.Kind == 9 || c.Kind == 12
	isFragment := c.Kind == 8 || c.Kind == 12
	if len(c.Data) > 65535 || isData && len(c.Control) != 0 || !isData && (len(c.Control) != fixed || len(c.Data) != 0) ||
		isFragment != (c.Fragment != nil) || c.SecondarySequence != 0 && c.Kind != 7 && c.Kind != 9 {
		return nil, fmt.Errorf("ENet 命令 %d 的控制、数据或分片字段不匹配", c.Kind)
	}
	if err := validateFragment(c); err != nil {
		return nil, err
	}
	flags := c.Kind
	if c.AckRequired {
		flags |= 0x80
	}
	if c.Unsequenced {
		flags |= 0x40
	}
	result := make([]byte, 4+fixed+len(c.Data))
	result[0], result[1] = flags, c.Channel
	binary.BigEndian.PutUint16(result[2:], c.Sequence)
	body := result[4:]
	switch c.Kind {
	case 6:
		binary.BigEndian.PutUint16(body, uint16(len(c.Data)))
	case 7, 9:
		binary.BigEndian.PutUint16(body, c.SecondarySequence)
		binary.BigEndian.PutUint16(body[2:], uint16(len(c.Data)))
	case 8, 12:
		f := c.Fragment
		binary.BigEndian.PutUint16(body, f.StartSequence)
		binary.BigEndian.PutUint16(body[2:], uint16(len(c.Data)))
		binary.BigEndian.PutUint32(body[4:], f.Count)
		binary.BigEndian.PutUint32(body[8:], f.Number)
		binary.BigEndian.PutUint32(body[12:], f.TotalLength)
		binary.BigEndian.PutUint32(body[16:], f.Offset)
	default:
		copy(body, c.Control)
	}
	copy(result[4+fixed:], c.Data)
	return result, nil
}
