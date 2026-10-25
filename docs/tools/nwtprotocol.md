# nwtprotocol：离线原生协议核心

本文说明 `internal/nativeproto` 和 `cmd/nwtprotocol` 的已实现功能、调用方式和未完成部分。字段依据见[协议规格](../research/reports/native-protocol/protocol-spec.md)，运行样本见[启动退出分析](../research/reports/native-protocol/lifecycle-validation-20261025.md)。

## 1. 当前能力

已实现：

- 304 字节发现/节点控制包：类型 1、2、4，保留模板中的未知字节。
- ENet 包和 1..12 命令编解码：无压缩、无 checksum 扩展，支持一包多命令及逐字节往返。
- ENet 分片重组：乱序、重复、冲突、缺口、显式缓存预算和到期清理。
- 1000 握手、1018 READY、1016 心跳、1017 QUIT、3004 群资料请求的 XML 编解码。
- XTEA 业务帧；3005 保留原文，不修补厂商 XML 标签错配。
- 单对端双向时序观察、60 秒心跳发送检查和 120 秒接收超时判断。
- 二进制解析与 JSON Lines 离线回放命令。

已新增[单对端 ENet 可靠传输](native-transport.md)及[独立新节点运行](nwtnode.md)，真实上线获原客户端UI确认并正常退出，见[验收记录](../research/reports/native-protocol/native-node-online-confirmation-20261025.md)。这不改变 `Peer` 离线观察器的含义：它的 online 本身仍不代表UI确认。完整多peer、临时连接管理和1004/3011业务提交尚未完成。

本命令保持离线，不创建 socket。包中的 UDPTransport 只使用调用者明确提供的独占 socket 和单个远端，不自动读取客户端、联系人库或凭据，不自动建连/重连。现有 IPC 网关不受影响。

## 2. 构建

```bash
go build -o /tmp/nwtprotocol ./cmd/nwtprotocol
GOOS=windows GOARCH=386 go build -o /tmp/nwtprotocol-windows-386.exe ./cmd/nwtprotocol
```

输出文件须使用新的私有路径，二进制不提交 Git。以下示例使用新临时目录，避免覆盖已有文件：

```bash
work=$(mktemp -d)
printf '%s' '<SYNTHETIC>离线测试🙂</SYNTHETIC>' > "$work/input.xml"
go run ./cmd/nwtprotocol -mode encode -command 1004 < "$work/input.xml" > "$work/frame.bin"
go run ./cmd/nwtprotocol -mode decode < "$work/frame.bin"
```

这里的 XML 只验证编码，不是可向客户端发送的业务消息。编码输出为二进制，不要直接显示到终端。

## 3. 命令模式

| `-mode` | stdin | stdout |
|---|---|---|
| `encode` | 完整 UTF-8 正文 | 一帧 XTEA 业务二进制；必须传 `-command` |
| `decode` | 恰好一帧业务二进制 | `Frame` JSON |
| `node-decode` | 完整 304 字节节点包 | 已知节点字段 JSON，包括输入中的 UserID |
| `enet-decode` | 一份 UDP payload，不含 UDP/IP 头 | ENet 包、命令和正文 JSON |
| `enet-encode` | 一份 ENet 包 JSON | 重新编码的二进制 UDP payload |
| `enet-reassemble` | 每行一份 datagram 记录 | 每行一个完整、未解密的数据单元 |
| `peer-replay` | 每行一个双向消息/计时/断开事件 | 不含身份资料的阶段和计时状态 |

`enet-decode` 的 `Data`、`Control` 是 Go JSON 中的 base64 字节字段；可直接交给 `enet-encode`。这与回放输入的十六进制字段不同，不要混用。

所有模式均不直接读 PCAP，也不重组 TCP。必须先提取正确的 UDP payload 或已重组应用帧。ENet 中可能携带未加密的 type 4 节点包，不要把所有数据都交给业务 XML 解码器。

### 参数

- `-mode` 必填，没有隐式模式。
- `-command` 仅 `encode` 接受，十进制 32 位无符号数。
- `-max-frame-bytes` 默认 1048576，允许参数范围 9..4294967295，另受本机可分配范围约束。
- `-local-user-id`、`-remote-user-id` 仅 `peer-replay` 接受，必须明确提供两个不同的合成或合法标识；命令不会读取或生成身份。

1 MiB 是本工具的读取/重组预算，不是厂商可用消息上限，也不放宽 IPC 正文预算。`enet-encode` 的 JSON 输入同样受读取预算限制。

