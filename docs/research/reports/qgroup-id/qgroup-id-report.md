# 内网通 3.4.3055：只读获取群名称对应的 QGROUP_ID

本文记录群 ID 的生成、SQLite 持久化、群列表显示和只读验证方法；不涉及发送器或 IPC 长度实验。

## 1. 结论与范围

**群 ID 不只存在于内存。该版本将原生群元数据写入 SQLite 文件 `<数据根目录>\data\qrp`，无扩展名。表名为 `QGroupInfo_<本机 CUser 内部标识>`，群名称字段是 `Name`，原生逻辑群 ID 字段是 `QGroupId`。`PriKey` 是本地自增主键，不是 QGROUP_ID。** [G1、G2、G4、G5]

可以在 Windows 上对正确文件取得一致性副本，用普通 SQLite 只读查询精确群名，再人工核对授权群。无需修改 EXE、注入 DLL、读取进程内存、取得数据库密钥或发送消息。[G2、G6]

可靠提取的条件是：**文件属于当前实例、表属于当前身份、授权群名称精确匹配且只有一条记录、当前群及成员资格由操作者正常核实**。多身份表无法归属、多个同名群、旧副本或查询错误时停止，不取第一条、不试发。

本轮仅完成静态证明和查询逻辑的合成数据检查，**没有读取 Windows 实际数据库，也没有获得实际测试群 ID**。尚不能宣布“唯一授权测试群 ID 已取得”。合成验证结果见 [query-validation.json](query-validation.json)：覆盖唯一匹配、同名、无匹配、单引号、大小写、尾随空格及空标识，并验证只读连接拒绝写入；没有运行 Windows 版 SQLite 工具。

### 分析对象

- EXE：`/root/reverse-lab/extracted/app/ShiYeLine.exe`。
- SHA-256：`e7caef3fe744f9fe6ba90dd1d53b7e64241cb54020a8bf37be5e756e2fe881b1`。
- REA 6.7.0 + Ghidra 12.1.4，x86，ImageBase `0x400000`，地址均为静态 VA。
- 新会话的分析配置摘要与历史相同：`7810ea9437da59780820ec6fedb47ce47390841a95d527ddb6c3b3700fe180cb`。
- 复用了历史 `CQGroupMgr::Init`、E25 群查找证据；没有分析 cbData，没有改发送程序或授权常量，没有执行目标、注入、发送消息、读取真实聊天正文或凭据。
- 默认 MCP 的 Ghidra 导入超过 330000 ms 后关闭；沿用第七阶段办法，独立临时 REA 进程使用 `REA_GHIDRA_STARTUP_TIMEOUT_MS=1200000`，没有改配置或关闭分析器。完成后导出证据并关闭会话。

## 2. 群 ID 的来源和字段语义

### 2.1 创建 ID，而不是由群名计算 ID

`CQGroupMgr::Create @ 0x6161f0` 在 `0x616233` 调用 `0x592690`，结果在 `0x61623d–0x61624b` 写入群对象 `+0x08` 的宽字符串。[G7]

`0x592690 → 0x592630 → CoCreateGuid`；前者按 GUID 的 16 个内存字节逐个 `%02x` 格式化，生成 32 个小写十六进制字符，没有连字符。**这是本版本创建路径的格式，不是根据名称推导某个实际 ID 的算法，也不证明所有导入/历史对象都必须符合这一格式。** 不把它转换成常见带连字符的 GUID；直接保留数据库中的原字符串。[G8]

创建后 `0x6162bf` 调用 `CQGroupInfoDb` 的插入过程 `0x63f1f0`，成功后在 `0x6162cc–0x6162d9` 按同一字符串写入群管理表。[G7、G9]

### 2.2 QGroupInfo 的相关字段

以下是当前二进制的对象偏移，不是可供外部进程读取的 ABI；不据此读取运行时内存。

| 相对对象起点 | 语义 | SQLite 字段 | 说明 |
|---|---|---|---|
| `+0x00` | 本地记录号 | `PriKey` | 64 位读取的本地自增主键，不作发送目标 |
| `+0x08` | 原生逻辑群 ID 宽字符串对象 | `QGroupId` | 对应网络字段 `QGROUP_ID`、IPC 的目标值 |
| `+0x24` | 群名称宽字符串对象 | `Name` | 对应群列表名称、网络字段 `QGROUP_NAME` |
| `+0x40` | 群主标识宽字符串对象 | `Master` | 元数据，不读取登录凭据 |
| `+0x98 / +0xa0` | 信息/成员版本 | `InfoVer / UserVer` | 不是群 ID |
| `+0xa8` | 创建时间 | `CreateTime` | 可辅助人工识别，不单独证明授权 |

