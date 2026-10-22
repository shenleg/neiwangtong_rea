# 第五阶段：WM_COPYDATA 契约恢复与单次验证准备

本文记录内网通 3.4.3055 文本业务 IPC 的只读静态研究；它不是可直接执行的发送协议或发送器。

## 研究范围

- 样本：`/root/reverse-lab/extracted/app/ShiYeLine.exe`。
- SHA-256：`e7caef3fe744f9fe6ba90dd1d53b7e64241cb54020a8bf37be5e756e2fe881b1`。
- REA 6.7.0 + Ghidra 12.1.4；静态 VA，ImageBase `0x400000`。
- 复用 [已有契约](../../ipc/ipc-contract.md) 和 [历史报告](../gateway-static/gateway-report.md)；本目录保存新增查询。
- 不运行 EXE、不注入、不改样本、不读取聊天记录或凭据、不发送消息。Windows API 文档的规定与样本的静态行为分别标注，不能互相代替。
- 本轮原连接的 Ghidra 启动超过 330000 ms。关闭该会话后，另开临时 REA 进程，仅为该进程设置 `REA_GHIDRA_STARTUP_TIMEOUT_MS=1200000`；不修改项目或客户端配置，不禁用分析器。

## A. IPC 字段与证据

### 已有直接证据

`0x449cb4` 取 `COPYDATASTRUCT` 首 DWORD，`0x44a119` 与 `0x0fecf10f` 比较；因此该常量是 `dwData`，不是载荷内字段。`0x44a124` 从结构 `+8` 取 `lpData`。业务调用之前未按 `cbData` 限制读取。[H1]

`0x44a1f9–0x44a20f` 将目标值压栈、正文放入 `EDX`、选择器放入 `ECX`，调用 `0x4c41b0`；后者的参数使用确认三个区域的业务含义。[H1、H2]

`0x44cbd0` 调用 `MultiByteToWideChar(65001, 0, source, -1, ...)`：载荷是 UTF-8、NUL 终止字符串；API 名称中的 `W` 不会把 `lpData` 变成 UTF-16。转换长度查询返回 0 时，可见代码保留初始化后的空宽字符串；没有独立业务错误响应。[H3]

### 本轮补充：生产者搜索与长度规则

**没有恢复文本业务生产者，完整契约未完成。**

- REA 在主 EXE 全部初始化分析块中搜索 `0f f1 ec 0f`，覆盖 8,059,940 字节，完整返回一个命中 `0x44a11a`，即 `0x44a119` 比较指令的立即数，不是生产者。[N1]
- 在 `.text` 搜索短指令编码 `6a 4a`，得到 39 个字节命中；它们不是 39 个已确认发送点，有的只是其他指令/立即数中的字节。沿 Win32 `SendMessageW` 的直接调用者和代表性候选复核：`0x401200 → 0x401020` 构造的是 `dwData=0x0fe1c10b` 的另一条启动/激活消息；`0x5490f0` 是 `dwData=0x3f3` 的 CEF 消息；`0x554370` 是 `dwData=0x3ea` 的 CEF 消息；`0x5235e0` 属于窗口探测回传。这些结构和长度均不能套到文本业务。[N2–N6]
- 独立文件字节扫描还检查发行目录顶层的 `CrashRpt.exe`、`LiveUpdate.exe`、`Remote.dll`、`ShellExt32.dll`、`ShellExt64.dll` 和主 EXE；业务常量只在主 EXE 文件偏移 `0x4951a` 出现。扫描原始记录为 [producer-literal-file-scan.json](producer-literal-file-scan.json)，含各文件哈希。该记录是 Python 字面字节观察，**不是 REA Evidence**；不能排除计算常量、拆分写入、压缩内容或发行目录以外的生产者。
- 字符串 `ShiYeLineExternal.exe @ 0x9d7018` 的引用属于 `SkinUI::GetCefExternal @ 0x51a1d0`；不能据文件名把它命名为文本发送器。[N7]

**接收端长度结论**：文本分支不读取 `cbData`；基类对该 `dwData` 也不进入任何读取 `cbData` 的 CEF 分支，而是返回 0。[H1、N8] 上层消息映射按消息号分发，并不按载荷长度校验。[N9] 所以已复核部分没有文本载荷长度检查；但 Windows 仍按 `cbData` 所示字节数传送数据，`cbData=0` 或只有 `0x12c` 字节不是合法发送方案。字段容量、总大小、填充和生产者 NUL 写入规则全部未知，不补造数值。

