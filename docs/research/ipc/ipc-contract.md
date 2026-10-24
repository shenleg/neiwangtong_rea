# 内网通 WM_COPYDATA 业务 IPC 契约与证据

本文维护当前结论、接收端字段和静态证据索引，不是完整厂商 SDK。**已验证的实施路线以 [validated-route.md](validated-route.md) 为准**；维护模块或判断下一步工作时先读该文，不从历史阶段的“下一轮任务”重新开始逆向。

## 1. 当前结论与验证范围

**原0001方式已成功送达。** `2026-10-23T06:40:27.9898784Z` 的独立复验使用原载荷和原 Win32 实现，API=1、错误0、LRESULT=1、1ms、未超时、调用后客户端存活；操作者随后明确确认独立接收端在正确原生群按本次时间新增一条，发送者和正文正确，最终为 **`recipient_observed`**。见[原0001方式复验报告](../reports/ipc-phase12/0001-original-method-confirmation.md)。迁移到 `internal/ipc` 后，另行授权的04复验于 `2026-10-23T07:14:23.1251137Z` 再次送达，API=1、错误0、LRESULT=1、1ms、未超时，独立接收端确认新增一条、发送者正确；见[重构后复验报告](../reports/ipc-phase12/ipc-refactor-confirmation-04.md)。

| 项目 | 当前依据 |
|---|---|
| 客户端 | 内网通 3.4.3055，Windows x86 / 原生 C++ / SkinUI |
| 样本 SHA-256 | `e7caef3fe744f9fe6ba90dd1d53b7e64241cb54020a8bf37be5e756e2fe881b1` |
| 已验证发送端 | Go Windows 386，正常授权客户端身份，同用户/完整性、session 和 desktop |
| 已验证业务 | `group_id`，唯一授权原生群，固定 ASCII 正文 `NWT-GW-AUTH-TEST-0001` |
| 实现 | `internal/ipc` 非交互式 Adapter 仅绑定客户端，群/用户目标与正文显式传入，发送返回结构化结果；自定义正文按内容预算检查，`Config / LoadConfig` 仅供 CLI 等调用方读取目标，实验交互仅在 `cmd/ipcexperiment` |
| 实验长度 | 历史固定正文为 322 字节，三个 UTF-8 字符串的 NUL 均在传送区内；当前按转换后正文动态分配，不把 322 当作厂商结构总长 |
| 成功任务 | 原方式03、迁移后04、正式Adapter直接调用05均获独立接收确认；各自日志保留，未自动重试 |
| 当前真实发送 | 已另经明确授权完成[固定正文单聊验证01](../reports/ipc-phase12/user-confirmation-01.md)；该次标识已占用，开发和测试不代表可以自行操作真实客户端复验 |

生产者完整结构、字段容量、生产者 `cbData` 和厂商最大正文长度仍未恢复；这些未知项**不撤销固定用例已成功的事实，也不再作为复用该用例的前置恢复任务**。后续[正文定向静态分析](../reports/ipc-body/body-contract-report.md)明确了JSON/外层转义、换行、两个XML packer的 `0x40000` 宽字符容量及溢出处理；[按正文内容计算预算](../reports/ipc-message-content/refined-body-bound.md)进一步给出基于静态证据的序列化空间上界，当前据此支持自定义正文，不再采用固定正文或统一 512 字节限制。不扫描长度、不追加猜测字段、不发送无 NUL、超预算或畸形载荷；按已知偏移和最终 NUL 动态分配，不等于向真实客户端盲试长度。静态事实、运行验证和未知范围分别标注，不互相替代。

当前 Go 接口为 `NewAdapter(exePath string) (*Adapter, error)`，以及返回 `SendResult, error` 的 `SendGroup(groupID, message string)`、`SendUser(userID, message string)`。Adapter 只绑定正常登录的唯一本机客户端，不保存目标配置，不做授权目标白名单或 `Config` 比较；默认调用方有权向指定目标发送，Adapter 不另设权限检查，合法参数调用即尝试发送。两个方法支持非空、有效 UTF-8、无 NUL 且转换后满足内容预算的 `message`，失败时明确拒绝、不发送、不创建发送日志。`ipc.Message` 保留为历史默认及 CLI 固定测试正文；本轮不增加 CLI 正文参数、单聊开关或 HTTP。

