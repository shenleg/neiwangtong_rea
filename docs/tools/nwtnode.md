# nwtnode：独立身份上线与指定联系人单聊

`cmd/nwtnode` 将显式本机身份、304字节发现/节点控制、ENet和应用1000/1018/1016/1017串成一次限时运行。需要启动后常驻、同一连接连续单聊时使用新增的[nwtclient](nwtclient.md)，本命令的默认只读和限时语义保持不变。**已完成单对端真实上线及独立原客户端 UI 验收**，见[上线验收记录](../research/reports/native-protocol/native-node-online-confirmation-20261025.md)。

## 1. 默认只校验

以下是参数格式，默认不创建身份/记录，不绑定端口，不发包：

```bash
go run ./cmd/nwtnode \
  -local 127.0.0.1 -prefix 127.0.0.0/8 -remote 127.0.0.2:9011 \
  -name Synthetic-Node \
  -identity /absolute/private/path/identity \
  -record /absolute/private/path/new-attempt.jsonl \
  -duration 90s
```

这是只读示例，不是对真实目标的运行授权。实际运行另需显式 `-run`，只使用已确认的本机地址与唯一目标。

- `-local`：明确的本机IPv4，不能是未指定或组播地址。
- `-prefix`：包含本机地址的IPv4子网，不窄于/30；只计算公告的广播字段，不向该广播地址发包。
- `-remote`：唯一IPv4发现端点，拒绝本机、广播和组播目标，不枚举或扫描。
- `-name`：新节点显示名，必填。
- `-identity`、`-record`：两个不同的绝对私有路径；父目录由操作者明确准备。
- `-discovery-port`、`-enet-port`：本机实际端口，缺省9011/9012；端口冲突立即失败，不自动换端口。
- `-duration`：总时限须大于5秒且最多90秒；预留5秒正常退出。

退出码：0表示校验或限时运行完成，1表示运行错误，2表示参数错误。原客户端UI是否看到在线由外部观察确认，不根据退出码自动填写。

## 2. 一次运行的行为

1. 以 `O_EXCL` 创建记录并 Sync；记录已存在则停止，不换记录名继续。
2. 读取自己的独立身份文件；不存在时按[已恢复的新身份规则](../research/reports/native-protocol/node-registration-contract.md)生成、独占保存；损坏时不自动重置。
3. 本次另生成二进制会话GUID和ConnectID；不复制已有Windows身份，也不读取其acc/数据库/内存。
4. 绑定显式本机IPv4与两个UDP端口，只向唯一目标发现一次；对其正常发现请求可回复type2，其他来源不响应。
5. 比较双方完整会话字段决定主动Connect或被动Accept；不按IP大小选角色。
6. 交换type4、1000、1018，已就绪后维持应用1016和ENet自身保活。
7. 到时发送应用1017并正常断开；错误/未知结果保留，不重连、不换身份补偿试发。

只实现默认空 CorpNumber 的新节点路径，不猜组织FEATURE或把自己注册到某个原生群。INFO/GROUP是明确的联系人资料分组标签 `NWT-Go-Test`，不是QGROUP_ID。

## 3. 资料与协议限制

实验资料使用兼容的3.4.3055版本字段与自己的显示名。APP、MSG、NAME、GROUP必须非空；MSG的 `2:1` 是控制消息协议兼容声明，不是通过留空来关闭聊天的开关。

程序现在提供显式指定联系人的一次1004单聊入口，见第6节；默认只上线，不发聊天。没有群聊、文件或远控发送入口。不支持的入站业务仍只记录命令号，不保存其正文、不假造成功响应，也不把任何ACK当送达。

新节点未提供TCP或reverse listener时不公告这些端口；当前只运行一个指定对端，不冒充多peer共享host或所有主/临时连接规则已实现。

## 4. 记录与验收

本项目私有目录 `local/nativeclient/` 已被Git忽略，内含自己的身份、程序和逐次JSONL记录；其中真实端点、UserID及会话值不提交。