### 本轮补充：换行与特殊字符

`0x9d1580` 的字节为 `5c 00 72 00 5c 00 6e 00`，是四个宽字符 `\r\n`；`0x9b6860` 为 `0d 00 0a 00`，是真正 CRLF。[N10、N11]

`0x4c4240–0x4c4253` 的压栈顺序表明 `CStringHelper::Replace(body, literal_backslash_r_backslash_n, CRLF)`；`0x568ef0` 搜索第二参数，再用第三参数替换找到的区域。因此该路径是**把字面转义串变成 CRLF**，不是把 CRLF 转为转义串，也不是通用 JSON/C 风格反转义。[H2、N12、N13]

已有真正 CRLF 不会被这一替换变为其他文本。单独 `\n`、单独 `\r`、制表符及 XML 特殊字符的最终消息显示/转义语义，仍未完整恢复。`0x569610` 中的容量增长和 `0x7ffffffe` 检查属于宽字符串实现，不是业务消息上限。[N13] `0x4b4a70` 有空原始片段提前返回，但这仍不能证明整条 IPC 对空正文有明确拒绝响应。[N14]

## B. 字节偏移与长度表

以下结构偏移是目标 **x86 接收端** 的内存布局，不是要求 x64 发送进程硬编码同一结构大小。

| 所属区域 | 字节偏移 | 字段 | 已确认长度/单位 | 未确认项 |
|---|---:|---|---|---|
| `COPYDATASTRUCT` | `+0x00` | `dwData` | 目标进程中 4 字节，值 `0x0fecf10f` | 无 |
| `COPYDATASTRUCT` | `+0x04` | `cbData` | DWORD；Windows 定义为 `lpData` 数据的字节数 | 本业务的合法值、固定或变长、是否含全部填充 |
| `COPYDATASTRUCT` | `+0x08` | `lpData` | 目标进程中 4 字节指针 | 完整载荷结构 |
| `lpData` | `+0x000` | 选择器 | UTF-8 至首个 NUL；`group_id` 是 8 个 ASCII 字节，连 NUL 是 9 字节 | 区域容量与生产者写入规则 |
| `lpData` | `+0x064` | 目标值 | UTF-8 至首个 NUL | ID 语法、字节上限、填充 |
| `lpData` | `+0x12c` | 正文 | UTF-8 至首个 NUL；固定测试文本为 21 个 ASCII 字节，连 NUL 是 22 字节 | 正文容量、结构末尾、整体长度上限 |

选择器与目标起点相差 100 字节，目标与正文起点相差 200 字节；这只能说明起点间距，不能证明字段被声明为 `char[100]`、`char[200]`。固定测试文本末尾落在什么位置是算术结果，不是生产者 `cbData` 合法值的证据。[H1–H3]

## C. Windows API 调用要求

以下是 Microsoft 的平台规定或未来验证设计，不是反编译得到的客户端要求。

1. `WM_COPYDATA == 0x004a`，`wParam` 是发送方窗口 HWND，`lParam` 指向调用进程本地的 `COPYDATASTRUCT`；`cbData` 的单位是字节。`dwData` 是 `ULONG_PTR`，`lpData` 是指针，发送器须按自身 ABI 布局。首个验证可选择 `GOARCH=386` 减少跨位数变量；这是设计选择，不是 Windows 禁止跨位数。[W1、W2]
2. 使用一个确定 HWND，不使用 `HWND_BROADCAST`。传送内容不得包含接收进程不可访问的嵌套指针；发送期间不得被其他线程改写，接收方只能只读使用，处理后继续使用必须复制。[W1]
3. 不用 `PostMessageW`、`SendNotifyMessage` 或 `SendMessageCallback` 发送带指针的这条消息。[W3]
4. 进程必须在同一 desktop；desktop 属于 window station，window station 属于 session。同一 Windows 用户并不足够，例如不同 RDP 会话。验证器放在正常授权客户端所在的交互会话和桌面，不从 Session 0 服务直接发窗口消息。[W4、W5]
5. `SendMessageW` 受 UIPI 约束；默认只能向相同或更低完整性级别的进程发消息，受阻时错误为 5。首次验证使用同用户、同完整性级别；不提权、不改消息过滤、不切换身份。[W6]
6. 候选窗口可通过 `EnumWindows` 枚举，再用 `GetWindowThreadProcessId` 绑定 PID/TID；窗口与样本路径、版本、哈希及实例的匹配是发送前要求。不能只凭标题或随便选择同名窗口。[W7、W8]

