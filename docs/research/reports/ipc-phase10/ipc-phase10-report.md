# 阶段十：Windows 只读提取授权测试群 QGroupId

本文记录实际数据库选择、退出后的私有快照、身份表确认及精确名称查询结果；不包含真实群 ID、身份表后缀、聊天内容或凭据。

## 1. 结论

**已成功取得授权原生测试群 `qun` 的真实 QGroupId。精确匹配行数为 1，不同 QGroupId 数量为 1，原始值为 32 个十六进制字符；原样保存在仓库外的私有目录。**

**后续续作已完成本地配置绑定、Windows amd64/386 单元测试与构建，以及客户端重启后的 Windows 386 完整只读预检。** 已将源码常量绑定改为显式 `-config` 单群配置；真实 ID 仅写入受 Git 忽略和 Windows ACL 保护的 `local/ipcexperiment/config.json`。默认模式仍不发送消息。

已具备进入另行授权的首次受控发送流程的技术准备；本轮没有实际发送授权，发送前还须确认当前群发送权限和独立授权接收方就绪。本轮仅运行不含 `-send` 的实验程序，没有调用内网通业务 IPC，也没有创建真实发送尝试记录。

执行时间：2026-10-23。首次路径采集于 `00:30:03 UTC`，首次快照与查询完成于 `00:44:49–00:45:27 UTC`。续作于 `01:14:25 UTC` 再次制作独立快照并绑定配置；重启后的窗口采集于 `01:31:09–01:31:13 UTC`，完整只读预检通过。

## 2. 实际数据库位置及选择依据

- 当前源码版 `windowprobe` 观察到唯一 `ShiYeLine.exe`：PID 9656、session 2、创建 FILETIME `134371608457181945`，路径为 `C:\Program Files (x86)\nwt3055\ShiYeLine.exe`。
- EXE 磁盘 SHA-256 为 `e7caef3fe744f9fe6ba90dd1d53b7e64241cb54020a8bf37be5e756e2fe881b1`，与静态分析对象一致。
- `<EXE目录>\Green` 存在，是普通文件。因此命中 Green 分支，使用 `<EXE目录>\Syl` 作为数据根目录；静态条件检查存在性，不要求 Green 为目录。
- 实际数据库是 **`C:\Program Files (x86)\nwt3055\Syl\data\qrp`**。
- 通过 `SHGetKnownFolderPath(FOLDERID_Public)` 得到 `C:\Users\Public`。`Public\ShiYeLine`、`Public\ShiYeLine\data\qrp` 和 `Public\Nwt\data\qrp` 均不存在。由于已经命中 Green 分支，不采用 Public 分支，也没有按修改时间选择文件。

静态依据见[群 ID 获取报告](../qgroup-id/qgroup-id-report.md) G16–G19：`0x5b7030` 选择数据根目录，`0x5b73f0 / 0x5b77e0` 保存目录并追加 `data`，`0x5927f0` 获取 Public，`0x615ee0–0x615eef` 追加 `qrp`（G1、G5）。

最初执行的现有 `windowprobe-386.exe` 是缺少路径字段的旧构建，只用于初步观察；路径结论来自随后 `go run ./cmd/windowprobe -format json` 的当前源码版。所有探针输出均排除了窗口标题。

续作开始时客户端仍已退出，没有把旧 PID 当作活跃实例。完成配置绑定后，操作者正常启动并确认登录同一授权账号；源码版探针重新观察到 PID 4488、创建 FILETIME `134371926424130349`、session 2，EXE 完整路径与上次相同。完整预检再次核对磁盘哈希一致，没有沿用旧 HWND。

## 3. 退出与一致性快照

操作者明确确认：当前授权账号已正常登录，能够看到原生测试群 `qun` 且具有正常成员资格，随后使用客户端退出功能正常退出。

- 复制前、复制后和查询完成后的源码版探针均报告 `not_found`，同名进程数为 0；没有强制终止客户端。
- 私有检查目录位于当前 Windows 用户的 `LocalAppData` 下、仓库之外，名称前缀为 `nwt-qgroup-phase10-`。目录 ACL 移除了继承，仅授予当前用户和 SYSTEM 完全控制，复制文件继承该权限。
- 源数据库大小为 11264 字节。退出后不存在 `qrp-wal`、`qrp-shm` 或 `qrp-journal`，因此只需复制 `qrp`。复制前后检查了关联文件集合，没有执行 checkpoint、VACUUM 或恢复。
- 复制前源文件、复制后源文件及副本的 SHA-256 均为 `a9631301dd101206e4b9cb6799d44fc45fcffdcdcab2fbfb4b92625485155bee`。源文件大小及修改时间在复制期间未变；查询后又确认源数据库 SHA-256 未变。
- 使用 Python 标准库 SQLite 3.50.4，以 URI `mode=ro` 打开副本，并启用 `PRAGMA query_only=ON`。成功读取表目录、目标表结构及授权名称结果；查询前后副本 SHA-256 一致。

