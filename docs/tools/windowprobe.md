# Windows 只读窗口探测器

`windowprobe` 为后续 HWND 定位收集窗口元数据，不实现消息发送或 HTTP 网关。

## 运行

在已经正常运行内网通的交互 Windows 会话中运行，不需要管理员权限。工具不会启动、登录、激活、移动、最小化或关闭客户端。

要求 Go 1.22 或更新版本；没有第三方依赖。

```bash
# 默认文本输出
go run ./cmd/windowprobe

# 机器可读 JSON
go run ./cmd/windowprobe -format json

# 编译本机架构的独立程序
go build -o windowprobe.exe ./cmd/windowprobe
./windowprobe.exe -format text
```

Windows PowerShell 中执行当前目录程序时使用 `./windowprobe.exe` 或 `.\windowprobe.exe`。

在 Git Bash 中编译和测试 x86 版本：

```bash
GOARCH=386 go build -o windowprobe-386.exe ./cmd/windowprobe
GOARCH=386 go test ./...
./windowprobe-386.exe -format json
```

在 PowerShell 中设置单条命令的架构变量可使用 `$env:GOARCH = '386'`；完成后恢复原值。64 位探测器可以读取本次已观察到的 x86 客户端窗口元数据，不需要注入或访问目标内存。此观察不代表未来跨位数 IPC 已验证。

支持 `-format text|json`、`-count`、`-interval`、`-label`、`-baseline` 和帮助参数；不接受消息、接收方、PID 筛选或发送命令。

### 重复扫描和跨状态对比

```bash
# 同一状态采集 5 次，每次完成后等待 1 秒
go run ./cmd/windowprobe -format json -count 5 -interval 1s -label normal

# 将输出保存到仓库外的私有目录；文件名必须尚不存在，避免覆盖历史记录
# 下列两个命令中的路径需要替换为实际私有目录；Git Bash 中禁止覆盖已有输出文件
set -o noclobber
go run ./cmd/windowprobe -format json -count 5 -interval 1s -label normal > /private/path/normal.json

# 操作者手动切换状态后，使用前一记录末样本作首对比
go run ./cmd/windowprobe -format json -count 5 -interval 1s -label restored -baseline /private/path/normal.json
```

上例重定向并不会自动脱敏，可能保存 caption 和安装路径；本轮实测用外部包装先清空全部 `title` 再保存，未将原始快照放入仓库。探针自身仍默认只输出 stdout，不自动创建记录文件。

- `count` 范围 1–1000，默认 1；`interval` 默认 1s，不得为负，多次扫描至少 1ms。它是两次采集之间的等待时间，不是固定采样时钟。
- `label` 是操作者声明的状态，不是探针对“已登录”“已最小化”的证明。必须同时核对 `visible`、`minimized` 和实际操作。
- 默认单次仍输出一个 `Report`；重复扫描或指定 baseline 时输出 `Series`：`scans`、`comparisons`、`stability`。
- baseline 可为单个 Report 或非空 Series，Series 使用最后一个扫描；baseline→首次扫描的比较排在第一位，其后是本轮相邻比较。
- baseline 在采集前严格解码；未知字段、非法句柄、尾随 JSON、空 Series、缺少采集时间均报错。旧版记录缺少路径、创建时间、存活状态时可读取，但比较结果为 `incomplete`，不能宣称稳定。旧记录内的 assessment 不作为可信结论，比较会重算分类。
- Ctrl-C 可停止等待或后续扫描；单次 Win32 调用不能在中途取消。已完成的样本仍输出，错误写 stderr，退出码为 1；没有样本则只输出错误。部分记录的稳定性只描述已完成样本，不代表原计划已完成。

### 退出码

| 退出码 | 含义 |
|---|---|
| `0` | 探测完成；仍须检查 `assessment.status`，并不代表找到已验证主窗口 |
| `1` | 采集失败/取消、baseline 无效、输出失败、任一样本或比较为 `incomplete`；有已完成样本时仍输出报告 |
| `2` | 命令行参数错误 |

