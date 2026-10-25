package web

import (
	"net/http"

	"github.com/lcyan/office365-manager/internal/auth"
)

type dashboardView struct {
	View
	Total             int
	Available         int
	Unavailable       int
	Untested          int
	IsDefaultPassword bool
}

func (s *Server) handleDashboard(w http.ResponseWriter, r *http.Request) {
	orgs := s.store.Orgs()
	available, unavailable := s.apiStatus.counts(orgs)
	s.renderPage(w, r, http.StatusOK, "dashboard", dashboardView{
		View:              s.viewData(r, "仪表盘"),
		Total:             len(orgs),
		Available:         available,
		Unavailable:       unavailable,
		Untested:          len(orgs) - available - unavailable,
		IsDefaultPassword: auth.VerifyPassword(s.store.PasswordHash(), auth.DefaultPassword),
	})
}
