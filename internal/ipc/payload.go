// 本文件按已确认的三个字符串偏移构造可变正文载荷；最小传送长度和空隙初始化不是厂商结构定义。
package ipc

import "fmt"

const (
	// Message 保留历史固定实验正文，供CLI与兼容性测试使用；Adapter也接受合法自定义正文。
	Message = "NWT-GW-AUTH-TEST-0001"
	// SampleSHA256 是已静态分析的客户端文件摘要，运行前必须完全匹配。
	SampleSHA256   = "e7caef3fe744f9fe6ba90dd1d53b7e64241cb54020a8bf37be5e756e2fe881b1"
	selectorOffset = 0
	targetOffset   = 0x64
	bodyOffset     = 0x12c
)

// message须由prepareMessage校验；其容量预算已限制字节数，长度加法及DWORD转换不会溢出。
func buildPayload(selector, targetID, message string) ([]byte, error) {
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
	// 只传已知读取范围并包含正文NUL，不猜测厂商尾部字段。
	b := make([]byte, bodyOffset+len(message)+1)
	copy(b[selectorOffset:], selector)
	copy(b[targetOffset:], targetID)
	copy(b[bodyOffset:], message)
	return b, nil
}