插入函数 `0x63f1f0` 用 `QGroupId, Name, Master, Intr, Ann, InfoVer, UserVer, CreateTime, Reserved1..4` 构造 INSERT；名称字面串 `0x99de08` 的原始 UTF-16LE 字节确认为 `Name`。[G9、G10]

`0x63fb10` 读取查询结果时，列 0 经 `0x67c130(0)` 读整数，列 1/2 分别经 `0x67c1f0` 读入两个连续宽字符串；指令 `0x63fc14–0x63fc77` 明确给出列序号 1/2。结合建表、复制和插入偏移，可以区分 `PriKey / QGroupId / Name`，不是只凭字段名称判断。[G2、G4、G9、G11]

`X_QGROUP_INVITE` 序列化函数 `0x663cd0` 调用 `0x6601f0` 取出带 `QGroupInfo` RTTI 的对象，再构造包含 `QGROUP_ID` 和 `QGROUP_NAME` 的 XML；同一结构通过 `0x4dba10` 复制。这里仅静态读取格式与字段传递，没有构造或发送任何网络请求。[G12]

### 2.3 从数据库进入群管理和列表

```text
CQGroupMgr::Init 0x615e80
  → CQGroupInfoDb::Open 0x63ef50
  → LoadData 0x617a70
      → 0x63f540：select * from 当前 QGroupInfo 表
      → 0x63fb10：将列读成 QGroupInfo
      → 0x4dab70 / 0x4d7e60：按群 ID 放入群管理表
  → 0x4edf10：将群管理对象复制到 UI 使用的群表
  → CQGroupListView 初始化 0x4982b0
      → 0x498570：创建 CQGroupItem
      → 0x4971e0：保存条目的群 ID
      → 0x497380：按该 ID 查群对象并显示名称
```

关键指令：[G13–G15]

- `0x617b44–0x617b54`：用读出记录的 ID 找到群管理项，再复制完整群对象。
- `0x4edf82–0x4edf90`：复制管理表中的群对象和 ID，不重新生成 UI 群 ID。
- `0x49720d–0x497214`：将传入 ID 保存到列表项 `+0x254`。
- `0x4973ca–0x4973e5`：使用条目保存的 ID 调用 `0x4ede90 → 0x4edfc0`。
- `0x4973fd` 查控件 `0x3ea == 1002`；`0x497417–0x497445` 从返回对象的名称字符串复制显示文字。

发行资源 `res/ShiYeLine/layout/QGroupItem.xml:3` 正是 `SkinTextViewEx Id="1002"`；`MainDialog.xml` 的原生群列表控件是 `QGroupListView Id="4000"`。这些资源辅助核对显示用途，不把 XML 控件 ID 当作实际群 ID。

IPC 的 `group_id → 0x4ee740 → 0x4edfc0` 使用同一个按逻辑 ID 查群的 UI 群表。历史 E25-G1/G2 已确认该行为，故获取目标应使用 `QGroupId`，不是 `PriKey` 或控件编号。[历史 E25；G14、G15]

## 3. 本地 SQLite 存储

### 3.1 文件路径构造

路径由以下过程直接支持，不猜测 `qgroup.db` 或 `%APPDATA%`：[G1、G16–G19]

1. `0x5951c0` 用 `GetModuleFileNameW(NULL, ...)` 取得当前 EXE 路径，`0x54fb10` 取目录。
2. `0x5b7030` 检查 `<EXE目录>\Green` 是否存在；存在时，数据根目录设为 `<EXE目录>\Syl`。
3. 没有 `Green` 时，用 `0x5927f0` 取得公共根目录 `P`，优先选择已经存在的 `P\ShiYeLine`，否则选择 `P\Nwt`。这里只是路径存在判断，不是从配置中任取一个用户目录。
4. `0x5b73f0` 把选定数据根目录存入目录对象的 `+0x20`；`0x5b77e0` 在其后追加 `\data`。
5. `0x615ee0–0x615eef` 再追加字面串 `0x9e18e0 == "\\qrp"`，并把这个完整文件名同时传给群信息和群成员数据库包装。