### 客户端主窗口与分发（静态已确认部分）

- `0x54fd20` 注册类 `SkinDialog`，窗口过程是 `0x54fcd0`；`CSkinDialog::Create @ 0x524890` 用该类调用 `CreateWindowExW` 并把 HWND 保存于对象 `+0x240`。`SkinDialog` 是通用类，不能仅用它区分主窗口、隐藏 owner 和其他对话框。[N15、N16]
- `0x43ea90` 使用 `MainDialog.xml` 构造 `CMainDialog`，设置虚表 `0x9b80dc`，把对象保存在 `DAT_00b28914`。`0x4029d0` 另建一个隐藏 owner，再将 CMainDialog 存为 app 的 `+4` 主对象并调用 `DoModal`。因此“目标进程中的第一个 SkinDialog”不是可靠选择规则。[N17、N18]
- `0x43d200` 的主窗口消息表初始化把消息 `0x4a` 对应到 `0x449c60`。主虚表 `+0x288` 指向主消息表 getter，`+0x290` 指向 `CSkinDialog::OnPreMessage @ 0x51fa20`；该前置函数对 `0x4a` 返回 0，不阻止这条消息。[N19–N21]
- 分发顺序：`0x54fcd0` 用目标 HWND 查本进程窗口对象 → `CSkinWndHost::WindowProc @ 0x535bc0` → `OnMessage @ 0x535d50` → 主窗口消息表 → `0x449c60`。`WindowProc` 对 `0x4a` 不走命令消息或 `ShellGetDragImage` 分支。[N9、N22、N23]
- 在这段分发及文本处理路径中，没有来源 HWND→PID 验证、发送者 token/SID/身份检查；文本分支不根据 `wParam` 决定是否调用业务。没有发现口令或签名。此结论仅限已复核函数，Windows 的 desktop/UIPI 限制仍适用；不能把“没有调用方认证”解释为发送授权。[H1、N8、N9、N20、N22、N23]
- `0x5b6c10` 的单实例信息使用命名映射，记录中含 HWND/PID 字段；这是启动辅助信息，不是本轮允许使用的外部网关接口。未读取任何运行进程的映射或内存。[N24]

**仍未完成的窗口要求**：稳定区分主窗口与隐藏 owner 的外部特征、多实例/多会话选择及 HWND 失效后的处理。设计应由操作者确认特定实例，再验证 PID、哈希及窗口特征；不是向多个候选各试发一次。

## D. 失败、超时、重复与返回值

### 已有业务证据

- `group_id` 查群结果为空时，跳过 `0x4c43c9 → 0x4b9c80`。这条路径未显示创建新群。[H2、H4、H5]
- `0x617600` 有管理对象状态、群 ID 非空和构造后正文非空条件；成员检查失败不进入成员发送循环，返回 0。日志 `I'm not in qgroup %s` 受日志级别控制，不保证输出。不能把未解释的状态字段直接命名为“登录状态”。[H6]
- 上述正文已由 `0x4b8470` 构造，不能据“构造后非空”声称原始空文本一定被拒绝；原始正文上限、完整特殊字符处理仍未恢复。[H7]
- 未知 `user_id` 可创建状态 4 的本机记录；单聊状态 4 有本机记录分支，不等于拒绝、不等于联网投递。[H8、H9]
- 群成员投递循环不汇总成员 ACK；IPC 返回不等于某个接收方已收到，更不等于全群送达。[H6]
- `0x449c60` 执行业务后在 `0x44a255` 调用 `CSkinDialog::OnCopyData @ 0x522c70`，然后执行异常栈/栈保护清理并在 `0x44a27d` 返回。基类在本业务 `dwData` 下的内部返回值是 0，**但最终窗口 LRESULT 是消息映射预置的 1，不是该内部返回值**。[H1、N8、N9、N22、N23]

### 最终 LRESULT：正常返回路径固定为 1（静态已确认）

这是本轮恢复的窗口分发契约，条件是找到正确的 CMainDialog 消息表、处理过程正常返回且没有异常终止：

