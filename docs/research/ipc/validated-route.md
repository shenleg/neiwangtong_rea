# 已验证的 IPC 路线与后续工作范围

本文是当前技术路线的入口。维护发送模块、判断是否需要继续研究、准备真实验证时先读本文；字段和原始证据按需查阅 [IPC 契约](ipc-contract.md)，代码使用见 [internal/ipc 模块说明](../../tools/ipc.md)。

## 1. 当前结论

**已知异常**：[261003字真实验证](../reports/ipc-phase12/custom-max-body-abnormal.md)虽同步调用完成，随后用户报告客户端卡顿/退出及持续重试，并自行重装恢复；未取得完整接收确认。序列化容量不是产品可用上限，当前尚未增加产品级限额。500汉字五段仍是本轮已确认的长消息成功样本，2000字测试尚未执行。

**采用已成功送达的原方式：独立 Go Windows 386 程序，通过正常登录的唯一本机客户端的业务 WM_COPYDATA，向显式传入的原生群或用户 ID 发送消息。** 保留已验证的选择器、偏移、UTF-8/NUL 和单次调用方式；自定义正文按静态证据计算内容预算并动态分配载荷，不以调试器为发送前提，不重新研究替代发送方案。历史群验证只使用当次授权的唯一原生群，不代表当前 Adapter 仍绑定配置目标。

当前接口为 `NewAdapter(exePath string) (*Adapter, error)`、`SendGroup(groupID, message string) (SendResult, error)` 和 `SendUser(userID, message string) (SendResult, error)`。Adapter 只绑定客户端，不保存目标白名单、不与 `Config` 比较；合法目标配合通过校验的正文调用即尝试发送。`message` 支持非空、有效 UTF-8、无 NUL 的自定义正文，取消固定正文和统一 512 字节限制；`ipc.Message` 保留为历史默认及 CLI 固定测试正文。默认调用方有权向指定目标发送，Adapter 不另设权限检查。本轮不开放 HTTP 或新增 CLI 正文参数；开发期间操作真实客户端复验须另行明确授权，并说明具体目标、正文与次数，这不是现行发送接口的额外检查。

