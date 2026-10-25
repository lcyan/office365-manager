package web

import (
	"bytes"
	"fmt"
	"html/template"
	"io/fs"
	"log/slog"
	"net/http"
	"strings"

	"github.com/lcyan/office365-manager/assets"
	"github.com/lcyan/office365-manager/internal/config"
)

// Renderer 解析并执行页面模板。每个页面模板定义 "content" 块，
// 与 base.html 组合成一个模板集。
type Renderer struct {
	pages map[string]*template.Template
}

// NewRenderer 从内嵌资源加载全部模板。
func NewRenderer() (*Renderer, error) {
	entries, err := fs.ReadDir(assets.Templates, "templates")
	if err != nil {
		return nil, fmt.Errorf("读取模板目录失败：%w", err)
	}
	r := &Renderer{pages: make(map[string]*template.Template)}
	for _, entry := range entries {
		name := strings.TrimSuffix(entry.Name(), ".html")
		if name == "base" || entry.IsDir() {
			continue
		}
		t, err := template.New("base").ParseFS(assets.Templates,
			"templates/base.html", "templates/"+entry.Name())
		if err != nil {
			return nil, fmt.Errorf("解析模板 %s 失败：%w", entry.Name(), err)
		}
		r.pages[name] = t
	}
	return r, nil
}

// Render 输出页面；模板执行错误返回 500 并记录日志（不向客户端泄露细节）。
func (r *Renderer) Render(w http.ResponseWriter, status int, page string, data any) {
	t, ok := r.pages[page]
	if !ok {
		http.Error(w, "页面不存在。", http.StatusInternalServerError)
		return
	}
	var buf bytes.Buffer
	if err := t.ExecuteTemplate(&buf, "base", data); err != nil {
		slog.Error("渲染模板失败", "page", page, "err", err)
		http.Error(w, "页面渲染失败。", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_, _ = buf.WriteTo(w)
}

// staticFiles 返回静态资源子文件系统。
func (s *Server) staticFiles() fs.FS {
	sub, err := fs.Sub(assets.Static, "static")
	if err != nil {
		panic(err) // embed 路径固定，编译期保证存在
	}
	return sub
}

// OrgLink 供导航栏组织切换器使用。
type OrgLink struct {
	ID   string
	Name string
}

// View 是所有页面视图模型的公共部分。每个页面的数据结构嵌入它，
// 模板经显式字段访问（不存在全局隐式注入）。
type View struct {
	Title         string
	LoggedIn      bool
	CSRFToken     string
	Flashes       []string
	ActiveOrgID   string
	ActiveOrgName string
	OrgSwitcher   []OrgLink
	AssetVersion  string
}

// viewData 组装公共视图数据：闪现消息、CSRF 令牌与当前组织。
// 必须在 handler 追加新闪存消息之前调用（先取后加）。
func (s *Server) viewData(r *http.Request, title string) View {
	v := View{
		Title:        title,
		LoggedIn:     s.isLoggedIn(r),
		CSRFToken:    s.csrfToken(r),
		Flashes:      s.takeFlashes(r),
		AssetVersion: s.assetVersion,
	}
	if orgID := s.activeOrgID(r); orgID != "" {
		if org, ok := s.store.Org(orgID); ok {
			v.ActiveOrgID = org.ID
			v.ActiveOrgName = org.Name
			for _, o := range s.store.Orgs() {
				if o.ID != org.ID {
					v.OrgSwitcher = append(v.OrgSwitcher, OrgLink{ID: o.ID, Name: o.Name})
				}
			}
		}
	}
	return v
}

// redirect 发出闪存消息并重定向（303 See Other，PRG 模式）。
func (s *Server) redirect(w http.ResponseWriter, r *http.Request, msg, target string) {
	if msg != "" {
		s.addFlash(r, msg)
	}
	s.sessions.Save(w, r)
	http.Redirect(w, r, target, http.StatusSeeOther)
}

// renderPage 保存会话（闪存/CSRF 等修改）后渲染页面。
func (s *Server) renderPage(w http.ResponseWriter, r *http.Request, status int, page string, data any) {
	s.sessions.Save(w, r)
	s.render.Render(w, status, page, data)
}

// requireOrg 校验会话中已选择有效组织；失败时闪存并重定向。
// 返回 false 表示已写出响应。
func (s *Server) requireOrg(w http.ResponseWriter, r *http.Request) (config.Org, bool) {
	orgID := s.activeOrgID(r)
	if orgID == "" {
		s.redirect(w, r, "请先选择组织。", "/organizations")
		return config.Org{}, false
	}
	org, ok := s.store.Org(orgID)
	if !ok {
		s.clearActiveOrg(r)
		s.redirect(w, r, "组织配置错误，请重新选择。", "/organizations")
		return config.Org{}, false
	}
	return org, true
}

// validNext 只接受站内相对跳转目标（以单个 / 开头），防御开放重定向。
func validNext(next string) string {
	if strings.HasPrefix(next, "/") && !strings.HasPrefix(next, "//") {
		return next
	}
	return ""
}
