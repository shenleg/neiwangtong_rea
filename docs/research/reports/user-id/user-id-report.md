# 内网通 3.4.3055：联系人列表 user_id 的来源与只读提取设计

本文恢复联系人对象、持久化表和 IPC `user_id` 的对应关系；只做定向静态分析，不实现或修改 Windows IPC Adapter。

后续补充：[nr codec 静态契约与合成兼容验证](../nr-codec/nr-codec-report.md) 已完成固定版本 `aes128cbc`、`legacy=1` 的比对、页大小恢复和合成已知答案测试。本文第 4.3、6.1、7.2 节的 codec 待验证描述保留当时状态，以补充报告的后续结果为准；真实私有 `nr`、Windows 实例/身份归属和实际 ID 仍未读取验证。

## 1. 结论

**联系人列表自身存在可持久化的名称—user_id 映射，不需要从群成员提取。** 对本次样本，联系人来源是 `<数据根目录>\cache\db\nr` 中的 `OrgStruct_<当前身份标识>` 表：`Type = 1` 的记录表示原生用户，`Id` 列恢复到 `CUser +0x20`；`Data` 列包含 XML，`Name` 子元素恢复到 `CUser +0xb0`。IPC 的 `user_id` 分支按 `CUser +0x20` 对应的完整字符串键查找同一联系人管理器。[U1–U6、U11、U12]

**因此，正确的联系人映射是 `OrgStruct.Id ↔ Data/U/Name`，不是 `PriKey ↔ 名称`，也不是用户名、IP 或原生传输路由号。** 界面存在本地备注覆盖：用户眼前的显示文字不一定就是 `Data` 中的原始 `Name`。[U6、U7、U10、U13]

**尚不能直接用普通 `sqlite3 -readonly` 读取 `nr`。** 此路径向 SQLite 包装传入非空、样本内固定地址的密钥参数，并安装页 codec。它与历史 `qrp` 的空密钥打开路径不同。已恢复联系人表及映射，尚未验证兼容读取工具、真实数据库副本或 Windows 提取结果；不能宣布“实际联系人 ID 已取得”。[U8、U9、U20]

按操作者本轮补充要求，**第 6 节只设计联系人表读取，不采用 `QGroupUser`、群名称或共同群成员作为 ID 来源**。群成员相关证据只用于初始任务要求的类型一致性对照，不是提取方案。

### 关键结果

| 问题 | 结果 |
|---|---|
| IPC `user_id` 究竟是什么 | `CUser +0x20` 的原始宽字符串身份键；磁盘对应 `OrgStruct.Id` [U3–U6、U11、U12] |
| 联系人名称在哪里 | `OrgStruct.Data` 的 XML `U/Name`；内存对应 `CUser +0xb0` [U3、U6、U7] |
| 联系人是否必须依赖群 | 否，联系人管理器直接加载 `OrgStruct_*` [U2、U3] |
| 本地 SQLite 是否普通无密钥文件 | 该打开路径不是空密钥；普通 SQLite 的可读性未验证 [U8、U9] |
| 名称是否唯一 | 建表只有 `UNIQUE(Id)`，没有名称唯一约束 [U5] |
| 全新错误 ID 是否必然发送离线消息 | 否。先创建状态 4 的空名称对象，随后名称为空则不加入发送集合 [U11、U14、U15] |
| 已有离线用户 | 名称非空时可进入发送集合；状态 4 走本机离线保存分支，不走当次普通网络发送 [U14、U16] |
| 能否根据 IPC 返回 1 判定用户存在或送达 | 不能，窗口消息分发不返回这些业务状态 [H3] |
| 是否已修改或验证 `SendUser()` | 没有，本轮没有修改 Adapter、运行 EXE 或发送消息 |

## 2. 样本、方法与边界

- 目标：`/root/reverse-lab/extracted/app/ShiYeLine.exe`，内网通 3.4.3055，原生 x86 C++ / SkinUI。
- SHA-256：`e7caef3fe744f9fe6ba90dd1d53b7e64241cb54020a8bf37be5e756e2fe881b1`。
- REA 6.7.0、Ghidra 12.1.4；静态 VA，以 `0x400000` 为 ImageBase。
- Ghidra 分析配置摘要：`7810ea9437da59780820ec6fedb47ce47390841a95d527ddb6c3b3700fe180cb`，与历史一致。
- 先读现有 IPC 契约、gateway 报告和 QGroupId 报告，复用接收入口、原生单聊发送和窗口返回值证据。新会话仅对联系人加载、表结构、字段赋值、比较器及数据库打开/codec 作定向查询，没有逐个反编译全程序。
- 独立 REA 进程使用已有长启动超时，未修改客户端、Ghidra 标记或系统配置；完成后导出证据并关闭分析器。
- 没有打开真实 `nr`、`qrp`、配置或聊天数据库，没有读取登录凭据、运行时内存或真实联系人；没有运行目标 EXE、注入、发送消息或改动 `internal/ipc`。
- 以下“确认”均指静态代码确认；查询合成测试不是客户端运行证据。`FUN_` 名称、伪代码寄存器变量和错误类型不是原始源码契约。

## 3. 联系人数据模型与界面

### 3.1 唯一身份、名称、IP、路由号分离

以下偏移是本次样本的内部对象布局，用于静态解释，不是外部内存读取接口。

| `CUser` 相对偏移 | 已确认语义 | 证据 |
|---|---|---|
| `+0x18 / +0x1c` | 原生传输使用的 64 位路由值 | U10、U16 |
| `+0x20` | 用户身份字符串；IPC 用户表键及联系人 `Id` | U3、U6、U11、U12 |
| `+0x80` | IP 数值字段，序列化时由 `0x592250` 转成 `Ip` 文本 | U7、U10 |
| `+0xb0` | 原始用户名称字符串，对应 `Data` 的 `Name` | U3、U7 |
| `+0xe8 / +0x104` | 公司、部门文字，不是用户 ID | U7 |
| `+0x1e0` | 用户状态；4 对应已检查的本机离线路径 | U11、U15、U16 |