1. `OnMessage @ 0x535d50` 找到映射后，在 `0x535e64–0x535e6d` 把局部 `handled`（`[ESP+0x10]`）和结果（`[ESP+0x34]`）都初始化为 1。[N9]
2. 消息号经 `DEC` 后索引跳转表；`0x536469` 的字节是 `09`，对应表项 `0x536410 → 0x5362bf`，确认 `WM_COPYDATA` 走这一分支。[N25]
3. `0x5362df–0x5362f2` 将 `&handled`、原 `lParam`、原 `wParam` 传给处理器；调用后在 `0x5362f4` **直接跳到 `0x536335`**，跳过 `0x536331` 保存 `EAX` 的指令。因此处理器的返回寄存器不成为窗口结果。[N9]
4. 文本业务处理器和基类针对本 `dwData` 均不写 `handled`。`OnMessage` 在 `0x5363c8–0x5363cc` 将仍为 1 的结果写入调用者结果地址，返回仍为 1 的 `handled`；栈保护函数正常路径不修改 `EAX`。[H1、N8、N9、N26]
5. `WindowProc` 在 `0x535d20` 调用 `OnMessage`，其返回非零则跳到 `0x535c16` 取局部结果；`0x54fcd0` 不改该返回值，直接返回 Windows。[N22、N23]

**含义**：`SendMessageW == 1` 或 `SendMessageTimeoutW` 成功后 `lpdwResult == 1`，只表明该消息映射正常处理结束。目标群查找为空、群资格不足或其他下级业务失败，只要正常回到上述路径，也会得到同一个 1。不能把 1 解释为“目标有效”“已发送到网络”或“接收方收到”。异常、窗口退出及超时不属于这个正常返回结论。

### 调用控制设计

选用 `SendMessageTimeoutW`，显式区分：

- API 自身返回非零：同步调用成功完成；另读 `lpdwResult` 保存窗口过程结果。
- API 返回 0：失败或超时；调用前 `SetLastError(ERROR_SUCCESS)`，调用后立即保存错误值。错误仍为 0 时是一般失败，不能伪造具体错误原因。
- 超时/窗口退出不表示业务未发生；不可自动重试。窗口结果为 0 也不自动证明文本未被提交。

建议有限超时并使用 `SMTO_ABORTIFHUNG | SMTO_ERRORONEXIT`。不使用会让活跃接收线程忽略期限的 `SMTO_NOTIMEOUTIFNOTHUNG`；期限是验证器配置，不是客户端协议字段。`SMTO_BLOCK` 会阻止调用线程处理其他请求，如果接收端同步回调发送方可能造成相互等待，必须先确认回调关系，不能把该标志当通用解法。独立发送线程不持有 UI/业务锁，不并发发两次。[W9]

普通 `SendMessageW` 可能一直阻塞；等待时还会处理传入的 nonqueued messages。`SendMessageTimeoutW` 在同一消息队列直接调用窗口过程时忽略 timeout；独立进程设计避开这个情形。[W6、W9]

在契约确认前不创建待发送载荷。若以后实现 Go，调用期间必须保持结构和字节缓冲区有效并禁止其他 goroutine 修改；超时内存处理须按 Windows 的系统消息复制语义实现，不能把任意自定义指针消息的生命周期照搬到这里。

## E. 最小发送器设计（未实现、未执行）

发送前必须先完成字段容量、总大小、`cbData`、窗口身份和最终返回处理的静态复核；当前不提供发送代码。

满足条件后，另行授权的验证程序仅执行以下用例：

1. 操作者正常登录两个授权测试账户，在只含授权成员的原生测试群确认成员资格；人工提供一个确定的 `group_id`。
2. 固定 `selector=group_id`、正文 `NWT-GW-AUTH-TEST-0001`；没有任意文本、任意目标、广播或列表参数。
3. 对照样本哈希，验证进程、窗口、会话/桌面和完整性级别；候选不唯一或任一条件不成立即停止，不能尝试多个窗口。
4. 调用前持久记录本次尝试，进程重启也不自动再次调用；仅调用一次，不自动重试、不转用单聊。
5. 分开记录 Win32 API 返回、GetLastError、窗口 LRESULT、耗时；不把窗口响应命名为 `delivered`。
6. 操作者仅在两个授权测试群窗口核对固定文本，发送方和接收方各应只有一条；不读取历史聊天或凭据。
7. 无独立接收证据标记 `unknown` 并停止。`recipient_observed` 只描述该授权接收方可见，不描述全部成员。

