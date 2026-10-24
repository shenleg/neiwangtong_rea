# 统一发送 API（nwtagent / nwttray）

统一发送 API 有两个宿主：`cmd/nwtagent`（命令行）与 `cmd/nwttray`（Windows 托盘程序）。两者的 HTTP 层都在 `internal/api`、业务都在 `internal/agent`，不直接接触 HWND、WM_COPYDATA 或数据库文件；发送沿用 `internal/ipc` 的既定纪律——单次调用、不自动重试、每次发送独立日志（`%AppData%\neiwangtong-gateway\ipc-experiment\`）。

## 1. 构建与启动（命令行）

```bash
GOOS=windows GOARCH=386 go build -o nwtagent.exe ./cmd/nwtagent

./nwtagent.exe \
  -exe 'C:\Program Files (x86)\nwt3055\ShiYeLine.exe' \
  -nr 'C:\私有检查目录\nr-copy' \
  -password-file 'C:\私有检查目录\codec-password.bin' \
  -qrp 'C:\私有检查目录\qrp-copy'
```

## 2. 托盘程序（nwttray）

```bash
GOOS=windows GOARCH=386 go build -o nwttray.exe ./cmd/nwttray

./nwttray.exe \
  -exe 'C:\Program Files (x86)\nwt3055\ShiYeLine.exe' \
  -nr 'C:\私有检查目录\nr-copy' \
  -password-file 'C:\私有检查目录\codec-password.bin' \
  -qrp 'C:\私有检查目录\qrp-copy' [-autostart]
```

托盘提供：

- **图标**：绿色=客户端已连接，红色=未连接；提示文字含 API 监听状态，每 10 秒轮询一次客户端实例。
- **菜单**：「启动 API 服务 / 停止 API 服务（地址）」切换监听；「打开 API 文档」在浏览器打开内置接口说明页（服务未启动时会先启动）；「客户端状态：已连接（PID）/未连接」为只读显示；「退出」停止服务并退出程序。
- `-autostart`：启动后立即开始提供 API 服务，不等待托盘菜单。

## 3. 公共参数说明

- 监听地址默认 `127.0.0.1:8787`（仅本机回环），用 `-addr` 修改。
- `-nr`/`-qrp` **推荐直接指向客户端正在使用的实时数据库文件**（`<EXE目录>\Syl\cache\db\nr` 与 `<EXE目录>\Syl\data\qrp`）：Agent 按文件 mtime 缓存，客户端写盘后下一次请求自动加载新数据（实测建群后立即可查）。需要审计级一致性时改用[退出后副本](contactid-runbook.md)，替换副本文件后同样自动生效。
- 实时文件模式下读取发生在客户端运行期间：存在 `-journal` 等边文件的事务瞬间会明确失败（不重试、不修复），下一次请求恢复；结构异常同样明确报错。每次加载完成后做**读后复核**（mtime 与边文件复查）：读取期间客户端恰好开始或完成写事务时，本次结果作废并返回 503（`ErrConcurrentWrite`），立即重试即可，不会缓存可疑数据。
- 口令文件内容必须是 codec 口令的精确字节（读至 EOF 不去换行，末尾勿附加换行）；口令只在内存使用。

## 4. API

### GET /docs（内置接口文档）
自包含的 Swagger 风格说明页（无外部资源，随服务分发）：端点清单、参数表、请求/响应示例与错误码，每个端点带"试一下"按钮同源调用真实接口；`/` 自动重定向到 `/docs`。

### GET /health
核对并返回当前客户端实例身份；客户端未运行/多实例时 503。

### GET /contacts 与 GET /groups
返回全部联系人（`contacts:[{name,user_id}]`）或群（`groups:[{name,group_id}]`），附 `table/page_size/source_sha256`。加 `?name=精确名称` 转为名称查询：唯一命中 200 `unique`；未命中 404 `not_found`；同名 409 `ambiguous`（`candidates` 含全部候选）。

### POST /send/user 与 POST /send/group
请求体 `{"user_id"|"group_id"|"name" 二选一, "text": "正文"}`；按名称发送时先做同样的唯一解析（404/409 同上）。正文规则同 Adapter：非空、UTF-8、无 NUL、按内容预算，字面 `\n`/`\r\n` 转实际换行。

成功响应 200：

```json
{"attempt_id":"NWT-GW-...","attempt_reserved":true,"preflight":{...},"call":{...},"delivery":"unknown"}
```

`delivery` 始终为 `unknown`——API 完成不代表送达；如需接收证据，用返回的 `attempt_id` 走 `ipcexperiment -observe` 记录独立观察。

错误映射：400 目标/正文校验失败或请求 JSON 非法；404 名称未命中；409 同名歧义；503 客户端不可用或快照未配置；502 已占用发送日志但 Win32 调用失败（响应仍携带 `attempt_id` 等已发生事实）。

## 5. 示例

```bash
curl -s http://127.0.0.1:8787/groups
curl -s 'http://127.0.0.1:8787/contacts?name=PVE-WIN11'
curl -s -X POST http://127.0.0.1:8787/send/user \
  -H 'Content-Type: application/json' \
  -d '{"name":"PVE-WIN11","text":"hello world"}'
curl -s -X POST http://127.0.0.1:8787/send/group \
  -H 'Content-Type: application/json' \
  -d '{"name":"qun","text":"hello group"}'
```

## 6. 边界与安全

- 初版无鉴权，默认只监听回环地址：仅适用于本机自动化。暴露到网络前须自行加反向代理鉴权与访问控制。
- 发送不设目标白名单（同 Adapter 契约：调用方即授权方）；每次 HTTP 发送就是一次新的单次请求，不会重试。
- 快照多身份表、损坏行、非明文 qrp 等情况一律明确失败，不挑第一条。
- Windows 386 构建发送；仅读取快照或列表不需要客户端运行，发送需要客户端正常登录。

## 7. 后续可选升级（当前维持现状，不做）

实测联系人/群在客户端运行期间即时落盘（2026-10-24：新群 qun2 建群即写；新联系人 PVE-email 运行中出现并实时可查），实时文件 + mtime 重载 + 读后复核已覆盖需求，残余差距仅"客户端刚知道、尚未写盘"的秒级窗口。若未来确需消除该窗口，两条路径及代价：

1. **逆向查询方向的 IPC**：现有 WM_COPYDATA 入口只恢复了发送方向；需 REA 研究是否存在查询接口。代价：一轮新的静态分析，结果存在不确定性。
2. **读客户端进程内存**：研究已恢复 CUser/CQGroupMgr 对象偏移与容器结构，技术可行。代价：违背本项目"不读进程内存"的既有纪律，且偏移随客户端版本变化，维护成本高。
