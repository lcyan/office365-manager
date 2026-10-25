package web

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"
	"time"
)

// SessionManager 实现与 Flask 语义一致的有签名 cookie 会话：
// 会话数据 JSON 序列化后存入 cookie，HMAC-SHA256 签名防篡改，
// 数据不加密（与 Flask 一致，勿存放敏感信息）。签名密钥来自 SESSION_SECRET。
type SessionManager struct {
	secret   []byte
	cookie   string
	secure   bool
	lifetime time.Duration
}

// NewSessionManager 构造会话管理器。
func NewSessionManager(secret []byte, secureCookie bool) *SessionManager {
	return &SessionManager{
		secret:   secret,
		cookie:   "o365_session",
		secure:   secureCookie,
		lifetime: 12 * time.Hour,
	}
}

// sessionState 是单个请求的会话状态；dirty 标记有修改需写回 cookie。
type sessionState struct {
	data  map[string]any
	dirty bool
}

type ctxKey int

const sessionStateKey ctxKey = 0

// Middleware 解析请求 cookie 中的会话并放入请求上下文。
func (m *SessionManager) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		state := &sessionState{data: map[string]any{}}
		if c, err := r.Cookie(m.cookie); err == nil && c.Value != "" {
			if data, ok := m.decode(c.Value); ok {
				state.data = data
			}
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), sessionStateKey, state)))
	})
}

// Save 在响应写出前把会话写回 cookie。无修改时不产生 Set-Cookie。
func (m *SessionManager) Save(w http.ResponseWriter, r *http.Request) {
	state, _ := r.Context().Value(sessionStateKey).(*sessionState)
	if state == nil || !state.dirty {
		return
	}
	cookie := http.Cookie{
		Name:     m.cookie,
		Path:     "/",
		HttpOnly: true,
		Secure:   m.secure,
		SameSite: http.SameSiteLaxMode,
	}
	if len(state.data) == 0 {
		cookie.MaxAge = -1 // 立即删除
	} else {
		cookie.Value = m.encode(state.data)
		cookie.MaxAge = int(m.lifetime.Seconds())
	}
	http.SetCookie(w, &cookie)
}

func (m *SessionManager) encode(data map[string]any) string {
	payload, err := json.Marshal(data)
	if err != nil {
		return ""
	}
	mac := hmac.New(sha256.New, m.secret)
	mac.Write(payload)
	return base64.RawURLEncoding.EncodeToString(payload) + "." +
		base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func (m *SessionManager) decode(value string) (map[string]any, bool) {
	payload, sig, found := strings.Cut(value, ".")
	if !found {
		return nil, false
	}
	pb, err1 := base64.RawURLEncoding.DecodeString(payload)
	sb, err2 := base64.RawURLEncoding.DecodeString(sig)
	if err1 != nil || err2 != nil {
		return nil, false
	}
	mac := hmac.New(sha256.New, m.secret)
	mac.Write(pb)
	if !hmac.Equal(mac.Sum(nil), sb) {
		return nil, false
	}
	var data map[string]any
	if err := json.Unmarshal(pb, &data); err != nil {
		return nil, false
	}
	return data, true
}

// --- Server 侧会话辅助 ---

func (s *Server) session(r *http.Request) *sessionState {
	state, _ := r.Context().Value(sessionStateKey).(*sessionState)
	return state
}

func (s *Server) sessGet(r *http.Request, key string) any {
	if st := s.session(r); st != nil {
		return st.data[key]
	}
	return nil
}

func (s *Server) sessSet(r *http.Request, key string, value any) {
	if st := s.session(r); st != nil {
		st.data[key] = value
		st.dirty = true
	}
}

func (s *Server) sessRemove(r *http.Request, keys ...string) {
	if st := s.session(r); st != nil {
		changed := false
		for _, key := range keys {
			if _, ok := st.data[key]; ok {
				delete(st.data, key)
				changed = true
			}
		}
		st.dirty = st.dirty || changed
	}
}

const (
	sessionAuthKey = "logged_in"
	sessionOrgKey  = "org_id"
	sessionCSRFKey = "csrf_token"
	sessionMemKey  = "flashes"
)

func (s *Server) isLoggedIn(r *http.Request) bool {
	logged, _ := s.sessGet(r, sessionAuthKey).(bool)
	return logged
}

func (s *Server) login(r *http.Request) {
	s.sessSet(r, sessionAuthKey, true)
}

// logout 清空全部会话数据（cookie 将被删除）。
func (s *Server) logout(r *http.Request) {
	if st := s.session(r); st != nil {
		st.data = map[string]any{}
		st.dirty = true
	}
}

func (s *Server) activeOrgID(r *http.Request) string {
	id, _ := s.sessGet(r, sessionOrgKey).(string)
	return id
}

func (s *Server) setActiveOrg(r *http.Request, orgID string) {
	s.sessSet(r, sessionOrgKey, orgID)
}

func (s *Server) clearActiveOrg(r *http.Request) {
	s.sessRemove(r, sessionOrgKey)
}

// forceRelogin 清除登录态与关联会话键。签名密钥不变，但登录态随之失效，
// 用于修改密码后强制重新登录。
func (s *Server) forceRelogin(r *http.Request) {
	s.sessRemove(r, sessionAuthKey, sessionOrgKey, sessionCSRFKey)
}

// csrfToken 返回当前会话的 CSRF 令牌；不存在时生成并写入会话。
func (s *Server) csrfToken(r *http.Request) string {
	token, _ := s.sessGet(r, sessionCSRFKey).(string)
	if token == "" {
		buf := make([]byte, 32)
		if _, err := rand.Read(buf); err != nil {
			return ""
		}
		token = hex.EncodeToString(buf)
		s.sessSet(r, sessionCSRFKey, token)
	}
	return token
}

// addFlash 追加一条闪现消息，供下一次请求渲染。
func (s *Server) addFlash(r *http.Request, msg string) {
	list, _ := s.sessGet(r, sessionMemKey).([]any)
	msgs := make([]string, 0, len(list)+1)
	for _, item := range list {
		if str, ok := item.(string); ok {
			msgs = append(msgs, str)
		}
	}
	msgs = append(msgs, msg)
	s.sessSet(r, sessionMemKey, msgs)
}

// takeFlashes 取出并清空闪现消息。
func (s *Server) takeFlashes(r *http.Request) []string {
	list, _ := s.sessGet(r, sessionMemKey).([]any)
	if len(list) == 0 {
		return nil
	}
	msgs := make([]string, 0, len(list))
	for _, item := range list {
		if str, ok := item.(string); ok {
			msgs = append(msgs, str)
		}
	}
	s.sessRemove(r, sessionMemKey)
	return msgs
}

// secureEqual 常数时间比较两个字符串。
func secureEqual(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}
