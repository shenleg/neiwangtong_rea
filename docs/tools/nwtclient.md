# nwtclient：常驻在线与连续单聊/群聊

`cmd/nwtclient` 启动后自动发现指定对端并交换1000/1018，持续维护同一连接；从stdin逐行接收单聊或群聊请求，收到退出信号后正常下线。它不再每发一条就重新上线。

**协议群聊也已真实验收**：正常3004→3006/3007同步确认`qun3`完整成员仅本机与当前对端，同连接两条3011均获用户确认，175.35秒后正常退出。使用`group_id`输入，完整规则与限制见[群聊说明](native-group-chat.md)，结果见[群聊验收](../research/reports/native-protocol/native-group-confirmation-20261025.md)。后文单聊章节保留1004专属规则；群同步不保证穷尽列出本身份所有群。

**常驻与连续发送已完成真实验收**：一次连接运行175.4秒，向PVE-WIN10连续两条固定短正文各发送一次，均获1005及用户独立接收确认；完成两轮本机1016心跳、SIGTERM/1017正常退出，用户另确认看到在线、当前已离线。见[常驻验收记录](../research/reports/native-protocol/native-resident-confirmation-20261025.md)。原[单条1004验收](../research/reports/native-protocol/native-user-message-confirmation-20261025.md)分别保留；更长运行、其他正文、真实SIGKILL与群聊仍未由本次覆盖。

## 1. 启动和停止

与限时实验命令 `nwtnode` 不同，**nwtclient默认启动即联网，不需要-run**。先校验配置时指定 `-check`，它不创建身份、记录或socket。

```bash
# 合成参数示例，只校验；不是对真实目标的运行授权。
go run ./cmd/nwtclient -check \
  -local 127.0.0.1 -prefix 127.0.0.0/8 -remote 127.0.0.2:9011 \
  -name Synthetic-Resident \
  -identity /absolute/private/identity \
  -record /absolute/private/new-resident.jsonl \
  -contacts /absolute/private/contacts.json
```

运行时去掉 `-check`；使用独立程序构建时执行 `go build -o /absolute/private/nwtclient ./cmd/nwtclient`，随后直接启动该程序并传上述显式参数。不提供固定运行时长，也不需要额外的“上线”指令。

- 仅向 `-remote` 指定的一个IPv4端点单播，不扫描或广播。发现/握手仍需实际完成，不把进程创建成功当作协议在线。
- `-local`、`-prefix`、`-name`、`-identity`、`-record`的规则沿用[nwtnode](nwtnode.md)；发现/ENet本机端口默认9011/9012，可显式指定，冲突直接失败。
- 身份文件保存Go节点自己的32字节身份；损坏不重置。联系人列表必须属于该身份，读取Windows客户端数据库不属于本命令。
- 每次启动必须提供新的私有记录路径，记录独占创建并Sync后才执行网络操作。已有记录不覆盖，不自动换名。
- 没有 `-contacts` 也可以只上线，但所有单聊请求明确报错；发送时必须提供本身份联系人列表。
- 发现最多10秒、ENet握手及应用就绪阶段各有15秒预算。启动失败停止，不自动重连或持续探测。
- 上线后由运行循环维护应用1016和ENet保活。stdin EOF只停止接收命令，**不会下线**；终端关闭若同时终止进程，则按操作系统实际终止方式处理。

| 停止方式 | 本机行为与对端限制 |
|---|---|
| Ctrl+C / SIGINT / SIGTERM | 停止接收新请求，健康会话发送1017并正常断开，最多5秒退出清理 |
| SIGKILL / `kill -9` / Windows强制结束进程 | 操作系统关闭本机socket；程序没有机会发送1017或完成日志，对端依靠超时判定离线，不能保证UI立即消失 |
| 对端正常退出 | 结束当前连接与进程，不自动重连 |
| 传输、协议、阶段记录或输出失败 | 明确返回错误并退出；不重发已提交消息 |

如果退出信号中断了已经Queue但尚未获传输确认的消息，该条仍是未知结果，可能无法正常完成QUIT；不能为了退出码0而改成成功。

## 2. 连续单聊

等待stdout输出 `"stage":"protocol_ready"` 后，在该进程的stdin输入JSON Lines，每行是一条独立请求：

```json
{"user_id":"列表中已确认的联系人ID","text":"第一条完整正文"}
{"user_id":"列表中已确认的联系人ID","text":"第二条完整正文\n实际换行后的第二行"}
```

输入协程会等待就绪后才开始读取，因此可以在启动时通过管道提供多行；读完这些行后进程仍保持在线。JSON中的 `\n` 按JSON规范解为实际换行，想发送字面反斜杠+n应使用 `\\n`。

当前只复用一个明确对端连接。选定联系人必须在本身份列表中，发现端点必须等于本次 `-remote`，实际连接身份还须与UserID相同；不会因为列表还有其他联系人就自动建立新连接。多peer共享host仍未实现；群聊仅支持完整非己成员就是当前对端的群，不跳过其他成员。

