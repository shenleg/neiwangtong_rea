# 原生聊天静态契约：1004、1005、1006及3011边界

本文恢复内网通3.4.3055的聊天字段、普通文本表示和回执模板，供独立节点实现使用；只记录静态事实与明确的实现建议，不记录真实聊天结果。

后续另经授权已完成[固定短正文一次真实接收验收](native-user-message-confirmation-20261025.md)，确认紧凑JSON在该正文上的互通；本文静态调查时的未知范围保留，不能把该短正文结果扩展为字面实体、长正文或3011已验证。

## 1. 结论与范围

**普通在线单聊已有足够静态依据实现1004编码：外层MSG_ID是无符号64位递增消息编号；MSG是JSON内容对象经六项实体替换后的文本；普通不隐藏路径使用RECEIPT=0、MSG_TIME=0、OFFLINE=0、HIDE_RECORD=0。1005与1006均只带MSG_ID，但根名和命令不同。**

- 样本为`/root/reverse-lab/extracted/app/ShiYeLine.exe`，Windows x86，ImageBase `0x400000`，SHA-256 `e7caef3fe744f9fe6ba90dd1d53b7e64241cb54020a8bf37be5e756e2fe881b1`。以下地址均为该样本静态VA。
- 先复核`protocol-spec.md`第6/7/9节，使用`python3 -I`选择性读取已有`static-evidence.json`和IPC正文证据。没有全量打印大证据包。
- 使用`reverse-engineer-anything` skill。首次`binary_session`显示无打开目标；本任务自行打开匹配样本并导入已有快照。新批量反编译因Ghidra启动330000ms超时，7项均失败，没有把失败查询写成新的成功反编译。此后使用已有REA记录与本地GNU objdump定点核对，不重复启动大分析。超时记录仅检查结构/失败字段，重复诊断日志未完整阅读、不作为聊天结论依据。
- 会话完整证据已导出到`/tmp/nwt-chat-contract-rea-evidence-20261025.json`，随后关闭本任务打开的目标。没有关闭任务开始前的他人会话。
- 新独立选集`chat-contract-evidence.json`保存17条原始REA记录及`local_*`本地静态观察；后者**不是REA EvidenceID**。本次没有运行客户端、连接节点、发送、读取进程内存、聊天记录或凭据；没有改Go或既有文档。
- 后续任务明确先实现单聊，故3011的群加入/同步及完整成员接收条件保留为未完成项，不以单成员投递冒充整群发送。

## 2. 1004外层字段

`0x65ca70`的模板与命令号见已有[N02]；它按`%llu`写MSG_ID/MSG_TIME，以`%u`写归一化布尔值，最后交命令`0x3ec=1004`。

```xml
<X_SEND_MSG docver="1"><MSG_ID>1</MSG_ID><RECEIPT>0</RECEIPT><MSG>实体替换后的JSON</MSG><MSG_TIME>0</MSG_TIME><OFFLINE>0</OFFLINE><HIDE_RECORD>0</HIDE_RECORD></X_SEND_MSG>
```

这里的`1`只是合成示例编号，不是固定消息编号。普通路径各值的数据流如下。

| 字段 | 确切静态依据 | 实现可用结论 |
|---|---|---|
| `docver` | `0x65ca70`格式实参固定1 [N02] | 字面`"1"` |
| `MSG_ID` | `0x4b9590`低32位add 1、`0x4b959b`高32位adc；`0x5bc200`读取该值，`0x60d930`以`boost::any::holder<unsigned __int64>`保存 [C1、C2、L1] | `uint64`，十进制，无负号；不是GUID、UserID或本地连接路由号 |
| `RECEIPT` | `0x60d930`第三业务参数构造`holder<bool>`时直接写0 [C2]，packer输出`(value != 0)`的`%u` [N02] | 本条普通路径输出`0`，不是XML文本`false` |
| `MSG` | 同一参数列表中的宽字符串，经`0x659a80`转换后代入`%s` [C2、C6、N02] | 见第3节，不直接代入用户原文 |
| `MSG_TIME` | `0x4b95b0–0x4b95b3`压入三个0，结合`0x5bc200 → 0x60d930`参数映射，前两个0组成此字段的低/高32位 [C1、C2、L1] | 普通在线发送使用`0`；不是Unix毫秒或GetTickCount |
| `OFFLINE` | 上述第三个0经`0x60d930`保存为bool [C1、C2、L1] | 普通在线发送输出`0` |
| `HIDE_RECORD` | `0x4b95a1–0x4b95ac`传入`0x4b0360`结果的非零判断；`0x4b038c`使用键`HideRecord`，`0x4b03d4–0x4b03d8`按配置结果返回bool [L1、L2] | 不隐藏的普通入口显式选`0`；不能说原客户端此值永远为0 |

