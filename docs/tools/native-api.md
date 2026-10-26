# 原生消息网关 HTTP API

本文说明 `cmd/nwtweb` 的公开调用接口。接口参考开发时更新后的远端 `main`：`c4a88cc` 的 `internal/api/api.go`，保留 `/health`、`/contacts`、`/groups`、`/send/user`、`/send/group` 和精确名称解析方式，底层使用已有原生协议后台，不调用 Windows IPC。

**初版无任何鉴权**：网页、API、Swagger 均不需要用户名、密码、token 或 `Authorization`，也不需要 `X-NWT-Request`。JSON 写入请求只需 `Content-Type: application/json`。仍保留 Host/Origin 检查、明确监听地址和输入校验；它们不是登录或凭据校验。

## 1. 启动与文档

沿用 [nwtweb 启动参数](nwtweb.md)。默认只启动 HTTP，协议仍离线；可使用网页或会话接口明确上线，启动即上线则另行显式指定 `-online`。API 与网页共用一个后台身份、联系人快照及连接，不会每发一条消息重新握手。

假设服务地址为 `http://127.0.0.1:8788`：

- 聊天页面：`/`
- Swagger UI：`/docs`，也支持常用路径 `/swagger/index.html`
- OpenAPI 3.0.3：`/openapi.json`

官方 Swagger UI 5.34.0 的脚本、样式和许可证随 Go 二进制内嵌；无外部 CDN、Petstore、字体或在线校验请求，详细来源及哈希见 `internal/nativeweb/assets/swagger/SOURCE.txt`。访问文档只加载规范，不自动上线、不自动提交消息。`Try it out` 后明确点击 `Execute` 才调用接口。

局域网仍须显式 `-lan -listen 私有IPv4:端口`。无鉴权也无 TLS，仅部署在受控内网；不要公开转发端口。旧进程不会因源码修改自行更新。

## 2. 与远端 main 的对应关系

| 项目 | 远端 main 的 IPC 接口 | 本原生接口 |
|---|---|---|
| 路径及目标选择 | `/send/user`、`/send/group`，ID或name二选一 | 沿用；拒绝混用ID、名称及两类目标ID |
| 列表及精确名称查询 | `/contacts`、`/groups`、`?name=` | 沿用；不去空格、不改大小写、不挑在线同名目标 |
| 名称结果 | 唯一200、未命中404、同名409并列候选 | 沿用 |
| 健康检查 | Windows客户端预检，可能包含PID/窗口 | 原生后台阶段、身份和联系人数量，不伪造PID或窗口 |
| 列表来源 | Windows本地数据库与文件摘要 | 本独立身份正常协议学习/同步快照，`source=native_protocol`，`complete=false` |
| 发送记录 | IPC `attempt_id` 及Win32调用事实 | 服务端新生成 `request_id` 与原生 `message_id`、提交及确认事实 |
| 正文换行 | IPC会转换字面 `\\n` / `\\r\\n` | 原生正文原样保留，仅按JSON自身规则解释转义 |

这是相同路由及请求用法的原生实现，不是冒充IPC响应结构的替代品。客户端应读取下文原生字段，不依赖 `preflight`、`call` 或 `attempt_reserved`。

## 3. 健康、联系人与群

### `GET /health`

后台 `phase=online` 返回200，其余阶段返回503。多对端Host已准备接受发现但尚无peer时也可 ready；`online_contacts` 才是当前完成双向READY的联系人数量。

```json
{
  "status": "ready",
  "phase": "online",
  "name": "Synthetic-Web",
  "own_user_id": "synthetic-own",
  "discovery_mode": "subnet",
  "online_contacts": 3,
  "contacts_count": 3,
  "groups_count": 1,
  "error": ""
}
```

该状态不是 Windows UI 或消息独立接收确认。

### `GET /contacts` 与 `GET /groups`

联系人返回：

```json
{
  "contacts": [
    {"user_id":"synthetic-peer","name":"合成联系人","discovery":"127.0.0.2:9011","online":true,"sendable":true,"reason":""}
  ],
  "complete": false,
  "own_user_id": "synthetic-own",
  "source": "native_protocol"
}
```

群响应使用 `groups`，每条包含 `group_id`、`name`、`master_id`、`info_version`、`user_version`、`members`、`sendable`、`reason`。

列表不是全部网络名册或全部已加入群。离线时仍可读取已知联系人；群只包含本次已同步快照，不从历史成员恢复可发送状态。当前群发送只支持完整成员恰为本机与一个已连接成员的群，不支持多成员投递，不主动入群。

### 精确名称查询

```bash
curl --get --data-urlencode 'name=合成联系人' \
  http://127.0.0.1:8788/contacts
```

- 唯一：200，`{"status":"unique","candidates":[唯一对象]}`。
- 未命中：404，`{"status":"not_found","error":"指定目标未命中"}`。
- 多个同名：409，`{"status":"ambiguous","error":"...","candidates":[全部同名对象]}`，调用方需明确使用原始ID。
- 省略或空 `name` 返回列表，与远端main一致；重复名称参数或错误查询编码返回400。

## 4. 发送接口

### `POST /send/user`

必须提供 `text`，并在 `user_id` 与 `name` 中只提供一个非空字符串。

