# 内网通 WM_COPYDATA 业务 IPC 契约

本文整理业务文本发送 IPC 的已有静态证据、未恢复字段及后续任务，不是可直接实现的发送协议说明。

## 1. 范围与当前结论

- 软件：内网通 3.4.3055，Windows x86 / 原生 C++ / SkinUI。
- 客户端：`/root/reverse-lab/extracted/app/ShiYeLine.exe`。
- SHA-256：`e7caef3fe744f9fe6ba90dd1d53b7e64241cb54020a8bf37be5e756e2fe881b1`。本轮只读计算的样本哈希与历史证据、快照一致。
- 历史分析工具：REA 6.7.0 + Ghidra 12.1.4，ImageBase `0x400000`。下文地址均为静态 VA；`FUN_` 名称是分析器生成的名称。
- 本轮仅阅读历史报告、证据和快照并整理文档，未重新反编译、未运行客户端、未发送消息、未修改 EXE、未生成发送器。

**已确认（静态）**：`0x449c60` 的业务分支比较 `dwData == 0x0fecf10f`，从 `lpData + 0x000 / 0x064 / 0x12c` 分别读取选择器、目标值和正文，均经 UTF-8 转换后进入 `0x4c41b0`。`user_id` 和 `group_id` 分支分别调用既有原生单聊和群聊发送过程。[E22、E23]

**尚未恢复**：生产者载荷结构、字段容量、`cbData` 合法值、正文上限、可靠窗口定位及完整返回值契约。因此目前不能编写实际发送器。

证据状态的含义：

- **已确认（静态）**：已有指令或伪代码直接支持，不代表执行时已验证。
- **强推断**：已有数据流支持，但仍缺少生产者或完整实现证据。
- **未知 / 未验证**：现有证据不足，或必须在另行授权的运行测试中确认。

## 2. 已确认的接收端字段

### 2.1 COPYDATASTRUCT

以下是目标 x86 接收端的结构访问，不是跨架构发送端的布局承诺。

| 项目 | 静态事实 | 证据位置 |
|---|---|---|
| 处理入口 | `0x449c60` 接收 `tagCOPYDATASTRUCT *`；空结构指针有提前返回路径 | E22-P；E22-I：`0x449ca0–0x449cb4` |
| `dwData`（结构偏移 `+0x0`） | `0x449cb4` 从结构首 DWORD 取值，`0x44a119` 与 `0x0fecf10f` 比较；该常量直接对应本版本文本发送分支，不是 `lpData` 内的字段 | E22-I |
| `cbData`（结构偏移 `+0x4`） | 在 `0x44a119–0x44a246` 文本业务分支中未见读取，也未见按该值限制三个字符串的读取 | E22-I |
| `lpData`（结构偏移 `+0x8`） | `0x44a124` 从 `[EDI + 0x8]` 取载荷基址，再读取三处字符串 | E22-I |
| 后续处理 | 业务分支执行后仍在 `0x44a255` 调用 `CSkinDialog::OnCopyData @ 0x522c70` | E22-P、E22-I |

`cbData` 未在上述分支中读取，**不等于可以传 0 或任意长度**；Windows 传输要求、上层消息分发和基类处理仍未完整复核。当前文档不指定最小长度、固定总长度或终止符是否计入 `cbData`。

历史入口还比较了 `0x0fe1c10b`、`0x0fecf10b`、`0x0fecf10e`，它们不是本次文本发送契约，不能混用。[E22-P、E22-I]

### 2.2 lpData 三个字符串区域

| 起始字节偏移 | 已确认用途 | 接收端编码与读取方式 | 容量 / 合法长度 | 依据 |
|---|---|---|---|---|
| `0x000` | 目标选择器，如 `user_id`、`group_id` | UTF-8，读取到 NUL，再转换为宽字符串 | 未知 | `0x44a124–0x44a156`；转换结果作为 `0x4c41b0` 的 `ECX` 输入 |
| `0x064` | 目标值，由选择器决定按用户 ID、群 ID、名称或 IP 解释 | 同上 | 未知 | `0x44a16c–0x44a19b`；转换结果作为栈参数传给 `0x4c41b0`，其后用于目标查找 |
| `0x12c` | 消息正文 | 同上 | 未知 | `0x44a1b1–0x44a1e3`；转换结果作为 `EDX` 输入，随后传给正文构造函数 |

以上字段用途由 E22-I 的调用前寄存器/栈准备与 E23-I 的参数使用共同确认：

- `0x44a1f9–0x44a20f`：将目标值压栈，将正文放入 `EDX`，将选择器放入 `ECX`，调用 `0x4c41b0`。
- `0x4c41e5 / 0x4c41ee / 0x4c4200`：分别保存目标值、选择器、正文。
- `0x4c42a1–0x4c42a6`：将正文传给 `0x4b8470`；后续选择器比较和 ID 查找使用另外两个输入。

