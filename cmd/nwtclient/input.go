// 本文件逐行验证stdin单聊/群聊请求并在发送前同步保存摘要；输出与私有记录共用锁，不记录正文或重试请求。
package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"os"
	"sync"
	"unicode/utf8"

	"neiwangtong-gateway/internal/nativeproto"
)

// 最长正文4096字节经JSON的\uXXXX转义可膨胀6倍；额外1024字节容纳目标及键，限制无穷输入分配。
const maxInputLineBytes = nativeproto.MaxTextBytes*6 + 1024

type eventLog struct {
	mu             sync.Mutex
	file           *os.File
	record, output *json.Encoder
}

// write 在同一锁内完成记录编码、Sync及可选stdout编码；任何I/O失败直接返回，发送意图必须先成功落盘。
func (log *eventLog) write(value any, output bool) error {
	log.mu.Lock()
	defer log.mu.Unlock()
	if err := log.record.Encode(value); err != nil {
		return fmt.Errorf("写入私有记录: %w", err)
	}
	if err := log.file.Sync(); err != nil {
		return fmt.Errorf("同步私有记录: %w", err)
	}
	if output {
		if err := log.output.Encode(value); err != nil {
			return fmt.Errorf("写入stdout: %w", err)
		}
	}
	return nil
}

type requestResult struct {
	Event    string                 `json:"event"`
	Sequence uint64                 `json:"sequence"`
	Result   nativeproto.ChatResult `json:"result"`
	Error    string                 `json:"error"`
}

// consumeRequests 依次调用sendUser或sendGroup，发送前将目标/正文hash及字节数Sync；坏行只输出错误，不调用发送。
// EOF只返回nil给输入协程，不结束客户端；超长行报错后停止读取以保持Scanner固定预算，其他读取/记录错误返回调用者。
func consumeRequests(ctx context.Context, input io.Reader, contacts *nativeproto.Directory, remote netip.AddrPort, sendUser, sendGroup func(context.Context, string, string) (nativeproto.ChatResult, error), log *eventLog) error {
	scanner := bufio.NewScanner(input)
	scanner.Buffer(make([]byte, 4096), maxInputLineBytes)
	var sequence uint64
	for scanner.Scan() {
		if ctx.Err() != nil {
			return nil
		}
		sequence++
		request, err := decodeRequest(scanner.Bytes())
		if err == nil && request.UserID != "" {
			err = selectContact(contacts, remote, request.UserID)
		}
		targetID, targetKey, send := request.UserID, "target_user_id_sha256", sendUser
		command := nativeproto.CommandUserMessage
		if request.GroupID != "" {
			targetID, targetKey, send = request.GroupID, "target_group_id_sha256", sendGroup
			command = nativeproto.CommandGroupMessage
		}
		result := nativeproto.ChatResult{Command: command, Delivery: "unknown"}
		if err == nil {
			if err := log.write(map[string]any{"event": "chat_request", "sequence": sequence,
				targetKey:        fmt.Sprintf("%x", sha256.Sum256([]byte(targetID))),
				"message_sha256": fmt.Sprintf("%x", sha256.Sum256([]byte(request.Text))), "message_bytes": len(request.Text), "delivery": "unknown"}, false); err != nil {
				return err
			}
			err = ctx.Err()
			if err == nil {
				result, err = send(ctx, targetID, request.Text)
			}
		}
		if err := log.write(requestResult{Event: "request_result", Sequence: sequence, Result: result, Error: errorText(err)}, true); err != nil {
			return err
		}
	}
	if ctx.Err() != nil {
		return nil
	} // 运行结束关闭input，用于解除阻塞读取，不把该次关闭误记为网络失败。
	if err := scanner.Err(); err != nil {
		if errors.Is(err, bufio.ErrTooLong) {
			return log.write(requestResult{Event: "request_result", Sequence: sequence + 1, Result: nativeproto.ChatResult{Command: nativeproto.CommandUserMessage, Delivery: "unknown"}, Error: fmt.Sprintf("输入行超过%d字节，停止接受stdin；客户端保持在线", maxInputLineBytes)}, true)
		}
		return fmt.Errorf("读取stdin: %w", err)
	}
	return nil
}

// decodeRequest 仅接受一个含text和互斥user_id/group_id字符串的JSON对象；拒绝缺键、未知键、重复键、非法UTF-8及尾随数据。
// 返回经协议正文校验的请求或不含原始输入的明确错误，防止错误日志泄漏正文。
func decodeRequest(line []byte) (nativeproto.ChatRequest, error) {
	var request nativeproto.ChatRequest
	if !utf8.Valid(line) {
		return request, fmt.Errorf("请求必须是合法UTF-8")
	}
	decoder := json.NewDecoder(bytes.NewReader(line))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return request, fmt.Errorf("每行须为包含text及user_id或group_id的JSON对象")
	}
	seen := make(map[string]bool, 2)
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return request, fmt.Errorf("请求JSON键无效")
		}
		key, ok := token.(string)
		if !ok || (key != "user_id" && key != "group_id" && key != "text") || seen[key] {
			return request, fmt.Errorf("请求只允许user_id、group_id及text，字段不得重复")
		}
		seen[key] = true
		token, err = decoder.Token()
		value, ok := token.(string)
		if err != nil || !ok {
			return request, fmt.Errorf("user_id、group_id及text必须是字符串")
		}
		switch key {
		case "user_id":
			request.UserID = value
		case "group_id":
			request.GroupID = value
		case "text":
			request.Text = value
		}
	}
	if token, err = decoder.Token(); err != nil || token != json.Delim('}') {
		return request, fmt.Errorf("请求JSON对象未完整结束")
	}
	if _, err := decoder.Token(); err != io.EOF {
		return request, fmt.Errorf("每行只允许一个JSON对象")
	}
	if !seen["text"] || seen["user_id"] == seen["group_id"] {
		return request, fmt.Errorf("请求必须包含text，且user_id与group_id必须二选一")
	}
	if err := nativeproto.ValidateChatRequest(request); err != nil {
		return request, err
	}
	return request, nil
}

// selectContact 只按当前身份目录中的原始ID选择当前单peer；无列表、未知ID或发现端点不同均拒绝。
func selectContact(contacts *nativeproto.Directory, remote netip.AddrPort, userID string) error {
	if contacts == nil {
		return fmt.Errorf("发送需要-contacts提供当前身份的联系人列表")
	}
	contact, err := contacts.ContactByID(userID)
	if err != nil {
		return err
	}
	if contact.Discovery != remote {
		return fmt.Errorf("指定联系人不是当前单peer发现端点")
	}
	return nil
}