现代 Windows 的 `P` 来源是 `SHGetKnownFolderPath`，其 GUID 字节位于 `0x999744`：

```text
{DFDF76A2-C82A-4D63-906A-5644AC457385} == FOLDERID_Public
```

默认是 `%PUBLIC%`，通常 `C:\Users\Public`，**不是 ProgramData**。仅当该 API 入口不可用时，代码才使用 `SHGetFolderPathW(..., 0x8023, ...)`；其中 CSIDL 为 `0x23 == CSIDL_COMMON_APPDATA`，这条旧系统分支通常对应 ProgramData。API 入口存在但调用失败时并不切换到旧 API；实际取目录失败则应停止核验。[G18、G19；Microsoft KNOWNFOLDERID 文档]

| 分支 | 群数据库路径 |
|---|---|
| EXE 旁存在 `Green` | `<EXE目录>\Syl\data\qrp` |
| 现代 Windows，已有旧命名目录 | `<FOLDERID_Public>\ShiYeLine\data\qrp` |
| 现代 Windows，未命中旧目录 | `<FOLDERID_Public>\Nwt\data\qrp` |
| 旧 API 分支 | `<CSIDL_COMMON_APPDATA>\ShiYeLine\data\qrp` 或 `...\Nwt\data\qrp`，选择规则相同 |

不通过创建 `Green`、目录或空数据库测试路径；这会改变客户端之后使用的位置。若有多个安装实例、目录重定向或残留文件，先根据**当前实例的 EXE 路径与上述分支**确定文件，不能按文件新旧随意挑选。

### 3.2 表名和字段

`0x63ef50` 先赋值 `QGroupInfo`，追加 `_`，再追加传入的本机 `CUser +0x20` 字符串。调用者 `0x5baf30` 在 `0x5bb339` 将本机 CUser 指针传给 `CQGroupMgr::Init`；后者在 `0x615f8c` 取其 `+0x20` 作为表后缀。因此**后缀不是群 ID，也不应假定等于 UI 显示的账号名或“复制用户 ID”输出**。[G1、G2、G20]

```text
QGroupInfo_<内部身份标识>
  PriKey INTEGER PRIMARY KEY AUTOINCREMENT
  QGroupId TEXT
  Name TEXT
  Master TEXT
  Intr TEXT
  Ann TEXT
  InfoVer INTEGER
  UserVer INTEGER
  CreateTime INTEGER
  Reserved1 TEXT
  Reserved2 TEXT
  Reserved3 INTEGER
  Reserved4 INTEGER

QGroupUser_<同一内部身份标识>
  PriKey INTEGER PRIMARY KEY AUTOINCREMENT
  QGroupId TEXT
  UserId TEXT
  UserName TEXT
  Reserved1 TEXT
  Reserved2 TEXT
  Reserved3 INTEGER
  Reserved4 INTEGER
```

这是从建表函数恢复的结构，不是读取实际数据库得到的 schema。`QGroupInfo` 的 `QGroupId` 上建立的是普通索引，`Name` 没有唯一约束；既不能假定群名唯一，也不能忽略重复行。[G2]

### 3.3 是否需要解密

所检查的群信息打开路径在 `0x63f009 / 0x63f017` 明确执行 `XOR EBX, EBX`，再调用 `Kompex::SQLiteDatabase` 构造函数 `0x67d530`。该函数把 EBX 保留到 `0x67d610`；后者 `0x67d661–0x67d663` 发现其为 0，跳过非空密钥处理分支。[G6]

**因此，已检查的群信息路径不提供数据库密钥，支持使用普通 SQLite 读取该文件。** 本轮不读取或提取任何登录密码/密钥。真实副本若不是普通 SQLite、报 `file is not a database`、锁或损坏错误，停止，不自动寻找密钥、解密或修改原文件。群成员包装在本轮指令中也以 EBX=0 构造，完整指令保存在 `0058-read_function_instructions.json`。

这里确认了一种持久化机制，没有穷举所有本地缓存、网络更新和退出保存分支；不据此声称其他地方绝不保存副本。群简介、公告等虽有元数据字段，本方案不查询它们。

## 4. Windows 端最小只读验证步骤

以下是**供操作者执行的方案，本轮未执行**。它不调用 IPC，也不启动发送器。

### 4.1 准备一致性副本