不能把偏移差 `0x64`、`0xc8` 当作合法字段容量，也不能据此声明结构是 `char[100] + char[200] + char[N]`。最后一个字段的末尾、完整结构大小、填充和其他字段均未知。

### 2.3 编码与终止规则

三个区域均调用 `0x44cbd0`，调用点为 `0x44a130`、`0x44a178`、`0x44a1c0`。[E22-I]

该函数两次调用：

```text
MultiByteToWideChar(0xfde9, 0, source, -1, ...)
```

`0xfde9 == 65001`，即 CP_UTF8；输入长度 `-1` 表明接收端按 NUL 终止字符串读取。[E22-U]

**已确认的是接收端读取方式**，不是生产者的完整写入规则。仍未知：各区域允许的最大 UTF-8 字节数、终止符必须位于何处、是否清零未使用空间、嵌入 NUL 后续字节如何处理、非法 UTF-8 的完整业务结果，以及超长正文的截断或拒绝行为。不得用畸形或越界载荷测试这些问题。

## 3. 目标选择、正文与发送调用

### 3.1 选择器

| 选择器 | 字符串地址 | 已有静态行为 | 网关首版范围 |
|---|---|---|---|
| `user_name` | `0x9d158c` | 查找名称匹配的用户集合，再遍历发送 | 禁用 |
| `user_ip` | `0x9d15a0` | 按 IP 查找用户集合，再遍历发送 | 禁用 |
| `user_id` | `0x9d15b0` | `0x4c4499 → 0x4f48d0 → 0x4f49e0`，随后遍历调用单聊发送 | 仅允许一个授权 ID |
| `group_name` | `0x9d15c0` | 查找名称匹配的群集合，再遍历发送 | 禁用 |
| `group_id` | `0x9d15d8` | `0x4c4387 → 0x4ee740 → 0x4edfc0`，随后遍历调用原生群发送 | 仅允许一个授权 ID |

选择器字符串地址由补充证据 S1 确认，分支和查找调用由 E23、E25 确认。名称匹配存在多目标发送路径；仅接受 ID 是项目的限制，不是客户端只支持这两种选择器。

ID 的字符集合、大小写规则、空白处理及目标值是否存在列表语法尚未恢复；不得根据选择器名称补造这些规则。

### 3.2 调用顺序与正文构造

```text
主窗口业务处理 0x449c60
  → 比较 dwData，取三处 UTF-8 字符串并转换
  → 0x44a20f 调用 0x4c41b0
  → 正文替换处理（CStringHelper::Replace @ 0x568ef0）
  → 0x4c42a6 调用正文构造 0x4b8470
  → 按选择器查找目标并发送
```

E23-P 显示正文替换涉及 `0x9b6860` 与字面值 `L"\\r\\n"`，但本轮未恢复前者内容，也未核实替换函数的参数语义。不能据此声称换行已经规范化或支持某种转义协议。

`0x4b8470` 构造客户端自身的消息内容，包含 `shiyeline` 字面值及后续构造调用。[E24] 这支持“传入正文，由客户端构造内部消息”的判断；不要求发送端拼造 `X_SEND_MSG` 网络 XML。完整格式、转义规则和长度检查仍未知。E24 的伪代码在若干释放函数调用后被分析器截断，不能把它视为完整格式定义。

### 3.3 单聊

```text
user_id
  → 0x4f48d0 → 0x4f49e0（用户查找，未解析 ID 可创建状态 4 的本机记录）
  → 0x4c44dd 调用 0x4b9320（既有原生单聊发送）
```

依据：E23-I、E25-U、补充 E16。未知用户 ID 不保证被拒绝，不能将 IPC 处理完成解释为“目标有效”或“已联网发送”。原生单聊更下游的消息封装与 ACK 证据见历史报告第 2 节；本轮不重复复制。

### 3.4 原生群聊

```text
group_id
  → 0x4ee740 → 0x4edfc0（查现有群表）
  → 0x4c43c9 调用 0x4b9c80
  → 0x617600（CQGroupMgr::SendMsg）
```

依据：E23-I、E23-G、E25-G1、E25-G2。`0x617600` 检查群、正文和本机成员条件；失败分支包含 `I'm not in qgroup %s` 日志。成员遍历中调用 `0x6114f0`，函数没有汇总各成员的送达结果。[补充 E19]

这是原生群发送，不是向多个联系人逐次发送私聊。`0x617600` 的内部返回值也没有由这条 IPC 调用过程作为结构化群投递结果返回给调用方。

## 4. 返回值、窗口和身份约束

### 静态已知

