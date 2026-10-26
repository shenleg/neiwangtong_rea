# nwtweb：独立原生协议 Web 客户端

`cmd/nwtweb` 将已有的常驻原生协议客户端接到网页：后台维护连接，网页控制上线、下线、查看联系人与已同步的已加入群，并提交单聊或群聊。无需 Windows 客户端、IPC、前端构建工具或外部静态资源。

## 1. 本版功能与范围

| 功能 | 行为 |
|---|---|
| 后台常驻 | 上线后没有固定运行时限；关闭、隐藏或刷新网页不使后台下线 |
| 联系人 | 双向 READY 后从当前对端正常握手取得 UserID、名称与实际发现端点，保存为本独立身份的联系人快照 |
| 已加入群 | 展示本次正常群同步确认本机属于其中的完整成员快照；不是全部群的穷尽列表 |
| 单聊 | 从联系人列表选择原始 UserID，复用同一连接提交1004 |
| 群聊 | 从已同步群选择原始 GroupID，检查完整成员后提交3011 |
| 结果 | 分别展示提交、传输确认、业务 ACK、协议回执；送达状态仍为 unknown |
| 退出 | 网页下线或 SIGINT/SIGTERM 请求正常1017及传输断开；SIGKILL无法执行正常退出 |

默认仍使用显式 `-remote` 单对端；新增 **`-discover` 本子网正常发现与多对端连接**。多个客户端共享同一个独立身份和同一组发现/ENet端口，每个已就绪身份分别显示在线并可选为单聊目标。只发送一次标准定向广播，并接受本子网的正常发现包，不枚举地址、不扫描、不自动重连失败连接。

群聊仍仅支持完整成员恰为本机与一个已连接身份的群，按该成员选择连接；多成员群投递尚未实现。网页禁用不能发送的目标，协议层仍检查实际身份与完整成员。不主动入群。

本版只显示此次网页会话主动发送的消息，不接收显示对方聊天，不读取聊天历史；刷新页面后本页正文和草稿不保留。联系人与群名均按文本展示，不作为 HTML 渲染。

## 2. 构建和只读校验

```bash
go build -o /absolute/private/nwtweb ./cmd/nwtweb

# 合成参数，只读校验，不是连接真实对端的授权。
/absolute/private/nwtweb -check \
  -listen 127.0.0.1:8788 \
  -local 127.0.0.1 -prefix 127.0.0.0/8 \
  -remote 127.0.0.2:9011 -name Synthetic-Web \
  -data-dir /absolute/private/web-node
```

`-check` 不创建目录、身份、记录或 socket；同时设置 `-online` 也只校验。已有身份或最新联系人快照损坏时直接报错，不重建身份或静默使用旧联系人。

### 正常启动

去掉 `-check` 后，程序**默认仅启动 HTTP，仍未连接协议对端**。访问 `http://127.0.0.1:8788`，点击“上线”才开始一次常驻连接。连接失败后网页保留错误，程序继续提供网页，不自动重连。再次上线须明确点击按钮。

- `-local`：明确本机 IPv4。
- `-prefix`：包含本机的 IPv4 子网，不窄于 `/30`；默认只计算公告字段。`-discover`时同时限制发现来源与定向广播范围。
- `-remote`：默认模式的唯一 IPv4 单播发现端点；`-discover`时可选，作为明确的额外单播种子，不扩展子网范围。
- `-discover`：显式启用本子网一次标准广播及多对端共享Host，最大32个已观察身份，不扫描或重连。
- `-session-duration`：每次协议上线的活动时限，默认0表示常驻。正值到期正常下线，HTTP继续运行；适用于有明确时长的真实验证。
- `-name`：本独立节点的默认显示名称。网页离线时可在“上线名称”输入框修改，再点击上线；也可使用 `POST /api/session/start {"name":"自定义名称"}`。成功受理后本进程记住新名称，重启仍使用CLI配置。
- `-data-dir`：私有绝对目录；明确上线时才创建，目录创建权限0700，新文件0600。
- `-discovery-port` / `-enet-port`：默认9011/9012；实际端口冲突即失败，不自动更换。
- `-listen`：默认 `127.0.0.1:8788`，默认只接受 IPv4 回环地址。可用端口0分配临时网页端口，实际地址输出到终端。
- `-lan`：显式允许监听一个明确的私有IPv4地址；不能监听 `0.0.0.0` 或公网地址。初版按要求移除鉴权，页面、API和Swagger都不需要用户名、密码、token，不再生成临时密码。HTTP不加密，仅供可信内网。
- `-online`：操作者显式要求程序启动后立即上线；默认不启用。失败仍不自动重连。

