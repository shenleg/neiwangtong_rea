# 正式 IPC Adapter

`internal/ipc` 已从交互式实验流程中分离。载荷构造、窗口定位、配置读取、身份复核、单次发送与日志都可由其他Go入口复用，不依赖 `cmd/ipcexperiment`、stdin、stdout或人工输入。

仍只采用[已验证路线](../research/ipc/validated-route.md)：唯一授权原生群、固定 `NWT-GW-AUTH-TEST-0001`、Windows 386。接口正式化不等于开放任意正文、私聊、HTTP或生产部署；本次重构只测试和构建，没有新增真实发送。

## 1. 可复用接口

| Interface | 行为 |
|---|---|
| `ipc.LoadConfig(path)` | 严格读取现有单群JSON，返回 `Config`，不查窗口或发送 |
| `ipc.NewAdapter(exePath, config)` | 只读校验并绑定唯一客户端与配置群，返回 `*Adapter`，不创建日志 |
| `adapter.Preflight()` | 重新核对已绑定的同一实例，返回 `Preflight`，不输出或发送 |
| `adapter.SendGroup(attemptID)` | 持久化该请求标识后，只向配置群发送一次固定Message，返回 `SendResult, error` |
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

空 `Config{}` 只支持构造后的只读预检，`SendGroup`明确拒绝。绑定后的配置被复制；调用者修改原Config不会改变发送目标。配置格式、真实群ID和私有路径保持不变。

## 2. 结构化结果与错误

`SendResult` 包含 `AttemptID`、`AttemptReserved`、`Preflight`、`Call` 和 `Delivery`，不包含真实群ID、窗口标题、SID或正文内容。

| 状态 | 解释 |
|---|---|
| `AttemptReserved=false` | 本次没有占用标识；可能是输入、平台、预检或已有日志拒绝，须检查错误 |
| `AttemptReserved=true, Call.Invoked=false` | 已占用日志但最终检查或调用前步骤失败；记录保留，不恢复资格 |
| `Call.Invoked=true` | 已进入Win32调用；结果、错误码、LRESULT、耗时与超时均保留，即使同时返回错误 |
| `Call.Completed=true` | 同步窗口调用完成，不代表送达 |
| `Delivery=ipc.Unknown` | `SendGroup`所有返回均为该值，调用结果不推导接收事实 |
| `RecordObservation` 返回 `ipc.RecipientObserved` | 调用者明确提供独立接收方的 `one`，正确群、发送者、正文及本次新增条数已核对 |

调用与日志失败立即返回，不等待控制台观察。既有日志冲突可用 `errors.Is(err, os.ErrExist)` 区分。调用后日志错误仍保留 `SendResult.Call`，不能因返回错误而当作没有发送。

## 3. 单次标识与历史记录

- 标识须以 `NWT-GW-` 开头，后缀首字符为字母或数字，总长8至128字符，仅允许ASCII字母数字、下划线、连字符。该限制用于安全日志文件名，不是IPC字段规则。
- 不再有模块级固定 `AttemptID`，也不自动生成。调用 `SendGroup` 表示上层已取得对应目标、固定正文和一次调用的明确授权；这个方法不代替调用方的权限校验。
- 日志目录仍为 `%APPDATA%\neiwangtong-gateway\ipc-experiment\`，文件仍为 `<attemptID>.jsonl`。首次0001、03、04及unknown任务的旧文件继续阻止同名调用。
- 创建使用 `O_EXCL` 并Sync后才调用，失败或超时保留记录；不同Adapter实例和进程也不能争用同一标识重复发送。
- 不通过删除日志、更换APPDATA或自动改标识重试。同一目标的新真实消息仍需新的明确授权。

## 4. 外部观察

`Observation` 包含来源 `Kind` 与固定词 `State`：

| 来源 | 允许状态 |
|---|---|
| `ipc.ClientObservation` | `stable / crashed / abnormal / unknown` |
| `ipc.SenderObservation` | `one / none / multiple / wrong_target / unknown` |
| `ipc.RecipientObservation` | `one / none / multiple / wrong_target / unknown` |

`RecordObservation`校验已有记录的请求、配置群、固定正文及唯一实际调用，只追加事件。没有实际调用或记录损坏时拒绝，不把文件存在当作已发送。客户端异常/unknown、多条或错误目标会先保存观察再返回错误。

只有独立接收 `one` 返回RecipientObserved；发送方one不能替代。返回值属于本条观察，不回写之前返回的SendResult，也不声称全群送达。Adapter不读取聊天、凭据、客户端UI或进程内存来自动构造观察。

## 5. 命令层与测试

`cmd/ipcexperiment` 现在只负责参数、显式发送授权、确认输入、展示结构化结果和收集外部观察。配置校验不再复制在cmd目录中；命令调用 `ipc.LoadConfig` 和Adapter。

默认只读：

```bash
GOARCH=386 go run ./cmd/ipcexperiment \
  -exe 'C:\实际安装目录\ShiYeLine.exe' \
  -config local/ipcexperiment/config.json
```

无 `-send` 不创建尝试记录。实际实验额外要求显式 `-attempt-id` 和完整确认行；没有默认新标识、任意正文或群参数。程序化调用Adapter不需要这些控制台输入，但上层授权要求不变。本次不提供可复制后直接发送的新请求示例。

原Win32、窗口识别、载荷和ABI/跨进程fixture测试保留。原确认测试移到命令层，单次、超时、并发和观察测试改为直接调用Adapter；新增配置绑定、历史日志兼容、结构化失败、无效标识和无调用证据拒绝测试。

运行Windows amd64与386的 `go test -count=1 -timeout 45s ./...` 和 `go vet ./...`。386 IPC fixture仅操作独立测试子进程；不向真实客户端发送。非Windows仍明确拒绝平台操作，可编译纯逻辑测试。重构后另经用户明确授权，已直接调用正式Adapter完成[复验05](../research/reports/ipc-phase12/formal-adapter-confirmation-05.md)：SendGroup返回结构化调用结果，独立接收确认后RecordObservation返回RecipientObserved，全程没有控制台输入或调试器。
