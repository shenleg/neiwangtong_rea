# 内网通独立消息网关

已验证：独立 Go Windows 386 程序通过正常登录的内网通 3.4.3055 客户端，以业务 `WM_COPYDATA` 向唯一授权原生群发送固定文本，独立接收端确认新增一条、发送者和正文正确。

当前只维护这条已成功的 IPC 路线，不继续开发 IPMsg 替代原生群、UI 自动化发送或自造网络协议；不使用注入、提权或修改客户端。

## 从这里开始

- **维护技术路线或准备验证**：[已验证路线与停止事项](docs/research/ipc/validated-route.md)。
- **修改载荷、编码、目标解析或返回判定**：[IPC 契约与 Evidence](docs/research/ipc/ipc-contract.md)。
- **使用或维护模块**：[internal/ipc 说明](docs/tools/ipc.md)。
- **核对真实成功依据**：[原0001方式复验](docs/research/reports/ipc-phase12/0001-original-method-confirmation.md)与[正式Adapter非交互式复验05](docs/research/reports/ipc-phase12/formal-adapter-confirmation-05.md)。
- **维护窗口定位**：[windowprobe 说明](docs/tools/windowprobe.md)。
- **提取联系人ID或准备单聊**：[只读提取工具](docs/tools/contactid.md)，字段依据见[联系人研究](docs/research/reports/user-id/user-id-report.md)，加密读取依据见[codec验证](docs/research/reports/nr-codec/nr-codec-report.md)。

## 代码

- `internal/ipc`：非交互式正式Adapter，复用配置读取、窗口复核、固定载荷和Win32发送，返回结构化结果，支持单次记录及外部接收观察。
- `cmd/ipcexperiment`：薄实验命令，仅处理参数、确认与观察，调用Adapter；默认只读，实际发送必须显式提供 `-attempt-id`。
- `internal/windowprobe`、`cmd/windowprobe`：只读窗口采集与严格定位。
- `tools/contactid.py`：固定codec读取离线加密联系人副本，支持仅看schema和原始姓名精确匹配，不发送、不改配置；Windows合成加密测试及[授权真实副本读取](docs/research/reports/user-id/readonly-validation.md)已通过，联系人名称匹配为not_found；经操作者另确认是本机名称，已从唯一身份表后缀取得本机ID，未配置或发送。

Adapter已新增 `SendUser(userID, attemptID)`，原始用户ID由参数传入且必须与配置的 `authorized_user_id` 完全相同，仅发送原固定正文，沿用原生单聊路径；**另经明确授权已完成一次真实单聊，独立接收确认新增一条、发送者和正文正确，结果为 `recipient_observed`**，见[单聊验证01](docs/research/reports/ipc-phase12/user-confirmation-01.md)。原群发送行为保留，没有新增HTTP、任意文本或单聊命令开关。真实目标 ID 和本地构建在 Git 忽略的 `local/ipcexperiment/`；原始尝试记录在 APPDATA，均不提交。

## 测试

```bash
go test -count=1 -timeout 45s ./...
go vet ./...
GOARCH=386 go test -count=1 -timeout 45s ./...
GOARCH=386 go vet ./...
```

Windows 386 IPC fixture 只访问独立测试子进程，不向内网通发送。amd64 构建通过不代表 amd64 实际发送已验证。

**成功及unknown实验的旧标识继续占用。Adapter要求调用者明确提供请求标识，不自动生成新标识或重复执行旧任务。** 新的真实发送需要新的明确授权；API完成与 `LRESULT=1` 不代表送达，只有独立接收观察才记为 `recipient_observed`。
