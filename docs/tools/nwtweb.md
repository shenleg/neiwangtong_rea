# nwtweb：独立原生协议 Web 客户端

`cmd/nwtweb` 将已有的常驻原生协议客户端接到网页：后台维护连接，网页控制上线、下线、查看联系人与已同步的已加入群，并提交单聊或群聊。无需 Windows 客户端、IPC、前端构建工具或外部静态资源。

## 1. 本版功能与范围

| 功能 | 行为 |
|---|---|
| 后台常驻 | 上线后没有固定运行时限；关闭、隐藏或刷新网页不使后台下线 |
| 联系人 | 双向 READY 后从当前对端正常握手取得 UserID、名称与实际发现端点，保存为本独立身份的联系人快照 |
| 已加入群 | 展示本次正常群同步确认本机属于其中的完整成员快照；不是全部群的穷尽列表 |
| 单聊 | 从联系人列表选择原始 UserID，复用同一连接提交1004 |
| 群聊 | 从已同步群选择原始 GroupID，检查完整成员后向在线可用成员提交3011，其余列为未投递 |
| 结果 | 分别展示提交、传输确认、业务 ACK、协议回执；群发送另展示逐成员提交/ACK/错误，送达状态仍为 unknown |
| 退出 | 网页下线或 SIGINT/SIGTERM 请求正常1017及传输断开；SIGKILL无法执行正常退出 |

**上线后自动发现本子网客户端并维护多对端连接**，无需指定对端IP或选择发现模式。多个客户端共享同一个独立身份和同一组发现/ENet端口，每个已就绪身份分别显示在线并可选为单聊目标。每次上线在约两秒内发送三次标准定向广播，并接受本子网的正常发现包，不枚举地址、不扫描、不自动重连失败连接。广播不能保证发现不可达、不响应或子网外的客户端；从未学习过的离线联系人无法凭广播获知。

共享Host支持多成员群：发送前已同步最新完整成员且本机在群内；按用户选择向在线READY且支持群消息的成员投递，离线、未READY或能力不支持的成员列为skipped/未投递，不阻断其他成员，不离线存储、不自动补发。不要求每个成员重复提供群快照。只生成一个MSG_ID和正文GUID，各可用路由提交同一3011，每路由最多一次应用提交，并发等待结果。至少一个可用成员且它们均传输确认时HTTP200/error为空，存在跳过成员时整群传输ACK仍为false；实际路由失败时HTTP502并保留结果。原网页接口的Host检查发现全体不可用时返回502，不分配MSG_ID、不Queue，保留全体skipped；公开接口若预先发现全员未就绪则返回503。群版本变更或日志失败仍报错，不补发。网页以后台sendable为准，至少一个可用成员才启用发送，协议层仍检查实际身份与完整成员。不主动入群。旧单Client与实验CLI仍限双成员。多成员群仅经合成/loopback验证，尚未真实验收；历史双成员真实验收见[群聊报告](../research/reports/native-protocol/native-group-confirmation-20261025.md)。

本版只显示此次网页会话主动发送的消息，不接收显示对方聊天，不读取聊天历史；刷新页面后本页正文和草稿不保留。联系人与群名均按文本展示，不作为 HTML 渲染。

## 2. 构建和只读校验

```bash
go build -o ./local/nativeclient/bin/nwtweb ./cmd/nwtweb

# 合成参数，只读校验，不是连接真实对端的授权。
./local/nativeclient/bin/nwtweb -check -local 127.0.0.1/8
```

`-check` 只读识别及校验，不创建目录、身份或记录，不绑定HTTP/UDP端口；系统网卡枚举可通过本地内核接口读取，不向网络对端发包。同时设置 `-online` 也只校验。已有身份或最新联系人快照损坏时直接报错，不重建身份或静默使用旧联系人。

### 正常启动

去掉 `-check` 后，程序**默认读取本机网络并仅启动 HTTP，仍未连接协议对端**。本机或局域网通过 `http://实际宿主IP:8788` 访问，点击“上线”才开始子网发现并常驻维护各对端连接。连接失败后网页保留错误，程序继续提供网页，不自动重连。再次上线须明确点击按钮。

最短启动示例：

```bash
./local/nativeclient/bin/nwtweb

# 合成参数，仅检查配置，不启动网页或发广播。
./local/nativeclient/bin/nwtweb -local 127.0.0.1/8 -check
```