- 业务调用后继续进入 `0x522c70` 基类处理；不能仅凭业务函数末尾的寄存器值定义 `SendMessage` 返回契约。[E22-I]
- 现有业务处理代码没有向调用方构造包含消息 ID、网络 ACK 或群成员结果的结构化响应。[E22、E23]
- IPC 返回不能等同消息送达；群发送内部返回也不代表全部成员收到。[E22、补充 E19]
- 在已检查的文本分支中未见独立口令、签名或来源进程校验；这不是整个窗口处理和 Windows 消息访问均无校验的证明。[E22]

### 未知 / 未验证

- 上层消息映射、`param_3` 的用途、基类处理和最终 `LRESULT` 的关系。
- 合法输入、无目标、无群资格、异常和转换失败时的最终返回值。
- 调用阻塞、超时、客户端退出期间的行为。
- 主窗口类名、标题、创建过程、窗口与目标进程的可靠绑定、多实例选择。
- 来源 HWND 是否有必须遵守的要求、完整分发路径是否校验调用来源。
- Windows 用户会话、完整性级别和消息过滤的实际约束；跨位数调用尚未验证。

后续验证必须使用正常登录的授权客户端身份，不通过提权、注入、修改消息过滤或认证绕过取得访问。

## 5. 证据索引与复用方式

证据目录：[gateway-static](../reports/gateway-static/)。原始报告：[gateway-report.md](../reports/gateway-static/gateway-report.md)，业务入口见第 4 节，E22–E25 的历史编号见其证据索引。

下表 `E22-P` 等后缀仅用于本文定位；原始 Evidence ID 保持不变。单次查询 JSON 的 `response.result.structuredContent` 本身就是证据记录，可按其 `evidence_id` 对照证据包；关键指令地址见前文。

| 本文标记 / 历史分组 | 函数与内容 | 单次查询文件 | 完整 Evidence ID |
|---|---|---|---|
| E22-P / E22 | `0x449c60` 接收端伪代码 | [0082-procedure_pseudo_code.json](../reports/gateway-static/0082-procedure_pseudo_code.json) | `ev_acdffca2137a48fa2ca47862b1048633f8e329196bea438815a8e8d5be8a1a53` |
| E22-I / E22 | `0x449c60` 指令，字段及调用点 | [0107-read_function_instructions.json](../reports/gateway-static/0107-read_function_instructions.json) | `ev_54eb49153b17019a3fd40ec4b7410c9fbb28af0820a320ffb94ee230a3a3e3fa` |
| E22-U / E22 | `0x44cbd0` UTF-8/NUL 读取 | [0102-procedure_pseudo_code.json](../reports/gateway-static/0102-procedure_pseudo_code.json) | `ev_55e9d1c18e869d695db7bbce0670183a838e02cb8b886ed62a29e9af0c7d477d` |
| E23-P / E23 | `0x4c41b0` 分流伪代码 | [0086-procedure_pseudo_code.json](../reports/gateway-static/0086-procedure_pseudo_code.json) | `ev_7d8dd8829f020ce87d0522d93c760f1e1a4a77f9b512caa91d079d5a14eb186c` |
| E23-I / E23 | `0x4c41b0` 指令及参数使用 | [0108-read_function_instructions.json](../reports/gateway-static/0108-read_function_instructions.json) | `ev_1ac48d41a61fa5a2855f4b68d9a83c51a57b894e0ad06e82fcec0ba285e9b307` |
| E23-G / E23 | `0x4b9c80 → 0x617600` | [0096-procedure_pseudo_code.json](../reports/gateway-static/0096-procedure_pseudo_code.json) | `ev_2032cf47084fb7c1f2b0885ad292b86c19c8b20d41ef4a3ee170239e197a2674` |
| E24 / E24 | `0x4b8470` 正文构造 | [0103-procedure_pseudo_code.json](../reports/gateway-static/0103-procedure_pseudo_code.json) | `ev_1494a83ac2f8c587de7cec975a7e084c5e8a7e96e91a7af0e29fb23d853d5c12` |
| E25-U / E25 | `0x4f48d0` 用户 ID 查找 | [0100-procedure_pseudo_code.json](../reports/gateway-static/0100-procedure_pseudo_code.json) | `ev_552d8130062183f991aad5619b6679c5111ba9594da430ef5df113ab6252288d` |
| E25-G1 / E25 | `0x4ee740` 群查找包装 | [0095-procedure_pseudo_code.json](../reports/gateway-static/0095-procedure_pseudo_code.json) | `ev_e634e94298a89381b255a69e1b83322cd1e97c15d4db057e6751ecb97be5f613` |
| E25-G2 / E25 | `0x4edfc0` 群表查找 | [0101-procedure_pseudo_code.json](../reports/gateway-static/0101-procedure_pseudo_code.json) | `ev_f56396820bc581983dc5b523e7726d4d4dbd5466ee3b3b412dd30438a0667f79` |
| 补充 S1 | 五个选择器的字符串地址 | [0116-search_strings.json](../reports/gateway-static/0116-search_strings.json) | `ev_9bf29a6ba84e4a7fff249d8f20cd34fa7ee1d778816a81df099480749aec6d30` |
| 补充 E16 | `0x4f49e0` 未解析用户状态 4 | [0110-procedure_pseudo_code.json](../reports/gateway-static/0110-procedure_pseudo_code.json) | `ev_c959dd3d77da1c1e1c7f25b787e766d99d24706a731aaae8c3c96ee3e2e20cf6` |
| 补充 E19 | `0x617600` 群成员检查与遍历发送 | 证据包内按 ID 查询；历史报告第 3 节 | `ev_17d599ba7a6250a059051f51594bdcf6194448246f10f3ac131758f1b83d01b0` |

