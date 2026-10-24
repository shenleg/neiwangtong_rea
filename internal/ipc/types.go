// 本文件定义Adapter的结构化预检、调用、单次发送和外部接收观察，不依赖控制台或CLI。
package ipc

import "neiwangtong-gateway/internal/windowprobe"

// Preflight 是只读身份复核结果，不含目标ID、窗口标题或账户SID，不是长期发送许可。
type Preflight struct {
	PID                  uint32             `json:"pid"`
	HWND                 windowprobe.Handle `json:"hwnd"`
	CreationTime         uint64             `json:"creation_time_ticks,string"`
	SHA256               string             `json:"sha256"`
	SessionID            uint32             `json:"session_id"`
	SameUserAndIntegrity bool               `json:"same_user_and_integrity"`
	SameDesktop          bool               `json:"same_desktop"`
	Reliability          string             `json:"reliability"`
}

// CallResult 保存一次Win32调用事实；Completed和有效LResult不代表送达。
type CallResult struct {
	Invoked          bool   `json:"invoked"`
	APIReturn        uint64 `json:"api_return"`
	Completed        bool   `json:"completed"`
	LastError        uint32 `json:"last_error"`
	LResult          uint64 `json:"lresult"`
	LResultValid     bool   `json:"lresult_valid"`
	Timeout          string `json:"timeout"`
	ElapsedMS        int64  `json:"elapsed_ms"`
	ClientAliveAfter *bool  `json:"client_alive_after"`
}

// Delivery 区分发送调用与独立接收观察；不能由API返回值推导。
type Delivery string

const (
	// Unknown 表示没有独立接收成功证据，包含API完成但尚未观察的情况。
	Unknown Delivery = "unknown"
	// RecipientObserved 表示外部观察者明确确认正确目标、正确发送者的一条本次新增消息。
	RecipientObserved Delivery = "recipient_observed"
)

// SendResult 即使发送失败也保留已发生事实；AttemptID是内部生成的记录标识，AttemptReserved表示已占用日志。
// SendGroup和SendUser的Delivery始终为Unknown；调用者可用AttemptID向RecordObservation提供独立接收证据。
// AttemptID仅用于关联记录，不用于调用方重复请求去重；再次调用发送方法会创建新请求。
type SendResult struct {
	AttemptID       string     `json:"attempt_id"`
	AttemptReserved bool       `json:"attempt_reserved"`
	Preflight       Preflight  `json:"preflight"`
	Call            CallResult `json:"call"`
	Delivery        Delivery   `json:"delivery"`
}

// ObservationKind 标识外部观察来源，不表示Adapter自动读取了客户端UI或聊天。
type ObservationKind string

const (
	// ClientObservation 记录客户端stable、crashed、abnormal或unknown状态。
	ClientObservation ObservationKind = "client_observation"
	// SenderObservation 记录发送方对本次新增消息的观察。
	SenderObservation ObservationKind = "sender_observation"
	// RecipientObservation 记录独立接收方对正确目标、发送者及本次新增消息的观察。
	RecipientObservation ObservationKind = "recipient_observation"
)

// Observation 是调用者提供的非内容性观察；State使用原实验的固定状态词，不接受正文。
type Observation struct {
	Kind  ObservationKind `json:"event"`
	State string          `json:"state"`
}
