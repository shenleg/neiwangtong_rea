# 自定义正文与换行的群聊、私聊真实验证

本文记录 2026-10-24 在用户明确授权后进行的两次独立 Adapter 调用，以及随后由独立接收端提供的确认。不包含真实目标 ID、聊天历史、凭据或原始私有日志。

## 1. 结论

**群聊和私聊均已成功接收自定义三行正文：各新增恰好一条，发送者、中文、英文、数字、emoji 和三行显示全部正确。** 用户分别确认后，各通过 `RecordObservation` 追加 `RecipientObservation / one`，结果均为 `recipient_observed`。

- 群 `qun`：调用参数中的字面两字符 `\n` 转为实际 LF，按三行接收。
- 私聊 `PVE-WIN11`：调用参数中的字面四字符 `\r\n` 转为实际 CRLF，按三行接收。
- 实际发送共两次，每个目标一次；观察记录和结束后的只读核对均没有再次发送。
- API 完成、LRESULT 和客户端存活只记录为调用事实，成功结论来自操作者分别在独立接收端的明确确认。

本次不证明 500 字或更长正文已经真实送达；长正文和容量边界仍只有此前的离线测试与静态上界依据。不扩展为任意特殊字符、单独 CR 显示、离线补发或全群送达的运行证明。

## 2. 本次授权与目标核对

操作者先明确选择向 `qun` 群和 `PVE-WIN11` 各发送一次下述完整正文，共两条，失败或超时不重试。

- 群目标来自原私有配置，并与[新版群接口真实成功记录](explicit-target-group-confirmation.md)中的原始 ID 只读比较，一致。
- 用户目标来自[新版私聊真实成功记录](explicit-target-user-confirmation.md)，保留原始 ID，不按名称重新解析，不使用本机身份 ID 替代。
- 只读核对历史记录的唯一预约、固定正文、实际调用和独立接收 `one`，然后核对当前群配置。
- 当前 Windows 386 构建通过 `NewAdapter / Preflight` 定位唯一客户端；PID 2652，HWND `0x3030A`，样本哈希、同用户/完整性、session 和 desktop 检查通过。

群发送完成后，私聊首次执行被权限检查拦截，未执行程序、未创建本次私聊执行记录。随后只读比较历史成功日志的用户 ID 与此前成功验证程序中的完整常量，确认相同；操作者再次明确确认该接收者就是 `PVE-WIN11` 并授权继续尚未发送的私聊一次，之后才实际调用。拦截没有被计为发送，也没有更换工具规避检查。

没有读取加密联系人库、聊天、进程内存或凭据，没有修改真实配置或客户端。

## 3. 完整正文

### 群聊

传给 `SendGroup` 的参数使用字面换行标记：

```text
NWT-GW-CUSTOM-GROUP-01\n自定义群聊测试：中文 English 123 😀\n换行测试：这是第三行
```

独立接收方确认显示为：

```text
NWT-GW-CUSTOM-GROUP-01
自定义群聊测试：中文 English 123 😀
换行测试：这是第三行
```

### 私聊

传给 `SendUser` 的参数使用字面 CRLF 标记：

```text
NWT-GW-CUSTOM-USER-01\r\n自定义私聊测试：中文 English 123 😀\r\n换行测试：这是第三行
```

独立接收方确认显示为：

```text
NWT-GW-CUSTOM-USER-01
自定义私聊测试：中文 English 123 😀
换行测试：这是第三行
```

## 4. 调用与观察结果

| 项目 | 群聊 | 私聊 |
|---|---|---|
| 目标名称 | `qun` | `PVE-WIN11` |
| 发送结果输出时间 UTC，不是接收时间 | `2026-10-24T08:37:24.370399Z` | `2026-10-24T08:42:07.5027941Z` |
| AttemptID | `NWT-GW-5e2908b2a87c4d37deed878990a1d58d` | `NWT-GW-0f506ab1a42781fa0a29e1dda9d5efbd` |
| AttemptReserved | true | true |
| Invoked / Completed | true / true | true / true |
| API 返回 / 错误码 | 1 / 0 | 1 / 0 |
| LRESULT / 有效性 | 1 / true | 1 / true |
| 耗时 / 超时 | 4 ms / no | 4 ms / no |
| 调用后客户端存活 | true | true |
| 转换后正文 UTF-8 字节数 | 101 | 102 |
| 转换后正文 UTF-16 units | 59 | 60 |
| 实际 cbData | 402 | 403 |
| 正文实际 LF / CR 数量 | 2 / 0 | 2 / 2 |
| 正文行数 | 3 | 3 |
| SendGroup / SendUser 返回 Delivery | unknown | unknown |
| 独立接收确认 | 一条，发送者、正文、emoji、三行均正确 | 一条，发送者、正文、emoji、三行均正确 |
| RecordObservation 返回 Delivery | **recipient_observed** | **recipient_observed** |
| 实际发送调用次数 | 1 | 1 |
| 追加观察时新增发送 | 0 | 0 |

结束后只读解析各自 APPDATA 日志，分别确认一条预约、一条实际调用、一条独立接收观察。保存的正文与本节预期完全一致，且已无字面换行标记；长度均满足 `cbData=0x12c+UTF8正文长度+1`。这项字节核对不依赖读取客户端聊天。

## 5. 构建和私有记录

- 构建为 Windows 386，直接使用当前 `feature/ipc-custom-messages` 工作区的正式 `internal/ipc` 源码。
- 验证程序及构建位于 Git 忽略的 `local/ipcexperiment/custom-message-newlines-validation*`。
- 本次 386 验证 EXE 的 SHA-256：`9800945fcb96cab515534afc3f0a1dfcd138926376439ddb6a1fb83bec40a9c3`。
- 每个目标使用独立且独占创建的本地执行记录，Sync 后才调用，防止误重跑时生成新标识再次发送；本次记录保留，不删除或改名。
- 正式调用和观察仍保存在原 APPDATA 目录，所有历史记录保留。原始 ID、私有构建和完整日志不提交 Git。
- 验证完成后停止发送；没有增加 CLI 任意正文参数、HTTP 或新目标，本次没有自动 commit/push。
