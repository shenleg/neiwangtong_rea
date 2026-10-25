# 单对端 ENet 可靠传输

本文说明 `internal/nativeproto` 的 ENet 运行核心、同步 UDP 驱动及本机互通验证。它在[离线协议核心](nwtprotocol.md)之上增加实际传输能力，但不执行内网通发现、身份初始化或应用登录。Go 实现只依赖标准库，不需要 CGO 或 ENet DLL；C 源码仅用于可选的独立参考测试。

## 1. 已实现范围

- 主动 CONNECT / VERIFY_CONNECT / ACK，以及被动握手。
- 单可靠 channel 0、控制 channel 255、无压缩/校验和扩展。
- peer/session/ConnectID 核对、16 位序号回绕、按序交付、重复数据确认但不重复交付。
- 可靠发送字节窗口、有限序号窗口、分片发送/重组、ACK 后继续发送已排队片段。
- 显式接收超时、有限指数退避重传、空闲 ENet PING。
- 可靠队列排空后的 DISCONNECT / ACK。
- 独占 socket 的同步 UDP 驱动，取消处理、来源核对、入站缓存预算和关闭责任。

**没有实现完整内网通客户端。** 这里的 `connected` 只代表 ENet 传输握手完成；不生成或交换 304 字节节点包、1000、1018、1016、1017，不提供群/用户业务发送，也不代表 `recipient_observed`。

`cmd/nwtprotocol` 仍然只有离线模式，不因新增这个包而绑定端口或连接真实节点。现有 IPC 网关未改用本传输。

## 2. 两个接口

### 2.1 显式时间的调度核心

```go
runtime, err := nativeproto.NewENetRuntime(config)
events, err := runtime.Start(elapsed)
events, err = runtime.Handle(datagram, elapsed)
events, err = runtime.Queue(message, elapsed)
events, err = runtime.Tick(elapsed)
events, err = runtime.Disconnect(elapsed)
status := runtime.Status()
```

- 只处理数据和单调时间，不持有 socket，不依赖后台 goroutine。
- `ENetEvents.Datagrams` 是本轮需要写出的 UDP payload，`Messages` 是首次按序交付的数据。
- 调用者必须处理输出 datagram，再检查阶段完成；否则会漏发 VERIFY 或 DISCONNECT 的最终 ACK。
- `Queue` 一次是一次应用提交。重传使用同一命令序号和内容，不能在超时后再调用一次 Queue 冒充“传输重传”。
- 队列预算不足时拒绝整条消息，不部分排队；连接已超时也不会先发新消息再报超时。
- 缺片/接收超时或重发次数耗尽后进入 `failed`，不隐式重新 CONNECT，不换 ConnectID 继续尝试。

### 2.2 独占 socket 的同步驱动

```go
transport, err := nativeproto.NewUDPTransport(conn, remote, config)
err = transport.Connect(ctx) // 主动方；被动方调用 Accept(ctx)
err = transport.Send(ctx, data)
message, err := transport.Receive(ctx)
err = transport.Close(ctx)
```

`conn` 必须由调用者明确创建且未连接，`remote` 为明确提供的 `netip.AddrPort`；不会从收到的 XML/身份推导新端点。

- 构造成功后接管 socket 的关闭责任；构造失败不关闭调用方 socket。
- 一份驱动只能服务一个明确对端，所有方法串行调用，不能与其他 peer 共享 socket。
- IPv4-mapped 地址先 Unmap；忽略其他来源的包，不让它们刷新本会话状态。拒绝未指定、组播和全局广播目标；调用者仍负责确认实际端点为授权单播节点。
- 不创建后台读协程。同步驱动使用最多 20ms 的 deadline 推进取消、计时和重发；长期空闲时必须由调用者持续驱动接收，而不是把它当后台连接池。
- Send 仅 Queue 一次，等待全部可靠命令得到 ACK；同时收到的消息保存供后续 Receive 获取。
- **Send 返回 nil 只表示传输确认。** 对端业务是否处理、正文是否显示和独立接收仍须另外确认。
- 纯读等待取消可重试；已经推进协议状态后，事件写出失败/取消会标为 failed，只允许 Close，避免丢掉待发 ACK 后继续使用损坏状态。
- 提交后的失败结果未知，后续 Send 不会偷偷重提旧任务或提交新任务。
- 入站消息预算在发 ACK 前校验；预算不足明确失败，不确认自己无法保存的数据。
- Close 先等待可靠队列排空，然后发 ENet DISCONNECT 并等待相应 ACK；无论结果如何都会关闭 socket，重复调用保留第一次结果。
- Close **不发送应用 1017**。完整内网通退出流程必须由上层先提交自己的合法 QUIT，再关闭传输。

状态与应用层观察器区分：

```text
ENetRuntime：idle → connecting / accepting → connected → disconnecting → closed
                    任一传输失败 → failed
Peer：       awaiting_node → handshaking → readying → online → closing → closed
```

第二行仍是原先的离线应用时序观察器，不会因为第一行 connected 就自动把应用标记为 online。

## 3. 配置与限制

配置均显式提供，不读取环境、客户端资料或隐藏默认目标。

| 配置 | 规则 |
|---|---|
| LocalPeerID | 0..4094；4095 为尚未分配 peer 的协议保留值 |
| ConnectID | 本次主动连接的新鲜非零 4 字节值；按不透明原始字节传递，不是身份凭据 |
| MTU | 576..4096；与对方取较小值；本样本常见 1400 |
| WindowBytes | 4096..65536；字节窗口，不是业务正文上限 |
| MaxMessageBytes | 单条数据预算，受本机可分配范围约束 |
| MaxQueuedBytes | 不小于单消息预算和已广告的 WindowBytes；分别限制发送/接收缓存 |
| MaxPendingCommands | 1..4096；同时约束可靠待确认数和接收序号占用 |
| RetransmitAfter | 至少 1ms；初始重发间隔，后续指数增加但不超过 ReceiveTimeout |
| MaxAttempts | 包含初次发送；达到次数仍无确认则失败 |
| ReceiveTimeout | 不小于初始重发间隔；握手和已连接状态均受限制 |
| PingInterval | 大于零且小于 ReceiveTimeout；ENet PING，不是应用层 1016 |

