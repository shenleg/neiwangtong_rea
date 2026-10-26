// 本文件验证常驻命令的显式参数、只读检查、私有记录及合成loopback运行；不连接真实客户端。
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"neiwangtong-gateway/internal/nativeproto"
)

// testArgs 在独立临时目录中生成合成参数；不创建身份或记录，返回可直接传给runContext的参数。
func testArgs(t *testing.T) []string {
	t.Helper()
	dir := t.TempDir()
	return []string{"-local", "127.0.0.1", "-prefix", "127.0.0.0/8", "-remote", "127.0.0.2:9011", "-name", "Synthetic-Resident", "-identity", filepath.Join(dir, "identity"), "-record", filepath.Join(dir, "record.jsonl")}
}

// argValue 返回测试参数对应值，找不到说明fixture无效并终止测试。
func argValue(t *testing.T, args []string, flag string) string {
	t.Helper()
	for index := 0; index+1 < len(args); index++ {
		if args[index] == flag {
			return args[index+1]
		}
	}
	t.Fatalf("missing %s", flag)
	return ""
}

// replaceArg 返回替换指定参数后的副本，避免表驱动用例相互修改。
func replaceArg(args []string, key, value string) []string {
	result := append([]string(nil), args...)
	for index := 0; index+1 < len(result); index++ {
		if result[index] == key {
			result[index+1] = value
			return result
		}
	}
	return append(result, key, value)
}

// TestCheckNoSideEffects 验证-check不创建身份、记录或socket，并关闭拥有的输入。
func TestCheckNoSideEffects(t *testing.T) {
	args := testArgs(t)
	// 已占用测试端口仍能check成功，证明只读检查未尝试绑定socket。
	discovery, enet := loopbackSocket(t, "127.0.0.1"), loopbackSocket(t, "127.0.0.1")
	t.Cleanup(func() { _ = discovery.Close(); _ = enet.Close() })
	args = append(args, "-check", "-discovery-port", fmtPort(discovery.LocalAddr().(*net.UDPAddr).AddrPort().Port()), "-enet-port", fmtPort(enet.LocalAddr().(*net.UDPAddr).AddrPort().Port()))
	input := &trackedInput{Reader: strings.NewReader("")}
	var output, stderr bytes.Buffer
	if code := runContext(context.Background(), args, input, &output, &stderr); code != 0 {
		t.Fatalf("code=%d stderr=%s", code, &stderr)
	}
	if !input.closed {
		t.Fatal("input not closed")
	}
	for _, flag := range []string{"-identity", "-record"} {
		if _, err := os.Stat(argValue(t, args, flag)); !os.IsNotExist(err) {
			t.Fatalf("%s: %v", flag, err)
		}
	}
	if !strings.Contains(output.String(), `"network_invoked":false`) {
		t.Fatal(output.String())
	}
}

// trackedInput 记录Close是否发生；用于检查命令对输入的关闭责任。
type trackedInput struct {
	io.Reader
	closed bool
}

// Close 标记输入已关闭；无外部I/O或错误。
func (input *trackedInput) Close() error { input.closed = true; return nil }

