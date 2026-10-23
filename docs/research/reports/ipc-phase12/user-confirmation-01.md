# 正式 Adapter 单聊验证 01

本文记录 `SendUser(userID, attemptID)` 的一次独立接收验证；接口说明见[IPC Adapter](../../../tools/ipc.md)，静态路径见[IPC 契约第3节](../../ipc/ipc-contract.md#3-目标解析与正文处理)。真实用户ID、私有调用程序和完整日志不进入Git。

## 1. 授权与接口

- 操作者明确授权向已通过离线联系人原始名称唯一匹配取得ID的目标用户发送一次固定正文 `NWT-GW-AUTH-TEST-0001`。
- 请求标识：`NWT-GW-AUTH-TEST-0001-user-confirmation-01`。该标识已占用，不得再次使用。
- 本次调用 `adapter.SendUser(userID, attemptID)`；原始用户ID由参数传入，必须与构造时复制的 `Config.AuthorizedUserID` 完全相同。不按姓名/IP发送，不解析目标列表。
- 本次授权配置仅在内存构造，未修改原私有群配置。发送期间未读取数据库、口令、进程内存或聊天历史；接收事实由操作者提供。

## 2. 实施与调用结果

正式代码位于 `internal/ipc`，私有验证入口及Windows 386构建位于Git忽略的 `local/ipcexperiment/`。默认模式只读预检，发送与追加观察分开执行；没有重试或自动请求标识。

- 样本SHA-256匹配既有内网通3.4.3055客户端，定位到唯一窗口，同用户、完整性、session及desktop检查通过。
- 选择器 `user_id`，目标起点 `0x64`，正文起点 `0x12c`，UTF-8、NUL终止，载荷322字节；沿用现有 `WM_COPYDATA` 实现。
- 先独占创建并Sync单次日志，再最终复核客户端身份，仅调用一次 `SendMessageTimeoutW`。
- 私有验证构建SHA-256：`b8473a4563b2008e01fd969cd0883010de2b337b3014fdebabf0813adfb80b1e`。

| 项目 | 结果 |
|---|---|
| 调用结果记录时间（UTC） | `2026-10-23T16:35:16.41601Z` |
| `attempt_reserved / invoked / completed` | 均为true |
| API返回 / LastError / LRESULT | `1 / 0 / 1` |
| 耗时 / 超时 | `1 ms / no` |
| 调用后客户端存活 | true |
| SendUser直接返回的Delivery | `unknown` |
| 实际调用次数 | 1 |

API完成和LRESULT不作为送达证据。

## 3. 独立接收确认

操作者在目标用户的独立接收端明确确认：正确单聊中本次新增且仅新增一条，发送者和固定正文均正确。

随后只调用 `RecordObservation(attemptID, Observation{RecipientObservation, "one"})`，没有再发送消息。记录时间为 `2026-10-23T16:36:04.894352Z`，返回 **`recipient_observed`**。

最后只读核对原始日志：`attempt_reserved`、`call_result`、`recipient_observation` 各1条；用户目标及正文与授权一致，无群目标字段。未删除或改写历史事件。

## 4. 测试与验证范围

发送前，Windows amd64和386的以下命令均通过：

- `go test -count=1 -timeout 45s ./...`
- `go vet ./...`
- `go build ./...`

合成测试覆盖用户ID参数原样匹配配置、错误目标不占用日志或调用发送、UTF-8原值、固定载荷、群/用户共享单次标识、超时不重试和观察目标核对。跨进程fixture只使用独立测试子进程窗口。

本次确认了一个已核实用户ID的固定正文单聊送达，不扩展为任意正文、名称/IP发送、多目标、离线补发、跨位数发送、HTTP或生产部署。本次授权已使用，后续真实发送仍需另行明确授权。