这不是已有的幂等协议：客户端未确认提供请求 ID 去重；网关只能控制自身的一次调用，无法撤销超时之前已产生的业务。

## F. 待验证项与任务状态

### 原始问题逐项核对

| 问题 | 状态 | 结论与剩余工作 |
|---|---|---|
| `0x0fecf10f` 比较位置 | 已确认（静态） | `COPYDATASTRUCT.dwData`，不是载荷字段；H1、N1 |
| `cbData` 长度检查 | 部分完成 | 已复核接收和分发路径无文本长度检查；生产者合法值未恢复；H1、N8、N9 |
| 三字段布局与用途 | 部分完成 | 起点和用途已确认；字段末尾、容量、填充、总大小未知；H1–H3 |
| 编码和终止方式 | 部分完成 | 接收端 UTF-8/NUL 已确认；生产者终止符写入与边界规则未知；H3 |
| 额外校验、长度与特殊字符 | 部分完成 | 字面 `\r\n`→CRLF 已确认；最大消息长度、其他特殊字符完整行为未知；N10–N14 |
| `SendMessageW` 返回语义 | 已确认正常路径（静态） | 窗口结果为 1，不代表业务成功；N8、N9、N22、N23、N25、N26 |
| 错误群与未知用户 | 已确认已有分支（静态） | 群结果为空跳过发送；未知用户可创建状态 4 记录；H2、H4、H5、H8、H9 |
| 客户端未登录 | 未解决 | 状态字段对应的初始化/登录/连接生命周期未恢复，不能用非零对象字段替代登录证明；H6 |
| 群资格不足 | 部分完成 | `0x617600` 成员检查失败不发送，窗口正常结果仍为 1；H6、N9；完整禁言/群角色权限未恢复 |
| 主窗口创建与识别 | 部分完成 | 类、对象、owner 与创建过程已恢复；可靠的外部唯一匹配规则尚未完成；N15–N18、N28 |
| `WM_COPYDATA` 分发 | 已确认（静态） | 主消息表与窗口过程对应关系已恢复；N9、N19–N23 |
| session/desktop/完整性级别 | 平台规则已确认，运行未验证 | 相同 desktop、正常可通信完整性级别；同用户是本验证设计限制，不是已证明的客户端 SID 校验；W4–W6 |
| 来源 HWND/PID/身份校验 | 已检查范围内未发现 | 分发与文本处理不验证发送方身份；不外推为整个进程没有安全措施；H1、N8、N9、N20、N22、N23 |
| 重复提交 | 部分完成 | 已检查接收路径未读取调用方 request_id 或进行本次请求去重；重复调用会重新进入业务；下级网络重试/去重未完整恢复 |
| 阻塞与死锁 | 部分完成 | 同步调用可阻塞，Windows 等待/重入规则已明确；未完成所有下级锁与同步调用的遍历，不能证明无死锁；W6、W9 |
| 最小验证方案 | 已给出设计，不可实施 | 固定一个授权测试群、一条固定文本、一次调用；协议与窗口缺项完成后才编写和执行 |

### 需要另行授权的运行时验证

1. 正常登录、正常群资格下，选定 HWND 确实对应该主窗口实例，且隐藏/最小化不改变入口可达性。
2. 同一 session/desktop、同完整性级别进程实际能够调用，不依赖提权或消息过滤变更。
3. 用合法生产者结构与 `cbData` 提交一次固定 ASCII 文本，客户端没有崩溃、截断或错误转码。**不能用试错长度、超长或无 NUL 载荷来补协议。**
4. 发送端和唯一授权接收端各出现一条固定消息，发送者和原生群正确；IPC 的 1 单独记录，不升级为送达状态。
5. 正文 Unicode、字面转义串及实际换行的显示一致性，在后续独立用例验证，不混入首次一条固定文本用例。
6. 窗口结果、客户端本机记录及接收结果之间的差异；超时、进程退出后无法确定是否提交时维持 `unknown`。不为观察失败分支向未授权群或用户发送。

### 下一轮静态分析优先级

先寻找该版本的独立文本 IPC 生产者、SDK 示例或接口说明，并核实来源与版本。当前发行目录中的字面搜索未提供生产者；**不要再用接收端偏移差推算字段容量**。随后恢复总大小、`cbData`、边界写入及最大正文长度，再完成主窗口唯一匹配和未登录状态分析。协议完整恢复并复核前，发送器保持未实现。

