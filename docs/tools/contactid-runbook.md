# 真实联系人 user_id 提取操作指引

本文是"联系人原始姓名 → user_id"的端到端操作流程。**推荐使用 Go 实现**：`internal/contacts` 包（`contacts.Open` / `LoadUsers` / `LookupName`）与 `cmd/contactlookup` 命令，纯标准库实现已验证的 nr codec 与只读 SQLite 解析，无需下载源码或编译 codec DLL。需要审计级严格 schema 检查时可改用 [Python 只读工具](contactid.md)，两者依据同一份[联系人研究](../research/reports/user-id/user-id-report.md)与[codec 报告](../research/reports/nr-codec/nr-codec-report.md)。

本文所有私有路径为占位符，不含任何真实身份、口令或联系人数据。

## 1. 准备离线私有副本

1. 在客户端界面确认目标联系人的**原始名称**——不是本地备注；界面显示名可能被备注覆盖。
2. 定位 `nr`：本实例上次核实走 Green 分支，即 `<EXE目录>\Syl\cache\db\nr`。客户端重装或路径变化时按联系人研究 4.1 节的四个分支重新定位，不递归搜索同名文件。
3. 正常退出客户端，确认无同名进程。
4. 复制 `nr` 到私有目录（如 `C:\私有检查目录\nr-copy`），确认源与副本 SHA-256 一致。
5. 源或副本旁存在 `nr-copy-wal`、`-shm`、`-journal` 任一文件即停止（空文件也不例外）；工具也会拒绝，不删除、不修复。

## 2. 准备口令文件

口令是上次授权会话从客户端静态常量核实的**原始窄字符串字节**，只存在于私有记录。将其按精确字节写入私有文件（如 `C:\私有检查目录\codec-password.bin`）：

- 工具从 stdin 读至 EOF 且**不去除末尾换行**：文件内容必须恰好是口令字节，末尾不要附加回车或换行。
- 口令不进命令行、不进环境变量、不提交仓库。

## 3. 用 Go 工具查询

在 Git Bash 中从项目根目录执行（`<` 字节重定向 PowerShell 5.1 不支持）：

```bash
go build -o contactlookup.exe ./cmd/contactlookup

# 先确认身份表：多表时报错并列出全部表名；单表直接进入查询
./contactlookup.exe -nr 'C:\私有检查目录\nr-copy' < 'C:\私有检查目录\codec-password.bin'

# 指定身份表与原始名称精确查询
./contactlookup.exe -nr 'C:\私有检查目录\nr-copy' \
  -table 'OrgStruct_已核实的本机身份后缀' -name '目标联系人原始名称' \
  < 'C:\私有检查目录\codec-password.bin'
```

表名后缀须与私有记录中的本机原始身份后缀一致；多张表时必须人工核实后指定，不能挑第一张。省略 `-name` 输出该表全部用户联系人。库函数用法见 `internal/contacts` 包文档：`contacts.Open(path, password)` 后调用 `LookupName(table, name)`。

## 4. 结果判读

| 退出码 / `status` | 含义 | 操作 |
|---|---|---|
| 0 `unique` | 恰好一行匹配 | 取 `candidates[0].user_id`，原样进入第 5 步 |
| 3 `not_found` | 零匹配 | 名称不是缓存中的原始 Name：核对是否为备注、本机身份名或改名未刷新；不模糊重试 |
| 4 `ambiguous` | 多个同名候选 | 按 `candidates` 在客户端界面用公司、部门等元数据人工消歧后重新确认；不自动选、不试发 |
| 1 / 2 | 文件、口令、身份或参数错误 | 按错误信息处理；不跳过坏行、不换口令或页长参数 |

- `user_id` 必须原样使用：不 trim、不改大小写、不转码；ID 含罕见字符时从 JSON 输出复制，不手工重打。
- 查询结果只证明本次副本中该姓名唯一对应此 ID，不证明对方在线或可送达。

## 5. 绑定到发送配置

将唯一匹配的原始 ID 写入**仓库外**私有配置（与群配置同一格式，两者可并存）：

```json
{"authorized_user_id": "从查询结果原样复制的ID"}
```

`ipc.LoadConfig` 的校验要求：有效 UTF-8、1 至 199 字节、不含 NUL、JSON 字符串无孤立代理项；不满足时整体拒绝，不回显配置值。

单聊发送路径已接线：`cmd/ipcexperiment` 支持 `-config` 加载仅含 `authorized_user_id` 的配置，`-send` 后输入完整确认短语即调用一次 `SendUser`（Windows 386 构建）；发送与观察可用 `-observe/-observe-kind/-observe-state` 分开执行。群与用户目标同时存在时拒绝发送。每次真实发送与观察仍须单独授权，不自动重试。

## 附：测试库重建

`internal/contacts/testdata` 的合成加密库由 `tools/gen_contacts_fixture.py` 生成（依赖仓库内证据文件与 openssl），清单记录密文摘要；测试打开时会校验摘要，不符时重新生成即可。
