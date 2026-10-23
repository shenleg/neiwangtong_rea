// Package ipc 提供内网通固定群文本的非交互式IPC Adapter。
// LoadConfig读取单群配置，NewAdapter只读绑定客户端，Preflight重新复核身份，
// SendGroup按显式请求标识持久化并调用一次，返回结构化结果，不读取输入或输出提示。
// Win32完成仍为Unknown；只有调用者提供的独立接收观察才能产生RecipientObserved。
// 模块复用已验证载荷、窗口定位和Win32实现，不自动生成任务、切换目标或重试。
package ipc
