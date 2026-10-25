// 本文件按可靠序号交付 channel 0，缓存乱序消息并复用分片重组；ACK 与首次应用交付分别处理。
package nativeproto

import (
	"bytes"
	"fmt"
	"time"
)

// receiveReliable 先约束序号与内存，再保存数据；重复旧命令仍由上层 ACK，但不再次交付应用消息。
func (r *ENetRuntime) receiveReliable(command ENetCommand, events *ENetEvents, at time.Duration) error {
	distance := uint16(command.Sequence - r.lastData)
	if distance == 0 || distance >= 0x8000 {
		return nil
	}
	if distance > 4096 {
		return fmt.Errorf("ENet 接收序号超出本实现的有限窗口")
	}
	if command.Kind == 6 {
		return r.receiveSingle(command, events)
	}
	f := command.Fragment
	if f.Count > r.config.MaxPendingCommands || f.TotalLength > r.config.MaxMessageBytes ||
		command.Sequence != f.StartSequence+uint16(f.Number) {
		return fmt.Errorf("ENet 分片序号、总长度或片数超出预算")
	}
	startDistance := uint16(f.StartSequence - r.lastData)
	if startDistance == 0 || startDistance >= 0x8000 || uint64(startDistance)+uint64(f.Count)-1 > 4096 {
		return fmt.Errorf("ENet 分片跨度超出接收序号窗口")
	}
	message := r.receive[f.StartSequence]
	if message != nil && (!message.fragmented || message.span != f.Count || message.total != f.TotalLength) {
		return fmt.Errorf("ENet 分片与已缓存消息冲突")
	}
	if message != nil && message.complete {
		if !bytes.Equal(message.data[f.Offset:uint64(f.Offset)+uint64(len(command.Data))], command.Data) {
			return fmt.Errorf("已重组消息的重复片内容冲突")
		}
		return nil
	}
	if message == nil {
		if err := r.reserveReceive(f.StartSequence, f.Count); err != nil {
			return err
		}
	}
	key := r.assemblyKey(f.StartSequence)
	group := r.assembler.groups[key]
	newPart := group == nil
	if group != nil {
		_, exists := group.parts[f.Number]
		newPart = !exists
	}
	if newPart && uint64(len(command.Data)) > r.config.MaxQueuedBytes-r.receiveBytes {
		return fmt.Errorf("ENet 接收数据超过队列字节预算")
	}
	assembled, err := r.assembler.Add(key.stream, command, at)
	if err != nil {
		return err
	}
	if message == nil {
		message = &receivedMessage{span: f.Count, total: f.TotalLength, fragmented: true}
		r.receive[f.StartSequence] = message
		for number := uint32(0); number < f.Count; number++ {
			r.owners[f.StartSequence+uint16(number)] = f.StartSequence
		}
	}
	if newPart {
		r.receiveBytes += uint64(len(command.Data))
	}
	if assembled != nil {
		message.data, message.complete = assembled, true
		// 完成后由可靠序号去重，不再让离线缓存 TTL 决定应用交付；释放已完成片组避免吞吐长期占满缓存。
		r.assembler.remove(key)
	}
	r.deliver(events)
	return nil
}

func (r *ENetRuntime) receiveSingle(command ENetCommand, events *ENetEvents) error {
	if uint64(len(command.Data)) > uint64(r.config.MaxMessageBytes) {
		return fmt.Errorf("ENet 数据超过接收消息预算")
	}
	if owner, exists := r.owners[command.Sequence]; exists {
		message := r.receive[owner]
		if owner != command.Sequence || message.fragmented || !bytes.Equal(message.data, command.Data) {
			return fmt.Errorf("ENet 相同可靠序号内容或类型冲突")
		}
		return nil
	}
	if err := r.reserveReceive(command.Sequence, 1); err != nil {
		return err
	}
	if uint64(len(command.Data)) > r.config.MaxQueuedBytes-r.receiveBytes {
		return fmt.Errorf("ENet 接收队列字节预算不足")
	}
	r.receive[command.Sequence] = &receivedMessage{span: 1, total: uint32(len(command.Data)), complete: true, data: command.Data}
	r.owners[command.Sequence] = command.Sequence
	r.receiveBytes += uint64(len(command.Data))
	r.deliver(events)
	return nil
}

func (r *ENetRuntime) reserveReceive(start uint16, span uint32) error {
	if uint64(len(r.owners))+uint64(span) > uint64(r.config.MaxPendingCommands) {
		return fmt.Errorf("ENet 接收缓存的序号数超过预算")
	}
	for number := uint32(0); number < span; number++ {
		if _, exists := r.owners[start+uint16(number)]; exists {
			return fmt.Errorf("ENet 接收消息序号范围重叠")
		}
	}
	return nil
}

func (r *ENetRuntime) assemblyKey(start uint16) fragmentKey {
	return fragmentKey{stream: ENetStream{Flow: "single-runtime-peer", PeerID: r.config.LocalPeerID, SessionID: r.incomingSession},
		channel: 0, kind: 8, start: start}
}

func (r *ENetRuntime) deliver(events *ENetEvents) {
	for {
		start := r.lastData + 1
		message := r.receive[start]
		if message == nil || !message.complete {
			return
		}
		events.Messages = append(events.Messages, message.data)
		for number := uint32(0); number < message.span; number++ {
			delete(r.owners, start+uint16(number))
		}
		delete(r.receive, start)
		r.receiveBytes -= uint64(message.total)
		r.lastData += uint16(message.span)
	}
}
