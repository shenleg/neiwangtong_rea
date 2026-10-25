// 本文件对单向 ENet 会话重组分片，限制缓存并辨认重复/冲突片段，不实现传输重发或应用发送重试。
package nativeproto

import (
	"bytes"
	"fmt"
	"sort"
	"time"
)

// ReassemblyLimits 是调用方的离线资源预算，不是厂商协议或消息正文上限。
// TTL 从该组首片到达计时；完成组也保留到过期，以免传输重发再次交付相同应用帧。
type ReassemblyLimits struct {
	MaxMessageBytes  uint32
	MaxBufferedBytes uint64
	MaxAssemblies    uint32
	MaxFragments     uint32
	TTL              time.Duration
}

// ENetStream 标识单向传输会话；Flow 必须由调用者区分源/目的端点，不能只用相同 channel 或 sequence。
// PeerID 和 SessionID 取自所属 ENetPacket；断开或重新建连后应丢弃旧 Reassembler。
type ENetStream struct {
	Flow      string
	PeerID    uint16
	SessionID uint8
}

// Reassembler 以显式预算保存分片和已完成组；调用者串行调用，不持有 socket 或后台定时器。
// 它不排序不同应用消息，不替代 ENet ACK、可靠窗口和序号回绕管理。
type Reassembler struct {
	limits ReassemblyLimits
	groups map[fragmentKey]*fragmentGroup
	bytes  uint64
	lastAt time.Duration
}

type fragmentKey struct {
	stream  ENetStream
	channel uint8
	kind    uint8
	start   uint16
}

type fragmentPart struct {
	offset uint32
	data   []byte
}

type fragmentGroup struct {
	created   time.Duration
	total     uint32
	count     uint32
	parts     map[uint32]fragmentPart
	delivered bool
}

// NewReassembler 按 limits 建立空缓存并返回实例；预算为零、TTL 非正或单消息超本机分配范围时报错。
// 不设置隐式默认限额，不分配按 total_length 指定的巨大稀疏缓冲。
func NewReassembler(limits ReassemblyLimits) (*Reassembler, error) {
	if limits.MaxMessageBytes == 0 || uint64(limits.MaxMessageBytes) > uint64(int(^uint(0)>>1)) ||
		limits.MaxBufferedBytes < uint64(limits.MaxMessageBytes) || limits.MaxAssemblies == 0 || limits.MaxFragments == 0 || limits.TTL <= 0 {
		return nil, fmt.Errorf("分片重组预算须非零，缓存预算不少于单消息预算，且 TTL 为正")
	}
	return &Reassembler{limits: limits, groups: make(map[fragmentKey]*fragmentGroup)}, nil
}