每次有效发送调用内部用 `crypto/rand` 生成 `NWT-GW-<随机十六进制>`，独占创建日志并 Sync 后只调用一次，不自动重试；随机源失败或日志碰撞直接报错，不改 ID 再试。相同参数再次调用是新任务，随机 ID 不提供调用方重试幂等性。`SendResult.AttemptID` 用于 `RecordObservation`：依据已有日志核验标识、合法群/用户目标、合法正文和唯一实际调用，不再要求等于固定正文；只校验已保存正文，不重新转换或改写历史事件。不与 Adapter 配置目标比较，重建 Adapter 后可继续追加观察。日志目录和历史文件保持不变。历史成功报告使用当时的配置绑定及显式 attemptID 接口，不改写其事实。接口调整阶段仅使用合成 fixture；随后另经用户授权完成[显式目标群聊真实复验](../reports/ipc-phase12/explicit-target-group-confirmation.md)，`SendGroup(groupID, ipc.Message)` 调用一次并获独立接收确认，`RecordObservation` 返回 `recipient_observed`。随后新版 `SendUser(userID, ipc.Message)` 也获授权完成一次[单聊真实复验](../reports/ipc-phase12/explicit-target-user-confirmation.md)，同样经独立接收确认后返回 `recipient_observed`；群与单聊的新参数形式均已有固定正文送达证据。开发期间操作真实客户端复验须另行明确授权，不是现行发送接口每次调用的额外要求。

自定义正文与换行兼容实现经离线单测和 Windows 386 独立 fixture 后，已另获授权完成[自定义三行消息真实验证](../reports/ipc-phase12/custom-message-newlines-confirmation.md)：群聊字面 `\n` 转 LF、私聊字面 `\r\n` 转 CRLF，各调用一次并获独立接收确认，均记录为 `recipient_observed`。随后另经授权完成[500汉字五段真实验证](../reports/ipc-phase12/custom-500-newlines-confirmation.md)，群聊LF及私聊CRLF各一条，全文、发送者及显式换行完整，均为 `recipient_observed`。运行证据分别覆盖对应正文与目标；更长正文及其他边界不继承这些成功结论。

静态地址均以 ImageBase `0x400000` 记载；运行地址按实际加载基址换算。历史分析工具为 REA 6.7.0 / Ghidra 12.1.4，`FUN_` 是分析器名称。

## 2. 接收端字段与实验值

### 2.1 COPYDATASTRUCT

以下结构访问来自目标 x86 接收端，不是跨架构布局承诺。

| 项目 | 已确认事实 | 依据 |
|---|---|---|
| 入口 | `0x449c60` 接收 `COPYDATASTRUCT *`，有空结构指针提前返回分支 | E22-P/I |
| `dwData`，结构 `+0x0` | 文本分支比较 `0x0fecf10f`，不是 lpData 内的字段 | `0x449cb4 / 0x44a119`，E22-I |
| `cbData`，结构 `+0x4` | 已复核文本分支没有读取它或按它限制三个字符串 | `0x44a119–0x44a246`，E22-I |
| `lpData`，结构 `+0x8` | 取载荷基址后读取三个字符串 | `0x44a124`，E22-I |
| 后续 | 业务执行后仍调用基类 `CSkinDialog::OnCopyData @ 0x522c70` | `0x44a255`，E22-P/I |

Windows 仍按 `cbData` 复制传送区；接收分支不读取它不等于可传0或任意值。第五阶段确认上层消息映射不校验文本长度，基类本业务分支跳到 `0x523161`，不额外读取 cbData/lpData。[第五阶段 N8/N9]

历史固定正文真实验证的发送端与接收端均为386：COPYDATASTRUCT为12字节，载荷322字节。amd64结构24字节仅经ABI测试，不代表实际发送已验证。其他业务常量 `0x0fe1c10b / 0x0fecf10b / 0x0fecf10e` 不属于本用例，不混用。

### 2.2 三个字符串