阶段记录不含远端姓名/身份、群资料或聊天内容。它记录消息版本、控制命令号、状态、传输ACK和收到的DISCONNECT data。DISCONNECT data是原始应用数据字段，`0`不自动解释为某个具体拒绝原因。

验收分三项：

- `protocol_ready=true`：已观察到双向1000/1018。
- 用户在原客户端明确看到新名称/IP在线：另存 `peer_ui_online_observed` 观察；不改写原日志的unknown字段。
- `graceful_exit=true`：本次应用QUIT与传输关闭成功；不自动宣称UI已人工确认下线。

历史上线验收没有聊天消息，不产生 `recipient_observed`。1004实现完成后，另经明确授权完成联系人核对及一次固定短正文发送，用户确认目标、发送者、全文和条数正确，见[单聊真实验收](../research/reports/native-protocol/native-user-message-confirmation-20261025.md)。后续真实单聊仍需具体联系人、完整正文、次数及上线时长授权。本限时命令不启用3011；群聊使用[nwtclient群同步与发送](native-group-chat.md)，已完成[qun3连续两条真实验收](../research/reports/native-protocol/native-group-confirmation-20261025.md)。入群仍由人工操作，仅向本独立身份已加入且完整成员可覆盖的群发送。

## 5. 测试

默认测试使用合成资料和loopback端点；`cmd/nwtnode` 自身测试缺省只读、参数拒绝及记录碰撞，不向真实节点发包。

```bash
go test -count=1 -timeout 45s ./internal/nativeproto ./cmd/nwtnode ./cmd/nwtprotocol
GOARCH=386 go test -count=1 -timeout 45s ./internal/nativeproto ./cmd/nwtnode ./cmd/nwtprotocol
go test -race -count=2 -timeout 45s ./internal/nativeproto ./cmd/nwtnode
```

模拟端检查消息版本与双向READY，单聊覆盖主动/被动、一次提交、1005/1006区别、消息编号匹配、记录失败、提交前取消、提交后未知、目标身份不符、正文转义及预算；另有非空必填资料、真实失败阶段关闭回放与独立GUID/身份持久化回归。已有[官方ENet独立互通](native-transport.md)与历史真实UI上线验收分别保留，不相互替代。

## 6. 联系人列表与单聊

### 6.1 联系人属于哪个身份

`Directory` 保存独立Go身份自己的联系人，每条包含 `user_id`、`name`、`discovery`，与Windows客户端的nr/qrp无关。只接受当前身份的列表；同名联系人可以并存，通过原始UserID选择，不按同名、IP或顺序替代。

```json
{"own_user_id":"自己的32字符身份","contacts":[{"user_id":"已确认的联系人ID","name":"联系人原始名称","discovery":"127.0.0.2:9011"}]}
```

上面是文件格式，身份/ID是说明占位值，不是可发送配置或真实联系人。列表及其端点放在Git忽略的私有目录。

- `-list-contacts -contacts /absolute/private/contacts.json`：只读列出联系人，不需要本机/上线参数，不生成身份或打开socket。
- 一次经授权的正常上线可额外指定 `-contacts-out /absolute/private/new-contacts.json`：双向READY完成后，将type4/发现包的UserID、1000的NAME和实际发现端点写入新列表；不是完整局域网名册。指定旧 `-contacts` 时保留旧列表其他条目，更新此次同ID联系人到新文件。
- 输出文件独占创建，不覆盖原列表；运行未就绪时保留输出文件但不写未经确认的联系人。已有文件碰撞直接停止，不换文件名重试。
- 列表只是已知对端资料，不保证当前在线。发送还会比较实际发现包和READY的UserID；端点上出现不同身份则停止。

### 6.2 默认只校验单聊参数