本轮验证了副本可只读打开和所需查询可执行，没有进行全库完整性扫描或任何修复。源文件未通过 SQLite 打开，只用于复制及哈希核对。

## 4. 当前账号身份表确认

首先只执行：

```sql
SELECT name
FROM sqlite_master
WHERE type = 'table' AND name GLOB 'QGroupInfo_*'
ORDER BY name;
```

结果只有 **1 张 `QGroupInfo_*` 表**，完整名称保存在私有 `manifest.json`，不写入本报告。

归属依据是：已经按当前唯一实例的路径规则确定正确数据库；操作者确认当前账号正常登录且原生群可见；该库只有一个群信息身份表。`CQGroupMgr::Init @ 0x615e80` 与 `CQGroupInfoDb::Open @ 0x63ef50` 会为当前身份打开或创建对应表，故按 G1、G2 的单表规则选择该表。**没有假设表后缀等于用户名，也没有由账号名计算后缀。** 本次不存在多个身份表需要另行确认的情况。

通过 `PRAGMA table_info` 确认实际列名及声明类型与静态报告的 13 列结构一致，包括 `PriKey INTEGER`、`QGroupId TEXT` 和 `Name TEXT`。未读取其他列的数据或其他表的记录。

## 5. 授权群唯一匹配结果

计数与取值查询在同一个只读事务中执行，使用已确认的完整表名、SQL 标识符引用以及参数化的精确名称匹配：

```sql
SELECT COUNT(*), COUNT(DISTINCT QGroupId)
FROM "QGroupInfo_<已确认后缀>"
WHERE Name COLLATE BINARY = ?;

SELECT Name, QGroupId
FROM "QGroupInfo_<已确认后缀>"
WHERE Name COLLATE BINARY = ?;
```

参数均为原始字符串 `qun`，不使用模糊匹配、trim、大小写转换或 `LIMIT 1`。

| 验证项 | 结果 |
|---|---|
| 授权名称 | `qun` |
| 精确匹配行数 | 1 |
| 不同 QGroupId 数量 | 1 |
| 取值查询行数 | 1 |
| QGroupId 格式 | 恰好 32 个十六进制字符，完整匹配 `[0-9a-fA-F]{32}` |
| 原始字符串 | 保持大小写及格式，无截断、连字符转换或额外换行 |
| 真实 ID 获取 | 成功，仅保存在私有 `authorized-group-id.txt` |
| 聊天记录及凭据 | 未读取 |

`QGroupId` 是原生逻辑群 ID，而不是本地 `PriKey`，依据为 G2、G4、G9、G11。其加载到群管理及 UI 群表的过程见 G13–G15；IPC 的 `group_id → 0x4ee740 → 0x4edfc0` 查同一群表，见[IPC 契约](../../ipc/ipc-contract.md) E25-G1/G2。格式检查依据 G7、G8 的创建路径，但没有根据群名称生成或猜测 ID。

私有目录另保存 `manifest.json`（完整身份表名、来源与哈希）和 `validation.redacted.json`（不含实际群 ID 的机器可读结果）。磁盘快照及操作者确认不是在线发送权限或送达证明。

## 6. 本地配置绑定与安全读取

续作得到明确的本地绑定授权后，重新制作了独立私有快照。源数据库哈希与先前人工确认账号时的快照一致，仅有的身份表名称也一致。再次只读精确查询得到相同的唯一原始 ID；未在运行中的数据库上执行 SQL。

真实值已写入 `local/ipcexperiment/config.json`，不写入源码。该目录由 `/local/ipcexperiment/` 规则从 Git 排除，ACL 移除继承，仅允许当前用户与 SYSTEM 访问。配置只包含一个 `authorized_group_id` 字符串，没有群名列表、备用目标或正文参数。私有构建也保存在该目录中。

新增 `cmd/ipcexperiment/config.go` 负责只读解析配置：

- 必须通过 `-config` 显式指定文件；没有自动搜索、环境变量目标或默认群 ID。
- 仅接受一个 JSON 对象和一个 `authorized_group_id` 字段；拒绝重复键、额外字段、多个对象、数组、异常类型或尾随垃圾。
- 完整匹配 32 个十六进制字符，不转换大小写、trim、截断或改为带连字符格式。
- 文件读取失败或格式错误立即停止；解析错误不回显配置值。
- 无 `-config` 时仍可进行无目标的只读客户端预检，但发送请求被拒绝；指定配置也不会隐式启用 `-send`。
- 保留 Windows 386 发送限制、固定正文、逐字人工确认、独占创建且落盘的尝试记录、单次调用和不自动重试。

