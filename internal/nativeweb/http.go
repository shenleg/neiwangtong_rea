// 本文件提供经认证的HTTP、内嵌聊天页及Swagger文档，并校验Host、Origin和JSON输入。
package nativeweb

import (
	"bytes"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"unicode/utf8"

	"neiwangtong-gateway/internal/nativeproto"
)

//go:embed assets/index.html assets/app.css assets/app.js assets/docs/* assets/swagger/*
var assets embed.FS

// NewHandler 创建受认证保护的网页及API；address为实际监听端点，output接收首次初始化码。
// 凭据、输出或监听地址无效时返回错误，不进行网络连接。
func NewHandler(manager *Manager, address netip.AddrPort, output io.Writer) (http.Handler, error) {
	if !address.IsValid() || !address.Addr().Is4() || address.Port() == 0 || !(address.Addr().IsLoopback() || address.Addr().IsPrivate() || address.Addr().IsUnspecified()) {
		return nil, fmt.Errorf("网页须绑定回环、私有或0.0.0.0 IPv4地址及非零端口")
	}
	auth, err := newAuth(manager.config.DataDir, output)
	if err != nil {
		return nil, err
	}
	mux := http.NewServeMux()
	registerPublicAPI(mux, manager)
	if err := registerDocs(mux); err != nil {
		return nil, err
	}
	mux.HandleFunc("GET /api/state", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, manager.State())
	})
	mux.HandleFunc("GET /api/contacts", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"contacts": manager.State().Contacts, "complete": false})
	})
	mux.HandleFunc("GET /api/groups", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"groups": manager.State().Groups, "complete": false})
	})
	mux.HandleFunc("POST /api/session/start", func(w http.ResponseWriter, r *http.Request) {
		fields, err := decodeFields(w, r, nil, []string{"name"})
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		name, provided := fields["name"]
		if !provided {
			name = manager.State().Name // {}明确沿用当前名称；显式空字符串由Start拒绝，不静默改用默认值。
		}
		if err := manager.Start(name); err != nil {
			status := http.StatusConflict
			if errors.Is(err, ErrInvalidName) {
				status = http.StatusBadRequest
			}
			writeError(w, status, err)
			return
		}
		writeJSON(w, http.StatusAccepted, manager.State())
	})
	mux.HandleFunc("POST /api/session/stop", func(w http.ResponseWriter, r *http.Request) {
		if _, err := decodeObject(w, r); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		manager.Stop()
		writeJSON(w, http.StatusAccepted, manager.State())
	})
	for _, kind := range []string{"user", "group"} {
		kind := kind
		mux.HandleFunc("POST /api/messages/"+kind, func(w http.ResponseWriter, r *http.Request) {
			targetKey := kind + "_id"
			fields, err := decodeObject(w, r, "request_id", targetKey, "text")
			if err != nil {
				writeError(w, http.StatusBadRequest, err)
				return
			}
			request := nativeproto.ChatRequest{Text: fields["text"]}
			if kind == "group" {
				request.GroupID = fields[targetKey]
			} else {
				request.UserID = fields[targetKey]
			}
			result, status := manager.Send(r.Context(), fields["request_id"], request)
			writeJSON(w, status, result)
		})
	}
	for path, file := range map[string]string{"/": "index.html", "/app.css": "app.css", "/app.js": "app.js"} {
		path, file := path, file
		data, err := assets.ReadFile("assets/" + file)
		if err != nil {
			return nil, err
		}
		contentType := map[string]string{"index.html": "text/html; charset=utf-8", "app.css": "text/css; charset=utf-8", "app.js": "text/javascript; charset=utf-8"}[file]
		mux.HandleFunc("GET "+path, func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != path {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Content-Type", contentType)
			_, _ = w.Write(data)
		})
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		stylePolicy := "'self'"
		if r.URL.Path == "/docs" || r.URL.Path == "/swagger/index.html" || strings.HasPrefix(r.URL.Path, "/docs/") {
			stylePolicy += " 'unsafe-inline'" // Swagger UI通过React设置部分元素样式；仅放行文档样式，不开放内联脚本。
		}
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src "+stylePolicy+"; connect-src 'self'; img-src 'self' data:; object-src 'none'; base-uri 'none'; frame-ancestors 'none'; form-action 'self'")
		local := address
		if address.Addr().IsUnspecified() {
			// 通配地址不是浏览器访问地址；使用当前TCP连接实际到达的本机IP，不信任Host或转发头来推断。
			endpoint, ok := r.Context().Value(http.LocalAddrContextKey).(*net.TCPAddr)
			if !ok {
				writeError(w, http.StatusForbidden, fmt.Errorf("无法确认请求到达的本机地址"))
				return
			}
			local = endpoint.AddrPort()
			local = netip.AddrPortFrom(local.Addr().Unmap(), local.Port())
			if !local.Addr().Is4() || local.Addr().IsUnspecified() || local.Port() != address.Port() {
				writeError(w, http.StatusForbidden, fmt.Errorf("无效的本机HTTP端点"))
				return
			}
		}
		allowedHost := r.Host == local.String() || local.Addr().IsLoopback() && r.Host == "localhost:"+strconv.Itoa(int(local.Port()))
		if !allowedHost || r.Header.Get("Sec-Fetch-Site") == "cross-site" {
			writeError(w, http.StatusForbidden, fmt.Errorf("不允许的Host或跨站请求"))
			return
		}
		if origin := r.Header.Get("Origin"); origin != "" && origin != "http://"+r.Host {
			writeError(w, http.StatusForbidden, fmt.Errorf("仅允许同源请求"))
			return
		}
		if r.Method == http.MethodPost || r.Method == http.MethodDelete {
			if mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type")); err != nil || mediaType != "application/json" {
				writeError(w, http.StatusUnsupportedMediaType, fmt.Errorf("请求必须使用application/json"))
				return
			}
		}
		if strings.HasPrefix(r.URL.Path, "/auth/") {
			auth.serve(w, r)
			return
		}
		auth.mu.Lock()
		err := auth.checkDisk()
		allowed, cookie := auth.authenticate(r)
		auth.mu.Unlock()
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		publicAsset := r.Method == http.MethodGet && (r.URL.Path == "/" || r.URL.Path == "/app.js" || r.URL.Path == "/app.css")
		if !publicAsset && !allowed {
			writeError(w, http.StatusUnauthorized, fmt.Errorf("需要认证"))
			return
		}
		if allowed && cookie && r.Method != http.MethodGet && r.Method != http.MethodHead && r.Header.Get("Origin") != "http://"+r.Host {
			writeError(w, http.StatusForbidden, fmt.Errorf("必须提供同源Origin"))
			return
		}
		mux.ServeHTTP(w, r)
	}), nil
}

