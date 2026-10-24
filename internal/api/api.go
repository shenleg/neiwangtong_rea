// 本文件实现统一发送 API 的路由与错误映射；处理器只调用 API 接口，
// 不感知窗口或数据库细节，是 HTTP 层与 Agent 层之间的唯一边界。
package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"neiwangtong-gateway/internal/agent"
	"neiwangtong-gateway/internal/contacts"
	"neiwangtong-gateway/internal/groups"
	"neiwangtong-gateway/internal/ipc"
)

// api 是处理器依赖的全部能力；真实实现为 *agent.Agent，测试注入替代。
// API 是处理器依赖的全部能力；真实实现为 *agent.Agent，测试注入替代。
type API interface {
	Preflight() (ipc.Preflight, error)
	ListContacts() (agent.ContactsSnapshot, error)
	ResolveUserName(name string) (contacts.Contact, error)
	ListGroups() (agent.GroupsSnapshot, error)
	ResolveGroupName(name string) (groups.Group, error)
	SendToUser(userID, text string) (ipc.SendResult, error)
	SendToGroup(groupID, text string) (ipc.SendResult, error)
}

// NewMux 注册全部资源路由。
func NewMux(handle API) *http.ServeMux {
	mux := http.NewServeMux()
	registerDocs(mux)
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		info, err := handle.Preflight()
		if err != nil {
			writeError(w, http.StatusServiceUnavailable, err)
			return
		}
		writeJSON(w, http.StatusOK, info)
	})
	mux.HandleFunc("GET /contacts", func(w http.ResponseWriter, r *http.Request) {
		if name := r.URL.Query().Get("name"); name != "" {
			writeContactLookup(w, handle, name)
			return
		}
		snapshot, err := handle.ListContacts()
		if err != nil {
			writeResolveError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, snapshot)
	})
	mux.HandleFunc("GET /groups", func(w http.ResponseWriter, r *http.Request) {
		if name := r.URL.Query().Get("name"); name != "" {
			writeGroupLookup(w, handle, name)
			return
		}
		snapshot, err := handle.ListGroups()
		if err != nil {
			writeResolveError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, snapshot)
	})
	mux.HandleFunc("POST /send/user", func(w http.ResponseWriter, r *http.Request) {
		var request sendRequest
		if !readRequest(w, r, &request) {
			return
		}
		userID, ok := resolveTarget(w, request.UserID, request.Name, func(name string) (string, error) {
			contact, err := handle.ResolveUserName(name)
			if err != nil {
				return "", err
			}
			return contact.UserID, nil
		})
		if !ok {
			return
		}
		result, err := handle.SendToUser(userID, request.Text)
		writeSendResult(w, result, err)
	})
	mux.HandleFunc("POST /send/group", func(w http.ResponseWriter, r *http.Request) {
		var request sendRequest
		if !readRequest(w, r, &request) {
			return
		}
		groupID, ok := resolveTarget(w, request.GroupID, request.Name, func(name string) (string, error) {
			group, err := handle.ResolveGroupName(name)
			if err != nil {
				return "", err
			}
			return group.GroupID, nil
		})
		if !ok {
			return
		}
		result, err := handle.SendToGroup(groupID, request.Text)
		writeSendResult(w, result, err)
	})
	return mux
}

// sendRequest 是发送请求体：ID 与名称二选一，text 必填。
type sendRequest struct {
	UserID  string `json:"user_id,omitempty"`
	GroupID string `json:"group_id,omitempty"`
	Name    string `json:"name,omitempty"`
	Text    string `json:"text"`
}

// readRequest 严格解码 JSON 请求体；任何解析问题都以 400 拒绝，不部分执行。
func readRequest(w http.ResponseWriter, r *http.Request, request *sendRequest) bool {
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return false
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(request); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return false
	}
	if request.Text == "" {
		writeError(w, http.StatusBadRequest, errors.New("text 不能为空"))
		return false
	}
	return true
}

// resolveTarget 处理 ID/名称二选一的目标指定；解析失败时已写出响应并返回 false。
func resolveTarget(w http.ResponseWriter, id, name string, resolve func(string) (string, error)) (string, bool) {
	switch {
	case id != "" && name != "":
		writeError(w, http.StatusBadRequest, errors.New("目标 ID 与名称只能提供其一"))
		return "", false
	case id == "" && name == "":
		writeError(w, http.StatusBadRequest, errors.New("必须提供目标 ID 或名称"))
		return "", false
	case id != "":
		return id, true
	default:
		target, err := resolve(name)
		if err != nil {
			writeResolveError(w, err)
			return "", false
		}
		return target, true
	}
}

// writeSendResult 映射发送结果：成功 200；客户端不可用 503；调用后失败 502；
// 已占用日志前的校验失败（ID/正文非法）400。响应始终携带已发生的调用事实。
func writeSendResult(w http.ResponseWriter, result ipc.SendResult, err error) {
	if err == nil {
		writeJSON(w, http.StatusOK, result)
		return
	}
	response := struct {
		ipc.SendResult
		Error string `json:"error"`
	}{SendResult: result, Error: err.Error()}
	switch {
	case errors.Is(err, agent.ErrClientUnavailable):
		writeJSON(w, http.StatusServiceUnavailable, response)
	case result.AttemptReserved:
		writeJSON(w, http.StatusBadGateway, response)
	default:
		writeJSON(w, http.StatusBadRequest, response)
	}
}

// writeResolveError 统一映射解析类错误：未配置 503、未命中 404、同名歧义 409、其余 500。
func writeResolveError(w http.ResponseWriter, err error) {
	var ambiguousContacts *agent.AmbiguousContactsError
	var ambiguousGroups *agent.AmbiguousGroupsError
	switch {
	case errors.Is(err, agent.ErrNotConfigured), errors.Is(err, agent.ErrConcurrentWrite):
		// 未配置与"读取撞上客户端写事务"都是明确可恢复状态：重试或补配置即可。
		writeError(w, http.StatusServiceUnavailable, err)
	case errors.Is(err, agent.ErrNotFound):
		writeJSON(w, http.StatusNotFound, map[string]any{"status": "not_found", "error": err.Error()})
	case errors.As(err, &ambiguousContacts):
		writeJSON(w, http.StatusConflict, map[string]any{"status": "ambiguous", "error": err.Error(), "candidates": ambiguousContacts.Matches})
	case errors.As(err, &ambiguousGroups):
		writeJSON(w, http.StatusConflict, map[string]any{"status": "ambiguous", "error": err.Error(), "candidates": ambiguousGroups.Matches})
	default:
		writeError(w, http.StatusInternalServerError, err)
	}
}

func writeContactLookup(w http.ResponseWriter, handle API, name string) {
	contact, err := handle.ResolveUserName(name)
	if err != nil {
		writeResolveError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "unique", "candidates": []contacts.Contact{contact}})
}

func writeGroupLookup(w http.ResponseWriter, handle API, name string) {
	group, err := handle.ResolveGroupName(name)
	if err != nil {
		writeResolveError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "unique", "candidates": []groups.Group{group}})
}

func writeJSON(w http.ResponseWriter, code int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	encoder := json.NewEncoder(w)
	encoder.SetEscapeHTML(false)
	// 状态码已写出，编码失败无法回传；连接会被客户端按坏响应处理。
	_ = encoder.Encode(value)
}

func writeError(w http.ResponseWriter, code int, err error) {
	writeJSON(w, code, map[string]string{"error": err.Error()})
}
