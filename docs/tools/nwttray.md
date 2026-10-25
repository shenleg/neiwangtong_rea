# 统一发送 API（nwttray）

统一发送 API 的唯一宿主是 `cmd/nwttray`（Windows 托盘程序）。HTTP 层在 `internal/api`、业务在 `internal/agent`，不直接接触 HWND、WM_COPYDATA 或数据库文件；发送沿用 `internal/ipc` 的既定纪律——单次调用、不自动重试、每次发送独立日志（`%AppData%\neiwangtong-gateway\ipc-experiment\`）。

曾经的命令行宿主 `cmd/nwtagent` 已于 2026-10-25 移除：脚本和自动化走 HTTP API，只需要服务在跑，托盘 `-autostart` 即覆盖"开机即服务"，保留第二个入口只增加使用者的选择负担。

## 1. 构建与运行（托盘程序）

```bash
# 发布构建：无控制台窗口，双击即启动
GOOS=windows GOARCH=386 go build -ldflags "-H=windowsgui" -o nwttray.exe ./cmd/nwttray

# 调试构建：保留 stderr 输出
GOOS=windows GOARCH=386 go build -o nwttray.exe ./cmd/nwttray
```

启动参数全部可留空：**双击 `nwttray.exe`（或加 `-autostart` 做开机自启）即可**，程序自动探测客户端安装位置、数据文件与 codec 口令。识别失败时弹消息框说明原因；需要覆盖时才传参：

```bash
./nwttray.exe -autostart                    # 常规用法：零参数 + 自动识别
./nwttray.exe -exe 'C:\私有检查目录\ShiYeLine.exe' -nr 'C:\私有检查目录\nr-copy' ...   # 全显式覆盖
```

托盘提供：

- **图标**：绿色=客户端已连接，红色=未连接；提示文字含 API 监听状态，每 10 秒轮询一次客户端实例。
- **菜单**：「启动 API 服务 / 停止 API 服务（地址）」切换监听；「启用外网监听（0.0.0.0）/ 停止外网监听（地址）」两态菜单项在 `-addr` 形态与 `0.0.0.0` 通配监听间切换（运行中平滑切换，未运行时下次启动生效），启用时弹无鉴权警告确认，停止即切回仅本机；「打开 API 文档」在浏览器打开内置接口说明页（服务未启动时会先启动）；「客户端状态：已连接（PID）/未连接」为只读显示；「退出」停止服务并退出程序。
- **外网监听的防火墙前提**：首次监听 `0.0.0.0` 时 Windows 防火墙会弹"允许访问"提示，放行后局域网其他机器才能连入（调用地址如 `http://<本机IP>:8787`）。
- `-autostart`：启动后立即开始提供 API 服务，不等待托盘菜单。

## 2. 自动识别与参数覆盖

| 参数 | 留空时的自动识别 | 显式传入 |
|---|---|---|
| `-exe` | 探测 `C:\Program Files (x86)` 与 `C:\Program Files` 下的 `nwt*\ShiYeLine.exe`，唯一命中即用；零命中或多命中报错，不猜测 | 指定客户端绝对路径 |
| `-nr` | `<exe目录>\Syl\cache\db\nr`（客户端实时库） | 指定副本或实时库路径 |
| `-qrp` | `<exe目录>\Syl\data\qrp`（客户端实时库） | 同上 |
| `-password-file` | 从 exe 内嵌字符串提取（样本地址 0x9e2e3c，读至 NUL，上限 64 字节）；已在真实环境与既有口令文件核对一致 | 指定口令文件 |
| `-addr` | 默认 `127.0.0.1:8787`（仅本机回环） | 修改监听地址 |

- 数据路径默认指向**客户端正在使用的实时数据库文件**：Agent 按文件 mtime 缓存，客户端写盘后下一次请求自动加载新数据（实测建群后立即可查）。需要审计级一致性时改用[退出后副本](contactid-runbook.md)显式传参，替换副本文件后同样自动生效。实时文件在客户端首次登录前尚不存在，对应接口会明确报"检查数据文件"错误，客户端登录落盘后自动恢复。
- 实时文件模式下读取发生在客户端运行期间：存在 `-journal` 等边文件的事务瞬间会明确失败（不重试、不修复），下一次请求恢复；结构异常同样明确报错。每次加载完成后做**读后复核**（mtime 与边文件复查）：读取期间客户端恰好开始或完成写事务时，本次结果作废并返回 503（`ErrConcurrentWrite`），立即重试即可，不会缓存可疑数据。
- 口令提取地址绑定已验证的 3.4.3055 样本（依据：证据 0055 的 `MOV EBX, 0x9e2e3c`）；客户端升级后提取失败时程序会提示改用 `-password-file`。口令文件内容必须是 codec 口令的精确字节（读至 EOF 不去换行，末尾勿附加换行）；口令只在内存使用。

## 3. API

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

错误映射：400 目标/正文校验失败或请求 JSON 非法；404 名称未命中；409 同名歧义；503 客户端不可用或快照不可用（文件缺失、读取撞上写事务）；502 已占用发送日志但 Win32 调用失败（响应仍携带 `attempt_id` 等已发生事实）。

## 4. 示例

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

## 5. 边界与安全

- API 无鉴权。默认只监听回环地址，仅适用于本机自动化；托盘「启用外网监听」菜单项可切到 `0.0.0.0` 供局域网调用（启用时有确认提示），前提是该局域网完全可信。跨不受信网络暴露前须自行加反向代理鉴权与访问控制。
- 发送不设目标白名单（同 Adapter 契约：调用方即授权方）；每次 HTTP 发送就是一次新的单次请求，不会重试。
- 快照多身份表、损坏行、非明文 qrp 等情况一律明确失败，不挑第一条。
- Windows 386 构建发送；仅读取快照或列表不需要客户端运行，发送需要客户端正常登录。

## 6. 后续可选升级（当前维持现状，不做）

实测联系人/群在客户端运行期间即时落盘（2026-10-24：新群 qun2 建群即写；新联系人 PVE-email 运行中出现并实时可查），实时文件 + mtime 重载 + 读后复核已覆盖需求，残余差距仅"客户端刚知道、尚未写盘"的秒级窗口。若未来确需消除该窗口，两条路径及代价：

1. **逆向查询方向的 IPC**：现有 WM_COPYDATA 入口只恢复了发送方向；需 REA 研究是否存在查询接口。代价：一轮新的静态分析，结果存在不确定性。
2. **读客户端进程内存**：研究已恢复 CUser/CQGroupMgr 对象偏移与容器结构，技术可行。代价：违背本项目"不读进程内存"的既有纪律，且偏移随客户端版本变化，维护成本高。
