# 内网通客户端被动抓包采集说明

本文件记录 2026-10-25 凌晨对内网通客户端（ShiYeLine.exe）的一次受限被动网络采集，供离线协议分析使用。全部产物仅保存在本机私有目录，未上传任何公共服务。

## 1. 目标确认

| 项目 | 值 |
|---|---|
| 进程 | ShiYeLine.exe（唯一实例，无多候选） |
| PID | 12916（进程启动于 2026-10-24 22:37:15） |
| EXE 路径 | `C:\Program Files (x86)\nwt3055\ShiYeLine.exe` |
| EXE SHA-256 | `e7caef3fe744f9fe6ba90dd1d53b7e64241cb54020a8bf37be5e756e2fe881b1`（与预期完全一致） |
| 客户端版本 | 3.4.3055（Windows x86）。EXE 的 VersionInfo 字段为空，版本以任务说明的预期哈希匹配为准 |

确认方式：`Get-Process` / `Get-FileHash` 只读查询磁盘文件，未读取进程内存。

## 2. 时间与时区

- 采集发生于 2026-10-25 01:49–02:03（本地时间）。
- 时区：China Standard Time，UTC+08:00。本文件中所有带 `+08:00` 的时间戳均为 Windows 本地时刻。
- 主采集窗口：02:01:28.05 → 02:02:58.54（精确 90 秒，由 `-a duration:90` 到时正常结束）。

## 3. 采集工具

- **Wireshark dumpcap 4.6.9 (v4.6.9-0-g2d548b197c75) + Npcap 1.88**
  - 本机原无 Wireshark/Npcap/NetMon。最初按任务约束使用系统内置 pktmon，两次尝试不理想（见 §8 过程审计）；经操作者在本会话中明确授权"允许网络下载最合适的抓包工具"后，从官方源 `https://2.na.dl.wireshark.org/win64/Wireshark-4.6.9-x64.exe` 下载并静默安装（默认目录 `C:\Program Files\Wireshark`）。
  - 安装包 Authenticode 签名验证：`CN=Wireshark Foundation`，Status=Valid。
  - 安装包 SHA-256：`bf9b5ce8a89f244c376a9b1a946276eaa06463dde3e33069a34d7f102f5878cf`。
- 抓包接口：dumpcap 接口编号 **4**（`以太网`，`\Device\NPF_{2EF47BE2-9DF2-4036-9796-A4D6C1F3B631}`），Red Hat VirtIO Ethernet Adapter，本机地址 **10.1.1.241/24**。
- 操作系统：Windows 10 21H2（build 19044），64 位；采集以管理员权限运行。

## 4. 采集过滤器及依据

dumpcap 捕获过滤器（BPF）原文：

```
port 9011 or port 9012 or port 9013 or port 9014 or port 2425 or port 56814 or port 56815 or port 56816 or port 59493 or port 60281
```

依据：采集前一刻用 `Get-NetTCPConnection` / `Get-NetUDPEndpoint`（PID 12916）取到的**进程实际拥有端口全集**，未硬编码任何静态线索端口（9011/9012 恰好也在实际端点中）：

| 端口 | TCP/UDP | 端点性质 | 说明 |
|---|---|---|---|
| 9011 | UDP | 绑定 | 节点发现（含广播） |
| 9012 | TCP 监听 + UDP | 监听/绑定 | 主监听口/ENet |
| 9013, 9014 | TCP 监听 | 监听 | 客户端监听口 |
| 2425 | TCP 监听 + UDP | 监听/绑定 | IPMsg 兼容协议，非主要分析对象，因属进程端点仍纳入 |
| 56814, 56815, 56816 | TCP 监听 | 监听 | 动态监听口 |
| 59493 | TCP | 已建立（CloseWait/Bound），对端 74.52.13.6:80 | 唯一外联 TCP 连接 |
| 60281 | UDP | 绑定 | 动态 UDP 口 |

`port N` 匹配 TCP 与 UDP 的源/目的两个方向，因此覆盖上述端口的全部双向流量与广播。快照参数：未设 snaplen（dumpcap 默认完整包，文件中 Capture length=262144），保留完整包内容以支持 TCP 分段与 ENet 分片的离线重组；混杂模式为 dumpcap 默认（写入文件的内容仍被上述过滤器限制）。

## 5. 采集执行与结果（主交付 capture.pcapng）

完整命令行：

```
"C:\Program Files\Wireshark\dumpcap.exe" -i 4 -f "port 9011 or port 9012 or port 9013 or port 9014 or port 2425 or port 56814 or port 56815 or port 56816 or port 59493 or port 60281" -a duration:90 -a filesize:65536 -w capture.pcapng
```