```bash
# 合成格式示例，不是执行真实发送的授权。
curl http://127.0.0.1:8788/send/user \
  -H 'Content-Type: application/json' \
  --data '{"user_id":"synthetic-peer","text":"第一行\n第二行"}'
```

按名称：

```json
{"name":"合成联系人","text":"完整正文"}
```

### `POST /send/group`

必须提供 `text`，并在 `group_id` 与 `name` 中只提供一个非空字符串。

```json
{"group_id":"synthetic-group","text":"完整群消息"}
```

两种发送均拒绝未知、重复、非字符串字段、null、尾随JSON、两类目标混用及调用方 `request_id`。正文要求1..4096 UTF-8字节，实际换行原样保留，不截断、不拆分；该预算不是厂商上限或全部长度已真实验收。

### 请求与结果语义

公开 `/send/*` 沿用 main 的“每次调用都是新请求”行为：服务端生成请求标识，**不要自动重试**。重复HTTP调用即使目标和正文相同，也会生成新请求。接口不提供跨HTTP或跨进程的重复请求幂等保证。

正常返回200，原生结果为顶层扁平结构：

```json
{
  "request_id": "00112233445566778899aabbccddeeff",
  "command": 1004,
  "message_id": "1",
  "submitted": true,
  "transport_acknowledged": true,
  "business_acknowledged": false,
  "receipt_received": false,
  "delivery": "unknown"
}
```

- `submitted`：原生消息已进入本次Queue，不代表已显示。
- `transport_acknowledged`：传输确认，不代表业务接收。
- `business_acknowledged`、`receipt_received`：已观察到对应协议事件，不称为已读。
- `delivery`：始终 `unknown`，独立接收观察另外记录。

发送前先写入并Sync摘要，失败时不调用协议；受理后由后台管理发送，不因HTTP连接关闭而丢弃结果。提交后错误返回502时仍保留原请求标识、消息编号和 `submitted` 等事实，不能把错误当成“未发送”后补发。

| 状态码 | 含义 |
|---|---|
| 400 | JSON、目标选择、字段或正文非法 |
| 403 | Host或浏览器来源不符；不是鉴权失败 |
| 404 | 原始目标未知或名称未命中 |
| 409 | 同名歧义，或群当前不能完整投递 |
| 408 | 请求受理前取消 |
| 415 | 写入请求不是 `application/json` |
| 503 | 后台未就绪、目标离线或会话请求预算耗尽 |
| 502 | 协议操作失败，结果可能已提交，继续保留原生事实 |
| 500 | 随机源或记录I/O错误；已经取得的提交事实不能丢弃 |

## 5. 原网页会话接口

以下接口仍可调用，也全部不要求鉴权或自定义请求头：

- `GET /api/state`：完整网页状态。
- `GET /api/contacts`、`GET /api/groups`：原网页列表形状。
- `POST /api/session/start {"name":"自定义本机名称"}`：以所填显示名称上线；空对象 `{}` 沿用当前名称。只能在离线或上次失败后修改，在线/退出中返回409；空名、纯空白、控制字符或非法编码返回400，联网前拒绝，不静默使用旧名。只改显示名称，不更换独立身份或联系人；本进程记住新名称，重启后仍以 `-name` 配置为准。
- `POST /api/session/stop {}`：明确请求正常下线。两者返回202表示已受理，不代表网络操作完成。
- `POST /api/messages/user {"request_id":"调用方标识","user_id":"...","text":"..."}`。
- `POST /api/messages/group {"request_id":"调用方标识","group_id":"...","text":"..."}`。

原网页消息接口要求调用方标识，并在同一次后台会话内对相同标识、目标和正文只调用一次协议；与公开 `/send/*` 不同。两者共享Manager的记录、容量和未知结果处理，不创建第二套网络发送实现。

## 6. 验证

默认测试只使用合成资料和loopback，不读取真实客户端或现有私有身份：

```bash
ENET_REFERENCE_BINARY= go test -count=1 -timeout 45s ./internal/nativeweb ./cmd/nwtweb
ENET_REFERENCE_BINARY= GOARCH=386 go test -count=1 -timeout 45s ./internal/nativeweb ./cmd/nwtweb
ENET_REFERENCE_BINARY= go test -race -count=3 -timeout 45s ./internal/nativeweb ./cmd/nwtweb
```

真实Chrome验收通过显式开关运行，后台仍为Go合成会话，不向真实客户端发送：

```bash
NWT_SWAGGER_BROWSER=1 \
PLAYWRIGHT_MODULE=/absolute/path/to/playwright \
NWT_BROWSER_OUTPUT=/tmp/nwtweb-api-check \
go test -count=1 -timeout 110s ./internal/nativeweb \
  -run '^TestSwaggerBrowserInteraction$' -v
```

浏览器默认启用sandbox；当前root环境必须另经明确批准后，才可为本次检查临时设置 `NWT_BROWSER_NO_SANDBOX=1`。

已覆盖精确名称及歧义、两种目标发送、三联系人路由、无凭据与无自定义头访问、未知结果不补发、OpenAPI本地引用与官方资源校验，以及公开名称发送入口到真实Go协议模块的loopback集成。Swagger在真实Chrome中完成健康检查、单聊/群聊合成请求各一次、桌面/手机布局及返回聊天页面；无外部资源请求，没有自动POST。
