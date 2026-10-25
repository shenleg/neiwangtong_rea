// 本文件实现固定客户端样本的原生业务帧编解码，仅处理字节，不建立连接或代表登录成功。
package nativeproto

import (
	"encoding/binary"
	"fmt"
	"io"
	"math"
	"strings"
	"unicode/utf8"
)

const headerSize = 8

// Frame 是解密后的原生业务帧；Command 是业务命令号，Payload 是 UTF-8 XML，不含末尾 NUL。
// 路由值不在此帧头中，帧解析成功也不代表认证或消息送达。
type Frame struct {
	Command uint32 `json:"command"`
	Payload string `json:"payload"`
}

// Encode 将 command 和完整 UTF-8 XML payload 编码为独立业务帧并返回新切片。
// 空正文、无效 UTF-8、嵌入 NUL、总长度超出 32 位字段或本机分配范围时返回错误；不校验业务语义。
func Encode(command uint32, payload string) ([]byte, error) {
	if err := validatePayload(payload); err != nil {
		return nil, err
	}
	length := uint64(len(payload)) + headerSize
	if length > math.MaxUint32 || length > uint64(int(^uint(0)>>1)) {
		return nil, fmt.Errorf("业务帧超过 32 位长度字段或本机可分配范围")
	}
	frame := make([]byte, int(length))
	binary.BigEndian.PutUint32(frame, uint32(len(frame)))
	binary.BigEndian.PutUint32(frame[4:], command)
	copy(frame[headerSize:], payload)
	cryptPayload(frame[headerSize:], false)
	return frame, nil
}

// Decode 从 reader 精确读取一帧，按 maxFrameBytes 限制包含帧头的总长度，返回解密正文。
// 截断、非法长度、超过调用方限制或正文无效时返回错误；不会读取下一帧，也不建立网络连接。
func Decode(reader io.Reader, maxFrameBytes uint32) (Frame, error) {
	var header [headerSize]byte
	if _, err := io.ReadFull(reader, header[:]); err != nil {
		return Frame{}, fmt.Errorf("读取业务帧头: %w", err)
	}
	length := binary.BigEndian.Uint32(header[:])
	if length <= headerSize || length > maxFrameBytes {
		return Frame{}, fmt.Errorf("业务帧总长度 %d 不在 9..%d 范围", length, maxFrameBytes)
	}
	// 长度来自外部输入；386 上须在转成 int 和分配前排除溢出。
	if uint64(length-headerSize) > uint64(int(^uint(0)>>1)) {
		return Frame{}, fmt.Errorf("业务帧正文长度超过本机可分配范围")
	}
	payload := make([]byte, int(length-headerSize))
	if _, err := io.ReadFull(reader, payload); err != nil {
		return Frame{}, fmt.Errorf("读取业务帧正文: %w", err)
	}
	cryptPayload(payload, true)
	text := string(payload)
	if err := validatePayload(text); err != nil {
		return Frame{}, err
	}
	return Frame{Command: binary.BigEndian.Uint32(header[4:]), Payload: text}, nil
}

// validatePayload 保留客户端 strlen 和 UTF-8 输入约束；XML 的业务结构由上层实验明确提供。
func validatePayload(payload string) error {
	if payload == "" || !utf8.ValidString(payload) || strings.IndexByte(payload, 0) >= 0 {
		return fmt.Errorf("正文必须为非空、有效 UTF-8 且不含 NUL 的完整 XML")
	}
	return nil
}
