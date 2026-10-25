// 本文件验证乱序、重复、冲突、缺口和缓存到期的 ENet 重组行为；输入均为合成数据。
package nativeproto

import (
	"bytes"
	"testing"
	"time"
)

// newSyntheticReassembler 创建小预算缓存供测试；t 接收初始化失败，返回独立实例，不访问外部资源。
func newSyntheticReassembler(t *testing.T) *Reassembler {
	t.Helper()
	r, err := NewReassembler(ReassemblyLimits{MaxMessageBytes: 64, MaxBufferedBytes: 128, MaxAssemblies: 4, MaxFragments: 8, TTL: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// fragmentCommand 创建合成二分片命令，number/offset/data 决定片段；返回可单独修改的元数据。
func fragmentCommand(number, offset uint32, data string) ENetCommand {
	return ENetCommand{Kind: 8, Channel: 0, Sequence: uint16(65535 + number), AckRequired: true,
		Data: []byte(data), Fragment: &ENetFragment{StartSequence: 65535, Count: 2, Number: number, TotalLength: 8, Offset: offset}}
}

// TestReassemblyOutOfOrderAndRetransmit 验证乱序重组及序号回绕，并抑制同组的重复应用交付。
func TestReassemblyOutOfOrderAndRetransmit(t *testing.T) {
	r := newSyntheticReassembler(t)
	stream := ENetStream{Flow: "synthetic-A-to-B", PeerID: 1, SessionID: 2}
	last := fragmentCommand(1, 4, "efgh")
	if data, err := r.Add(stream, last, 0); err != nil || data != nil {
		t.Fatalf("incomplete data = %q, err = %v", data, err)
	}
	last.Data[0] = 'x'
	first := fragmentCommand(0, 0, "abcd")
	data, err := r.Add(stream, first, time.Millisecond)
	if err != nil || string(data) != "abcdefgh" {
		t.Fatalf("assembled = %q, err = %v", data, err)
	}
	data[0] = 'x'
	for _, command := range []ENetCommand{first, fragmentCommand(1, 4, "efgh")} {
		if data, err := r.Add(stream, command, 2*time.Millisecond); err != nil || data != nil {
			t.Fatalf("retransmit delivered %q, err = %v", data, err)
		}
	}
}

// TestReassemblySeparatesStreams 验证端点方向、peer、session、channel 和片组序号都参与键值。
func TestReassemblySeparatesStreams(t *testing.T) {
	variants := []func(*ENetStream, *ENetCommand){
		func(s *ENetStream, c *ENetCommand) { s.Flow = "synthetic-B-to-A" },
		func(s *ENetStream, c *ENetCommand) { s.PeerID++ },
		func(s *ENetStream, c *ENetCommand) { s.SessionID++ },
		func(s *ENetStream, c *ENetCommand) { c.Channel++ },
		func(s *ENetStream, c *ENetCommand) { c.Fragment.StartSequence-- },
		func(s *ENetStream, c *ENetCommand) { c.Kind = 12 },
	}
	for i, change := range variants {
		r := newSyntheticReassembler(t)
		stream := ENetStream{Flow: "synthetic-A-to-B"}
		if _, err := r.Add(stream, fragmentCommand(0, 0, "abcd"), 0); err != nil {
			t.Fatal(err)
		}
		other, command := stream, fragmentCommand(1, 4, "efgh")
		change(&other, &command)
		if data, err := r.Add(other, command, 0); err != nil || data != nil {
			t.Fatalf("variant %d mixed streams: %q, %v", i, data, err)
		}
	}
}

// TestReassemblyConflictsAreRejected 验证相同片号冲突、元数据冲突及交叠均不覆盖先前有效片段。
func TestReassemblyConflictsAreRejected(t *testing.T) {
	cases := []ENetCommand{
		fragmentCommand(0, 0, "ABCD"), fragmentCommand(0, 1, "abcd"), fragmentCommand(1, 3, "efgh"),
	}
	changedCount := fragmentCommand(1, 4, "efgh")
	changedCount.Fragment.Count = 3
	cases = append(cases, changedCount)
	changedLength := fragmentCommand(1, 4, "efgh")
	changedLength.Fragment.TotalLength = 9
	cases = append(cases, changedLength)
	for _, conflict := range cases {
		r := newSyntheticReassembler(t)
		stream := ENetStream{Flow: "A-to-B"}
		if _, err := r.Add(stream, fragmentCommand(0, 0, "abcd"), 0); err != nil {
			t.Fatal(err)
		}
		if _, err := r.Add(stream, conflict, 0); err == nil {
			t.Fatal("accepted conflict")
		}
		data, err := r.Add(stream, fragmentCommand(1, 4, "efgh"), 0)
		if err != nil || string(data) != "abcdefgh" {
			t.Fatalf("conflict changed group: %q, %v", data, err)
		}
	}
}

// TestReassemblyRejectsGaps 验证片号齐全但实际覆盖不完整时失败并释放无效组，而不是补零交付。
func TestReassemblyRejectsGaps(t *testing.T) {
	r := newSyntheticReassembler(t)
	stream := ENetStream{Flow: "A-to-B"}
	if _, err := r.Add(stream, fragmentCommand(0, 0, "abc"), 0); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Add(stream, fragmentCommand(1, 4, "efgh"), 0); err == nil {
		t.Fatal("accepted gap")
	}
	if r.bytes != 0 || len(r.groups) != 0 {
		t.Fatal("invalid group retained cache")
	}
}

// TestReassemblyBudgetsAndExpiry 验证缓存限额和显式到期策略，重发不能不断延长首片到期时间。
func TestReassemblyBudgetsAndExpiry(t *testing.T) {
	r, err := NewReassembler(ReassemblyLimits{MaxMessageBytes: 8, MaxBufferedBytes: 8, MaxAssemblies: 1, MaxFragments: 2, TTL: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	stream := ENetStream{Flow: "A-to-B"}
	first := fragmentCommand(0, 0, "abcd")
	if _, err := r.Add(stream, first, 0); err != nil {
		t.Fatal(err)
	}
	other := stream
	other.PeerID++
	if _, err := r.Add(other, first, 0); err == nil {
		t.Fatal("accepted extra pending group")
	}
	if _, err := r.Add(stream, first, time.Second/2); err != nil {
		t.Fatal(err)
	}
	if count, err := r.Expire(time.Second - 1); err != nil || count != 0 {
		t.Fatalf("premature expiry: %d, %v", count, err)
	}
	if count, err := r.Expire(time.Second); err != nil || count != 1 || r.bytes != 0 {
		t.Fatalf("expiry: %d, %v, bytes=%d", count, err, r.bytes)
	}
	if _, err := r.Add(other, first, time.Second); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Add(stream, first, 0); err == nil {
		t.Fatal("accepted backwards time")
	}
	if _, err := r.Expire(0); err == nil {
		t.Fatal("accepted backwards cleanup")
	}
}

// TestReassemblyBufferBudget 验证未到组数上限时，缓存实际字节数仍独立受限制。
func TestReassemblyBufferBudget(t *testing.T) {
	r, err := NewReassembler(ReassemblyLimits{MaxMessageBytes: 8, MaxBufferedBytes: 8, MaxAssemblies: 3, MaxFragments: 2, TTL: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if _, err := r.Add(ENetStream{Flow: "A-to-B", PeerID: uint16(i)}, fragmentCommand(0, 0, "abcd"), 0); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := r.Add(ENetStream{Flow: "A-to-B", PeerID: 2}, fragmentCommand(0, 0, "abcd"), 0); err == nil {
		t.Fatal("accepted over-budget data")
	}
}

// TestReassemblyInputAndCompleteExpiry 验证错误输入不占缓存，完成组过期后允许新的相同序号片组。
func TestReassemblyInputAndCompleteExpiry(t *testing.T) {
	r := newSyntheticReassembler(t)
	stream := ENetStream{Flow: "A-to-B"}
	for _, command := range []ENetCommand{{Kind: 6}, {Kind: 8}, fragmentCommand(2, 0, "abcd"), fragmentCommand(0, 7, "abcd")} {
		if _, err := r.Add(stream, command, 0); err == nil {
			t.Fatal("accepted invalid fragment")
		}
	}
	if _, err := r.Add(ENetStream{}, fragmentCommand(0, 0, "abcd"), 0); err == nil {
		t.Fatal("accepted empty flow")
	}
	for _, command := range []ENetCommand{fragmentCommand(0, 0, "abcd"), fragmentCommand(1, 4, "efgh")} {
		if _, err := r.Add(stream, command, 0); err != nil {
			t.Fatal(err)
		}
	}
	if count, err := r.Expire(time.Second); err != nil || count != 1 {
		t.Fatalf("completed group expiry = %d, %v", count, err)
	}
	if _, err := r.Add(stream, fragmentCommand(0, 0, "abcd"), time.Second); err != nil {
		t.Fatal(err)
	}
	data, err := r.Add(stream, fragmentCommand(1, 4, "efgh"), time.Second)
	if err != nil || !bytes.Equal(data, []byte("abcdefgh")) {
		t.Fatalf("new group = %q, %v", data, err)
	}
}
