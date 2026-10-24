# 正式 IPC Adapter

`internal/ipc` 提供非交互式载荷构造、窗口定位、身份复核、单次发送与日志，不依赖 `cmd/ipcexperiment`、stdin、stdout 或人工输入。`Config / LoadConfig` 保留给实验 CLI 读取目标，不是 Adapter 的授权配置。

群与单聊继续采用[已验证路线](../research/ipc/validated-route.md)：正常登录的唯一本机客户端、业务 WM_COPYDATA、Windows 386。固定 `NWT-GW-AUTH-TEST-0001` 的群与单聊均已有独立接收成功证据，单聊见[单聊验证01](../research/reports/ipc-phase12/user-confirmation-01.md)。当前接口保持显式目标和 `message` 参数，支持按内容预算检查的自定义正文与下述换行兼容；实现经离线单测和 386 独立 fixture 后，已另获授权完成[自定义三行消息的群聊和私聊真实接收确认](../research/reports/ipc-phase12/custom-message-newlines-confirmation.md)。不开放名称/IP 目标、HTTP 或生产部署，测试不真实发送。

## 1. 可复用接口

| Interface | 行为 |
|---|---|
| `ipc.LoadConfig(path)` | 严格读取群和/或用户目标 JSON，返回 `Config`，不查窗口或发送；仅供实验 CLI 等调用方读取目标 |
| `ipc.NewAdapter(exePath string) (*Adapter, error)` | 只读校验并绑定唯一客户端，不接收或保存配置目标，不创建日志 |
| `adapter.Preflight()` | 重新核对已绑定的同一实例，返回 `Preflight`，不输出或发送 |
| `adapter.SendGroup(groupID, message string) (SendResult, error)` | 校验显式群 ID，转换正文换行并检查内容预算，内部生成并持久化 AttemptID，然后只尝试发送一次 |
| `adapter.SendUser(userID, message string) (SendResult, error)` | 校验显式原始用户 ID，转换正文换行并检查内容预算，内部生成并持久化 AttemptID，然后只尝试发送一次 |
| `adapter.RecordObservation(attemptID, observation)` | 校验既有实际调用并追加调用者提供的观察，返回本条观察的 `Delivery, error`，不发消息 |

使用顺序示例，`verifiedGroupID` 是已核实的目标 ID；开发期间不能仅凭此示例操作真实客户端复验：

```go
adapter, err := ipc.NewAdapter(exePath)
if err != nil {
    return err
}
result, err := adapter.SendGroup(verifiedGroupID, ipc.Message)
// 即使 err 非 nil，也保留 result 中的标识、占用和调用事实；不自动重试。
```

Adapter 构造时只绑定客户端，每次预检或发送仍重新 Resolve，不用缓存句柄直接发送。实例或窗口变化会报错，不自动换进程；同一 Adapter 的方法串行执行，不持有人工确认输入或接收观察等待。客户端须由操作者正常登录，Adapter 不读取凭据或替代登录流程。

Adapter 不维护目标白名单，不将参数与 `Config` 比较。合法目标配合通过正文校验的消息调用发送方法即尝试发送，不再单独等待授权确认；默认调用方有权向指定目标发送，Adapter 不另设权限检查。程序化调用不需要先加载配置。

### 显式目标与自定义正文

- `SendGroup(groupID, message)` 的群 ID 必须为 32 个十六进制字符；不按群名重匹配，不向候选 ID 试发。
- `SendUser(userID, message)` 使用[只读联系人提取工具](contactid.md)核实的完整原始用户 ID，不 trim、不 split、不折叠大小写，也不套用 ASCII 或十六进制正则。当前载荷只接受 1 至 199 字节有效 UTF-8 且无 NUL；199 字节仅保证目标 NUL 不覆盖正文起点，不是厂商字段容量。过长 ID 明确拒绝，不截断、不改写。
- 两个方法均接受非空、有效 UTF-8、无 NUL 的自定义 `message`，不 trim；空白和实际换行本身不会被删改。非法正文或预算超出时明确返回错误，不发送、不创建发送日志。
- `ipc.Message` 继续保留为历史默认及 CLI 固定测试正文 `NWT-GW-AUTH-TEST-0001`，不再是 Adapter 唯一允许值。

#### 换行兼容

