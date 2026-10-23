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
| 实现 | `internal/ipc`非交互式Adapter，配置读取和发送返回结构化结果；实验交互仅在 `cmd/ipcexperiment` |
| 实验长度 | 322 字节，三个 UTF-8 字符串的 NUL 均在传送区内；不是厂商结构总长 |
| 成功任务 | 原方式03、迁移后04、正式Adapter直接调用05均获独立接收确认；各自日志保留，未自动重试 |
| 当前真实发送 | 已另经明确授权完成[固定正文单聊验证01](../reports/ipc-phase12/user-confirmation-01.md)；该次标识已占用，开发和测试不产生后续发送授权 |

生产者完整结构、字段容量、生产者 `cbData`、任意正文上限/转义仍未恢复；这些未知项**不撤销固定用例已成功的事实，也不再作为复用该用例的前置恢复任务**。不扫描长度、不追加猜测字段、不开放任意文本。静态事实、运行验证和未知范围分别标注，不互相替代。

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

本次发送端与接收端均为386：COPYDATASTRUCT为12字节，载荷322字节。amd64结构24字节仅经ABI测试，不代表实际发送已验证。其他业务常量 `0x0fe1c10b / 0x0fecf10b / 0x0fecf10e` 不属于本用例，不混用。

### 2.2 三个字符串

| lpData偏移 | 静态用途 | 成功用例 | 容量状态 |
|---|---|---|---|
| `0x000` | 选择器，经转换后作为 `0x4c41b0` 的 ECX 输入 | `group_id`，NUL在 `0x008` | 厂商容量未知 |
| `0x064` | 目标值，作为栈参数传给目标查找 | 本地配置的唯一原始 QGroupId，32个十六进制字符 | 不把200字节偏移差当作容量 |
| `0x12c` | 正文，经转换后作为 EDX 输入 | 21字节 `NWT-GW-AUTH-TEST-0001`，NUL在 `0x141` | 厂商正文上限未知 |

调用点为 `0x44a124–0x44a1e3`；`0x44a1f9–0x44a20f` 的寄存器/栈准备与 E23 参数使用共同确认用途。[E22-I、E23-I]

固定实验缓冲区先清零，再原样写入三个字符串，`cbData=0x142=322`。偏移差不是 `char[100]+char[200]+char[N]` 的结构证据，空隙不是已恢复的厂商保留区。[阶段八第4节]

### 2.3 UTF-8、终止与读取范围

三个字符串分别在 `0x44a130 / 0x44a178 / 0x44a1c0` 调用 `0x44cbd0`；它两次使用 `MultiByteToWideChar(65001,0,source,-1,...)`，即 UTF-8、NUL终止。[E22-U；第七阶段转换指令]

- 查询宽字符数后按需要分配宽字符串，再转换；宽缓冲区大小不是 UTF-8 源容量。字符串对象的7/8是内联/堆存储选择，也不是消息上限。[第七阶段 P1]
- `dwFlags=0` 不启用严格非法UTF-8拒绝；不能声称任意非法序列会被客户端拒绝。
- 文本分支没有按 cbData 截断、strnlen 或先按100/200字节复制。NUL不在传送区内会导致区外读取风险；未进行该类异常实验。
- 正文后到首个NUL之前仍可能属于正文；已复核正常路径和基类没有独立第四字段读取，这不证明生产者没有尾部字段。

令传送区为B，C=cbData；对于每个起点 `o ∈ {0,0x64,0x12c}`、NUL前的字节数n，必要条件为 `0 ≤ o < C` 且 `o+n < C`，`B[o+n]==0`；字段不得相互覆盖。成功用例满足这些条件，**条件本身不是任意长度发送许可**。原第七阶段“不能据此编写发送器”的结论是当时完整厂商契约的判断；后续明确授权的固定实验已有独立接收成功证据。

## 3. 目标解析与正文处理

| 选择器 | 字符串地址 | 静态行为 | 当前模块范围 |
|---|---|---|---|
| `group_id` | `0x9d15d8` | `0x4ee740 → 0x4edfc0` 查现有群表，`0x4b9c80 → 0x617600` 原生群发送 | 已验证并采用，原群行为不变 |
| `user_id` | `0x9d15b0` | `0x4f48d0 → 0x4f49e0 → 0x4b9320`；未知用户可创建状态4本机记录 | `SendUser(userID, attemptID)`接收原始用户ID，必须与配置的唯一授权用户完全相同；固定正文已获一次真实独立接收确认，见[单聊验证01](../reports/ipc-phase12/user-confirmation-01.md) |
| `group_name` | `0x9d15c0` | 查名称匹配集合并遍历 | 不采用 |
| `user_name` | `0x9d158c` | 查名称匹配用户并遍历 | 不采用 |
| `user_ip` | `0x9d15a0` | 按IP查用户集合并遍历 | 不采用 |