退出码：0 为离线处理成功，1 为输入/输出或协议错误，2 为参数错误。任一记录失败即停止；先前已输出的记录仍保留，但不能凭部分输出声称整个流成功。

### 3.1 分片重组输入

每行是一个 JSON 对象，时间为相对同一起点的非负单调毫秒：

```json
{"at_ms":0,"flow":"synthetic-A-to-B","packet_hex":"800000018600000100086162636465666768"}
```

这是一个承载合成字符串 `abcdefgh` 的可靠数据命令，不是业务 XML。输出：

```json
{"at_ms":0,"flow":"synthetic-A-to-B","channel":0,"data_hex":"6162636465666768"}
```

- `flow` 必须区分单向源/目的端点。组键还包含 peer、session、channel、命令类型及片组起始序号。
- 相同片号、相同偏移与内容只保存一次；相同片号冲突、不同片号重叠、长度/片数变化均失败。
- 完整片组也保留到缓存到期，以抑制相同片组再次交付。不是完整 ENet 可靠窗口去重实现，未分片数据仍逐条输出。
- EOF 存在未完成组、到期时仍缺片，都返回错误，不补零、不静默忽略。
- CLI 的缓存预算为最多 64 组、每组 4096 片、总实际数据不超过 `4 × max-frame-bytes`、TTL 120 秒。TTL 是工具缓存规则，不是恢复出的 ENet 超时。
- 每行最多 256 KiB，另受 `2 × max-frame-bytes + 1024` 限制；缺失时间、倒退时间、未知 JSON 字段及同一行第二个 JSON 均失败。

### 3.2 会话回放输入

```json
{"at_ms":0,"kind":"message","outbound":false,"data_hex":"此处放完整type4或业务帧的十六进制"}
{"at_ms":60000,"kind":"timers"}
{"at_ms":60001,"kind":"disconnect"}
```

第一行只是格式说明，必须替换十六进制值。`outbound=true` 表示本机发出，默认 false 表示对端发来；`disconnect/timers` 不接受正文或 outbound=true。

用 `-local-user-id local-synthetic -remote-user-id remote-synthetic` 提供身份，不按抓包里的名称猜目标。回放不会根据状态补造发包：

```text
awaiting_node → handshaking → readying → online → closing → closed
```

- 首先观察可靠连接中的 type 4；身份须与声明的该方向发送者一致。
- 双向 1000 都存在后才接受 1018；READY 的 UserID 同样与该方向发送者核对。
- 双向 READY 后才接受心跳、群请求/原文响应和 QUIT；收到或发出 QUIT 只进入 closing，外部明确报告传输关闭才进入 closed。
- 不要求两个方向都出现 type 4，避免把实际主会话中不存在的反向节点包变成必要步骤。
- `timers` 只判断是否到期，不实际发送/断开；关闭会话不能隐式重新上线。
- 这是一份已观察初始顺序的参考状态模型，不承诺它覆盖所有版本、重连和业务更新场景。

## 4. 包接口

### 业务帧与 XML

```go
wire, err := nativeproto.Encode(command, xmlPayload)
frame, err := nativeproto.Decode(reader, maxFrameBytes)

frame, err = nativeproto.EncodeHandshake(profile)
profile, err = nativeproto.DecodeHandshake(frame)
frame, err = nativeproto.EncodeReady(nativeproto.Ready{UserID: localID})
ready, err := nativeproto.DecodeReady(frame)
heartbeat := nativeproto.HeartbeatFrame()
quit := nativeproto.QuitFrame()
```

- `Encode/Decode` 只检查正文非空、有效 UTF-8、无 NUL 与帧长，不解析 XML，不制造 MSG_ID 或认证参数。
- XML 函数使用 `encoding/xml`，拒绝重复必要字段、不支持的版本、命名空间、嵌套和尾随第二根；FEATURE/INFO 的未知叶子与属性保留。
- `HandshakePorts` 使用主机序；XML 中的 9 个端口十进制字段由模块交换 16 位字节序。
- `Handshake` 不默认为调用者补充“足以上线”的资料，也不臆测厂商所有必需叶子；调用者负责本机合法身份和完整初始化。
- 目前只验证 READY `Param=0`，其他值明确不支持。
- 3005 原文可能有 `<QGROUP_INFO_VER></GROUP_INFO_VER>` 错配；Frame 保留该原文，不自动修补，也不把它当作已解析、可信的群资料。

### 节点包

```go
node, err := nativeproto.DecodeNodePacket(explicitTemplate)
node.Type = 4
wire, err := node.Encode()
```

