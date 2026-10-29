// 本文件沿用远端main的health/contacts/groups/send路由及ID或名称二选一请求，返回真实原生协议事实，不依赖Windows IPC。
package nativeweb

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/url"

	"neiwangtong-gateway/internal/nativeproto"
)

// publicSendResponse 是公开发送接口的扁平结果；保留原生提交事实，不虚构IPC窗口、调用或独立接收证据。
type publicSendResponse struct {
	RequestID string `json:"request_id"`
	nativeproto.ChatResult
	Error string `json:"error,omitempty"`
}

func registerPublicAPI(mux *http.ServeMux, manager *Manager) {
	mux.HandleFunc("POST /session/restart", func(w http.ResponseWriter, r *http.Request) {
		if _, err := decodeObject(w, r); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		if err := manager.Restart(); err != nil {
			writeError(w, http.StatusConflict, err)
			return
		}
		writeJSON(w, http.StatusAccepted, manager.State())
	})
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		state := manager.State()
		status, code := "ready", http.StatusOK
		if state.Phase != "online" {
			status, code = "unavailable", http.StatusServiceUnavailable
		}
		online := 0
		for _, contact := range state.Contacts {
			if contact.Online {
				online++
			}
		}
		writeJSON(w, code, map[string]any{"status": status, "phase": state.Phase, "name": state.Name,
			"own_user_id": state.OwnUserID, "discovery_mode": state.DiscoveryMode,
			"online_contacts": online, "contacts_count": len(state.Contacts), "groups_count": len(state.Groups), "error": state.Error})
	})
	mux.HandleFunc("GET /contacts", func(w http.ResponseWriter, r *http.Request) {
		state := manager.State()
		name, ok := lookupName(w, r)
		if !ok {
			return
		}
		if name == "" {
			writeJSON(w, http.StatusOK, map[string]any{"contacts": state.Contacts, "complete": false, "own_user_id": state.OwnUserID, "source": "native_protocol"})
			return
		}
		matches := matchingContacts(state, "", name)
		if requireUnique(w, len(matches), matches) {
			writeJSON(w, http.StatusOK, map[string]any{"status": "unique", "candidates": matches})
		}
	})
	mux.HandleFunc("GET /groups", func(w http.ResponseWriter, r *http.Request) {
		state := manager.State()
		name, ok := lookupName(w, r)
		if !ok {
			return
		}
		if name == "" {
			writeJSON(w, http.StatusOK, map[string]any{"groups": state.Groups, "complete": false, "own_user_id": state.OwnUserID, "source": "native_protocol"})
			return
		}
		matches := matchingGroups(state, "", name)
		if requireUnique(w, len(matches), matches) {
			writeJSON(w, http.StatusOK, map[string]any{"status": "unique", "candidates": matches})
		}
	})
	for _, kind := range []string{"user", "group"} {
		kind := kind
		mux.HandleFunc("POST /send/"+kind, func(w http.ResponseWriter, r *http.Request) {
			sendPublicMessage(w, r, manager, kind)
		})
	}
}

func lookupName(w http.ResponseWriter, r *http.Request) (string, bool) {
	values, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("查询参数编码无效"))
		return "", false
	}
	query := values["name"]
	if len(query) > 1 {
		writeError(w, http.StatusBadRequest, fmt.Errorf("name只能出现一次"))
		return "", false
	}
	if len(query) == 0 {
		return "", true
	}
	return query[0], true
}

func matchingContacts(state State, id, name string) []ContactView {
	matches := []ContactView{}
	for _, contact := range state.Contacts {
		if id != "" && contact.UserID == id || id == "" && contact.Name == name {
			matches = append(matches, contact)
		}
	}
	return matches
}

func matchingGroups(state State, id, name string) []GroupView {
	matches := []GroupView{}
	for _, group := range state.Groups {
		if id != "" && group.GroupID == id || id == "" && group.Name == name {
			matches = append(matches, group)
		}
	}
	return matches
}

// requireUnique 不从同名目标中挑在线者或第一条；歧义响应保留所有候选，让调用方按原始ID选择。
func requireUnique(w http.ResponseWriter, count int, matches any) bool {
	if count == 0 {
		writeJSON(w, http.StatusNotFound, map[string]any{"status": "not_found", "error": "指定目标未命中"})
		return false
	}
	if count > 1 {
		writeJSON(w, http.StatusConflict, map[string]any{"status": "ambiguous", "error": "名称对应多个目标，请使用原始ID选择", "candidates": matches})
		return false
	}
	return true
}

func sendPublicMessage(w http.ResponseWriter, r *http.Request, manager *Manager, kind string) {
	targetKey := kind + "_id"
	fields, err := decodeFields(w, r, []string{"text"}, []string{targetKey, "name"})
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if fields["text"] == "" {
		writeError(w, http.StatusBadRequest, fmt.Errorf("text不能为空"))
		return
	}
	id, hasID := fields[targetKey]
	name, hasName := fields["name"]
	if hasID == hasName || hasID && id == "" || hasName && name == "" {
		writeError(w, http.StatusBadRequest, fmt.Errorf("必须且只能指定目标ID或name之一"))
		return
	}
	state := manager.State()
	request := nativeproto.ChatRequest{Text: fields["text"]}
	var userOnline bool
	var group GroupView
	if kind == "user" {
		matches := matchingContacts(state, id, name)
		if !requireUnique(w, len(matches), matches) {
			return
		}
		request.UserID, userOnline = matches[0].UserID, matches[0].Online
	} else {
		matches := matchingGroups(state, id, name)
		if !requireUnique(w, len(matches), matches) {
			return
		}
		request.GroupID, group = matches[0].GroupID, matches[0]
	}
	if err := nativeproto.ValidateChatRequest(request); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if state.Phase != "online" || kind == "user" && !userOnline {
		writeError(w, http.StatusServiceUnavailable, fmt.Errorf("后台或目标联系人尚未就绪"))
		return
	}
	if kind == "group" && !group.Sendable {
		writeError(w, http.StatusServiceUnavailable, fmt.Errorf("指定群当前不可发送：%s", group.Reason))
		return
	}
	// 对外接口沿用main的每次调用新请求语义；不会把两个HTTP请求合并，也不会重试未知结果。
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	response, status := manager.Send(r.Context(), hex.EncodeToString(random[:]), request)
	switch status {
	case http.StatusUnprocessableEntity:
		status = http.StatusBadRequest
	case http.StatusConflict:
		status = http.StatusServiceUnavailable
	}
	writeJSON(w, status, publicSendResponse{RequestID: response.RequestID, ChatResult: response.Result, Error: response.Error})
}