`not_found`、`no_candidate`、`ambiguous` 是有效诊断结果，退出码为 0。`go run` 在子程序非零退出时会额外输出 `exit status`，自动化调用建议使用编译后的程序。

## 读取内容及范围

1. 用 Toolhelp 进程快照按文件名大小写不敏感匹配 `ShiYeLine.exe`，保留所有匹配进程。每个 PID 仅申请 `PROCESS_QUERY_LIMITED_INFORMATION | SYNCHRONIZE`，从同一个进程 handle 查询完整 EXE 路径、创建 FILETIME、session ID，并用 `WaitForSingleObject(handle, 0)` 确认检查时仍存活；拒绝访问或已退出时报告问题，不提权。创建时间以 `creation_time_ticks` 十进制字符串编码，防止 JSON 数字精度丢失。
2. 用 `EnumWindows` 枚举当前线程 desktop 的顶层窗口，包含隐藏窗口，再用 `GetWindowThreadProcessId` 过滤 PID。
3. 只对目标 PID 的这些窗口读取：
   - HWND、PID、TID；
   - `GetClassNameW` 类名和 `GetWindowTextW` 顶层 caption；
   - 可见性、最小化状态；
   - `GW_OWNER`、`GA_PARENT`、`GA_ROOT`、`GA_ROOTOWNER`；
   - style、ex_style 和 `GetWindowRect` 坐标与宽高。
4. 复查已记录 HWND 的存在性、PID/TID、类名、owner/root/root_owner、style 与可见性；再取一次进程快照，比较进程集合、路径、创建时间、会话与存活状态。即使 PID 相同，创建时间不同也视为不同进程。
5. 输出全部窗口与读取问题，不隐去非 `SkinDialog` 窗口。

`parent` 来自 `GetAncestor(GA_PARENT)`。普通顶层窗口可能返回 desktop HWND；它与 `owner` 不是同一概念。`top_level` 另根据 root 是否等于自身及 `WS_CHILD` 判断；拥有 owner 的窗口仍然可以是顶层窗口。

窗口坐标可以是负值；宽高从 RECT 的两个端点计算，不把负坐标视为错误。尺寸可能受 DPI 虚拟化影响，包含系统定义的窗口边框，不作为识别主窗口的固定规则。

不枚举子控件或读取编辑框文本，不读取 message-only 窗口，不遍历其他 desktop，不截图、不读取数据库、进程内存、聊天正文或凭据。跨进程 `GetWindowTextW` 读取系统维护的 caption，不向客户端发送 `WM_GETTEXT`；本工具没有 `SendMessage`、`PostMessage` 或 `WM_COPYDATA` 发送代码。

初始进程快照失败时直接返回错误；其他 API 失败及观察到的变化写入 `issues`，评估为 `incomplete`，不把读取失败伪装成空标题或零尺寸。空 caption 是合法结果。文本读取达到缓冲区边界会报告可能截断。

标题可能包含组织、联系人或群名称。默认只输出到 stdout，不保存到磁盘；不要把原始输出提交 Git 或公开分享。文本格式会转义标题中的控制字符。JSON 使用 UTF-8，HWND 为十六进制字符串，避免 JSON 数字精度损失。

## SkinDialog 分类与依据

分类不依赖标题字面值、固定尺寸、枚举顺序或“第一个可见窗口”。

