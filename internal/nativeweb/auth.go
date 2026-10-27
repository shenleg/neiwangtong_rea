// 本文件管理独立的网页管理员凭据、一次性初始化码、会话和 API 密钥。
package nativeweb

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/argon2"
)

const sessionCookie = "nwt_session"
const sessionLifetime = 24 * time.Hour

type credentials struct {
	Version  int    `json:"version"`
	Salt     []byte `json:"salt"`
	Password []byte `json:"password"`
	APIKey   []byte `json:"api_key,omitempty"`
}
type authManager struct {
	mu        sync.Mutex
	dir       string
	setup     string
	creds     *credentials
	persisted []byte
	sessions  map[string]time.Time
	attempts  []time.Time
}

func randomToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func newAuth(dir string, output io.Writer) (*authManager, error) {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	a := &authManager{dir: dir, sessions: make(map[string]time.Time)}
	marker, me := os.ReadFile(filepath.Join(dir, "auth-initialized"))
	data, ce := os.ReadFile(filepath.Join(dir, "auth.json"))
	if me != nil && !errors.Is(me, os.ErrNotExist) {
		return nil, me
	}
	if ce != nil && !errors.Is(ce, os.ErrNotExist) {
		return nil, ce
	}
	if errors.Is(me, os.ErrNotExist) && errors.Is(ce, os.ErrNotExist) {
		code, err := randomToken()
		if err != nil {
			return nil, err
		}
		a.setup = code
		if _, err := fmt.Fprintf(output, "初始化码：%s\n", code); err != nil {
			return nil, err
		}
		return a, nil
	}
	if me != nil || ce != nil || string(marker) != "nwtweb-auth-v1\n" {
		return nil, fmt.Errorf("认证标记或凭据缺失或损坏")
	}
	var c credentials
	if err := json.Unmarshal(data, &c); err != nil || c.Version != 1 || len(c.Salt) != 16 || len(c.Password) != 32 || (len(c.APIKey) != 0 && len(c.APIKey) != 32) {
		return nil, fmt.Errorf("认证凭据损坏")
	}
	a.creds = &c
	a.persisted = data
	return a, nil
}

// checkDisk 在每次请求前检查凭据是否被删除或修改，避免继续使用失效的内存凭据。
func (a *authManager) checkDisk() error {
	if a.creds == nil {
		for _, name := range []string{"auth-initialized", "auth.json"} {
			_, err := os.Stat(filepath.Join(a.dir, name))
			if err == nil || !errors.Is(err, os.ErrNotExist) {
				return fmt.Errorf("认证状态已变更或无法读取")
			}
		}
		return nil
	}
	marker, err := os.ReadFile(filepath.Join(a.dir, "auth-initialized"))
	if err != nil || string(marker) != "nwtweb-auth-v1\n" {
		return fmt.Errorf("认证标记缺失或损坏")
	}
	data, err := os.ReadFile(filepath.Join(a.dir, "auth.json"))
	if err != nil || subtle.ConstantTimeCompare(data, a.persisted) != 1 {
		return fmt.Errorf("认证凭据缺失或变更")
	}
	return nil
}

// syncAuthDirectory 在支持目录 fsync 的平台持久化目录项；Windows 不支持对目录使用 File.Sync。
func syncAuthDirectory(path string) error {
	if runtime.GOOS == "windows" {
		return nil
	}
	d, err := os.Open(path)
	if err != nil {
		return err
	}
	return errors.Join(d.Sync(), d.Close())
}

func (a *authManager) save(c credentials) error {
	data, err := json.Marshal(c)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(a.dir, ".auth-*")
	if err != nil {
		return err
	}
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(data)
	}
	if err == nil {
		err = f.Sync()
	}
	err = errors.Join(err, f.Close())
	if err != nil {
		return err
	}
	if err = os.Rename(f.Name(), filepath.Join(a.dir, "auth.json")); err != nil {
		return err
	}
	if err = syncAuthDirectory(a.dir); err != nil {
		return err
	}
	a.creds = &c
	a.persisted = data
	return nil
}

func (a *authManager) authenticate(r *http.Request) (bool, bool) {
	if h := r.Header.Values("Authorization"); len(h) != 0 {
		if len(h) != 1 || !strings.HasPrefix(h[0], "Bearer ") || a.creds == nil || len(a.creds.APIKey) != 32 {
			return false, false
		}
		token := strings.TrimPrefix(h[0], "Bearer ")
		if len(token) != 43 {
			return false, false
		}
		raw, err := base64.RawURLEncoding.DecodeString(token)
		if err != nil || len(raw) != 32 || base64.RawURLEncoding.EncodeToString(raw) != token {
			return false, false
		}
		digest := sha256.Sum256([]byte(token))
		return subtle.ConstantTimeCompare(digest[:], a.creds.APIKey) == 1, false
	}
	c, err := r.Cookie(sessionCookie)
	if err != nil {
		return false, false
	}
	expiry, ok := a.sessions[c.Value]
	if !ok {
		return false, false
	}
	if !time.Now().Before(expiry) {
		delete(a.sessions, c.Value)
		return false, false
	}
	return true, true
}