// Add 将 stream 上的单条分片命令加入缓存，完整且首次交付时返回新切片；不完整或相同重发返回 nil。
// 时间倒退、非分片、越预算、字段矛盾、重叠或冲突片段返回错误，不覆盖已保存数据。
// 缓存到期由 Expire 显式释放；完整组的覆盖校验失败会明确丢弃该无效组，不填零或触发网络重试。
func (r *Reassembler) Add(stream ENetStream, command ENetCommand, at time.Duration) ([]byte, error) {
	if at < r.lastAt || stream.Flow == "" || stream.PeerID > 0x0fff || stream.SessionID > 3 {
		return nil, fmt.Errorf("分片时间倒退或会话标识无效")
	}
	if command.Kind != 8 && command.Kind != 12 || command.Fragment == nil || len(command.Control) != 0 || command.SecondarySequence != 0 {
		return nil, fmt.Errorf("重组只接受字段匹配的 ENet 8/12 分片命令")
	}
	if err := validateFragment(command); err != nil {
		return nil, err
	}
	f := command.Fragment
	if f.TotalLength > r.limits.MaxMessageBytes || f.Count > r.limits.MaxFragments || len(command.Data) > 65535 {
		return nil, fmt.Errorf("分片消息长度或片数超过调用方预算")
	}
	key := fragmentKey{stream: stream, channel: command.Channel, kind: command.Kind, start: f.StartSequence}
	group := r.groups[key]
	if group == nil {
		if uint64(len(r.groups)) >= uint64(r.limits.MaxAssemblies) {
			return nil, fmt.Errorf("分片组数超过调用方预算；须先显式清理过期组")
		}
	} else {
		if group.total != f.TotalLength || group.count != f.Count {
			return nil, fmt.Errorf("同一分片组的总长度或片数冲突")
		}
		if previous, found := group.parts[f.Number]; found {
			if previous.offset != f.Offset || !bytes.Equal(previous.data, command.Data) {
				return nil, fmt.Errorf("同一分片编号内容冲突")
			}
			r.lastAt = at
			return nil, nil
		}
		for _, part := range group.parts {
			end := uint64(part.offset) + uint64(len(part.data))
			if uint64(f.Offset) < end && uint64(part.offset) < uint64(f.Offset)+uint64(len(command.Data)) {
				return nil, fmt.Errorf("不同分片编号的字节范围重叠")
			}
		}
	}
	if uint64(len(command.Data)) > r.limits.MaxBufferedBytes-r.bytes {
		return nil, fmt.Errorf("分片缓存字节超过调用方预算")
	}
	if group == nil {
		group = &fragmentGroup{created: at, total: f.TotalLength, count: f.Count, parts: make(map[uint32]fragmentPart)}
		r.groups[key] = group
	}
	group.parts[f.Number] = fragmentPart{offset: f.Offset, data: append([]byte(nil), command.Data...)}
	r.bytes += uint64(len(command.Data))
	r.lastAt = at
	if uint32(len(group.parts)) != group.count {
		return nil, nil
	}
	// 片数齐全仍不等于覆盖完整正文；按 offset 验证每一字节，拒绝将缺口补成零。
	parts := make([]fragmentPart, 0, len(group.parts))
	for _, part := range group.parts {
		parts = append(parts, part)
	}
	sort.Slice(parts, func(i, j int) bool { return parts[i].offset < parts[j].offset })
	var next uint64
	for _, part := range parts {
		if uint64(part.offset) != next {
			r.remove(key)
			return nil, fmt.Errorf("分片编号齐全但正文存在缺口，已丢弃该无效组")
		}
		next += uint64(len(part.data))
	}
	if next != uint64(group.total) {
		r.remove(key)
		return nil, fmt.Errorf("分片覆盖长度与声明总长不同，已丢弃该无效组")
	}
	data := make([]byte, 0, int(group.total))
	for _, part := range parts {
		data = append(data, part.data...)
	}
	group.delivered = true
	return data, nil
}

// Expire 删除从首片到 at 已达到 TTL 的组，返回删除数量；时间倒退时返回错误且不修改缓存。
// 未完成组和已交付组都计入数量；用 Pending 比较未完成数，不能把清理数量解释为重组成功或送达。
func (r *Reassembler) Expire(at time.Duration) (int, error) {
	if at < r.lastAt {
		return 0, fmt.Errorf("分片清理时间倒退")
	}
	removed := 0
	for key, group := range r.groups {
		if at-group.created >= r.limits.TTL {
			r.remove(key)
			removed++
		}
	}
	r.lastAt = at
	return removed, nil
}

// Pending 返回尚未交付的分片组数，无输入且不会失败；调用者须检查 EOF，不能把缺片当作成功。
func (r *Reassembler) Pending() int {
	count := 0
	for _, group := range r.groups {
		if !group.delivered {
			count++
		}
	}
	return count
}

// remove 同步释放组与缓存计数；key 已由内部查表或遍历保证存在。
func (r *Reassembler) remove(key fragmentKey) {
	for _, part := range r.groups[key].parts {
		r.bytes -= uint64(len(part.data))
	}
	delete(r.groups, key)
}