| `role` | 含义 | 是否保留为主窗口候选 |
|---|---|---|
| `hidden_owner_suspect` | 稳定的隐藏、无 owner 顶层 SkinDialog，且被同 PID 的另一个稳定 SkinDialog 作为直接 owner 引用 | 否，但身份仍只是推断 |
| `main_candidate` | 直接 owner 符合上述隐藏 owner 特征的稳定顶层 SkinDialog | 是 |
| `owned_dialog_suspect` | owner 是同 PID 的其他 SkinDialog，但不满足隐藏 owner 特征；疑似普通对话框 | 是，证据不足时不草率排除 |
| `skin_dialog_unclassified` | 其他稳定顶层 SkinDialog | 是，包括隐藏、最小化或没有可见 owner 的窗口 |
| `other_window` | 非 SkinDialog、非顶层或元数据不完整/复查失败的窗口 | 否；读取问题会阻止整体唯一性结论 |

隐藏 owner 的识别依赖其被另一个 SkinDialog 引用，不是“隐藏即 owner”。隐藏的主窗口本身仍可保留为候选。由于通用类无法排除某些普通对话框，新增对话框可能使结果变为 `ambiguous`，这是刻意保留的不确定性。

### 静态证据

以下均来自 [第五阶段报告 C 节和证据索引](../research/reports/ipc-phase5/ipc-phase5-report.md)，不是新反编译或运行时对象读取：

- `0x54fd20` 注册通用 `SkinDialog`；N15：`ev_adab67eb5ac406258385864564bdc7c7f1f5c59b081d7403650621fa710a5b8f`。
- `CSkinDialog::Create @ 0x524890` 使用该类创建窗口；N16：`ev_42fab04517a97aa27816fe824718380101b7e8067e778158b4ddfa19ebeb6aea`。
- `0x4029d0` 创建隐藏 owner，随后将其 HWND 传给主窗口 `DoModal`；N18：`ev_b0e215670dc529323c5c94c2fffd0ad6d267b489b86df2f1446bb95e939f7a90`。
- `0x43ea90` 构造 `MainDialog.xml` 对应主对象；N17：`ev_206846c545bf5033d7df2fd89f524db7cd14c17069e5e4cc484128f86223b6de`。本探测器不读取该对象或资源名，因此不能用元数据证明窗口对应此对象。

### 可靠性状态

| `assessment.status` | 解释 |
|---|---|
| `not_found` | 进程快照中未找到同名程序 |
| `no_candidate` | 找到进程，但当前 desktop 没有可用 SkinDialog 候选 |
| `unique_unverified` | 单进程、单候选、无已记录读取问题；CMainDialog 身份仍未验证 |
| `ambiguous` | 同名进程多于一个，或者窗口候选多于一个；不自动选实例或窗口 |
| `incomplete` | API 读取或复查失败，或采集前后观察到进程变化；优先于其他状态 |

任何状态都不是发送许可。报告不含默认选中的 HWND。

### 已确认、推断和未验证部分

- **运行时已观察**：可以按 PID 枚举该运行客户端的窗口，并读取类名、caption、几何和 owner/root 元数据。
- **推断**：符合静态创建关系的隐藏 owner 及主窗口候选；类名和外部关系无法证明其 C++ 对象类型。
- **运行时已观察**：完整进程路径、创建时间和检查时存活状态；路径只用于实例绑定，不等于验证版本或文件真实性。
- **未验证**：版本、哈希、用户身份、登录状态的程序化确认、完整性级别、业务处理器绑定、IPC 可达性和消息送达。重启时的正常登录由操作者确认，探针不读取其账户或凭据。
- **本阶段已手动实测**：托盘隐藏、恢复显示、打开其他 SkinDialog、正常退出与重新启动。探针未操作 UI。标准最小化 `IsIconic=true` 没有实际观察到，仍未验证，不能与托盘隐藏混同；详见阶段六记录。

同名文件不等于已验证样本。同一用户也不等于同一 session/desktop。进程枚举是系统范围的快照，窗口枚举受当前 desktop 限制；在其他会话的同名进程仍会使报告拒绝自动选实例。