默认单对端上线只有一次明确单播发现请求，发现最多10秒。`-discover`模式由共享Host发送一次子网定向广播，并对正常发现的每个身份建立独立连接；各连接的ENet与应用就绪各有15秒预算。一个对端失败不会将其他对端标成离线，错误带连接序号保留在阶段记录中。常驻阶段维护各自的ENet与1016心跳；退出时并发正常关闭各连接，正常退出清理预算5秒，命令总退出预算10秒。

多对端模式的 `protocol_ready` / 后台在线表示Host已准备接收发现，不等于所有客户端完成握手；真正可发送的联系人以各身份双向READY后发布的 `OnlineUserIDs` 为准。尚无客户端就绪时网页显示“等待客户端”。

### 服务器上的网页访问

默认本机模式可使用 SSH 转发，并保持浏览器地址与后台监听端口一致：

```bash
ssh -N -L 8788:127.0.0.1:8788 user@server
```

随后在本机浏览器访问 `http://127.0.0.1:8788`。后台校验 Host、同源 Origin 和JSON类型，拒绝跨站请求和 DNS 重绑定，不要求自定义请求头；不信任代理转发头。页面设置 CSP、禁止嵌入及 `no-store`。

### 局域网体验

显式传入 `-lan -listen 私有IPv4:8788`，浏览器直接打开启动输出的地址，无登录框或临时密码。该选项只改变网页访问，不隐含协议上线或聊天发送；不设置 `-online` 时仍等待网页操作。不要公开转发此端口；需要远程或不可信网络访问时使用SSH。

浏览器使用 `crypto.getRandomValues` 生成32字符随机请求标识，支持局域网HTTP，不依赖仅安全上下文可用的 `randomUUID`，也不使用 `Math.random`。

## 3. 后台进程

程序不自行 fork，也不把网络运行绑定到网页连接。可由终端、`nohup` 或进程管理器保持进程。下面仍是合成配置格式；实际部署必须替换为已确认的本机、身份目录和对端。

```bash
nohup /absolute/private/nwtweb \
  -listen 127.0.0.1:8788 \
  -local 127.0.0.1 -prefix 127.0.0.0/8 \
  -remote 127.0.0.2:9011 -name Synthetic-Web \
  -data-dir /absolute/private/web-node \
  > /absolute/private/nwtweb-output.log 2>&1 &
```

该命令只启动离线网页；操作者在页面点击上线。需要启动即上线时额外显式传入 `-online`。结束自己的进程使用 SIGTERM，不用 SIGKILL 验证正常退出。不配置自动重启后上线或消息重放。

局域网体验以独立进程运行；未安装 systemd 服务、设置开机启动或自动重连。

## 4. 独立身份和记录

数据目录使用专属于此 Web 节点的文件：

- `identity`：自己的32字节持久身份；首次明确上线时生成。后续上线和进程重启复用，损坏不重置。
- `session-<UTC纳秒时间>-<随机值>.jsonl`：每次明确上线的新记录。以独占创建方式打开，逐条 Sync 后继续；碰撞或写入失败停止，不换名称重试。
- `directory-<同次运行标识>-<递增序号>.json`：每个新联系人双向 READY 后，将合并后的完整目录保存为一份新快照。回调串行执行，成功保存后才发布，不让后一个连接覆盖前一个联系人；保留旧快照。进程启动读取文件名排序最新的一份，损坏时不回退。早期无序号的单对端快照仍可正常读取。

记录保存运行阶段、群快照、联系人资料、发送目标/正文 SHA-256、正文 UTF-8 字节数，以及请求与消息编号的对应结果，**不保存聊天正文**。资料和日志仍属于私有数据；建议放在已忽略的 `local/nativeclient/` 子目录，不提交 Git。

