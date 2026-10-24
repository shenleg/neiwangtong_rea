# 只读联系人 ID 提取工具

`tools/contactid.py` 从已确认实例的完整离线 `nr` 副本提取联系人原始名称与 ID。它不查询群成员表、不读取聊天或凭据、不连接客户端进程、不发送消息、不创建 AttemptID，也不修改发送配置。

日常查询推荐改用 Go 实现（`internal/contacts` 包与 `cmd/contactlookup` 命令，见[操作指引](contactid-runbook.md)）：同一 codec 与解析契约的纯标准库版本，无需构建 codec DLL；本文的 Python 工具保留用于审计级严格 schema 检查。

映射依据见[联系人研究](../research/reports/user-id/user-id-report.md)：`OrgStruct_<当前身份>` 中 `Type=1` 的 `Id` 是 IPC 用户身份键，`Data` 中的 XML `U/Name` 是原始名称。固定 codec 和页头依据见[nr-codec 报告](../research/reports/nr-codec/nr-codec-report.md)。

## 1. 构建可信读取库

需要 Python 3.13，以及可信 C 编译器。Windows 已验证 Python amd64 和 Zig 0.15.2；库位数必须与 Python 相同。Go 发送程序的386要求不适用于这个独立Python工具。Linux构建参数已提供，但本轮未在Linux执行。

从项目根目录运行，下列路径均为占位说明：

```powershell
python -I -B tools/build_contactid.py --work-dir "C:\私有工作目录\new-codec-build" --cc "C:\可信工具目录\zig.exe"
```

`--work-dir` 必须是尚不存在的绝对路径。脚本只下载固定提交 `f29b06b424e556cf0ad8b19646b30dfc3e3e36d8`，核对归档 SHA-256 `dfbfb8651ff8b1d6fe14ee928ea002faf351d1ef37bb9a452ba1be87e23c1fe4` 后提取 `src/`，从另一个独立 `build/` 目录编译。不会执行下载目录中的脚本。

输出 `build/contactid_codec.dll` 和 `build/build-manifest.json`。读取时核对提交、归档摘要、包装源码摘要、库摘要和运行版本。构建清单用于一致性检查，不是发行签名；只加载自己信任并按上述流程构建的库，不能加载客户端 DLL 或别人提供的任意 DLL。构建需要网络，实际查询不需要网络。

## 2. 准备离线私有副本

1. 正常客户端中确认当前实例、账户和目标联系人的原始名称。界面备注可能覆盖原名，不能将备注自动当作 `Name`。
2. 按研究报告定位当前实例的 `<数据根目录>\cache\db\nr`。`Green` 分支使用 `<EXE目录>\Syl\cache\db\nr`；不递归挑选同名文件，不改变客户端目录配置。
3. 操作者正常退出客户端，确认退出后取得完整副本，保存于仓库外的私有目录。工具不关闭客户端、不自动复制活动数据库。
4. 原文件或副本存在同名 `-wal`、`-shm`、`-journal` 时停止，空文件也不例外；不删除边文件，不恢复、不修复、不checkpoint。
5. 密钥使用已核实的原始窄字符串字节，不猜编码或口令。工具不从 EXE、进程、配置或环境变量发现实际密钥；本轮没有提取样本实际密钥。

`--offline-confirmed` 是操作者对副本来源和一致性的声明，不能代替实际正常退出；它不会自动证明来源正确或账户归属。

## 3. 先查看 schema，不读联系人行

密钥只从 `--password-stdin` 接收，读至 EOF，不 trim、不去除换行。下面使用 **Git Bash** 的字节重定向；PowerShell 5.1 不支持该 `<` 写法。私有口令文件须已由授权操作者准备，不在仓库内创建，不将密钥放入命令行、环境变量或日志。

```bash
python -I -B tools/contactid.py \
  --snapshot 'C:\私有检查目录\nr-copy' \
  --library-dir 'C:\私有工作目录\new-codec-build\build' \
  --schema-only --offline-confirmed --password-stdin \
  < 'C:\私有检查目录\codec-password.bin'
```

此模式只查询 `sqlite_master` 的 `OrgStruct_*` 表名和列结构，不读取联系人行。输出含表名，其身份后缀也是私有标识，输出应保持私有。多身份时必须由操作者核实当前本机原始身份与后缀的对应，不能挑第一张、取最新一张或按姓名计算。

## 4. 精确查询原始名称

核实身份后指定 `--identity`，工具只读取这个身份的表，即使库中还有其他身份也不读取它们的联系人行。