采集不是原子快照：复查不保证同时刻一致，也不能发现全部短暂变化、同 PID/TID 下且元数据完全相同的 HWND 复用，或采集期间新增但尚未枚举的窗口。创建时间检查可以识别已观察到的 PID 复用，但不保证所有瞬时变化都被捕获。已有 HWND 复查之后仍可立即失效；新对话框也可能在枚举完成后出现。未来发送器仍须完成完整 IPC 契约、身份授权及调用前重新定位，不能缓存本报告作为长期目标。

## 对比判定与内部定位接口

`Compare(before, after)` 不依赖枚举顺序，重新评估两次快照，记录进程新增/移除/身份变化、候选集变化、窗口新增/移除/身份变化与外观变化。标题不参与身份比较，changes 也不保存标题。

| `comparison.status` | 判定 |
|---|---|
| `stable_unique_unverified` | 两次完整且时间有序的单进程、单候选快照；进程 PID/路径/创建时间/session，以及候选和快照中可观测 owner 的 HWND/PID/TID/类名/owner/parent/root/root_owner/顶层关系一致（Resolve 另外强制要求隐藏 owner 存在） |
| `identity_changed` | 两次均有唯一候选，但进程身份、候选集合或候选/owner 关系变化；旧 Target 不可沿用 |
| `ambiguous` | 任一完整快照存在多进程或多候选；即使候选集连续不变也不选第一个 |
| `unresolved` | 任一快照为 not_found/no_candidate；“进程一直不存在”不是候选识别稳定 |
| `incomplete` | 读取或复查失败、进程身份字段缺失/未存活，或采集时间无效/逆序/重叠 |

最小化、隐藏、恢复、尺寸和 style 的变化可记录为 `presentation`，不单独否定身份一致性；但扫描内部若正在改变仍按原规则标为 `incomplete`，保留该样本，不自动重试或过滤失败记录。无关辅助窗口的变化也会记录，但不会单独否定候选身份。`Series.stability` 无比较时为 `insufficient_samples`，全部比较通过时为 `stable_unique_unverified`，否则为 `not_stable`。后者包括有意重启和歧义，不意味着发生识别错误。

后续 Go IPC Adapter 通过 `internal/windowprobe` 调用以下内部定位函数：

```go
// 每次使用前重新获取；previous 为 nil 表示明确获取当前候选。
func Resolve(expectedPath string, previous *Target) (Target, error)
```

- 必须传入操作者确认的 `ShiYeLine.exe` Windows 绝对路径；支持盘符和 UNC，大小写及斜杠差异可匹配，不展开环境变量或猜测安装目录。设备路径、相对路径和 NUL 被拒绝。
- 每次调用都重新 Scan；全系统出现多个同名进程时，即便仅一个匹配路径，也拒绝自动选实例。
- 要求唯一候选形成同 PID 的隐藏、无 owner、顶层 SkinDialog owner 关系，且 `root_owner` 一致。通用孤立 SkinDialog 即使单一，也不能通过 Resolve。
- 返回前持有只读进程 handle，重新检查 PID/路径/创建时间/session 与存活，重新读取并复查候选和 owner 的 PID/TID、类名、存在性与窗口关系，最后再次检查同一进程 handle 仍存活。
- `previous != nil` 时，进程或候选/owner 身份不同即返回错误与零值 Target，不自动把旧客户端绑定替换为新进程。重启后由调用方明确重新获取，不把重定位变成自动重发。
- 失败绝不返回旧 HWND。成功返回的 `Target.Window.HWND` 及 `CheckedAt` 只说明刚刚检查时成立，`Reliability` 仍为 `unique_unverified`，不是发送许可。首次获取不承诺历史稳定；需要历史证据时另用 Observe/Compare。
- 此接口不能消除上述时间竞争：进程 handle 在返回时关闭，返回后窗口可立即销毁或复用。未来 Adapter 每次业务调用前都须 Resolve，且不能据此声明消息可达。

`Observe(ctx, count, interval, label)` 封装重复 Scan 和相邻 Compare；测试替换内部采集函数，真实窗口快照与合成快照使用同一套判定代码。

