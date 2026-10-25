# 内网通独立消息网关

## 当前状态

**超长正文异常未修复**：另经授权的261003字群聊/私聊各一次调用后，用户报告卡顿、退出及持续重试；用户自行重装后恢复，见 `docs/research/reports/ipc-phase12/custom-max-body-abnormal.md`。该值仅是静态序列化边界，不能作为产品可用长度；当前代码尚未另加产品限额。500汉字五段已真实成功，2000字测试尚未执行。后续真实发送必须重新取得具体目标、完整正文和次数授权。

业务 WM_COPYDATA 的原生群固定文本实验已成功，独立接收端确认正确群中新增一条、发送者与正文正确。生产发送继续维护已验证的 Go 实现，不从历史报告重新开始生产者搜索。

**独立网络上线已验收**：2026-10-25，Linux Go 新节点完成真实单对端上线，用户在 Windows 原客户端确认 `NWT-Go-Linux` 在线；随后程序正常退出。原生网络模块尚未实现1004/3011业务发送，不替换现有IPC。依据见 `docs/research/reports/native-protocol/native-node-online-confirmation-20261025.md`；上线成功不延续真实连接或聊天发送授权。

- 实现：`internal/ipc`非交互式Adapter，提供 `LoadConfig`、`NewAdapter`、`Preflight`、`SendGroup`、`SendUser`、`RecordObservation`；命令入口保留 `cmd/ipcexperiment`，仍只处理群发送参数、确认与观察，默认只读。
- 客户端：正常授权身份登录的内网通 3.4.3055，Windows x86。
- 真实发送构建：Windows 386；amd64仅测试、构建和只读检查。
- 已确认成功的 `NWT-GW-AUTH-TEST-0001-confirmation-03`、`-04` 及正式Adapter直接调用的 `-05` 均已占用，重构与测试不产生新增发送授权。
- `NewAdapter(exePath)`只绑定本机客户端；`SendGroup(groupID, message)`、`SendUser(userID, message)`使用调用参数目标，不检查目标授权名单，也不要求目标配置。`message` 接受合法自定义UTF-8正文，兼容字面 `\n` 和 `\r\n` 换行，按转换后的正文检查序列化预算，不采用512字节限制；规则及上界依据见 `docs/tools/ipc.md`。自定义三行正文已另经授权完成群聊LF和私聊CRLF各一次独立接收确认，见 `docs/research/reports/ipc-phase12/custom-message-newlines-confirmation.md`；随后500汉字五段也已完成群聊LF和私聊CRLF各一次独立接收确认，见 `docs/research/reports/ipc-phase12/custom-500-newlines-confirmation.md`；更长正文及其他边界未真实验证。发送记录标识内部生成，通过 `SendResult.AttemptID` 返回；每次调用是新请求，不自动重试。详见 `docs/tools/ipc.md`。
- 群与单聊固定正文均已另经明确授权获独立接收确认，单聊见 `docs/research/reports/ipc-phase12/user-confirmation-01.md`。该次 `NWT-GW-AUTH-TEST-0001-user-confirmation-01` 和其他历史记录继续保留。接口修改后已另经授权通过新版 `SendGroup(groupID, ipc.Message)` 向 `qun` 发送一次，独立接收确认后记录为 `recipient_observed`，见 `docs/research/reports/ipc-phase12/explicit-target-group-confirmation.md`；新版 `SendUser(userID, ipc.Message)` 随后也获授权完成一次单聊并得到独立接收确认，见 `docs/research/reports/ipc-phase12/explicit-target-user-confirmation.md`。
- 项目长期目标仍是独立 Windows Go 网关与 HTTP 接口；Adapter已提供有容量校验的自定义正文，当前未开放名称/IP目标或 HTTP。实验CLI仍只发送固定 `ipc.Message`。

## 按任务读取

