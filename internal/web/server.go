// Package web 实现 HTTP 层：会话、中间件、模板渲染与全部页面 handler。
package web

import (
	"log/slog"
	"net/http"

	"github.com/lcyan/office365-manager/internal/auth"
	"github.com/lcyan/office365-manager/internal/background"
	"github.com/lcyan/office365-manager/internal/config"
	"github.com/lcyan/office365-manager/internal/graph"
	"github.com/lcyan/office365-manager/internal/licenses"
)

// Server 聚合全部依赖并装配路由。
type Server struct {
	store      *config.Store
	sessions   *SessionManager
	limiter    *auth.Limiter
	log        *slog.Logger
	render     *Renderer
	graph      *graph.Client
	apiStatus  *orgStatus
	catalog    *licenses.Catalog
	summaries  *summaryCache
	background *background.Runner
}

// Deps 是构造 Server 的依赖集合。
type Deps struct {
	Store      *config.Store
	Sessions   *SessionManager
	Limiter    *auth.Limiter
	Log        *slog.Logger
	Graph      *graph.Client
	Catalog    *licenses.Catalog
	Background *background.Runner
}

// New 构造 Server（渲染器从内嵌资源加载模板）。
func New(d Deps) (*Server, error) {
	renderer, err := NewRenderer()
	if err != nil {
		return nil, err
	}
	log := d.Log
	if log == nil {
		log = slog.Default()
	}
	catalog := d.Catalog
	if catalog == nil {
		catalog = licenses.NewCatalog(nil)
	}
	runner := d.Background
	if runner == nil {
		runner = background.New(log)
	}
	return &Server{
		store:      d.Store,
		sessions:   d.Sessions,
		limiter:    d.Limiter,
		log:        log,
		render:     renderer,
		graph:      d.Graph,
		apiStatus:  newOrgStatus(),
		catalog:    catalog,
		summaries:  newSummaryCache(),
		background: runner,
	}, nil
}

// Handler 返回装配好中间件的完整 HTTP 处理器。
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})

	static := http.FileServer(http.FS(s.staticFiles()))
	mux.Handle("GET /static/", http.StripPrefix("/static/", static))

	// 登录与静态资源无需会话；其余全部要求登录（包括未知路径，
	// 未登录时统一重定向，避免暴露路径存在性）。
	mux.HandleFunc("GET /login", s.handleLoginGET)
	mux.HandleFunc("POST /login", s.csrf(s.handleLoginPOST))
	mux.HandleFunc("POST /logout", s.requireAuth(s.csrf(s.handleLogoutPOST)))
	mux.HandleFunc("GET /set_password", s.requireAuth(s.handleSetPasswordGET))
	mux.HandleFunc("POST /set_password", s.requireAuth(s.csrf(s.handleSetPasswordPOST)))

	mux.HandleFunc("GET /dashboard", s.requireAuth(s.handleDashboard))
	mux.HandleFunc("GET /organizations", s.requireAuth(s.handleOrgsList))
	mux.HandleFunc("POST /organizations", s.requireAuth(s.csrf(s.handleOrgAdd)))
	mux.HandleFunc("GET /organizations/edit/{id}", s.requireAuth(s.handleOrgEditGET))
	mux.HandleFunc("POST /organizations/edit/{id}", s.requireAuth(s.csrf(s.handleOrgEditPOST)))
	mux.HandleFunc("GET /organizations/delete/{id}", s.requireAuth(s.handleOrgDeleteGET))
	mux.HandleFunc("POST /organizations/delete/{id}", s.requireAuth(s.csrf(s.handleOrgDeletePOST)))
	mux.HandleFunc("GET /organizations/test/{id}", s.requireAuth(s.handleOrgTest))
	mux.HandleFunc("GET /organizations/test_all", s.requireAuth(s.handleOrgTestAll))
	mux.HandleFunc("GET /organizations/template", s.requireAuth(s.handleOrgTemplate))
	mux.HandleFunc("POST /organizations/import", s.requireAuth(s.csrf(s.handleOrgImport)))
	mux.HandleFunc("GET /select_org/{id}", s.requireAuth(s.handleSelectOrg))

	mux.HandleFunc("GET /users", s.requireAuth(s.handleUsers))
	mux.HandleFunc("GET /user/{id}", s.requireAuth(s.handleUserDetail))
	mux.HandleFunc("POST /user/{id}/update", s.requireAuth(s.csrf(s.handleUserUpdate)))
	mux.HandleFunc("GET /user/{id}/license", s.requireAuth(s.handleLicenseGET))
	mux.HandleFunc("POST /user/{id}/license", s.requireAuth(s.csrf(s.handleLicensePOST)))
	mux.HandleFunc("GET /user/{id}/delete", s.requireAuth(s.handleDeleteUserGET))
	mux.HandleFunc("POST /user/{id}/delete", s.requireAuth(s.csrf(s.handleDeleteUserPOST)))
	mux.HandleFunc("GET /user/{id}/created", s.requireAuth(s.handleUserCreated))
	mux.HandleFunc("GET /users/add", s.requireAuth(s.handleAddUserGET))
	mux.HandleFunc("POST /users/add", s.requireAuth(s.csrf(s.handleAddUserPOST)))

	mux.HandleFunc("/", s.requireAuth(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))

	return s.sessions.Middleware(secureHeaders(mux))
}

// requireAuth 要求已登录，否则重定向到登录页。
func (s *Server) requireAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.isLoggedIn(r) {
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}
		next(w, r)
	}
}

// csrf 校验请求中的 CSRF 令牌（表单字段 csrf_token 或 X-CSRF-Token 头）。
func (s *Server) csrf(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token := r.PostFormValue("csrf_token")
		if token == "" {
			token = r.Header.Get("X-CSRF-Token")
		}
		if token == "" || !secureEqual(token, s.csrfToken(r)) {
			http.Error(w, "请求校验失败（CSRF），请返回重试。", http.StatusForbidden)
			return
		}
		next(w, r)
	}
}

// secureHeaders 设置基础安全响应头。站内无脚本与内联样式，CSP 收紧。
func secureHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "strict-origin-when-cross-origin")
		h.Set("Content-Security-Policy",
			"default-src 'self'; style-src 'self'; img-src 'self' data:; form-action 'self'; frame-ancestors 'none'")
		next.ServeHTTP(w, r)
	})
}
