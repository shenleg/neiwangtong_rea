# nr 联系人库 codec：静态契约与合成兼容验证

本文补充 [user-id-report.md 第 4.3、6.1 节](../user-id/user-id-report.md#43-数据库密钥与编码)，只确定离线文件 codec，不重新研究联系人字段，不实现真实副本读取工具或 `SendUser`。

## 1. 结论

**固定提交 `f29b06b424e556cf0ad8b19646b30dfc3e3e36d8` 的 SQLite3 Multiple Ciphers `aes128cbc`、`legacy=1` 与本样本的页算法兼容。** 口令补齐、两组 MD5 迭代、RC4、页密钥、IV、AES-128-CBC、第一页整页处理均已逐项比对；样本指令转写、固定版参考实现和 OpenSSL 的合成结果一致。[C1–C14、R1–R3]

**页大小不是 codec 中硬编码的 nr 参数，也不能把参考库的默认 4096 当成实际页大小。** 样本 `0x6f97f0` 是单条 `RET`；codec 每次从 `BtShared +0x20` 取当前页大小。样本先用 Pager 当前大小读取、解密第一页，再由 SQLite 检查解密后的页头、设置真实页大小。独立读取端可以只解密文件前 32 字节，从第一页头取得准确大小，再明确设置参考实现的 `legacy_page_size`；不需要遍历大小猜参数。[C4、C15–C20]

已实际通过：

- **7 个口令派生向量**：空串、短串、固定人工口令、32 字节、超过 32 字节、含 NUL 的显式长度输入、UTF-8 字节。
- **40 个整页向量**：8 种页大小 × 5 个页号，逐项比较基础密钥、页密钥、IV、全部密文及双向解密。
- **8 个合成 `OrgStruct_SYNTHETIC` 数据库**：前 32 字节恢复页大小、独立只读打开、schema/列检查、`Type=1` 和 XML 原始名称精确匹配、拒绝写入、拒绝边文件；每个加密库查询前后 SHA-256 相同。

原始结果见 [synthetic-results.json](synthetic-results.json)，全部合成输入与预期密文见 [synthetic-vectors.json](synthetic-vectors.json)。这满足本轮 **codec 静态比对和合成验证** 的前置条件，不等于已读取真实 `nr`，也不等于已取得 `PVE-WIN10` 的实际 ID。

## 2. 样本、参考版本与边界

- 样本：内网通 3.4.3055、Windows x86，`ShiYeLine.exe`。
- SHA-256：`e7caef3fe744f9fe6ba90dd1d53b7e64241cb54020a8bf37be5e756e2fe881b1`；静态 ImageBase `0x400000`。
- 静态工具：REA 6.7.0、Ghidra 12.1.4；分析配置摘要 `7810ea9437da59780820ec6fedb47ce47390841a95d527ddb6c3b3700fe180cb`，与历史证据相同。
- 参考提交：`f29b06b424e556cf0ad8b19646b30dfc3e3e36d8`；编译产物实际报告 `SQLite3 Multiple Ciphers 2.7.0`、SQLite `3.54.1`，不采用旧 readme 的版本号。
- 本轮只读取客户端代码与公开算法常量，**没有读取样本实际数据库密钥的字节**。报告中的所有口令、派生材料、ID 和数据库均为人工合成数据。
- 没有运行客户端、注入、读取进程内存、真实联系人库、聊天或凭据；没有修改 Adapter、配置、原始数据库或单次尝试记录，没有发送消息。
- 参考源码编译与运行取得了单独明确授权；参考库只在 Linux 本地处理合成数据。没有加载客户端 EXE/DLL 或调用客户端内部地址。
- 第一版读取条件限定为正常退出后取得的完整独立副本；不处理 WAL、journal 恢复、活动库复制或多身份表归属。

下文 C/H 是报告别名，原始 Evidence ID 保留在第 9 节。静态代码说明算法；合成测试说明独立实现结果；两者均不冒充客户端运行证据。

## 3. 摘要、口令输入与基础密钥

### 3.1 `0x6f9100` 是 MD5，不是 SHA 或 AES

入口使用 `0x67452301 / 0xefcdab89 / 0x98badcfe / 0x10325476` 初始化四字状态，调用更新 `0x6f8890` 与结束 `0x6f87e0`。后者添加 `0x80`、64 位位长度并输出四个小端字。压缩函数 `0x6f8970` 的 64 个加数、四组布尔操作、字索引和旋转与 MD5 一致；例如 `0x6f8992` 使用 `0xd76aa478`、`0x6f899c` 旋转 7 位，最后使用 `0xeb86d391`。[C1、C2]

不是只凭初始常量命名：测试从保存指令提取全部 64 个加数和旋转数，转写完整压缩过程，并把每次结果与 Python 标准库 MD5 比较。

`0x6f9100` 的真实传参为：

| 入口位置 | 语义 |
|---|---|
| ECX | 16 字节摘要输出地址；入口保存至 EBX |
| `[ESP+4]` | 输入字节地址 |
| `[ESP+8]` | 输入字节长度，不是位数 |

用途必须分开：

- **基础密钥**：`0x6f95a0` 直接调用相同的更新/结束原语，**不是调用 `0x6f9100` 包装器**。输入依次为补齐串 32 字节、16 字节摘要的 50 次重哈希、两个 32 字节串连续更新合计 64 字节、16 字节摘要的另 50 次重哈希。
- **页密钥**：`0x6f9342` 调用 `0x6f9100`，长度 `0x18=24`。
- **IV**：`0x6f92a2` 调用 `0x6f9100`，长度 `0x10=16`。

`0x6f9100` 的定向 xrefs 只有后两处；不能据包装器名称推断基础密钥输入长度。[C3、C6、C8]

### 3.2 字节口令、截断与空输入

历史 `0x67d670–0x67d677` 逐字节搜索 NUL，按窄字符串 `strlen` 的字节数传入 `0x6f98e0`，不包含终止符；没有在此处做 UTF-16 转换。[H1]

- `0x6f9552–0x6f955c` 将大于 32 的长度截为 32。
- 先复制前 `min(n,32)` 个字节，再从补齐表的**第 0 字节**开始补到 32，不是从表的第 n 字节续取。[C7]
- 算法函数可计算空口令的补齐值；但**客户端安装 codec 的路径**在 `0x6f9915–0x6f991f` 要求口令指针非空且长度大于 0。空字符串不会在此路径激活一个“空口令加密库”。[H2]
- 显式长度函数可以处理嵌入 NUL，客户端包装器的 `strlen` 则在第一个 NUL 停止。合成 `a\0b` 向量检验的是前者，不声称客户端会传入 3 字节。
- 本轮确认的是长度的计算规则，不读取实际密钥来计算或公布其数值长度。后续私有读取必须复用同一窄字符串字节序列，不自行猜编码或把实际密钥写入命令行。

### 3.3 补齐表与完整派生

`0xa8fa9c` 只读取下面 32 字节的公开算法补齐表，与参考 `cipher_common.c:12-13` 逐字节相同：[C5、R3]

```text
28 bf 4e 5e 4e 75 8a 41 64 00 4e 56 ff fa 01 08
2e 2e 00 b6 d0 68 3e 80 2f 0c a9 fe 64 53 69 7a
```

记该串为 S，`Pad(P)` 为上述截断/补齐；MD5 返回原始 16 字节，不是十六进制文本：

```text
U = Pad(P)
D = MD5(S)
重复 50 次：D = MD5(D)
R = U
对 i = 0..19：R = RC4(key = D 的每个字节 XOR i, data = R)
K = MD5(U || R)
重复 50 次：K = MD5(K)
```

K 为基础密钥。每一组都是初始一次加 50 次，即 **51 次 MD5**；20 次 RC4 每次都重新初始化 256 字节状态，密钥 16 字节、数据 32 字节。对应关键指令：`0x6f9639 / 0x6f968a–0x6f968d`、`0x6f96b0–0x6f96df`、`0x6f9717 / 0x6f972a`、`0x6f973f / 0x6f978e–0x6f9791`。[C8、H3]

固定版本参考 `GenerateKeyAES128Cipher` 与此顺序相同；不使用 PBKDF2、随机 salt 或 `raw:` 密钥旁路。[R1]

## 4. 页密钥、IV 与 AES 调用实参

### 4.1 页密钥

对从 **1** 开始的页号 p：

```text
Kp = MD5(K[0:16] || LE32(p) || 73 41 6c 54)
```

`0x6f9300–0x6f932a` 将 EBX 页号按低字节在前写入；`0x6f932e–0x6f933d` 写入 `sAlT`，`0x6f9315` 明确压入总长度 24。该固定四字节后缀不是文件随机盐。[C6]

### 4.2 IV 的整数过程

`0x6f9180–0x6f9182` 以 EDX 页号加 1 为种子。四次展开的计算实际使用：

```text
q = signed_high32(z * 0x4f7a09cd) >> 14
q = q + (uint32(q) >> 31)
z = int32(int32(z * 0x9ef4) - int32(q * 0x7fffff07))
若 z < 0：z = int32(z + 0x7fffff07)
追加 LE32(z)
```

其中 `0x9ef4=40692`、`0x7fffff07=2147483399`，`0x4f7a09cd` 是有符号除以 **52774** 的乘法优化常量。利用 `40692*52774 + 3791 = 2147483399`，这与固定源码的 Schrage 形式一致：

```text
q = trunc(z / 52774)
z = 40692 * (z - 52774*q) - 3791*q
若 z < 0：z += 2147483399
```

四轮产生 16 字节，`0x6f91ac` 起的逐字节存储及后续三组均为小端，最后 `0x6f9294–0x6f92a2` 对这 16 字节做 MD5，得到 IV。测试转写保留样本的有符号高位乘法、移位及 32 位回绕，不把源码公式直接当成样本已经执行的算法。[C9、R2]

### 4.3 调用点核对：不能省略寄存器参数

下面 ESP 均指**被调用函数刚进入、尚未执行序言时**的值，`[ESP]` 是返回地址。

| 函数 | 寄存器参数 | 栈参数 |
|---|---|---|
| `0x6f9800` codec 回调 | 无需假定寄存器携参 | `+4 codec`、`+8 page data`、`+0xc 页号`、`+0x10 SQLite 模式` |
| `0x6f92c0` 页处理 | ECX=输入；EDX=输出；EBX=页号；EDI=codec | `+4 加密标志`、`+8 基础密钥地址`、`+0xc 页字节数` |
| `0x6f9170` IV | EDX=页号；ECX=IV 输出 | 无显式业务栈参数 |
| `0x6f7540` AES 初始化 | ESI=`[codec+0x2c]` AES 对象；EDX=派生页密钥；ECX=IV | `+4 方向`，0=加密、1=解密 |
| `0x6f7630 / 0x6f79b0` | ECX=AES 对象；EDX=输入 | `+4 位数`、`+8 输出地址` |

对应调用点：[C4、C6、C10]

- `0x6f983f–0x6f984e`：压入完整页长、`codec+8` 读基础密钥、标志 0；ECX/EDX 同为页缓冲区；EBX 从回调的页号参数取得，原位解密。
- `0x6f9866–0x6f9883`：先复制完整页到 `codec+0x38`，使用 `codec+0x1c` 写基础密钥，标志 1；模式 7 的 `0x6f98a9–0x6f98ae` 改用 `codec+8`。
- `0x6f9352–0x6f935e`：方向是“加密标志等于 0”的结果，不是按伪代码参数名猜正反方向；ESI 从 codec 的 AES 指针加载，ECX/EDX 指向刚生成的 IV/页密钥。
- `0x6f9367–0x6f939b`：页字节数乘 8 后压栈，非零加密标志调用 `0x6f7630`，零调用 `0x6f79b0`。
- `0x6f7552–0x6f7558` 把模式设为 1；`0x6f75aa` 设 10 轮，`0x6f75ed` 读取 16 字节密钥。这是 AES-128-CBC。

模式跳转表 `0x6f98bc` 逐项确认：0、2、3 为解密，6 为写密钥加密，7 为读密钥加密，1、4、5 原样返回。[C11]

### 4.4 不是仅凭“10 轮”识别 AES

`0x6f7d50` 是 128 位密钥扩展，`0x6f8010 / 0x6f83f0` 是正/逆轮，`0x6f7f20` 对中间轮密钥作逆 MixColumns。读取的算法表范围为 `0xa8c820..0xa8fa47`，不包含实际数据库密钥。[C12–C14]

测试使用保存的 S-box、正/逆 T 表、逆混合表与轮常量，逐项按 AES 的有限域运算验证，再转写样本轮次、列移位、轮密钥与 CBC。`0x6f78e8` 开始先异或 IV；后续块异或上一密文；解密 `0x6f79b0` 则在逆轮后异或前一密文。[C13、C14]

参考版本底层 Rijndael 实现经过重构，结构体布局和接口与样本不同；本轮确认的是算法和磁盘字节兼容，不宣称二进制或内部 ABI 相同。[R4]

## 5. 页大小、第一页与保留区

### 5.1 codec 安装与第一页之前的大小

安装路径复用历史指令 [H2]：

1. `0x6f98f7` 分配 `0x1003c` 字节 codec，包含 65536 字节页副本缓冲区。
2. `0x6f9944–0x6f994e` 保存 Btree 指针到 `codec+0x30`。
3. `0x6f9983` 注册 `0x6f97f0` 为大小变更回调，`0x6f99c2` 用 Pager 当前大小/保留数调用它。
4. **`0x6f97f0` 只有 `RET`，没有读头、设置页长或固定页长的效果。** Ghidra 给它的 `CSkinWndView::CreateWindowHost` 名称不符合上下文；这里以实际一条指令为准。[C15]
5. `0x6f9818–0x6f9822` 每次通过 `codec+0x30 → Btree+4 → BtShared+0x20` 取实际处理长度；不读某个“codec 页长字段”。[C4]

向前追查打开过程，`0x690bd0` 先调用 Pager 打开，再由 `0x68aa10` **直接读磁盘前 100 字节**；此时 codec 尚未安装。`0x691020–0x69104e`检查头中的大小编码、512..65536 和二次幂；无有效大小时将 Btree 页长置 0，传给 `0x68a8c0`，后者保留 Pager 已有大小。[C16、C17]

Pager 打开 `0x68b320` 的初始值是 1024；可按底层文件方法返回的扇区大小上调，上限 8192。`0x689c70` 调用文件方法 `+0x2c` 获取扇区大小，保存至 `Pager+0x94`；`0x68b320` 将其用于初始页长选择。因此**默认 1024 不等于真实 nr 固定为 1024**，也不把密文页头偶然呈现的值当作已解密参数。[C18、C19]

### 5.2 真正的大小由解密页头取得

`0x6917dc` 明确请求页号 **1**；实际页读 `0x68a010`：

- `0x68a045–0x68a050` 计算 `(page-1) * Pager.pageSize` 文件偏移。
- `0x68a0b5–0x68a0cd` 在磁盘读取后，用 `codec(ctx, data, page, 3)` 解密。
- 回到 `0x6917c0`，`0x69185a–0x691884` 比较全部 16 字节 SQLite 魔数。
- `0x691908–0x69192f` 解析大小并检查二次幂和范围；与当前页长不同，则 `0x691942–0x691972` 释放当前页、更新 `BtShared+0x20` 并调用 Pager 设置大小，再由后续读取取得正确完整页。[C20、C21]

大小的合法编码等价于：头字节 16..17 为大端值，普通页长直接表示；值 `0x0001` 表示 65536。样本优化写法为 `(h[16] | (h[17]<<8))<<8`；对合法的二次幂编码，与参考 `(h[16]<<8)|(h[17]<<16)` 一致。

已确认支持的页长为：`512,1024,2048,4096,8192,16384,32768,65536`。下限 512 来自二次幂与范围检查共同约束，不把中间算式中的 257 当成可用页长。[C16、C20]

### 5.3 全页、第一页与 reserved

- 页号从 1 开始，不从 0 开始。
- `0x6f9800` 不判断是否第一页；每次按完整 `_Size` 加/解密，输入输出从 offset 0 开始。
- 第一页的 `SQLite format 3\0`、大小字段与其余页头都在密文中；解密自然恢复魔数。样本不另行覆盖魔数，不搬移 8 字节，不留明文的 16..23 字节。
- 后续页使用相同算法，只更换页号派生的 Kp/IV；CBC 不跨页延续。
- 不使用 PKCS#7，不增加块、salt、HMAC 或 codec 专用尾部区；包括 SQLite 可能声明的 reserved 区在内，传入的完整页都经过 CBC。参考 `GetReservedAES128Cipher=0`、`GetSaltAES128Cipher=NULL` 与此一致。[C4、C6、R1]
- 这说明的是 **codec 不另占 reserved bytes**，不是一次真实库的头字节 20 读取结果。本轮合成库均为 reserved=0；首版真实读取应对未经验证的非零值明确拒绝。

### 5.4 独立只读读取端的准确初始化契约

无需模仿旧 Pager 的初始页长探测：

1. 仅接受已正常退出后取得的完整私有副本；存在同名 `-wal`、`-shm`、`-journal` 任一个即拒绝，空边文件也不例外。不删除边文件、不恢复、不修复。
2. 使用已知口令字节派生 K；以 **页号 1** 的 Kp/IV 解密前 **32 字节**，不读取真实联系人行。CBC 的前两块与整页最终长度无关。[C6、C13、C14]
3. 严格验证魔数、合法页长、文件长度为页长整数倍、读写版本及 reserved。首版只接受 rollback 模式头（18/19 均为 1）、reserved=0；WAL 模式头不因缺少边文件而默认接受。错误立即停止，不换 cipher、换口令或遍历页长。
4. 独立 SQLite 以 `SQLITE_OPEN_READONLY` 打开；在 `sqlite3_key` **之前**设置 `cipher=aes128cbc`、`legacy=1`、`legacy_page_size=已恢复页长`。校验 `sqlite3_db_readonly(...,"main")==1`。
5. schema 检查与联系人查询继续遵守原报告，不读取群成员、聊天或其他个人资料；原文件与副本不写入，查询前后保留哈希。

本轮测试验证了第 2、4 项及合成查询/拒写/边文件拒绝。第 1 项的真实副本取得、第 3 项的完整生产输入校验、第 5 项的 Windows 身份归属尚未实现；`verify_codec.py` **不接受数据库输入参数**，不是可用于真实文件的提取器。

## 6. 与固定版本参考源码的比较

| 项目 | 样本与参考 |
|---|---|
| 口令、padding、两组 51 次 MD5、20 次 RC4 | 一致，样本固定 16 字节材料；参考同模式生成相同基础密钥 [C5、C7、C8、R1、R3] |
| 页密钥 | 同为 MD5 的 24 字节输入，页号小端、后缀 `sAlT` [C6、R2] |
| IV | 同一 Schrage 序列和小端摘要输入；样本编译成高位乘法/移位与回绕 [C9、R2] |
| AES/CBC/整页 | 一致；结构体、内部 ABI 与源实现不是同一版本 [C10、C12–C14、R4] |
| 第一页 | 只与 `legacy=1` 的全页模式相同；不采用默认新格式的头搬移算法 [C4、R1:190-265] |
| 页大小 | 样本由旧 Pager/Btree 提供并从解密头调整；新参考提供 `legacy_page_size`，0 表示沿用 Btree 当前值，不是有保证的自动探测 [C15–C21、R1:104-118] |
| 空口令入口 | 派生算法一致；安装 codec 的空输入语义与“直接调用派生函数”不同，不能互相替代 [H1、H2] |
| 加密错误返回 | 样本页处理包装器不传播 AES 返回值；参考 `sqlite3mcAES128` 会将负结果转成 `SQLITE_ERROR`；正常合法整页字节不受影响 [C6、R2:135-140] |
| WAL/journal | 样本有模式分支；不据此声称当前读取端处理了边文件、恢复或并发快照 |

参考定位（所有仓库链接固定在同一提交）：

- **R1**：[cipher_wxaes128.c](https://github.com/utelle/SQLite3MultipleCiphers/blob/f29b06b424e556cf0ad8b19646b30dfc3e3e36d8/src/cipher_wxaes128.c)：24–35 参数；104–130 页长/reserved/salt；132–188 派生；190–234 legacy 页处理。
- **R2**：[codec_algos.c](https://github.com/utelle/SQLite3MultipleCiphers/blob/f29b06b424e556cf0ad8b19646b30dfc3e3e36d8/src/codec_algos.c)：19–55 RC4；58–65 MD5；73–90 IV；94–141 页密钥与 AES。
- **R3**：[cipher_common.c](https://github.com/utelle/SQLite3MultipleCiphers/blob/f29b06b424e556cf0ad8b19646b30dfc3e3e36d8/src/cipher_common.c)：12–13 padding；607–623 截断与补齐。
- **R4**：[rijndael.c](https://github.com/utelle/SQLite3MultipleCiphers/blob/f29b06b424e556cf0ad8b19646b30dfc3e3e36d8/src/rijndael.c)：软件轮、1123 起块加密、1317 起块解密；调用的是 Block 接口而非 Pad 接口。
- 参数说明：[官方 aes128cbc](https://utelle.github.io/SQLite3MultipleCiphers/docs/ciphers/cipher_aes128cbc/)；语义以固定源码和样本指令为准。

## 7. 合成已知答案与执行结果

### 7.1 输入与可重放向量

人工口令：ASCII `nr-codec-synthetic-only-2026`，不是客户端实际口令。

```text
P hex        = 6e722d636f6465632d73796e7468657469632d6f6e6c792d32303236
U            = 6e722d636f6465632d73796e7468657469632d6f6e6c792d3230323628bf4e5e
owner digest = 5a00344f40d0a5c52b160b830e6e086e
owner key    = 708878eebc64d45e5463a514a3740226e3be18e60e30ccfe75af787997b070bf
K            = 846b716d2005a787d5267a9b467b69b2
```

固定明文为第 i 字节 `(i*17+3)&255`；页 1 的前 16 字节替换为 SQLite 魔数。选例：

| 页号 | Kp | IV | 1024 字节密文 SHA-256 |
|---|---|---|---|
| 1 | `89352c516654ed16d49ed99ebb104dd1` | `20d7420f9c37a35dca6fe92a1c6999a9` | `4695e97292da451e0285865333a4df97cec7c8d22803951695db8077fe2a0a1a` |
| 2 | `0e65b7dbe9f1f99322c99f08d8f783aa` | `b2547aac5270eef583e1d267a725d988` | `b2ee35c1482f91eba9c965c059f64aa07b74704ae5d731003c35d24b63c20bdc` |
| `0x01020304` | `6fd3592abcfdc3b78ba6992ce40c62c1` | `d0bc39bf7af11bf6c7d8f15940a28f12` | `6b3346648444caa94b55b2c3aa38779c285dc4966b4dffdb21e8270a79a9477f` |

最后一例页密钥输入中的页号是 `04 03 02 01`，可发现字节序错误；40 个页向量的页号为 `1,2,256,65537,0x01020304`。JSON 保存全部密文、IV 摘要输入和页密钥输入，不仅保存哈希。

### 7.2 三方验证，不执行样本

[verify_codec.py](verify_codec.py) 的样本侧转写包括：

- 从指令读取 MD5 的 64 个常量和旋转、完整 MD5 压缩、结束与小端输出；每次与标准库摘要比较。
- 口令截断/补齐、RC4 状态重建、迭代顺序与次数。
- IV 的 x86 高位乘法、位移、32 位回绕和字节序。
- 样本 AES 常量表、密钥扩展、正逆轮、CBC；全部表值还经过有限域计算校验。

[reference_probe.c](reference_probe.c) 在同一翻译单元包含固定源码，只公开合成派生/材料/页处理入口。参考 SQLite 的真正页读取仍使用该版本自身的 codec/VFS，不由 Python 替代。另用 OpenSSL `enc -aes-128-cbc -nopad -nosalt` 检查整页结果。

合成数据库由 Python SQLite 3.46.1 建表、写入人工记录，再由样本转写逐页加密；参考 SQLite 3.54.1 **直接只读打开加密文件**，不是先解密成普通数据库再查询。8 种页长均成功；库包含多页，覆盖 schema 和联系人 B-tree 的实际读取；没有单独构造溢出页或损坏页测试。

查询只取 `Type=1` 的 `Id,Data`，按 `<U><Name>` 文本精确匹配，唯一结果为人工 `synthetic-target-id`。该测试不取代原联系人报告的 18 项匹配边界测试。

所有库拒写：`SQLite 8: attempt to write a readonly database`；错误人工口令均为 `SQLite 26: file is not a database`；三个边文件名逐一在打开之前拒绝。每个加密库的查询前后哈希见结果文件，全部相同。

### 7.3 构建、命令与重放

环境：Debian amd64；GCC `14.2.0`；Python `3.13.5`；OpenSSL `3.5.7`。参考库默认 cipher 编译为 AES128，但运行时仍明确设置 `legacy=1` 与准确页长，不通过全局旧格式宏掩盖配置。

构建选项由 [build_reference.py](build_reference.py) 固定：`-O2 -shared -fPIC -Wl,-z,defs`；`CODEC_TYPE=CODEC_TYPE_AES128`；关闭 AES 硬件加速及 AES256、SQLCipher、RC4 cipher、ASCON、AEGIS；保留 `HAVE_CIPHER_CHACHA20=1`，因为该参考版本的随机数生成也需要其实现。**保留依赖不表示以 ChaCha20 格式测试数据库。**

脚本下载固定提交的仓库归档，但只解压其中的 `src/`；归档 SHA-256 为 `dfbfb8651ff8b1d6fe14ee928ea002faf351d1ef37bb9a452ba1be87e23c1fe4`。下载、解压与构建分别使用新目录；编译器从独立构建目录运行，所有源码路径为绝对路径，Python 使用 `-I`。没有解压或读取参考仓库测试目录中的数据库样本。

从项目根目录重放（工作目录必须尚不存在）：

```sh
REPORT="$PWD/docs/research/reports/nr-codec"
WORK="/tmp/nr-codec-replay-example"
python3 -I "$REPORT/build_reference.py" --work-dir "$WORK"
python3 -I "$REPORT/verify_codec.py" \
  --library "$WORK/build/libnr_reference.so" \
  --expect-vectors "$REPORT/synthetic-vectors.json" \
  --vectors "$WORK/replayed-vectors.json" \
  --results "$WORK/replayed-results.json"
```

输出文件独占创建，不覆盖已有结果。参考源码 SHA-256、完整编译命令、产物哈希由构建脚本记录到 `build/build-manifest.json`，本次实际记录保存为 [reference-build.json](reference-build.json)。重放不需要客户端样本、实际密钥、真实数据库、Windows 或客户端进程。

已实际从全新工作目录 `/tmp/nr-codec-replay-20261023` 执行上述两个脚本，编译器 stderr 为 0 字节，`--expect-vectors` 对全部保存向量比较通过；[replayed-results.json](replayed-results.json) 再次记录 7 个派生向量、40 个整页向量和 8 个数据库全部通过。参考共享库 SHA-256 为 `4e6d70635701d2691b287f6ecfd3ae362c21ec41f5e19a2d52075eaf3ec04f87`；保存的向量文件 SHA-256 为 `e1a4f4d3a7f206d8aacfbcf0a44af09eaf8c07bf1ffd10d7999a8c47cc767f06`。

现有 Go 模块没有代码改动，仍实际运行并通过 `go test -count=1 -timeout 45s ./...`、`go vet ./...` 及对应的 `GOARCH=386` 两项检查；这些 Linux 测试没有向 Windows 客户端发送消息。

本轮首次参考库构建曾因禁用 ChaCha20 留下两个未解析随机数符号；检查后保留其实现并以 `-Wl,-z,defs` 重新构建成功。首次完整测试还发现一个**错误的测试预期**：“传错 legacy_page_size 必须读失败”。参考 SQLite 可以从解密后的第一页重新调整大小，故该断言不成立；已去掉它，改为直接验证从前 32 字节恢复并明确配置正确大小，保留错误口令拒绝测试。失败不作为通过结果隐藏；最终结果来自修改后的完整成功运行。

## 8. 完成范围与下一步

| 项目 | 状态 |
|---|---|
| MD5、输入长度及用途 | 已完成，包装器与基础派生直接使用原语的区别已说明 |
| IV 的整数、常量、小端、摘要 | 已完成，样本整数转写与固定源码逐项相同 |
| 页处理/AES 调用寄存器、栈实参 | 已完成，以调用点指令为准 |
| padding、口令长度、截断、空口令规则 | 已完成；没有读取或公布实际密钥字节 |
| 页大小来源、第一页前初始化 | 已完成；没有把默认值冒充真实页长 |
| 第一页、后续页、偏移、reserved | 已完成；完整页 CBC，无新格式头搬移 |
| 合成向量、只读加密库查询/拒写/哈希 | 已通过，详见结果与完整向量 |
| 实际 nr 的页头、schema、真实目标 ID | **未读取、未取得**；需要另行安排私有副本授权验证 |
| Windows 读取工具、实例/身份归属 | 未实现或运行 |
| 活动库、WAL/journal/恢复/并发复制 | 不支持，本轮不扩展 |
| `SendUser` | 未开发、未运行、未授权发送 |

下一步可以按上述契约另行实现独立只读读取器；真实私有副本的取得与读取验证仍需单独安排。本次不继续该步骤。

## 9. Evidence 索引

### 9.1 新证据

| 别名 | 文件与用途 | 原 Evidence ID |
|---|---|---|
| C1 | [0003](0003-read_function_instructions.json)，MD5 包装器 | `ev_c806ad2435a36e591320f73a0a69c92b0497f6293f15aaf7b57f2140f495918d` |
| C2 | [0010](0010-read_function_instructions.json)、[0011](0011-read_function_instructions.json)、[0019](0019-read_function_instructions.json)，MD5 完成/更新/压缩 | `ev_55a2068645328b36fac548ffa736376f7ac31f508c6c69d298f849bbb0b81ffb`；`ev_10227aaee4aa5405b1f5761ade452f7a3f2ddbdac662bf16274a389510aed762`；`ev_85d231cb1c2a918b0c8956fa875680ce96098fdad7a5eb9e4fc90af226ccf813` |
| C3 | [0014](0014-xrefs.json)，MD5 包装器调用点 | `ev_1e6baee8b5c99c0011cbd7b3ad5ab7c717ddc27c9246810c7471782e825e44f1` |
| C4 | [0005](0005-read_function_instructions.json)，codec 模式/页长/基础密钥/缓冲区 | `ev_34e58ea853448d51fd782eb2d0792cbcb3959d89a22865a9140091b17539cfe4` |
| C5 | [0008](0008-read_bytes.json)，32 字节公开 padding | `ev_07c5339ab8bcb0149152bc038bc36081e72d2f6ee3655ba3124f8261c89b0598` |
| C6 | [0006](0006-read_function_instructions.json)，页密钥、IV 和 AES 调用点 | `ev_dc9417255b034e9b3f2998a0a46893b00e13adba50aa3d704675a6d04069af25` |
| C7 | [0012](0012-read_function_instructions.json)，口令补齐 | `ev_1bec9a81eee3b2c70e9b177158e09b2e7f54841355fe3f492c0d630d3f99a670` |
| C8 | [0013](0013-read_function_instructions.json)，基础密钥派生 | `ev_dd81d10f9ee2a28d772e7827f10410572a443df8c217b6882d917c222e42d2da` |
| C9 | [0004](0004-read_function_instructions.json)，IV 整数与字节序 | `ev_d7d5c5c66489b5c5bf3d378bb598e9e26ee25fd53bf33e4e93ebcca0fe543c1c` |
| C10 | [0009](0009-read_function_instructions.json)，AES 初始化 | `ev_afd7ea3d6d2ae950259d86aec1c8bc1d9b0a2518b7dafff47ba35f1e801f3bdf` |
| C11 | [0044](0044-read_bytes.json)，模式跳转表 | `ev_3606076e40fd4285c4d4e2aefa32b4df88528447d74e7317619ed37c195ef797` |
| C12 | [0031](0031-read_bytes.json)、[0032](0032-read_bytes.json)、[0042](0042-read_bytes.json)，AES 非敏感常量表 | `ev_208bdd5d0d406f6b3e7305ab8a7b328efa30e197fb162eef50720ef84ca550ac`；`ev_9b9c48476de1bd09b40a68228f28e5387ec05277d3266538f7284f590f711eb4`；`ev_f7ae43b7377309b450d6ff7f7ee1cae37c46151b5f1e85e69fa5a25eab68a986` |
| C13 | [0020](0020-read_function_instructions.json)、[0021](0021-read_function_instructions.json)，CBC 正逆页处理 | `ev_1324f9e81e6ebd805ccd3e906da7432fb189ce497d6491b75e59763f51ace732`；`ev_2a446ac378f74bff2199e01f317460709d54a06d9cbf8497236d56fb82d70908` |
| C14 | [0035](0035-read_function_instructions.json)、[0039](0039-read_function_instructions.json)、[0040](0040-read_function_instructions.json)、[0041](0041-read_function_instructions.json)，扩展/逆混合/正逆轮 | `ev_3707eaa737b59246e1f96ec4cfa50cdc51e027d9da590696800e1607b3a41e78`；`ev_8b9eb65de47e036910a76d17ead217cb43979f45ebbbb75e6df59b10096c808a`；`ev_63fc2e757a0d4b9b8d736634172cd3c3d763eef60a3b440da902cf1e4f15c1e2`；`ev_24a9c984a1e126bf72777b1e758d825297c5e2be35ba9560a6adad1123176b36` |
| C15 | [0007](0007-read_function_instructions.json)，大小回调只有 RET | `ev_8dc5f77215d516523c93ab5c955c8bc24a26c7153a6f177d081a99439866de5b` |
| C16 | [0025](0025-read_function_instructions.json)，Btree 打开与页头大小检查 | `ev_e8eaf81bea4f92ab296ec43c739d53aef07ef26b3b0bb6b47c3f5113673b4dc3` |
| C17 | [0034](0034-read_function_instructions.json)，原始前 100 字节读取 | `ev_65d347a80d0b68bdaf604f91b730234f6cda97462beef149884bf0c418d698ca` |
| C18 | [0030](0030-read_function_instructions.json)，Pager 默认/上调页长 | `ev_21de57b91712b28b2d3d3f96f090a4bf8c51a76295e2b3677d24355577e0d954` |
| C19 | [0045](0045-read_function_instructions.json)，底层扇区大小来源 | `ev_8792df22aba5d24d228a7c1b33c3d8f4a02ad3892c3adbbcc8cd3e505dc7180a` |
| C20 | [0033](0033-read_function_instructions.json)，页 1、解密头检查/页长调整 | `ev_0bcd56df042fc0929b624b5890437d860e27721585b8ef094fe464deac1cc8c0` |
| C21 | [0038](0038-read_function_instructions.json)、[0029](0029-read_function_instructions.json)，磁盘偏移、codec mode 3、Pager 设置页长 | `ev_40fbd1f6690787dbd7a0c1b7505efccbb8cee7945df02a3567c2586e1a036ce9`；`ev_1793730579cbc56b7bdcefb755cdaface991890e65e000f00969c2ef75ff132a` |

### 9.2 复用证据

| 别名 | 文件 | 原 Evidence ID |
|---|---|---|
| H1 | [qgroup-id/0024](../qgroup-id/0024-read_function_instructions.json)，窄口令 strlen 与传参 | `ev_8ede1456c04057b1c31b0a083aff7913c5be1e1ab68f536764853b10590915c7` |
| H2 | [user-id/0138](../user-id/0138-read_function_instructions.json)，codec 安装与空输入分支 | `ev_17eb8090a36855459a758bf95a10a3068b720347e2b7e623abaa67e90cc7651a` |
| H3 | [user-id/0139](../user-id/0139-batch_decompile.json)，RC4 及页派生；[0134](../user-id/0134-batch_decompile.json)、[0137](../user-id/0137-batch_decompile.json)、[0141](../user-id/0141-batch_decompile.json) 作历史对照 | `ev_2b1598dd7045ee9b3805ef67f43440a82b73038c56583dc19ba8c551559e101c`；`ev_f2b53abd9bdf3b1f26435de71b20670a80d19a715f37cb6c6146a043decd35e4`；`ev_c85b51650d9c97356d050a1a809773e4cfface05f2aa8c0d3585c471c3905014`；`ev_242df3b5a4eedc0d25f0cb03cdef55bb45e8172e61854ea331a2ff026cd8dacf` |

### 9.3 保存与限制

[nr-codec-evidence.json](nr-codec-evidence.json) 为本轮 43 条完整 Evidence 记录，不含实际密钥字节；[0047](0047-export_evidence_bundle.json) 是导出回执，[0048](0048-close_binary.json) 确认分析会话已关闭。原始小文件保留探索记录；`0015` 是一次使用错误参数名的请求，不是有效证据；`0016–0018` 名称搜索未命中，不用于证明函数不存在。请求 `0x691972 / 0x6915a6 / 0x688e08` 分别按规范入口解释，不把内部地址当新函数。

首次默认 MCP 会话在 330 秒启动期限内未完成 Ghidra 分析，已关闭；随后采用项目已有的独立 REA 保存方式，将本次进程等待设为 1200000 ms，完成同一配置的分析，没有修改客户端或 REA 配置。没有因为超时改用未完成分析结果。`unknowns=0` 只表示未登记机器条目，不改变第 8 节的未验证范围。
