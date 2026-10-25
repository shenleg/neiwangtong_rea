// 本文件构造明确标记的新 Go 上线实验资料；不复制联系人/群资料，不宣称支持尚未实现的聊天、文件或远控功能。
package nativeproto

import "fmt"

// ProbeHandshake 为 displayName 创建显式、无组织归属的 3.4.3055 兼容握手资料。
// MSG 公布已实现控制帧的消息协议版本；聊天/文件/远控业务仍不分派，其他业务版本为空。
// 名称无效或 XML 编码失败时返回错误，不把消息协议版本列表误作聊天功能开关。
// 这是实验资料配置，不把这些值声明为所有厂商账号的默认值，也不读取实际用户资料。
func ProbeHandshake(displayName string) (Handshake, error) {
	if displayName == "" {
		return Handshake{}, fmt.Errorf("实验节点必须有明确显示名称")
	}
	features := []XMLField{{Name: "MAJOR", Value: "3"}, {Name: "MINOR", Value: "4"}, {Name: "BUILD", Value: "3055"}, {Name: "APP", Value: "1"}, {Name: "MSG", Value: "2:1"}}
	for _, name := range []string{"FILE_TRAN", "FOLDER_TRAN", "REMOTE_ASSISTANCE", "FILE_SHARE", "QGROUP", "QGROUP_MSG", "QGROUP_FILE", "QGROUP_FOLDER", "OFFLINE_MSG", "OFFLINE_FILE", "LAN_UPDATE", "HIDE_RECORD"} {
		features = append(features, XMLField{Name: name})
	}
	info := []XMLField{{Name: "CORP_ID"}, {Name: "CORP_TIME", Value: "0"}, {Name: "NAME", Value: displayName}, {Name: "GROUP", Value: "NWT-Go-Test"}, {Name: "CORPORATION"}, {Name: "DEPARTMENT"},
		{Name: "SEX", Value: "0"}, {Name: "POST"}, {Name: "TEL"}, {Name: "MOBILE"}, {Name: "EMAIL"}, {Name: "SIGN"}, {Name: "SYS_FACE", Value: "0"},
		{Name: "USER_FACE", Attributes: map[string]string{"id": "0"}}, {Name: "STATUS", Value: "0"}, {Name: "ACTIVE_SEND", Value: "1"},
		{Name: "ONLINE_POPUP", Value: "0"}, {Name: "HIDE_IP", Value: "0"}, {Name: "COLOR_NAME", Value: "0"}, {Name: "SORT_NAME", Value: "0"}, {Name: "DECORATE_NAME", Value: "0"}}
	profile := Handshake{Features: features, Info: info}
	if _, err := EncodeHandshake(profile); err != nil {
		return Handshake{}, err
	}
	if err := validateClientProfile(profile); err != nil {
		return Handshake{}, err
	}
	return profile, nil
}

// validateClientProfile 检查原生握手的非空必需字段；它们不是可用空串关闭的业务开关。
func validateClientProfile(profile Handshake) error {
	for _, required := range []struct {
		fields []XMLField
		names  []string
	}{{profile.Features, []string{"APP", "MSG"}}, {profile.Info, []string{"NAME", "GROUP"}}} {
		for _, name := range required.names {
			found := false
			for _, field := range required.fields {
				if field.Name == name && field.Value != "" {
					found = true
					break
				}
			}
			if !found {
				return fmt.Errorf("原生应用握手要求非空 %s", name)
			}
		}
	}
	return nil
}