| lpData偏移 | 静态用途 | 成功用例 | 容量状态 |
|---|---|---|---|
| `0x000` | 选择器，经转换后作为 `0x4c41b0` 的 ECX 输入 | `group_id`，NUL在 `0x008` | 厂商容量未知 |
| `0x064` | 目标值，作为栈参数传给目标查找 | 本地配置的唯一原始 QGroupId，32个十六进制字符 | 不把200字节偏移差当作容量 |
| `0x12c` | 正文，经转换后作为 EDX 输入 | 21字节 `NWT-GW-AUTH-TEST-0001`，NUL在 `0x141` | 厂商正文上限未知 |

调用点为 `0x44a124–0x44a1e3`；`0x44a1f9–0x44a20f` 的寄存器/栈准备与 E23 参数使用共同确认用途。[E22-I、E23-I]

固定实验缓冲区先清零，再原样写入三个字符串，`cbData=0x142=322`。当前自定义正文通过校验及换行兼容后，仍清零并按相同偏移写入三个字符串，载荷长度与 `cbData` 均为 `0x12c + 转换后UTF-8正文字节数 + 1`，包含最终 NUL；`ipc.Message` 的字节布局仍为历史 322 字节。偏移差不是 `char[100]+char[200]+char[N]` 的结构证据，空隙不是已恢复的厂商保留区。[阶段八第4节；当前预算依据见第3.1节]

### 2.3 UTF-8、终止与读取范围

三个字符串分别在 `0x44a130 / 0x44a178 / 0x44a1c0` 调用 `0x44cbd0`；它两次使用 `MultiByteToWideChar(65001,0,source,-1,...)`，即 UTF-8、NUL终止。[E22-U；第七阶段转换指令]

- 查询宽字符数后按需要分配宽字符串，再转换；宽缓冲区大小不是 UTF-8 源容量。字符串对象的7/8是内联/堆存储选择，也不是消息上限。[第七阶段 P1]
- `dwFlags=0` 不启用严格非法UTF-8拒绝；不能声称任意非法序列会被客户端拒绝。
- 文本分支没有按 cbData 截断、strnlen 或先按100/200字节复制。NUL不在传送区内会导致区外读取风险；未进行该类异常实验。
- 正文后到首个NUL之前仍可能属于正文；已复核正常路径和基类没有独立第四字段读取，这不证明生产者没有尾部字段。

令传送区为B，C=cbData；对于每个起点 `o ∈ {0,0x64,0x12c}`、NUL前的字节数n，必要条件为 `0 ≤ o < C` 且 `o+n < C`，`B[o+n]==0`；字段不得相互覆盖。成功用例满足这些条件，**条件本身不是任意长度发送许可**；自定义正文还须通过第 3.1 节的内容预算，不能只满足 NUL 范围就发送。原第七阶段“不能据此编写发送器”的结论是当时完整厂商契约的判断；后续明确授权的固定实验已有独立接收成功证据。

## 3. 目标解析与正文处理

| 选择器 | 字符串地址 | 静态行为 | 当前模块范围 |
|---|---|---|---|
| `group_id` | `0x9d15d8` | `0x4ee740 → 0x4edfc0` 查现有群表，`0x4b9c80 → 0x617600` 原生群发送 | 原生路径已验证并采用；当前 `SendGroup(groupID, message)` 显式接收合法群 ID，正文通过换行兼容和内容预算，不与配置比较 |
| `user_id` | `0x9d15b0` | `0x4f48d0 → 0x4f49e0 → 0x4b9320`；未知用户可创建状态4本机记录 | 当前 `SendUser(userID, message)` 原样接收合法用户 ID，正文通过换行兼容和内容预算，不与配置比较；固定正文已获真实独立接收确认，历史调用见[单聊验证01](../reports/ipc-phase12/user-confirmation-01.md) |
| `group_name` | `0x9d15c0` | 查名称匹配集合并遍历 | 不采用 |
| `user_name` | `0x9d158c` | 查名称匹配用户并遍历 | 不采用 |
| `user_ip` | `0x9d15a0` | 按IP查用户集合并遍历 | 不采用 |

依据为 E23、E25、S1、补充 E16。厂商 ID 完整语法、大小写/空白规则及列表语法未恢复；当前原样使用调用方显式提供且已核实的单个 ID，不据名称补造规则。Adapter 只做当前载荷所需的格式检查，不以配置目标判断授权。

