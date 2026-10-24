// Package contacts 从内网通客户端的离线联系人数据库（nr）只读解析联系人原始名称与 user_id。
//
// 依据 docs/research/reports/user-id/user-id-report.md 与
// docs/research/reports/nr-codec/nr-codec-report.md 的静态结论：nr 是 SQLite3MultipleCiphers
// aes128cbc、legacy=1 模式加密的 SQLite 库；OrgStruct_<本机身份> 表中 Type=1 行的
// Id 列即 IPC SendUser 的 user_id，Data 列 XML 的 U/Name 是原始名称。
//
// 使用流程：Open 用口令解密完整离线副本并完成文件头与表结构校验，Tables 列出身份表，
// LoadUsers/LookupName 在操作者核实的身份表上做原始名称精确匹配。名称可能重复，
// 多个命中由调用方人工选择；本包不发送消息、不修改配置、不写数据库文件。
package contacts
