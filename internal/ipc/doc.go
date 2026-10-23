// Package ipc 提供内网通固定文本的非交互式IPC Adapter。
// LoadConfig读取显式群/用户授权配置，NewAdapter只读绑定客户端，Preflight重新复核身份，
// SendGroup和SendUser按显式请求标识持久化并调用一次，返回结构化结果，不读取输入或输出提示。
// SendUser显式接收原始用户ID，必须与构造时绑定的授权用户完全相同，不根据参数切换授权目标。
// group_id与user_id均已有固定正文的独立接收成功证据；不代表任意正文或其他目标发送已验证。
// Win32完成仍为Unknown；只有调用者提供的独立接收观察才能产生RecipientObserved。
// 模块复用固定载荷、窗口定位和Win32实现，不自动生成任务、切换目标或重试。
package ipc