群成员不从旧运行恢复为可发送状态；新会话必须重新正常同步。成员版本变化时撤销旧快照，收到不属于本机的结果时移除群，网页随状态轮询更新。

## 5. HTTP 接口

新增公开接口沿用远端main的 `/health`、`/contacts`、`/groups`、`/send/user`、`/send/group`，支持原始ID或唯一精确名称。请求格式及原生结果见[原生API说明](native-api.md)，Swagger UI为 `/docs`，OpenAPI为 `/openapi.json`。公开发送由服务端生成请求标识，每次HTTP调用是新请求；下列原网页发送仍由调用方提供标识，两者不要混淆。

所有网页、API和文档均无鉴权。下面保留原网页接口说明：全部接口同源，状态默认每2秒查询。只读接口：

| 方法与路径 | 返回 |
|---|---|
| `GET /api/state` | 阶段、错误、身份、联系人、群、最近128条阶段及正文预算 |
| `GET /api/contacts` | `contacts` 与 `complete:false`，不冒充局域网全部名册 |
| `GET /api/groups` | `groups` 与 `complete:false`，不冒充全部已加入群 |

写入接口只需带 `Content-Type: application/json`，不要求 `X-NWT-Request` 或鉴权头，拒绝未知/重复/缺失字段、尾随 JSON、非字符串、非法 UTF-8 及超预算请求体。

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

成功表示传输确认，不是独立接收。发送失败返回非2xx时也可能已经提交，响应继续带本条编号、`submitted` 和 `unknown`；网页显示“结果未知，请先确认，勿重复发送”。晚到的1005/1006按本次会话与消息编号展示，不把它们归为群消息确认，也不称为已读。

## 6. 实现位置

- `internal/nativeproto/client_snapshot.go`：并发安全深拷贝快照，显式启用的正常握手联系人学习；`OnlineUserIDs`保存实际就绪身份。
- `internal/nativeproto/host*.go`、`packet_conn.go`：一个身份、两个真实socket下的多对端正常发现、包分派、独立会话、联系人合并和正常退出。
- `internal/nativeweb`：配置、会话生命周期、私有记录、请求去重和 HTTP。
- `internal/nativeweb/assets/`：内嵌 HTML、CSS、JavaScript；不依赖 CDN 或 Node 运行时。
- `cmd/nwtweb`：参数、回环监听、信号和正常退出。

原有 `nwtclient`、`nwtnode` 默认行为保持不变，原 IPC 模块不替换。

## 7. 三客户端真实验证

2026-10-25经另行明确授权完成共享Host真实验证，HTTP曾同时返回3个联系人、3个在线，三个不同连接完成双向READY。原厂响应公告IP为0.0.0.0，已按实际UDP来源建连，并保留非零地址不符等拒绝规则。成功会话170.209秒后自动正常下线，`graceful_exit=true`、`chat_sends=0`；HTTP继续运行，三个联系人保留但标为离线。

第一次失败和一次诊断记录全部保留，最终复验没有改写它们；依据与授权顺序见[三客户端报告](../research/reports/native-protocol/native-web-three-contacts-20261025.md)。这不是聊天送达或用户UI验收，后续真实连接不沿用此次验证授权。

## 8. 开发验证

默认仅使用合成资料、内存 HTTP 和 loopback 网络，不加载真实身份、联系人或客户端资料。

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

可重复的浏览器检查脚本位于 `tools/nwtweb-browser-check.mjs`。先启动上述**合成loopback配置**的 `nwtweb`，再使用已安装的 Playwright：

```bash
PLAYWRIGHT_MODULE=/path/to/playwright \
  node tools/nwtweb-browser-check.mjs http://127.0.0.1:18788 /tmp/nwtweb-screenshots
```

脚本要求实际后台对端为127.*，聊天接口在浏览器内被合成响应接管；不用于真实环境验收。当前root环境经用户明确批准后临时设置 `NWT_BROWSER_NO_SANDBOX=1` 才运行 Chrome；脚本默认启用sandbox，不修改 MCP 或浏览器的永久配置。

初版开发验证只使用合成及loopback；后续三客户端真实发现与正常退出另经授权完成，见第7节。该真实验证没有提交聊天，未安装开机服务、commit 或 push。后续真实连接仍需明确身份、范围与时长；聊天另需目标、完整正文和次数。