自定义正文采用[按内容预算的证明](../reports/ipc-message-content/refined-body-bound.md)：用转换后正文的 UTF-16 units 数 `U`、ASCII 点号数 `D` 和转义内容长度 `F`，加普通片段 446、链接片段 328、根开销最多 501，计算保守上界并与共同预算 **261950 units** 比较；完整公式见[模块说明](../../tools/ipc.md#按正文内容计算预算)。超出时发送前明确拒绝，不截断、不拆分、不试发、不重试；这不是厂商最大长度。

实际 LF/CRLF/CR 原样保留；字面四字符 `\r\n` 转为实际 CRLF，字面两字符 `\n` 转为实际 LF，先匹配长串，只作一次字面匹配替换。不作通用 `Unquote`，不解释 `\t`、`\u` 或单独字面 `\r`，也不提供双反斜杠保护；因此两种受支持的字面序列不能原样发送，路径或代码调用者须了解。预算、载荷和日志 `text` 均使用转换后正文。实现经离线单测与 386 独立 fixture 后，已另获授权完成[自定义三行消息真实验证](../reports/ipc-phase12/custom-message-newlines-confirmation.md)：群聊LF与私聊CRLF各一条，独立接收确认发送者、三行正文和emoji正确，两次均为 `recipient_observed`。这项新增证据不改写以下固定正文历史记录。

2026-10-23 的 `NWT-GW-AUTH-TEST-0001-confirmation-03` 运行得到：

- `invoked=true`，`api_return=1`，`last_error=0`，`LRESULT=1`，1 ms，未超时，调用后客户端存活。
- 操作者在独立接收端确认：正确原生群中按本次时间新增一条，发送者、正文均正确。
- 最终结果为 **`recipient_observed`**；不是仅凭 API 返回判断送达。

完整结果、构建摘要和逐次调用区别见[原0001方式复验](../reports/ipc-phase12/0001-original-method-confirmation.md)。首次消息后来被操作者发现，之前是观察遗漏；没有证据支持“原方式发送失败，所以必须另造载荷”。

模块迁移后，用户另行授权的 `0001-confirmation-04` 于 `2026-10-23T07:14:23.1251137Z` 再次调用成功，独立接收端确认本次新增一条、发送者正确，结果仍为 `recipient_observed`。这是重构后真实复验，见[复验04报告](../reports/ipc-phase12/ipc-refactor-confirmation-04.md)，不是仅凭单元测试宣布兼容。

正式Adapter拆分后，另行授权的 `0001-confirmation-05` 直接调用 `LoadConfig / NewAdapter / Preflight / SendGroup` 完成一次发送；独立接收端确认新增一条、发送者正确，再由 `RecordObservation` 返回 `recipient_observed`。全程无控制台输入或调试器，见[正式Adapter复验05](../reports/ipc-phase12/formal-adapter-confirmation-05.md)。

显式目标接口调整后，用户指定 `qun` 群进行一次真实验证：`SendGroup(groupID, ipc.Message)` 内部生成记录标识，调用一次，API 正常完成且未超时；用户独立确认新增一条、发送者和正文正确，再追加观察得到 `recipient_observed`。见[显式目标群聊复验](../reports/ipc-phase12/explicit-target-group-confirmation.md)。随后新版 `SendUser(userID, ipc.Message)` 也向用户明确确认的原接收者发送一次，独立接收确认后追加观察得到 `recipient_observed`，见[显式目标单聊复验](../reports/ipc-phase12/explicit-target-user-confirmation.md)；发送前曾因附属弹窗造成两个候选而停止，用户处理弹窗后才通过唯一窗口预检。

## 2. 唯一保留的实施顺序

1. **固定样本与身份**：客户端为内网通 3.4.3055，磁盘 SHA-256 必须匹配 `ipc.SampleSHA256`；使用正常登录的授权账号，群发送须具备该群正常成员资格，同 Windows 用户、完整性级别、session 和 desktop。
2. **显式提供已核实 ID**：调用方原样传入群或用户 ID，Adapter 不加载或保存配置目标，不做授权目标白名单检查。实验 CLI 仍可用 `LoadConfig` 读取 `authorized_group_id`；该字段是 CLI 目标来源，不是 Adapter 授权配置。不按名称重匹配，不向候选 ID 试发。
3. **复用现有窗口识别**：每次用 `windowprobe.Resolve` 重新定位并复核。多个候选即拒绝；必要时经操作授权正常关闭已确认的非主窗口，再重新定位。PID/HWND 不作为永久配置。
4. **复用字段并按正文分配载荷**：业务 `dwData=0x0fecf10f`，选择器为 `group_id` 或 `user_id`，目标和正文分别位于 `0x64 / 0x12c`，三个字符串为 UTF-8、NUL 终止。校验正文并完成换行兼容与内容预算检查后，按 `0x12c + 转换后UTF-8正文字节数 + 1` 分配载荷；322 字节仍是历史 `ipc.Message` 的实验长度，不是厂商结构总长或正文上限。
5. **先保存单次记录再调用**：每次有效发送调用内部用 `crypto/rand` 生成 `NWT-GW-<随机十六进制>`，独占创建同名日志并 Sync，随后最终复核实例，再调用一次 `SendMessageTimeoutW`，5000 ms，`SMTO_ABORTIFHUNG | SMTO_ERRORONEXIT`。随机数失败或日志碰撞直接报错，不换 ID 再试；失败或超时不自动重试。再次有效调用是新任务，随机 ID 不提供调用方重试幂等性。
6. **分别记录完成和送达**：API 返回、错误码、LRESULT、耗时、超时、进程存活属于调用事实；独立接收端在正确群或单聊中看到本次新增消息，才记为 `recipient_observed`。用 `SendResult.AttemptID` 追加观察；`RecordObservation` 按既有日志核对 attemptID、合法目标、合法正文和唯一实际调用，不再要求固定正文；只校验已保存正文，不重新转换或改写历史事件。不与 Adapter 配置目标比较，重建 Adapter 后也可继续记录。

客户端内部依据：`0x449c60 → 0x4c41b0 → 0x4ee740 / 0x4edfc0 → 0x4b9c80 → 0x617600`。[E22、E23、E25、E19；完整 Evidence ID 见契约第5节]

## 3. 停止继续验证的路线

“停止”是当前工程决策，不把未执行的方案虚构为实测失败。

| 路线或做法 | 当前决定 | 原因 |
|---|---|---|
| 原业务 WM_COPYDATA | **保留** | 已有独立接收成功证据，直接复用 |
| 以 IPMsg 或逐个私聊代替原生群 | 不采用、不继续验证 | 不满足原生群语义；兼容通道与原生群是不同过程，见历史报告第1、3节 |
| UI Automation、坐标点击发送 | 不采用、不继续验证 | 现有 IPC 已可用，不再维护另一套发送入口 |
| 自行实现原生网络协议或手拼网络 XML | 不采用、不继续验证 | 已由正常客户端完成身份和协议处理，无需替换 |
| DLL 注入、提权、认证绕过、修改 EXE 或消息过滤 | 禁止 | 不属于项目授权方式 |
| 猜 cbData、扫描长度、无 NUL、超预算或畸形载荷 | 禁止 | 不向真实客户端盲试长度或越界探测；依据静态预算、已知偏移和正文最终 NUL 动态分配载荷不属于猜长度 |
| 把找到生产者或完整厂商结构当作固定用例的前置条件 | 停止重复恢复 | 不妨碍当前已验证的固定用例；未知容量不因此变成已知 |
| 把调试器、硬件断点或读取运行内存作为每次发送前提 | 不采用 | 仅用于历史诊断，最终成功发送没有使用调试器 |
| 0002观察器控制台方案 | 停止继续验证 | 0002实际调用次数为0；等待超时和窗口交互问题不是另一条IPC失败证据 |
| 让操作者每次手敲包含群 ID 的长串 | 不作为协议要求 | 这是实验程序确认规则，不是客户端认证；明确授权的单次执行可由受控执行者提供确认，再由操作者确认接收 |
| 把 LRESULT=1 或 sender退出码直接当送达 | 不采用 | 正常消息映射可固定返回1；缺少UI观察也会令程序退出码为1 |

已保存的报告、证据、原始尝试记录继续保留；不删除它们或清空尝试日志，不重放旧任务。开发期间操作真实客户端复验须另行明确授权。

## 4. 尚未验证的范围

自定义中文、特殊字符、长正文及换行兼容已纳入 Adapter 的预算检查和离线测试范围；其中自定义三行中文、英文、数字、emoji及LF/CRLF兼容已有[群聊和私聊各一次真实接收证据](../reports/ipc-phase12/custom-message-newlines-confirmation.md)。随后[500汉字五段](../reports/ipc-phase12/custom-500-newlines-confirmation.md)也获群聊LF和私聊CRLF各一次独立接收确认，全文及显式换行完整；更长正文、其他特殊字符及显示边界仍未真实验证；后续真实验证须另行授权，不发送接近预算上限的载荷来探测容量。字段完整容量、厂商最大正文长度、名称/IP 选择器、跨位数实际发送、多实例/跨会话、离线补发、全群 ACK 和生产部署仍未验证或开放。序列化空间证明不替代传输、显示、离线保存及独立接收验证，不为模块改名重复真实发送。

当前单聊接口为 `SendUser(userID, message)`，原样使用已核实的用户 ID，不与配置比较，与群发送共用自定义正文、换行兼容及内容预算检查。静态路径为 `0x449c60 → 0x4c41b0 → 0x4f48d0 → 0x4f49e0 → 0x4b9320`，共用三字符串偏移及正文处理。[E22、E23、E25-U、E16；见契约第3节] **单聊已另经明确授权完成一次真实验证，独立接收确认新增一条、发送者和正文正确，最终为 `recipient_observed`**，见[单聊验证01](../reports/ipc-phase12/user-confirmation-01.md)。当时使用 `SendUser(userID, attemptID)` 旧接口并与内存配置核对，该历史事实不改写为当前接口的真实复验。该次请求标识 `NWT-GW-AUTH-TEST-0001-user-confirmation-01` 的日志保留，不重复执行旧任务；开发与测试不代表可以自行操作真实客户端复验。未知 ID 可能创建状态 4 本机记录，不能凭 API 完成判断用户存在或送达。

联系人只读工具现已实现，使用固定版codec直接只读打开完整离线加密副本，提供schema-only及原始名称精确匹配，详见[contactid说明](../../tools/contactid.md)。Windows合成测试覆盖8种页长；后续另经授权已完成[真实副本只读验证](../reports/user-id/readonly-validation.md)，联系人原始名称为not_found；操作者另确认输入为本机名称后，已从唯一身份表后缀确认本机原始ID，未改配置或发送，实际口令未输出或保存。工具不会修改发送配置，Adapter原样使用经人工核实的用户ID，继续保留单次记录及独立接收观察；正文能力按本文第 1 节执行，联系人读取不构成真实发送授权。

## 5. 当前模块与历史资料

- `internal/ipc` 提供非交互式正式 Adapter：`NewAdapter(exePath) / Preflight` 仅绑定及复核客户端，`SendGroup(groupID, message) / SendUser(userID, message)` 显式接收目标并返回结构化调用结果，`RecordObservation` 追加外部证据。调用方不需要输入流或控制台，详见[模块说明](../../tools/ipc.md)。
- `Config / LoadConfig` 保留供实验 CLI 读取目标，不是 Adapter 授权配置。`cmd/ipcexperiment` 默认只读，真实群实验仍须显式 `-send` 并确认群目标与固定正文；已去除 `-attempt-id`，从发送返回值取得自动标识追加观察。单聊仍仅提供 Adapter 接口，CLI 本轮不增加单聊开关或 `message` 参数，仍使用 `ipc.Message`；自定义正文仅通过既有 Adapter 参数传入。`internal/windowprobe` 继续负责窗口识别。
- 历史报告中的 `internal/ipcexperiment`、`ipc.Run`、`NewAdapter(exePath, config)` 及调用方显式提供 attemptID 的发送方法均为当时实现；不改写历史调用或成功证据，当前不保留旧接口转发层。
- 模块内部生成随机 AttemptID，日志仍在 `%APPDATA%\neiwangtong-gateway\ipc-experiment\`，全部历史记录保留。新标识不提供重试幂等性，开发期间也不能凭新标识自行操作真实客户端复验。接口调整阶段只用合成 fixture 测试和构建；随后另经用户授权完成上述显式目标群聊复验，只追加本次调用及观察记录，未修改真实配置或历史记录。