`0x4f4480` 取传入 `CUser +0x20`，在管理器 `+0x50` 的容器中查找/插入，再由 `0x4d78b0` 复制对象。后者的 `0x4d78d7–0x4d78e1` 明确把源 `+0x20` 复制到目的 `+0x20`。它不会把名称、IP、数据库自增记录号当作用户表键。[U6]

`0x4daae0 → 0x4dbfa0 → 0x4071f0 → 0x4071b0` 的比较按完整 UTF-16 代码单元和长度进行。已检查的比较过程没有大小写折叠、trim、名称解析或 IP 转换。因此提取后保留 ID 的原始字符、大小写与空格，不进行“规范化”。[U12]

本轮不据字符串形状制定 ID 正则，也不把 UI 中某些路径的 32 字符判断扩大为所有历史或导入身份的格式要求。

### 3.2 联系人列表的创建与加载

```text
0x442d60：主界面选择 UserItem.xml，配置用户树
  → 0x4a8600：初始化用户树
      → 0x4f4150：取得联系人管理器
          → 首次创建时 0x4f4ad0：加载持久化联系人
              → 0x5d1d80：查询 OrgStruct_<身份>，按 UpdateTime 降序
              → 0x5dd830：逐列解码记录
              → Type == 1：Id → CUser +0x20
              → Data/XML/Name → CUser +0xb0
      → 遍历同一管理器的用户表
      → 0x4aa820：按用户键创建 CUserItem
          → 0x4a6750：CUserItem 构造
          → 0x4a67e0：条目保存用户键到 +0x254
```

`0x4f4bf8–0x4f4c06` 是数据库记录 ID 到 `CUser +0x20` 的直接赋值；`0x4f4d40–0x4f4d4e` 是 `Name` 节点值到 `CUser +0xb0` 的直接赋值。不是仅靠 SQL 字段名称猜测对应关系。[U2、U3]

条目保存的 `+0x254` 是用于查找用户的键，`+0x270` 另存显示相关内容。创建和刷新不根据显示名称生成新的 user_id。[U2、U13]

### 3.3 名称、备注、搜索与点击

- `CUserItem` 刷新函数 `0x4a6c00` 查控件 `1002 / 0x3ea`，通过条目的 ID 和 `0x4b1930` 取原始名称；`0x4b1930 → 0x4b1830 → 0x4f4150 → 0x4f49e0` 回到相同的 `CUser` 容器。`0x4b19cc–0x4b19e6` 取出的字符串正是复制对象的 `+0xb0`。[U13]
- `0x4a6c00` 还调用 `0x4f13a0 → 0x4f1aa0` 取得单独维护的本地备注；`0x4b1d50` 在备注非空时优先返回备注，否则返回原始名称。因此“精确匹配显示文字”和“精确匹配 `Data/U/Name`”不是同一个操作。[U13]
- 搜索结果构造 `0x49c4b0` 接收并保存目标键，原生用户分支仍经 `0x4b1830` 解析对象，再使用用户名称/备注和 IP 信息填充 `SearchResultUserItem.xml`，不是按结果显示文字反算身份。[U18]
- 条目消息映射 `0x4a6570` 注册了 `1005 / 0x3ed → 0x4a7940`。该地址未被 Ghidra 建成独立函数，原始指令确认它读取虚表 `+0x140` 并作间接调用。这里**只确认一个条目事件跳板**，没有把它称为完整的联系人双击打开会话处理。普通单击、双击和搜索激活的完整消息传播尚未逐条恢复；这不影响数据库 ID 到用户表键的直接赋值证据。[U19]

**本轮建议先以客户端原始名称匹配。** 若用户只提供本地备注，应明确返回“该输入是备注，尚未建立备注映射”，不能用模糊匹配或尝试发送消除歧义。本地备注的独立持久化路径不在本轮已恢复的联系人提取契约中。

### 3.4 用户 ID 从哪里进入客户端

观察到两个来源，不能简化成“ID 一定由中心服务器分配”：

1. **本地已有联系人缓存**：`OrgStruct.Id → 0x5dd830 → 0x4f4ad0 → CUser +0x20`。[U2–U5]
2. **原生对端连接元数据**：`0x5bd4c0` 接收路由、IP 和身份字符串，明确把身份参数写入新建 `CUser +0x20`。另一个已确认的原生身份交换入口是 `0x6596f0`：它在 `X_READY` 中取 `USER_ID`，按 UTF-8 转宽字符串后进入原生事件。两处都说明网络侧存在独立于名称/IP 的身份字符串，但本轮没有逐层恢复两处事件之间的全部参数传播，不能把它们画成已经证明的直接调用关系。缓存写入侧 `0x4d5830` 则明确用 `CUser +0x20` 构造 `Type = 1` 的记录，`0x4aefa0` 把名称等序列化到 `Data`。[U7、U10、U17]

已确认连接元数据可以填充内存身份、磁盘缓存可以恢复同一个身份字段；**没有恢复所有身份生成、中心服务同步、重装、重置或冲突处置过程**。不能据此承诺 ID 跨重装或跨所有账户永久不变，也不能声称收到一个 `USER_ID` 就完成了真实性验证。

## 4. 本地联系人存储

### 4.1 文件路径

```text
0x441d90
  → 0x5b8260
      → 0x5b7d20
          → 0x5b7800：目录对象 +0x20 的数据根目录 + "\cache"
          → 追加 "\db"
      → 追加 "\nr"
  → 0x5d1570：以此文件打开组织联系人数据库
```

