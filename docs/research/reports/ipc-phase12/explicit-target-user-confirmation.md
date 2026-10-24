# 显式目标接口的单聊真实复验

本文记录用户指定 `PVE-WIN11` 后，对新版 `SendUser(userID, message)` 的一次真实验证。不包含原始用户 ID、聊天历史、凭据或完整私有日志。

## 结论

**新接口已成功发送：操作者在 PVE-WIN11 的独立接收端确认，正确单聊中本次新增恰好一条 `NWT-GW-AUTH-TEST-0001`，发送者和正文正确。随后调用 `RecordObservation`，返回 `recipient_observed`。**

本次实际发送调用只有一次，未重试。追加接收确认和最后的日志核对均未再次发送。

## 目标依据与窗口处理

- 用户明确确认，`PVE-WIN11` 就是[单聊验证01](user-confirmation-01.md)中成功接收消息的同一个用户。
- 复用该次已验证的原始用户 ID，并只读核对原始成功日志与本次私有调用程序中的 ID 完全相同；未猜 ID、按名称生成 ID 或使用本机身份 ID 替代联系人。
- 本次未重新读取加密联系人库或 codec 口令，没有读取进程内存、聊天或凭据，也未修改私有配置。
- 首次只读预检发现两个候选窗口并停止。用户处理窗口后仍有一个附属弹窗；只读窗口结构显示它由原主窗口拥有。用户自行处理弹窗后，重新预检得到唯一候选。
- 所有上述预检失败均发生在实际发送之前，没有创建本次 Adapter 发送记录。没有猜选窗口、关闭客户端或绕过多候选拒绝规则。

## 实际调用

Windows 386 私有程序直接使用当前 `internal/ipc` 源码构建，调用顺序为：

1. `NewAdapter(exePath)` 只绑定本机正常登录客户端，不传目标配置。
2. `Preflight()` 复核唯一窗口、样本哈希及同用户/完整性、session、desktop。
3. `SendUser(userID, ipc.Message)` 显式传入已核实原始用户 ID 和固定正文；Adapter 内部生成记录标识，调用一次。
4. 用户独立确认后，通过返回的 `AttemptID` 追加 `RecipientObservation / one`，没有再次调用发送。

| 项目 | 结果 |
|---|---|
| 发送结果输出时间（UTC） | `2026-10-23T17:59:10.9820376Z`，不是接收时间戳 |
| 构建 | Windows 386 |
| 目标名称 | `PVE-WIN11`，用户确认是此前成功接收的同一用户 |
| 正文 | `NWT-GW-AUTH-TEST-0001` |
| 客户端 PID / HWND | 2652 / `0x3030A` |
| 样本及安全上下文检查 | 通过 |
| 内部生成的 AttemptID | `NWT-GW-c02f02aab3fa326c8dfadd40715459c4` |
| AttemptReserved | true |
| Invoked / Completed | true / true |
| API 返回 / 错误码 | 1 / 0 |
| LRESULT / 有效性 | 1 / true |
| 耗时 / 超时 | 0 ms / no，耗时字段以整毫秒记录 |
| 调用后客户端存活 | true |
| SendUser 返回 Delivery | unknown |
| 独立接收确认 | 正确单聊本次新增恰好一条，发送者及正文正确 |
| RecordObservation 返回 Delivery | **recipient_observed** |
| 真实发送调用次数 | 1 |
| 追加接收观察时新增发送 | 0 |

之后只读核对原始日志，确认预约、实际调用结果和独立接收观察各一条，目标为 `user_id` 且不含群目标字段。API 完成、LRESULT 和进程存活没有被用作送达证据。

## 私有记录与结束

- 本次原始调用及观察日志保留在原 APPDATA 目录；历史群与用户记录均未删除或改写。
- 私有调用程序、386 构建及脱敏执行记录位于 Git 忽略的 `local/ipcexperiment/explicit-target-pve-win11-validation*`。调用前独占创建执行记录，防止误重跑后生成新任务重复发送。
- 群与单聊的新参数接口至此均已有一次独立接收确认，群聊见[显式目标群聊复验](explicit-target-group-confirmation.md)。
- 本次验证完成，停止发送；不扩展任意正文、其他接收者、名称/IP 选择器、HTTP 或生产部署。