源码中的空常量已移除；原 `TestAuthorizationNotBound` 替换为 `TestNoImplicitAuthorization` 等配置测试，不再要求把真实 ID 编译进源码。新增已绑定目标时的只读流程测试，证明该模式不调用发送、不创建尝试记录，也不输出群 ID。

可重复执行的**只读**预检命令（无实际 ID、无 `-send`）：

```bash
./local/ipcexperiment/ipcexperiment-386.exe \
  -exe 'C:\Program Files (x86)\nwt3055\ShiYeLine.exe' \
  -config local/ipcexperiment/config.json
```

不要显示、上传或提交配置及数据库快照；Git 忽略不能阻止 `git add -f`，不得强制添加该目录。今后实际发送产生的尝试记录会含目标值，同样必须私有保存；本轮未创建该记录。

## 7. 测试、编译与 Windows 只读预检

以下命令均通过：

```bash
go test -count=1 -timeout 45s ./...
go vet ./...
GOARCH=386 go test -count=1 -timeout 45s ./...
GOARCH=386 go vet ./...
GOARCH=386 go build -o local/ipcexperiment/ipcexperiment-386.exe ./cmd/ipcexperiment
go build -o local/ipcexperiment/ipcexperiment-amd64.exe ./cmd/ipcexperiment
```

单元测试仅使用合成配置及模拟发送；既有 Win32 发送/超时 fixture 只作用于独立测试进程，不向内网通调用业务 IPC。保留既有空授权、人工确认、并发单次记录、身份变化和超时不重试测试。

第一次实际执行无 `-send` 的 386 程序时，客户端尚未运行，返回 `assessment=not_found candidates=0`，退出码为 1；这次不计通过。随后操作者正常启动客户端并明确确认登录同一授权账号，再次执行完整只读预检成功，退出码为 0：

| 预检项 | 结果 |
|---|---|
| 实际进程 | PID 4488，创建 FILETIME `134371926424130349`，session 2 |
| 当前唯一窗口候选 | `0x18062C`，正常显示、非最小化 |
| 磁盘 EXE SHA-256 | 与分析样本一致 |
| Windows 用户、完整性级别 | 与预检程序一致 |
| session / desktop | 与预检程序一致 |
| 五次窗口采集 | 均为一个进程、一个候选、无读取问题 |
| 四次相邻比较 | 均为 `stable_unique_unverified`，无变化 |
| 带真实本地配置执行 | 配置成功解析，不输出 ID，未启用发送 |

随后再次执行同一无 `-send` 命令并通过，将不含群 ID 的结果保存为被 Git 忽略的 `local/ipcexperiment/preflight.redacted.json`。检查固定 APPDATA 尝试记录路径，确认 `NWT-GW-AUTH-TEST-0001.jsonl` 不存在；没有为本轮恢复或删除发送资格。已检查所有改动及未忽略的新文件，不含真实群 ID 或完整身份表名。

`unique_unverified` 仍只表示外部窗口身份检查通过，没有证明该 HWND 的 C++ 对象及业务处理器绑定，也不代表 IPC 可达或消息送达。这里只报告已有预检所验证的项目。

## 8. 首次真实发送准备状态

| 条件 | 本轮状态 |
|---|---|
| 当前实例数据库及身份表 | 已确认；续作复核与原账号快照一致 |
| `qun` 的真实唯一 QGroupId | 已取得，原样绑定到 Git 忽略的私有配置 |
| 当前账号及群成员资格 | 退出前已确认；重启后操作者确认登录同一账号 |
| 本地配置读取、单元测试、386 构建 | 全部通过；不再依赖源码常量 |
| Windows 386 完整只读预检 | 已通过；五次窗口采集稳定 |
| 当前群发送权限、独立接收方就绪 | 实际发送前须再次由操作者确认，预检不自动验证 |
| 本次真实发送授权与逐字确认 | 未进行；本任务禁止发送 |
| 内网通业务 IPC、真实发送 | 均未执行；发送消息数为 0 |
| 送达结果 | `unknown`，没有进行发送实验 |

**只读提取、本地绑定、测试、构建和 Windows 预检已完成，可以进入另行授权的首次受控发送流程，但本轮不允许发送。** 不提供或执行实际发送命令，不预填人工确认，不自动继续。

阶段八的 322 字节仍是受控实验选择，不是完整厂商协议值；本轮没有恢复或修改生产者结构、字段容量和 `cbData` 契约，不据此宣布正式 Adapter 可以开发。