1. 正常客户端中确认当前账号、唯一授权原生测试群的完整名称和正常成员资格；不打开聊天历史来寻找 ID。
2. 记录当前实例的 EXE 路径；使用第 3.1 节规则定位它实际使用的 `qrp`。不是递归搜索整个磁盘，不读取其他存储文件。
3. 操作者正常退出该客户端，确认进程已经结束；不强杀，不由脚本自动关闭。需要保持运行时，应另用经授权的一致性备份方法，不能把运行中的数据库/WAL 逐个普通复制当作原子快照。
4. 将这个 `qrp` 复制到新建的私有工作目录。若正常退出后仍有同名 `qrp-wal`，一起保留在副本旁；存在 journal 或恢复要求时停止，不能删除边文件、checkpoint、VACUUM 或修复原件。记录源文件和副本的 SHA-256。此处复制的是群元数据载体，不读取其他聊天/凭据文件。
5. 用可信 SQLite 工具的**只读模式**打开副本。例如现有 `sqlite3.exe`：

```powershell
sqlite3.exe -readonly 'D:\授权检查目录\qrp'
```

不要用默认可写模式，不加载客户端 DLL，不启用数据库解密。副本及实际 ID 均留在仓库外，不上传整个数据库。

### 4.2 先查表结构，不导出群列表

```sql
SELECT name
FROM sqlite_master
WHERE type = 'table' AND name GLOB 'QGroupInfo_*'
ORDER BY name;
```

这一步只列**表名**，不列任何群记录。不要使用 `.dump`、全库导出或 `SELECT *`。

- 正确的当前实例已完成群管理初始化，且只有一个 `QGroupInfo_*` 表时，可以选这个表；初始化会为当前身份打开/创建自己的表。[G1、G2]
- 有多个表时，不选第一张，不把后缀当群 ID，也不尝试由账号显示名称计算后缀。必须先有可核实的当前身份与内部后缀对应信息；本轮没有建立运行时的对应关系，不能自动继续。
- 没有表时停止：可能选错文件、初始化未完成或实际对象不符合本轮静态路径；不能临时创建表。

将下文的 `QGroupInfo_<实际后缀>` 换成**刚刚确认的完整表名**，以双引号引用。执行：

```sql
PRAGMA table_info("QGroupInfo_<实际后缀>");
```

确认 `QGroupId` 和 `Name` 字段存在，结构符合第 3.2 节；不把相似字段名代替它们。

### 4.3 只查询精确授权名称

下面的字符串是占位说明，不是真实群名。填写时保留原始字符、空格和大小写；SQL 字符串中的单引号写成两个单引号。

```sql
SELECT COUNT(*) AS matching_rows,
       COUNT(DISTINCT QGroupId) AS distinct_ids
FROM "QGroupInfo_<实际后缀>"
WHERE Name COLLATE BINARY = '授权测试群的精确名称';
```

只在两个计数**均为 1**时继续：

```sql
SELECT Name, QGroupId
FROM "QGroupInfo_<实际后缀>"
WHERE Name COLLATE BINARY = '授权测试群的精确名称';
```

不使用模糊匹配、`LIMIT 1`、trim、大小写转换或由名称计算 ID。数据库本身未保证 QGroupId 非空；结果为空字符串、格式异常或两次查询出现不一致也停止。

如果管理员另有当前测试群的创建时间、群主原始标识，可以只对**同一个名称匹配对象**查询 `Master, CreateTime` 辅助核对；不能单凭“名称相似”或“一条缓存记录”替代真实群授权。多个同名群必须先由管理员明确区分，本方案不自动挑选。

### 4.4 判定和记录

- 让操作者与测试群管理员核对当前实例、表归属、精确名称和当前群成员；这是磁盘元数据提取，不是活跃群状态或发送权限的实时 API。
- 原样保存唯一 `QGroupId`、来源文件的私有路径/哈希、表名、核验时间和授权范围；不保存聊天正文或凭据。
- 本轮不填写发送器的 `authorizedGroupID`、不改变未授权阶段测试、不生成可发送构建、不发送测试消息。
- 如果 Windows 核验得到单表、单行和明确授权，**可以可靠提取该授权群的原生 ID**。如果多身份归属、同名消歧或实际文件读取仍有问题，结果是“尚未取得”，不是猜一个值。

## 5. 证据索引

