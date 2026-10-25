// 本文件复现样本 0x598e50/0x598f40 的 XTEA 和 0x599030/0x599080 的整块处理规则。
package nativeproto

import "encoding/binary"

const (
	// 样本使用标准 XTEA 增量与 32 轮；不能替换成 TEA 或带填充的分组模式。
	xteaDelta  uint32 = 0x9e3779b9
	xteaRounds        = 32
)

// cryptPayload 使用 .rdata:0x9ef1c8 的固定 16 字节常量，并保持不足 8 字节的尾部不变。
// 这是客户端兼容格式，不是供新协议使用的安全加密方案：它没有随机 IV 或完整性校验。
func cryptPayload(payload []byte, decrypt bool) {
	key := [4]uint32{0x38617366, 0x686a406b, 0x372a3230, 0x68626c61}
	for len(payload) >= 8 {
		xteaBlock(payload[:8], key, decrypt)
		payload = payload[8:]
	}
}

// xteaBlock 对一个大端 64 位分组原地运算；key 的四个字也按大端解释，所有加减均为模 2^32。
func xteaBlock(block []byte, key [4]uint32, decrypt bool) {
	left := binary.BigEndian.Uint32(block)
	right := binary.BigEndian.Uint32(block[4:])
	var sum uint32
	if decrypt {
		sum = uint32((uint64(xteaDelta) * xteaRounds) & 0xffffffff)
		for i := 0; i < xteaRounds; i++ {
			right -= (((left << 4) ^ (left >> 5)) + left) ^ (sum + key[(sum>>11)&3])
			sum -= xteaDelta
			left -= (((right << 4) ^ (right >> 5)) + right) ^ (sum + key[sum&3])
		}
	} else {
		for i := 0; i < xteaRounds; i++ {
			left += (((right << 4) ^ (right >> 5)) + right) ^ (sum + key[sum&3])
			sum += xteaDelta
			right += (((left << 4) ^ (left >> 5)) + left) ^ (sum + key[(sum>>11)&3])
		}
	}
	binary.BigEndian.PutUint32(block, left)
	binary.BigEndian.PutUint32(block[4:], right)
}
