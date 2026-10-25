package web

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/lcyan/office365-manager/internal/auth"
	"github.com/lcyan/office365-manager/internal/config"
)

// newTestServer 构造带独立配置与会话的测试服务器。
// limiterCapacity <=0 时使用大容量（限速不干扰普通用例）。
func newTestServer(t *testing.T, password string, legacyHash bool, limiterCapacity int) (*httptest.Server, *config.Store) {
	t.Helper()
	store, err := config.Load(filepath.Join(t.TempDir(), "config.json"), "unused")
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	var hash string
	if legacyHash {
		digest := sha256.Sum256([]byte(password))
		hash = hex.EncodeToString(digest[:])
	} else {
		hash, err = auth.HashPassword(password)
		if err != nil {
			t.Fatalf("HashPassword: %v", err)
		}
	}
	if err := store.SetPasswordHash(hash); err != nil {
		t.Fatalf("SetPasswordHash: %v", err)
	}

	sessions := NewSessionManager([]byte("test-secret-0123456789abcdef"), false)
	if limiterCapacity <= 0 {
		limiterCapacity = 1000
	}
	server, err := New(Deps{
		Store:    store,
		Sessions: sessions,
		Limiter:  auth.NewLimiter(limiterCapacity, time.Hour), // 慢恢复：限速用例不受时间影响
		Log:      slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ts := httptest.NewServer(server.Handler())
	t.Cleanup(ts.Close)
	return ts, store
}

var csrfRe = regexp.MustCompile(`name="csrf_token" value="([0-9a-f]+)"`)

// newBrowser 返回不跟随重定向、带 cookie 的客户端。
func newBrowser() *http.Client {
	return &http.Client{Jar: newMemoryJar(), CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
}

// getCSRF 抓取页面里的 CSRF 令牌。
func getCSRF(t *testing.T, client *http.Client, url string) string {
	t.Helper()
	resp, err := client.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	m := csrfRe.FindSubmatch(body)
	if m == nil {
		t.Fatalf("page %s missing csrf token", url)
	}
	return string(m[1])
}

// loginAs 登录成功后返回客户端。
func loginAs(t *testing.T, ts *httptest.Server, password string) *http.Client {
	t.Helper()
	client := newBrowser()
	token := getCSRF(t, client, ts.URL+"/login")
	resp, err := client.PostForm(ts.URL+"/login", url.Values{
		"password":   {password},
		"csrf_token": {token},
	})
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/dashboard" {
		t.Fatalf("login = %d %s, want 303 /dashboard", resp.StatusCode, resp.Header.Get("Location"))
	}
	return client
}

func TestUnauthenticatedPathsRedirectToLogin(t *testing.T) {
	ts, _ := newTestServer(t, "secret-pass", false, 0)
	// 已知与未知路径统一重定向，不泄露路径存在性。
	for _, path := range []string{"/dashboard", "/users", "/no-such-page"} {
		resp, err := newBrowser().Get(ts.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/login" {
			t.Errorf("GET %s unauthenticated = %d %s, want 303 /login", path, resp.StatusCode, resp.Header.Get("Location"))
		}
	}
}

func TestLoginFlowAndAuthGate(t *testing.T) {
	ts, _ := newTestServer(t, "secret-pass", false, 0)

	// 错误密码：重定向登录，闪存消息出现在下一页。
	client := newBrowser()
	token := getCSRF(t, client, ts.URL+"/login")
	resp, err := client.PostForm(ts.URL+"/login", url.Values{
		"password":   {"wrong-pass"},
		"csrf_token": {token},
	})
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/login" {
		t.Fatalf("wrong password = %d %s, want 303 /login", resp.StatusCode, resp.Header.Get("Location"))
	}
	page, _ := getBody(t, client, ts.URL+"/login")
	if !strings.Contains(page, "密码错误") {
		t.Error("wrong-password flash missing on login page")
	}

	// 正确密码登录成功；未知路径返回 404（已通过认证）。
	authed := loginAs(t, ts, "secret-pass")
	resp2, err := authed.Get(ts.URL + "/no-such-page")
	if err != nil {
		t.Fatal(err)
	}
	resp2.Body.Close()
	if resp2.StatusCode != http.StatusNotFound {
		t.Errorf("authenticated unknown path = %d, want 404", resp2.StatusCode)
	}
}

func TestCSRFRequiredOnLogin(t *testing.T) {
	ts, _ := newTestServer(t, "secret-pass", false, 0)
	resp, err := http.PostForm(ts.URL+"/login", url.Values{"password": {"secret-pass"}})
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("login without csrf = %d, want 403", resp.StatusCode)
	}
}

func TestLoginRateLimit(t *testing.T) {
	ts, _ := newTestServer(t, "secret-pass", false, 2)
	client := newBrowser()
	token := getCSRF(t, client, ts.URL+"/login")
	for i := 0; i < 2; i++ {
		resp, err := client.PostForm(ts.URL+"/login", url.Values{
			"password":   {"wrong"},
			"csrf_token": {token},
		})
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
	}
	// 第 3 次被限速，提示出现在跟随重定向后的登录页。
	resp, err := client.PostForm(ts.URL+"/login", url.Values{
		"password":   {"wrong"},
		"csrf_token": {token},
	})
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	page, _ := getBody(t, client, ts.URL+"/login")
	if !strings.Contains(page, "尝试过于频繁") {
		t.Errorf("rate limit message missing:\n%s", page)
	}
}

