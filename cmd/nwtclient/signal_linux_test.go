// 本文件在Linux独立子进程中验证真实SIGTERM/SIGKILL行为；对端只使用loopback，不连接真实客户端。
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
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"

	"neiwangtong-gateway/internal/nativeproto"
)

// TestResidentProcessHelper 仅在受控子进程中进入命令main，验证signal.NotifyContext而不是只模拟context取消。
func TestResidentProcessHelper(t *testing.T) {
	if os.Getenv("NWT_CLIENT_SIGNAL_FIXTURE") != "1" {
		t.Skip("仅由进程信号测试启动")
	}
	for index, arg := range os.Args {
		if arg == "--" {
			os.Args = append([]string{os.Args[0]}, os.Args[index+1:]...)
			main()
			return
		}
	}
	t.Fatal("missing child command arguments")
}

// TestResidentProcessSignals 验证SIGTERM发送QUIT并退出0；SIGKILL只能终止本机进程，保留未正常结束记录，均释放端口。
func TestResidentProcessSignals(t *testing.T) {
	for _, signal := range []syscall.Signal{syscall.SIGTERM, syscall.SIGKILL} {
		t.Run(signal.String(), func(t *testing.T) {
			args := testArgs(t)
			localD, localE := loopbackSocket(t, "127.0.0.1"), loopbackSocket(t, "127.0.0.1")
			discovery := localD.LocalAddr().(*net.UDPAddr).AddrPort()
			enet := localE.LocalAddr().(*net.UDPAddr).AddrPort()
			if err := errors.Join(localD.Close(), localE.Close()); err != nil {
				t.Fatal(err)
			}
			peerD, peerE := loopbackSocket(t, "127.0.0.2"), loopbackSocket(t, "127.0.0.2")
			t.Cleanup(func() { _ = peerD.Close(); _ = peerE.Close() })
			remote := peerD.LocalAddr().(*net.UDPAddr).AddrPort()
			args = replaceArg(args, "-remote", remote.String())
			args = append(args, "-discovery-port", fmtPort(discovery.Port()), "-enet-port", fmtPort(enet.Port()))
			config, err := parseOptions(args, io.Discard)
			if err != nil {
				t.Fatal(err)
			}
			node, err := nativeproto.NewNodePacket(nativeproto.NodeRegistration{UserID: "22222222222222222222222222222222", SessionGUID: [16]byte{1}, Address: remote.Addr(), Broadcast: netip.MustParseAddr("127.255.255.255"), DiscoveryPort: remote.Port(), ENetPort: peerE.LocalAddr().(*net.UDPAddr).AddrPort().Port()}, 1)
			if err != nil {
				t.Fatal(err)
			}
			peerConfig := clientConfig(config, node, [4]byte{2})
			peerConfig.RemoteDiscovery = discovery
			peerConfig.ShutdownTimeout = 100 * time.Millisecond // SIGKILL后对端不可能收到退出ACK，测试只需有限清理。
			peer, err := nativeproto.NewClient(peerD, peerE, peerConfig)
			if err != nil {
				t.Fatal(err)
			}
			peerCtx, stopPeer := context.WithCancel(context.Background())
			t.Cleanup(stopPeer)
			peerDone := make(chan error, 1)
			go func() {
				_, err := peer.Run(peerCtx, func(nativeproto.ClientEvent) error { return nil })
				peerDone <- err
			}()

			executable, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			childCtx, stopChild := context.WithTimeout(context.Background(), 10*time.Second)
			defer stopChild()
			command := exec.CommandContext(childCtx, executable, append([]string{"-test.run=^TestResidentProcessHelper$", "--"}, args...)...)
			command.Env = append(os.Environ(), "NWT_CLIENT_SIGNAL_FIXTURE=1")
			var stderr bytes.Buffer
			command.Stderr = &stderr
			stdout, err := command.StdoutPipe()
			if err != nil {
				t.Fatal(err)
			}
			if err := command.Start(); err != nil {
				t.Fatal(err)
			}
			childDone := make(chan error, 1)
			go func() { childDone <- command.Wait() }()
			finished := false
			t.Cleanup(func() {
				stopChild()
				if !finished {
					<-childDone
				}
			})
			ready := make(chan error, 1)
			go func() {
				decoder := json.NewDecoder(stdout)
				for {
					var event map[string]any
					if err := decoder.Decode(&event); err != nil {
						ready <- err
						return
					}
					if event["stage"] == "protocol_ready" {
						ready <- nil
						_, _ = io.Copy(io.Discard, stdout)
						return
					}
				}
			}()
			select {
			case err := <-ready:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("child never became ready")
			}
			if err := command.Process.Signal(signal); err != nil {
				t.Fatal(err)
			}
			select {
			case err := <-childDone:
				finished = true
				if signal == syscall.SIGTERM && err != nil || signal == syscall.SIGKILL && err == nil {
					t.Fatalf("signal=%v err=%v stderr=%s", signal, err, &stderr)
				}
			case <-time.After(8 * time.Second):
				t.Fatal("child did not stop")
			}
			stopPeer()
			select {
			case err := <-peerDone:
				if signal == syscall.SIGTERM && err != nil {
					t.Fatal(err)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("fixture peer did not stop")
			}
			data, err := os.ReadFile(argValue(t, args, "-record"))
			if err != nil {
				t.Fatal(err)
			}
			graceful := strings.Contains(string(data), `"graceful_exit":true`)
			if graceful != (signal == syscall.SIGTERM) {
				t.Fatalf("signal=%v graceful=%v", signal, graceful)
			}
			for _, endpoint := range []netip.AddrPort{discovery, enet} {
				socket, err := net.ListenUDP("udp4", net.UDPAddrFromAddrPort(endpoint))
				if err != nil {
					t.Fatalf("port not released: %v", err)
				}
				if err := socket.Close(); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}