## 初始只读运行记录（已省略标题）

首次观察时间：2026-10-22 15:55:55 UTC，Windows amd64 / Go 1.27.3。

- 找到 1 个 `ShiYeLine.exe`，session ID 为 2。
- 枚举到该 PID 的 20 个顶层窗口，其中 2 个使用 `SkinDialog`。
- 一个 SkinDialog 隐藏、无 owner、尺寸为 0×0，被另一个可见的 304×574 SkinDialog 直接引用为 owner。
- 可见 SkinDialog 的 root 是自身，root_owner 是前述隐藏窗口；与 N18 的静态创建关系一致。
- 所记录窗口复查通过，无读取问题；评估为 `unique_unverified`，候选数 1。
- 未记录真实标题、PID 或 HWND 到本文；这些值会随客户端生命周期变化。

该记录只说明这一次快照，不能据此宣称所有状态下主窗口定位已经可靠，也不能将推断提升为已验证 IPC 入口。

## 阶段六：手动状态测试记录与标准

任务执行日期按会话为 2026-10-23；以下 UTC 时间是探针所在主机实际报告的 2026-10-22，保留原值，不据此改写系统时钟或虚构采集日期。环境：Windows 10 19044、Go 1.27.3，amd64 与 386。每个状态每种架构各 5 次，间隔 1s，共 60 次状态快照。时间均来自 `started_at/finished_at`。状态由操作者手动改变，探针不启动、退出或自动操作客户端。

本文保留非内容性 PID/HWND 作为本轮证据，均为生命周期内标识，不是配置值；不记录实际 caption、聊天记录、账户名或完整安装路径。EXE 路径仅在本机查询、按指定路径匹配，创建时间用来区分重启前后。

### 逐项结果

| 状态 | amd64 UTC 时间 | 386 UTC 时间 | 实际观察和结论 |
|---|---|---|---|
| 初始显示 | 16:28:53–16:28:58 | 16:28:59–16:29:03 | PID 7716，session 2，20 个顶层窗口，2 个 SkinDialog，1 个候选；每次 unique_unverified，8 个架构内相邻比较均 stable_unique_unverified |
| 操作者执行“最小化”后的状态 | 16:33:08–16:33:12 | 16:33:12–16:33:16 | 候选 visible=false、minimized=false，实际为托盘隐藏，不是标准最小化；同一 PID、HWND 和 owner，双架构均稳定 |
| 恢复显示 | 16:34:22–16:34:26 | 16:34:28–16:34:32 | 候选 visible=true、minimized=false；相对隐藏快照首比较只有 presentation 变化，身份一致；双架构最终 Resolve 连续两次通过 |
| 打开普通附加对话框 | 16:36:44–16:36:48 | 16:36:50–16:36:54 | 共 4 个 SkinDialog、3 个候选，10 次快照全部 ambiguous；Resolve 均报 assessment=ambiguous candidates=3，返回零值 HWND。没有按旧候选、面积或 owner 特征强行选择 |
| 正常退出，暂不重启 | 16:39:06–16:39:10 | 16:39:11–16:39:15 | 10 次快照均为进程 0、窗口 0、候选 0、not_found；相对退出前记录了 process_removed/candidate_set/window_removed，不沿用旧句柄 |
| 正常重新启动并显示 | 16:41:20–16:41:24 | 16:41:25–16:41:29 | 新 PID 9656、session 2、20 个顶层窗口，唯一候选与隐藏 owner 均改变。相对旧进程首比较 identity_changed，其后 4 个相邻比较均 stable_unique_unverified；旧 Target 被拒绝，明确重新获取后连续两次最终复核通过 |

重启前关系：`0x1036C → owner/root_owner 0x1036A`，隐藏 owner 自身无 owner，两个类名均为 SkinDialog；TID 7680。重启后关系：`0x2036E → owner/root_owner 0x30344`，隐藏 owner 同样无 owner。创建 FILETIME 从 `134371571670317414` 变为 `134371608457181945`，进程路径一致，最终查询时均仍存活。

