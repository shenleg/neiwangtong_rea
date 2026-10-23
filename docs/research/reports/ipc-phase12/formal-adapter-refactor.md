# 正式 IPC Adapter 重构记录

本文记录已验证发送实现从交互实验程序整理为可复用Adapter的范围与验证，不包含真实群ID、私人日志内容或凭据。本轮未发送真实消息，也未读取实际本地授权配置或APPDATA实验记录。

## 完成的分离

| 能力 | 当前归属 |
|---|---|
| 单群配置读取与严格校验 | `internal/ipc/config.go` 的 `LoadConfig`，不再位于cmd目录 |
| 实例绑定与窗口复核 | `NewAdapter / Preflight`，内部复用原 `checkClient` 和 `windowprobe.Resolve` |
| 固定载荷与Win32发送 | 原 `buildPayload / sendOnce`，实现未变 |
| 单次日志与调用错误 | `Adapter.SendGroup(attemptID)`，非交互式结构化返回 |
| 外部接收观察持久化 | `Adapter.RecordObservation`，只追加匹配的既有实际调用记录 |
| 参数、人工确认与观察提示 | `cmd/ipcexperiment`，通过正式Adapter执行，不再构造载荷或调用Win32 |

旧的 `ipc.Run(..., io.Reader, io.Writer)` 已移除；交互只存在于命令程序。没有保留旧包或旧接口的转发层。正式Adapter不依赖stdin、stdout、bufio或输入流。

## Interface

```go
config, err := ipc.LoadConfig(configPath)
adapter, err := ipc.NewAdapter(exePath, config)
preflight, err := adapter.Preflight()
result, err := adapter.SendGroup(authorizedAttemptID)
delivery, err := adapter.RecordObservation(authorizedAttemptID, observation)
```

以上仅展示接口形态，实际错误处理与调用规则见[模块说明](../../../tools/ipc.md)。`SendResult`在失败时仍保留占用和调用事实；`Delivery`始终Unknown，接收成功须由独立接收观察提供。不等待人工观察、不自动重试、不生成请求ID，不开放新正文或私聊。

固定模块级AttemptID已移除。标识由上层在取得具体授权后明确提供，命令发送必须显式 `-attempt-id`；日志仍在原APPDATA目录，文件名仍为标识加`.jsonl`，已有首次、03、04和unknown记录继续阻止同名发送。新请求标识不等于新的发送授权。

## 保持不变的行为

- 固定Message、三个偏移、UTF-8/NUL、322字节成功载荷保持原样。
- `platform_windows.go`、`platform_other.go`、`platform_windows_test.go`、`result.go`、`windowprobe/resolve.go` 与重构前文件摘要完全一致。
- 按唯一窗口、样本、安全上下文和原绑定实例复核；出现歧义或身份变化即失败，不自动换目标或关闭窗口。
- 只允许Windows 386真实发送，日志独占创建、Sync之后才调用一次；超时、日志或最终检查失败保留标识。
- API完成不等于送达；独立接收one才产生RecipientObserved。日志损坏、群不符或未实际调用时不能追加送达证据。

## 测试迁移与新增

原IPC及命令的24个测试函数名全部保留，确认交互测试移到命令层，单次/超时/观察测试改为经过Adapter公开方法验证。新增用例包含：

- 共享配置原样读取、重复键/未知字段/类型/尾随内容拒绝及错误脱敏。
- 构造时配置校验、配置复制后不受外部修改影响、绑定实例变化不切换。
- 发送无需输入流、结构化结果不含目标、API成功仍Unknown、错误不丢失调用事实。
- 显式标识、路径和设备名拒绝、平台不支持时不占用记录。
- 同Adapter或不同Adapter/重建实例的重复任务、并发争用及历史日志保护。
- 外部观察只追加、发送方one不等于接收、异常观察停止、无实际调用/错误群/损坏日志拒绝。
- 命令只读无副作用、完整确认、显式标识传递、模块错误立即停止及观察输入中断不重发。

amd64、386的完整 `go test -count=1 -timeout 45s ./...` 和 `go vet ./...` 已通过，386跨进程与超时fixture仍只访问独立合成测试进程。Linux交叉构建和测试编译通过，未在Windows执行Linux测试。

## 使用范围

重构阶段只进行代码与Interface的正式化，不为此新增测试消息。随后用户另行明确授权真实复验，[正式Adapter复验05](formal-adapter-confirmation-05.md)已通过直接调用SendGroup发送并由独立接收端确认，再调用RecordObservation得到RecipientObserved；补充了[原方式03](0001-original-method-confirmation.md)和[迁移后04](ipc-refactor-confirmation-04.md)的证据。下一阶段若接入HTTP，直接使用Adapter并在上层完成调用权限、具体请求授权和接收证据处理，不再通过控制台脚本驱动发送。
