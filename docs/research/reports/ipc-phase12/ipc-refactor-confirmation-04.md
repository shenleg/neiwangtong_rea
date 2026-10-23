# internal/ipc 迁移后的真实发送复验

本文记录模块迁移完成后、用户另行明确要求再发送一条消息的单次复验；不包含真实群 ID、成员身份、聊天历史或凭据。

## 结论

**重构后的 `internal/ipc` 已通过真实端到端复验：向原配置的同一授权原生群发送一条 `NWT-GW-AUTH-TEST-0001`，独立接收端明确确认本次新增一条、发送者和群正确。最终结果为 `recipient_observed`。**

## 本次范围

- 用户明确要求“再发一条消息验证下”，沿用已明确选择的助手直接执行方式，不重复要求输入长串。
- 仍调用 `cmd/ipcexperiment → ipc.Run → sendOnce`，载荷、Win32实现、窗口与身份复核不变。
- 只将固定 AttemptID 从已占用的03改为新的 `NWT-GW-AUTH-TEST-0001-confirmation-04`，同步固定标识测试；不更换正文、群、APPDATA或旧任务记录。
- 先完成测试、构建和只读预检，然后向现有程序提供本次完整确认行，只调用一次。
- 没有使用调试器、注入、提权或自动重试，没有关闭额外窗口。

## 实际结果

| 项目 | 结果 |
|---|---|
| 调用时间 | `2026-10-23T07:14:23.1251137Z` |
| 模块 / 构建 | `internal/ipc` / Windows 386 |
| AttemptID | `NWT-GW-AUTH-TEST-0001-confirmation-04` |
| 正文 | `NWT-GW-AUTH-TEST-0001` |
| 授权群及正文与配置匹配 | true / true，不输出真实 ID |
| cbData | 322 |
| invoked / completed | true / true |
| API返回 / 错误码 | 1 / 0 |
| LRESULT / 有效性 | 1 / true |
| 超时 / 耗时 | no / 1ms |
| 调用后进程存活 | true |
| 旧记录 | 均保持原字节，不覆盖 |
| 独立接收观察 | 用户明确选择“收到一条，发送者正确”，指本次新增消息，不是旧记录 |
| 观察补记时间 | `2026-10-23T07:16:05.559007Z`，非收件端自动时间戳 |
| 最终状态 | **recipient_observed** |

发送构建 SHA-256：`7d6907479d090dd47915c5392805697d36d43f100a390ea8c7d07aef86e9c497`。

原程序内未自动采集UI观察，因此输入unknown后按原逻辑以退出码1停止；随后独立接收确认另行补记，未改写原始事件。该退出码不代表本次发送失败，送达依据是独立接收观察而不是LRESULT。

私有记录：

- `%APPDATA%\neiwangtong-gateway\ipc-experiment\NWT-GW-AUTH-TEST-0001-confirmation-04.jsonl`。
- `local/ipcexperiment/0001-confirmation-04-result.redacted.json`。
- `local/ipcexperiment/0001-confirmation-04-recipient-observation.redacted.json`。

## 验证与结束

Windows amd64、386 的全部 `go test -count=1 -timeout 45s ./...`、`go vet ./...` 与 `git diff --check` 均通过。常规IPC测试只使用独立合成fixture；本报告中的实际调用来自另行授权的单次运行，不与合成测试混淆。

该结果补充了[原方式成功报告](0001-original-method-confirmation.md)，确认包迁移后同一固定用例仍可送达；不扩大任意文本、私聊或其他协议范围。本次任务已占用，停止发送，不重跑或删除日志恢复资格。
