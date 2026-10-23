/* 固定版 SQLite3MultipleCiphers 的合成对照入口；不包含样本或真实数据库密钥。 */
#include "sqlite3mc.c"

/* 对显式给定的合成口令派生基础密钥，写入 key；不打开文件，无异常接口。 */
void nr_ref_key(const unsigned char* password, int length, unsigned char key[16])
{
  AES128Cipher cipher;
  memset(&cipher, 0, sizeof(cipher));
  cipher.m_keyLength = 16;
  GenerateKeyAES128Cipher(&cipher, (char*) password, length, 0, NULL);
  memcpy(key, cipher.m_key, 16);
}

/* 根据合成基础密钥和页号写入页密钥与 IV；不打开文件，无异常接口。 */
void nr_ref_material(const unsigned char key[16], int page,
                     unsigned char pagekey[16], unsigned char iv[16])
{
  unsigned char input[24];
  int j;
  memcpy(input, key, 16);
  for (j = 0; j < 4; ++j) input[16+j] = (unsigned char) (page >> (8*j));
  memcpy(input + 20, "sAlT", 4);
  sqlite3mcGetMD5Binary(input, 24, pagekey);
  sqlite3mcGenerateInitialVector(page, iv);
}

/* 原位处理一个合成完整页；encrypt 选择加密或解密，返回 SQLite 错误码。 */
int nr_ref_page(const unsigned char key[16], int page, int encrypt,
                unsigned char* data, int length)
{
  Rijndael aes;
  RijndaelCreate(&aes);
  return sqlite3mcAES128(&aes, page, encrypt, (unsigned char*) key,
                         data, length, data);
}
