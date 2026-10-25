// 本文件处理 ENet 控制握手、ACK、PING 和断开；会话号按固定 ENet 1.3 字段顺序协商，不涉及账户认证。
package nativeproto

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"time"
)

func (r *ENetRuntime) handleCommand(packet ENetPacket, command ENetCommand, events *ENetEvents, at time.Duration) (bool, error) {
	switch command.Kind {
	case 2:
		return r.acceptConnection(packet, command)
	case 3:
		if err := r.verifyConnection(packet, command); err != nil {
			return false, err
		}
		r.acknowledge(packet, command, events, at)
		return true, nil
	case 1:
		if command.AckRequired || command.Unsequenced || command.Channel != 0 && command.Channel != 255 || command.Sequence != binary.BigEndian.Uint16(command.Control) {
			return false, fmt.Errorf("ACK 控制字段无效")
		}
		return r.handleAck(command), nil
	}
	if r.state != ENetConnected && r.state != ENetDisconnecting {
		// UDP 可能让数据先于 VERIFY 的最终 ACK 到达；此时不确认或交付，等待可靠重传，而不是破坏握手。
		if r.state == ENetAccepting || r.state == ENetConnecting {
			return false, nil
		}
		return false, fmt.Errorf("ENet 未建连时收到命令 %d", command.Kind)
	}
	switch command.Kind {
	case 6, 8:
		if r.state != ENetConnected || command.Channel != 0 || !command.AckRequired || command.Unsequenced {
			return false, fmt.Errorf("只支持 connected 的可靠 channel 0 数据")
		}
		if err := r.receiveReliable(command, events, at); err != nil {
			return false, err
		}
	case 4:
		if command.Channel != 255 {
			return false, fmt.Errorf("DISCONNECT 必须使用控制 channel")
		}
		r.acknowledge(packet, command, events, at)
		r.remoteDisconnect = true
		r.disconnectData = binary.BigEndian.Uint32(command.Control)
		r.state = ENetClosed
		return true, nil
	case 5:
		if command.Channel != 255 || !command.AckRequired || command.Unsequenced {
			return false, fmt.Errorf("PING 控制字段无效")
		}
	case 10:
		if command.Channel != 255 || binary.BigEndian.Uint32(command.Control) != 0 || binary.BigEndian.Uint32(command.Control[4:]) != 0 {
			return false, fmt.Errorf("当前运行核心不支持非零 ENet 带宽限制")
		}
	case 11:
		if command.Channel != 255 || binary.BigEndian.Uint32(command.Control) != 5000 || binary.BigEndian.Uint32(command.Control[4:]) != 2 || binary.BigEndian.Uint32(command.Control[8:]) != 2 {
			return false, fmt.Errorf("当前运行核心不支持其他 throttle 参数")
		}
	default:
		return false, fmt.Errorf("当前运行核心不支持 ENet 命令 %d", command.Kind)
	}
	r.acknowledge(packet, command, events, at)
	return true, nil
}

// validateConnection 检查本轮已实现的单 channel、零带宽模式，不为未知协商参数提供静默回退。
func validateConnection(command ENetCommand) error {
	body := command.Control
	peer := binary.BigEndian.Uint16(body)
	mtu, window := binary.BigEndian.Uint32(body[4:]), binary.BigEndian.Uint32(body[8:])
	if command.Channel != 255 || command.Sequence != 1 || !command.AckRequired || command.Unsequenced || peer >= 0x0fff ||
		mtu < 576 || mtu > 4096 || window < 4096 || window > 65536 || binary.BigEndian.Uint32(body[12:]) != 1 ||
		binary.BigEndian.Uint32(body[16:]) != 0 || binary.BigEndian.Uint32(body[20:]) != 0 ||
		binary.BigEndian.Uint32(body[24:]) != 5000 || binary.BigEndian.Uint32(body[28:]) != 2 || binary.BigEndian.Uint32(body[32:]) != 2 {
		return fmt.Errorf("ENet 连接参数不在单可靠 channel 的已实现范围")
	}
	return nil
}

func (r *ENetRuntime) acceptConnection(packet ENetPacket, command ENetCommand) (bool, error) {
	if r.state != ENetIdle || packet.PeerID != 0x0fff {
		return false, fmt.Errorf("不接受覆盖已有 ENet 会话的 CONNECT")
	}
	if err := validateConnection(command); err != nil {
		return false, err
	}
	body := command.Control
	if body[2] != 0xff && body[2] > 3 || body[3] != 0xff && body[3] > 3 {
		return false, fmt.Errorf("CONNECT 会话号无效")
	}
	r.remotePeer = binary.BigEndian.Uint16(body)
	copy(r.connectID[:], body[36:40])
	r.connectBody = append([]byte(nil), body...)
	r.mtu = min(r.mtu, binary.BigEndian.Uint32(body[4:]))
	r.remoteWindow = binary.BigEndian.Uint32(body[8:])
	// ENet 的局部变量 incoming/outgoing 与 peer 字段交叉赋值：VERIFY 中 incoming 是本机 outgoing。
	r.outgoingSession = (body[2] + 1) & 3
	r.incomingSession = (body[3] + 1) & 3
	r.verifyBody = r.connectionBody(r.config.LocalPeerID, r.outgoingSession, r.incomingSession, false)
	r.state = ENetAccepting
	r.enqueueControl(3, r.verifyBody)
	return true, nil
}

func (r *ENetRuntime) verifyConnection(packet ENetPacket, command ENetCommand) error {
	if r.state == ENetConnected && bytes.Equal(command.Control, r.verifyBody) {
		return validateConnection(command)
	}
	if r.state != ENetConnecting {
		return fmt.Errorf("非 connecting 状态收到不匹配的 VERIFY_CONNECT")
	}
	if err := validateConnection(command); err != nil {
		return err
	}
	body := command.Control
	if body[2] > 3 || body[3] > 3 || !bytes.Equal(body[36:40], r.connectID[:]) {
		return fmt.Errorf("VERIFY_CONNECT 的连接标识或会话号不匹配")
	}
	if packet.SessionID != body[2] {
		return fmt.Errorf("VERIFY_CONNECT 包头会话号不匹配")
	}
	r.remotePeer = binary.BigEndian.Uint16(body)
	r.incomingSession, r.outgoingSession = body[2], body[3]
	r.mtu = min(r.mtu, binary.BigEndian.Uint32(body[4:]))
	r.remoteWindow = binary.BigEndian.Uint32(body[8:])
	r.verifyBody = append([]byte(nil), body...)
	// CONNECT 由有效 VERIFY 隐式确认，单独 ACK 不能完成主动建连。
	r.removePending(pendingKey{255, 1})
	r.state = ENetConnected
	return nil
}

func (r *ENetRuntime) handleAck(command ENetCommand) bool {
	sequence := binary.BigEndian.Uint16(command.Control)
	stamp := binary.BigEndian.Uint16(command.Control[2:])
	key := pendingKey{command.Channel, sequence}
	pending := r.pending[key]
	if pending == nil || pending.attempts == 0 || pending.command.Kind == 2 {
		return false
	}
	valid := false
	for _, sent := range pending.stamps {
		if sent == stamp {
			valid = true
			break
		}
	}
	if !valid {
		return false
	}
	removed := r.removePending(key)
	if r.state == ENetAccepting && removed.command.Kind == 3 {
		r.state = ENetConnected
	}
	if r.state == ENetDisconnecting && removed.command.Kind == 4 {
		r.state = ENetClosed
	}
	return true
}
