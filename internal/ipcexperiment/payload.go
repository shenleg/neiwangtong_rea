// 本文件构造阶段八单次实验缓冲区；其长度和空隙初始化不是厂商协议定义。
package ipcexperiment

import "fmt"

const (
	// Message 是本次实验唯一允许的正文，不接受调用者提供其他文本。
	Message = "NWT-GW-AUTH-TEST-0001"
	// SampleSHA256 是已静态分析的客户端文件摘要，运行前必须完全匹配。
	SampleSHA256   = "e7caef3fe744f9fe6ba90dd1d53b7e64241cb54020a8bf37be5e756e2fe881b1"
	selectorOffset = 0
	targetOffset   = 0x64
	bodyOffset     = 0x12c
	// 仅覆盖已知的三个源字符串，包含正文 NUL；不是恢复的生产者总长。
	payloadSize = bodyOffset + len(Message) + 1
)

func buildPayload(groupID string) ([]byte, error) {
	if groupID == "" || len(groupID)+1 > bodyOffset-targetOffset {
		return nil, fmt.Errorf("实验群 ID 必须非空，且含 NUL 后不覆盖正文起点")
	}
	// 这是实验允许的输入子集，不是客户端 ID 语法；不改写人工提供的 ID。
	for _, c := range groupID {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-') {
			return nil, fmt.Errorf("实验群 ID 只允许 ASCII 字母、数字、下划线和连字符")
		}
	}
	b := make([]byte, payloadSize)
	copy(b[selectorOffset:], "group_id")
	copy(b[targetOffset:], groupID)
	copy(b[bodyOffset:], Message)
	return b, nil
}
