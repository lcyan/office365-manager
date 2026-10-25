package web

import (
	"net/http"

	"github.com/lcyan/office365-manager/internal/graph"
)

type licenceBadge struct {
	ProductName string
	SkuPart     string
}

type userDetailView struct {
	View
	OrgName     string
	User        graph.User
	Roles       []string
	CurrentRole string // normal / Global Administrator / Privileged Role Administrator
	Licences    []licenceBadge
	Warnings    []string // 本次请求内的降级提示（角色/订阅读取失败等）
}

func (s *Server) handleUserDetail(w http.ResponseWriter, r *http.Request) {
	org, ok := s.requireOrg(w, r)
	if !ok {
		return
	}
	userID := r.PathValue("id")
	user, err := s.graph.GetUser(r.Context(), org, userID, true)
	if err != nil {
		s.redirect(w, r, "无法获取用户信息："+errMessage(err), "/users?view=users")
		return
	}

	view := userDetailView{View: s.viewData(r, "用户详情"), OrgName: org.Name, User: user}

	// 角色读取失败不阻断详情页，但要在本页明确提示（降级展示）。
	if assignments, err := s.graph.RoleAssignmentsForPrincipalNamed(r.Context(), org, userID); err == nil {
		for _, assignment := range assignments {
			name := assignment.RoleDefinition.DisplayName
			if name == "" {
				name = assignment.RoleDefinition.ID
			}
			view.Roles = append(view.Roles, name)
			switch name {
			case roleGlobalAdmin:
				view.CurrentRole = roleGlobalAdmin
			case rolePrivilegedAdmin:
				if view.CurrentRole == "" {
					view.CurrentRole = rolePrivilegedAdmin
				}
			}
		}
	} else {
		view.Warnings = append(view.Warnings, "角色信息获取失败，下方角色可能不准确："+errMessage(err))
	}
	if view.CurrentRole == "" {
		view.CurrentRole = "normal"
	}

	if details, err := s.graph.UserLicenseDetails(r.Context(), org, userID); err == nil {
		for _, detail := range details {
			view.Licences = append(view.Licences, licenceBadge{
				ProductName: s.catalog.Product(detail.SkuPartNumber),
				SkuPart:     detail.SkuPartNumber,
			})
		}
	} else {
		view.Warnings = append(view.Warnings, "订阅信息获取失败："+errMessage(err))
	}

	s.renderPage(w, r, http.StatusOK, "user_detail", view)
}