三个字面串已分别读取原始 UTF-16LE 字节：`\cache @ 0x9e169c`、`\db @ 0x9e172c`、`\nr @ 0x9e1910`。因此联系人载体是 **`<数据根目录>\cache\db\nr`**，不是 `<数据根目录>\data\qrp`。[U1]

数据根目录选择复用历史 G16–G19：[H1]

| 分支 | 联系人数据库位置 |
|---|---|
| EXE 同目录存在 `Green` | `<EXE目录>\Syl\cache\db\nr` |
| 现代 Windows，公共根目录下已有旧命名目录 | `<FOLDERID_Public>\ShiYeLine\cache\db\nr` |
| 现代 Windows，未命中旧目录 | `<FOLDERID_Public>\Nwt\cache\db\nr` |
| 旧 API 分支 | `<CSIDL_COMMON_APPDATA>\ShiYeLine\cache\db\nr` 或 `...\Nwt\cache\db\nr` |

`FOLDERID_Public` 通常是 `%PUBLIC%`，不是 ProgramData。根据当前实例 EXE 路径和实际目录分支定位，不能递归找一个同名 `nr` 就认定属于当前账户。

### 4.2 表名、字段和身份范围

`0x441dc8` 传入全局身份字符串 `0xb28df0`；`0x5d15c9–0x5d15d3` 把它保存到数据库对象 `+0x10`；`0x5dd000` 用该字符串构造 `OrgStruct_%s`。本机 `CUser` 则由 `0x5baf30` 从全局 `CUser @ 0xb28dd0` 复制，源对象的 `+0x20` 正好是 `0xb28df0`。所以联系人表后缀属于当前本机 `CUser +0x20` 这一身份空间，而不是名称、IP 或用户选择器本身。[U4、U6、H2]

`0x5da2d0` 恢复的建表结构：[U5]

```sql
CREATE TABLE OrgStruct_<当前身份标识> (
    PriKey INTEGER PRIMARY KEY AUTOINCREMENT,
    UpdateTime INTEGER,
    OnlineTime INTEGER,
    Type INTEGER,
    Id TEXT,
    Data TEXT,
    UNIQUE(Id)
);
```

- `PriKey`：本地行号，不作目标 ID。
- `Id`：组织条目的逻辑键；**必须结合 `Type = 1` 才解释为原生用户 ID**，不把其他类型条目的 Id 当用户。
- `Data`：该条目的元数据 XML；对已检查的用户序列化路径，根名为 `U`，包含 `Name`、`Sign`、`Sex`、`Company`、`Department`、`Phone`、`Mobile`、`Mail`、`Version`、`Ip`、`Flag` 等子元素。提取器只保留 `Name`，不输出其他个人资料。[U7、U21]
- `UpdateTime / OnlineTime`：持久化时间元数据，不能作为实时在线证明。
- 表没有名称列，不能写 `WHERE Name = ?`；`Name` 也没有唯一约束。`UNIQUE(Id)` 不等于“名称唯一”，且实际 schema、旧库迁移和 NULL 值仍须检查。

列解码已按指令核对：[U3]

| 列序号 | SQL 字段 | 临时记录偏移 | 用户加载用途 |
|---|---|---|---|
| 0 | `PriKey` | `+0x00` | 本地记录号 |
| 1 | `UpdateTime` | `+0x08` | 元数据时间 |
| 2 | `OnlineTime` | `+0x10` | 元数据时间 |
| 3 | `Type` | `+0x18` | 用户分支判断 `== 1` |
| 4 | `Id` | `+0x1c` | 复制到 `CUser +0x20` |
| 5 | `Data` | `+0x38` | 解析 XML，`Name` 复制到 `CUser +0xb0` |

写入侧 `0x5d8a40` 使用 `REPLACE INTO ... (UpdateTime, OnlineTime, Type, Id, Data)`，与读取侧一致，不是从 `PriKey` 重建身份。[U5]

### 4.3 数据库密钥与编码

`0x5d1570` 在 `0x5d15ff / 0x5d1610` 把固定非零地址装入 EBX，然后调用 `Kompex::SQLiteDatabase` 构造。历史指令已证明 EBX 会传入 `0x67d610`，非空时进入 `0x6f98e0`。该函数会设置页处理函数，`0x6f9800` 区分读取/写入模式并进入 `0x6f92c0`。这不是一个空的密钥兼容接口。[U8、U9、H4]

**已确认事实**：

- 本路径提供非空的样本内常量参数，不是这里从用户输入读取登录密码。
- 存在页 codec、口令派生和块处理代码，不能用“群数据库未加密”推断联系人库也未加密。
- `0x6f9550` 有 32 字节补齐，`0x6f95a0` 有 MD5 初始常量、50 次迭代及后续处理；块初始化 `0x6f7540` 使用 16 字节材料和 10 轮参数。[U20]

**强推断**：这些特征与旧式 SQLite AES-128 codec 相符。尚未和某个明确版本的公开实现逐项比对，也未用真实库或已知答案样本验证，故**不宣布它就是 SQLCipher、wxSQLite3 的某个版本，也不提供猜测的 `PRAGMA key` 命令**。[U20]

本轮不公布或将数据库密钥内容提交 Git。一个包含静态密钥局部字节的查询仅留在本地、已由 `.git/info/exclude` 排除；公共证据选编也排除此记录。未读取真实登录密码、令牌或运行时密钥。

## 5. IPC `user_id` 匹配、未知用户和离线用户

### 5.1 正常匹配

历史接收入口与选择器分流继续复用，不改变现有 group_id 实现：[H3]

