# 正式 IPC Adapter

`internal/ipc` 已从交互式实验流程中分离。载荷构造、窗口定位、配置读取、身份复核、单次发送与日志都可由其他Go入口复用，不依赖 `cmd/ipcexperiment`、stdin、stdout或人工输入。

群发送继续采用[已验证路线](../research/ipc/validated-route.md)：唯一授权原生群、固定 `NWT-GW-AUTH-TEST-0001`、Windows 386。新增 `SendUser` 使用同一业务IPC的 `user_id` 选择器，沿已静态定位的原生单聊路径，仅允许配置中的唯一授权用户。另经明确授权，已完成一次真实单聊并获独立接收确认，见[单聊验证01](../research/reports/ipc-phase12/user-confirmation-01.md)。不开放任意正文、名称/IP目标、HTTP或生产部署；测试默认不真实发送。

## 1. 可复用接口

| Interface | 行为 |
|---|---|
| `ipc.LoadConfig(path)` | 严格读取群和/或用户授权JSON，返回 `Config`，不查窗口或发送 |
| `ipc.NewAdapter(exePath, config)` | 只读校验并绑定唯一客户端与配置目标，返回 `*Adapter`，不创建日志 |
| `adapter.Preflight()` | 重新核对已绑定的同一实例，返回 `Preflight`，不输出或发送 |
| `adapter.SendGroup(attemptID)` | 持久化该请求标识后，只向配置群发送一次固定Message，返回 `SendResult, error` |
| `adapter.SendUser(userID, attemptID)` | 显式用户ID须与配置授权用户完全相同；持久化请求标识后发送一次固定Message，返回 `SendResult, error` |
| `adapter.RecordObservation(attemptID, observation)` | 对既有实际调用追加调用者提供的观察，返回本条观察的 `Delivery, error`，不发消息 |

使用顺序示例，`authorizedAttemptID` 必须由上层在取得该次授权后明确提供，不能随失败自动换值：

```go
config, err := ipc.LoadConfig(configPath)
if err != nil {
    return err
}
adapter, err := ipc.NewAdapter(exePath, config)
if err != nil {
    return err
}
result, err := adapter.SendGroup(authorizedAttemptID)
// 即使err非nil，也保留result中的占用和调用事实；不自动重试。
```

Adapter构造时绑定客户端，每次方法调用仍重新Resolve，不用缓存句柄直接发送。实例或窗口变化会报错，不自动换进程；同一Adapter的方法串行执行，不持有人工确认输入或接收观察等待。

空 `Config{}` 只支持构造后的只读预检，两个发送方法均拒绝。绑定后的配置被复制；调用者修改原Config不会改变发送目标。原单群配置仍有效，私有路径保持不变。

### 显式用户配置

私有JSON可只包含 `authorized_user_id`，也可与原 `authorized_group_id` 同时配置；至少有一个字段。下面仅为合成配置，不是实际授权用户或发送授权：

```json
{"authorized_user_id":"synthetic-user_1"}
```

- `SendUser(userID, authorizedAttemptID)`显式接收已核实的原始用户ID，必须与构造时复制的 `AuthorizedUserID` 完全相同；没有该字段或参数不符即拒绝，不创建单次记录、不发送，也不改用群ID、姓名、IP或候选ID。参数不会改变授权配置。