## 证据索引

### 历史直接证据

完整记录在 `../gateway-static/`，别名只用于本文引用。

| 别名 | 内容 | 完整 Evidence ID |
|---|---|---|
| H1 | `0x449c60` 指令 | `ev_54eb49153b17019a3fd40ec4b7410c9fbb28af0820a320ffb94ee230a3a3e3fa` |
| H2 | `0x4c41b0` 指令 | `ev_1ac48d41a61fa5a2855f4b68d9a83c51a57b894e0ad06e82fcec0ba285e9b307` |
| H3 | `0x44cbd0` 转换 | `ev_55e9d1c18e869d695db7bbce0670183a838e02cb8b886ed62a29e9af0c7d477d` |
| H4 | `0x4ee740` 群查找包装 | `ev_e634e94298a89381b255a69e1b83322cd1e97c15d4db057e6751ecb97be5f613` |
| H5 | `0x4edfc0` 群查找 | `ev_f56396820bc581983dc5b523e7726d4d4dbd5466ee3b3b412dd30438a0667f79` |
| H6 | `0x617600` 成员检查与发送 | `ev_17d599ba7a6250a059051f51594bdcf6194448246f10f3ac131758f1b83d01b0` |
| H7 | `0x4b8470` 正文构造 | `ev_1494a83ac2f8c587de7cec975a7e084c5e8a7e96e91a7af0e29fb23d853d5c12` |
| H8 | `0x4f49e0` 未知用户 | `ev_c959dd3d77da1c1e1c7f25b787e766d99d24706a731aaae8c3c96ee3e2e20cf6` |
| H9 | `0x4b9320` 单聊与状态 4 | `ev_585b8c6bd9d12509095fdc5e1a15e3bece344edf8686d9e29b1bfb112368edf7` |
| H10 | `0x522c70` 基类伪代码 | `ev_79ea8a82554a5d976ed1b4052908e2885708c3bc55dfbb552aa52e6f5d4be77e` |

Ghidra 将 `0x87fc86` 视为 no-return，造成若干函数体不连续或伪代码截断。这是分析器模型，不是独立证明该调用真的不返回。本文不把 SSO 容量判断 `7/8` 当作消息长度限制，不把截断当作业务拒绝。[H1–H3、H7、H9、H10]

### 本轮新增证据索引