```text
WM_COPYDATA 文本入口 0x449c60
  → 0x4c41b0：选择器 == user_id
      → 0x4f4150：同一个联系人管理器
      → 0x4f48d0：目标字符串作为 EDX 输入
          → 0x4f49e0：按此字符串访问用户容器
          → 复制 CUser
          → 原始名称非空才加入目标集合
      → 遍历集合
          → 0x4b9320
              → 非状态 4：0x5bc200 → 0x60d930
              → 状态 4：本机离线保存分支
```

`0x4f4480` 的键取自 `CUser +0x20`，`0x4f49e0` 用输入字符串查此键。比较器按完整字符串比较，不将 UI 显示 ID、名称、IP、`PriKey` 或 64 位路由号转成这个键。[U6、U11、U12]

### 5.2 全新不存在的 ID：不等于“必然排队离线发送”

本轮补出了历史报告尚未区分的后半段：[U11、U14、U15]

1. `0x4f49e0` 查不到键时，`0x4f4a55` 构造 `CUser`。
2. `0x4f4a5a–0x4f4a6e` 把输入字符串复制到新对象 `+0x20`。
3. `0x4f4a79` 把 `+0x1e0` 设为 4，经 `0x4f4480` 插入用户表。
4. `CUser` 构造 `0x5b8350–0x5b8356` 将原始名称 `+0xb0` 初始化为空。
5. `0x4f48d0` 的 `0x4f4947–0x4f495f` 将复制对象的 `+0xb0` 与空字符串比较；`0x4f497f–0x4f4983` 在为空时跳过集合插入。
6. 因而**这个全新、未被其他过程补过名称的错误 ID 不会进入此次 `0x4b9320` 调用集合**，但用户表已出现状态 4 的本机对象。

这修正的是历史“未知用户可创建状态 4，所以可能进入离线发送”的范围：前半句成立，后半句还受名称非空条件控制。不能把状态对象存在当作真实远端存在；如果某个 ID 已有带名称的旧缓存，行为又不同。

### 5.3 已有离线联系人和业务返回

- 已有原始名称非空的联系人，即使当前状态为 4，也能进入目标集合。
- `0x4b956d–0x4b958e` 检查状态，4 跳到 `0x4b95c0`；该分支构造本机离线项，条件满足时调用 `0x63d120`。非 4 分支在 `0x4b95b6` 经当前路由值进入 `0x5bc200`。本轮没有证明队列刷新、重启恢复或离线送达时限。[U16]
- 磁盘存在一条记录不证明用户当前在线；状态非 4 也不等于消息已经送达。
- 窗口分发预置正常结果 1，没有把“未命中”“名称为空”“已进入离线保存”“网络 ACK”作为结构化结果返回。`LRESULT = 1` 不能区分以上情况。[H3]

后续 `SendUser()` 不应通过发送、观察 LRESULT 或重新查询本机对象来验证 ID。配置绑定应使用已经核实的联系人元数据；当前阶段不实现该方法。

### 5.4 与群成员 ID 的一致性，仅作对照

初始任务要求对照群成员类型，本轮已有以下静态证明：群成员表列 2 的 `UserId` 进入成员对象 `+0x24`；其容器节点的相应位置为 `+0x54`。群成员界面 `0x499a98–0x499aa2` 把这个字段交给 `0x4b1830 → 0x4f49e0` 查相同的 `CUser`；必要时再用缓存成员名称补充空名称。[U22]

这说明两种对象引用同一用户字符串身份，不是说成员表才是联系人 ID 的权威来源。**本报告的提取方案不查询群成员表。** `QGroupId` 仍是另一种群对象身份，与用户 `Id` 不互换。

### 5.5 UI 的“复制用户 ID”不能直接当作 IPC ID

`0x467f80` 的复制功能和 `0x467b10` 的显示功能调用 `0x5edac0`。该函数执行转换、两次 `0x5932d0` 调用并截取结果；不是直接返回 `CUser +0x20`。本轮没有建立其输出与 IPC 身份键可互换的证明。[U23]

因此即使 UI 标签写着“用户 ID”，也不把这段显示/复制结果直接放入 `user_id` 目标字段。本轮采用联系人 `Id` 的原值。

## 6. Windows 端联系人只读提取与唯一匹配方案

**这是待实现的方案，不是已经可运行的提取器。数据来源仅为联系人 `OrgStruct_*`，不从群成员寻找。**

### 6.1 先解决唯一剩余的文件读取条件

已有静态证据足以设计映射，不足以直接宣称普通 SQLite 可打开 `nr`。最小下一步是：

1. 把本样本的页 codec 与一个明确版本的兼容实现逐项比对，确认密钥输入方式、页大小处理、第一页和日志/WAL 行为；使用合成数据库的已知答案验证，不靠遍历真实数据库试参数。
2. 读取端使用独立可信 SQLite 实现，以操作系统只读权限打开**私有副本**；不加载或执行客户端 EXE/DLL，不调用客户端的内部函数地址，不连接或注入进程。
3. 密钥若需要由样本内常量提供，只在私有工作目录处理，不能写入源码、报告、命令行历史或 Git。它不是登录凭据提取任务；本轮没有实现该步骤，也不尝试真实库解密。
4. 实现不匹配、数据库打不开或 schema 不符时明确停止，不尝试换成普通 SQLite、自动迁移、修复、导出全库或回到群成员来源。

**当前缺少的是兼容 codec 的离线读取验证，不是联系人表和 ID 字段位置。**

### 6.2 正确文件与身份表

