# 原生协议群聊

本功能在独立Go身份的正常协议会话中发送3011，不调用IPC，不读取Windows客户端群数据库，不主动创建或加入群。已完成`qun3`连续两条的[真实独立接收验收](../research/reports/native-protocol/native-group-confirmation-20261025.md)。字段与版本依据见[群静态契约](../research/reports/native-protocol/group-chat-contract.md)。

## 目标与提交规则

Go接口：

```go
result, err := client.SendGroup(ctx, groupID, completeText)
```

- `groupID`必须是本身份正常同步得到的原始群ID，不接受名称/IP替代。
- 与`SendUser`共用常驻运行循环、同一连接和进程内递增MSG_ID；每条正文另生成独立GUID。
- 群聊JSON正文及六实体转换复用单聊。原始纯文本预算为1..4096 UTF-8字节，保留实际LF/CRLF；这是实现预算，不是厂商最大值或已经真实验证的可用长度。
- 发送意图持久化成功后才进入一次Queue；传输层可重传同一可靠数据，但不创建第二次聊天请求，不自动重连或补发。

当前传输只维护一个明确对端。发送前检查完整成员列表：本机和当前已就绪对端必须在列表中，且不能包含其他成员。存在其他成员时明确拒绝，不把只向其中一人投递称为整群发送。多成员投递需要多peer传输实现及另行取得真实连接范围授权。

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

`submitted`只代表Queue接受，`transport_acknowledged`只代表传输确认。单聊1005/1006不得确认同编号3011。`delivery`始终为`unknown`；只有独立接收者核实正确群、发送者、全文、顺序与新增总数后，另存`recipient_observed`。

真实验证仅使用当次明确授权的身份、对端、群、完整正文、次数与最长在线时间。历史单聊成功不是群聊成功，也不延续真实连接授权。
