# 内网通独立消息网关

已验证：独立 Go Windows 386 程序通过正常登录的内网通 3.4.3055 客户端，以业务 `WM_COPYDATA` 向唯一授权原生群发送固定文本，独立接收端确认新增一条、发送者和正文正确。

现有网关继续使用这条已成功的 IPC 路线，不用 IPMsg 或 UI 自动化代替原生群；不使用注入、提权或修改客户端。另行开展的[原生网络协议实现](docs/research/reports/native-protocol/protocol-spec.md)已通过真实上线验收：Linux Go 新节点在 Windows 原客户端显示在线，用户已确认，随后正常退出，见[验收记录](docs/research/reports/native-protocol/native-node-online-confirmation-20261025.md)。1004单聊已实现按本身份联系人列表选择目标、一次提交及1005/1006回执解析，并完成[固定短正文一次真实接收验收](docs/research/reports/native-protocol/native-user-message-confirmation-20261025.md)；3011已实现正常群同步和单peer完整成员检查，并完成向`qun3`同连接连续两条的[独立接收验收](docs/research/reports/native-protocol/native-group-confirmation-20261025.md)；共享Host现支持多成员群投递，仅经合成/loopback验证，尚未真实验收；旧单Client与实验CLI仍限双成员，入群由人工操作。不替换现有IPC发送模块。

## 独立 Web 客户端

新增 [`cmd/nwtweb`](docs/tools/nwtweb.md)：Go 单程序内嵌网页，支持后台常驻上线、正常下线、联系人与已同步的已加入群列表，以及网页单聊/群聊。默认自动识别本机网络并启动离线网页，点击“上线”后才发现并连接本子网客户端；关闭网页不使后台下线。首次正常握手自动保存当前对端联系人，无需 Windows 客户端或手工编写联系人文件。离线时可在网页填写自定义上线名称，或通过 `POST /api/session/start {"name":"自定义名称"}` 设置；不更换本机身份和联系人。

Web上线后自动发现本子网客户端，共享Host维护多个对端连接，可显示并选择多个已就绪联系人，无需指定对端IP或切换发现模式。群发送须先同步最新完整成员且本机在群内；按用户选择向在线就绪且支持群消息的成员投递，离线、未READY或能力不支持的成员列为 `skipped`/未投递，不阻断其他成员，不离线存储、不自动补发。不要求每个成员重复提供快照。所有可用路由共用一个消息编号和正文GUID，每路由最多一次应用提交。至少一个可用成员且它们均传输确认时HTTP200，但存在跳过成员时整群传输ACK仍为false；实际路由失败或全体不可用时HTTP502。结果保留逐成员跳过、提交、传输ACK与原因，部分提交后不要重复发送整群；群列表不保证穷尽。多成员群仅完成合成/loopback验证，不代表真实接收验收。网页默认监听 `0.0.0.0:8788`，本机或局域网通过实际宿主IP的8788端口访问；网页使用管理员密码登录，程序通过API Key调用，完整启动参数、结果语义和验证范围见[使用说明](docs/tools/nwtweb.md)。默认测试使用合成/loopback；真实上线、联系人验证与聊天发送分别取得授权，不凭ACK认定送达。

### 构建与启动

在仓库根目录构建，无需安装 Node.js 或单独构建前端：

```bash
go build -o ./local/nativeclient/bin/nwtweb ./cmd/nwtweb
```

**启动客户端**：默认自动读取本机网络，无需填写参数。

```bash
./local/nativeclient/bin/nwtweb
```

Linux优先选择默认IPv4路由网卡的私有IPv4及真实掩码；没有合适默认网卡时，仅在局域网IPv4唯一时自动选择，否则报错要求 `-local`。非Linux仅在私有IPv4唯一时自动识别。识别只读取本机网络信息，不发探测包，也不猜 `/24`。可用 `-local 192.168.1.50` 选择本机地址并读取真实掩码，或用 `-local 192.168.1.50/24`、旧IP加 `-prefix` 显式覆盖；同时填写CIDR和 `-prefix` 时必须指定同一子网。

默认名称为 `NWT-Go-Web`，网页监听 `0.0.0.0:8788`，资料保存在**当前工作目录下**的 `local/nativeclient/web-node`。`-local`、`-name`、`-listen`、`-data-dir` 均为可选覆盖；相对数据目录以工作目录为准，重启时应使用同一目录以复用身份。

首次启动时从终端输出取得一次性初始化码，在网页设置管理员密码；登录后可在网页生成API Key。Compose部署可用 `docker compose logs` 查看初始化码，日志应限制读取权限。本机或局域网浏览器打开 `http://实际宿主IP:8788`，点击“上线”后才连接对端。`0.0.0.0` 是监听地址，不是访问地址。仅校验配置时追加 `-check`，不启动网页、不创建身份或绑定协议UDP端口；需要启动后立即上线时显式追加 `-online`。协议发现和 ENet 的本机端口默认是 UDP 9011/9012，端口冲突直接报错。

点击“上线”后自动发送一次子网定向广播，并连接正常发现的对端；所有连接共享本机身份和端口，不扫描地址或自动重连。真实上线或广播须先确认身份、对端或子网范围及运行时长，聊天另行确认目标、完整正文与次数；限时上线可追加 `-session-duration 3m`，到期下线但保留网页。

如仅需本机访问，可用 `-listen 127.0.0.1:8788` 限定监听，再通过 SSH 转发访问服务器（在浏览器所在机器执行，将 `user@server` 替换为实际登录地址）：

```bash
ssh -N -L 8788:127.0.0.1:8788 user@server
```