### 2.1 MSG_ID的初值边界

计数存储为`0xb28888/0xb2888c`。该地址位于PE `.data`虚拟尾部：RVA `0x676000`、VirtualSize `0xc7e24`、SizeOfRawData `0x2ae00`，字段在节内偏移`0xb2888`，加载映像初始8字节为零。[L3]

本次对objdump完整反汇编的**直接绝对地址引用**检查只发现读取及递增，没有发现直接赋初值。尚未证明所有启动流程不存在指针别名写入。因此：**独立实现自己的进程内计数从0开始、首次取1，是有静态模式依据的实现选择；厂商所有运行态初始化路径仍未完整恢复。** 不声称恢复了跨重启持久化规则或接收端全局去重键；不从Windows进程复制计数。

### 2.2 MSG_TIME单位

本条普通在线发送实际传`0`，已经可以固定该值。`0x4b9320`另有`__time64(NULL)`，其结果用于本地记录构造，不是这里压入1004的线上MSG_TIME；`0x5bc200`中的GetTickCount用于待确认发送记录及活动时间，也不是MSG_TIME。[C1、C2、L1]

**非零MSG_TIME的时间基准/单位未确定。** 不能把附近的`__time64`或GetTickCount直接当成线上字段来源；本任务不为离线历史消息构造非零时间。

## 3. MSG普通文本内容

### 3.1 JSON结构与两种不同标识

下面展示已恢复的IPC普通文本结构，排版为便于阅读；不是原生writer输出逐字节样本。[C3–C5]

```json
{
  "ver": "6.0",
  "app": "shiyeline",
  "id": "00112233445566778899aabbccddeeff",
  "type": "0",
  "ft": {"s": "10"},
  "dt": [{"txt": {"t": "normal", "v": "合成正文"}}]
}
```

- `ver="6.0"`、`app="shiyeline"`：`0x4b84cb–0x4b8502`和`0x4b855b–0x4b8592`的常量及复制；原始宽字符串在`0x9d0b00/0x9d0b08`。[C3、C5]
- `type="0"`：内容初始化`0x5f60c0`将对象`+0x54`设0，`0x5f8380`通过`0x593ce0`的`%u`产生**字符串**后写入`type`。[C4]
- `ft`在**根对象**，与`dt`同级，不在`txt`中。初始化关闭其他字体标志；`0x4b85bc/0x4b85c4`显式启用大小并写10，serializer把大小转换为字符串，写`ft.s="10"`。[C3、C4]
- 普通片段类型0、子类型0写为`dt`数组中的`{"txt":{"t":"normal","v":...}}`；`0x5f8ae0–0x5f8c4e`及常量`0x9e5304`。[C3、C5]
- 内容`id`是每次正文生成的**32个小写十六进制字符**：`0x4b8526`调用`0x592690`，后者将16个GUID内存字节按`%02x`逐字节连接。`0x592658`调用GUID生成导入，再复制16字节；它不同于外层递增的`MSG_ID`，也不同于固定节点UserID。[C3、C7、L4]
- 可复用独立节点既有GUID生成函数的**新调用**，但不得直接复用节点UserID值，也不得把UUID带连字符的标准显示形式放入该字段。新生成失败应返回错误，不复刻原客户端的固定失败常量。

这里的版本、类型、字体大小在JSON中均为字符串，而非数字。JSON对象键的先后及writer缩进不是业务字段；原生writer有自己的排版并追加末尾LF。紧凑JSON是独立实现的序列化选择，不是声称与原生输出逐字节相同；接收内容入口`0x5f6dd0`先交JSON parser，`0x5f6ee5`为解析调用，见新证据的`local_json-parse-entry`。本轮没有进行网络字节一致性验证。

单个`normal`片段是明确存在的普通文本表示。IPC原构造器还会识别链接并拆成类型3片段；独立纯文本API将URL保留在一个normal片段，是主动选择纯文本，不宣称复刻IPC所有富文本分片行为。

### 3.2 编码与实体替换顺序

`0x5f8380`产生JSON；`0x659a80`对整个JSON宽字符串依次执行以下替换，再由`0x408950`通过CP65001编码为UTF-8，交给业务封包器。[C3、C6]