func TestLegacyHashUpgradedOnLogin(t *testing.T) {
	ts, store := newTestServer(t, "legacy-pass", true, 0)
	if !auth.NeedsRehash(store.PasswordHash()) {
		t.Fatal("setup: expected legacy hash")
	}
	loginAs(t, ts, "legacy-pass")

	hash := store.PasswordHash()
	if auth.NeedsRehash(hash) {
		t.Fatalf("hash not upgraded after login: %q", hash)
	}
	if !auth.VerifyPassword(hash, "legacy-pass") {
		t.Error("upgraded hash does not verify the same password")
	}
}

func TestSetPasswordFlow(t *testing.T) {
	ts, store := newTestServer(t, "old-password", false, 0)
	client := loginAs(t, ts, "old-password")

	post := func(form url.Values) *http.Response {
		form.Set("csrf_token", getCSRF(t, client, ts.URL+"/set_password"))
		resp, err := client.PostForm(ts.URL+"/set_password", form)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp
	}

	fails := []url.Values{
		{"current_password": {"nope"}, "new_password": {"new-password-1"}, "confirm_password": {"new-password-1"}},
		{"current_password": {"old-password"}, "new_password": {"short"}, "confirm_password": {"short"}},
		{"current_password": {"old-password"}, "new_password": {"        "}, "confirm_password": {"        "}},
		{"current_password": {"old-password"}, "new_password": {"new-password-1"}, "confirm_password": {"different-1"}},
	}
	for i, form := range fails {
		if resp := post(form); resp.Header.Get("Location") != "/set_password" {
			t.Errorf("case %d redirected to %s, want /set_password", i, resp.Header.Get("Location"))
		}
	}
	if auth.VerifyPassword(store.PasswordHash(), "new-password-1") {
		t.Error("password changed by failed attempts")
	}

	resp := post(url.Values{
		"current_password": {"old-password"}, "new_password": {"new-password-1"}, "confirm_password": {"new-password-1"},
	})
	if resp.Header.Get("Location") != "/login" {
		t.Errorf("success redirected to %s, want /login", resp.Header.Get("Location"))
	}
	if !auth.VerifyPassword(store.PasswordHash(), "new-password-1") {
		t.Error("password not updated after success")
	}
	// 会话已失效：受保护页面重定向登录，且登录页出现提示。
	resp2, err := client.Get(ts.URL + "/set_password")
	if err != nil {
		t.Fatal(err)
	}
	resp2.Body.Close()
	if resp2.StatusCode != http.StatusSeeOther {
		t.Errorf("after password change GET /set_password = %d, want 303", resp2.StatusCode)
	}
}

func TestLogoutRequiresPostAndCSRF(t *testing.T) {
	ts, _ := newTestServer(t, "secret-pass", false, 0)
	client := loginAs(t, ts, "secret-pass")
	token := getCSRF(t, client, ts.URL+"/set_password")

	resp, err := client.PostForm(ts.URL+"/logout", url.Values{})
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("logout without csrf = %d, want 403", resp.StatusCode)
	}

	resp, err = client.PostForm(ts.URL+"/logout", url.Values{"csrf_token": {token}})
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Errorf("logout = %d, want 303", resp.StatusCode)
	}
	resp2, err := client.Get(ts.URL + "/no-such")
	if err != nil {
		t.Fatal(err)
	}
	resp2.Body.Close()
	if resp2.StatusCode != http.StatusSeeOther {
		t.Errorf("after logout = %d, want 303 login redirect", resp2.StatusCode)
	}
}

func TestSessionCookieTampering(t *testing.T) {
	ts, _ := newTestServer(t, "secret-pass", false, 0)
	client := loginAs(t, ts, "secret-pass")

	// 取出会话 cookie，篡改已登录标记后重放：签名校验必须拒绝。
	jar := client.Jar.(*memoryJar)
	host := strings.TrimPrefix(ts.URL, "http://")
	var raw string
	for _, c := range jar.cookies[host] {
		if c.Name == "o365_session" {
			raw = c.Value
		}
	}
	if raw == "" {
		t.Fatal("session cookie not set after login")
	}
	payload, sig, _ := strings.Cut(raw, ".")
	pb, _ := base64.RawURLEncoding.DecodeString(payload)
	var data map[string]any
	if err := json.Unmarshal(pb, &data); err != nil {
		t.Fatalf("session payload not JSON: %v", err)
	}
	data["logged_in"] = false
	forged, _ := json.Marshal(data)
	tampered := base64.RawURLEncoding.EncodeToString(forged) + "." + sig
	jar.cookies[host] = []*http.Cookie{{Name: "o365_session", Value: tampered}}

	resp, err := client.Get(ts.URL + "/no-such-page")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Errorf("tampered cookie accepted: GET = %d, want 303 /login", resp.StatusCode)
	}
}

func TestSecureHeaders(t *testing.T) {
	ts, _ := newTestServer(t, "secret-pass", false, 0)
	resp, err := http.Get(ts.URL + "/login")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	for header, want := range map[string]string{
		"X-Content-Type-Options":  "nosniff",
		"X-Frame-Options":         "DENY",
		"Content-Security-Policy": "default-src 'self'",
	} {
		if got := resp.Header.Get(header); !strings.Contains(got, want) {
			t.Errorf("%s = %q, want containing %q", header, got, want)
		}
	}
}

func getBody(t *testing.T, client *http.Client, url string) (string, *http.Response) {
	t.Helper()
	resp, err := client.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	return string(body), resp
}

// memoryJar 是最小 cookie jar，测试会话足够。
type memoryJar struct {
	cookies map[string][]*http.Cookie
}

func newMemoryJar() *memoryJar { return &memoryJar{cookies: map[string][]*http.Cookie{}} }

func (j *memoryJar) SetCookies(u *url.URL, cookies []*http.Cookie) {
	j.cookies[u.Host] = cookies
}

func (j *memoryJar) Cookies(u *url.URL) []*http.Cookie {
	return j.cookies[u.Host]
}
