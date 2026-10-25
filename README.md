# 内网通独立消息网关

已验证：独立 Go Windows 386 程序通过正常登录的内网通 3.4.3055 客户端，以业务 `WM_COPYDATA` 向唯一授权原生群发送固定文本，独立接收端确认新增一条、发送者和正文正确。

现有网关继续使用这条已成功的 IPC 路线，不用 IPMsg 或 UI 自动化代替原生群；不使用注入、提权或修改客户端。另行开展的[原生网络协议实现](docs/research/reports/native-protocol/protocol-spec.md)已通过单对端真实上线验收：Linux Go 新节点在 Windows 原客户端显示在线，用户已确认，随后正常退出，见[验收记录](docs/research/reports/native-protocol/native-node-online-confirmation-20261025.md)。1004/3011业务发送尚未实现，不替换现有IPC发送模块。

## 从这里开始

- **维护技术路线或准备验证**：[已验证路线与停止事项](docs/research/ipc/validated-route.md)。
- **修改载荷、编码、目标解析或返回判定**：[IPC 契约与 Evidence](docs/research/ipc/ipc-contract.md)。
- **使用或维护模块**：[internal/ipc 说明](docs/tools/ipc.md)。
- **核对真实成功依据**：[原0001方式复验](docs/research/reports/ipc-phase12/0001-original-method-confirmation.md)与[正式Adapter非交互式复验05](docs/research/reports/ipc-phase12/formal-adapter-confirmation-05.md)。
- **维护窗口定位**：[windowprobe 说明](docs/tools/windowprobe.md)。
- **研究原生网络协议**：[静态规格、Evidence 与未完成项](docs/research/reports/native-protocol/protocol-spec.md)，[nwtprotocol 离线 PoC](docs/tools/nwtprotocol.md)；不连接真实端点。
- **提取联系人ID或准备单聊**：[只读提取工具](docs/tools/contactid.md)，字段依据见[联系人研究](docs/research/reports/user-id/user-id-report.md)，加密读取依据见[codec验证](docs/research/reports/nr-codec/nr-codec-report.md)。

## 代码

- `internal/ipc`：非交互式正式 Adapter，`NewAdapter(exePath)` 只绑定正常登录的唯一本机客户端；`SendGroup(groupID, message)` 和 `SendUser(userID, message)` 显式接收目标与正文，返回 `SendResult, error`，支持单次记录及外部接收观察。
- `cmd/ipcexperiment`：薄实验命令，仅处理参数、确认与观察；默认只读，群实验须显式 `-send` 并确认群目标与固定正文。`Config / LoadConfig` 保留供 CLI 读取目标，不是 Adapter 授权配置；已去除 `-attempt-id`，使用发送返回的自动标识追加观察。
- `internal/windowprobe`、`cmd/windowprobe`：只读窗口采集与严格定位。
- `internal/nativeproto`：节点/ENet/业务帧、分片、XML、新身份、单对端 UDP 和限时应用上线；`cmd/nwtprotocol` 只开放离线模式；`cmd/nwtnode` 默认只校验，显式 `-run` 才执行已授权上线，见[说明](docs/tools/nwtnode.md)。
- `tools/contactid.py`：固定codec读取离线加密联系人副本，支持仅看schema和原始姓名精确匹配，不发送、不改配置；Windows合成加密测试及[授权真实副本读取](docs/research/reports/user-id/readonly-validation.md)已通过，联系人名称匹配为not_found；经操作者另确认是本机名称，已从唯一身份表后缀取得本机ID，未配置或发送。

Adapter 不保存配置目标，不做授权目标白名单或 `Config` 比较；默认调用方有权向指定目标发送，Adapter 不另设权限检查，合法目标与正文调用即尝试发送。两个发送方法支持非空、有效 UTF-8、无 NUL 的自定义正文；不再限定为 `ipc.Message` 或统一 512 字节，而是按[正文内容预算](docs/research/reports/ipc-message-content/refined-body-bound.md)计算序列化保守上界，与共同预算 261950 个 UTF-16 code units 比较，超出时在发送前明确拒绝，不截断、不拆分、不重试。这不是厂商最大消息长度。**已知问题：另行授权的261003字真实验证引发客户端卡顿/退出及用户报告的持续重试，用户自行重装恢复；当前尚未另加产品级限额，不能把静态预算当作可用长度。** 详见[异常与恢复记录](docs/research/reports/ipc-phase12/custom-max-body-abnormal.md)。

实际 LF、CRLF、CR 原样保留；字面四字符 `\r\n` 转为实际 CRLF，字面两字符 `\n` 转为实际 LF，长串优先、单次匹配替换。不解释其他转义，也不提供双反斜杠保护，因此这些字面序列不能原样发送，路径或代码正文的调用者须注意。预算、载荷和日志 `text` 均使用转换后的正文，详见[模块说明](docs/tools/ipc.md#显式目标与自定义正文)。`ipc.Message` 保留为历史默认及 CLI 固定测试正文；CLI 本轮不新增正文参数、HTTP 或单聊开关。

**单聊已另经明确授权完成一次真实验证，独立接收确认新增一条、发送者和正文正确，结果为 `recipient_observed`**，见[单聊验证01](docs/research/reports/ipc-phase12/user-confirmation-01.md)。该报告使用当时需配置绑定与显式 attemptID 的旧接口，不改写为当前接口的真实复验。真实目标 ID 和本地构建仍在 Git 忽略的 `local/ipcexperiment/`；原始尝试记录在 APPDATA，均不提交。

## 测试

```bash
go test -count=1 -timeout 45s ./...
go vet ./...
GOARCH=386 go test -count=1 -timeout 45s ./...
GOARCH=386 go vet ./...
```

Windows 386 IPC fixture 只访问独立测试子进程，不向内网通发送。amd64 构建通过不代表 amd64 实际发送已验证。

**Adapter 内部用 `crypto/rand` 为每次有效发送调用生成 `NWT-GW-<随机十六进制>` 标识；每次调用是新任务，不自动重试，日志碰撞直接报错，不换 ID 再试。随机 ID 不提供调用方重试幂等性。** 用 `SendResult.AttemptID` 追加观察，重建 Adapter 后也可依据已有日志继续记录。日志目录不变，成功及 unknown 实验的全部历史记录保留，不重复执行旧任务。接口调整阶段仅使用合成 fixture；随后另经用户授权，已通过[新版群接口](docs/research/reports/ipc-phase12/explicit-target-group-confirmation.md)向 `qun` 发送一条固定正文，并通过[新版单聊接口](docs/research/reports/ipc-phase12/explicit-target-user-confirmation.md)向用户确认的原接收者发送一条固定正文，两次均获独立接收确认。自定义正文实现经离线测试后，另获授权完成[三行消息真实验证](docs/research/reports/ipc-phase12/custom-message-newlines-confirmation.md)：`qun` 群字面 `\n` 转 LF、`PVE-WIN11` 私聊字面 `\r\n` 转 CRLF，各调用一次，独立接收确认条数、发送者、三行正文和 emoji 正确，均记为 `recipient_observed`。随后另经授权完成[500汉字五段真实验证](docs/research/reports/ipc-phase12/custom-500-newlines-confirmation.md)：群聊LF和私聊CRLF各新增一条，全文、发送者和显式换行均正确，均为 `recipient_observed`。更长正文及其他显示边界仍未真实验证。开发期间操作真实客户端复验须另行明确授权，这不是现行发送接口每次调用的额外要求。API 完成与 `LRESULT=1` 不代表送达，只有独立接收观察才记为 `recipient_observed`。