### 证据包与快照核对

- [gateway-evidence.json](../reports/gateway-static/gateway-evidence.json)：245 条记录，目标身份与当前样本一致；E22–E25 的 10 个 ID 均存在。
- [gateway-analysis.snapshot.json](../reports/gateway-static/gateway-analysis.snapshot.json)：内嵌的 245 条证据记录与证据包逐条一致；provider 为 Ghidra 12.1.4，analysis profile digest 为 `7810ea9437da59780820ec6fedb47ce47390841a95d527ddb6c3b3700fe180cb`。
- 快照只有 1 条 primitive 缓存（`read_binary_inventory_summary`）和 1 条 workflow 缓存（`binary_overview`）。它保留证据与恢复信息，不是完整的 Ghidra 工程，也不表示任意新查询都能离线重放。
- 导出、关闭回执分别是 `0120-export_evidence_bundle.json`、`0121-close_binary.json`。回执里的 `/tmp/shiyeline-gateway-static-WOXZRF/` 是历史导出位置；本仓库中的实际文件应使用上述相对路径。
- 证据包的 `unknowns` 数组为空，只表示当时未登记 unknown 记录，不表示协议没有未知字段。
- Ghidra 对若干 `0x87fc86` 调用标记 `hasNoReturn=true`，导致函数体范围不连续、伪代码出现截断；原始类型和函数原型也未完整恢复。字段传递优先引用已有指令，完整函数行为仍需后续逐项核实。

## 6. 下一轮 IPC 契约恢复任务

下一轮仍以只读静态分析为限，按下表逐项补证据，不重新完整反编译。每项产出均须带函数/指令地址、完整 Evidence ID 和未解决部分。

| 顺序 | 任务与起点 | 完成标准 |
|---|---|---|
| 1 | 从 `0x0fecf10f` 的已有引用、WM_COPYDATA 发送点及相关生产者查找载荷构造 | 找到实际分配、三个字段写入、`COPYDATASTRUCT` 赋值和发送调用；若本 EXE 中未找到，记录搜索范围，不能宣称不存在生产者 |
| 2 | 恢复生产者的 `cbData`、结构总大小、各字段容量、NUL 与填充规则；对照 `0x44a124–0x44a20f` | 每个长度都有写入或分配证据，说明计量单位、是否含终止符、固定/可变长度；不以字段偏移差代替容量证据 |
| 3 | 复核 `0x44cbd0`、`0x568ef0`、`0x4b8470` 及必要的直接被调用函数 | 确认转换失败、换行/转义、正文空值和长度限制；需要时仅对受 `hasNoReturn` 影响的相关范围读指令 |
| 4 | 复核 `0x4c41b0`、`0x4f48d0 / 0x4f49e0`、`0x4ee740 / 0x4edfc0`、`0x617600` | 记录无效 ID、未知用户、无群成员资格和离线分支，以及目标值能否解析成多个对象；区分本机状态变化与实际投递 |
| 5 | 跟随 `0x449c60` 的上层消息分发、`param_3`、`0x522c70` 与主窗口创建引用 | 恢复最终返回值来源、静态可确定的来源检查、窗口身份和实例选择规则；会话/权限实际可达性仍列为动态待验证 |
| 6 | 复核本契约的完整性 | 必需字段及长度规则有直接证据，未确认内容逐项列出；在完整协议恢复并复核前保持“不可实现发送器”状态 |

### 完成静态契约后的独立阶段（本轮不执行）

完成并复核完整契约后，才设计 Windows Go 最小验证程序；实际发送仍需明确测试授权：

- 使用正常登录的授权客户端和两个授权测试账号。
- 只允许一个已确认的原生测试群 `group_id`。
- 固定消息 `NWT-GW-AUTH-TEST-0001`，只调用一次，不自动重试。
- 分别记录 IPC 返回、本机可见结果、接收端可见结果；无独立接收证据不得标记已送达，结果不确定时记录 `unknown` 并停止。
- 单聊测试另行授权，不与首次群测试一起发送。
