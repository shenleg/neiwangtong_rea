# 显式目标接口的群聊真实复验

本文记录用户指定群名称 `qun` 后，对新版 `SendGroup(groupID, message)` 的一次真实验证。不包含真实群 ID、身份表后缀或原始私有日志。

## 结论

**新接口已成功发送：独立接收端确认 `qun` 群按本次时间新增一条 `NWT-GW-AUTH-TEST-0001`，发送者和正文正确。随后调用 `RecordObservation` 追加接收确认，返回 `recipient_observed`。**

本次真实发送调用只有一次，没有重试；记录接收确认和随后只读检查均未再次发送。不把 API 返回或客户端存活当作送达证据。

## 目标核对与预检

- 复用[阶段十](../ipc-phase10/ipc-phase10-report.md)已确认的群元数据一致性副本，只读精确查询名称 `qun`；匹配行数为 1，原始 QGroupId 与保存的私有配置完全一致。
- 当前群元数据文件与该已验证副本的 SHA-256 相同，未发现 WAL 或 journal；没有对运行中的数据库执行 SQL，没有读取聊天、进程内存或凭据。
- Windows 386 程序直接使用当前 `internal/ipc` 源码构建。`NewAdapter(exePath)` 只绑定客户端，目标由调用方读取后作为参数传给 `SendGroup`，不向构造函数传配置。
- 只读预检通过：唯一客户端 PID 2652，HWND `0x3030A`，样本 SHA-256 匹配，同用户及完整性、session、desktop 检查通过。预检未调用发送。

## 调用结果

实际发送方法为 `adapter.SendGroup(groupID, ipc.Message)`，没有要求调用方提供 attemptID，也没有控制台确认输入。

| 项目 | 结果 |
|---|---|
| 发送结果输出时间（UTC） | `2026-10-23T17:40:59.1487481Z`，不是接收时间戳 |
| 构建 | Windows 386 |
| 目标名称 | `qun`，内部使用已核实的原生群 ID |
| 正文 | `NWT-GW-AUTH-TEST-0001` |
| 内部生成的 AttemptID | `NWT-GW-ba22decd045e205abe4a67a969d78b62` |
| AttemptReserved | true |
| Invoked / Completed | true / true |
| API 返回 / 错误码 | 1 / 0 |
| LRESULT / 有效性 | 1 / true |
| 耗时 / 超时 | 1 ms / no |
| 调用后客户端存活 | true |
| SendGroup 返回 Delivery | unknown |
| 独立接收方确认 | 正确群按本次时间新增一条，发送者、正文正确 |
| RecordObservation 返回 Delivery | **recipient_observed** |
| 真实发送调用次数 | 1 |
| 追加接收观察时新增发送 | 0 |

用户明确选择“收到一条，发送者正确”后，使用上述返回标识调用 `RecordObservation(attemptID, Observation{Kind: RecipientObservation, State: "one"})`。之后只读核对持久化日志，确认一条预约、一条实际调用结果和一条独立接收观察。

## 私有记录与结束

- 原始日志保留在原 APPDATA 目录，未删除、改名或修改旧任务。
- 验证程序、386 构建和脱敏执行记录位于 Git 忽略的 `local/ipcexperiment/explicit-target-qun-validation*`。验证程序在调用前独占创建执行记录，防止误重跑后生成新标识再次发送。
- 未修改私有目标配置、客户端或固定载荷；未新增 HTTP 或任意正文能力。
- 本次群聊验证完成后停止发送。当时新参数形式的 `SendUser` 尚未真实复验；随后用户另行授权完成了[显式目标单聊复验](explicit-target-user-confirmation.md)，历史单聊成功证据仍见[单聊验证01](user-confirmation-01.md)。