1. 操作者在正常客户端确认当前实例、当前账户及目标联系人的原始名称。目标若只以备注识别，先人工核对原始名称；不打开聊天正文找身份。
2. 按第 4.1 节定位当前实例的 `nr`。不修改 `Green`、不创建目录诱导客户端改变数据位置。
3. 操作者正常退出客户端并确认其已结束，再对该文件取得一致性副本；不能把活动中的数据库/WAL 分别普通复制当作原子快照。存在未处理 journal 或恢复要求时停止，不删除边文件或修复原件。
4. 原文件、副本及其哈希留在仓库外。只读读取器不导出数据库，不扫描其他文件或其他账户记录。
5. 先用兼容只读连接查询 `sqlite_master` 的 `OrgStruct_*` 表名及 schema，不读取任何聊天或凭据表。
6. 只有一张、且已经确认属于当前实例及正常初始化身份的表时，可检查这张表；多张表必须有当前本机原始身份与后缀的可核实对应，**不能选第一张、按最近时间挑选、用显示名称计算后缀或用 UI 复制 ID 猜测**。本轮没有完成 Windows 多身份归属验证。

以下 SQL 只描述已恢复 schema 下的查询，不包含也不暗示数据库解密命令：

```sql
SELECT name
FROM sqlite_master
WHERE type = 'table' AND name GLOB 'OrgStruct_*'
ORDER BY name;

PRAGMA table_info("OrgStruct_<已确认后缀>");
```

检查列名、顺序、类型和索引后，才解释用户记录。表名取自已核实的 schema 并正确引用，不将任意用户输入拼成标识符。

### 6.3 名称到 ID 的匹配

只在已确认的一个身份表中读取两列、一个类型：

```sql
SELECT Id, Data
FROM "OrgStruct_<已确认后缀>"
WHERE Type = ?;
```

参数为整数 1。读取器在本地逐行处理 `Data`，不是把 XML 原文打印出来或落盘：

1. 使用禁用外部实体/网络访问的 XML 解析器，识别已确认的 `U` 根和直接 `Name` 子元素；不使用正则截取、SQL `LIKE`、XML 文本子串搜索或假定 `Name` 是 SQL 列。
2. 只提取 `Name` 文本并进行精确比较；保留大小写、前后空格和 XML 实体还原后的字符。不保留 `Sign`、电话、邮箱等其他字段，不读取聊天正文。
3. 收集名称等于输入值的记录。结果必须恰好一行；两条同名但不同 ID 的记录返回 `ambiguous`，不能 `LIMIT 1`、挑“在线的一条”或试发。
4. 对唯一记录检查 `Id` 为非空文本、无嵌入 NUL，并检查当前实例/身份归属。不要增加未经恢复的十六进制长度规则，也不要把 `PriKey` 转成目标。
5. 返回原始 `Id`、原始名称、匹配条数、私有源文件哈希、表归属及检查时间。只输出该目标的结果，不默认导出全部联系人或 XML。
6. XML 损坏、缺少或重复 `Name`、同名、身份不确定、空 ID、实际 schema 不符都明确失败，不跳过坏数据后声称唯一。

**联系人姓名可以重复，不是唯一标识；本地备注和界面显示文字也不能作为唯一标识。** 建表只约束 `UNIQUE(Id)`，没有对 XML 中的 `Name` 设置唯一约束。[U5、U13]

本节的“唯一匹配”仅表示：在已确认的当前身份表及本次一致性副本中，输入名称恰好匹配一条用户记录，读取器才接受该结果；不是客户端保证姓名唯一，也不保证今后的同名匹配仍然唯一。

后续设计应区分搜索与绑定：

- **姓名只用于搜索，原始 `Id` 用于最终绑定和发送。** 同名时应返回所有精确匹配的候选联系人的必要元数据供人工选择，不自动选中任何一条；自动名称解析仍返回 `ambiguous`。
- 如需同名消歧，可另行设计按需展示已核实的公司、部门等元数据；默认提取流程仍只输出名称与 ID，不批量暴露其他个人资料。辅助信息仍不足以区分时停止，不取第一条、不按在线状态挑选、不试发。
- 人工确认后保存原始 `Id` 与核验依据。联系人改名时，不应仅因名称变化就按姓名重新搜索并改绑另一个 ID；缓存名称只用于展示和复核。
- 对于备注输入、改名后缓存尚未刷新或无法区分的同名联系人，须补充正常客户端中可核对的身份信息，不能仅凭缓存显示文字决定发送目标。

### 6.4 与 Adapter 的边界

- 读取器只产生候选元数据和人工核验结果，不发送消息，不创建占用发送资格的 AttemptID，不修改单次授权状态。
- 元数据存在不证明在线、可送达或当前会话具备发送权限。当前 `internal/ipc` 仍只有已验证的群聊范围；本报告不为开放私聊提供运行验证。
- 若将来实现 `SendUser()`，应原样使用经过核验的 `Id`，继续保留单次授权、持久记录、禁止自动重试和独立接收观察。此项不属于本次代码修改。

## 7. 验证结果与尚未解决项

### 7.1 已做的验证

- 联系人数据库路径、表后缀、建表、列序号、`Type == 1`、`Id → CUser +0x20`、`Name → CUser +0xb0` 均有定向指令或与写入过程相互验证的证据。
- 复核 IPC 的真实名称非空条件，区分“错误 ID 新建空对象”和“有名称的离线联系人”。
- 用合成 `OrgStruct` 数据运行 **18 项检查，全部通过**：唯一匹配、类型过滤、不存在、同名、大小写、前后空格、XML 实体、单引号、NULL/空/NUL ID、坏 XML、重复/缺少 Name、Id 与 PriKey 区别、备注不冒充原名以及只读拒绝写入。结果见 [query-validation.json](query-validation.json)。
- 合成数据库使用普通 SQLite 3.46.1，仅验证查询/匹配逻辑，**没有验证 nr 的 codec、Windows 文件选择或客户端运行行为**。
- 没有修改或运行 Adapter，因此未运行 Go 测试/构建；本轮检查的是静态证据和合成读取逻辑。