自动识别只读取本机网卡、地址、掩码及Linux默认IPv4路由，不发探测包、不猜 `/24`。Linux优先选择默认IPv4路由网卡的私有IPv4和真实掩码；没有合适默认网卡时，仅在局域网IPv4唯一时自动选择，否则报错要求 `-local`。默认网卡有多个可用IP而无法唯一确定时也须显式指定。非Linux仅在私有IPv4唯一时自动识别，不使用默认路由选择。

以下可选覆盖仅适用于 `nwtweb`，不改变 `nwtclient` 或 `nwtnode` 的参数与上线方式。

- `-local`：默认自动识别。可传本机裸IPv4读取对应网卡真实掩码，例如 `192.168.1.50`；也可显式传 `192.168.1.50/24`，保留本机地址 `.50` 并使用子网 `192.168.1.0/24`。旧写法 `-local 192.168.1.50 -prefix 192.168.1.0/24` 仍可显式覆盖。
- `-prefix`：可选覆盖子网，须包含本机IPv4且不窄于 `/30`。同时指定CIDR和 `-prefix` 时必须是同一子网，冲突直接报错。该子网用于本机公告、限制发现来源及定向广播范围；上线后自动发现，每次会话最多接纳256个身份，不扫描或重连；失败和已停止连接也占用本次会话的名额。
- `-session-duration`：每次协议上线的活动时限，默认0表示常驻。正值到期正常下线，HTTP继续运行；适用于有明确时长的真实验证。
- `-name`：本独立节点的默认显示名称，默认 `NWT-Go-Web`。网页离线时可在“上线名称”输入框修改，再点击上线；也可使用 `POST /api/session/start {"name":"自定义名称"}`。成功受理后本进程记住新名称，重启仍使用CLI配置。
- `-data-dir`：默认 `local/nativeclient/web-node`；CLI将相对路径按当前工作目录转换为绝对路径，底层模块仍要求绝对路径。启动网页时创建目录供认证资料使用，明确上线时才创建协议身份。新建目录权限0700，新文件0600；重启须使用同一目录才能复用认证配置和身份，已有私有部署可继续传原绝对路径。
- `-discovery-port` / `-enet-port`：默认9011/9012；实际端口冲突即失败，不自动更换。
- `-listen`：默认 `0.0.0.0:8788`，监听全部IPv4网卡；可用私有IPv4限定网卡，或 `127.0.0.1:8788` 仅供本机访问。可用端口0分配临时网页端口；日志分别展示实际监听地址和可访问的网页地址，通配监听时网页地址使用选定的本机IP。网页通过密码登录，API使用密钥。
- `-online`：操作者显式要求程序启动后立即上线；默认不启用。失败仍不自动重连。

上线后由共享Host间隔一秒发送三次子网定向广播，并对正常发现的每个身份建立独立连接；各连接的ENet与应用就绪各有15秒预算。一个对端失败不会将其他对端标成离线，错误带连接序号保留在阶段记录中。常驻阶段维护各自的ENet与1016心跳；退出时并发正常关闭各连接，正常退出清理预算5秒，命令总退出预算10秒。

`protocol_ready` / 后台在线表示Host已准备接收发现，不等于所有客户端完成握手；真正可发送的联系人以各身份双向READY后发布的 `OnlineUserIDs` 为准。尚无客户端就绪时网页显示“等待客户端”。

### 服务器上的网页访问

默认通过实际宿主IP的8788端口访问。如只需本机访问，可用 `-listen 127.0.0.1:8788` 限定监听，再通过SSH转发访问服务器，并保持浏览器地址与后台监听端口一致：

```bash
ssh -N -L 8788:127.0.0.1:8788 user@server
```

随后在本机浏览器访问 `http://127.0.0.1:8788`。后台校验 Host、同源 Origin 和JSON类型，拒绝跨站请求和 DNS 重绑定，不要求自定义请求头；不信任代理转发头。页面设置 CSP、禁止嵌入及 `no-store`。

### 局域网体验

