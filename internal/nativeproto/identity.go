// 本文件为独立节点保存自己的新身份并生成本次会话 GUID，不读取或复制原客户端的 acc、资料或凭据。
package nativeproto

import (
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// NewSessionGUID 从 random 精确读取 16 字节，返回 Windows GUID 内存布局的随机 v4 GUID。
// 版本位在内存字节 7、variant 在字节 8；读取失败立即返回错误，不换来源或自动重试。
func NewSessionGUID(random io.Reader) ([16]byte, error) {
	var guid [16]byte
	if _, err := io.ReadFull(random, guid[:]); err != nil {
		return [16]byte{}, fmt.Errorf("生成新 GUID: %w", err)
	}
	guid[7] = guid[7]&0x0f | 0x40
	guid[8] = guid[8]&0x3f | 0x80
	return guid, nil
}

// LoadOrCreateIdentity 只读取自己的 path；不存在时独立生成 GUID 并将其内存字节小写 hex 持久化为新身份。
// path 须为绝对路径且父目录已由调用者创建；已有内容须恰好 32 个小写 hex 字节。
// I/O、随机源、身份格式或独占创建碰撞均报错，不重置损坏文件、不覆盖或复制 Windows 身份。
func LoadOrCreateIdentity(path string, random io.Reader) (string, error) {
	if !filepath.IsAbs(path) {
		return "", fmt.Errorf("独立身份文件须为绝对路径")
	}
	file, err := os.Open(path)
	if err == nil {
		defer file.Close()
		data, err := io.ReadAll(io.LimitReader(file, 33))
		if err != nil {
			return "", fmt.Errorf("读取独立身份: %w", err)
		}
		if len(data) != 32 {
			return "", fmt.Errorf("独立身份文件须恰好为 32 字节")
		}
		for _, b := range data {
			if !(b >= '0' && b <= '9' || b >= 'a' && b <= 'f') {
				return "", fmt.Errorf("独立身份文件不是小写 GUID 内存字节 hex")
			}
		}
		return string(data), nil
	}
	if !os.IsNotExist(err) {
		return "", fmt.Errorf("打开独立身份: %w", err)
	}
	guid, err := NewSessionGUID(random)
	if err != nil {
		return "", err
	}
	id := hex.EncodeToString(guid[:])
	file, err = os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return "", fmt.Errorf("独占创建独立身份: %w", err)
	}
	if n, err := io.WriteString(file, id); err != nil || n != len(id) {
		_ = file.Close()
		return "", fmt.Errorf("独立身份写入失败或短写，文件保留: %v", err)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return "", fmt.Errorf("独立身份落盘: %w", err)
	}
	if err := file.Close(); err != nil {
		return "", fmt.Errorf("关闭独立身份文件: %w", err)
	}
	return id, nil
}