1. **维护IPC发送或准备IPC真实验证**：先读 `docs/research/ipc/validated-route.md`，遵循已验证发送路线及停止事项。
2. **修改载荷、编码、群/用户解析或返回值**：读 `docs/research/ipc/ipc-contract.md`，保留地址/Evidence依据，区分静态事实、运行事实和未知范围。
3. **使用或重构模块**：读 `docs/tools/ipc.md`；复用 `internal/windowprobe`，保持每次调用最多一次Win32发送、不自动重试及相关测试。
4. **核对成功结果**：读 `docs/research/reports/ipc-phase12/0001-original-method-confirmation.md`。历史阶段的“未调用”“尚不可实现”仅描述当时状态，不覆盖后续成功证据。
5. **提取联系人ID或准备单聊**：使用 `docs/tools/contactid.md` 的只读工具，联系人来源仅为 `cache/db/nr` 的 `OrgStruct_*`，原样保留ID，结果人工核实后才配置发送。字段依据见 `docs/research/reports/user-id/user-id-report.md`，已验证codec见 `docs/research/reports/nr-codec/nr-codec-report.md`；已通过合成加密测试及授权真实副本只读验证，联系人原始名称为not_found；操作者另确认输入为本机名称后已从唯一身份表后缀取得本机ID，未改配置或发送，见联系人报告同目录 `readonly-validation.md`。不使用群成员表替代，不猜ID或解密参数。
6. **维护原生网络上线或继续实现发送**：先读 `docs/tools/nwtnode.md`、`docs/research/reports/native-protocol/native-node-online-confirmation-20261025.md`；字段修改读同目录 `node-registration-contract.md`，传输修改读 `docs/tools/native-transport.md`。复用已验收的新身份/type4/1000/1018，不从“尚未上线”重新开始。
7. 只有追查具体证据时才读对应小文件，不一次加载全部大型证据包或快照。

## 已验证路线

`独立Go386程序 → 正常授权客户端的唯一主窗口 → WM_COPYDATA → 原生group_id发送 → 独立接收观察`。

- 接收入口 `0x449c60`，业务 `dwData=0x0fecf10f`，字段起点 `0 / 0x64 / 0x12c`，UTF-8、NUL终止。[E22]
- 目标处理 `0x4c41b0`，查群 `0x4ee740 / 0x4edfc0`，原生群发送 `0x4b9c80 → 0x617600`。[E23、E25、E19]
- 固定正文 `NWT-GW-AUTH-TEST-0001` 的322字节载荷已实测。自定义正文使用按内容计算的静态序列化上界，依据见 `docs/research/reports/ipc-message-content/refined-body-bound.md`；厂商完整结构及实际最大正文仍未知，不盲试边界。
- 记录独占创建并落盘后只调用一次 `SendMessageTimeoutW`，不自动重试。API完成、进程存活和LRESULT均不等于送达；只有独立接收观察才记为 `recipient_observed`。
- 人工长串只属于实验命令，不是IPC协议或Adapter依赖。调用方直接提供目标ID和正文；内部生成标识并独占创建记录，失败或碰撞立即返回，不换标识重试。命令不再接收 `-attempt-id`，仍默认只读、显式 `-send` 并人工确认；独立接收观察使用返回的记录标识。

## 工作范围

- 生产发送复用已成功IPC，不用IPMsg或UI Automation替代原生群，也不把调试器作为发送前提。另行授权的原生网络实现按独立模块维护；默认运行合成/loopback测试，真实上线另须明确对端、身份和时长，聊天另须目标、完整正文与次数。
- 不修改原始客户端，不注入、不提权、不绕过认证或消息过滤。
- 日常发送与开发不读取进程内存、聊天历史或凭据。历史限定调试仅是授权诊断，不带入模块依赖。
- 多窗口候选时拒绝发送，不按标题、面积、枚举顺序挑选；只有确认非主窗口并获得操作授权时才正常关闭它，再重新定位。
- 已占用或unknown任务保留记录，不删除、改名、切换APPDATA或换标识自动重试。Adapter默认调用方有权向参数目标发送，不另设权限检查；开发期间操作真实客户端复验仍须另行获得具体目标、正文与次数的明确授权。
- 真实群ID、客户端样本、私有构建与原始日志保持私有，不提交Git。`local/ipcexperiment/`继续受忽略；`local/nativeclient/`保存独立节点自己的身份、逐次上线及UI观察记录，同样受忽略。失败和unknown记录保留，不覆盖成成功。
- 文档整理和保持行为的模块重构默认只测试、构建；测试使用合成数据和独立fixture，不向真实客户端发送。真实复验需要单独明确授权，已完成的04复验见路线文档。不自动commit/push。

## 验证

运行amd64与386的 `go test -count=1 -timeout 45s ./...` 和 `go vet ./...`。Go代码保留文件职责说明、导出文档注释及测试说明，修改限制在当前任务所需范围。