| 别名 | 函数/查询 | 原始查询文件 | 完整 Evidence ID |
|---|---|---|---|
| N1 | 业务常量全初始化块搜索 | [0003-search_bytes.json](0003-search_bytes.json) | `ev_25836f6adc543720b67b886374ea0d156d298827df306775f1e039b8c9f3f4de` |
| N2 | `.text` 中 `6a 4a` 搜索 | [0031-search_bytes.json](0031-search_bytes.json) | `ev_dd86967a01516050abc5b27f4b31d41b05a3503826fb34fb079e9f98f6a8eee6` |
| N3 | `0x401020` 其他业务结构 | [0030-procedure_pseudo_code.json](0030-procedure_pseudo_code.json) | `ev_fe667f3522c85ac402b36114216ef781608e75275fb3880742b195e52edaa0b6` |
| N4 | `0x5490f0` CEF 释放消息 | [0056-procedure_pseudo_code.json](0056-procedure_pseudo_code.json) | `ev_6995621aa4d29a286114c3d0722c583eb519a8f8525cd50453e67f04ed47ed48` |
| N5 | `0x554370` CEF 消息 | [0057-procedure_pseudo_code.json](0057-procedure_pseudo_code.json) | `ev_5dd09ff2af8ff3c1bf94abe6d2d5c91799521578f20f8ae7dc3876ef4fc9ab09` |
| N6 | `0x5235e0` 探测回传 | [0055-procedure_pseudo_code.json](0055-procedure_pseudo_code.json) | `ev_9a96e3c5925a94d06455899089ea580ff9796ac0b1cc495361863ea38d0c3e93` |
| N7 | `0x51a1d0` CEF 辅助程序名 | [0029-procedure_pseudo_code.json](0029-procedure_pseudo_code.json) | `ev_587a9408565d711f6f5b641f069c84fe6ca8ef6b814545fa3cbbe7e098e8af27` |
| N8 | `0x522c70` 基类原始指令 | [0021-read_function_instructions.json](0021-read_function_instructions.json) | `ev_a7fbaca75f51154dfbf0a7281cb4d18dbe78a98aa4eed76253c9f55c0ed67a16` |
| N9 | `0x535d50` 分发原始指令 | [0034-read_function_instructions.json](0034-read_function_instructions.json) | `ev_74ceb756db1d0ca05cf77f192c7e646cd6cfb430a9532cea99c4aea3f26db43b` |
| N10 | `0x9d1580` 字面转义串 | [0035-read_bytes.json](0035-read_bytes.json) | `ev_5342d29a07bc24b3f720cbf0b45521411a2bba8356edd792fc8a3edf7f2d932e` |
| N11 | `0x9b6860` CRLF | [0004-read_bytes.json](0004-read_bytes.json) | `ev_b22afb894390fe21866cdc2421a5b146b8f8c4988425d6f8193123b4832dffc3` |
| N12 | `0x568ef0` Replace 指令 | [0016-read_function_instructions.json](0016-read_function_instructions.json) | `ev_3bdec0426711373aef0541b0e43dcfa046380f41f3122373bab0b0d0082bab3e` |
| N13 | `0x569610` 替换/容量实现 | [0018-procedure_pseudo_code.json](0018-procedure_pseudo_code.json) | `ev_30ba34b1edc069ec281062d4693ed3eeaaf0b7ffd80ad64c137dc24b4f5dcc3a` |
| N14 | `0x4b4a70` 原始空片段判断 | [0068-read_function_instructions.json](0068-read_function_instructions.json) | `ev_2f64158c3515b70851f6fef47f8c47a760e124355aebe606747fd5989236e28b` |
| N15 | `0x54fd20` SkinDialog 注册 | [0064-procedure_pseudo_code.json](0064-procedure_pseudo_code.json) | `ev_adab67eb5ac406258385864564bdc7c7f1f5c59b081d7403650621fa710a5b8f` |
| N16 | `0x524890` 创建窗口 | [0012-procedure_pseudo_code.json](0012-procedure_pseudo_code.json) | `ev_42fab04517a97aa27816fe824718380101b7e8067e778158b4ddfa19ebeb6aea` |
| N17 | `0x43ea90` 主对象/虚表 | [0053-read_function_instructions.json](0053-read_function_instructions.json) | `ev_206846c545bf5033d7df2fd89f524db7cd14c17069e5e4cc484128f86223b6de` |
| N18 | `0x4029d0` owner 与主对象 | [0054-procedure_pseudo_code.json](0054-procedure_pseudo_code.json) | `ev_b0e215670dc529323c5c94c2fffd0ad6d267b489b86df2f1446bb95e939f7a90` |
| N19 | `0x43d200` 主消息表初始化 | [0010-procedure_pseudo_code.json](0010-procedure_pseudo_code.json) | `ev_bcae740764c33a30381d3d62ac70562bded42995190d8d6161580555f96f39e2` |
| N20 | `0x51fa20` 前置处理 | [0063-procedure_pseudo_code.json](0063-procedure_pseudo_code.json) | `ev_40650147f8f40b68264caa0b8cbf1783aefe3c049acdf55ec19691a2e18038a9` |
| N21 | `0x9b8364` 主虚表相关槽 | [0062-read_bytes.json](0062-read_bytes.json) | `ev_6c521db60f955de67947999420c6cedbe48a693dc6e401a6d8eed26acc99a867` |
| N21 | `0x43d1f0` 主消息表 getter | [0071-procedure_pseudo_code.json](0071-procedure_pseudo_code.json) | `ev_1ad742a2f314167050cdc174035e4939c8fc79e133590f8c64fb577ed2c8aec9` |
| N22 | `0x535bc0` WindowProc 指令 | [0069-read_function_instructions.json](0069-read_function_instructions.json) | `ev_e59cd33929eee5632ea4370e0a161a03c8e11dbbffc4172c7f7701a00b7e876a` |
| N23 | `0x54fcd0` Win32 窗口过程指令 | [0070-read_function_instructions.json](0070-read_function_instructions.json) | `ev_3d150e21bd9b5128e66d70683757cbd596b03f31c0f05220972d6fcf8603c60f` |
| N24 | `0x5b6c10` 单实例映射代码 | [0043-procedure_pseudo_code.json](0043-procedure_pseudo_code.json) | `ev_7321b1aa9ec65118fdeaaf7723ba48c30f296c939bd7bd8aab09350998c265d5` |
| N25 | `0x536469` 消息跳转索引 | [0060-read_bytes.json](0060-read_bytes.json) | `ev_82502aeb8d4209307b74351d890270b40b920612774d05eeb937b0ec8cea693d` |
| N25 | `0x5363ec` 跳转地址表 | [0061-read_bytes.json](0061-read_bytes.json) | `ev_2aae10725d76fcd03e54d9f193e5c95ed7eea49a0e24dfd82faced65a2754dd8` |
| N26 | `0x87fc91` 栈保护正常路径 | [0033-read_function_instructions.json](0033-read_function_instructions.json) | `ev_0db1c96446b5a53f926bebc59a3c9547ed48516f4f036d697b00b5bf28a9caf1` |
| N27 | SendMessageW 直接调用者 | [0014-procedure_callers.json](0014-procedure_callers.json) | `ev_7eeaad6f638fe4f418469f7f31ba42feb62b38291ae294c354dfef8e5d758128` |
| N28 | `0x524aa0` DoModal 创建/消息循环 | [0073-procedure_pseudo_code.json](0073-procedure_pseudo_code.json) | `ev_9e9ee1205e893db8bfc457b0d7b63046af782a3944093785363fe6bef1c7d54e` |
| N29 | `0x87fc86` free 跳板伪代码 | [0020-procedure_pseudo_code.json](0020-procedure_pseudo_code.json) | `ev_c80af268e976b145f5d1c3da13bad6f1cab52a91e7d375687df0014f416a41ec` |
| N29 | `0x87fc86 → 0x880ebe` 原始指令 | [0076-read_function_instructions.json](0076-read_function_instructions.json) | `ev_9575f18404176012c951180b3bdf088acaafad30749d4fd9322dbba133b878ba` |

