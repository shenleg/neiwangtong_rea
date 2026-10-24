// 本文件用 nr-codec 报告保存的已知答案向量验证 codec 转写：口令派生、页密钥/IV 与整页解密。
package contacts

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"testing"
)

const vectorPath = "../../docs/research/reports/nr-codec/synthetic-vectors.json"

// vectorFile 对应 docs/research/reports/nr-codec/synthetic-vectors.json 的字段子集。
type vectorFile struct {
	Derivation []struct {
		PasswordHex string `json:"password_hex"`
		BaseKey     string `json:"base_key"`
	} `json:"derivation"`
	Pages []struct {
		PasswordHex     string `json:"password_hex"`
		Page            int    `json:"page"`
		PageKey         string `json:"page_key"`
		IV              string `json:"iv"`
		CiphertextHex   string `json:"ciphertext_hex"`
		PlaintextSHA256 string `json:"plaintext_sha256"`
	} `json:"pages"`
}

// loadVectors 读取报告保存的合成已知答案向量；文件缺失或损坏直接让测试失败。
func loadVectors(t *testing.T) vectorFile {
	t.Helper()
	data, err := os.ReadFile(vectorPath)
	if err != nil {
		t.Fatalf("读取向量文件: %v", err)
	}
	var vectors vectorFile
	if err := json.Unmarshal(data, &vectors); err != nil {
		t.Fatalf("解析向量文件: %v", err)
	}
	return vectors
}

func hexBytes(t *testing.T, encoded string) []byte {
	t.Helper()
	value, err := hex.DecodeString(encoded)
	if err != nil {
		t.Fatalf("十六进制解码 %q: %v", encoded, err)
	}
	return value
}

// TestDeriveBaseKeyVectors 验证 7 个口令派生向量逐字节一致，覆盖空串、短串、
// 32 字节、超长截断、含 NUL 显式长度与 UTF-8 字节输入。
func TestDeriveBaseKeyVectors(t *testing.T) {
	vectors := loadVectors(t)
	if len(vectors.Derivation) != 7 {
		t.Fatalf("派生向量数量为 %d，预期 7", len(vectors.Derivation))
	}
	for _, vector := range vectors.Derivation {
		key := deriveBaseKey(hexBytes(t, vector.PasswordHex))
		if got := hex.EncodeToString(key[:]); got != vector.BaseKey {
			t.Errorf("口令 %q 派生基础密钥 %s，预期 %s", vector.PasswordHex, got, vector.BaseKey)
		}
	}
}

// TestPageMaterialVectors 验证 40 个整页向量的页密钥与 IV，
// 覆盖 8 种页长与含 0x01020304 的多字节序页号。
func TestPageMaterialVectors(t *testing.T) {
	vectors := loadVectors(t)
	if len(vectors.Pages) != 40 {
		t.Fatalf("页向量数量为 %d，预期 40", len(vectors.Pages))
	}
	baseKeys := make(map[string][16]byte)
	for _, vector := range vectors.Pages {
		key, ok := baseKeys[vector.PasswordHex]
		if !ok {
			key = deriveBaseKey(hexBytes(t, vector.PasswordHex))
			baseKeys[vector.PasswordHex] = key
		}
		pageKey, iv := pageMaterial(key, vector.Page)
		if got := hex.EncodeToString(pageKey[:]); got != vector.PageKey {
			t.Errorf("页 %d 页密钥 %s，预期 %s", vector.Page, got, vector.PageKey)
		}
		if got := hex.EncodeToString(iv[:]); got != vector.IV {
			t.Errorf("页 %d IV %s，预期 %s", vector.Page, got, vector.IV)
		}
	}
}

// TestDecryptPageVectors 用保存密文整页解密并比对明文 SHA-256，验证 CBC 链与块序。
func TestDecryptPageVectors(t *testing.T) {
	vectors := loadVectors(t)
	baseKeys := make(map[string][16]byte)
	for _, vector := range vectors.Pages {
		key, ok := baseKeys[vector.PasswordHex]
		if !ok {
			key = deriveBaseKey(hexBytes(t, vector.PasswordHex))
			baseKeys[vector.PasswordHex] = key
		}
		plaintext, err := decryptPage(key, vector.Page, hexBytes(t, vector.CiphertextHex))
		if err != nil {
			t.Fatalf("页 %d 解密: %v", vector.Page, err)
		}
		sum := sha256.Sum256(plaintext)
		if got := hex.EncodeToString(sum[:]); got != vector.PlaintextSHA256 {
			t.Errorf("页 %d 明文 SHA-256 %s，预期 %s", vector.Page, got, vector.PlaintextSHA256)
		}
	}
}