默认监听 `0.0.0.0:8788`，可通过本机各IPv4网卡访问，也可用 `-listen 私有IPv4:8788` 只监听一张网卡。浏览器打开实际宿主IP，例如 `http://192.168.1.50:8788`，而不是 `http://0.0.0.0:8788`。监听设置不隐含协议上线或聊天发送；不设置 `-online` 时仍等待网页操作。HTTP不加密，密码、会话和API密钥可能被网络上的其他人截获；需要防止窃听时可使用SSH隧道。当前Host和Origin校验不信任代理转发头，不能直接假定HTTPS反向代理可用。

浏览器使用 `crypto.getRandomValues` 生成32字符随机请求标识，支持局域网HTTP，不依赖仅安全上下文可用的 `randomUUID`，也不使用 `Math.random`。

## 3. 后台进程

程序不自行 fork，也不把网络运行绑定到网页连接。可由终端、`nohup` 或进程管理器保持进程。首次启动时从终端输出取得一次性初始化码，在网页设置管理员密码并登录，再按需生成API Key；后台运行时初始化码写入标准输出日志，需限制日志读取权限。重启仍未初始化的服务会生成新码，旧码失效。认证资料与协议身份保存在数据目录，应一起备份；不要让多个服务进程同时使用同一数据目录。认证标记或凭据之一缺失时程序拒绝启动，若两者都被删除则无法与新安装区分。首次初始化过程中写入失败也会拒绝启动，须人工检查并恢复资料；Windows无法同步目录项，突然断电后目录项持久性不受保证。下面仍是合成配置格式；实际部署必须替换为已确认的本机、身份目录和对端。

```bash
nohup ./local/nativeclient/bin/nwtweb \
  -local 127.0.0.1/8 \
  -data-dir /absolute/private/web-node \
  > /absolute/private/nwtweb-output.log 2>&1 &
```

该命令只启动离线网页；操作者在页面点击上线。需要启动即上线时额外显式传入 `-online`。结束自己的进程使用 SIGTERM，不用 SIGKILL 验证正常退出。不配置自动重启后上线或消息重放。

直接运行时是独立进程；未安装 systemd 服务、设置开机启动或自动重连。

### 重置管理员密码

网页目前没有修改密码的入口。需要更改或忘记密码时，可通过重新初始化认证来设置新密码：

1. 正常停止 `nwtweb`，确认使用该数据目录的进程均已退出。Compose 部署可使用 `docker compose stop`；停止会同时使协议会话下线。
2. 确认实际数据目录：直接运行默认为当前工作目录下的 `local/nativeclient/web-node`，也可能由 `-data-dir` 指定；默认 Compose 部署为宿主机的 `./data`，旧部署可能使用命名卷。**保留整个数据目录及协议身份**，不要清空目录或更换空卷。
3. 将数据目录中的 `auth.json` 和 `auth-initialized` **同时移到目录外的安全位置备份**。不要只移动其中一个，否则启动时会报告认证资料缺失；备份包含旧认证摘要，应限制访问。
4. 用原配置和原数据目录重新启动服务，从本次启动输出取得新的初始化码，在网页设置新密码。Compose 可用 `docker compose logs` 查看本次启动日志；旧日志可能仍包含之前的初始化码。
5. 登录网页后，按需生成新的 API Key，并更新调用程序。**旧 API Key 和所有旧登录会话失效**；新 Key 仅在生成时显示一次。协议身份、联系人快照和运行记录不受此操作影响。

如果在首次初始化中断后只剩一个认证文件，先停止服务并备份现有认证文件，再确保这两个文件均已移出数据目录，按上述步骤重新初始化。不要在服务运行时修改认证文件；若需要恢复旧认证备份，应先停止服务，并同时恢复这两个文件。

### Docker Compose 部署（Linux）

仓库根目录的 `Dockerfile` 只静态构建 `cmd/nwtweb`，不构建托盘程序。运行镜像使用 `scratch`，没有shell、包管理器或Windows客户端；Go程序直接作为入口接收SIGTERM，默认以root运行，镜像默认参数为 `-data-dir /data`。`.dockerignore` 继续使用严格允许列表，不包含本地资料、Git或研究证据。

`compose.yaml` 使用Linux Docker Engine的 `network_mode: host`，自动读取宿主机网络；UDP 9011/9012和HTTP 8788直接占用宿主机端口。不使用 `ports:`、`privileged` 或额外capability。bridge的容器地址、NAT和广播不适合直接套用当前实现；不以Docker Desktop网络行为作为验收依据。

