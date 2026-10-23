// 本文件只读加载授权群和用户配置，严格校验结构和ID，错误信息不包含配置内容。
package ipc

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

var groupIDPattern = regexp.MustCompile(`^[0-9a-fA-F]{32}$`)

// 联系人Id按完整字符串比较，不能限制为ASCII或改写空白；字节数仅保证NUL不覆盖正文起点。
func validUserID(id string) bool {
	return id != "" && utf8.ValidString(id) && !strings.ContainsRune(id, 0) && len(id) < bodyOffset-targetOffset
}

// Config 显式绑定唯一授权原生群和/或唯一授权用户；不包含正文、名称、列表或备用目标。
// 空配置仅允许只读预检；每种发送方法只使用对应的授权ID。
type Config struct {
	AuthorizedGroupID string `json:"authorized_group_id,omitempty"`
	AuthorizedUserID  string `json:"authorized_user_id,omitempty"`
}

// LoadConfig 只读解析path指定的授权配置并保留ID原值；不定位客户端或发送。
// 对象至少包含一个授权ID；文件、重复键、未知字段、类型、ID格式或尾随内容错误时返回零值和错误，不回显配置值。
func LoadConfig(path string) (Config, error) {
	file, err := os.Open(path)
	if err != nil {
		return Config{}, fmt.Errorf("打开本地授权配置: %w", err)
	}
	defer file.Close()

	decoder := json.NewDecoder(file)
	invalid := fmt.Errorf("本地授权配置必须是仅含 authorized_group_id 和/或 authorized_user_id 的 JSON 对象；群ID须为32个十六进制字符，用户ID须为1至199字节有效UTF-8且不含NUL")
	start, err := decoder.Token()
	if err != nil || start != json.Delim('{') {
		return Config{}, invalid
	}
	var config Config
	seen := make(map[string]bool)
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return Config{}, invalid
		}
		key := token.(string)
		if seen[key] {
			return Config{}, invalid
		}
		seen[key] = true
		id, err := decodeConfigID(decoder)
		if err != nil {
			return Config{}, invalid
		}
		switch key {
		case "authorized_group_id":
			if !groupIDPattern.MatchString(id) {
				return Config{}, invalid
			}
			config.AuthorizedGroupID = id
		case "authorized_user_id":
			if !validUserID(id) {
				return Config{}, invalid
			}
			config.AuthorizedUserID = id
		default:
			return Config{}, invalid
		}
	}
	end, err := decoder.Token()
	if err != nil || end != json.Delim('}') || len(seen) == 0 {
		return Config{}, invalid
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return Config{}, invalid
	}
	return config, nil
}

// encoding/json会把非法UTF-8或孤立UTF-16代理项替换为U+FFFD；身份键不能在解析时被静默改写。
func decodeConfigID(decoder *json.Decoder) (string, error) {
	var raw json.RawMessage
	if err := decoder.Decode(&raw); err != nil {
		return "", err
	}
	invalid := fmt.Errorf("授权ID必须是未损坏的JSON文本字符串")
	if len(raw) < 2 || raw[0] != '"' || !utf8.Valid(raw) {
		return "", invalid
	}
	for i := 1; i < len(raw)-1; i++ {
		if raw[i] != '\\' {
			continue
		}
		i++
		if raw[i] != 'u' {
			continue
		}
		code, _ := strconv.ParseUint(string(raw[i+1:i+5]), 16, 16)
		i += 4
		if code >= 0xdc00 && code <= 0xdfff {
			return "", invalid
		}
		if code < 0xd800 || code > 0xdbff {
			continue
		}
		if i+6 >= len(raw) || raw[i+1] != '\\' || raw[i+2] != 'u' {
			return "", invalid
		}
		low, _ := strconv.ParseUint(string(raw[i+3:i+7]), 16, 16)
		if low < 0xdc00 || low > 0xdfff {
			return "", invalid
		}
		i += 6
	}
	var id string
	if err := json.Unmarshal(raw, &id); err != nil {
		return "", err
	}
	return id, nil
}