- 实际 LF、CRLF、CR 原样保留，不统一换行格式。
- 字面四字符 `\r\n` 转为实际 CRLF；字面两字符 `\n` 转为实际 LF。扫描输入时先匹配较长的 `\r\n`，只对原输入作一次字面匹配替换，不递归处理替换结果。
- 不使用通用 `Unquote`，不解释 `\t`、`\u` 或单独的字面 `\r`；这些字符保持原样。
- 不提供双反斜杠保护：额外反斜杠不能阻止后面的 `\r\n` 或 `\n` 被转换。因此这两种字面序列不能原样发送，发送路径或代码正文的调用者必须了解这一规则。Go 源码中的 `"\n"` 已是实际 LF，而 `"\\n"` 或原始字符串中的 `\n` 才是这里的字面两字符。
- 换行转换在预算计算和载荷构造之前完成；预算、UTF-8 载荷和预约日志 `text` 始终使用同一份转换后正文，不记录一份文字却发送另一份。

#### 按正文内容计算预算

取消固定正文限制和统一 512 字节限制，采用[按正文内容计算 IPC 序列化预算](../research/reports/ipc-message-content/refined-body-bound.md)第 3 节的证明。该研究文档当时拒绝字面 `\r\n`；当前接口改为先执行上述兼容转换，再对转换后的正文应用同一预算公式，不改写研究报告。

令 `U` 为转换后正文的 UTF-16 code units 数，`D` 为 ASCII 点号 `.` 的数量，`F` 为正文值经客户端 JSON 字符转义及外层转义后的内容长度（不含片段界定引号）。常见汉字占 1 unit，补充平面 emoji 占 2 units；UTF-8 字节数不用于替代 `U`。

| 正文字符 | 对 `F` 的贡献 |
|---|---:|
| 双引号 | 7 |
| 反斜杠 | 2 |
| 单引号、ASCII 空格 | 6 |
| `&` | 5 |
| `<`、`>` | 4 |
| BS、TAB、LF、FF、CR | 2 |
| 其他非 NUL C0 控制字符 | 6 |
| 其他基本平面字符 | 1 |
| 补充平面字符 | 2 |

普通片段固定开销为 446，链接片段为 328，根开销最多 501。共同预算为 **261950 units**，保守上界为：

```text
Q = min(D, floor(U/4))
a = min(Q, floor((U-1)/5))
b = min(Q, a+1)
H(L) = 446*min(L+1, U-4L) + 328*L
上界 = F + 501 + max(H(a), H(b))
```

正文已保证非空；两个候选点覆盖片段开销的最大值，无须模拟客户端 URL 识别或递归拆片。上界 `<= 261950` 才允许继续；超出时在调用客户端之前明确拒绝，不截断、不拆分、不试发、不自动重试。计数仅用于证明空间足够，不提前转义正文或手拼网络 JSON/XML。

这是已分析样本的序列化空间保守预算，不是厂商最大消息长度，也不证明传输、显示、离线保存或送达。**后续261003字真实验证已出现客户端卡顿/退出及用户报告的持续重试，用户自行重装恢复；当前代码尚未另加产品限额，不能将该序列化边界当作可用发送上限。** 见[异常与恢复记录](../research/reports/ipc-phase12/custom-max-body-abnormal.md)。[容量边界离线验证](../research/reports/ipc-message-content/adapter-capacity-boundary-check.md)已确认纯中文261003字接受、261004字拒绝，并通过386独立fixture完整传送783310字节载荷；该fixture不调用真实客户端。上界超出只表示无法用此算法证明容量足够。例如 5000 个 `中` 的上界为 5947，可通过；`a.aa ` 重复 334 次的上界为 262357，应拒绝，不能用统一字节限额代替内容预算。完整静态依据可从上述本地研究文档进入固定提交 `6d06c31` 的远端证据。

```go
result, err := adapter.SendUser(verifiedUserID, ipc.Message)
// 返回错误也不能自动重试；新的有效调用会生成新的 AttemptID。
```