`NodePacket` 内保存模板。零值不能直接编码；只更新已知字段，其他字节不动。UserID槽按UTF-8字节计长，最多64字节加NUL。后续已恢复[正常默认初始化](../research/reports/native-protocol/node-registration-contract.md)：`NewNodePacket`可用自己的新UserID、独立二进制会话GUID和实际端口构造默认包，明确零区有原厂初始化证据，不靠猜测填零。不会扫描客户端或复制对端身份。解码成功本身不等于完成上线。

### ENet 与重组

```go
packet, err := nativeproto.DecodeENet(datagram)
wire, err := nativeproto.EncodeENet(packet)
reassembler, err := nativeproto.NewReassembler(limits)
message, err := reassembler.Add(stream, fragmentCommand, elapsed)
removed, err := reassembler.Expire(elapsed)
pending := reassembler.Pending()
```

- 当前无压缩、无 checksum 扩展；包最大 65535 字节、每包最多 32 命令是本模块预算。
- 未知命令、保留标志、截断和尾随不完整命令报错，不扫描偏移或静默跳过。
- 控制正文 `Control` 保持原始字节，数据命令单独暴露 `Data`；分片定位字段使用 `Fragment`，错误字段组合在编码时拒绝。
- 输入数据复制保存；调用者修改原始 datagram 不会改变已解码命令或已缓存片段。
- 不为声明的 total_length 分配巨大稀疏缓冲；限额分别约束消息长度、实际缓存字节、组数与片数。
- `Expire` 返回全部已清理组数量；用 `Pending` 识别未完成组，不能把清理数量当成功或送达数。

### 单对端离线会话

```go
peer, err := nativeproto.NewPeer(localID, remoteID, maxFrameBytes)
err = peer.Observe(outbound, completeNodeOrApplicationBytes, elapsed)
status := peer.Status()
timers, err := peer.Timers(elapsed)
err = peer.Disconnect(elapsed)
```

该接口没有 socket、凭据读取、隐式 ID 匹配或发送副作用。重连须新建实例，不能把旧路由、会话状态或 ENet 序号复制到新连接。

## 5. 验证结果

合成单测和集成测试覆盖节点未知字节保留、ENet 1..12 往返、一包多命令、分片乱序/重复/冲突/缺口/限额/到期、XML 转义/版本/结构/端口、双向会话及心跳检查。测试不读取真实抓包或身份，不使用网络。

另对先前已授权、已分析的三份采集做显式离线核对，不把原始包或身份放入测试 fixture：

| 采集 | 节点包成功解码 | ENet 解码/重新编码逐字节一致 | 完整数据单元与既有分析摘要一致 |
|---|---:|---:|---:|
| 稳态 `20261025-014922` | 4 | 22 | 5 |
| 启动 `20261025-092254-startup` | 32 | 132 | 34，含 6 个二进制节点包和 4 个重组握手 |
| 退出 `20261025-093200-exit` | 0 | 10 | 2 |

P1 的实际双向消息可在参考状态模型中回放为 online，再记录退出与外部关闭为 closed。两份采集之间的未捕获时间段未被用于推断在线存活；**不是独立 Go 进程真实上线成功**。

```bash
go test -count=1 -timeout 45s ./internal/nativeproto ./cmd/nwtprotocol
go vet ./internal/nativeproto ./cmd/nwtprotocol
GOARCH=386 go test -count=1 -timeout 45s ./internal/nativeproto ./cmd/nwtprotocol
GOARCH=386 go vet ./internal/nativeproto ./cmd/nwtprotocol
go test -race -count=1 -timeout 45s ./internal/nativeproto ./cmd/nwtprotocol
```

上述离线检查通过；两个解析器各进行 5 秒、2 worker 的模糊测试，往返不变量通过。新增传输的 loopback、丢包恢复和官方独立实现验证见[传输测试说明](native-transport.md#4-测试)。Linux 全仓托盘依赖问题与其他构建结果按项目验证记录区分，不以局部通过冒充全仓通过。

## 6. 下一步

单对端真实应用上线已由 [nwtnode](nwtnode.md)实现并验收；不需要再为“尚未上线”重复抓稳态/启动/退出。接下来是完整主/临时连接、多peer及1004/3011业务生成。

聊天发送和独立接收尚未完成。传输层重传与应用层重新提交必须分开；上线验收不延续真实连接或发送授权。聊天验收仍采用具体消息的 `recipient_observed`，不能由 codec、回放online、UI看到节点或ACK代替。
