# nwtnode：独立新节点限时单播上线

`cmd/nwtnode` 将显式本机身份、304字节发现/节点控制、ENet和应用1000/1018/1016/1017串成一次限时运行。**已完成单对端真实上线及独立原客户端 UI 验收**，见[上线验收记录](../research/reports/native-protocol/native-node-online-confirmation-20261025.md)。

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

程序没有聊天/文件/远控发送入口。不支持的业务只记录命令号，不保存其正文、不假造成功响应，也不把握手ACK当送达。

新节点未提供TCP或reverse listener时不公告这些端口；当前只运行一个指定对端，不冒充多peer共享host或所有主/临时连接规则已实现。

## 4. 记录与验收

本项目私有目录 `local/nativeclient/` 已被Git忽略，内含自己的身份、程序和逐次JSONL记录；其中真实端点、UserID及会话值不提交。

阶段记录不含远端姓名/身份、群资料或聊天内容。它记录消息版本、控制命令号、状态、传输ACK和收到的DISCONNECT data。DISCONNECT data是原始应用数据字段，`0`不自动解释为某个具体拒绝原因。

验收分三项：

- `protocol_ready=true`：已观察到双向1000/1018。
- 用户在原客户端明确看到新名称/IP在线：另存 `peer_ui_online_observed` 观察；不改写原日志的unknown字段。
- `graceful_exit=true`：本次应用QUIT与传输关闭成功；不自动宣称UI已人工确认下线。

没有聊天消息，因此这项上线验收不产生 `recipient_observed`。发送1004/3011需要另行开发与具体目标、完整正文、次数授权。

## 5. 测试

默认测试使用合成资料和loopback端点；`cmd/nwtnode` 自身测试缺省只读、参数拒绝及记录碰撞，不向真实节点发包。

```bash
go test -count=1 -timeout 45s ./internal/nativeproto ./cmd/nwtnode ./cmd/nwtprotocol
GOARCH=386 go test -count=1 -timeout 45s ./internal/nativeproto ./cmd/nwtnode ./cmd/nwtprotocol
go test -race -count=2 -timeout 45s ./internal/nativeproto ./cmd/nwtnode
```

模拟端现在检查消息版本协商，避免只解析格式便接受空MSG列表；另有非空必填资料、真实失败阶段的关闭回放和独立GUID/身份持久化回归。已有[官方ENet独立互通](native-transport.md)与本次实际内网通UI验收分别保留，不相互替代。