完整包：[qgroup-id-evidence.json](qgroup-id-evidence.json)，57 条新会话记录；批量记录中按 `items[].procedure.address` 定位函数。此包不包含历史 E25 原始记录，历史部分见 [IPC 契约证据索引](../../ipc/ipc-contract.md#5-证据索引与复用方式)。表内 G 编号仅是本文引用别名，不替换原 Evidence ID。

| 标记 | 内容 / 查询文件 | Evidence ID |
|---|---|---|
| G1 | `CQGroupMgr::Init @ 0x615e80` 指令，路径、表后缀参数；`0016` | `ev_9b1f6a0e7c82c0abd42c0334e925f83d6e41abf4a14b36373ecb435fed3486aa` |
| G2 | `0x63ef50 / 0x63ffb0` 建表，`0x617a70` 加载；`0003` | `ev_336b27193b78e56252b395d4fcca6e23a4809de049d1de7ecbb0621ad2001297` |
| G3 | 表名分隔符 `_ @ 0x9e42b0`；`0014` | `ev_da844967356faec42f9ba6fb6995b138e735c82440ba765a9a23a4eb1aa45d77` |
| G4 | `0x63f540` 查询调用指令；`0043` | `ev_2486f87577daee372ae63bf45f0756b6139050cf89da6a1e0d9747e164ad8021` |
| G5 | `\\qrp @ 0x9e18e0` 原始字节；`0021` | `ev_d3965020a6d01e819da81dbdfea7d0d3038867197c692b0bd9829427cd3150a7` |
| G6 | `0x63ef50` 指令置 EBX=0；`0028` | `ev_fba2af7c266d3a1eeefd804d11363ca6ef2ace08876a06ed1eb6e62555cc7bfa` |
| G6 | `0x67d530` 构造转发；`0023` | `ev_1a7cf78c013b664e454069626a0e4f8a0d1d486dd9d44b3052075f24842a917c` |
| G6 | `0x67d610` 非空密钥分支；`0024` | `ev_8ede1456c04057b1c31b0a083aff7913c5be1e1ab68f536764853b10590915c7` |
| G7 | `0x6161f0` 创建与落盘调用指令；`0037` | `ev_02765b590ed286989b74a70512add01eda981281bb45587c86e68dcd63d9c776` |
| G8 | `0x592690` 格式化、`0x63f1f0` 插入伪代码；`0017` | `ev_69a304fc1d88d80b8a628c170791242173e498c3d698dcb61e8bccdba39dbb8b` |
| G8 | `0x592630` CoCreateGuid、公共目录与 CUser 构造；`0020 / 0033` | `ev_a6801b325dea659452c655f9ec3346dedc611949ad5f28708a06662ad0586ede`；`ev_e36da89fd5c3e666e5e9fd82d5d42a55ea791afb66ee379f81b8abacb628a12c` |
| G9 | `0x63f1f0` 插入指令；`0057` | `ev_0198b9dc611ff900a906d74d93736221e344c76edec4168ae40857e57a64a33f` |
| G10 | `Name @ 0x99de08` 原始字节；`0013` | `ev_4b84946556fd3175517fd8b9b379321a345632a6cf8af20dbc84786ca3ea3d80` |
| G11 | `0x63fb10` 列序号与读入指令；`0022` | `ev_f0322e4bafe1e8241b6aa069ad5489e802f0d3030c663fc0ea67b409084be689` |
| G12 | `0x663cd0` 群信息网络序列化；`0048 / 0054` | `ev_1f1dfca7d84a59d20306e6547a2efadc871e38c3625c4d9773e7ae856419009b`；`ev_55304b4ba9ccc35cab3c75e2e5959cd9988c2c70f50bc739637e6f2dd7b11cf5` |
| G12 | `0x6601f0` QGroupInfo RTTI 与 `0x4dba10` 复制；`0053 / 0056` | `ev_b18a6de0b452c9cbd3577cc490a4e7a94ec10c0ad58a90e9288acf17958e4530`；`ev_5493b30ef5a806c0059af50c4b37832a8ebc717cec628c26d32ad8c0fe2a7394` |
| G13 | `0x617a70` 加载到群表的指令；`0051` | `ev_1cc5d37a1c51277d965aeb0cce3a4bdadc7923eb642803c61073db5b7a16a7eb` |
| G14 | `0x4edf10` 群管理表到 UI 表指令；`0050` | `ev_5b68233b759ff0f2a9583953974b52c13007ef211a6b4fdc085ae9976a87953d` |
| G14 | `0x4982b0 / 0x498570` 列表初始化与条目创建；`0036 / 0038` | `ev_85637738fd2740d520cd4084ba8483216b29395205ea3f4d438f70cb502798c9`；`ev_d86fef892b50f217ba3ff9feb1d3ae0f5078b705717814beb094da7f014d1238` |
| G15 | `0x4971e0` 保存条目 ID；`0045` | `ev_4a819106f17eca9947b74c08c96a48e3d61db108a4a03d72230bbd61bc3bfdf9` |
| G15 | `0x497380` 查群并显示名称；`0049` | `ev_767d8cc920bf7c60d14766f9ef58490fd0320d3f1edcd0756bf0ece2fb42e6f4` |
| G16 | `0x5b7030` 数据根目录选择指令；`0015` | `ev_f6c1a787d8d9829a7c2c4ef219fee45373ec2a8be2f36e2a452b68f01f1247f3` |
| G17 | `0x5b73f0` 保存目录，`0x5b77e0` 追加 data；`0041 / 0012` | `ev_c9643ba0f4cb0556d7656eb2500a9614144183f90fdc28dc86669485bfd9a7d1`；`ev_c8c114de3a1789986b06a94fcf5ed783f13f77f1c420c9e1f7916b7cd579d423` |
| G18 | Public GUID、Green/Syl/data、Nwt 字节；`0026 / 0019 / 0027` | `ev_54239da866f7adf93e1f06885977f6776e855d70f5a64f9299e34d9b869d1d5c`；`ev_dc46f0ad24893cc4dcb149045165d62cae4d1208aaed2ef3ef5f08517434f31d`；`ev_60fb4f54354d9e4a6214fc2706cd2057ffc5851b566837bba933c0a1429498f6` |
| G19 | `0x5927f0` 现代/旧系统目录 API 指令；`0032` | `ev_ef63dba2f124ef9a8192dea365ed63ec57f4f8891105597df4311d99ecebc45a` |
| G20 | `0x5baf30` 本机 CUser 指针传参；`0029` | `ev_fdcfcbb25a9892ee4e00d6874f9732dec345c8b2bc1316dd08db7563daa0131d` |

`file-string-seeds.json` 是本轮直接读取发行 EXE 字面串得到的辅助线索，不是运行结果；函数关系和字段结论以上表 REA 指令/伪代码为准。部分伪代码存在不正确原型和乱码、若干 free 跳板被 Ghidra 标注 no-return，关键路径已经用指令核对；不把伪代码的局部变量名和截断退出路径当作原始源代码。

导出与关闭回执为 `0061-export_evidence_bundle.json`、`0062-close_binary.json`。未保存新分析快照；原 EXE 未修改。

## 6. 验证状态与未解决项

| 用户要求 | 结果 |
|---|---|
| 检查已有 CQGroupMgr 证据 | 已完成，历史 Init 与 E25 已复用 |
| 群 ID 来源、对象和群列表显示 | 已恢复创建、落盘、加载、名称显示及 ID 查找路径 |
| 是否持久化 | 已静态确认 SQLite，不是仅内存 |
| 存储路径、表、字段、查询 | 已恢复，包括 Green/Public/旧 API 的目录差异 |
| 不修改/不注入的只读获取 | 已给出一致性副本与只读精确查询步骤 |
| 是否已经可靠取得实际测试群 ID | 尚未：没有读取 Windows 实际群元数据 |

仍需 Windows 操作者核实：当前实例实际选中的文件、表后缀归属、实际 schema 和标准 SQLite 可读性、名称匹配是否恰好一行，以及当前群成员资格。未测试运行中更新、退出刷新时机、断电恢复、重命名后缓存时效；磁盘快照不作为当前在线状态或消息送达证据。

### 平台资料

- Microsoft：<https://learn.microsoft.com/en-us/windows/win32/shell/knownfolderid>，核对 Public 与 ProgramData 的不同 GUID 和默认位置。
- Microsoft：<https://learn.microsoft.com/en-us/windows/win32/api/shldisp/ne-shldisp-shellspecialfolderconstants>，`ssfCOMMONAPPDATA == 0x23`。
- SQLite：<https://www.sqlite.org/cli.html>，`-readonly` 打开只读数据库；<https://www.sqlite.org/uri.html>，程序化读取可使用 `mode=ro`。仅 `PRAGMA query_only` 不等于以只读权限打开文件。
