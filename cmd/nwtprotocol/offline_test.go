// 本文件验证新离线命令模式的二进制读取、JSON Lines 预算、分片输出和会话状态，不使用真实资料。
package main

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"

	"neiwangtong-gateway/internal/nativeproto"
)

// runOffline 执行内存中的命令，返回 stdout、stderr 与退出码；不创建文件或网络连接。
func runOffline(args []string, input []byte) (string, string, int) {
	var stdout, stderr bytes.Buffer
	code := run(args, bytes.NewReader(input), &stdout, &stderr)
	return stdout.String(), stderr.String(), code
}

// syntheticNodeBytes 构造仅用于命令测试的节点模板；未知字段保留非零标记，不冒充真实身份。
func syntheticNodeBytes() []byte {
	data := bytes.Repeat([]byte{0xab}, 304)
	binary.BigEndian.PutUint32(data, 304)
	binary.BigEndian.PutUint32(data[4:], 4)
	binary.BigEndian.PutUint32(data[8:], 1)
	clear(data[30:95])
	copy(data[30:], "remote-synthetic")
	return data
}

// TestOfflineNodeAndENetDecode 验证原始二进制模式及命令参数互斥，不将 ENet 包直接按 XML 解码。
func TestOfflineNodeAndENetDecode(t *testing.T) {
	stdout, stderr, code := runOffline([]string{"-mode", "node-decode"}, syntheticNodeBytes())
	if code != 0 || !strings.Contains(stdout, `"user_id":"remote-synthetic"`) || stderr != "" {
		t.Fatalf("node code=%d, out=%q, err=%q", code, stdout, stderr)
	}
	packet, err := nativeproto.EncodeENet(nativeproto.ENetPacket{Commands: []nativeproto.ENetCommand{{Kind: 5, Channel: 255}}})
	if err != nil {
		t.Fatal(err)
	}
	stdout, stderr, code = runOffline([]string{"-mode", "enet-decode"}, packet)
	if code != 0 || !strings.Contains(stdout, `"kind":5`) || stderr != "" {
		t.Fatalf("ENet code=%d, out=%q, err=%q", code, stdout, stderr)
	}
	reencoded, stderr, code := runOffline([]string{"-mode", "enet-encode"}, []byte(stdout))
	if code != 0 || stderr != "" || !bytes.Equal([]byte(reencoded), packet) {
		t.Fatalf("ENet re-encode code=%d, err=%q", code, stderr)
	}
	for _, args := range [][]string{
		{"-mode", "node-decode", "-command", "4"},
		{"-mode", "enet-decode", "-local-user-id", "local"},
		{"-mode", "peer-replay"},
	} {
		if _, _, code := runOffline(args, nil); code != 2 {
			t.Fatalf("args=%v code=%d", args, code)
		}
	}
	for _, mode := range []string{"node-decode", "enet-decode"} {
		if _, _, code := runOffline([]string{"-mode", mode}, nil); code != 1 {
			t.Fatal("accepted empty binary input")
		}
		if _, _, code := runOffline([]string{"-mode", mode, "-max-frame-bytes", "9"}, bytes.Repeat([]byte{1}, 10)); code != 1 {
			t.Fatal("ignored binary read budget")
		}
	}
}

// fragmentJSONLines 编码合成分片记录；numbers 决定乱序或重发顺序，失败时终止 t。
func fragmentJSONLines(t *testing.T, numbers ...int) []byte {
	t.Helper()
	var output bytes.Buffer
	for _, number := range numbers {
		part := []byte("abcdefgh")[number*4 : number*4+4]
		command := nativeproto.ENetCommand{Kind: 8, Channel: 0, Sequence: uint16(number + 1), Data: part,
			Fragment: &nativeproto.ENetFragment{StartSequence: 1, Count: 2, Number: uint32(number), TotalLength: 8, Offset: uint32(number * 4)}}
		data, err := nativeproto.EncodeENet(nativeproto.ENetPacket{Commands: []nativeproto.ENetCommand{command}})
		if err != nil {
			t.Fatal(err)
		}
		if err := json.NewEncoder(&output).Encode(map[string]any{"at_ms": 0, "flow": "synthetic-A-to-B", "packet_hex": hex.EncodeToString(data)}); err != nil {
			t.Fatal(err)
		}
	}
	return output.Bytes()
}