// TestDefaultResidentConfig 验证没有-run/-duration时仍配置持续上线且不发启动聊天，预算与nwtnode一致。
func TestDefaultResidentConfig(t *testing.T) {
	config, err := parseOptions(testArgs(t), io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if config.check {
		t.Fatal("defaulted to check")
	}
	client := clientConfig(config, nativeproto.NodePacket{}, [4]byte{1})
	if client.Duration != 0 || client.Chat != nil || !client.GroupChat || client.ShutdownTimeout != 5*time.Second || client.Transport.MTU != 1400 || client.Transport.MaxMessageBytes != 1<<20 || config.discoveryPort != 9011 || config.enetPort != 9012 {
		t.Fatalf("config=%+v options=%+v", client, config)
	}
}

// TestInvalidOptionsBeforeRecord 验证广播、组播、缺参数及非法身份在创建记录前直接拒绝。
func TestInvalidOptionsBeforeRecord(t *testing.T) {
	for _, test := range []struct{ key, value string }{
		{"-remote", "127.255.255.255:9011"}, {"-remote", "255.255.255.255:9011"}, {"-remote", "224.0.0.1:9011"}, {"-remote", "127.0.0.1:9011"},
		{"-local", "0.0.0.0"}, {"-prefix", "127.0.0.0/31"}, {"-identity", "relative"}, {"-record", "relative"}, {"-name", ""}, {"-enet-port", "9011"}, {"-discovery-port", "65536"},
	} {
		t.Run(test.key+test.value, func(t *testing.T) {
			args := replaceArg(testArgs(t), test.key, test.value)
			var stderr bytes.Buffer
			if code := runContext(context.Background(), args, io.NopCloser(strings.NewReader("")), io.Discard, &stderr); code != 2 {
				t.Fatalf("code=%d error=%s", code, &stderr)
			}
			if path := argValue(t, args, "-record"); filepath.IsAbs(path) {
				if _, err := os.Stat(path); !os.IsNotExist(err) {
					t.Fatalf("record created: %v", err)
				}
			}
		})
	}
	args := testArgs(t)
	if err := os.WriteFile(argValue(t, args, "-identity"), []byte("bad"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := parseOptions(args, io.Discard); err == nil {
		t.Fatal("accepted corrupt identity")
	}
	if _, err := os.Stat(argValue(t, args, "-record")); !os.IsNotExist(err) {
		t.Fatal(err)
	}
}

// TestRecordCollisionDoesNotCreateIdentity 验证记录碰撞直接停止，不生成身份，不覆盖记录。
func TestRecordCollisionDoesNotCreateIdentity(t *testing.T) {
	args := testArgs(t)
	path := argValue(t, args, "-record")
	if err := os.WriteFile(path, []byte("preserve"), 0600); err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	if code := runContext(context.Background(), args, io.NopCloser(strings.NewReader("")), io.Discard, &stderr); code != 1 {
		t.Fatalf("code=%d stderr=%s", code, &stderr)
	}
	if _, err := os.Stat(argValue(t, args, "-identity")); !os.IsNotExist(err) {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "preserve" {
		t.Fatalf("data=%q err=%v", data, err)
	}
}

// TestContactsOwnershipBeforeRecord 验证目录归属不匹配或缺现有身份时在创建记录之前拒绝。
func TestContactsOwnershipBeforeRecord(t *testing.T) {
	args := testArgs(t)
	path := filepath.Join(filepath.Dir(argValue(t, args, "-identity")), "contacts.json")
	if err := os.WriteFile(path, []byte(`{"own_user_id":"11111111111111111111111111111111","contacts":[]}`), 0600); err != nil {
		t.Fatal(err)
	}
	args = append(args, "-contacts", path)
	if _, err := parseOptions(args, io.Discard); err == nil {
		t.Fatal("accepted missing own identity with contacts")
	}
	if err := os.WriteFile(argValue(t, args, "-identity"), []byte("22222222222222222222222222222222"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := parseOptions(args, io.Discard); err == nil {
		t.Fatal("accepted different identity")
	}
	if _, err := os.Stat(argValue(t, args, "-record")); !os.IsNotExist(err) {
		t.Fatal(err)
	}
}

// synchronizedOutput 收集并通知完整JSON行，允许测试线程在命令运行时安全观察就绪和请求结果。
type synchronizedOutput struct {
	mu    sync.Mutex
	data  bytes.Buffer
	lines chan []byte
}

// Write 保存单个编码器写入，并向有容量的测试通知通道提交副本；不阻塞网络回调。
func (output *synchronizedOutput) Write(data []byte) (int, error) {
	output.mu.Lock()
	defer output.mu.Unlock()
	count, err := output.data.Write(data)
	output.lines <- append([]byte(nil), data...)
	return count, err
}

// awaitEvent 等待指定JSON事件，超时或命令提前退出时报告错误；返回该事件内容。
func awaitEvent(t *testing.T, output *synchronizedOutput, done <-chan int, event string, count int) map[string]any {
	t.Helper()
	timer := time.NewTimer(8 * time.Second)
	defer timer.Stop()
	for {
		select {
		case line := <-output.lines:
			var value map[string]any
			if err := json.Unmarshal(line, &value); err != nil {
				t.Fatal(err)
			}
			if value["stage"] == event || value["event"] == event {
				count--
				if count == 0 {
					return value
				}
			}
		case code := <-done:
			t.Fatalf("command stopped before %s: %d", event, code)
		case <-timer.C:
			t.Fatalf("timeout waiting for %s", event)
		}
	}
}

// loopbackSocket 绑定测试拥有的loopback UDP端口；创建失败终止测试，关闭由调用者负责。
func loopbackSocket(t *testing.T, address string) *net.UDPConn {
	t.Helper()
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.ParseIP(address)})
	if err != nil {
		t.Fatal(err)
	}
	return conn
}

// TestResidentEOFAndSequentialSends 用两个合成loopback节点验证握手后多行发送、EOF不退出及取消后等待输入关闭。
func TestResidentEOFAndSequentialSends(t *testing.T) {
	testResident(t, false)
}

// TestResidentClosesBlockedInput 验证取消时关闭仍阻塞读取的stdin并等待输入协程退出。
func TestResidentClosesBlockedInput(t *testing.T) {
	testResident(t, true)
}

// testResident 建立两个合成loopback节点并验证两条发送；keepInputOpen选择EOF或保持阻塞输入，错误终止测试。
func testResident(t *testing.T, keepInputOpen bool) {
	t.Helper()
	const ownID = "11111111111111111111111111111111"
	const peerID = "22222222222222222222222222222222"
	args := testArgs(t)
	localDiscovery := loopbackSocket(t, "127.0.0.1")
	localENet := loopbackSocket(t, "127.0.0.1")
	localD := localDiscovery.LocalAddr().(*net.UDPAddr).AddrPort()
	localE := localENet.LocalAddr().(*net.UDPAddr).AddrPort()
	if err := errors.Join(localDiscovery.Close(), localENet.Close()); err != nil {
		t.Fatal(err)
	}
	peerD, peerE := loopbackSocket(t, "127.0.0.2"), loopbackSocket(t, "127.0.0.2")
	t.Cleanup(func() { _ = peerD.Close(); _ = peerE.Close() })
	remote := peerD.LocalAddr().(*net.UDPAddr).AddrPort()
	args = replaceArg(args, "-remote", remote.String())
	args = append(args, "-discovery-port", fmtPort(localD.Port()), "-enet-port", fmtPort(localE.Port()))
	if err := os.WriteFile(argValue(t, args, "-identity"), []byte(ownID), 0600); err != nil {
		t.Fatal(err)
	}
	contacts := filepath.Join(filepath.Dir(argValue(t, args, "-identity")), "contacts.json")
	directory, err := nativeproto.NewDirectory(ownID, []nativeproto.Contact{{UserID: peerID, Name: "Synthetic-Peer", Discovery: remote}})
	if err != nil {
		t.Fatal(err)
	}
	var directoryData bytes.Buffer
	if err := directory.Write(&directoryData); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(contacts, directoryData.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	args = append(args, "-contacts", contacts)
	config, err := parseOptions(args, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	profile, err := nativeproto.ProbeHandshake("Synthetic-Peer")
	if err != nil {
		t.Fatal(err)
	}
	node, err := nativeproto.NewNodePacket(nativeproto.NodeRegistration{UserID: peerID, SessionGUID: [16]byte{1}, Address: remote.Addr(), Broadcast: netip.MustParseAddr("127.255.255.255"), DiscoveryPort: remote.Port(), ENetPort: peerE.LocalAddr().(*net.UDPAddr).AddrPort().Port()}, 1)
	if err != nil {
		t.Fatal(err)
	}
	peerConfig := clientConfig(config, node, [4]byte{2})
	peerConfig.Profile, peerConfig.RemoteDiscovery, peerConfig.Contacts, peerConfig.GroupChat = profile, localD, nil, false
	peer, err := nativeproto.NewClient(peerD, peerE, peerConfig)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	peerCtx, cancelPeer := context.WithCancel(context.Background())
	defer cancelPeer()
	peerDone := make(chan error, 1)
	go func() {
		_, err := peer.Run(peerCtx, func(nativeproto.ClientEvent) error { return nil })
		peerDone <- err
	}()
	lines := `{"user_id":"` + peerID + `","text":"first synthetic message"}` + "\n" + `{"user_id":"` + peerID + `","text":"second synthetic message"}` + "\n"
	var input io.ReadCloser = io.NopCloser(strings.NewReader(lines))
	if keepInputOpen {
		reader, writer := io.Pipe()
		input = reader
		t.Cleanup(func() { _ = writer.Close() })
		go func() {
			if _, err := io.WriteString(writer, lines); err != nil {
				t.Error(err)
			}
		}()
	}
	output := &synchronizedOutput{lines: make(chan []byte, 256)}
	var stderr bytes.Buffer
	done := make(chan int, 1)
	go func() {
		code := runContext(ctx, args, input, output, &stderr)
		if code != 0 {
			t.Logf("command stderr: %s", &stderr)
		}
		done <- code
	}()
	awaitEvent(t, output, done, "protocol_ready", 1)
	last := awaitEvent(t, output, done, "request_result", 2)
	if last["sequence"] != float64(2) || last["error"] != "" {
		t.Fatalf("result=%v", last)
	}
	select {
	case code := <-done:
		t.Fatalf("EOF stopped online client: code=%d", code)
	case <-time.After(80 * time.Millisecond):
	}
	cancel()
	select {
	case code := <-done:
		if code != 0 {
			t.Fatalf("code=%d stderr=%s", code, &stderr)
		}
	case <-time.After(8 * time.Second):
		t.Fatal("client did not stop on cancel")
	}
	cancelPeer()
	select {
	case err := <-peerDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(8 * time.Second):
		t.Fatal("peer did not stop")
	}
	info, err := os.Stat(argValue(t, args, "-record"))
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("record mode/error: %v %v", info, err)
	}
	data, err := os.ReadFile(argValue(t, args, "-record"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(data, []byte("first synthetic message")) || bytes.Contains(data, []byte("second synthetic message")) {
		t.Fatal("record contains plaintext")
	}
	if !bytes.Contains(data, []byte(`"chat_sends":2`)) {
		t.Fatalf("missing accepted count: %s", data)
	}
}

// fmtPort 将测试端口编码为十进制参数，不修改系统网络配置。
func fmtPort(port uint16) string { return strconv.Itoa(int(port)) }
