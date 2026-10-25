// 本文件处理离线节点/ENet 解码、JSON Lines 分片重组和双向会话回放，不创建任何网络连接。
package main

import (
	"bufio"
	"bytes"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"time"

	"neiwangtong-gateway/internal/nativeproto"
)

func supportedMode(mode string) bool {
	switch mode {
	case "encode", "decode", "node-decode", "enet-encode", "enet-decode", "enet-reassemble", "peer-replay":
		return true
	}
	return false
}

// runProtocolMode 执行已校验模式；参数失败返回 2，输入/输出失败返回 1，成功返回 0，不访问网络。
func runProtocolMode(mode string, budget uint32, localID, remoteID string, stdin io.Reader, stdout, stderr io.Writer) int {
	var err error
	switch mode {
	case "node-decode", "enet-decode":
		var data []byte
		data, err = io.ReadAll(io.LimitReader(stdin, int64(budget)+1))
		if err == nil && uint64(len(data)) > uint64(budget) {
			err = fmt.Errorf("输入超过读取预算")
		}
		if err == nil {
			if mode == "node-decode" {
				var packet nativeproto.NodePacket
				packet, err = nativeproto.DecodeNodePacket(data)
				if err == nil {
					err = json.NewEncoder(stdout).Encode(packet)
				}
			} else {
				var packet nativeproto.ENetPacket
				packet, err = nativeproto.DecodeENet(data)
				if err == nil {
					err = json.NewEncoder(stdout).Encode(packet)
				}
			}
		}
	case "enet-encode":
		var data []byte
		data, err = io.ReadAll(io.LimitReader(stdin, int64(budget)+1))
		if err == nil && uint64(len(data)) > uint64(budget) {
			err = fmt.Errorf("ENet JSON 输入超过读取预算")
		}
		if err == nil {
			var packet nativeproto.ENetPacket
			if err = decodeRecord(data, &packet); err == nil {
				data, err = nativeproto.EncodeENet(packet)
				if err == nil {
					var count int
					count, err = stdout.Write(data)
					if err == nil && count != len(data) {
						err = io.ErrShortWrite
					}
				}
			}
		}
	case "enet-reassemble":
		err = replayFragments(stdin, stdout, budget)
	case "peer-replay":
		var peer *nativeproto.Peer
		peer, err = nativeproto.NewPeer(localID, remoteID, budget)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 2
		}
		err = replayPeer(stdin, stdout, budget, peer)
	}
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return 0
}

type datagramRecord struct {
	AtMS      *int64 `json:"at_ms"`
	Flow      string `json:"flow"`
	PacketHex string `json:"packet_hex"`
}

type reassembledRecord struct {
	AtMS    int64  `json:"at_ms"`
	Flow    string `json:"flow"`
	Channel uint8  `json:"channel"`
	DataHex string `json:"data_hex"`
}

