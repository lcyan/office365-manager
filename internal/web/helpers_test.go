package web

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/lcyan/office365-manager/internal/auth"
	"github.com/lcyan/office365-manager/internal/config"
	"github.com/lcyan/office365-manager/internal/graph"
)

// graphHandler 处理伪 Graph 请求；返回 false 表示未匹配（回 404）。
type graphHandler func(w http.ResponseWriter, r *http.Request) bool

type serverOpts struct {
	password        string
	legacyHash      bool
	limiterCapacity int
	graph           graphHandler
}

// newTestServerOpts 构造带独立配置、会话与（可选）伪 Graph 后端的测试服务器。
func newTestServerOpts(t *testing.T, opts serverOpts) (*httptest.Server, *config.Store, *graph.Client) {
	t.Helper()
	store, err := config.Load(filepath.Join(t.TempDir(), "config.json"), "unused")
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	var hash string
	if opts.legacyHash {
		digest := sha256.Sum256([]byte(opts.password))
		hash = hex.EncodeToString(digest[:])
	} else {
		hash, err = auth.HashPassword(opts.password)
		if err != nil {
			t.Fatalf("HashPassword: %v", err)
		}
	}
	if err := store.SetPasswordHash(hash); err != nil {
		t.Fatalf("SetPasswordHash: %v", err)
	}

	// 伪 Graph：令牌端点 + 可选业务 handler（默认一切 404）。
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/test-tenant/oauth2/v2.0/token" {
			_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "tok", "expires_in": 3600})
			return
		}
		if opts.graph == nil || !opts.graph(w, r) {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":{"code":"NotFound","message":"no fake route"}}`))
		}
	}))
	t.Cleanup(backend.Close)

	graphClient := graph.New(slog.New(slog.NewTextHandler(io.Discard, nil)))
	graphClient.SetHTTPClient(backend.Client())
	graphClient.SetBaseURL(backend.URL, backend.URL)

	sessions := NewSessionManager([]byte("test-secret-0123456789abcdef"), false)
	if opts.limiterCapacity <= 0 {
		opts.limiterCapacity = 1000
	}
	server, err := New(Deps{
		Store:    store,
		Sessions: sessions,
		Limiter:  auth.NewLimiter(opts.limiterCapacity, time.Hour),
		Log:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		Graph:    graphClient,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ts := httptest.NewServer(server.Handler())
	t.Cleanup(ts.Close)
	return ts, store, graphClient
}

func newTestServer(t *testing.T, password string, legacyHash bool, limiterCapacity int) (*httptest.Server, *config.Store) {
	ts, store, _ := newTestServerOpts(t, serverOpts{
		password: password, legacyHash: legacyHash, limiterCapacity: limiterCapacity,
	})
	return ts, store
}

func writeGraphJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
