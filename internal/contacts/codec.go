// 本文件转写 nr 联系人库的页加密 codec：口令派生基础密钥、页密钥、IV 与整页 AES-128-CBC。
// 算法逐项对应 docs/research/reports/nr-codec/nr-codec-report.md 第 3、4 节的已验证结论。
package contacts

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/md5"
	"crypto/rc4"
	"encoding/binary"
	"fmt"
)

// padding 是 wxAES128 的公开补齐表（cipher_common.c）：口令短于 32 字节时从表首字节续补。
var padding = [32]byte{
	0x28, 0xbf, 0x4e, 0x5e, 0x4e, 0x75, 0x8a, 0x41,
	0x64, 0x00, 0x4e, 0x56, 0xff, 0xfa, 0x01, 0x08,
	0x2e, 0x2e, 0x00, 0xb6, 0xd0, 0x68, 0x3e, 0x80,
	0x2f, 0x0c, 0xa9, 0xfe, 0x64, 0x53, 0x69, 0x7a,
}

// deriveBaseKey 转写样本 0x6f9550/0x6f95a0 的口令派生：口令截断/补齐到 32 字节，
// 补齐表 51 次 MD5、20 轮 XOR 迭代密钥的 RC4、再 51 次 MD5，得到 16 字节基础密钥。
func deriveBaseKey(password []byte) [16]byte {
	var user [32]byte
	n := copy(user[:], password)
	copy(user[n:], padding[:])

	digest := md5.Sum(padding[:])
	for i := 0; i < 50; i++ {
		digest = md5.Sum(digest[:])
	}

	owner := user
	var rc4Key [16]byte
	for iteration := 0; iteration < 20; iteration++ {
		for i := range rc4Key {
			rc4Key[i] = digest[i] ^ byte(iteration)
		}
		stream, err := rc4.NewCipher(rc4Key[:])
		if err != nil {
			// 16 字节密钥不可能触发 NewCipher 错误；保持签名要求的同时不静默跳过。
			panic(fmt.Sprintf("rc4.NewCipher: %v", err))
		}
		stream.XORKeyStream(owner[:], owner[:])
	}

	hash := md5.New()
	hash.Write(user[:])
	hash.Write(owner[:])
	var key [16]byte
	copy(key[:], hash.Sum(nil))
	for i := 0; i < 50; i++ {
		key = md5.Sum(key[:])
	}
	return key
}

// pageMaterial 转写样本 0x6f92c0/0x6f9170：页密钥 = MD5(基础密钥‖LE32(页号)‖"sAlT")，
// IV = MD5(以页号+1 为种子的 Schrage 序列四轮小端字)。页号从 1 开始。
func pageMaterial(base [16]byte, page int) (pageKey, iv [16]byte) {
	input := make([]byte, 0, 24)
	input = append(input, base[:]...)
	var pageNumber [4]byte
	binary.LittleEndian.PutUint32(pageNumber[:], uint32(page))
	input = append(input, pageNumber[:]...)
	input = append(input, "sAlT"...)
	pageKey = md5.Sum(input)

	// 样本把除法编译成高位乘法；与其等价的 Schrage 形式在此范围内保持 int32 不溢出。
	z := int32(page) + 1
	var ivInput [16]byte
	for i := 0; i < 4; i++ {
		quotient := z / 52774
		z = 40692*(z%52774) - 3791*quotient
		if z < 0 {
			z += 2147483399
		}
		binary.LittleEndian.PutUint32(ivInput[i*4:], uint32(z))
	}
	iv = md5.Sum(ivInput[:])
	return pageKey, iv
}

// decryptPage 用页号派生的密钥与 IV 解密一整页 legacy=1 整页 AES-128-CBC 密文。
// 密文长度须为 16 的倍数；无填充、无保留区，第一页文件头同样在密文中。
func decryptPage(base [16]byte, page int, ciphertext []byte) ([]byte, error) {
	pageKey, iv := pageMaterial(base, page)
	block, err := aes.NewCipher(pageKey[:])
	if err != nil {
		return nil, fmt.Errorf("初始化 AES: %w", err)
	}
	plaintext := make([]byte, len(ciphertext))
	cipher.NewCBCDecrypter(block, iv[:]).CryptBlocks(plaintext, ciphertext)
	return plaintext, nil
}