// replayFragments 只输出实际完整的数据命令；输入按单调 at_ms 排列，控制包不会被伪装成应用消息。
func replayFragments(stdin io.Reader, stdout io.Writer, budget uint32) error {
	// 以下是本工具的缓存预算；120 秒是缓存 TTL，不宣称等于 ENet 的片组超时。
	reassembler, err := nativeproto.NewReassembler(nativeproto.ReassemblyLimits{
		MaxMessageBytes: budget, MaxBufferedBytes: 4 * uint64(budget), MaxAssemblies: 64, MaxFragments: 4096, TTL: 120 * time.Second,
	})
	if err != nil {
		return err
	}
	encoder := json.NewEncoder(stdout)
	err = scanRecords(stdin, budget, func(line []byte) error {
		var record datagramRecord
		if err := decodeRecord(line, &record); err != nil {
			return err
		}
		at, err := recordTime(record.AtMS)
		if err != nil || record.Flow == "" {
			return fmt.Errorf("分片记录须有非空 flow 和合法 at_ms")
		}
		data, err := hex.DecodeString(record.PacketHex)
		if err != nil {
			return fmt.Errorf("packet_hex: %w", err)
		}
		packet, err := nativeproto.DecodeENet(data)
		if err != nil {
			return err
		}
		pending := reassembler.Pending()
		if _, err := reassembler.Expire(at); err != nil {
			return err
		}
		if reassembler.Pending() < pending {
			return fmt.Errorf("分片组到期时仍未完整，不能将其忽略")
		}
		stream := nativeproto.ENetStream{Flow: record.Flow, PeerID: packet.PeerID, SessionID: packet.SessionID}
		for _, command := range packet.Commands {
			var payload []byte
			switch command.Kind {
			case 6, 7, 9:
				if uint64(len(command.Data)) > uint64(budget) {
					return fmt.Errorf("数据命令超过应用帧预算")
				}
				payload = command.Data
			case 8, 12:
				payload, err = reassembler.Add(stream, command, at)
				if err != nil {
					return err
				}
			}
			if payload != nil {
				if err := encoder.Encode(reassembledRecord{AtMS: *record.AtMS, Flow: record.Flow, Channel: command.Channel, DataHex: hex.EncodeToString(payload)}); err != nil {
					return err
				}
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	if reassembler.Pending() != 0 {
		return fmt.Errorf("输入结束时仍有 %d 个不完整分片组", reassembler.Pending())
	}
	return nil
}

type peerRecord struct {
	AtMS     *int64 `json:"at_ms"`
	Kind     string `json:"kind"`
	Outbound bool   `json:"outbound"`
	DataHex  string `json:"data_hex"`
}

// replayPeer 只回放完整消息与外部断开事实，输出不含身份资料的状态；不补造缺失握手或发送动作。
func replayPeer(stdin io.Reader, stdout io.Writer, budget uint32, peer *nativeproto.Peer) error {
	encoder := json.NewEncoder(stdout)
	return scanRecords(stdin, budget, func(line []byte) error {
		var record peerRecord
		if err := decodeRecord(line, &record); err != nil {
			return err
		}
		at, err := recordTime(record.AtMS)
		if err != nil {
			return err
		}
		var timers *nativeproto.PeerTimers
		switch record.Kind {
		case "message":
			data, err := hex.DecodeString(record.DataHex)
			if err != nil {
				return fmt.Errorf("data_hex: %w", err)
			}
			if err := peer.Observe(record.Outbound, data, at); err != nil {
				return err
			}
		case "disconnect", "timers":
			if record.DataHex != "" || record.Outbound {
				return fmt.Errorf("disconnect/timers 不接受 data_hex 或 outbound=true")
			}
			if record.Kind == "disconnect" {
				if err := peer.Disconnect(at); err != nil {
					return err
				}
			} else {
				value, err := peer.Timers(at)
				if err != nil {
					return err
				}
				timers = &value
			}
		default:
			return fmt.Errorf("未知会话记录 kind %q", record.Kind)
		}
		return encoder.Encode(struct {
			AtMS   int64                   `json:"at_ms"`
			Status nativeproto.PeerStatus  `json:"status"`
			Timers *nativeproto.PeerTimers `json:"timers,omitempty"`
		}{AtMS: *record.AtMS, Status: peer.Status(), Timers: timers})
	})
}

// scanRecords 限制单行读取空间，避免 JSON 巨大字符串绕过消息预算；任一记录失败即停止。
func scanRecords(stdin io.Reader, budget uint32, consume func([]byte) error) error {
	// 每行至多 256 KiB，另受两倍应用预算加 1 KiB 元数据空间限制；不是线上报文长度规则。
	limit := int(min(2*uint64(budget)+1024, 256<<10))
	scanner := bufio.NewScanner(stdin)
	scanner.Buffer(make([]byte, min(4096, limit)), limit)
	count := 0
	for scanner.Scan() {
		count++
		if err := consume(scanner.Bytes()); err != nil {
			return fmt.Errorf("记录 %d: %w", count, err)
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("读取 JSON Lines: %w", err)
	}
	if count == 0 {
		return fmt.Errorf("没有输入记录")
	}
	return nil
}

func decodeRecord(line []byte, value any) error {
	decoder := json.NewDecoder(bytes.NewReader(line))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return fmt.Errorf("一行必须恰好包含一个 JSON 对象")
	}
	return nil
}

func recordTime(milliseconds *int64) (time.Duration, error) {
	if milliseconds == nil || *milliseconds < 0 || *milliseconds > math.MaxInt64/int64(time.Millisecond) {
		return 0, fmt.Errorf("须提供范围内的单调时间 at_ms")
	}
	return time.Duration(*milliseconds) * time.Millisecond, nil
}