当前运行实现只支持一个可靠 channel、零带宽限制及 throttle 参数 `5000/2/2`；其他模式明确不支持。离线编解码能识别 1..12 命令，不表示运行核心会接受不可靠传输、所有限速设置或所有扩展。

有限序号窗口为 4096，另受调用方预算限制；这是本实现的资源限制，不是厂商消息上限。最早未确认命令仍在时，不能仅凭部分字节已确认就绕回覆盖它。

分片阈值按 ENet 1.3 的 `MTU - 4 字节包头 - 24 字节分片命令头` 计算。不要用更小的 SEND_RELIABLE 头推导另一条切片阈值。

没有自适应拥塞算法、共享 host 的多 peer 分派、并发 Connect 冲突解决、主/临时会话选择或自动重连。这些不能用单对端回显成功替代。

## 4. 测试

默认测试只使用纯调度或新建 `127.0.0.1:0` socket，不联系真实客户端，不读取私有配置。

```bash
ENET_REFERENCE_BINARY= go test -count=1 -timeout 45s ./internal/nativeproto ./cmd/nwtprotocol
ENET_REFERENCE_BINARY= GOARCH=386 go test -count=1 -timeout 45s ./internal/nativeproto ./cmd/nwtprotocol
ENET_REFERENCE_BINARY= go test -race -count=3 -timeout 45s ./internal/nativeproto ./cmd/nwtprotocol
```

覆盖主动/被动握手、VERIFY/最终 ACK 丢失、ACK 时间回显、旧 session、握手 ACK 前数据乱序、首次按序交付、重复片段、65535→0 序号回绕、发送窗口、有限重发、接收超时、取消和 socket 所有权。模拟丢包只在本次合成连接中发生，不修改系统网络、网卡或其他进程。

### 4.1 独立官方实现互通

为避免两个 Go 实例共享同一错误，本轮另经操作者明确授权，在私有临时目录编译官方 [ENet v1.3.18](https://github.com/lsalzman/enet/tree/v1.3.18)，仅用 loopback 验证。没有安装系统库，也没有向真实节点发包。

官方源归档 SHA-256：`28603c895f9ed24a846478180ee72c7376b39b4bb1287b73877e5eae7d96b0dd`。

| 场景 | 实际结果 |
|---|---|
| Go 主动 → 官方 ENet 被动 | 90003 字节可靠数据分片、跨发送窗口、逐字节回显、正常断开 |
| 官方 ENet 主动 → Go 被动 | 70003 字节可靠数据、Go 回显、远端断开 ACK |
| Go → 本机丢包代理 → 官方 ENet | 丢首个分片和首个数据 ACK 后恢复，数据一致；参考端只接受一次应用交付 |

三种场景各连续 10 次通过，并通过 race 检查；另在 Linux 386 的 Go 测试进程中完成与同一独立参考驱动的互通。参考 C 构建出现上游未使用参数/符号比较告警，构建与测试均成功；未修改上游代码来隐藏这些告警。

本轮 amd64/386 的协议包测试、vet、race 和 Windows 两种架构全仓构建/vet 均通过；新增运行核心的 5 秒模糊测试通过。Linux 全仓测试/vet 已执行，仍受原有托盘依赖阻塞：amd64 缺 `pkg-config`，386 缺 `nativeLoop/registerSystray/...`。没有安装依赖、修改托盘或把局部成功写成全仓成功。

可选测试由 `ENET_REFERENCE_BINARY` 显式提供参考驱动的绝对路径：

```bash
ENET_REFERENCE_BINARY=/tmp/nwt-enet-runtime-0iaAKd6Z/driver/enet-reference \
  go test -race -count=10 -timeout 45s ./internal/nativeproto -run '^TestENetReference'
```

未设置时明确 skip，不下载、编译或执行外部源码。参考驱动源码在 `internal/nativeproto/testdata/enet_reference.c`，它只允许 server/client 模式且固定 loopback，返回 READY 端口后交换合成字节。运行外部构建仍须操作者批准，不把它变成日常默认测试的前置条件。

参考规则来源：

- [protocol.h](https://github.com/lsalzman/enet/blob/v1.3.18/include/enet/protocol.h)：头、命令结构及协议范围。
- [protocol.c](https://github.com/lsalzman/enet/blob/v1.3.18/protocol.c)：CONNECT/VERIFY session 交叉赋值、ACK 和断开行为。
- [peer.c](https://github.com/lsalzman/enet/blob/v1.3.18/peer.c)：分片阈值和可靠序号分配。

这是与官方指定版本的单对端互通结果，不把样本嵌入的 ENet 版本直接认定为 v1.3.18，也不等同于内网通应用上线成功。

## 5. 下一步

本机新身份、默认节点包、单对端发现与1000/1018/1016/1017运行已经接入，并完成[真实UI上线验收](../research/reports/native-protocol/native-node-online-confirmation-20261025.md)，入口见 [nwtnode](nwtnode.md)。共享host多peer、完整主/临时连接规则仍需实现。

真实运行只有上线控制，没有聊天业务发送或 `recipient_observed`。1004/3011仍需另行开发和具体授权的独立接收验收，原IPC继续负责生产发送。