本轮另查 `0x87fc86`：它跳转到 `_free @ 0x880ebe`，后者被 Ghidra 标注 `hasNoReturn=false`；跳板伪代码也显示 `_free(param); return`。这与一些调用点上的 no-return 模型不一致。本轮未修改数据库标记，也未重新完整反编译所有受影响函数；正文最大长度仍不能用截断伪代码推断。[N29]

### 证据保存与关闭

[ipc-phase5-evidence.json](ipc-phase5-evidence.json) 和 [ipc-phase5-analysis.snapshot.json](ipc-phase5-analysis.snapshot.json) 均保留 **317 条 Evidence**，包含导入历史记录。相同查询的确定性 ID 会复用，查询数量不等于新增 Evidence 数量。

导出回执为 [0077-export_evidence_bundle.json](0077-export_evidence_bundle.json)，关闭/快照回执为 [0078-close_binary.json](0078-close_binary.json)。独立 REA 服务已退出；原 MCP 会话也已关闭。快照仍是证据/缓存快照，不是完整 Ghidra 工程。`unknowns=0` 仅表示未调用 unknown 登记接口，未知事项以本文 F 节为准。

### Windows 官方资料（2026-10-22 查询）

外部平台文档没有样本 REA Evidence ID，以下用 W 编号与二进制证据区分。

- W1：[WM_COPYDATA](https://learn.microsoft.com/en-us/windows/win32/dataxchg/wm-copydata)。
- W2：[COPYDATASTRUCT](https://learn.microsoft.com/en-us/windows/win32/api/winuser/ns-winuser-copydatastruct)。
- W3：[PostMessageW](https://learn.microsoft.com/en-us/windows/win32/api/winuser/nf-winuser-postmessagew)。
- W4：[Desktops](https://learn.microsoft.com/en-us/windows/win32/winstation/desktops)。
- W5：[Window Stations](https://learn.microsoft.com/en-us/windows/win32/winstation/window-stations)。
- W6：[SendMessageW](https://learn.microsoft.com/en-us/windows/win32/api/winuser/nf-winuser-sendmessagew)。
- W7：[EnumWindows](https://learn.microsoft.com/en-us/windows/win32/api/winuser/nf-winuser-enumwindows)。
- W8：[GetWindowThreadProcessId](https://learn.microsoft.com/en-us/windows/win32/api/winuser/nf-winuser-getwindowthreadprocessid)。
- W9：[SendMessageTimeoutW](https://learn.microsoft.com/en-us/windows/win32/api/winuser/nf-winuser-sendmessagetimeoutw)。