附加对话框场景实际新增 `0xC01D4`（SkinDialog，无 owner）及 `0x2206B4`（SkinDialog，owner=0xC01D4），原有 `0x1036C` 仍在，但不能凭“过去出现过”自动选择。3 个候选持续不变也不是唯一识别稳定，因此 Series.stability=not_stable 是预期拒绝结果。

重启记录包含旧 baseline，所以整体 Series.stability=not_stable 是正确结果，不应掩盖首个 identity_changed；仅看重启后 5 次采集内部则身份一致。双架构实际验证了旧 Target 的拒绝以及新 Target 的返回前进程/窗口复核。未向任何窗口发送 WM_COPYDATA。

### 可重复执行的判定标准

每项先等待操作者确认状态，再采集 5 次，不能自动驱动真实 UI：

1. **显示基线**：每次恰好一个同名进程、一个候选，无读取问题；路径、创建时间、session、候选/owner 关系一致，4 个相邻比较均稳定。
2. **托盘隐藏**：实际确认 visible=false；不可只凭操作名称判断。主候选仍被保留，owner 与进程身份不变，隐藏本身不构成删除候选的理由。
3. **标准最小化**：必须至少观察到候选 minimized=true / IsIconic 非零，再检验候选和 owner 不变。本轮未出现此状态，只有合成快照测试，不记作真实通过。
4. **恢复**：visible=true，minimized=false，候选和 owner 身份与隐藏/最小化前一致；跨状态变化仅 presentation，若真实 HWND 改变则应明确 identity_changed，不能静默沿用。
5. **其他 SkinDialog**：记录新增窗口及 owner，候选超过一个时必须 ambiguous；Resolve 必须错误且 HWND=0，重复扫描不能用多数票选窗口。关闭全部附加对话框后应重新采集，不自动继续先前操作。本轮没有单独采集“仅关闭对话框、尚未退出”这一中间状态。
6. **退出与重启**：先看到 not_found 和零窗口，再由操作者正常启动；新 PID 或创建时间必须与旧进程区分。跨重启比较必须标记身份变化，旧 Target 必须失败；只有显式重新获取后可返回新候选。即使某次 PID/HWND 数字碰巧复用，也不能跳过创建时间和关系检查。
7. **过渡/错误**：API 拒绝访问、窗口销毁、采集过程中关系变化均 incomplete/定位失败，记录失败样本，不自动重试、忽略或升级为可靠。

### 稳定性评价及未验证问题

**在本轮单实例、同一交互 desktop、客户端显示/托盘隐藏/恢复和一次正常重启中，唯一候选的外部身份稳定；附加 SkinDialog 导致歧义时能够明确拒绝。不能称为在任意客户端 UI 状态下都能自动定位主窗口。**

仍未验证：

- 标准 IsIconic=true 最小化及其恢复；托盘隐藏不能替代该证据。
- 多轮重启、重新登录/登录界面、不同安装版本、多个真实同名客户端、其他 session/desktop；当前多实例拒绝仅有合成测试。
- 单独关闭附加对话框后的恢复，以及所有可能的 SkinDialog 类型/创建关系；本轮只覆盖操作者打开的普通对话框。
- 活进程身份与磁盘文件真实性：未读取版本或重新计算运行文件哈希，不读取令牌、凭据或完整性级别。
- CMainDialog 对象身份和 `0x449c60` 业务处理器绑定；静态依据仍为 N15–N18。成功定位不能代替业务验证。
- 相同进程/TID 下、元数据恰好完全相同的 HWND 快速复用，以及最终复核后立即销毁/创建的时间竞争。
- IPC 可达性、UIPI、跨位数 IPC、正常/超时返回和实际送达。此轮仅验证跨位数只读元数据，不发送消息。

