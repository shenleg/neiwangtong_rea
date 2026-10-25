// 本程序通过标准输入输出离线编解码节点/业务/ENet 包，重组分片并回放会话；不连接、登录或发送。
package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"math"
	"os"
	"strconv"

	"neiwangtong-gateway/internal/nativeproto"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

// run 解析离线模式与预算；stdin 提供正文、二进制或 JSON Lines，结果写入 stdout，错误写入 stderr。
// 返回 0 表示离线转换成功，1 表示输入或 I/O 错误，2 表示参数错误；没有网络副作用。
func run(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("nwtprotocol", flag.ContinueOnError)
	flags.SetOutput(stderr)
	mode := flags.String("mode", "", "encode/decode、node-decode、enet-encode/enet-decode、enet-reassemble 或 peer-replay")
	command := flags.String("command", "", "encode 必须指定十进制业务命令号，如 1004 或 3011")
	localUserID := flags.String("local-user-id", "", "peer-replay 使用的显式本机标识；不读取或生成身份")
	remoteUserID := flags.String("remote-user-id", "", "peer-replay 使用的显式对端标识；不代表网络发送目标授权")
	// 1 MiB 是本工具的默认文件读取预算，不是客户端或协议的可用消息上限。
	maxFrameBytes := flags.Uint64("max-frame-bytes", 1<<20, "本工具允许的总帧长，含 8 字节帧头")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 0 || !supportedMode(*mode) || *maxFrameBytes <= 8 || *maxFrameBytes > math.MaxUint32 {
		fmt.Fprintln(stderr, "必须指定受支持的 -mode；不接受位置参数，总帧长限制须为 9..4294967295")
		return 2
	}
	if *mode != "encode" && *command != "" {
		fmt.Fprintln(stderr, "只有 encode 接受 -command")
		return 2
	}
	if *mode != "peer-replay" && (*localUserID != "" || *remoteUserID != "") {
		fmt.Fprintln(stderr, "只有 peer-replay 接受本机和对端身份参数")
		return 2
	}
	if *mode != "encode" && *mode != "decode" {
		return runProtocolMode(*mode, uint32(*maxFrameBytes), *localUserID, *remoteUserID, stdin, stdout, stderr)
	}
	if *mode == "decode" {
		reader := bufio.NewReader(stdin)
		frame, err := nativeproto.Decode(reader, uint32(*maxFrameBytes))
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		if _, err := reader.Peek(1); err != io.EOF {
			if err == nil {
				fmt.Fprintln(stderr, "只接受一帧，输入含有额外字节")
			} else {
				fmt.Fprintf(stderr, "读取帧尾: %v\n", err)
			}
			return 1
		}
		if err := json.NewEncoder(stdout).Encode(frame); err != nil {
			fmt.Fprintf(stderr, "输出 JSON: %v\n", err)
			return 1
		}
		return 0
	}
	value, err := strconv.ParseUint(*command, 10, 32)
	if err != nil {
		fmt.Fprintln(stderr, "encode 必须用 -command 指定 32 位十进制命令号")
		return 2
	}
	payload, err := io.ReadAll(io.LimitReader(stdin, int64(*maxFrameBytes)-8+1))
	if err != nil {
		fmt.Fprintf(stderr, "读取 XML: %v\n", err)
		return 1
	}
	if uint64(len(payload))+8 > *maxFrameBytes {
		fmt.Fprintln(stderr, "输入超过本工具的总帧长限制")
		return 1
	}
	frame, err := nativeproto.Encode(uint32(value), string(payload))
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if n, err := stdout.Write(frame); err != nil || n != len(frame) {
		fmt.Fprintf(stderr, "输出业务帧未完成: %d/%d 字节，错误 %v\n", n, len(frame), err)
		return 1
	}
	return 0
}
