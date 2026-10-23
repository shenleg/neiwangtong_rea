// 本文件只读加载共享单群配置，严格校验结构和ID，错误信息不包含配置内容。
package ipc

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"regexp"
)

var groupIDPattern = regexp.MustCompile(`^[0-9a-fA-F]{32}$`)

// Config 绑定唯一已授权的原生群；不包含正文、群名、备用目标或隐式授权。
type Config struct {
	AuthorizedGroupID string `json:"authorized_group_id"`
}

// LoadConfig 只读解析path指定的单群配置并保留ID原值；不定位客户端或发送。
// 文件、重复键、未知字段、类型、ID格式或尾随内容错误时返回零值和错误，不回显配置值。
func LoadConfig(path string) (Config, error) {
	file, err := os.Open(path)
	if err != nil {
		return Config{}, fmt.Errorf("打开本地授权配置: %w", err)
	}
	defer file.Close()

	decoder := json.NewDecoder(file)
	invalid := fmt.Errorf("本地授权配置必须是仅含 authorized_group_id 的 JSON 对象，值须为 32 个十六进制字符")
	start, err := decoder.Token()
	if err != nil || start != json.Delim('{') {
		return Config{}, invalid
	}
	key, err := decoder.Token()
	if err != nil || key != "authorized_group_id" {
		return Config{}, invalid
	}
	var groupID string
	if err := decoder.Decode(&groupID); err != nil || !groupIDPattern.MatchString(groupID) {
		return Config{}, invalid
	}
	// 直接要求对象结束，拒绝重复键和未知字段，避免最后一个值覆盖授权目标。
	end, err := decoder.Token()
	if err != nil || end != json.Delim('}') {
		return Config{}, invalid
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return Config{}, invalid
	}
	return Config{AuthorizedGroupID: groupID}, nil
}
