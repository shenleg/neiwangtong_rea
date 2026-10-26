# 原生协议群聊

本功能在独立Go身份的正常协议会话中发送3011，不调用IPC，不读取Windows客户端群数据库，不主动创建或加入群。已完成`qun3`连续两条的[真实独立接收验收](../research/reports/native-protocol/native-group-confirmation-20261025.md)。字段与版本依据见[群静态契约](../research/reports/native-protocol/group-chat-contract.md)。

## 目标与提交规则

Go接口：

```go
result, err := client.SendGroup(ctx, groupID, completeText)
```

- `groupID`必须是本身份正常同步得到的原始群ID，不接受名称/IP替代。
- MSG_ID使用进程内统一递增编号。每条群请求只生成一个MSG_ID和正文GUID，所有成员路由共用同一3011；各连接仍由自己的常驻运行循环串行执行发送。
- 群聊JSON正文及六实体转换复用单聊。原始纯文本预算为1..4096 UTF-8字节，保留实际LF/CRLF；这是实现预算，不是厂商最大值或已经真实验证的可用长度。
- 发送意图持久化成功后才进入一次Queue；传输层可重传同一可靠数据，但不创建第二次聊天请求，不自动重连或补发。

共享Host（Web/API）支持多成员群。发送前须已同步最新完整成员且本机在群内；按用户选择向在线READY且支持群消息的成员投递，离线、未READY或能力不支持的成员列为skipped/未投递，不阻断其他成员，不离线存储、不自动补发。不要求每个成员重复提供群快照。所有可用成员共用一个MSG_ID/正文GUID，各路由最多一次应用提交，并发等待结果。至少一个可用成员且它们全部传输确认时HTTP200/error为空；存在skipped时顶层传输ACK仍false。实际路由失败时HTTP502并保留结果；Host发现全体不可用时返回错误，不分配MSG_ID、不Queue，并保留全体skipped；原网页接口返回502，公开接口若预先发现全员未就绪则返回503。群版本变更或日志失败仍失败，不补发。传输层重传同一可靠数据不属于再次应用提交。

旧单Client与`nwtclient`实验CLI仍只维护一个明确对端，完整成员必须恰为本机和当前READY对端，不能包含其他成员。共享Host多成员实现仅经合成/loopback验证，尚未真实验收；本文开头的历史双成员真实报告不变。

## 正常群同步

`ClientConfig.GroupChat=true`显式启用群同步与发送，同时要求本机声明兼容的`QGROUP`及`QGROUP_MSG`版本。`nwtclient`声明`2:1`并启用；旧`nwtnode`限时入口不因此自动发送群请求。

- 3002 `X_QGROUP_PUSH_INFO`只有基本资料，不能当作完整成员表。程序收到新成员版本后撤销旧快照，并发送一次3006 `X_QGROUP_REQ_USER`。
- 3003 `X_QGROUP_PUSH_USER`直接携带INFO和USER，可建立完整快照。
- 3007 `X_QGROUP_REQ_USER_RSP`通过RET区分资料与成员身份：`0x4`表示INFO/USER存在，`0x10`表示请求者是成员。典型RET=20才同时满足；RET=12虽有资料也不能当作本身份已加入。
- `USER`中的成员是`<ID name="名称">原始UserID</ID>`；按元素文本读取ID，不把name或列表顺序当ID。
- 3004/3005只请求/回复基本资料，不能取代3006/3007。正常入站3004可提供原始群ID种子，缺少快照时向同一对端请求3006，不伪造3005。收到的群不是穷尽列表；未收到推送不表示本身份没有群。3012是邀请命令，不是群消息ACK。
- 原厂群INFO模板把`QGROUP_INFO_VER`错闭合为`GROUP_INFO_VER`。只在群同步的解析副本中替换此确切结束标签，然后仍执行严格XML校验；不修改原始帧、不启用通用宽松解析。

完整快照先交给可选的`ObserveGroup`串行保存，成功后才写入可发送表并通知`group_synchronized`。回调错误终止本次运行。命令将群名、ID、版本与成员仅保存为私有`group_snapshot`，stdout阶段不含成员与群名，也不保存聊天正文。

## 命令输入

`cmd/nwtclient`沿用[常驻命令](nwtclient.md)的显式身份、端点及记录参数，stdin每行可提供一个群聊请求：