### 7.2 未解决项

| 项目 | 缺少的证据 / 下一步 |
|---|---|
| 独立只读读取真实 nr | codec 的明确实现版本、已知答案兼容测试和经授权私有副本的 schema-only 读取验证 |
| Windows 当前账户与多身份表归属 | 当前实例原始身份和表后缀的实际对应，不以 UI 复制 ID 替代 |
| 按本地备注精确查询 | 备注持久化位置、身份范围及其与用户键的完整映射；当前只支持原始 Name 的设计 |
| 网络用户资料的最初发行来源 | 所有中心服务/局域网发现、身份生成与重置流程未完整恢复；不能承诺跨重装永久不变 |
| 完整列表点击/搜索激活 | 已找到条目事件跳板，但未恢复所有间接调用后的窗口事件传播 |
| 缓存新鲜度 | 重命名、退出刷新、离线清理、缓存重建和迁移的完整时序尚未验证 |
| 实际单聊行为 | 未进行 user_id 的授权运行验证；未知用户、离线保存与 IPC 返回结论均为静态范围 |

## 8. Evidence 索引与复用

U/H 是本文别名，**不替换原 Evidence ID**。链接的小文件保留原始 MCP 响应；批量记录按 `items[].procedure.address` 选择函数，不因请求地址落在函数内部就把它当成函数入口。

### 8.1 新证据