// decodeObject 在外部输入处拒绝未知/重复/缺失字段、非字符串、非法UTF-8、超长及尾随数据，错误不含正文。
func decodeObject(w http.ResponseWriter, r *http.Request, keys ...string) (map[string]string, error) {
	return decodeFields(w, r, keys, nil)
}

// decodeFields 共用严格JSON读取，required为必填字段，optional为允许但可缺失的字段；不对字符串作默认值或类型转换。
func decodeFields(w http.ResponseWriter, r *http.Request, required, optional []string) (map[string]string, error) {
	// 4096字节正文最坏经JSON转义膨胀六倍，剩余空间容纳固定键及目标ID。
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, nativeproto.MaxTextBytes*6+4096))
	if err != nil {
		return nil, fmt.Errorf("请求体读取失败或超出预算")
	}
	if !utf8.Valid(data) {
		return nil, fmt.Errorf("请求必须为合法UTF-8")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return nil, fmt.Errorf("请求必须是JSON对象")
	}
	allowed := make(map[string]bool, len(required)+len(optional))
	for _, key := range required {
		allowed[key] = true
	}
	for _, key := range optional {
		allowed[key] = true
	}
	fields := make(map[string]string, len(allowed))
	for decoder.More() {
		token, err := decoder.Token()
		key, ok := token.(string)
		if err != nil || !ok || !allowed[key] {
			return nil, fmt.Errorf("请求包含未知字段")
		}
		if _, exists := fields[key]; exists {
			return nil, fmt.Errorf("请求字段不得重复")
		}
		token, err = decoder.Token()
		value, ok := token.(string)
		if err != nil || !ok {
			return nil, fmt.Errorf("请求字段必须为字符串")
		}
		fields[key] = value
	}
	if token, err := decoder.Token(); err != nil || token != json.Delim('}') {
		return nil, fmt.Errorf("请求JSON未完整结束")
	}
	if _, err := decoder.Token(); err != io.EOF {
		return nil, fmt.Errorf("请求存在尾随数据")
	}
	for _, key := range required {
		if _, ok := fields[key]; !ok {
			return nil, fmt.Errorf("请求缺少必填字段%s", key)
		}
	}
	return fields, nil
}

func writeError(w http.ResponseWriter, status int, err error) {
	writeJSON(w, status, map[string]string{"error": err.Error()})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value) // 浏览器断开属于HTTP输出失败，不中断后台会话或重新发送。
}