随后浏览器打开 `http://127.0.0.1:8788`。网页和 API 使用鉴权，但直接使用HTTP时密码和密钥不加密，可能被网络中的其他人截获。

关闭网页不影响后台连接；网页“下线”仅停止协议会话，`Ctrl+C` 或 SIGTERM 正常下线并退出程序。程序默认前台运行，不自行转为后台进程，也未安装开机服务；后台运行示例见[使用说明](docs/tools/nwtweb.md#3-后台进程)。

### Docker Compose 部署（Linux）

仓库提供 `Dockerfile`、`compose.yaml` 和严格允许列表的 `.dockerignore`。Compose使用宿主机网络，容器默认以root运行，不启用 `privileged` 或额外权限；程序直接接收SIGTERM，默认不自动重启，停止等待20秒。

**旧部署先保留原挂载**：继续将原命名卷挂到 `/data`，保持原Compose项目名；不要直接换成空 `./data`，否则原身份和日志不会出现在新目录中。不删除或自动迁移旧卷。

新部署默认挂载 `./data:/data`，只需按需修改左侧宿主文件夹，无需设置环境变量或创建 `.env`。在仓库根目录执行：

```bash
docker compose up -d --build
```

本机或局域网浏览器打开 **`http://实际宿主IP:8788`**。默认自动识别宿主机网络并只提供协议离线的网页，**点击“上线”后才发现本子网客户端**；不自动上线或重连。网页默认监听 `0.0.0.0:8788`，`0.0.0.0` 不是访问地址。host网络不需要 `ports:`，宿主机防火墙须允许可信客户端访问TCP 8788。

资料保存在挂载目录，容器不自动修改已有目录的归属；Go新建目录0700、新文件0600。可用 `docker compose logs -f` 查看日志、`docker compose stop` 正常停止并保留资料。需要覆盖网络、监听、名称或目录时可选用 `-local`、`-listen`、`-name`、`-data-dir`，详见[容器部署说明](docs/tools/nwtweb.md#docker-compose-部署linux)。网页和API使用鉴权，HTTP不加密；多成员群尚未完成真实验收。

## 原生 HTTP API 与 Swagger

`nwtweb` 同时提供参考远端 `main` 的 `GET /health`、`GET /contacts`、`GET /groups`、`POST /send/user`、`POST /send/group`。名称逐字精确匹配，ID与名称二选一，同名返回409并保留全部候选。API与网页共用已有原生后台，不使用Windows IPC；程序调用须携带API Key。

Swagger UI位于 `/docs`（也支持 `/swagger/index.html`），OpenAPI 3.0.3位于 `/openapi.json`；所有文档静态资源内嵌，不依赖外部CDN。请求、原生响应、错误码及与IPC接口的差异见[原生API说明](docs/tools/native-api.md)。

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
- `internal/nativeproto`：节点/ENet/业务帧、分片、XML、新身份、共享Host子网发现、多对端连接与连续单聊/群聊。当前客户端入口为上文的 `cmd/nwtweb`；`cmd/nwtprotocol`、`cmd/nwtnode`、`cmd/nwtclient` 保留用于离线研究及历史协议实验，不作为 Web 的可选运行模式，实验规则见各自[离线](docs/tools/nwtprotocol.md)、[限时](docs/tools/nwtnode.md)、[常驻](docs/tools/nwtclient.md)及[群聊](docs/tools/native-group-chat.md)说明。
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

2026-10-27，Linux amd64 与 386 全仓测试及 vet 在隔离容器中均通过，包含托盘程序编译；容器使用方式及宿主机依赖说明见[全仓隔离验证](docs/tools/nwtweb.md#2026-10-27全仓隔离验证)。Windows 386 IPC fixture 只访问独立测试子进程，不向内网通发送。amd64 构建通过不代表 amd64 实际发送已验证。

**Adapter 内部用 `crypto/rand` 为每次有效发送调用生成 `NWT-GW-<随机十六进制>` 标识；每次调用是新任务，不自动重试，日志碰撞直接报错，不换 ID 再试。随机 ID 不提供调用方重试幂等性。** 用 `SendResult.AttemptID` 追加观察，重建 Adapter 后也可依据已有日志继续记录。日志目录不变，成功及 unknown 实验的全部历史记录保留，不重复执行旧任务。接口调整阶段仅使用合成 fixture；随后另经用户授权，已通过[新版群接口](docs/research/reports/ipc-phase12/explicit-target-group-confirmation.md)向 `qun` 发送一条固定正文，并通过[新版单聊接口](docs/research/reports/ipc-phase12/explicit-target-user-confirmation.md)向用户确认的原接收者发送一条固定正文，两次均获独立接收确认。自定义正文实现经离线测试后，另获授权完成[三行消息真实验证](docs/research/reports/ipc-phase12/custom-message-newlines-confirmation.md)：`qun` 群字面 `\n` 转 LF、`PVE-WIN11` 私聊字面 `\r\n` 转 CRLF，各调用一次，独立接收确认条数、发送者、三行正文和 emoji 正确，均记为 `recipient_observed`。随后另经授权完成[500汉字五段真实验证](docs/research/reports/ipc-phase12/custom-500-newlines-confirmation.md)：群聊LF和私聊CRLF各新增一条，全文、发送者和显式换行均正确，均为 `recipient_observed`。更长正文及其他显示边界仍未真实验证。开发期间操作真实客户端复验须另行明确授权，这不是现行发送接口每次调用的额外要求。API 完成与 `LRESULT=1` 不代表送达，只有独立接收观察才记为 `recipient_observed`。