| 标记 | 结论及主要查询文件 | Evidence ID |
|---|---|---|
| U1 | `0x441d90` 路径传递；[0064](0064-read_function_instructions.json) | `ev_92d08e4505d156fb50f19fb6fda8cde402beb10aaf11e0e6e360e0c2d5852f7c` |
| U1 | `0x5b8260` 追加 nr；[0065](0065-read_function_instructions.json)；字节 [0070](0070-read_bytes.json) | `ev_e3e215ba7d2e7acde9178d01b5e0688572d724454ffff284f8e4c0fb4d6faae4`；`ev_56bec6f7eda5febd9f06c22b19d33031a5c3588f3356258a346cca7742ec43a0` |
| U1 | `0x5b7800` 根目录追加 cache；[0072](0072-read_function_instructions.json)；字节 [0077](0077-read_bytes.json) | `ev_a95e9906e5870ef801abd7b5b5378217be4a8eb1f2f0b09b8c3a0fc0061ad9af`；`ev_6b1d8b7403a864dbaab99d77786c21117bd52c3b549fceb4cff6953cd9ac2a9a` |
| U1 | `0x5b7d20` 追加 db；[0078](0078-read_function_instructions.json)；字节 [0083](0083-read_bytes.json) | `ev_76ba3897dcd276d0196888b5f5926cedaa3e537c7f1eb0d85c0a836a7fd2d4da`；`ev_403a78c515ff7c5354d0b8661ba3250f52b5f3b0c48b18c1c626b00d216f3234` |
| U2 | `0x4f4150 / 0x4aa820`；[0019](0019-batch_decompile.json)；`0x4a8600` [0015](0015-batch_decompile.json) | `ev_b3cba8e05ca3c2962a9f5234edff8433f9a0c886654e72a9b9ef1accf723e4ef`；`ev_1a47ea32dfef22bd2022c69c21748364d1074987b7419e738f8cba95739ea17f` |
| U2 | `0x4f4ad0` 缓存加载；[0020](0020-batch_decompile.json)；`0x5d1d80` 查询 [0021](0021-batch_decompile.json) | `ev_b9bfbc387a3747b13c88db40ae1fb89bfd3038994e3e4ac15722e9a05c5e22aa`；`ev_2db4e55dbd64e20558a599be6c494c12e28a2178df2b6bd6860133f84a0e5bef` |
| U3 | `0x5dd830` 列解码指令；[0025](0025-read_function_instructions.json) | `ev_adb117ddd649e493df74a8412fa5b6ce1e6deb9526c27a595d0b79bd43136ef5` |
| U3 | `0x4f4ad0` Type / Id / Name 指令；[0054](0054-read_function_instructions.json) | `ev_7061824a96a688fd310bc476ed526982fddee9a8c98fe27e05db1dde5c7d2155` |
| U4 | `0x5d1570` 身份保存；[0055](0055-read_function_instructions.json)；`0x5dd000` 表名 [0129](0129-read_function_instructions.json) | `ev_af8c1bf4e7e7ceb4fdfad754387abcd3a0b5367cc31ad13926dbba019985e11e`；`ev_7e500283222a0e40909467bb6d00045ab609a9a9d0e33c5710b63743902408d9` |
| U5 | `0x5da2d0` 建表、`0x5d8a40` 写入；[0035](0035-batch_decompile.json) | `ev_b036d4f74be84c68993089637f50b9bdd1be2c9fce8c91e97652a50973c8a459` |
| U5 | 建表指令 [0130](0130-read_function_instructions.json)；Id 原始字节 [0043](0043-read_bytes.json) | `ev_7dc3db17e871cda0917e5a07c96c2380a0abd1bcabb4e791737fca14024d0f7b`；`ev_efd6b502f87d2aad915543f0a96a0a9ce427fd242a723a14b5e4f944f0d782b7` |
| U6 | `0x4f4480` 取 +0x20 作键；[0029](0029-read_function_instructions.json) | `ev_431305e248a202835984171dc2b342c2be66b6cbd398942e31fc4d0fb1dd3c08` |
| U6 | `0x4d78b0` 字段复制；[0120](0120-read_function_instructions.json) | `ev_f1aef597b4235d1d847e1859db6562921b8c1d5571fd100aadff160b26f00f90` |
| U7 | `0x4aefa0` 用户 XML 序列化；[0038](0038-read_function_instructions.json)；标签字节 [0044](0044-read_bytes.json) | `ev_6e877c01a41b43ca3bf9e0ba096a69e45145e0f7b3a3c9ae59239ebb43c830ad`；`ev_8d72772e2e7bf8631b7bd642ea08d54a0b1498212e17ff2ebc33353b5c15e8d1` |
| U8 | `0x5d1570` 非空 codec 参数；[0055](0055-read_function_instructions.json) | `ev_af8c1bf4e7e7ceb4fdfad754387abcd3a0b5367cc31ad13926dbba019985e11e` |
| U9 | `0x6f98e0` 安装页 codec；[0124](0124-batch_decompile.json)；指令 [0138](0138-read_function_instructions.json) | `ev_3ad19aa6098414f47b249545bde9e42f2661848fe1398703f9a42fc9fb3496f8`；`ev_17eb8090a36855459a758bf95a10a3068b720347e2b7e623abaa67e90cc7651a` |
| U10 | `0x5bd4c0` 原生连接身份写入 CUser +0x20；[0060](0060-read_function_instructions.json) | `ev_e7db272729a63baeac02c7d56697bfa87199246630d9d4c8ff7da941b3b70ad7` |
| U10 | `0x6596f0` X_READY / USER_ID 指令；[0096](0096-read_function_instructions.json) | `ev_d1ac955e7d86369b60cbfc25d82e483e409723c2a573e7b30ba22df5c69e5998` |
| U11 | `0x4f49e0` 未命中对象、ID 和状态；[0004](0004-read_function_instructions.json) | `ev_01c090b5a175b732bd4c852ffe82486bb8403721ab4e80c00cead9727fb5121e` |
| U12 | `0x4daae0 / 0x4da5b0` 容器查找；[0102](0102-batch_decompile.json)；`0x4dbfa0 / 0x4071f0` [0106](0106-batch_decompile.json) | `ev_ecae18c9413f6da257844fd3d5a29b989de4879c97ed0434b6ae5e3e1599a989`；`ev_011707d0b3ad2ebcbe8946774a165df8d3a46a9866666cbf31a94dfed9178d14` |
| U12 | `0x4071b0` 完整代码单元比较；[0148](0148-batch_decompile.json) | `ev_272d6a98e1b903c964e7bc4877807a3046a54c6faa3875dca6da39bc60d5b57b` |
| U13 | `0x4a67e0` 条目保存 ID；[0028](0028-read_function_instructions.json)；`0x4aa820` [0048](0048-read_function_instructions.json) | `ev_9a51334f4cd5145cae750eb1fa2723c2225b8ccf0eb556ea4bbe5d2660b876d0`；`ev_5cd4ce9af61f805cc331cc71716a53dd0202e914c28bcf112336fdaf9d07488d` |
| U13 | `0x4a6c00` 名称控件、备注组合；[0093](0093-read_function_instructions.json) | `ev_a30ffd4d6ca14a08653632a78926bcaba8e8dc1769aada019994d80e8948ca44` |
| U13 | `0x4b1930` 原始名称；[0091](0091-read_function_instructions.json)；`0x4b1d50` 备注优先 [0100](0100-read_function_instructions.json) | `ev_b09e06540f1b845128122df70d83ec8c4c72f193d75efd21b8d6f8bef57fa8f1`；`ev_498cd2f05b1dd56afdc1a72a218664b7c861f6bf1f79aeb6ff096e7d017ef1f6` |
| U13 | 名称查找/备注函数伪代码；[0087](0087-batch_decompile.json)、[0095](0095-batch_decompile.json) | `ev_2e5c4412bd43063a1a4db2d1c4797188a382f3d95bd19a3b25cbce626e7c4000`；`ev_3d2da903b7186f0352da663e90d9d338b49529e5350a42678014f9c437b11700` |
| U14 | `0x4f48d0` 原始名称非空才入集合；[0103](0103-read_function_instructions.json) | `ev_dcdb93b2470f5b38a993172736ace877f111c594e4887e8a46e1ede10542d417` |
| U15 | `0x5b82e0` 初始化空名称；[0107](0107-read_function_instructions.json) | `ev_b3b158133b60dab16a307267a89cd94dd899b5391715a27a7025022feb259e81` |
| U16 | `0x4b9320` 状态 4 与网络/本机分支；[0110](0110-read_function_instructions.json) | `ev_c905b9a9a0ba4c40e814d191ab36f392db6461dc8ab543dd8b01ce10cf14fdac` |
| U17 | `0x4d5830` 由 CUser 构造 Type=1 缓存记录；[0089](0089-read_function_instructions.json)；[0059](0059-batch_decompile.json) | `ev_f7d097c678fbf288ee177129d2205d211df5181c44eb771867e838d3f6ae178e`；`ev_a1629db24e5b96f90eeedca632bb427e0c8ab7d15cecca334267c29582a62fbc` |
| U18 | `0x49c4b0` 搜索结果对象；[0019](0019-batch_decompile.json)、[0118](0118-read_function_instructions.json) | `ev_b3cba8e05ca3c2962a9f5234edff8433f9a0c886654e72a9b9ef1accf723e4ef`；`ev_64dcd4626162dc4a013265390cf24ff4f0b3c64e2b39518d6b5fbb04c44f9bf8` |
| U19 | `0x4a6570` 事件注册；[0131](0131-read_function_instructions.json)；跳板 [0116](0116-inspect_native_instruction.json)、[0121](0121-inspect_native_instruction.json)、[0122](0122-inspect_native_instruction.json) | `ev_75228843d3596d4168e5849352302a535143011e197f8b0f3ca2d44f67f1e87a`；`ev_31d750623b41954055573e4cd9b14b6682ba55e94ca4931fd90a2f84769b14c6`；`ev_a6dc5c7498ce5bba2e153611ff7f88969f8eb442e19504cc5814533cf4b7d44f`；`ev_471e57ea408a85a4c0a3fe42a9c76054b162b31ad0e7d1076c01611dca9fca64` |
| U20 | 口令派生和页处理；[0134](0134-batch_decompile.json)、[0137](0137-batch_decompile.json)、[0139](0139-batch_decompile.json)、[0141](0141-batch_decompile.json) | `ev_f2b53abd9bdf3b1f26435de71b20670a80d19a715f37cb6c6146a043decd35e4`；`ev_c85b51650d9c97356d050a1a809773e4cfface05f2aa8c0d3585c471c3905014`；`ev_2b1598dd7045ee9b3805ef67f43440a82b73038c56583dc19ba8c551559e101c`；`ev_242df3b5a4eedc0d25f0cb03cdef55bb45e8172e61854ea331a2ff026cd8dacf` |
| U21 | `0x4c7880` 添加 XML 子节点；[0133](0133-read_function_instructions.json)；`CXMLHelper::NodeToString / 0x56a130` [0134](0134-batch_decompile.json)、[0137](0137-batch_decompile.json) | `ev_1ce0d85d18be7795c53957efec77898dfa2264adbc08e0d40004dda3f7f40aaf`；`ev_f2b53abd9bdf3b1f26435de71b20670a80d19a715f37cb6c6146a043decd35e4`；`ev_c85b51650d9c97356d050a1a809773e4cfface05f2aa8c0d3585c471c3905014` |
| U22 | 成员列序号；[0026](0026-read_function_instructions.json)；成员界面传 ID 给 CUser 查找 [0114](0114-read_function_instructions.json) | `ev_d83cded04f732d06fc34f3b63450aa3cf22ca0f31f8e93025e877d3e1f50d8df`；`ev_48d8e0e27339e755a059ae48d43d181ab3b9cfb9dd98c4e7e5d2427a94a9abc4` |
| U23 | UI 显示/复制过程；[0013](0013-batch_decompile.json)、[0019](0019-batch_decompile.json)；`0x5edac0` 指令 [0037](0037-read_function_instructions.json) | `ev_5c2487011c19b4aa90f73ea5ad35e0c1d8001a582c9807e11452ee714401d650`；`ev_b3cba8e05ca3c2962a9f5234edff8433f9a0c886654e72a9b9ef1accf723e4ef`；`ev_857784ab9db83e2b921e43ebd44ab30616d61d881c70aa961c40b8c6859caaae` |