**旧部署先保留原资料卷**：继续将原命名卷挂到 `/data`，例如恢复 `nwtweb-data:/data` 并保持原Compose项目名；原命名卷声明仍保留。不要直接使用空 `./data`，否则原身份和日志不会出现在新目录中。不删除或自动迁移旧卷，也不以换卷方式重试失败或未知请求。

新部署默认使用 `./data:/data`，只需按需修改左侧宿主文件夹。不需要 `command`、环境变量或 `.env`。最短启动命令：

```bash
docker compose up -d --build
```

默认只识别本机网络并提供离线网页，不发协议探测包。Linux优先使用默认IPv4路由网卡的私有IPv4及真实掩码；没有合适默认网卡时仅接受唯一局域网IPv4，多IP有歧义则报错要求 `-local`，不猜 `/24`。本机或局域网浏览器打开 `http://实际宿主IP:8788`；默认监听 `0.0.0.0:8788`，但 `0.0.0.0` 不是访问地址。点击“上线”后才发送三次子网定向广播并连接正常发现的客户端。

可用 `docker compose config --quiet` 只解析配置、`docker compose logs -f` 查看日志、`docker compose stop` 正常停止并保留资料。构建需要取得 `golang:1.27.1-alpine` 基础镜像；运行镜像为本地 `nwtweb:local`，不发布到镜像仓库。`restart: "no"`，不自动重启；停止等待20秒，大于程序10秒的总退出预算。

可选覆盖时再添加服务的 `command`；Compose会替换镜像CMD，因此必须保留 `-data-dir /data`，否则会改用直接Go运行时的相对默认目录。例如仅限制网页为本机访问：

```yaml
    command: ["-data-dir", "/data", "-listen", "127.0.0.1:8788"]
```

`-local` 可选本机裸IP读取真实掩码，或用CIDR、旧IP加 `-prefix` 显式覆盖；不能用 `0.0.0.0` 代替协议本机IP。`-name` 可覆盖默认 `NWT-Go-Web`，也可在网页离线时更改本次上线名称。`-listen` 只改变网页访问，不自动上线；非Linux的直接Go运行仅支持唯一私有IPv4自动识别。完整参数见第2节。修改Compose参数后用 `docker compose up -d --build` 更新容器，单纯 `restart` 不会应用新参数。

**目录权限**：root运行通常可以直接写新挂载目录，不自动chown已有宿主目录。Go新建目录0700、新文件0600，已有目录保留其权限和归属；只明确上线时才生成身份。重建、重启继续使用同一资料目录，身份损坏或写入失败直接报错，不改用临时身份。默认 `/data/` 已加入Git忽略，其他自定义宿主目录也应避免提交。镜像没有shell，运行错误看日志，协议记录保存在挂载目录的 `session-*.jsonl`。

网页使用密码登录，API使用密钥；HTTP不加密，局域网内的凭据可能被截获。Host按TCP实际到达的本机IP和端口检查，Origin仍须同源。访问失败先核对日志地址及宿主机TCP 8788防火墙规则，不自动调整防火墙。真实上线仍须明确身份、对端或子网范围及时长，聊天另需目标、完整正文与次数；多成员群仅经合成/loopback验证，尚未真实验收。

## 4. 独立身份和记录

数据目录使用专属于此 Web 节点的文件：

- `identity`：自己的32字节持久身份；首次明确上线时生成。后续上线和进程重启复用，损坏不重置。
- `session-<UTC纳秒时间>-<随机值>.jsonl`：每次明确上线的新记录。以独占创建方式打开，逐条 Sync 后继续；碰撞或写入失败停止，不换名称重试。
- `directory-<同次运行标识>-<递增序号>.json`：每个新联系人双向 READY 后，将合并后的完整目录保存为一份新快照。回调串行执行，成功保存后才发布，不让后一个连接覆盖前一个联系人；保留旧快照。进程启动读取文件名排序最新的一份，损坏时不回退。早期无序号的目录快照仍可正常读取。

记录保存运行阶段、群快照、联系人资料、发送目标/正文 SHA-256、正文 UTF-8 字节数，以及请求与消息编号的对应结果，**不保存聊天正文**。资料和日志仍属于私有数据；建议放在已忽略的 `local/nativeclient/` 子目录，不提交 Git。