```bash
python -I -B tools/contactid.py \
  --snapshot 'C:\私有检查目录\nr-copy' \
  --library-dir 'C:\私有工作目录\new-codec-build\build' \
  --identity '已核实的原始身份后缀' --name 'PVE-WIN10' \
  --offline-confirmed --password-stdin \
  < 'C:\私有检查目录\codec-password.bin'
```

这不是发送授权，也不是已取得该名称实际 ID 的记录。

### 查询规则

- 先检查真实表、列顺序与声明类型、主键和 `UNIQUE(Id)` 的非部分 BINARY 唯一索引，不把视图或相似字段当作联系人表。
- 只执行 `SELECT Id, Data FROM 已核实表 WHERE Type=?`，绑定整数1。XML中的其他个人资料不输出或落盘。
- 只接受 `U` 根下唯一的直接 `Name` 文本，禁用 DTD、外部实体与网络访问。按实体还原后的文本精确比较，不 trim、不折叠大小写、不按备注、IP或模糊名称搜索。
- 任一用户行的 ID 非文本/空/NUL、XML损坏、缺少或重复 `Name` 都使本次查询失败，不能跳过坏行后宣称唯一。
- 原样返回 ID，不限制为ASCII或十六进制，不截断、不分割。提取器没有发送载荷长度限制；若ID不适合当前Adapter载荷，应由发送模块明确拒绝，不改写提取结果。
- 同名返回所有精确候选的名称与ID，但状态为 `ambiguous`，不自动选中、绑定或试发。必要的人工消歧不属于本工具的默认资料输出。

### 输出与退出码

JSON包括 `status`、`name`、`matching_rows`、`table`、`candidates`、源密文 `source_sha256`、`page_size`、`checked_at` 与 `codec_commit`。候选仅含 `name` 和 `user_id`；不含XML、口令或其他联系人资料。JSON使用ASCII转义，兼容Windows终端编码，解析后保留原始Unicode文本。

| 退出码 | 含义 |
|---|---|
| 0 | schema-only成功，或恰好一个姓名匹配 |
| 1 | 文件、codec、身份、schema、XML、口令或查询错误；stdout不输出部分结果 |
| 2 | 参数错误 |
| 3 | `not_found`，零匹配 |
| 4 | `ambiguous`，多个同名候选；不能当成绑定成功 |

唯一匹配只说明本次完整副本中该原始名称恰好对应一行，不证明在线、可送达、当前发送权限或名称今后仍唯一。改名后不能仅凭新姓名自动更换已经绑定的原始ID。

## 5. 只读保证与验证

读取器先用已验证算法解密前32字节，检查完整SQLite魔数并恢复准确页长；只接受8种已验证页长、rollback头、reserved=0、标准负载比例及完整页长倍数。随后以 `SQLITE_OPEN_READONLY` 打开加密副本，在 `sqlite3_key` 前显式设置 `aes128cbc / legacy=1 / legacy_page_size`，检查 `sqlite3_db_readonly`，开启 `query_only`。没有格式、口令或页大小试探，也没有普通SQLite替代读取。

codec直接读取加密页，不导出明文数据库。构建时使用 `SQLITE_TEMP_STORE=3`、关闭加载扩展。成功结果输出前再次检查边文件和密文SHA-256；副本变化即拒绝结果。口令不进入命令行、日志或输出JSON。

```powershell
python -I -B tools/test_contactid_records.py
python -I -B tools/test_contactid.py --library-dir "C:\私有工作目录\new-codec-build\build"
```

当前验证：40项纯匹配测试、10项输入/加密集成测试通过；加密测试覆盖全部8种页长、保存的独立头部向量、只读拒写、错误口令、嵌入NUL、schema-only及同名结果。缺少 `--library-dir` 时加密组会明确跳过，该结果不能称为全量验证。

后续另经操作者明确授权，已在客户端正常退出后完成真实私有副本的schema-only和一个指定原始名称的精确查询：页长1024，仅一张身份表，源库与副本哈希一致；指定名称的联系人查询为 `not_found`。操作者随后确认输入为本机身份名称，已依据静态证明和唯一表后缀确认本机原始ID，不将其当作其他联系人名称匹配。实际口令仅在内存使用，不输出、不保存；不改配置、不发送。详见[真实只读验证记录](../research/reports/user-id/readonly-validation.md)。私有结果留在仓库外，不自动写入 `authorized_user_id`。
