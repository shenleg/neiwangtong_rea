/* 本文件将已验证的固定版codec用于前32字节解密；不含实际口令，不打开数据库或导出明文库。 */
#include "sqlite3mc.c"

/* 避免编译器移除局部密钥擦除；data为待擦除缓冲区，length为字节数，无返回值或异常。 */
static void erase_material(void *data, size_t length)
{
  volatile unsigned char *p = (volatile unsigned char *) data;
  while (length--) *p++ = 0;
}

/* 使用显式口令字节解密页1前32字节到output；不打开文件，返回SQLite错误码，参数无效返回SQLITE_MISUSE。 */
SQLITE_API int nwt_contactid_header(const unsigned char *password, int length,
                                  const unsigned char encrypted[32], unsigned char output[32])
{
  AES128Cipher cipher;
  Rijndael aes;
  int result;
  if (!password || length <= 0 || !encrypted || !output) return SQLITE_MISUSE;
  memset(&cipher, 0, sizeof(cipher));
  cipher.m_keyLength = 16;
  GenerateKeyAES128Cipher(&cipher, (char *) password, length, 0, NULL);
  RijndaelCreate(&aes);
  result = sqlite3mcAES128(&aes, 1, 0, cipher.m_key, (unsigned char *) encrypted, 32, output);
  erase_material(&cipher, sizeof(cipher));
  erase_material(&aes, sizeof(aes));
  return result;
}