群成员不从旧运行恢复为可发送状态；新会话必须重新正常同步。成员版本变化时撤销旧快照，收到不属于本机的结果时移除群，网页随状态轮询更新。

## 5. HTTP 接口

新增公开接口沿用远端main的 `/health`、`/contacts`、`/groups`、`/send/user`、`/send/group`，支持原始ID或唯一精确名称。请求格式及原生结果见[原生API说明](native-api.md)，Swagger UI为 `/docs`，OpenAPI为 `/openapi.json`。公开发送由服务端生成请求标识，每次HTTP调用是新请求；下列原网页发送仍由调用方提供标识，两者不要混淆。

网页接口和文档需要登录会话，公开API可使用API Key。下面保留原网页接口说明：全部接口同源，状态默认每2秒查询。只读接口：

| 方法与路径 | 返回 |
|---|---|
| `GET /api/state` | 阶段、错误、身份、联系人、群、最近128条阶段及正文预算 |
| `GET /api/contacts` | `contacts` 与 `complete:false`，不冒充局域网全部名册 |
| `GET /api/groups` | `groups` 与 `complete:false`，不冒充全部已加入群 |

网页写入接口使用登录会话，并带 `Content-Type: application/json`；不要求 `X-NWT-Request`，拒绝未知/重复/缺失字段、尾随 JSON、非字符串、非法 UTF-8 及超预算请求体。

```text
POST /api/session/start   {"name":"自定义本机名称"}  # name可省略；{}沿用当前名称
POST /api/session/stop    {}
POST /api/messages/user  {"request_id":"浏览器生成的新随机标识","user_id":"原始联系人ID","text":"完整正文"}
POST /api/messages/group {"request_id":"浏览器生成的新随机标识","group_id":"原始群ID","text":"完整正文"}
```

名称只在离线/失败后上线时更改，在线或退出中请求返回409。空名、全空白、换行、控制字符或非法UTF-8返回400，不创建身份或记录、不打开socket；合法名称的原始空格保留。名称进入本次1000资料，不改变本机UserID、身份文件和联系人，也不触发入群。

`request_id` 为8..128个 ASCII 字母、数字、连字符或下划线，与原生 `MSG_ID` 不同。同一次后台会话中，相同标识及相同目标/正文只调用协议一次；再次提交返回原结果，内容不同则409拒绝。此去重范围不跨重新上线或进程重启；不要把旧请求换新标识或在新会话中补发。

每会话最多接收1024个不同发送标识；达到预算后拒绝新请求，不淘汰旧标识后把重复请求当成新发送。完整事实保留在私有日志。浏览器每次明确发送生成一个32字符随机标识，发送期间锁定提交，任何 POST 失败都不自动重试或跟随重定向。

正文为1..4096 UTF-8字节，实际换行原样保留。该值是原生模块的实现预算，不是厂商上限，也不表示所有长度已真实验收。这里不做 IPC 的字面 `\\n` 转换。

HTTP受理后的发送使用后台20秒时限；关闭网页不会取消它。关闭后台会中断尚未完成的发送，仍保留可能已提交的事实。发送摘要写入或 Sync 失败时不提交；结果记录失败时停止会话，不把未知改写为成功。

返回格式：

```json
{
  "request_id": "原网页请求标识",
  "result": {
    "command": 1004,
    "message_id": "本条原生编号",
    "submitted": true,
    "transport_acknowledged": true,
    "business_acknowledged": false,
    "receipt_received": false,
    "delivery": "unknown"
  },
  "error": ""
}
```

HTTP200表示至少一个可用成员且全部可用路由传输确认，不是独立接收或整群成功。群结果另有可省略的 `recipients`，每项包含 `user_id`、`peer_index`、`skipped`、`submitted`、`transport_acknowledged` 和 `error`；`skipped`与`error`始终输出，无错误时error为空字符串。计划时无就绪连接或能力不支持的成员skipped=true，submitted与transport_acknowledged均false，error说明原因，无就绪连接时peer_index=0；群快照等计划前检查失败不含该字段，全体不可用时可返回全体skipped。顶层 `submitted` 表示任一成员Queue接受，`transport_acknowledged` 仅完整名单全部成员ACK时为true，有skipped仍false；群 `business_acknowledged` / `receipt_received` 恒false，`delivery` 恒unknown。非2xx仍保留逐成员事实，部分提交不可重新发送整群，不能把HTTP错误当成未发送。网页在全部非skipped路由确认且至少一个时清除本次草稿，显示“在线成员传输已确认，另有N名成员未投递”和逐成员原因，不自动补发；HTTP错误或实际路由失败保留草稿和警告；单个3011成员事件不会修改群聚合结果，事件仅按本次会话及消息编号回填1004单聊记录：1004对应传输确认、1005对应业务ACK、1006对应协议回执，也不称为已读。

