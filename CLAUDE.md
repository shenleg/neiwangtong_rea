# 内网通独立消息网关

## 当前状态

业务 WM_COPYDATA 的原生群固定文本实验已成功，独立接收端确认正确群中新增一条、发送者与正文正确。当前工作是维护已验证的 Go 实现，不再从历史报告重新开始生产者搜索或替代方案验证。

- 实现：`internal/ipc`非交互式Adapter，提供 `LoadConfig`、`NewAdapter`、`Preflight`、`SendGroup`、`SendUser`、`RecordObservation`；命令入口保留 `cmd/ipcexperiment`，仍只处理群发送参数、确认与观察，默认只读。
- 客户端：正常授权身份登录的内网通 3.4.3055，Windows x86。
- 真实发送构建：Windows 386；amd64仅测试、构建和只读检查。
- 已确认成功的 `NWT-GW-AUTH-TEST-0001-confirmation-03`、`-04` 及正式Adapter直接调用的 `-05` 均已占用，重构与测试不产生新增发送授权。
- `SendUser(userID, attemptID)`显式接收原始用户ID，必须与配置的 `authorized_user_id` 完全相同，仅发送固定正文；已另经明确授权完成真实单聊并获独立接收确认，见 `docs/research/reports/ipc-phase12/user-confirmation-01.md`。`NWT-GW-AUTH-TEST-0001-user-confirmation-01`已占用；新增方法不产生后续发送授权，约束见 `docs/tools/ipc.md`。
- 项目长期目标仍是独立 Windows Go 网关与 HTTP 接口；当前未开放任意正文、名称/IP目标或 HTTP。

## 按任务读取

1. **维护发送实现、判断下一步或准备真实验证**：先读 `docs/research/ipc/validated-route.md`，遵循唯一已验证路线及停止事项。
2. **修改载荷、编码、群/用户解析或返回值**：读 `docs/research/ipc/ipc-contract.md`，保留地址/Evidence依据，区分静态事实、运行事实和未知范围。
3. **使用或重构模块**：读 `docs/tools/ipc.md`；复用 `internal/windowprobe`，保持原单次控制和测试。
4. **核对成功结果**：读 `docs/research/reports/ipc-phase12/0001-original-method-confirmation.md`。历史阶段的“未调用”“尚不可实现”仅描述当时状态，不覆盖后续成功证据。
5. **提取联系人ID或准备单聊**：使用 `docs/tools/contactid.md` 的只读工具，联系人来源仅为 `cache/db/nr` 的 `OrgStruct_*`，原样保留ID，结果人工核实后才配置发送。字段依据见 `docs/research/reports/user-id/user-id-report.md`，已验证codec见 `docs/research/reports/nr-codec/nr-codec-report.md`；已通过合成加密测试及授权真实副本只读验证，联系人原始名称为not_found；操作者另确认输入为本机名称后已从唯一身份表后缀取得本机ID，未改配置或发送，见联系人报告同目录 `readonly-validation.md`。不使用群成员表替代，不猜ID或解密参数。
6. 只有追查具体证据时才读对应小文件，不一次加载全部大型证据包或快照。

## 已验证路线

`独立Go386程序 → 正常授权客户端的唯一主窗口 → WM_COPYDATA → 原生group_id发送 → 独立接收观察`。

- 接收入口 `0x449c60`，业务 `dwData=0x0fecf10f`，字段起点 `0 / 0x64 / 0x12c`，UTF-8、NUL终止。[E22]
- 目标处理 `0x4c41b0`，查群 `0x4ee740 / 0x4edfc0`，原生群发送 `0x4b9c80 → 0x617600`。[E23、E25、E19]
- 固定正文 `NWT-GW-AUTH-TEST-0001` 的322字节载荷已实测；厂商完整结构、容量及任意文本上限仍未知，不猜补。
- 记录独占创建并落盘后只调用一次 `SendMessageTimeoutW`，不自动重试。API完成、进程存活和LRESULT均不等于送达；只有独立接收观察才记为 `recipient_observed`。
- 人工长串只属于实验命令，不是IPC协议或Adapter依赖。上层取得具体单次授权后可直接调用 `SendGroup(attemptID)`，再记录独立接收观察；不反复要求用户重录已保存的配置。模块不生成标识，命令发送必须显式提供 `-attempt-id`，不通过自动换值重试。

## 工作范围

- 复用已成功实现；不继续验证IPMsg替代原生群、UI Automation发送、自造原生网络协议或调试器作为发送前提。
- 不修改原始客户端，不注入、不提权、不绕过认证或消息过滤。
- 日常发送与开发不读取进程内存、聊天历史或凭据。历史限定调试仅是授权诊断，不带入模块依赖。
- 多窗口候选时拒绝发送，不按标题、面积、枚举顺序挑选；只有确认非主窗口并获得操作授权时才正常关闭它，再重新定位。
- 已占用或unknown任务保留记录，不删除、改名、切换APPDATA或隐式换AttemptID恢复资格。新的真实发送需要具体目标、正文与次数的明确授权。
- 真实群ID、客户端样本、私有构建与原始日志保持私有，不提交Git。`local/ipcexperiment/`继续受忽略，不因包迁移改变私有路径。
- 文档整理和保持行为的模块重构默认只测试、构建；测试使用合成数据和独立fixture，不向真实客户端发送。真实复验需要单独明确授权，已完成的04复验见路线文档。不自动commit/push。

## 验证

运行amd64与386的 `go test -count=1 -timeout 45s ./...` 和 `go vet ./...`。Go代码保留文件职责说明、导出文档注释及测试说明，修改限制在当前任务所需范围。