`SendUser`是明确新增的代码能力，不继承群用例的运行成功证据。E22-I的三字符串读取和E23-I的正文处理均在选择器分流之前，因此沿用同一 UTF-8/NUL、偏移和正文处理；固定正文仍为 322 字节，自定义正文按转换后字节数分配。`0x4c447a–0x4c4499`识别 `user_id` 并调用 `0x4f48d0`；E25-U只见一次用户查找和一次节点追加，没有列表展开的正面证据。Adapter不解析列表，也不进行名称/IP替代；用户ID按完整原值使用，只校验有效UTF-8、非空、无NUL及当前载荷的199字节限制，不套用ASCII或群ID格式，见[模块说明](../../tools/ipc.md#显式目标与自定义正文)。

未知ID可在 `0x4f49e0` 创建状态4记录；新对象名称为空时，`0x4f48d0` 不把它加入本次发送集合，因此不能推导“新建状态4对象就一定离线排队”。已有名称非空的离线联系人则可进入集合；`0x4b9320`读取用户对象 `+0x1e0` 后，状态4进入本机离线保存分支，非4才调用 `0x5bc200`。[E16、E14/H9；补充U11、U14–U16见联系人报告第5节] API成功既不确认用户存在，也不确认联网发送；开发期间操作真实客户端做单聊复验须另行明确授权具体目标、正文与次数，送达仍须独立接收观察确认；这不是 Adapter 的目标权限检查。

联系人ID的直接来源是 `<数据根目录>/cache/db/nr` 的 `OrgStruct_<当前身份>`：`Type=1` 的 `Id` 赋给 `CUser +0x20`，XML `Data/U/Name` 赋给原始名称 `+0xb0`，不使用 `PriKey`、IP、路由号或UI转换后的“复制用户ID”。完整字符串比较不折叠大小写、不trim。只读提取与codec前置条件以[联系人报告第6节](../reports/user-id/user-id-report.md#6-windows-端联系人只读提取与唯一匹配方案)为准，不从群成员表替代获取。后续[nr-codec报告](../reports/nr-codec/nr-codec-report.md)已确认固定版 `aes128cbc / legacy=1` 兼容并完成合成验证；当前[只读工具](../../tools/contactid.md)通过Windows合成加密测试，后续[授权真实副本验证](../reports/user-id/readonly-validation.md)也已成功只读打开；指定原始名称的联系人匹配为not_found；操作者另确认该名称属于本机后，已依据唯一身份表后缀确认本机原始ID，未配置或发送。

正文在 `0x4c41b0` 中先经 `CStringHelper::Replace @ 0x568ef0`，再于 `0x4c42a6` 进入 `0x4b8470` 构造客户端自身内容。[E23-I、E24]

[正文静态报告](../reports/ipc-body/body-contract-report.md)确认普通文本的实际LF/CRLF/CR分别保留，先写入JSON，再由 `0x659a80` 对完整JSON做六项XML风格转义；其中空格转为 `&nbsp;`，不等同于通用XML serializer。群 `0x679c10` 与单聊 `0x65ca70` 的 `0x40000` 容量按完整XML的UTF-16 units连同NUL计算；超容会清空输出并调用invalid-parameter处理，两个packer不检查格式化结果。发送端不需要手拼JSON或原生网络XML；这些静态事实不代替下述运行验证。

第五阶段 N10–N13 确认：`0x9d1580` 是四字符字面串 `\r\n`，`0x9b6860` 是实际 CRLF；`0x4c4240–0x4c4253` 把前者替换成后者。这是客户端字面 CRLF 替换的静态事实，不是通用转义规则；后续序列化空间研究与当前 Adapter 兼容规则分别如下，不改写历史成功用例。

### 3.1 自定义正文的序列化预算

**运行反例**：随后[261003字真实验证](../reports/ipc-phase12/custom-max-body-abnormal.md)发生客户端卡顿/退出及用户报告的持续重试，未确认完整接收；用户自行重装恢复。下面的证明只约束序列化空间，不保证客户端稳定或送达，当前代码尚未额外限制产品输入长度。

当前采用本地[按正文内容计算 IPC 序列化预算](../reports/ipc-message-content/refined-body-bound.md)第 3 节。完整固定元数据、片段结构和两个 XML packer 的依据位于固定提交 [`6d06c31` 的研究报告](https://github.com/shenleg/neiwangtong_rea/blob/6d06c31/docs/research/reports/ipc-body-bound/body-bound-report.md)及[证据索引](https://github.com/shenleg/neiwangtong_rea/blob/6d06c31/docs/research/reports/ipc-body-bound/evidence-index.md)，不把远端报告写成尚不存在的本地链接。

转换后正文须非空、有效 UTF-8、无 NUL。令 `U` 为其 UTF-16 code units 数，`D` 为 ASCII 点号数，`F` 为正文值经过客户端 JSON 字符转义和外层转义后的内容长度，不含片段界定引号。`F` 可逐字符累加：双引号 7；反斜杠 2；单引号及 ASCII 空格 6；`&` 为 5；`<`/`>` 为 4；BS/TAB/LF/FF/CR 为 2；其他非 NUL C0 为 6；其他基本平面字符 1；补充平面字符 2。

每个链接至少占 4 units 且消耗一个点号；普通片段数 `T` 满足 `T <= min(L+1, U-4L)`。普通片段固定开销 446、链接片段 328、根开销最多 501，故完整转义 JSON 的保守上界为：

```text
Q = min(D, floor(U/4))
上界 = F + 501 + max(446*min(L+1, U-4L) + 328*L), 0 <= L <= Q
```

最大值只需比较 `a = min(Q, floor((U-1)/5))` 和 `b = min(Q, a+1)` 两个整数点，不模拟 URL 识别器、不递归构造片段。上界不超过两个已定位 XML packer 的共同预算 **261950 units** 才继续发送；超出时在客户端调用前明确拒绝，不截断、不拆分、不重试，不实际试发验证。

预算证明的是已分析样本的序列化空间，不是厂商最大消息长度，不证明传输、显示、离线保存或真实送达。上界超出只说明本算法无法证明空间足够，不等于证明客户端实际一定失败。固定正文限制及统一 512 字节限制均取消；UTF-16 计数仅用于预算，实际载荷仍按 UTF-8 字节数分配。发送端不提前转义正文或手拼网络 JSON/XML。

### 3.2 Adapter 换行兼容

实际 LF、CRLF、CR 原样保留；新增兼容把字面四字符 `\r\n` 转为实际 CRLF、字面两字符 `\n` 转为实际 LF。扫描输入时先匹配较长序列，单次字面匹配替换，不递归、不使用通用 `Unquote`，不解释 `\t`、`\u` 或单独字面 `\r`。后一项字面 LF 兼容是 Adapter 行为，不冒充已证实的客户端规则。

不提供双反斜杠保护，额外反斜杠不能阻止后续字面换行序列被转换；因此字面 `\r\n` 与 `\n` 不能原样发送，路径或代码正文的调用者必须了解。预算、载荷和日志 `text` 均使用转换后正文；`RecordObservation` 校验已保存的合法正文和唯一实际调用，不重新转换正文或修改历史事件。本地预算研究文档当时拒绝字面 `\r\n`，当前接口改为先转换再套用其公式，历史报告保持原文。

这些规则已有离线单测及 Windows 386 独立 fixture 的字节验证；随后另经明确授权完成[群聊LF与私聊CRLF的自定义三行正文接收确认](../reports/ipc-phase12/custom-message-newlines-confirmation.md)，包含中文、英文、数字和emoji。随后500汉字五段的LF/CRLF兼容也已独立接收确认，见上述[500汉字报告](../reports/ipc-phase12/custom-500-newlines-confirmation.md)；更长正文、单独CR和其他显示边界仍未真实验证。

UI群表与原生管理器群表不同。`0x617600` 还检查管理器字段、群ID和构造后正文非空；其资格函数 `0x61afe0` 在找到群后，`0x61b02e–0x61b04b` 有本机ID相等即成功的分支，不是完整群权限校验。成员投递调用 `0x6114f0`，不汇总成员送达。[E19；群状态续查]

## 4. 返回值、窗口与运行事实

- `OnMessage @ 0x535d50`、`WindowProc @ 0x535bc0` 的正常WM_COPYDATA映射预置结果1，不保存业务处理器EAX；查群为空或下级失败也可能得到1。只有独立接收观察才是本次送达证据。[第五阶段N9、N22–N26]
- 候选的 CMainDialog 身份已在一次授权观察中通过虚表及对象内HWND确认；当前发送仍每次重新Resolve，不依赖旧PID/HWND或调试器。任意UI状态、多实例/跨会话及标准最小化未全部验证。[窗口绑定续查]
- 成功发送验证了当前同用户、完整性、session、desktop下的386调用可达性；没有修改UIPI、提权、注入或读取凭据。
- 342字节快照确认当时授权ID在UI/原生两张群表中均存在；121字节快照确认当时成员集合非空、路由表非空、传输对象字段非零。它们只证明当时元数据，不追溯首次调用轨迹，也不是每次发送的前置操作。
- 0002未实际调用；观察器等待超时和控制台问题不是IPC发送失败。成功复验没有使用观察器。
- `unknown`表示缺少接收证据；程序因UI观察unknown退出码1不等于没有调用。补充接收观察另行保存，不改写原始事件。[成功复验报告]

未恢复完整角色/禁言、异常时业务状态、离线补发、全群 ACK；跨位数实际发送仍不开放，自定义正文仅在上述校验和内容预算内提供，其中三行中文及500汉字五段的LF/CRLF兼容均已有上述独立接收证据，更长正文及其他边界仍未真实验证。下一步不再重复接收端逆向、生产者搜索或调试执行点等待；停止的路线见 [validated-route.md 第3节](validated-route.md#3-停止继续验证的路线)。

## 5. 证据索引与复用方式

以下后缀仅供定位，不改变原始Evidence ID。优先读取相应小文件，不一次加载完整包。

| 标记 | 函数/内容 | 单次查询文件 | 完整 Evidence ID |
|---|---|---|---|
| E22-P | `0x449c60` 接收端伪代码 | [0082](../reports/gateway-static/0082-procedure_pseudo_code.json) | `ev_acdffca2137a48fa2ca47862b1048633f8e329196bea438815a8e8d5be8a1a53` |
| E22-I | 入口指令、字段和调用点 | [0107](../reports/gateway-static/0107-read_function_instructions.json) | `ev_54eb49153b17019a3fd40ec4b7410c9fbb28af0820a320ffb94ee230a3a3e3fa` |
| E22-U | `0x44cbd0` UTF-8/NUL | [0102](../reports/gateway-static/0102-procedure_pseudo_code.json) | `ev_55e9d1c18e869d695db7bbce0670183a838e02cb8b886ed62a29e9af0c7d477d` |
| E23-P | `0x4c41b0` 分流 | [0086](../reports/gateway-static/0086-procedure_pseudo_code.json) | `ev_7d8dd8829f020ce87d0522d93c760f1e1a4a77f9b512caa91d079d5a14eb186c` |
| E23-I | 分流寄存器与参数 | [0108](../reports/gateway-static/0108-read_function_instructions.json) | `ev_1ac48d41a61fa5a2855f4b68d9a83c51a57b894e0ad06e82fcec0ba285e9b307` |
| E23-G | `0x4b9c80 → 0x617600` | [0096](../reports/gateway-static/0096-procedure_pseudo_code.json) | `ev_2032cf47084fb7c1f2b0885ad292b86c19c8b20d41ef4a3ee170239e197a2674` |
| E24 | `0x4b8470` 正文构造 | [0103](../reports/gateway-static/0103-procedure_pseudo_code.json) | `ev_1494a83ac2f8c587de7cec975a7e084c5e8a7e96e91a7af0e29fb23d853d5c12` |
| E25-U | `0x4f48d0` 用户查找 | [0100](../reports/gateway-static/0100-procedure_pseudo_code.json) | `ev_552d8130062183f991aad5619b6679c5111ba9594da430ef5df113ab6252288d` |
| E25-G1 | `0x4ee740` 群查找包装 | [0095](../reports/gateway-static/0095-procedure_pseudo_code.json) | `ev_e634e94298a89381b255a69e1b83322cd1e97c15d4db057e6751ecb97be5f613` |
| E25-G2 | `0x4edfc0` 群查找 | [0101](../reports/gateway-static/0101-procedure_pseudo_code.json) | `ev_f56396820bc581983dc5b523e7726d4d4dbd5466ee3b3b412dd30438a0667f79` |
| S1 | 五个选择器字符串 | [0116](../reports/gateway-static/0116-search_strings.json) | `ev_9bf29a6ba84e4a7fff249d8f20cd34fa7ee1d778816a81df099480749aec6d30` |
| E16 | 未解析用户状态4 | [0110](../reports/gateway-static/0110-procedure_pseudo_code.json) | `ev_c959dd3d77da1c1e1c7f25b787e766d99d24706a731aaae8c3c96ee3e2e20cf6` |
| E14/H9 | `0x4b9320` 单聊及状态4分支 | [0035](../reports/gateway-static/0035-procedure_pseudo_code.json) | `ev_585b8c6bd9d12509095fdc5e1a15e3bece344edf8686d9e29b1bfb112368edf7` |
| E19 | 群条件与成员投递 | [历史报告第3节](../reports/gateway-static/gateway-report.md#3-原生群聊发送过程) | `ev_17d599ba7a6250a059051f51594bdcf6194448246f10f3ac131758f1b83d01b0` |

第七阶段补充证据及范围见[该阶段报告](../reports/ipc-phase7/ipc-phase7-report.md)：

| 内容 | Evidence ID |
|---|---|
| 两遍转换和宽缓冲分配 | `ev_89899b27794c743dad2e0e08164ad975f47b4c9993105d152c4258592679e4aa` |
| `0x408420` 宽NUL扫描/构造 | `ev_d8bb8efc5399871153317b3d866de24dea186b4ba062cc70f3aa3e3bed0f6689` |
| `0x408330` 赋值/复制/增长 | `ev_33c18e9ef572ab3daf082349d2cce74e5cdb02fa064fcca580eddeac16d64e67` |
| `0x44cc7e` 释放后清理 | `ev_c8de904833360d72b27329bacfc09022a8fa323cccc9e9422ea31b1d089bf981` |

历史完整包和分析快照均含245条相同证据，样本身份一致，analysis profile digest为 `7810ea9437da59780820ec6fedb47ce47390841a95d527ddb6c3b3700fe180cb`。快照只有1条primitive及1条workflow缓存，不是完整Ghidra工程，不保证任意查询离线重放；包的unknowns为空不表示协议没有未知项。Ghidra把部分 `0x87fc86` 调用标成hasNoReturn，可能截断伪代码，完整行为优先对照原始指令。导出/关闭回执 `0120 / 0121` 的临时路径只是历史位置，使用仓库实际文件。

## 6. 历史阶段只作证据，不再作为待办

| 资料 | 当时产出与当前用途 |
|---|---|
| [历史总报告](../reports/gateway-static/gateway-report.md) | E22–E25和原生协议研究，当前不继续替代路线 |
| [阶段五](../reports/ipc-phase5/ipc-phase5-report.md) | 字段、窗口分发和LRESULT指令；当时未恢复生产者 |
| [阶段七](../reports/ipc-phase7/ipc-phase7-report.md) | 读取边界和生产者搜索；当时“不可实现”不覆盖后续获授权固定实验 |
| [阶段八](../reports/ipc-phase8/ipc-phase8-report.md) | 固定实验载荷与测试，当时未真实调用 |
| [阶段九](../reports/ipc-phase9/ipc-phase9-report.md) | 关闭附加窗口后唯一定位，当时未取得群ID |
| [群ID研究](../reports/qgroup-id/qgroup-id-report.md) | `data/qrp`、身份表和Name→QGroupId，非聊天或凭据 |
| [阶段十](../reports/ipc-phase10/ipc-phase10-report.md) | 私有单群配置绑定与实际预检 |
| [阶段十一](../reports/ipc-phase11/ipc-phase11-report.md) | 首次调用unknown；其后[窗口绑定](../reports/ipc-phase11/window-binding-followup.md)与[群状态](../reports/ipc-phase11/group-state-followup.md)观察，不再重复 |
| [阶段十二](../reports/ipc-phase12/ipc-phase12-report.md) | 0002未调用；后改回原方式并取得独立接收成功，见本文第1节 |

当前模块和命令使用见 [internal/ipc说明](../../tools/ipc.md)。后续只围绕已验证路径开发；新业务范围须有明确需求和授权，不从上述历史报告重新启动已停止的恢复任务。