## 6. 实现位置

- `internal/nativeproto/client_snapshot.go`：并发安全深拷贝快照，显式启用的正常握手联系人学习；`OnlineUserIDs`保存实际就绪身份。
- `internal/nativeproto/host*.go`、`packet_conn.go`：一个身份、两个真实socket下的多对端正常发现、包分派、独立会话、联系人合并和正常退出。
- `internal/nativeweb`：配置、会话生命周期、私有记录、请求去重和 HTTP。
- `internal/nativeweb/assets/`：内嵌 HTML、CSS、JavaScript；不依赖 CDN 或 Node 运行时。
- `cmd/nwtweb`：参数、本机网络识别、HTTP监听、信号和正常退出。

`nwtclient`、`nwtnode` 仅保留作历史协议实验入口，不作为 Web 的运行模式；原 IPC 模块不替换。

## 7. 三客户端真实验证

2026-10-25经另行明确授权完成共享Host真实验证，HTTP曾同时返回3个联系人、3个在线，三个不同连接完成双向READY。原厂响应公告IP为0.0.0.0，已按实际UDP来源建连，并保留非零地址不符等拒绝规则。成功会话170.209秒后自动正常下线，`graceful_exit=true`、`chat_sends=0`；HTTP继续运行，三个联系人保留但标为离线。

第一次失败和一次诊断记录全部保留，最终复验没有改写它们；依据与授权顺序见[三客户端报告](../research/reports/native-protocol/native-web-three-contacts-20261025.md)。这不是聊天送达或用户UI验收，后续真实连接不沿用此次验证授权。

## 8. 开发验证

默认仅使用合成资料、内存 HTTP 和 loopback 网络，不加载真实身份、联系人或客户端资料。

### 2026-10-27：全仓隔离验证

在本机 Docker 隔离环境中，Linux amd64 和 386 的全仓 `go test -count=1 -timeout 45s ./...` 与 `go vet ./...` 均通过，包含 `cmd/nwttray` 的编译；该包没有测试函数。amd64 的上游托盘库编译出现废弃 API 警告，不影响通过。测试禁用容器外网、只读挂载源码，使用合成和 loopback 数据，没有连接真实客户端或发送消息。此前各次宿主机验证失败记录保留在下方，不代表本次隔离验证失败。

本机保留了 `nwtweb-test-deps:local`（Debian 13 amd64）和 `nwtweb-test-deps:386`（Debian 12 i386）镜像，以及 `nwtweb-test-go-modcache` 模块缓存卷；镜像安装了 `pkgconf` 和 `libayatana-appindicator3-dev`，386 镜像原生启用 CGO。这些资源只存在于当前 Docker 环境，不属于仓库。宿主机没有安装这些托盘开发依赖，直接在宿主机运行全仓测试仍可能遇到原有依赖错误。

在项目根目录可重复执行（源码只读、禁用外网；缓存卷须已包含当前 Go 依赖）：

```bash
for image in nwtweb-test-deps:local nwtweb-test-deps:386; do
  docker run --network none \
    --mount "type=bind,src=$PWD,dst=/src,readonly" \
    --mount type=volume,src=nwtweb-test-go-modcache,dst=/go/pkg/mod \
    -e GOPROXY=off -e ENET_REFERENCE_BINARY= \
    "$image" sh -c 'go test -count=1 -timeout 45s ./... && go vet ./...'
done
```

386 镜像本身是 `linux/386`；如 Docker 没有自动选择该架构，运行对应镜像时加 `--platform linux/386`。依赖变更后需先为缓存卷下载新依赖，再断网运行测试。历史验证只说明当时结果；本次通过不替代后续代码修改后的重新测试。

