# 内网通独立消息网关项目

## 项目目标

基于内网通 3.4.3055，开发独立的 Windows 消息网关。

核心需求：
- 支持向指定 user_id 发送私聊消息
- 支持向已有原生群 group_id 发送群聊消息
- 使用正常登录的授权客户端身份
- 不修改原始 ShiYeLine.exe
- 不使用 DLL 注入、提权或认证绕过
- 最终使用 Go 开发独立网关并提供 HTTP API

## 当前技术路线

首选：WM_COPYDATA 业务 IPC。

客户端仍需正常运行并完成登录。

暂不开发：
- IPMsg 独立机器人
- Windows UI Automation
- 自行实现原生网络协议
- 修改 EXE 或进程注入

上述方案只保留为备用研究方向。

## 已确认的逆向发现

目标程序：
- 内网通 3.4.3055
- 主程序：ShiYeLine.exe
- 样本路径：`/root/reverse-lab/extracted/app/ShiYeLine.exe`
- 技术栈：原生 x86 C++ / SkinUI
- 分析工具：REA + Ghidra

WM_COPYDATA：
- 主窗口处理入口：0x449c60（E22）
- 文本业务分支：dwData == 0x0fecf10f（E22）
- 三个字符串区域：0 为选择器、0x64 为目标值、0x12c 为正文（E22、E23）
- 三个区域均按 UTF-8、NUL 终止方式读取：0x44cbd0（E22）；字段容量尚未恢复
- 目标选择器分流：0x4c41b0
- 单聊发送：0x4b9320
- 原生群聊发送：0x617600

候选目标选择器：
- user_id
- group_id
- user_name
- group_name
- user_ip

首版仅允许 user_id、group_id。

以上结论来自静态分析，尚未经过运行时验证。

## 当前未解决的问题

1. 文本业务载荷的生产者及完整 COPYDATASTRUCT 构造规则（dwData 分支值已确认）
2. cbData 长度要求
3. lpData 完整字段布局和总大小（三个字段用途已确认）
4. 三个字符串字段的容量及生产者终止符写入规则（接收端按 NUL 读取已确认）
5. 最大消息长度及特殊字符处理
6. 主窗口的可靠查找方式
7. IPC 调用结果和实际送达的区别
8. 错误目标、离线用户及群权限不足时的行为

严禁根据猜测补全上述字段。

## 分析资料

- IPC 字段、证据映射及恢复任务：`docs/research/ipc/ipc-contract.md`。开始 IPC 契约恢复或发送器设计前先读此文；协议未完整恢复并复核，尚不可实现发送器。
- 历史报告及 E22–E25 索引：`docs/research/reports/gateway-static/gateway-report.md`。
- 完整证据：`docs/research/reports/gateway-static/gateway-evidence.json`。
- 分析快照：`docs/research/reports/gateway-static/gateway-analysis.snapshot.json`；含相同的 245 条证据记录，不是完整 Ghidra 工程。

开始新任务前：
1. 先检查上述契约文档、报告和证据索引
2. 只读取与当前任务有关的报告
3. 不要一次性加载所有大型证据文件
4. 结论必须提供函数地址或 Evidence ID
5. 区分已确认事实、强推断和未验证假设

## 工作规则

- 当前阶段只读静态分析
- 不执行目标 EXE
- 不读取真实聊天记录和凭据
- 未获得明确测试授权前不发送消息
- 不直接修改原始客户端
- 不提交二进制样本、令牌或私人数据到 Git
- 优先复用已有证据，避免重复完整反编译
- 大型任务拆成独立阶段执行

## 下一阶段任务

已完成 E22–E25 文档整理；dwData 分支、三个字段用途及接收端编码/终止方式已补入契约，尚未动态验证。

下一轮按 `docs/research/ipc/ipc-contract.md` 第 6 节，先查载荷生产者与 cbData/容量，再复核正文、目标解析、窗口和返回值。

在完成并复核协议契约之前，不编写实际发送器。

完成后设计 Windows Go 最小验证程序：
- 只允许一个授权测试群
- 固定一条测试消息
- 只调用一次
- 不自动重试
- 结果不确定时记录 unknown
