// Package ipc 提供显式群/用户目标及自定义正文的非交互式IPC Adapter。
// LoadConfig供实验CLI读取目标，NewAdapter只读绑定客户端，Preflight重新复核身份。
// SendGroup和SendUser兼容字面换行并检查序列化预算，内部生成标识、持久化记录后只调用一次。
// Adapter不保存目标白名单，不预转义或拆分正文，不读取控制台，不自动重试或切换客户端。
// 群与单聊已有固定、自定义三行及500汉字五段正文的独立接收成功证据。
// 261003字真实验证随后出现客户端异常；当前仅检查序列化空间，尚无额外产品限额。
// Win32完成仍为Unknown；只有调用者提供的独立接收观察才能产生RecipientObserved。
package ipc