```bash
go run ./cmd/nwtnode \
  -local 127.0.0.1 -prefix 127.0.0.0/8 \
  -name Synthetic-Node \
  -identity /absolute/private/identity \
  -record /absolute/private/new-attempt.jsonl \
  -contacts /absolute/private/contacts.json \
  -user-id '列表中已确认的联系人ID' \
  -message '完整纯文本正文'
```

示例仍是只读校验，未设置 `-run`。`-remote`可省略，取选定联系人的发现端点；如显式提供，必须一致。`-send`也不隐含 `-run`；真实发送必须两者同时指定。带聊天参数的 `-run` 若缺 `-send` 则报错，不静默丢弃正文后上线。

原始正文限制为 **1..4096 UTF-8字节**，这是本实现预算，不是已证明的厂商上限或真实可用长度。保留实际LF/CRLF、emoji及普通空格；与IPC接口不同，这里不把字面 `\\n` / `\\r\\n` 转换成换行。URL作为普通文本，不自动拆成链接片段。完整字段、JSON及六实体依据见[聊天静态契约](../research/reports/native-protocol/chat-contract.md)。

### 6.3 一次提交与结果

Go调用者在 `ClientConfig` 提供 `Contacts` 与 `Chat: &nativeproto.ChatRequest{UserID: selectedID, Text: text}`，再按已有方式 `NewClient` / `Run`。构造时检查列表归属、目标ID/端点、正文预算并准备一次不可变帧；没有网络发送。

运行完成双向1000/1018后：先调用阶段记录器保存 `chat_send_planned` 和MSG_ID，成功后才调用一次传输Send。CLI记录器每条JSONL后Sync，另保存目标ID/正文的SHA-256与字节数，不保存正文。MSG_ID为本进程递增uint64（JSON结果中以十进制字符串表示），正文另生成新GUID；不接受调用者指定原始XML、时间、flag或复用历史MSG_ID。

| 结果字段 | 含义 |
|---|---|
| `submitted` | 本次1004已被ENet Queue接受；不是已经显示 |
| `transport_acknowledged` | 对端传输层确认该次可靠数据 |
| `business_acknowledged` | 收到同一peer、本次MSG_ID的1005 |
| `receipt_received` | 收到本次MSG_ID的1006；其触发条件未恢复，不称为人工已读 |
| `delivery` | 始终为 `unknown`，独立接收观察另存，不凭ACK生成 `recipient_observed` |

记录失败则不提交；提交后超时、取消或断开不生成新标识、不重连、不重新提交。ENet可以重传相同可靠命令，这是传输行为，不是第二次聊天调用。正常时仍等到活动时限再发1017并断开，期间接收回执；没有1005也不伪造成功或自动补发。未知/失败记录继续保留。

随后另经授权已向PVE-WIN10发送 `NWT-NATIVE-USER-TEST-0001` 一次，获1005并由用户确认仅新增一条、发送者和全文正确，见[真实接收验收](../research/reports/native-protocol/native-user-message-confirmation-20261025.md)。这验证了该短正文的紧凑JSON互通；字面实体、emoji、多行、长正文及其他显示边界尚未真实验证。原运行结果仍保留unknown，用户确认单独存为recipient_observed。

## 7. 本轮开发验证

- Linux amd64/386：全部 `internal/...` 与非托盘命令的测试、vet通过；单聊loopback分别覆盖主动/被动和无回执/1005/1006。
- amd64：协议包及两条命令的race连续3次通过；单聊/回执解码模糊测试2 worker、5秒通过。
- Windows amd64/386：全仓交叉构建与vet通过；没有在Windows运行测试或连接真实客户端。
- 要求的Linux两种架构全仓测试与vet均已执行，但未通过原有 `cmd/nwttray` 依赖：amd64报 `exec: "pkg-config": executable file not found in $PATH`，386报 `undefined: nativeLoop/registerSystray/...`。这些错误在本轮改动前已存在；未安装依赖或修改托盘模块。
- 可选官方ENet参考驱动测试本轮未设置 `ENET_REFERENCE_BINARY`，明确跳过；不把历史独立互通当作本轮重新运行。