// TestOfflineReassembly 验证乱序片完成一次、相同重发不重复输出，EOF 缺片则明确失败。
func TestOfflineReassembly(t *testing.T) {
	stdout, stderr, code := runOffline([]string{"-mode", "enet-reassemble"}, fragmentJSONLines(t, 1, 0, 1, 0))
	if code != 0 || stderr != "" || strings.Count(stdout, "\n") != 1 || !strings.Contains(stdout, `"data_hex":"6162636465666768"`) {
		t.Fatalf("code=%d, out=%q, err=%q", code, stdout, stderr)
	}
	stdout, stderr, code = runOffline([]string{"-mode", "enet-reassemble"}, fragmentJSONLines(t, 0))
	if code != 1 || stdout != "" || !strings.Contains(stderr, "不完整") {
		t.Fatalf("incomplete code=%d, out=%q, err=%q", code, stdout, stderr)
	}
}

// TestOfflineRecordErrors 验证必填时间、未知字段、坏十六进制、第二个 JSON 和行预算会失败。
func TestOfflineRecordErrors(t *testing.T) {
	for _, input := range []string{
		"", "null", `{}`, `{"flow":"x","packet_hex":"000005ff0000"}`,
		`{"at_ms":-1,"flow":"x","packet_hex":"000005ff0000"}`,
		`{"at_ms":9223372036854775807,"flow":"x","packet_hex":"000005ff0000"}`,
		`{"at_ms":0,"flow":"x","packet_hex":"zz"}`,
		`{"at_ms":0,"flow":"x","packet_hex":"00"}`,
		`{"at_ms":0,"flow":"x","packet_hex":"000005ff0000","unexpected":1}`,
		`{"at_ms":0,"flow":"x","packet_hex":"000005ff0000"} {}`,
	} {
		if _, _, code := runOffline([]string{"-mode", "enet-reassemble"}, []byte(input)); code != 1 {
			t.Fatalf("accepted invalid record %q", input)
		}
	}
	if _, _, code := runOffline([]string{"-mode", "enet-reassemble"}, bytes.Repeat([]byte{'x'}, 256<<10)); code != 1 {
		t.Fatal("accepted over-budget line")
	}
}

// TestOfflinePeerReplay 验证节点观察与异常断开可回放，状态输出不含身份或虚构送达结果。
func TestOfflinePeerReplay(t *testing.T) {
	var input bytes.Buffer
	for _, record := range []map[string]any{
		{"at_ms": 0, "kind": "message", "data_hex": hex.EncodeToString(syntheticNodeBytes())},
		{"at_ms": 100, "kind": "timers"},
		{"at_ms": 101, "kind": "disconnect"},
	} {
		if err := json.NewEncoder(&input).Encode(record); err != nil {
			t.Fatal(err)
		}
	}
	args := []string{"-mode", "peer-replay", "-local-user-id", "local-synthetic", "-remote-user-id", "remote-synthetic"}
	stdout, stderr, code := runOffline(args, input.Bytes())
	if code != 0 || stderr != "" || !strings.Contains(stdout, `"state":"closed"`) || strings.Count(stdout, "\n") != 3 {
		t.Fatalf("peer code=%d, out=%q, err=%q", code, stdout, stderr)
	}
	if strings.Contains(stdout, "remote-synthetic") || strings.Contains(stdout, "recipient_observed") {
		t.Fatal("status output leaked identity or claimed delivery")
	}
	for _, input := range []string{
		`{"at_ms":0,"kind":"unknown"}`,
		`{"at_ms":0,"kind":"timers","data_hex":"00"}`,
		`{"at_ms":0,"kind":"disconnect","outbound":true}`,
		`{"at_ms":0,"kind":"message","data_hex":"zz"}`,
		`{"at_ms":1,"kind":"timers"}` + "\n" + `{"at_ms":0,"kind":"timers"}`,
	} {
		if _, _, code := runOffline(args, []byte(input)); code != 1 {
			t.Fatalf("accepted invalid peer input %q", input)
		}
	}
}