```json
{"group_id":"本身份同步得到的原始群ID","text":"第一条完整正文"}
{"group_id":"本身份同步得到的原始群ID","text":"第二条完整正文"}
```

`group_id`和单聊的`user_id`必须二选一，与`text`各出现一次；不允许未知字段、重复字段、原始XML或调用方提供MSG_ID。群聊不依赖`-contacts`；群身份与成员检查由会话自身完成。

每条请求先Sync目标ID/正文SHA-256及正文UTF-8字节数，不保存聊天正文。坏行返回错误且不发送；关闭stdin不触发下线。取消或传输失败后的未知结果不补发。

## 结果含义

单Client的`submitted`只代表Queue接受，`transport_acknowledged`只代表传输确认。Host群结果的顶层`submitted`表示任一成员Queue接受，`transport_acknowledged`仅完整名单全部非己成员传输ACK时为true，存在skipped仍false。`recipients`按成员保留`user_id`、`peer_index`、`skipped`、`submitted`、`transport_acknowledged`及`error`；skipped与error始终输出，无错误时error为空字符串。计划时无就绪连接或能力不支持的成员skipped=true，submitted及transport_acknowledged均false，error说明原因；无就绪连接时peer_index=0。群快照等计划前检查失败省略recipients；全体不可用时可返回全体skipped。非2xx仍保留已取得的逐成员事实，部分提交后不得重复发送整群。网页区分“在线成员传输已确认，另有N名成员未投递”与实际路由失败；至少一个非skipped路由且它们全部确认时清稿，HTTP错误或实际失败保留草稿，不自动补发。

群`business_acknowledged`及`receipt_received`恒false。单peer的3011事件或单聊1005/1006都不得确认同编号整群结果。`delivery`始终为`unknown`；只有独立接收者核实正确群、发送者、全文、顺序与新增总数后，另存`recipient_observed`。

真实验证仅使用当次明确授权的身份、对端、群、完整正文、次数与最长在线时间。历史单聊成功不是群聊成功，也不延续真实连接授权。

## 多成员实现验证（2026-10-26）

- 合成loopback接收端验证三个非己成员各收到一次相同群ID、MSG_ID、正文GUID与完整正文；连续及并发群请求在各成员上的消息顺序一致。只由一个对端提供完整群快照，其余成员无需重复推送。
- 离线、未READY或群文本能力不兼容的成员计划时跳过，其他可用成员仍各提交一次；全体不可用时不分配消息编号、不Queue。本机不在群、请求取消、成员版本变化及整群发送意图记录失败时不提交；实际单路由失败仍保留该成员结果及其他成员的传输确认，不重新提交。
- Web测试验证至少一个成员可用时可发送、全体不可用时禁用、HTTP502仍返回逐成员结果、同一请求标识不重发，以及返回切片不能修改已缓存的结果。
- Linux amd64/386相关测试通过，协议/Web/命令入口race连续3次通过；Windows amd64/386全仓交叉构建和vet通过。全仓Linux测试/vet仍受原有托盘依赖阻塞：amd64缺少`pkg-config`，386缺少systray平台符号。
- 页面使用无socket合成DOM执行真实`app.js`，覆盖全部成员确认、在线加离线时清稿并显示未投递原因、全体离线禁用且不发送、HTTP错误及实际路由失败保留草稿、单peer ACK不修改群聚合结果及1004/1005/1006单聊事件分别回填。本次没有运行真实浏览器或真实多人群验收。

可重复的主要检查：

```bash
ENET_REFERENCE_BINARY= go test -count=1 -timeout 45s ./internal/nativeproto ./internal/nativeweb ./cmd/nwtweb
ENET_REFERENCE_BINARY= GOARCH=386 go test -count=1 -timeout 45s ./internal/nativeproto ./internal/nativeweb ./cmd/nwtweb
ENET_REFERENCE_BINARY= go test -race -count=3 -timeout 45s ./internal/nativeproto ./internal/nativeweb ./cmd/nwtweb
node tools/nwtweb-browser-check.mjs --synthetic-only
```

实现分别位于`internal/nativeproto/host_group.go`（整群计划与结果聚合）、`client_resident.go`（当前连接串行提交）和`internal/nativeweb/manager.go`（可发送判断与结果去重）。所有验证仅用合成资料，不连接真实客户端、不广播到真实子网、不发送真实聊天。