```go
result, err := adapter.SendUser(verifiedUserID, authorizedAttemptID)
// verifiedUserID须与config.AuthorizedUserID完全相同；返回错误也不能自动重试。
```
- 群ID仍为32个十六进制字符。用户ID使用[只读联系人提取工具](contactid.md)核实的原值，不 trim、不 split、不折叠大小写，也不套用ASCII或十六进制正则。当前载荷只接受1至199字节有效UTF-8且无NUL；199字节仅保证目标NUL不覆盖正文起点，不是厂商字段容量。过长ID明确拒绝，不截断、不改写。配置解析拒绝非法UTF-8和未配对Unicode代理项，避免JSON解析静默替换身份字符。
- 空值、null、数组、重复键、未知字段或尾随内容均拒绝；双字段配置不代表一次发送两个目标，调用哪个方法就只使用对应目标。
- `user_id`共享 `0 / 0x64 / 0x12c` 三个UTF-8、NUL终止字段，固定322字节载荷；只改变选择器与目标，不改变正文。静态路径为 `0x449c60 → 0x4c41b0 → 0x4f48d0 → 0x4f49e0 → 0x4b9320`，依据见[IPC契约第3节](../research/ipc/ipc-contract.md#3-目标解析与正文处理)。
- 未知用户可能创建状态4本机记录；API完成、LRESULT=1都不证明用户存在、联网发送或送达。只有独立接收方在正确单聊中确认发送者、正文及本次新增一条，才记录 `RecipientObserved`。真实单聊验证仅使用内存用户配置，未修改原群配置；调用一次后由操作者独立确认，随后追加观察且未再发送，见[单聊验证01](../research/reports/ipc-phase12/user-confirmation-01.md)。


## 2. 结构化结果与错误

`SendResult` 包含 `AttemptID`、`AttemptReserved`、`Preflight`、`Call` 和 `Delivery`，不包含真实群ID、窗口标题、SID或正文内容。

| 状态 | 解释 |
|---|---|
| `AttemptReserved=false` | 本次没有占用标识；可能是输入、平台、预检或已有日志拒绝，须检查错误 |
| `AttemptReserved=true, Call.Invoked=false` | 已占用日志但最终检查或调用前步骤失败；记录保留，不恢复资格 |
| `Call.Invoked=true` | 已进入Win32调用；结果、错误码、LRESULT、耗时与超时均保留，即使同时返回错误 |
| `Call.Completed=true` | 同步窗口调用完成，不代表送达 |
| `Delivery=ipc.Unknown` | `SendGroup / SendUser`所有返回均为该值，调用结果不推导接收事实 |
| `RecordObservation` 返回 `ipc.RecipientObserved` | 调用者明确提供独立接收方的 `one`，正确群或单聊、发送者、正文及本次新增条数已核对 |

调用与日志失败立即返回，不等待控制台观察。既有日志冲突可用 `errors.Is(err, os.ErrExist)` 区分。调用后日志错误仍保留 `SendResult.Call`，不能因返回错误而当作没有发送。

## 3. 单次标识与历史记录

- 标识须以 `NWT-GW-` 开头，后缀首字符为字母或数字，总长8至128字符，仅允许ASCII字母数字、下划线、连字符。该限制用于安全日志文件名，不是IPC字段规则。
- 不再有模块级固定 `AttemptID`，也不自动生成。调用 `SendGroup / SendUser` 表示上层已取得对应目标、固定正文和一次调用的明确授权；这个方法不代替调用方的权限校验。
- 日志目录仍为 `%APPDATA%\neiwangtong-gateway\ipc-experiment\`，文件仍为 `<attemptID>.jsonl`。首次0001、03、04及unknown任务的旧文件继续阻止同名调用。
- 创建使用 `O_EXCL` 并Sync后才调用，失败或超时保留记录；群与用户共用该目录及标识，不因目标类型新增命名空间。不同Adapter实例和进程也不能争用同一标识重复发送。
- 群预约事件保持原 `group_id` 字段，用户预约事件只写 `user_id`；观察同时核对目标类型和原始ID，不将同字符串的群与用户视作同一目标。
- 不通过删除日志、更换APPDATA或自动改标识重试。同一目标的新真实消息仍需新的明确授权。

## 4. 外部观察

`Observation` 包含来源 `Kind` 与固定词 `State`：

| 来源 | 允许状态 |
|---|---|
| `ipc.ClientObservation` | `stable / crashed / abnormal / unknown` |
| `ipc.SenderObservation` | `one / none / multiple / wrong_target / unknown` |
| `ipc.RecipientObservation` | `one / none / multiple / wrong_target / unknown` |

`RecordObservation`校验已有记录的请求、配置目标类型与ID、固定正文及唯一实际调用，只追加事件。没有实际调用或记录损坏时拒绝，不把文件存在当作已发送。客户端异常/unknown、多条或错误目标会先保存观察再返回错误。

只有独立接收 `one` 返回RecipientObserved；发送方one不能替代。返回值属于本条观察，不回写之前返回的SendResult，也不声称全群送达。Adapter不读取聊天、凭据、客户端UI或进程内存来自动构造观察。

## 5. 命令层与测试

`cmd/ipcexperiment` 现在只负责参数、显式发送授权、确认输入、展示结构化结果和收集外部观察。配置校验不再复制在cmd目录中；命令调用 `ipc.LoadConfig` 和Adapter。

默认只读：

```bash
GOARCH=386 go run ./cmd/ipcexperiment \
  -exe 'C:\实际安装目录\ShiYeLine.exe' \
  -config local/ipcexperiment/config.json
```

无 `-send` 不创建尝试记录。命令仍只提供群发送，未新增单聊开关；仅用户配置可用于只读预检，不能用命令发送。实际群实验额外要求显式 `-attempt-id` 和完整确认行；没有默认新标识、任意正文或群参数。程序化调用Adapter不需要这些控制台输入，但上层授权要求不变。本次不提供可复制后直接发送的新请求示例。

原Win32、窗口识别、载荷和ABI/跨进程fixture测试保留。原确认测试位于命令层，单次、超时、并发和观察测试直接调用Adapter。用户入口测试覆盖配置快照、严格配置、载荷字段、缺少对应授权拒绝、群/用户共享标识并发、超时不重试、目标类型与观察匹配；386跨进程fixture同时核对群与用户选择器的实际传送字节，不调用内网通。

运行Windows amd64与386的 `go test -count=1 -timeout 45s ./...` 和 `go vet ./...`。386 IPC fixture仅操作独立测试子进程；不向真实客户端发送。非Windows仍明确拒绝平台操作，可编译纯逻辑测试。重构后另经用户明确授权，已直接调用正式Adapter完成[复验05](../research/reports/ipc-phase12/formal-adapter-confirmation-05.md)：SendGroup返回结构化调用结果，独立接收确认后RecordObservation返回RecipientObserved，全程没有控制台输入或调试器。