| 项目 | 值 |
|---|---|
| 墙钟起止（本地 +08:00） | 2026-10-25T02:01:28.05 → 2026-10-25T02:02:58.54 |
| 文件内首/末包时间 | 02:01:36.369 → 02:02:57.955（纳秒精度），跨度 81.586 s |
| 停止原因 | `-a duration:90` 到时正常结束（退出码 0；64 MiB 文件上限未触发） |
| 文件大小 | 5,308 字节 |
| 包数量 | 38 |
| 丢包 | 工具报告 0：`Packets received/dropped: 38/0 (pcap:0/dumpcap:0/flushed:0/ps_ifdrop:0)` |
| 文件 SHA-256 | `55697ff51a8512ff479f695023fb13cdc27f702b71c5f5406929a7517a56bae6` |
| 格式校验 | capinfos 4.6.9 读取正常：pcapng / Ethernet / 1 接口 / 严格时间序 |

流量构成（仅 IP:端口 元数据，详见 traffic-summary.txt）：

- UDP 9012：本机 10.1.1.241:9012 ↔ 10.1.1.120:9012（12 帧，贯穿全程 81.6 s）；↔ 10.1.1.47:9012（10 帧，60.1 s）。
- UDP 9011：本机向 10.1.1.47:9011 单播 2 帧；并收到 10.1.1.47 向 10.1.1.255:9011 与 255.255.255.255:9011 的发现广播各 1 帧。
- TCP：74.52.13.6:80 ↔ 10.1.1.241:59493（12 帧，76.8 s，既有长连接的持续心跳）。

## 6. 采集前后端点对比（动态端口核查）

采集前（01:49 与 02:01 两次快照）与采集后（02:03 快照）的进程端点**完全一致**，未出现新端口或消失端口；本窗口内没有证据表明过滤器漏掉动态端口。风险声明：客户端若在采集期间新建临时端口（例如新的 ENet 会话或文件传输），端口级过滤器会漏掉该会话，本次无此情形但不能外推到消息收发时刻。

## 7. 合规声明

- 采集全程被动，未发送任何消息/探测/扫描，未主动连接对端。
- 未退出、重启、登录客户端，未改变其网络状态；采集后确认进程 PID 仍为 12916，客户端无异常。
- 未注入、调试、读取进程内存、聊天数据库或凭据；未修改防火墙与客户端配置。
- 未触碰他人抓包会话；pktmon 过滤器仅为本任务添加并在用后清空复原（无）。
- 原始抓包未做任何修改，未为结果重复采集；所有中间产物（含失败尝试）原样保留。
- 除经授权下载安装 Wireshark/Npcap 外，未安装其他软件。

## 8. 过程审计（三次尝试）

1. **01:49:50 pktmon 尝试 1**：命令语法不适用本机版本（`start capture` → 未知参数），采集从未开始，0 包无文件；过滤器当即清理。
2. **01:49:50–01:55:09 pktmon 尝试 2**：改用 `start --capture` 正常启动（同端口过滤器、同网卡组件 9、完整包、64 MB 循环上限）。原定 90 秒后停止的操作被会话取消打断，实际运行约 5 分钟才手动停止（超出 90 秒授权窗口，如实记录；停止时报"无事件丢失"）。产物保留为补充材料：
   - `capture.etl`（原始 ETL，24,579 字节，SHA-256 `33fab2d58322549e9975332a7b6d6762a8876bf7f7757eec49d8c47240d9978e`）
   - `capture-pktmon-attempt2-overrun.pcapng`（etl2pcap 转换，67 包，跨度 178.6 s：01:52:08→01:55:06，丢弃计数 0，SHA-256 `af042fe953f6fc3c386eb196747709cbe24171dc0339c4746dc4687c30ee8840`）
3. **02:01:28–02:02:58 dumpcap 正式采集**（经授权安装 Wireshark 后）：即 §5 的主交付 `capture.pcapng`。

## 9. 成功判定边界

本次"成功"仅指取得可读取的网络采集文件。**未**捕获启动握手（客户端自 2026-10-24 22:37 起持续运行，90 秒窗口内只有既有连接的心跳与发现流量），也未执行独立进程登录或 1004/3011 等报文的收发。若需启动握手流量，需另行授权一次客户端正常退出/启动，本次未执行。

## 10. 产物清单（本目录）

| 文件 | 说明 |
|---|---|
| `capture.pcapng` | **主交付**：90 秒 dumpcap 被动采集 |
| `capture-info.md` | 本说明 |
| `capture.etl` | 补充：pktmon 尝试 2 原始 ETL（超时运行） |
| `capture-pktmon-attempt2-overrun.pcapng` | 补充：上述 ETL 的 pcapng 转换 |
| `traffic-summary.txt` | tshark 会话级统计（仅 IP/端口元数据） |
| `pre-capture-state.txt` / `pre-capture-state-dumpcap.txt` / `post-capture-state.txt` | 采集前后的进程端点与网卡快照 |
| `timeline.txt` | 各阶段墙钟时间戳与关键输出 |
| `dumpcap-output.txt` | dumpcap 原始输出（含丢包统计） |
| `filters-applied.txt` / `filters-after-remove*.txt` / `stop-output*.txt` / `counters*.txt` / `pktmon-components.txt` | pktmon 阶段审计材料 |
