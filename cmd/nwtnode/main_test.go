// 本文件验证上线实验命令默认不产生身份、记录或 socket；真实协议只在 nativeproto 的 loopback fixture 中测试。
package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// nodeArgs 提供合成本机/对端与新的私有测试路径，返回参数和路径，不建立连接或写身份。
func nodeArgs(t *testing.T) ([]string, string, string) {
	t.Helper()
	dir := t.TempDir()
	id := filepath.Join(dir, "identity")
	record := filepath.Join(dir, "attempt.jsonl")
	return []string{"-local", "127.0.0.1", "-prefix", "127.0.0.0/8", "-remote", "127.0.0.2:9011", "-name", "Synthetic node", "-identity", id, "-record", record}, id, record
}

// TestNodeDefaultReadOnly 验证缺省只校验参数，既不生成身份，也不占用记录或产生网络操作。
func TestNodeDefaultReadOnly(t *testing.T) {
	args, id, record := nodeArgs(t)
	var stdout, stderr bytes.Buffer
	if code := run(args, &stdout, &stderr); code != 0 || stderr.Len() != 0 || !bytes.Contains(stdout.Bytes(), []byte(`"network_invoked":false`)) {
		t.Fatalf("code=%d out=%q err=%q", code, &stdout, &stderr)
	}
	for _, path := range []string{id, record} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatal("default mode wrote private state")
		}
	}
}

// TestNodeRejectsBroadcastAndBadDuration 验证广播目标、未指定本机与超出单次时限的参数在运行前失败。
func TestNodeRejectsBroadcastAndBadDuration(t *testing.T) {
	for _, extra := range [][]string{{"-remote", "127.255.255.255:9011"}, {"-remote", "255.255.255.255:9011"}, {"-local", "0.0.0.0"}, {"-duration", "91s"}, {"-duration", "5s"}, {"-enet-port", "9011"}, {"-prefix", "127.0.0.1/32"}} {
		args, _, _ := nodeArgs(t)
		args = append(args, extra...)
		var out, errOut bytes.Buffer
		if code := run(args, &out, &errOut); code != 2 {
			t.Fatalf("extra=%v code=%d", extra, code)
		}
	}
}

// TestNodeRecordCollisionStopsBeforeIdentity 验证已占用实验记录直接失败，不换文件名或生成新身份重试。
func TestNodeRecordCollisionStopsBeforeIdentity(t *testing.T) {
	args, id, record := nodeArgs(t)
	if err := os.WriteFile(record, []byte("occupied"), 0600); err != nil {
		t.Fatal(err)
	}
	args = append(args, "-run")
	var out, errOut bytes.Buffer
	if code := run(args, &out, &errOut); code != 1 {
		t.Fatalf("code=%d", code)
	}
	if _, err := os.Stat(id); !os.IsNotExist(err) {
		t.Fatal("record collision created identity")
	}
	data, err := os.ReadFile(record)
	if err != nil || string(data) != "occupied" {
		t.Fatal("record was overwritten")
	}
}