- 仅允许 `user_id`、`text`，各出现一次；不接受原始XML、MSG_ID、时间或flag。
- 每条原始正文1..4096 UTF-8字节，沿用已恢复的纯文本JSON及六实体编码；这只是实现预算，不是厂商上限或全部长度已经真实验证。
- 每行独立分配MSG_ID和正文GUID。同一进程内MSG_ID递增，重复输入同一正文也是新请求；不做调用者重试幂等化。
- 请求按输入顺序串行发送；Go调用者并发调用时，同样交由单一运行循环串行处理，不会并发读取UDP或重新握手。
- 发送前先Sync该请求的序号、目标/正文SHA-256和正文UTF-8字节数，再由运行循环保存 `chat_send_planned` 与MSG_ID并Sync，才进入Queue。不保存聊天正文。
- 坏行返回明确 `request_result` 错误而不发送，可以继续读取下一行。超过25600字节的输入行会报错并停止接收stdin，网络仍保持在线；该预算容纳4096字节正文的最坏JSON字符转义，避免无限分配。
- stdin读取、记录或stdout写入失败属于运行错误，停止进程，不吞掉异常后继续发送。

stdout为JSON Lines，阶段事件和请求结果共用串行编码。每个 `request_result` 包含输入顺序 `sequence`、`result`及 `error`。成功结果只说明该条获传输确认，不同步等待1005；之后的 `business_acknowledged` / `receipt_received` 事件通过 `message_id` 关联本条，可能早于或晚于 `request_result` 的输出。

1005/1006与传输ACK分别记录，`delivery`保持 `unknown`。只有外部接收者确认正确目标、发送者、全文和新增条数后，另存 `recipient_observed`；本命令不自动生成该观察。

## 3. Go调用方式

```go
client, err := nativeproto.NewClient(discovery, conn, nativeproto.ClientConfig{
    Node: node, Profile: profile, RemoteDiscovery: remote, Transport: transport,
    Contacts: directory,
    Duration: 0,
    ShutdownTimeout: 5 * time.Second,
    DiscoveryTimeout: 10 * time.Second,
    HandshakeTimeout: 15 * time.Second,
})
// 一个协程调用client.Run(ctx, observe)，observe收到protocol_ready后通知请求方。
result, err := client.SendUser(requestCtx, selectedUserID, completeText)
// 取消Run的ctx以退出；不要对同一个Client再次Run。
```

配置中的socket、资料及对端都由调用方明确提供。`Duration=0`表示常驻，正值仍供旧 `nwtnode` 限时实验使用。`SendUser`可以从外部协程连续或并发调用，但**不能在observe回调中同步调用**：observe本身就在持有网络的运行循环中。

握手未就绪或运行已停止时发送直接报错。请求还未交给运行循环便取消时，不生成消息编号、不Queue；交给运行循环后仍等待它返回本条编号及最终 `submitted` 事实，避免取消导致调用者丢掉未知发送结果。进入Queue后的传输错误停止会话，不自动重新提交。

返回的 `ChatResult` 是值快照，晚到ACK不会修改调用者已经拿到的值；完整事件交由observe持久化。`ClientResult.Chat`是最近一条消息的最终观察快照，`ChatSends`累计本次运行中被Queue接受的条数。

内存只保留最近1024条已提交消息的回执关联，超出后淘汰最旧项；完整落盘记录不删除，晚于缓存窗口的回执明确记为 `unmatched_chat_ack`，不猜测对应的新请求。

### 网页与并发状态读取

需要浏览器操作时使用独立入口 [`nwtweb`](nwtweb.md)，不把 HTTP 嵌入 stdin 命令。`client.Snapshot()` 可在运行中并发读取联系人、群及当前对端，返回深拷贝；联系人条目不等于在线，群列表不保证穷尽。

Go 调用者可显式设置 `LearnContacts=true`，双向 READY 后从正常握手合并当前对端联系人。可选 `ObserveDirectory` 成功保存后才发布新目录，失败停止会话；回调内不能同步调用发送。默认关闭学习，原 `nwtclient` 命令仍要求通过 `-contacts` 提供属于本身份的目录，行为不变。

## 4. 验证范围

默认测试只使用合成身份与loopback socket，不加载真实联系人、身份或客户端资料。

```bash
ENET_REFERENCE_BINARY= go test -count=1 -timeout 45s ./internal/nativeproto ./cmd/nwtclient ./cmd/nwtnode ./cmd/nwtprotocol
ENET_REFERENCE_BINARY= GOARCH=386 go test -count=1 -timeout 45s ./internal/nativeproto ./cmd/nwtclient ./cmd/nwtnode ./cmd/nwtprotocol
ENET_REFERENCE_BINARY= go test -race -count=3 -timeout 45s ./internal/nativeproto ./cmd/nwtclient ./cmd/nwtnode ./cmd/nwtprotocol
```

已覆盖：主动/被动常驻、同一连接连续四条、八个并发调用串行提交、逐条ID/GUID/完整正文、旧消息延迟回执、有限缓存、取消请求后仍可继续发送、应用心跳/超时、EOF保持在线、关闭阻塞stdin及旧限时实验回归。

Linux另外启动实际命令main的独立测试子进程：SIGTERM正常发送QUIT、退出0；SIGKILL终止进程且日志不伪造graceful结果，两种方式均释放本机UDP端口。此测试不观察Windows UI，也不把释放本机端口当成对端立即离线。

本轮Linux amd64/386全部非托盘包测试和vet通过；协议包与三条命令的race连续3次通过。Windows amd64/386全仓交叉构建与vet通过，没有在Windows运行测试。

要求的Linux两种架构全仓测试/vet均已执行，仍受既有托盘依赖阻塞：amd64缺少 `pkg-config`，386缺少 `nativeLoop/registerSystray/...` 平台符号。没有为运行本命令安装系统依赖、修改托盘或添加自动重启配置。可选官方ENet参考驱动本轮未提供，相关用例明确跳过；不以历史互通结果冒充本轮重新执行。
