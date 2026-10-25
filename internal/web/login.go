package web

import (
	"net"
	"net/http"

	"github.com/lcyan/office365-manager/internal/auth"
)

type loginView struct {
	View
}

func (s *Server) handleLoginGET(w http.ResponseWriter, r *http.Request) {
	if s.isLoggedIn(r) {
		http.Redirect(w, r, "/dashboard", http.StatusSeeOther)
		return
	}
	s.renderPage(w, r, http.StatusOK, "login", loginView{View: s.viewData(r, "登录")})
}

func (s *Server) handleLoginPOST(w http.ResponseWriter, r *http.Request) {
	password := r.PostFormValue("password")
	ip, _, _ := net.SplitHostPort(r.RemoteAddr)
	if !s.limiter.Allow(ip) {
		s.log.Warn("登录尝试过于频繁", "ip", ip)
		s.redirect(w, r, "尝试过于频繁，请稍后再试。", "/login")
		return
	}

	hash := s.store.PasswordHash()
	if !auth.VerifyPassword(hash, password) {
		s.redirect(w, r, "密码错误，请重试。", "/login")
		return
	}

	// 旧版 SHA-256 哈希在首次成功登录后透明升级为 argon2id。
	if auth.NeedsRehash(hash) {
		if upgraded, err := auth.HashPassword(password); err == nil {
			if err := s.store.SetPasswordHash(upgraded); err != nil {
				s.log.Error("升级密码哈希失败", "err", err)
			}
		}
	}

	s.login(r)
	s.log.Info("登录成功", "ip", ip)
	s.redirect(w, r, "", "/dashboard")
}

func (s *Server) handleLogoutPOST(w http.ResponseWriter, r *http.Request) {
	s.logout(r)
	s.redirect(w, r, "", "/login")
}

type setPasswordView struct {
	View
}

func (s *Server) handleSetPasswordGET(w http.ResponseWriter, r *http.Request) {
	s.renderPage(w, r, http.StatusOK, "set_password", setPasswordView{View: s.viewData(r, "修改密码")})
}

func (s *Server) handleSetPasswordPOST(w http.ResponseWriter, r *http.Request) {
	current := r.PostFormValue("current_password")
	newPassword := r.PostFormValue("new_password")
	confirm := r.PostFormValue("confirm_password")

	if !auth.VerifyPassword(s.store.PasswordHash(), current) {
		s.redirect(w, r, "当前密码不正确。", "/set_password")
		return
	}
	if err := auth.ValidateNewPassword(newPassword); err != nil {
		s.redirect(w, r, err.Error(), "/set_password")
		return
	}
	if newPassword != confirm {
		s.redirect(w, r, "两次输入的新密码不一致。", "/set_password")
		return
	}

	hash, err := auth.HashPassword(newPassword)
	if err != nil {
		s.log.Error("哈希新密码失败", "err", err)
		s.redirect(w, r, "密码更新失败，请重试。", "/set_password")
		return
	}
	if err := s.store.SetPasswordHash(hash); err != nil {
		s.log.Error("保存新密码失败", "err", err)
		s.redirect(w, r, "密码更新失败，请重试。", "/set_password")
		return
	}

	// 修改密码后强制重新登录（保留提示消息）。
	s.forceRelogin(r)
	s.redirect(w, r, "密码已更新，请使用新密码重新登录。", "/login")
}