完整去标题扫描暂存在系统临时目录的 `windowprobe-phase6-*` 目录，仅供本机复核，不在 Git 中；本文不依赖该临时目录长期存在。以后重测应新建私有记录目录，不覆盖已有文件。

## 测试与代码位置

```bash
go test ./...
go vet ./...
```

已验证 Windows amd64 和 386 的测试、vet 和独立 EXE 编译。测试覆盖：多进程、多个窗口候选、隐藏主窗口、疑似普通对话框、非目标类、子窗口排除、跨 PID owner、API 失败、不稳定窗口、UTF-16 caption、空 caption / LastError、RECT、owner/root_owner、失效句柄、64 位 HWND JSON 精度、参数检查和输出错误。

窗口创建/销毁测试只操作本测试进程线程中的隐藏 `STATIC` fixture；不会给内网通创建窗口、显示测试 UI 或修改目标进程。另有并发 Scan 测试，只读枚举现有进程元数据。真实客户端的状态切换全部由操作者完成；测试程序不发送消息或操作 UI。

新增测试覆盖：路径/创建时间/存活查询与无效进程 handle，最终复核拒绝、每次重新扫描而非缓存、旧 Target/复用 PID/owner 新增或消失、多候选拒绝、最小化/恢复合成快照、baseline 严格解码、FILETIME/句柄保真、时间顺序、采集取消和部分结果、早期歧义不能被后续成功覆盖、重复 CLI 参数、比较文本/JSON 输出与短写。重复采集复用同一个 Win32 callback 槽位，避免每次 NewCallback 却无法释放；并发 Scan 会串行采集。

Windows amd64、386 均已通过 `go test -count=1 -timeout 45s ./...`、`go vet ./...` 和 EXE 构建。Linux amd64 仅通过交叉构建，未在 Linux 执行测试，不视为 Linux 运行验证。

`TestResolveAuthorizedCandidate` 默认跳过，必须显式设置 `WINDOWPROBE_TEST_EXE` 为已确认的 EXE 绝对路径后才只读运行真实定位；本轮在正常显示、恢复和重启状态分别对两种架构执行并通过。设置 `WINDOWPROBE_TEST_REJECT=1` 时要求当前为 ambiguous 且 Resolve 拒绝；设置 `WINDOWPROBE_TEST_PREVIOUS` 为旧 Target JSON 路径时要求拒绝旧身份后可显式获取新身份。本轮分别在附加对话框和重启后真实执行并通过。这些变量仅由测试程序读取，不是应用配置或消息发送开关；未设置时常规测试不要求客户端运行。

| 文件 | 职责 |
|---|---|
| `cmd/windowprobe/main.go` | 参数处理、文本/JSON 输出与退出码 |
| `internal/windowprobe/model.go` | 窗口快照及评估结果结构 |
| `internal/windowprobe/scan_windows.go` | Windows 只读采集与复查 |
| `internal/windowprobe/assess.go` | 无平台 API 的分类与可靠性评估 |
| `internal/windowprobe/compare.go` | 跨快照身份、候选集、窗口与外观比较 |
| `internal/windowprobe/observe.go` | 可取消重复采集、样本保留和整体稳定性 |
| `internal/windowprobe/resolve.go` | 供后续 Adapter 调用的内部定位与旧身份拒绝 |
| `internal/windowprobe/stability_test.go` | 合成状态、比较、定位和取消回归测试 |
| `internal/windowprobe/scan_other.go` | 非 Windows 环境明确报错，仍可运行纯分类测试 |

本工具只负责只读窗口定位，不证明 IPC 送达。后续固定正文的群与单聊发送已获独立接收确认，当前 Adapter 每次调用仍复用 `Resolve` 复核客户端；完整厂商协议和任意正文范围仍未知，见 [IPC 契约](../research/ipc/ipc-contract.md)。