依据为 E23、E25、S1、补充E16。ID完整语法、大小写/空白规则及列表语法未恢复；当前原样使用显式配置的唯一ID，不据名称补造规则。

`SendUser`是明确新增的代码能力，不继承群用例的运行成功证据。E22-I的三字符串读取和E23-I的正文处理均在选择器分流之前，因此沿用同一UTF-8/NUL、偏移与固定322字节载荷。`0x4c447a–0x4c4499`识别 `user_id` 并调用 `0x4f48d0`；E25-U只见一次用户查找和一次节点追加，没有列表展开的正面证据。Adapter不解析列表，也不进行名称/IP替代；用户ID按完整原值使用，只校验有效UTF-8、非空、无NUL及当前载荷的199字节限制，不套用ASCII或群ID格式，见[模块说明](../../tools/ipc.md#显式用户配置)。

未知ID可在 `0x4f49e0` 创建状态4记录；新对象名称为空时，`0x4f48d0` 不把它加入本次发送集合，因此不能推导“新建状态4对象就一定离线排队”。已有名称非空的离线联系人则可进入集合；`0x4b9320`读取用户对象 `+0x1e0` 后，状态4进入本机离线保存分支，非4才调用 `0x5bc200`。[E16、E14/H9；补充U11、U14–U16见联系人报告第5节] API成功既不确认用户存在，也不确认联网发送；真实单聊验证仍须具体目标、固定正文与次数的单独授权及独立接收观察。

联系人ID的直接来源是 `<数据根目录>/cache/db/nr` 的 `OrgStruct_<当前身份>`：`Type=1` 的 `Id` 赋给 `CUser +0x20`，XML `Data/U/Name` 赋给原始名称 `+0xb0`，不使用 `PriKey`、IP、路由号或UI转换后的“复制用户ID”。完整字符串比较不折叠大小写、不trim。只读提取与codec前置条件以[联系人报告第6节](../reports/user-id/user-id-report.md#6-windows-端联系人只读提取与唯一匹配方案)为准，不从群成员表替代获取。后续[nr-codec报告](../reports/nr-codec/nr-codec-report.md)已确认固定版 `aes128cbc / legacy=1` 兼容并完成合成验证；当前[只读工具](../../tools/contactid.md)通过Windows合成加密测试，后续[授权真实副本验证](../reports/user-id/readonly-validation.md)也已成功只读打开；指定原始名称的联系人匹配为not_found；操作者另确认该名称属于本机后，已依据唯一身份表后缀确认本机原始ID，未配置或发送。

正文在 `0x4c41b0` 中先经 `CStringHelper::Replace @ 0x568ef0`，再于 `0x4c42a6` 进入 `0x4b8470` 构造客户端自身内容。[E23-I、E24]

第五阶段N10–N13确认：`0x9d1580` 是四字符字面串 `\r\n`，`0x9b6860` 是实际CRLF；`0x4c4240–0x4c4253` 把前者替换成后者。不能推导通用转义，单独换行、XML特殊字符和任意长度不在成功用例范围内。发送端不需要手拼原生网络XML。

UI群表与原生管理器群表不同。`0x617600` 还检查管理器字段、群ID和构造后正文非空；其资格函数 `0x61afe0` 在找到群后，`0x61b02e–0x61b04b` 有本机ID相等即成功的分支，不是完整群权限校验。成员投递调用 `0x6114f0`，不汇总成员送达。[E19；群状态续查]

## 4. 返回值、窗口与运行事实

- `OnMessage @ 0x535d50`、`WindowProc @ 0x535bc0` 的正常WM_COPYDATA映射预置结果1，不保存业务处理器EAX；查群为空或下级失败也可能得到1。只有独立接收观察才是本次送达证据。[第五阶段N9、N22–N26]
- 候选的 CMainDialog 身份已在一次授权观察中通过虚表及对象内HWND确认；当前发送仍每次重新Resolve，不依赖旧PID/HWND或调试器。任意UI状态、多实例/跨会话及标准最小化未全部验证。[窗口绑定续查]
- 成功发送验证了当前同用户、完整性、session、desktop下的386调用可达性；没有修改UIPI、提权、注入或读取凭据。
- 342字节快照确认当时授权ID在UI/原生两张群表中均存在；121字节快照确认当时成员集合非空、路由表非空、传输对象字段非零。它们只证明当时元数据，不追溯首次调用轨迹，也不是每次发送的前置操作。
- 0002未实际调用；观察器等待超时和控制台问题不是IPC发送失败。成功复验没有使用观察器。
- `unknown`表示缺少接收证据；程序因UI观察unknown退出码1不等于没有调用。补充接收观察另行保存，不改写原始事件。[成功复验报告]

未恢复完整角色/禁言、异常时业务状态、离线补发、全群ACK；跨位数与任意文本不开放。下一步不再重复接收端逆向、生产者搜索或调试执行点等待；停止的路线见 [validated-route.md 第3节](validated-route.md#3-停止继续验证的路线)。

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