| 顺序 | 输入 | 输出 |
|---|---|---|
| 1 | `&` | `&amp;` |
| 2 | `"` | `&quot;` |
| 3 | `<` | `&lt;` |
| 4 | `>` | `&gt;` |
| 5 | `'` | `&apos;` |
| 6 | 普通空格U+0020 | `&nbsp;` |

最后一项常量`0x9d007c`原始字节为`20 00 00 00`，不是U+00A0。[L3] 替换顺序重要：先替换原有`&`，后产生的实体不再经历第一步。不能对用户原文先做XML替换再JSON序列化，也不能仅用标准XML五实体编码器替代该六项规则。JSON字符串自身仍须正确处理引号、反斜杠与控制字符；不存在Base64或CDATA包装的依据。

1004接收虚表条目为`0x65ce10`。已核对的`0x65d054–0x65d104`从MSG节点取得文本、调用`0x44cbd0`做UTF-8→宽字符串，再复制；`0x65d0ff`调用的`0x5fc9f0`只是临时宽字符串释放，**不是第二次实体还原**。[L3、L5]

这段没有再调用六项反向Replace；XML parser内部、上层完整内容处理和字面`&nbsp;`/`&amp;`的最终显示保真尚未追完。**不得据此添加猜测的第二轮unescape。** 独立解码器若按标准XML读取原生MSG，需要单独考虑原生非标准`nbsp`实体；将其映射为空格是一项实现设计，不是本任务证明了所有嵌套实体都只解一次。

## 4. 1005与1006

已有packer证据给出以下精确输出。[C8]

```xml
<X_SEND_MSG_ACK docver="1"><MSG_ID>1</MSG_ID></X_SEND_MSG_ACK>
```

- 命令`1005=0x3ed`，packer `0x6747c0`。
- 除`docver`和无符号64位十进制`MSG_ID`外，模板没有时间、用户、状态码或正文。

```xml
<X_SEND_RECEIPT docver="1"><MSG_ID>1</MSG_ID></X_SEND_RECEIPT>
```

- 命令`1006=0x3ee`，packer `0x676660`。
- 同样只带`MSG_ID`，准确根名是`X_SEND_RECEIPT`，不是`X_SEND_MSG_RECEIPT`。

1005是应用消息ACK；1006是独立receipt命令，不是1005别名，也不是ENet传输ACK。**何时产生1006、是否由查看窗口/人工已读触发，以及不同RECEIPT选项的完整状态机，本轮未恢复。** 不能把收到1005或1006直接记为人工已读或`recipient_observed`；普通RECEIPT=0也不承诺一定收到1006。解码结果应保留命令种类和消息编号，按当前peer与已提交编号匹配。

## 5. 3011已知部分与停止点

已有[N02、N26]足以确认模板、无符号64位MSG_ID、相同MSG实体包装及HIDE_RECORD的0/1表示。

```xml
<X_QGROUP_SEND_MSG docver="1"><QGROUP_ID>...</QGROUP_ID><MSG_ID>...</MSG_ID><MSG>...</MSG><HIDE_RECORD>0</HIDE_RECORD></X_QGROUP_SEND_MSG>
```

`0x617600 → 0x61b350`按成员枚举非零本地连接路由，再逐路由调用`0x6114f0`。这不是把QGROUP_ID交给单个固定服务器广播。单成员3011只能叫对该成员的一次投递，不能称为向整群发送。[N26]

本轮没有恢复群回执的确切命令/字段，也没有完整证明3011接收时“群存在、发送者是成员、本机是成员”等各条件，不能填猜测。定点后续入口：3011 packer `0x679c10`；其虚表`0x9e96f0`的unpack入口`0x679f30`。[L3] 群资料3001/3004/3005、独立身份加入状态及成员来源均不由本报告解决，现阶段不开放群发送API。

## 6. 最小API与实现限制

以下是基于静态契约的建议，不是厂商导出的源码API。

- 对外只接收`SendUser(userID, text)`：联系人列表提供并人工确定目标；网络发送前检查连接握手身份与userID相同。用户ID不加入1004 XML中。
- 内部准备一次不可变消息，返回`MessageID uint64`和UTF-8业务正文；内容GUID每次新生成，MSG_ID由本进程计数器分配，普通路径固定三flag为0、MSG_TIME为0。不要把GUID、时间、flag或原始XML开放为普通发送参数。
- 回执解析返回`{Command, MessageID}`，区分1005/1006，保留`delivery=unknown`；不凭socket、Queue或ACK修改成独立接收成功。
- **4096 UTF-8字节是独立实现主动设置的原始正文预算，不是厂商上限，也不是由本轮逆向证明的可用长度。** 还须验证UTF-8并正确构造JSON/实体层。历史几十万字静态序列化预算不用于新接口产品限额。
- 一次调用最多提交一次，不自动重试。默认合成/loopback测试不等于真实聊天验收；后续仍需明确目标、完整正文和次数授权。

