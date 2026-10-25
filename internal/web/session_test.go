package web

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSessionEncodeDecodeRoundTrip(t *testing.T) {
	m := NewSessionManager([]byte("secret"), false)
	data := map[string]any{"logged_in": true, "flashes": []string{"你好"}}
	encoded := m.encode(data)
	decoded, ok := m.decode(encoded)
	if !ok {
		t.Fatalf("decode(valid) failed: %q", encoded)
	}
	if logged, _ := decoded["logged_in"].(bool); !logged {
		t.Errorf("logged_in = %v, want true", decoded["logged_in"])
	}
}

func TestSessionTamperedPayloadRejected(t *testing.T) {
	m := NewSessionManager([]byte("secret"), false)
	encoded := m.encode(map[string]any{"logged_in": false, "csrf_token": "abc"})

	// 攻击者不知道密钥，只能改 payload 后重用旧签名。
	payload, sig, _ := strings.Cut(encoded, ".")
	pb, _ := base64.RawURLEncoding.DecodeString(payload)
	var data map[string]any
	if err := json.Unmarshal(pb, &data); err != nil {
		t.Fatal(err)
	}
	data["logged_in"] = true
	newpb, _ := json.Marshal(data)
	forged := base64.RawURLEncoding.EncodeToString(newpb) + "." + sig
	if forged == encoded {
		t.Fatal("setup: forged cookie identical to original")
	}
	if _, ok := m.decode(forged); ok {
		t.Error("decode accepted payload with stale signature")
	}
	if _, ok := m.decode("garbage"); ok {
		t.Error("decode accepted garbage")
	}
	if _, ok := m.decode(""); ok {
		t.Error("decode accepted empty")
	}
	// 用别的密钥签名的 cookie 也要拒绝。
	other := NewSessionManager([]byte("other-secret"), false)
	if _, ok := other.decode(encoded); ok {
		t.Error("decode accepted signature from different secret")
	}
}

func TestSessionMiddlewareEndToEnd(t *testing.T) {
	m := NewSessionManager([]byte("secret"), false)
	handler := m.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		state, _ := r.Context().Value(sessionStateKey).(*sessionState)
		if state == nil {
			t.Error("session state missing in context")
			return
		}
		if _, ok := state.data["logged_in"]; ok {
			t.Error("unexpected logged_in in fresh session")
		}
		st := &serverForTest{m: m}
		st.login(r)
		m.Save(w, r)
	}))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	handler.ServeHTTP(rec, req)

	cookies := rec.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("cookies = %d, want 1", len(cookies))
	}
	value := cookies[0].Value

	// 携带该 cookie 再请求：应能读回 logged_in。
	var saw bool
	verify := m.Middleware(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		state, _ := r.Context().Value(sessionStateKey).(*sessionState)
		saw = state != nil && state.data["logged_in"] == true
	}))
	req2 := httptest.NewRequest(http.MethodGet, "/", nil)
	req2.AddCookie(&http.Cookie{Name: "o365_session", Value: value})
	verify.ServeHTTP(httptest.NewRecorder(), req2)
	if !saw {
		t.Error("logged_in not restored from cookie")
	}
}

// serverForTest 仅用于触发会话写入。
type serverForTest struct {
	m *SessionManager
}

func (s *serverForTest) sessSet(r *http.Request, key string, value any) {
	state, _ := r.Context().Value(sessionStateKey).(*sessionState)
	if state != nil {
		state.data[key] = value
		state.dirty = true
	}
}

func (s *serverForTest) login(r *http.Request) {
	s.sessSet(r, sessionAuthKey, true)
}