```bash
ENET_REFERENCE_BINARY= go test -count=1 -timeout 45s ./internal/nativeweb ./internal/nativeproto ./cmd/nwtweb ./cmd/nwtclient ./cmd/nwtnode
ENET_REFERENCE_BINARY= GOARCH=386 go test -count=1 -timeout 45s ./internal/nativeweb ./internal/nativeproto ./cmd/nwtweb ./cmd/nwtclient ./cmd/nwtnode
ENET_REFERENCE_BINARY= go test -race -count=3 -timeout 45s ./internal/nativeweb ./internal/nativeproto ./cmd/nwtweb ./cmd/nwtclient ./cmd/nwtnode
```

2026-10-25 验证结果：

- Linux amd64/386全部非托盘包测试与 vet 通过。
- 新 Web 后台、协议与常驻/限时入口 race 连续3次通过。
- Windows amd64/386全仓交叉构建与 vet 通过；没有在 Windows 实际运行本程序。
- 真实模块间 loopback 测试覆盖首次身份、双向 READY、联系人持久化、单聊一次提交与对端收到一次1004、正常退出及重启恢复。此集成测试不暴露入站正文；完整正文和群3011由已有原生模块合成用例验证，不冒充 Web 集成的独立真实接收。
- 实际命令 HTTP 验证了页面资源、离线状态、上线及 starting 阶段下线；只使用127.0.0.1/127.0.0.2，没有真实对端。
- 全仓 Linux 测试/vet 已执行但未全部通过：原有 `cmd/nwttray` 在 amd64 缺少 `pkg-config`，386缺少 systray 平台符号；未修改托盘或安装系统依赖。
- JavaScript语法、合成 DOM/接口行为，以及真实 Chrome 桌面1440px/手机390px检查通过。浏览器覆盖实际 HTTP 上下线、单聊/群聊合成接口、每目标草稿、精确换行/emoji、UTF-8限额、搜索与HTML文本展示、失败不补发；无横向溢出或脚本异常，截图已检查。
- 浏览器检查发现 starting 阶段人工下线被 `errors.Join` 包装的取消错误误记为失败，已修复并加入真实协议回归；同时发生的清理错误仍报告为失败。

可重复的浏览器检查脚本位于 `tools/nwtweb-browser-check.mjs`。无需浏览器、服务器或socket的合成DOM模式执行真实 `app.js`，覆盖全部成员确认、在线加离线成员时HTTP200且整群ACK为false仍清稿、全体离线禁用且不发送、HTTP502或实际失败保留草稿、单peer ACK不修改聚合结果及单聊事件回填：

```bash
node --check internal/nativeweb/assets/app.js
node --check tools/nwtweb-browser-check.mjs
node tools/nwtweb-browser-check.mjs --synthetic-only
```

实际浏览器检查先启动上述**合成loopback配置**的 `nwtweb`，再使用已安装的 Playwright：

```bash
PLAYWRIGHT_MODULE=/path/to/playwright \
  node tools/nwtweb-browser-check.mjs http://127.0.0.1:18788 /tmp/nwtweb-screenshots
```

实际浏览器模式要求后台发现子网完全位于127.0.0.0/8内，聊天接口在浏览器内被合成响应接管；不用于真实环境验收。当前root环境经用户明确批准后临时设置 `NWT_BROWSER_NO_SANDBOX=1` 才运行 Chrome；脚本默认启用sandbox，不修改 MCP 或浏览器的永久配置。

初版开发验证只使用合成及loopback；后续三客户端真实发现与正常退出另经授权完成，见第7节。该真实验证没有提交聊天，未安装开机服务、commit 或 push。后续真实连接仍需明确身份、范围与时长；聊天另需目标、完整正文和次数。

### 2026-10-26：简化参数与容器验证（当时版本）

以下保留当时需显式本机地址、回环监听、非root及命名卷版本的验证记录，不代表当前自动识别、通配监听、root与目录挂载版本已完成容器运行验收。