### 8.2 复用历史证据

| 标记 | 内容与文件 | Evidence ID |
|---|---|---|
| H1 | 数据根目录分支；[QGroupId 报告 G16–G19](../qgroup-id/qgroup-id-report.md#31-文件路径构造) | `ev_f6c1a787d8d9829a7c2c4ef219fee45373ec2a8be2f36e2a452b68f01f1247f3`；`ev_c9643ba0f4cb0556d7656eb2500a9614144183f90fdc28dc86669485bfd9a7d1`；`ev_ef63dba2f124ef9a8192dea365ed63ec57f4f8891105597df4311d99ecebc45a` |
| H2 | 本机 CUser 从全局对象复制；[qgroup-id/0029](../qgroup-id/0029-read_function_instructions.json) | `ev_fdcfcbb25a9892ee4e00d6874f9732dec345c8b2bc1316dd08db7563daa0131d` |
| H3 | IPC 分流指令；[gateway-static/0108](../gateway-static/0108-read_function_instructions.json)；窗口分发 [ipc-phase5/0034](../ipc-phase5/0034-read_function_instructions.json) | `ev_1ac48d41a61fa5a2855f4b68d9a83c51a57b894e0ad06e82fcec0ba285e9b307`；`ev_74ceb756db1d0ca05cf77f192c7e646cd6cfb430a9532cea99c4aea3f26db43b` |
| H4 | SQLite 构造转发和非空密钥分支；[qgroup-id/0023](../qgroup-id/0023-read_function_instructions.json)、[0024](../qgroup-id/0024-read_function_instructions.json) | `ev_1a7cf78c013b664e454069626a0e4f8a0d1d486dd9d44b3052075f24842a917c`；`ev_8ede1456c04057b1c31b0a083aff7913c5be1e1ab68f536764853b10590915c7` |

### 8.3 保存范围和分析器限制

[user-id-evidence.json](user-id-evidence.json) 是完整导出后的**143 条原记录选编**；只排除一条包含静态数据库密钥局部字节的 `read_bytes`，其余记录及 Evidence ID 未修改。完整原始导出仅存本地私有临时目录；[0149](0149-export_evidence_bundle.json) 是原始 144 条记录的导出回执，不把它误写成公共选编的条数。[0150](0150-close_binary.json) 确认已关闭会话，未保存新快照。

证据选编包含探索过程中的未命中和无效请求，不是每个结果都能用于结论：

- 名称搜索未命中不证明相关类或存储不存在，地址/字段赋值证据优先。
- 若干 `0x87fc86` 调用仍被标为 no-return，导致不连续函数体或错误伪代码；关键字段、状态分支和 codec 参数已对照指令。
- [0111](0111-batch_decompile.json) 的 `0x4a7940` 项同时报告身份解析失败和不相关伪代码，**该项伪代码弃用**。本报告只引用 [0116](0116-inspect_native_instruction.json)、[0121](0121-inspect_native_instruction.json)、[0122](0122-inspect_native_instruction.json) 对这个跳板的实际指令，不使用错误的函数归属。
- 一次成员写入查询请求 `0x6403a0`，提供方规范入口为 `0x640270`；按响应入口解释，不把内部地址当新函数。
- `unknowns = 0` 仅表示未登记机器可读 unknown 条目，不表示第 7.2 节的问题已解决。
