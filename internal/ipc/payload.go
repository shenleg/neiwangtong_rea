// 本文件构造阶段八单次实验缓冲区；其长度和空隙初始化不是厂商协议定义。
package ipc

import "fmt"

const (
	// Message 是群与用户发送共用的固定实验正文，不接受任意文本。
	Message = "NWT-GW-AUTH-TEST-0001"
	// SampleSHA256 是已静态分析的客户端文件摘要，运行前必须完全匹配。
	SampleSHA256   = "e7caef3fe744f9fe6ba90dd1d53b7e64241cb54020a8bf37be5e756e2fe881b1"
	selectorOffset = 0
	targetOffset   = 0x64
	bodyOffset     = 0x12c
	// 仅覆盖已知的三个源字符串，包含正文 NUL；不是恢复的生产者总长。
	payloadSize = bodyOffset + len(Message) + 1
)

func buildPayload(selector, targetID string) ([]byte, error) {
	if targetID == "" || len(targetID)+1 > bodyOffset-targetOffset {
		return nil, fmt.Errorf("实验目标 ID 必须非空，且含 NUL 后不覆盖正文起点")
	}
	if selector == "user_id" {
		if !validUserID(targetID) {
			return nil, fmt.Errorf("用户ID必须是有效UTF-8且不含NUL")
		}
	} else {
		// 保持原群实验的输入子集；用户Id按联系人表原值使用，不套用群格式。
		for _, c := range targetID {
			if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-') {
				return nil, fmt.Errorf("实验群 ID 只允许 ASCII 字母、数字、下划线和连字符")
			}
		}
	}
	b := make([]byte, payloadSize)
	copy(b[selectorOffset:], selector)
	copy(b[targetOffset:], targetID)
	copy(b[bodyOffset:], Message)
	return b, nil
}