- 新增 CIDR 解析、旧参数、子网冲突、默认名称/目录、相对路径、损坏身份拒绝及 `-check -online` 不产生协议资料的回归测试；Linux amd64/386通过。
- Linux amd64/386全仓测试和 vet 均已执行；本次相关包及其余非托盘包通过，原有 `cmd/nwttray` 仍因 amd64 缺少 `pkg-config`、386缺少 systray 平台符号而失败。Web/协议包 race 连续3次、Windows amd64/386全仓交叉构建和 vet 通过，未在 Windows 实际运行。
- 实际 Go 程序以最短参数启动，HTTP 页面、Swagger、离线状态、默认名称及 SIGTERM 退出0通过；默认目录未创建。
- Compose 配置解析、`NWT_LOCAL` 缺失/空值拒绝、host 网络、命名卷、20秒停止预算和默认不自动上线检查通过。
- 完整多阶段镜像构建尝试在下载 `golang:1.27.1-alpine` 的 Alpine 层时持续停滞，已停止，**完整构建未完成**。随后使用本机 `CGO_ENABLED=0` 静态程序及与 Dockerfile 相同的最终运行阶段，构建独立验证镜像，未将其标记为正式 `nwtweb:local`。
- 验证镜像通过 Compose 的 host 网络与命名卷运行，非 root 离线网页、页面/Swagger、仅向合成 `127.0.0.2` 接收器单播发现、首次身份写入、停止后退出0及重启身份不变均通过；没有连接真实客户端、广播或发送聊天。初次发现 COPY 新建目录为0755，已在最终阶段显式指定 `--chmod=0700`，重建验证 `/data` 为 UID/GID `10001:10001`、0700，身份与记录文件0600。
- 合成验证的容器均已停止，资料卷和失败检查结果保留；没有安装生产服务或自动重启配置。验证镜像不代替完整构建验收，也不证明真实子网发现或聊天送达。

### 2026-10-26：统一为默认子网发现

- Web配置和命令仅保留本机网络范围，不再提供目标IP或发现模式选择；会话始终创建共享Host。Compose、网页初始文案及Swagger规范同步使用子网发现，网页在没有已就绪联系人时显示“等待客户端”。
- 默认Host与合成对端通过正常公告建立连接，首次身份、联系人持久化、单聊及正常退出的集成测试通过；没有使用指定目标的配置或替换会话工厂。两种架构全仓测试与vet已执行，除既有托盘依赖问题外其余包通过；相关包race连续3次、Windows两种架构全仓交叉构建与vet通过。
- 实际命令只传本机CIDR及测试用临时端口启动，先验证离线网页未创建资料，再通过HTTP上线，在 `127.0.0.0/8` 合成范围内完成一次定向广播、Host就绪及正常下线，`chat_sends=0`，SIGTERM退出0。没有真实客户端连接、真实子网广播或聊天。
- 页面与浏览器验证脚本的JavaScript语法检查通过，OpenAPI枚举仅保留 `subnet`；该次未运行真实浏览器或重新完成完整多阶段镜像构建。

### 2026-10-26：自动网络识别与无参数启动

- 新增合成网卡/路由测试，验证最低metric默认路由、默认网卡优先于Docker网桥、读取真实 `/20` 等掩码、裸IP补掩码、手工CIDR覆盖及多候选明确报错。自动识别不向网络对端探测，不会因识别失败猜测地址或自动开始协议会话。
- 默认监听全部IPv4网卡，Host校验使用实际TCP本机地址；合成多网卡、错误Host/Origin及真实loopback HTTP回归通过。省略 `-local` 的实际程序识别本机子网，离线网页、联系人/群接口、Swagger及SIGTERM退出0通过，未生成身份或运行记录。
- Linux amd64/386非托盘包测试和vet通过，协议/Web/入口race连续3次通过；Windows amd64/386全仓交叉构建和vet通过。全仓Linux测试/vet已执行，仍仅受既有托盘依赖阻塞：amd64缺少 `pkg-config`、386缺少systray平台符号。
- 完整Dockerfile多阶段构建本次已通过，验证镜像为 `nwtweb:auto-network-check`，没有覆盖现有服务镜像。容器使用host网络与临时bind目录，默认root及镜像CMD `-data-dir /data` 已核对；仅覆盖网页为回环临时端口运行，未传 `-local` 即识别宿主子网，离线网页/API/Swagger、无身份资料创建及正常停止退出0通过。
- 未替换或重启现有服务，测试容器正常停止后保留；本次未进行真实协议上线、广播或聊天，未运行真实浏览器。
