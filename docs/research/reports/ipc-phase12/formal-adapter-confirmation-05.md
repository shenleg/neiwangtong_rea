# 正式 Adapter 非交互式发送复验05

本文记录用户明确要求发送一条消息后，对正式 `ipc.Adapter` 的一次真实复验。不包含真实群ID、聊天内容、凭据或私人日志副本。

## 结论

**直接调用正式Adapter已成功：同一授权原生群新增一条 `NWT-GW-AUTH-TEST-0001`，独立接收端确认发送者正确。随后通过 `Adapter.RecordObservation` 追加该观察，返回 `recipient_observed`。**

这次没有使用实验控制台、确认输入、调试器或自动重试，也没有改写模块源码、固定正文、配置和旧尝试记录。

## 实际调用

私有测试调用方按顺序使用：

1. `ipc.LoadConfig` 读取原配置，不输出真实群ID。
2. `ipc.NewAdapter` 和 `Preflight` 复核唯一客户端，PID 4488、HWND `0x18062C`，样本哈希及安全上下文通过。
3. `SendGroup("NWT-GW-AUTH-TEST-0001-confirmation-05")` 只调用一次，返回结构化结果。
4. 操作者选择“收到一条，发送者正确”后，调用 `RecordObservation`，来源为 `RecipientObservation`、状态为 `one`，只追加观察，不发送。

| 项目 | 结果 |
|---|---|
| SendResult输出时间 | `2026-10-23T08:59:31.1776212Z`，不是收件端自动时间戳 |
| 构建 | Windows 386，直接使用 `internal/ipc.Adapter` |
| 尝试标识 | `NWT-GW-AUTH-TEST-0001-confirmation-05` |
| 正文 | `NWT-GW-AUTH-TEST-0001` |
| AttemptReserved | true |
| Invoked / Completed | true / true |
| API返回 / 错误码 | 1 / 0 |
| LRESULT / 有效性 | 1 / true |
| 耗时 / 超时 | 1ms / no |
| 调用后进程存活 | true |
| SendGroup返回Delivery | unknown，未用API结果推导送达 |
| 独立接收观察 | 本次新增一条，发送者及原生测试群正确 |
| RecordObservation返回Delivery | **recipient_observed** |
| 真实发送次数 | 1 |
| 接收观察追加时新增发送 | 0 |

本次发送与观察调用均正常结束，不再因缺少实验控制台输入而把实际调用成功显示为交互错误。

## 记录与结束

- 原始尝试及接收观察追加到原APPDATA目录下的 `NWT-GW-AUTH-TEST-0001-confirmation-05.jsonl`，不改写之前事件或其他任务。
- 脱敏摘要保存在私有 `local/ipcexperiment/0001-confirmation-05-result.redacted.json`。
- 测试调用方及构建在受Git忽略的 `local/ipcexperiment/`，没有新增另一套载荷或发送实现。
- 本任务已占用并成功确认，停止发送，不重复运行或自动换标识重试。

本次补充了[正式Adapter重构记录](formal-adapter-refactor.md)的真实运行证据。适用范围仍是固定版本、唯一授权原生群和固定正文，不扩大任意文本、私聊或其他协议。