未完成项汇总：非零MSG_TIME单位；厂商计数所有启动/重启路径与去重范围；1006触发状态机；字面实体、多层unescape与完整接收显示；紧凑JSON的真实互通验收；3011接收成员条件、群回执与群同步/入群。以上均没有用既有IPC送达或节点上线成功代替静态证明。

## 7. 证据索引

以下C编号均位于新文件`chat-contract-evidence.json`；REA记录保留原始EvidenceID。N编号继续指既有`static-evidence.json`与`protocol-spec.md:207–232`。

| 编号 | 内容 | EvidenceID / 本地观察 |
|---|---|---|
| C1 | 普通发送调用方、计数器、零时间/离线参数 | `ev_585b8c6bd9d12509095fdc5e1a15e3bece344edf8686d9e29b1bfb112368edf7` |
| C2 | 发送参数传递、RECEIPT硬编码0与uint64 holder | `ev_b6dc4edbaa2b5733ca7f515174381cf19fab02cf1634c1532d03e9340b58176b`；`ev_568b4cb686f551544b37c852feea9e680ea84c7d659f84915220a8e48ee0d1c2` |
| C3 | 固定正文构造、完整JSON serializer指令 | `ev_c83cdc8ee6b746de0094d26fdb14305938dc67c7df11cfa0f19ff4f2801f1867`；`ev_938bf5f06cb284edba95b7ef9fe078b030535f4a5502c297abb2ec1d76b80b8e` |
| C4 | 根type/字体初始化、数值转字符串、片段初始化 | `ev_d170920cd26e18467c485ab940c73de152b28eacf1781d687f40c3ad8eb26f78`；`ev_ab1d66098cbfc6586f5c450fb5fd93c3d80e4184cda69650b0c5ea7019ea0edc`；`ev_c432541649d74517f0778e1c755d791aa8ecfac30f0281755ca6da1dc21567a8`；`ev_4ebb7d055688ff1d34db554e8c022929f1e828252727b4b62ee2168ac5d2801e` |
| C5 | 版本/app、normal、JSON字段名原始字节 | `ev_6a267e043b5dfb2162b1301a415ae570302da328c67ae94bfe5987ecf9f7674b`；`ev_cc5da11fa4426649b269fb8842f7ca2b70459523c42f03f9c6d7e5e92f98a6be`；`ev_75c25bf114e8b16429f49c1d302464399c34c192b2bb77139c5ce52fa4c5053b` |
| C6 | 六项实体替换、CP65001输出 | `ev_fbfe0feec9ccaecc1a579bda2a3f0ef1634f392b35b942591273d7f595013b04`；`ev_06a57fdfca53d486600c382450bc7c9b60f0484ae2bd9b84dd5b48d67203e8d0` |
| C7 | 内容GUID逐字节32位hex | `ev_8459002a53aa85acbf2e00db29b9babedc14bb51854e37a4300f5c0d7aea7572` |
| C8 | 1005、1006完整packer | `ev_b01d4e830136c002889231255181c934c95a39d33cc3721326227043cad38f6d`；`ev_a0514cc5a7bad2d6d95abb283d55610f6bd9c8ad3a415c1f3ae36c4e13bb9022` |
| L1 | 调用方连续指令，覆盖free误标后的fallthrough | `local_counter-online` |
| L2 | HideRecord键和bool配置返回路径 | `local_hide-record`；`local_hide-read-bool` |
| L3 | PE节映射、零初始化、常量、1004/3011虚表 | `local_static_bytes` |
| L4 | GUID生成导入调用与16字节复制 | `local_guid-source` |
| L5 | 1004 unpack、MSG取值复制、临时字符串析构 | `local_1004-unpack-time`；`local_msg-receive-copy`；`local_temporary-string-destructor` |

反编译存在`0x87fc86`释放函数被误标为no-return的历史限制，尤其不能相信`0x4b0360`旧伪代码末尾的恒0；本报告的真实配置bool结论以`0x4b03d2–0x4b03db`连续指令为准。没有修改样本或REA元数据来修补分析器。