func (a *authManager) newSession(w http.ResponseWriter) error {
	token, err := randomToken()
	if err != nil {
		return err
	}
	expiry := time.Now().Add(sessionLifetime)
	a.sessions[token] = expiry
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: token, Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode, Expires: expiry, MaxAge: int(sessionLifetime.Seconds())})
	return nil
}

func (a *authManager) limit() bool {
	now := time.Now()
	kept := a.attempts[:0]
	for _, t := range a.attempts {
		if now.Sub(t) < time.Minute {
			kept = append(kept, t)
		}
	}
	a.attempts = kept
	if len(kept) >= 5 {
		return false
	}
	a.attempts = append(a.attempts, now)
	return true
}

func (a *authManager) serve(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if err := a.checkDisk(); err != nil {
		writeError(w, 500, err)
		return
	}
	authenticated, cookie := a.authenticate(r)
	if r.URL.Path == "/auth/status" && r.Method == http.MethodGet {
		writeJSON(w, 200, map[string]bool{"initialized": a.creds != nil, "authenticated": authenticated, "api_key_configured": a.creds != nil && len(a.creds.APIKey) != 0})
		return
	}
	if r.Method != http.MethodPost && !(r.URL.Path == "/auth/api-key" && r.Method == http.MethodDelete) {
		http.NotFound(w, r)
		return
	}
	// 认证接口使用 Cookie 或建立 Cookie，必须有明确同源 Origin。
	if r.Header.Get("Origin") != "http://"+r.Host {
		writeError(w, 403, fmt.Errorf("必须提供同源Origin"))
		return
	}
	switch r.URL.Path {
	case "/auth/setup", "/auth/login":
		if !a.limit() {
			writeError(w, 429, fmt.Errorf("尝试过于频繁"))
			return
		}
		keys := []string{"password"}
		if r.URL.Path == "/auth/setup" {
			keys = append(keys, "code")
		}
		fields, err := decodeObject(w, r, keys...)
		if err != nil {
			writeError(w, 400, err)
			return
		}
		password := fields["password"]
		if len(password) < 8 || len(password) > 1024 {
			writeError(w, 400, fmt.Errorf("密码长度必须为8到1024字节"))
			return
		}
		if r.URL.Path == "/auth/setup" {
			if a.creds != nil {
				writeError(w, 409, fmt.Errorf("已经初始化"))
				return
			}
			if subtle.ConstantTimeCompare([]byte(fields["code"]), []byte(a.setup)) != 1 {
				writeError(w, 401, fmt.Errorf("初始化码错误"))
				return
			}
			salt := make([]byte, 16)
			if _, err = rand.Read(salt); err != nil {
				writeError(w, 500, err)
				return
			}
			c := credentials{Version: 1, Salt: salt, Password: argon2.IDKey([]byte(password), salt, 3, 64*1024, 1, 32)}
			f, e := os.OpenFile(filepath.Join(a.dir, "auth-initialized"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
			if e == nil {
				_, e = f.WriteString("nwtweb-auth-v1\n")
				if e == nil {
					e = f.Sync()
				}
				e = errors.Join(e, f.Close())
			}
			if e == nil {
				e = syncAuthDirectory(a.dir)
			}
			if e == nil {
				e = a.save(c)
			}
			if e != nil {
				a.setup = ""
				writeError(w, 500, e)
				return
			}
			a.setup = ""
		} else {
			if a.creds == nil {
				writeError(w, 401, fmt.Errorf("认证失败"))
				return
			}
			hash := argon2.IDKey([]byte(password), a.creds.Salt, 3, 64*1024, 1, 32)
			if subtle.ConstantTimeCompare(hash, a.creds.Password) != 1 {
				writeError(w, 401, fmt.Errorf("认证失败"))
				return
			}
		}
		if err := a.newSession(w); err != nil {
			writeError(w, 500, err)
			return
		}
		writeJSON(w, 200, map[string]any{})
		return
	case "/auth/logout":
		if _, err := decodeObject(w, r); err != nil {
			writeError(w, 400, err)
			return
		}
		if !authenticated || !cookie {
			writeError(w, 401, fmt.Errorf("需要登录"))
			return
		}
		c, _ := r.Cookie(sessionCookie)
		delete(a.sessions, c.Value)
		http.SetCookie(w, &http.Cookie{Name: sessionCookie, Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode, MaxAge: -1})
		writeJSON(w, 200, map[string]any{})
		return
	case "/auth/api-key":
		if _, err := decodeObject(w, r); err != nil {
			writeError(w, 400, err)
			return
		}
		if !authenticated || !cookie {
			writeError(w, 401, fmt.Errorf("需要登录"))
			return
		}
		c := *a.creds
		if r.Method == http.MethodDelete {
			c.APIKey = nil
			if err := a.save(c); err != nil {
				writeError(w, 500, err)
				return
			}
			writeJSON(w, 200, map[string]any{})
			return
		}
		token, err := randomToken()
		if err != nil {
			writeError(w, 500, err)
			return
		}
		digest := sha256.Sum256([]byte(token))
		c.APIKey = digest[:]
		if err = a.save(c); err != nil {
			writeError(w, 500, err)
			return
		}
		writeJSON(w, 200, map[string]string{"api_key": token})
		return
	default:
		http.NotFound(w, r)
	}
}