`user_id` 共享 `0 / 0x64 / 0x12c` 三个 UTF-8、NUL 终止字段及正文处理。载荷按转换后正文动态分配，长度为 `0x12c + UTF-8正文字节数 + 1`；`ipc.Message` 仍得到历史 322 字节载荷。仅复用已有偏移并包含最终 NUL，不猜补厂商字段。静态路径为 `0x449c60 → 0x4c41b0 → 0x4f48d0 → 0x4f49e0 → 0x4b9320`，依据见[IPC 契约第3节](../research/ipc/ipc-contract.md#3-目标解析与正文处理)。

未知用户可能创建状态 4 本机记录；API 完成、LRESULT=1 都不证明用户存在、联网发送或送达。只有独立接收方在正确单聊中确认发送者、正文及本次新增一条，才记录 `RecipientObserved`。[单聊验证01](../research/reports/ipc-phase12/user-confirmation-01.md)使用的是当时需配置目标及显式 attemptID 的旧接口：授权配置仅在内存中构造，未修改原群配置；调用一次后由操作者独立确认，随后追加观察且未再发送。这些是历史验证事实，不是当前接口要求。

### 实验 CLI 配置

私有 JSON 仍可只包含 `authorized_user_id`，也可与原 `authorized_group_id` 同时配置；至少有一个字段。字段名保持兼容，但只供 CLI 等调用方读取目标，不向 Adapter 授权，也不限制 Adapter 的目标。下面仅为合成配置，不是实际目标，不能据此操作真实客户端复验：

```json
{"authorized_user_id":"synthetic-user_1"}
```

`LoadConfig` 保留严格解析：拒绝空值、null、数组、重复键、未知字段、尾随内容、非法 UTF-8 和未配对 Unicode 代理项，避免 JSON 解析静默替换身份字符。双字段配置不代表一次发送两个目标。原私有配置路径不变，不要求操作者重录已保存的目标。

## 2. 结构化结果与错误

`SendResult` 包含 `AttemptID`、`AttemptReserved`、`Preflight`、`Call` 和 `Delivery`，不包含真实群 ID、窗口标题、SID 或正文内容。`AttemptID` 是本次调用内部生成的标识，后续通过它追加观察，不由调用方传入。

| 状态 | 解释 |
|---|---|
| `AttemptReserved=false` | 本次没有独占创建日志；可能是输入、平台、预检、随机数生成或日志创建失败，须检查错误 |
| `AttemptReserved=true, Call.Invoked=false` | 已占用日志但最终检查或调用前步骤失败；记录保留 |
| `Call.Invoked=true` | 已进入 Win32 调用；结果、错误码、LRESULT、耗时与超时均保留，即使同时返回错误 |
| `Call.Completed=true` | 同步窗口调用完成，不代表送达 |
| `Delivery=ipc.Unknown` | `SendGroup / SendUser` 所有返回均为该值，调用结果不推导接收事实 |
| `RecordObservation` 返回 `ipc.RecipientObserved` | 调用者明确提供独立接收方的 `one`，正确群或单聊、发送者、正文及本次新增条数已核对 |

调用与日志失败立即返回，不等待控制台观察。既有日志冲突可用 `errors.Is(err, os.ErrExist)` 区分，碰撞直接返回错误，不换 ID 再试。调用后日志错误仍保留 `SendResult.Call`，不能因返回错误而当作没有发送。

## 3. 单次标识与历史记录

- Adapter 内部使用 `crypto/rand` 生成 `NWT-GW-<随机十六进制>` 格式的 AttemptID，调用方不传入标识。随机源失败直接返回错误，不使用时间戳或其他替代值。
- 每次有效发送调用都是新任务。相同目标和正文再次调用仍会生成新标识，可能再发一条；随机 ID 不提供调用方重试幂等性。Adapter 不自动重试，调用方也不能把返回错误当作安全重试依据。
- 日志目录仍为 `%APPDATA%\neiwangtong-gateway\ipc-experiment\`，文件仍为 `<attemptID>.jsonl`。首次 0001、03、04、05、单聊 01 及 unknown 任务的历史文件全部保留；接口改变不自动重放旧任务。
- 创建使用 `O_EXCL` 并 Sync 后才调用，失败或超时保留记录；群与用户共用该目录及标识，不因目标类型新增命名空间。即使随机标识与既有日志碰撞，也直接报错，不重新生成标识。
- 群预约事件保持原 `group_id` 字段，用户预约事件只写 `user_id`；观察从日志核对目标类型和原始 ID，不将同字符串的群与用户视作同一目标。
- 历史标识仍可用于读取既有日志并追加观察。其安全文件名规则保留：以 `NWT-GW-` 开头，后缀首字符为字母或数字，总长 8 至 128 字符，仅允许 ASCII 字母数字、下划线、连字符。该规则不是 IPC 字段规则，也不是当前随机标识的生成方式。
- 不删除日志、更换 APPDATA 或以新调用自动重试。开发期间操作真实客户端复验须另行明确授权具体目标、正文和次数，不能凭随机 ID 自行复验；这不是现行发送接口每次调用的额外要求。

## 4. 外部观察

`Observation` 包含来源 `Kind` 与固定词 `State`：

| 来源 | 允许状态 |
|---|---|
| `ipc.ClientObservation` | `stable / crashed / abnormal / unknown` |
| `ipc.SenderObservation` | `one / none / multiple / wrong_target / unknown` |
| `ipc.RecipientObservation` | `one / none / multiple / wrong_target / unknown` |

用发送返回的标识追加观察：

```go
delivery, err := adapter.RecordObservation(result.AttemptID, observation)
```

`RecordObservation` 按已有日志校验 attemptID、合法的群/用户目标、合法正文及唯一实际调用，只追加事件；不与 Adapter 配置目标比较，因为 Adapter 不保存目标。正文按已保存的 `text` 校验非空、有效 UTF-8、无 NUL 及内容预算，原始 JSON 中非法 UTF-8 或孤立 Unicode 代理项明确拒绝，不允许解码器静默替换。正文还须已是换行兼容后的形式；若仍有待转换的字面换行串则拒绝，不自动修正历史记录。不再要求正文等于 `ipc.Message`。没有实际调用、目标或正文不合法、记录损坏时拒绝，不把文件存在当作已发送。重新构造绑定客户端的 Adapter 也可为已有调用追加观察，包括合法的历史标识，无须重新发送或加载当时的目标配置。客户端异常/unknown、多条或错误目标会先保存观察再返回错误。

只有独立接收 `one` 返回 `RecipientObserved`；发送方 `one` 不能替代。返回值属于本条观察，不回写之前返回的 `SendResult`，也不声称全群送达。Adapter 不读取聊天、凭据、客户端 UI 或进程内存来自动构造观察。

## 5. 命令层与测试

`cmd/ipcexperiment` 只负责参数、显式发送确认、展示结构化结果和收集外部观察。命令调用 `ipc.LoadConfig` 读取实验目标，通过 `NewAdapter(exePath)` 绑定客户端，再显式传入群 ID 与 `ipc.Message`。

默认只读：

```bash
GOARCH=386 go run ./cmd/ipcexperiment \
  -exe 'C:\实际安装目录\ShiYeLine.exe' \
  -config local/ipcexperiment/config.json
```

无 `-send` 不创建尝试记录。命令仍只提供群发送，未新增单聊开关；仅用户配置可用于只读预检，不能用命令发送。实际群实验仍须显式 `-send` 并由操作者确认群目标与固定正文；已去除 `-attempt-id` 参数。命令从 `SendResult.AttemptID` 取得自动标识，用它追加观察，不再让操作者提供请求标识。本轮不增加 `message` 命令参数、HTTP 或群目标参数；CLI 仍只发送 `ipc.Message`，不提供可复制后直接发送的新请求示例。程序化调用 Adapter 不需要这些控制台输入，也不要求额外的单次授权检查；开发期间操作真实客户端复验仍须另行明确授权。

测试只使用合成数据与独立 fixture，需验证显式目标、自定义中文及 emoji、长正文、`U/D/F` 与预算边界、非法正文拒绝且无日志、实际与字面换行、长串优先及不解释其他转义、转换后载荷与日志一致、随机标识生成失败与碰撞、超时不重试、共享日志、历史记录以及重建 Adapter 后的观察校验。原 Win32、窗口识别、载荷和 ABI/跨进程 fixture 测试保留；386 跨进程 fixture 核对群与用户选择器、自定义正文和换行的实际传送字节，不调用内网通。随后已另经授权完成[自定义三行消息真实验证](../research/reports/ipc-phase12/custom-message-newlines-confirmation.md)：群聊字面 `\n` 转 LF、私聊字面 `\r\n` 转 CRLF，各发送一条并获独立接收确认；随后另经授权完成[500汉字五段真实验证](../research/reports/ipc-phase12/custom-500-newlines-confirmation.md)，两个目标各一条，全文和显式换行完整；更长正文及其他显示边界仍未真实验证。

运行 Windows amd64 与 386 的 `go test -count=1 -timeout 45s ./...` 和 `go vet ./...`。386 IPC fixture 仅操作独立测试子进程；不向真实客户端发送。非 Windows 仍明确拒绝平台操作，可编译纯逻辑测试。接口调整阶段仅使用合成测试。随后用户另行指定 `qun` 群完成[新接口群聊真实复验](../research/reports/ipc-phase12/explicit-target-group-confirmation.md)：直接调用 `SendGroup(groupID, ipc.Message)` 一次，内部生成标识，独立接收确认后 `RecordObservation` 返回 `recipient_observed`，未重试。历史[复验05](../research/reports/ipc-phase12/formal-adapter-confirmation-05.md)及[单聊验证01](../research/reports/ipc-phase12/user-confirmation-01.md)保持原有事实；随后新版 `SendUser(userID, ipc.Message)` 也完成一次[真实单聊复验](../research/reports/ipc-phase12/explicit-target-user-confirmation.md)，独立接收确认新增一条、发送者及正文正确，再追加观察得到 `recipient_observed`；未重试。
