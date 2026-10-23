# WM_COPYDATA 自定义正文：长度、换行与字符处理的静态契约

本文记录已验证 IPC 路径中的正文转换和群/单聊序列化约束；不修改 Adapter，不开放任意正文，也不增加真实发送授权。

## 1. 范围与结论

- 样本：内网通 3.4.3055 x86，SHA-256 `e7caef3fe744f9fe6ba90dd1d53b7e64241cb54020a8bf37be5e756e2fe881b1`，ImageBase `0x400000`。
- REA 6.7.0 / Ghidra 12.1.4；全部结论来自磁盘样本的静态分析。未运行客户端、发送消息、读取运行内存、注入或修改样本；没有长度扫描或畸形载荷实验。
- **没有得到可以写入 Adapter 的统一原始正文长度上限。** 已确定群与单聊的外层 XML 格式化缓冲区都是 `0x40000` 个 Windows `wchar_t`，包括结尾 NUL；这限制的是转义后、加入全部外层字段的 XML，不是原始正文的 UTF-8 字节数或字符数。
- 共用正文处理先把四字符字面串 `\r\n` 替换为实际 CRLF，再构造文本片段 JSON。实际 LF、CRLF、单独 CR 在普通文本片段中分别保留，以 JSON 的 `\n`、`\r\n`、`\r` 表示；没有证据支持统一换行转换。
- 正文经过 **UTF-8 → UTF-16 → 文本片段 → UTF-8 JSON → UTF-16 JSON → XML 风格转义 → 外层 XML → UTF-8**，不是直接放进 XML，也不是 CDATA。
- 中文和有效 emoji 在这条发送端转换路径中保留；JSON 和外层转义都由客户端执行。接收端字体、排版及长消息处理没有运行验证。

下文 `[Bxx]` 的完整 Evidence ID 和查询文件见第 7 节。

## 2. 共用正文构造与长度单位

### 2.1 IPC 边界

`0x449c60` 从载荷 `0 / 0x64 / 0x12c` 读取三个 NUL 终止的 UTF-8 字符串。文本分支没有按 `cbData` 限定扫描范围；原始入口依据见 [IPC 契约第 2 节](../../ipc/ipc-contract.md#2-接收端字段与实验值)。

`0x44cbd0` 的两个 `MultiByteToWideChar` 调用使用 `CodePage=65001`、`dwFlags=0`、源长度 `-1`；查询返回的宽字符数包含 NUL，再据此动态分配。字符串对象的逻辑长度不包含 NUL。[B01]

因此必须分别计算：

1. 载荷偏移和 `cbData`：**UTF-8 字节**，且每个源字符串必须在传送区内有 NUL。
2. 内部宽字符串：**UTF-16 code units**，不是 Unicode code points；例如一个基本汉字通常占 1 unit，补充平面 emoji 占 2 units。
3. JSON 字符串转义：对 UTF-8 字节工作；反斜杠、双引号和控制字符会增加表示长度。
4. 外层 XML 格式化：**完整 XML 的 UTF-16 code units + 1 个结尾 NUL**。
5. 最终传输包：序列化 XML 转成 UTF-8 后的字节数，不发送字符串结尾 NUL。

### 2.2 `0x4b8470 → 0x4b4a70` 不提供固定正文配额

- `0x4b85cf: CALL 0x4b4a70` 构造片段，`0x4b85de: CALL 0x5f8380` 序列化共用内容。[B02]
- `0x4b4aa6: CMP [ESI+0x14],0`、`0x4b4aae: JZ 0x4b4beb`：原始片段为空就返回。
- `0x4b4abb: CALL 0x4b4480` 查找 URL；没有匹配时，`0x4b4adc: CALL 0x5f6260` 把**完整原始宽字符串**放入普通文本片段，未按一个固定正文长度截取。
- 有 URL 匹配时，`0x4b4afe: CALL 0x4b4930` 按匹配文字分段，`0x4b4b3c` 递归构造，`0x4b4b73: CALL 0x5f66c0` 加入链接片段。这不是按字数切成多条消息。[B03、B04]
- `0x5f62ca` 和 `0x5f6727` 通过 `0x407fe0` 的起点 0、长度 `0xffffffff` 复制完整片段，再加入动态容器；只检查空片段，没有固定正文容量检查。[B05、B06]

`0x4b4480` 的空格分割用于 **URL 候选识别**，不能解释为“所有正文都按空格切开后重组”。普通正文没有 URL 时依然从原字符串完整复制。URL 正则和逻辑见 `0x4b4030`；链接识别会使 JSON 结构和开销随内容变化。[B04、B07]

在上述完整静态范围中，没有发现原始正文的业务长度阈值或按固定长度保留前缀的操作。没有把 STL 内联容量 `7/8`、窄字符串容量 `15/16` 当作正文上限；历史 `0x569610` 的 `0x7ffffffe` 是宽字符串实现的长度异常检查，也不是业务可用上限。

### 2.3 `0x5f8380` 的正文表示

普通文本片段的正文在 `0x5f8bba` 调用 `0x408950` 转为 UTF-8；`0x5f8bef–0x5f8c13` 赋给 JSON 的 `txt.v`，片段集合赋给根对象 `dt`。根对象还含 `ver / app / id / type / ft` 等客户端字段。[B08、B09]

`0x5f9a65: CALL 0x70dab0`，经 `0x715b50 → 0x715bf0 → 0x7154f0` 写出 JSON；`0x5f9a87: CALL 0x44cbd0` 再转回 UTF-16，交给群或单聊发送。[B08、B10、B11]

`0x5f8380` 中的 `wchar_t[128]` 用于字体大小的 `_itow_s(...,0x80,10)` 和颜色的 `_swprintf_s(...,0x80,L"0x%02x%02x%02x",...)`，不是正文缓冲区。JSON 转义器 `0x715649` 的 `2*n+3` 是动态字符串预留量，不是截断阈值；遇到 `\u00xx` 等较长表示时继续追加。[B08、B12]

## 3. 群与单聊的外层长度约束

### 3.1 群与单聊的前置判断、实际格式串

群路径 `0x617600 → 0x6114f0` 检查必要状态、目标和非空内容，然后构造类型 `0xbc3`；未见正文长度与业务配额比较。[B30、B31]

单聊 `0x4b9320` 的 `0x4b9394–0x4b93e2` 检查消息类别/非空状态，`0x4b95b6` 调用 `0x5bc200`；`0x5bc2c5` 转交 `0x60d930`。这些指定函数未见正文配额或按长度截断；SSO 容量判断不是限长。单聊类型 `0x3ec` 的分发表 `0x657078 → 0x656a8b → vtable 0x9e9560+4` 指向 packer `0x65ca70`。[B32、B33、B34、B35、B36]

群 packer `0x679c10` 在 `0x679e57` 调用 `_swprintf_s`；单聊 packer `0x65ca70` 在 `0x65cd60` 调用。两者目标缓冲区均为 `0xaa84f0`，容量均为 `0x40000`。[B20、B21]

群格式串 `0x9eeea0`：[B22]

```xml
<X_QGROUP_SEND_MSG docver="%u"><QGROUP_ID>%s</QGROUP_ID><MSG_ID>%llu</MSG_ID><MSG>%s</MSG><HIDE_RECORD>%u</HIDE_RECORD></X_QGROUP_SEND_MSG>
```

单聊格式串 `0x9ea718`：[B23]

```xml
<X_SEND_MSG docver="%u"><MSG_ID>%llu</MSG_ID><RECEIPT>%u</RECEIPT><MSG>%s</MSG><MSG_TIME>%llu</MSG_TIME><OFFLINE>%u</OFFLINE><HIDE_RECORD>%u</HIDE_RECORD></X_SEND_MSG>
```

### 3.2 已明确的容量规则

令 `J` 为 `0x5f8380` 输出的**完整 JSON 宽字符串**，`E` 为 `0x659a80` 的六项替换，`W` 为把全部字段代入相应格式串得到的**完整 XML**。两个已定位 packer 的必要格式化条件均为：

```text
utf16_units(W) + 1 <= 0x40000
```

- `0x40000 = 262144` 是包含结尾 NUL 的宽字符容量，实际字节容量为 `0x80000`。
- 可容纳的 XML 内容最多为 `262143` UTF-16 code units。
- 容量包含 `E(J)`、XML 标签、目标 ID、消息 ID、时间及其他格式字段；不是给原始正文独占的容量。
- `E(J)` 的长度取决于正文、JSON 的引号与排版、片段数、客户端补充字段。仅检查 `len(body)`、rune 数或 `utf16_units(body)`，无法证明整个 XML 可以格式化。

令 `p64(v)` 为实际 64-bit 值用 `%llu` 格式化后的十进制位数。当前这两个 packer 的 `docver=1`，布尔字段都通过 `!=0` 规范为一位 `0/1`，可进一步精确写成：

```text
群：   u(W) = 129 + u(groupID) + p64(msgID) + u(E(J))
单聊： u(W) = 153 + p64(msgID) + p64(msgTime) + u(E(J))
```

129/153 已计入所有标签、`docver` 和布尔字段，不含上述变量值；公式由原始格式串及实际参数计算，不是测出的可发送正文上限。[B20–B23]

`E` 对其**输入中的原始字符**计数时，有：

```text
u(E(J)) = u(J)
        + 4*count('&') + 5*count('"')
        + 3*count('<') + 3*count('>')
        + 5*count('\'') + 5*count(ASCII space)
```

其中 `u` 表示 UTF-16 code units。`&` 最先替换，后续生成的实体不会再被本函数的 ampersand 替换处理。不要把这条公式用于尚未完成 JSON 序列化的原始正文。

### 3.3 超长不是自动截断发送

使用的是 `_swprintf_s`，不是 `_snwprintf_s(...,_TRUNCATE,...)`。静态调用为 `0x882549 → 0x882188 → 0x882066`：

- `0x8820e4` 把 wchar 容量乘 2；`0x882108–0x882143` 为终止 NUL 留出两字节。空间耗尽分支 `0x88214a–0x882158` 返回 `-2`。[B24]
- `0x8821e6–0x8821ec` 对负结果将目标首 wchar 清零；`0x8821ef–0x882204` 对 `-2` 设置 `errno=0x22`，调用 invalid-parameter 处理；处理函数若返回，格式化返回 `-1`。[B25]
- `0x8803c8` 优先调用已注册 handler；没有 handler 时进入 `0x8802a0`，其默认路径调用 `TerminateProcess(...,0xc0000417)`。本轮未检查应用是否注册 handler，不能断言所有真实超长都一定终止进程。[B26、B27]
- 群 `0x679e5c` 和单聊 `0x65cd65` 后都不检查格式化结果 EAX；附近的容量比较用于释放临时字符串，不是成功判断。若 handler 返回，仍在 `0x679e7c / 0x65cd85` 把**已清空的 XML**转成 UTF-8 并交给打包器。[B20、B21、B28、B29]

因此可能进入默认终止路径；即使 handler 返回，也不是发送一份正常截断的 XML，更不是一个能由 Adapter 可靠识别的“正文过长”返回。WM_COPYDATA 的正常 LRESULT 又不是下级业务状态，所以**不能靠 SendMessageTimeoutW 完成或 LRESULT=1 判断正文被接受**。

### 3.4 UTF-8 frame 的长度

共用 `0x67b3b0` 在 `0x67b470–0x67b479` 以 byte NUL 扫描 UTF-8 XML，得到 `n`；`0x67b48b` 使用 `n+8`。`0x67b4a6 / 0x67b4be / 0x67b4ca` 写入 4 字节长度、4 字节类型、`n` 字节 XML，**不写 XML 的 NUL**。群类型为 `0xbc3`，单聊为 `0x3ec`。[B37]

`0x67b43b` 的 `0x200` 是动态初始容量，不是 512 字节消息上限。写入辅助 `0x707070` 在 `0x70708a–0x7070cd` 检查余量，不足时按 `(当前长度+本次长度)*2` 分配。[B38]

这些证据只证明指定发送端 packer 和 frame 的容量/长度单位，不证明更后面的传输层或接收端没有其他限制。

## 4. 换行与反斜杠

`0x4c41b0` 在目标分流前执行替换：

```text
0x4c4223: PUSH 0x4
0x4c422a: PUSH 0x9d1580  ; UTF-16: 005c 0072 005c 006e
0x4c4244–0x4c424a: 压入替换串、匹配串、原正文
0x4c4253: CALL 0x568ef0  ; CStringHelper::Replace
```

替换串 `0x9b6860` 的字节是 `0d 00 0a 00 00 00`。`Replace` 从起点 0 反复查找匹配，再用 `0x569610` 替换；没有引号或反斜杠保护状态。[B13、B14、B15；Replace 的既有依据见 IPC 阶段五]

| Adapter 原始正文中的字符 | 进入普通文本片段的字符 | JSON 中正文值的词法表示 |
|---|---|---|
| 实际 LF，U+000A | LF | `\n` |
| 实际 CRLF，U+000D U+000A | CRLF | `\r\n` |
| 单独实际 CR，U+000D | CR | `\r` |
| 字面四字符 `\r\n` | **实际 CRLF** | `\r\n` |
| 字面两字符 `\n` 或 `\r`，未形成完整四字符匹配 | 仍为反斜杠与字母 | 反斜杠再由 JSON 写成 `\\` |

JSON 中看见 `\r\n` 并不证明原文是四个字面字符：原始 CRLF 和被替换的四字符串都会产生这一表示。

**未发现让四字符字面串 `\r\n` 原样通过该入口的方法。** 这里没有 C/JSON 风格的 `\\ → \` 输入解码；双写反斜杠不会在后面自动还原成期望的四个字符。也不能预先写 XML 实体来绕过替换：输入不是 XML，实体文本会被当作普通正文再转义。

普通文本保留 CR/LF 的依据是原字符串完整复制以及 JSON 写出器的独立分支：`0x71571b` 追加 `\n`，`0x71572d` 追加 `\r`，没有 CRLF 合并或分拆逻辑。[B03、B05、B12、B16]

以上是发送端的字段值语义，不声称已观察到单独 CR 在接收 UI 上造成什么排版。

## 5. 中文、emoji 与特殊字符

`0x408950` 在 `0x408998 / 0x4089cc` 调用 `WideCharToMultiByte(65001,0,source,-1,...)`，按需要分配 UTF-8 缓冲区；不是 ANSI code page 转换。[B17]

`0x7154f0` 的 JSON 转义规则：

- `0x7156d3`：双引号变成 `\"`。
- `0x7156e5`：反斜杠变成 `\\`。
- `0x71571b / 0x71572d`：LF / CR 分别变成 `\n / \r`。
- 另有 backspace、form feed、tab；其余 `U+0001..U+001F` 进入 `\u00xx` 分支。
- `0x7159a0` 只把字节值 `1..31` 判为这类控制字符；其他字节在 `0x715845–0x71584f` 原样追加。因此中文和 emoji 的 UTF-8 多字节不会被该写出器裁成 BMP，也不强制改成 `\uXXXX`。[B12、B16、B18]

外层 `0x659a80` 再依次执行：

| 输入字符 | 外层表示 |
|---|---|
| `&` | `&amp;` |
| `"` | `&quot;` |
| `<` | `&lt;` |
| `>` | `&gt;` |
| `'` | `&apos;` |
| ASCII 空格 | `&nbsp;` |

六个替换调用点依次为 `0x659acb / 0x659adf / 0x659af3 / 0x659b07 / 0x659b1b / 0x659b2f`；`0x659b70–0x659b83` 按 16-bit NUL 扫描最终长度。辅助 `0x5e2a80` 根据替换后的长度动态分配，没有该层的业务正文阈值。[B19、B39]

注意 `&nbsp;` **不是 XML 的五个预定义实体之一**，所以这里称为客户端的 XML 风格转义，不能拿通用 XML serializer 直接替代它。本轮没有证明接收端如何反转该实体。

| 原始正文 | 发送端如何表示 |
|---|---|
| 中文 | UTF-8 输入转 UTF-16；文本 JSON 中仍是对应 UTF-8，外层转换不改这些字符 |
| 有效 emoji | 输入转 UTF-16 代理对，再转回对应 UTF-8；宽字符长度按 2 units 计算 |
| `< > &` | JSON 文本值中不作 XML 转义；外层统一变成 `&lt; / &gt; / &amp;` |
| 双引号 | 内层先变为 JSON `\"`；外层其中的引号再变成 `&quot;`，反斜杠保留 |
| 单引号 | 内层原样；外层变成 `&apos;` |
| `]]>` | 内层原样；外层变成 `]]&gt;`，不形成 CDATA 结束标记；没有 CDATA 拼接步骤 |

上述表示不会把这些字符当成用户提供的 JSON/XML 结构。接收方按客户端相应格式解码后才能取得文本值；本轮没有运行验证接收渲染或另一个版本客户端的解码行为。

## 6. 可用于后续 Adapter 的规则与仍未知的部分

### 6.1 可以直接采用的输入与载荷规则

1. 在外部输入边界要求**有效 UTF-8、无嵌入 NUL**。客户端转换使用 flags=0，不替 Adapter 严格拒绝非法 UTF-8。
2. 不 trim、不截断、不自动拆成多次发送；空正文明确拒绝，不把空字符串的内部提前返回当作可诊断的发送错误。
3. 如 API 承诺“收到什么就发送什么”，遇到字面四字符 `\r\n` 应明确拒绝并说明原因，不能悄悄改写。也可以在未来接口明确暴露客户端原有的替换语义；这属于接口选择，不是本轮已经实现的功能。
4. LF、CRLF、单独 CR 都直接发送实际字符；调用方需要统一换行时，应在自己的明确业务规则中处理，不伪称这是客户端要求。
5. **不预先做 JSON/XML/HTML 转义。** `< > & ' " ]]>` 等直接作为原始 UTF-8 正文交给客户端；预转义会改变收到的正文。
6. 选择器、目标、正文继续分别从 `0 / 0x64 / 0x12c` 写入；各自 NUL 在传送区内，不覆盖相邻字段。目标的既有授权校验不变。
7. 若未来获准构造可变正文的最小三字符串载荷，可使用 `cbData = 0x12c + len(bodyUTF8) + 1`，按 UTF-8 **字节数**计算并检查长度算术及 Win32 `DWORD` 可表示范围；这是接收端已知读取范围的构造方法，**不是已恢复的厂商完整结构或任意长度发送保证**。
8. 保留单次日志、明确授权、一次调用不重试和独立接收观察；序列化失败不能作为自动换 AttemptID 重发的理由。

### 6.2 不能据此声称完成的校验

- 没有恢复统一的原始正文最大 UTF-8 字节数、最大 rune 数或最大 UTF-16 units；不填写猜测的“安全上限”。
- 已知 `utf16_units(W)+1<=0x40000` 只约束两个指定 packer。Adapter 没有完整客户端状态、最终 JSON 和所有格式字段时，不能仅靠原文长度精确计算 `W`。
- 没有恢复厂商 IPC 生产者完整结构和其原始正文数组容量。
- 未证明角色、离线保存/补发、其他发送配置或接收端使用相同上限；未证明超长会在所有运行配置下统一返回错误而不终止进程。
- 接收端对 LF/CR、emoji 字形、URL 识别后的展示和非常长消息的行为仍没有运行证据。

**工程决定：本轮不改 `internal/ipc/payload.go`，不增加任意正文入口；这些静态规则足以指导下一步接口设计，但不足以把“统一长度上限已完成”写进输入校验。**

## 7. Evidence 与复核方式

REA 的 `free @ 0x87fc86` 仍存在 hasNoReturn 误标。未使用截断伪代码来证明“之后没有逻辑”；关键调用以指令核对。对 `0x408950`、`0x4b4480`、`0x4b4a70`、`0x4b8470`、`0x5f8380` 另读取整个连续静态字节范围，与原 PE 对应文件字节逐字节比较，全部一致；结果见 `full-span-byte-verification.json`。

首次 Ghidra 默认自动分析超过 330 秒启动时限。本次另起独立 REA 进程，仅对该子进程设置 `REA_GHIDRA_STARTUP_TIMEOUT_MS=1200000`；未修改配置、analyzer 集合或样本。恢复后的 analysis profile digest 仍为 `7810ea9437da59780820ec6fedb47ce47390841a95d527ddb6c3b3700fe180cb`。

完整证据在 `body-evidence.json`；单次响应保存在同目录 JSON，原始反汇编和字节匹配用于复核，不伪称为运行轨迹。下表的 B 标号是本报告的定位简称，不是重命名 Evidence ID。

| 标记 | 内容 | 单次查询 | 完整 Evidence ID |
|---|---|---|---|
| B01 | UTF-8/NUL与动态分配 | [44cbd0-instructions.json](44cbd0-instructions.json) | `ev_89899b27794c743dad2e0e08164ad975f47b4c9993105d152c4258592679e4aa` |
| B02 | 共用内容构造 | [4b8470-instructions.json](4b8470-instructions.json) | `ev_c83cdc8ee6b746de0094d26fdb14305938dc67c7df11cfa0f19ff4f2801f1867` |
| B03 | 空片段、URL分流与递归 | [4b4a70-instructions.json](4b4a70-instructions.json) | `ev_2f64158c3515b70851f6fef47f8c47a760e124355aebe606747fd5989236e28b` |
| B04 | URL候选识别 | [4b4480-instructions.json](4b4480-instructions.json) | `ev_873976948d9473c5e2e179f49944f92395dd8062fd124bbd9ecd910ffeae9b0e` |
| B05 | 完整普通文本片段 | [5f6260-instructions.json](5f6260-instructions.json) | `ev_db2347251e12fc86553474298faf3d2ebfd01d349acadf20c63adb04b6e0ec7b` |
| B06 | 完整链接片段 | [5f66c0-instructions.json](5f66c0-instructions.json) | `ev_cc512700de3fd255936a19660c3269d20a5ef34819e7f03eb384c9450306b72b` |
| B07 | URL正则 | [4b4030-pseudo.json](4b4030-pseudo.json) | `ev_7a24aef00d0af32b0ce012e8abbe7020e3f45fe4601d8def7401d4aae75585ef` |
| B08 | 正文JSON与宽字符串 | [5f8380-instructions.json](5f8380-instructions.json) | `ev_938bf5f06cb284edba95b7ef9fe078b030535f4a5502c297abb2ec1d76b80b8e` |
| B09 | JSON字段常量 | [json-field-bytes.json](json-field-bytes.json) | `ev_75c25bf114e8b16429f49c1d302464399c34c192b2bb77139c5ce52fa4c5053b` |
| B10 | JSON writer入口 | [70dab0-instructions.json](70dab0-instructions.json) | `ev_a34a8ac31ef911de02f3c69eb5a8e8cbd4cd3e15b7301c9c25d2151123680707` |
| B11 | JSON字符串分派 | [715bf0-instructions.json](715bf0-instructions.json) | `ev_c32b161bccdcd87b25c16b68539f141d103a1acd4a4a05219534152d83297f55` |
| B12 | JSON quote完整指令 | [7154f0-instructions.json](7154f0-instructions.json) | `ev_0dca20f28bee520e3d6f325f24224b7fc8dd37e9a1d5b28cbb347ca23c97bc9a` |
| B13 | 字面换行替换与目标分流 | [4c41b0-instructions.json](4c41b0-instructions.json) | `ev_1ac48d41a61fa5a2855f4b68d9a83c51a57b894e0ad06e82fcec0ba285e9b307` |
| B14 | 四字符匹配串 | [literal-newline-bytes.json](literal-newline-bytes.json) | `ev_5342d29a07bc24b3f720cbf0b45521411a2bba8356edd792fc8a3edf7f2d932e` |
| B15 | 实际CRLF替换串 | [actual-newline-bytes.json](actual-newline-bytes.json) | `ev_847261941416a8d42170b7c5002aa43db7628bcf9bd9e876e170d0f73a519f80` |
| B16 | JSON转义输出常量 | [json-escape-bytes.json](json-escape-bytes.json) | `ev_3bb9e64a7b82b983679216dd28b31e2d7cd3baf6341688b65e6debaa5dccaf0b` |
| B17 | UTF-16转UTF-8 | [408950-instructions.json](408950-instructions.json) | `ev_06a57fdfca53d486600c382450bc7c9b60f0484ae2bd9b84dd5b48d67203e8d0` |
| B18 | JSON控制字节分类 | [7159a0-pseudo.json](7159a0-pseudo.json) | `ev_ff46c8c717accadde525c6a45fe098216e1baf9ada88d3215fa5be60150b0609` |
| B19 | 六项XML风格转义 | [659a80-asm.json](659a80-asm.json) | `ev_fbfe0feec9ccaecc1a579bda2a3f0ef1634f392b35b942591273d7f595013b04` |
| B20 | 群XML packer | [679c10-asm.json](679c10-asm.json) | `ev_0314d8189ef740ddfd5674fa8139e94cf1ad9373dc66dd1588b087d4095e7fa9` |
| B21 | 单聊XML packer | [65ca70-asm.json](65ca70-asm.json) | `ev_8b0e1bbbb5d05e149e91992e43b9e1217753c9622a274e5d10985542d5edea14` |
| B22 | 群模板 | [9eeea0-format-bytes.json](9eeea0-format-bytes.json) | `ev_c5400a479e5ba7dfb1afa0bd50c1f5046f33c4df1a9d59dbe937817c4edc8396` |
| B23 | 单聊模板 | [9ea718-format-bytes.json](9ea718-format-bytes.json) | `ev_353e3a6ca78d1196328dc6fbe2b6c08e0bca181290d05e466e99f5a2c1a46b46` |
| B24 | 格式化容量、NUL与-2 | [882066-instructions.json](882066-instructions.json) | `ev_93e29bc0f1ca4c0b7bf7584170cf105c89f65c6112d5d0d237e24f72f7bfdc67` |
| B25 | 清空目标与invalid parameter | [882188-instructions.json](882188-instructions.json) | `ev_60e53db9d4a657722f4b0ffb690b70e0af1005a85e526d62b381904ec0114332` |
| B26 | handler分派 | [8803c8-instructions.json](8803c8-instructions.json) | `ev_98b20d33f1ec9cc470cc716fd70f2c08d005c16465560aa8d119791ff20d5e13` |
| B27 | 默认TerminateProcess | [8802a0-instructions.json](8802a0-instructions.json) | `ev_f1957dc8f6d7695c85cb848e4eadeb8bdc892a121882e46744ec7e5c191bcfa4` |
| B28 | 群free后继续路径 | [679e57-bytes.json](679e57-bytes.json) | `ev_35fea66a2b1d381dc60fc3010d1638758d79e1e5346660ec50d2ab0fab3e30ba` |
| B29 | 单聊free后继续路径 | [65cd60-bytes.json](65cd60-bytes.json) | `ev_ca2603850b163a9fc21416fd53fef37f0970f11f2d4f4b8a2eb1860a9b0f5478` |
| B30 | 群入口检查 | [617600-pseudo.json](617600-pseudo.json) | `ev_17d599ba7a6250a059051f51594bdcf6194448246f10f3ac131758f1b83d01b0` |
| B31 | 群命令参数 | [6114f0-pseudo.json](6114f0-pseudo.json) | `ev_4e0da769ff15ee5e62bd452af61b0bda88030c244180a944999e9dc20a7d7643` |
| B32 | 单聊消息入口 | [4b9320-asm.json](4b9320-asm.json) | `ev_c6a30658e07285ac0e09fabf166441491ab36350fe6b4074d10d62efd63617ca` |
| B33 | 单聊会话包装 | [5bc200-asm.json](5bc200-asm.json) | `ev_58a0683baa97a3e655349293437ab86a56ca7519c2c2fcfd8b2937e5ac45aeaa` |
| B34 | 单聊命令参数 | [60d930-pseudo.json](60d930-pseudo.json) | `ev_568b4cb686f551544b37c852feea9e680ea84c7d659f84915220a8e48ee0d1c2` |
| B35 | 类型分派与vtable | [656900-asm.json](656900-asm.json) | `ev_c7f6193207c0e8238c93e835e0bb451125465a4e6c53e0bb8951acc1386f64dc` |
| B36 | 分派表 | [657068-bytes.json](657068-bytes.json) | `ev_e231b5bf12b7d83e7c646bedc766f8c92f0a40f132e525798c2426b86e63241f` |
| B37 | UTF-8 frame长度 | [67b3b0-asm.json](67b3b0-asm.json) | `ev_4537e047a9115b592401c8c67453019cd79b18419a198b082cdd5575b9c09b07` |
| B38 | frame动态扩容 | [707070-instructions.json](707070-instructions.json) | `ev_c30503a076800c93ce9c7f570b58580e9a1b668049975519ad2f37e56833f001` |
| B39 | 转义动态替换 | [5e2a80-pseudo.json](5e2a80-pseudo.json) | `ev_1df0b47a079d021309dd6804f5662fabd123b4aab161b242d9197883d9bba75d` |
