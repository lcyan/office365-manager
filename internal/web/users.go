package web

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/lcyan/office365-manager/internal/config"
	"github.com/lcyan/office365-manager/internal/graph"
	"github.com/lcyan/office365-manager/internal/licenses"
)

const (
	roleGlobalAdmin     = "Global Administrator"
	rolePrivilegedAdmin = "Privileged Role Administrator"
)

// --- 视图模型 ---

type orgPick struct {
	ID     string
	Name   string
	Status string // untested / ok / fail
}

type selectOrgView struct {
	View
	Orgs []orgPick
}

type errorPageView struct {
	View
	OrgName   string
	ErrDetail string
}

type summaryView struct {
	View
	OrgName string
	Summary orgSummary
}

type userRow struct {
	ID                string
	DisplayName       string
	Surname           string
	GivenName         string
	UPN               string
	Kind              string // user / group / servicePrincipal / unknown
	IsGlobalAdmin     bool
	IsPrivilegedAdmin bool
}

type pageLink struct {
	Number int
	URL    string
	Active bool
}

type usersListView struct {
	View
	OrgName        string
	Users          []userRow
	RolesAvailable bool
	Search         string
	Role           string
	Page           int
	PageSize       int
	TotalPages     int
	PrevURL        string
	NextURL        string
	PageLinks      []pageLink
}

// --- 路由入口 ---

func (s *Server) handleUsers(w http.ResponseWriter, r *http.Request) {
	if r.URL.Query().Get("reset") == "1" {
		s.clearActiveOrg(r)
		s.redirect(w, r, "", "/users")
		return
	}
	orgID := s.activeOrgID(r)
	if orgID == "" {
		view := selectOrgView{View: s.viewData(r, "选择组织")}
		for _, org := range s.store.Orgs() {
			pick := orgPick{ID: org.ID, Name: org.Name, Status: "untested"}
			if tested, ok := s.apiStatus.get(org.ID); tested {
				if ok {
					pick.Status = "ok"
				} else {
					pick.Status = "fail"
				}
			}
			view.Orgs = append(view.Orgs, pick)
		}
		s.renderPage(w, r, http.StatusOK, "users", view)
		return
	}

	org, ok := s.store.Org(orgID)
	if !ok {
		s.clearActiveOrg(r)
		s.redirect(w, r, "组织配置错误，请重新选择。", "/users")
		return
	}

	if r.URL.Query().Get("view") == "users" {
		s.usersListViewHandler(w, r, org)
		return
	}
	s.usersSummaryViewHandler(w, r, org)
}

// renderOrgError 渲染 API 不可用页面（带具体错误信息，不再静默）。
func (s *Server) renderOrgError(w http.ResponseWriter, r *http.Request, org config.Org, err error) {
	s.apiStatus.set(org.ID, false)
	s.renderPage(w, r, http.StatusOK, "error_page", errorPageView{
		View:      s.viewData(r, "API 不可用"),
		OrgName:   org.Name,
		ErrDetail: errMessage(err),
	})
}

// --- 概况视图 ---

func (s *Server) usersSummaryViewHandler(w http.ResponseWriter, r *http.Request, org config.Org) {
	if r.URL.Query().Get("refresh") == "1" {
		s.summaries.invalidate(org.ID)
	}
	if summary, ok := s.summaries.get(org.ID); ok {
		s.renderPage(w, r, http.StatusOK, "summary", summaryView{
			View: s.viewData(r, "组织概况"), OrgName: org.Name, Summary: summary,
		})
		return
	}

	// 先验证连通性；失败直接呈现错误页（含原因）。
	if _, err := s.graph.SubscribedSkus(r.Context(), org); err != nil {
		s.renderOrgError(w, r, org, err)
		return
	}
	s.apiStatus.set(org.ID, true)

	version := s.summaries.version(org.ID)
	summary, complete := s.buildSummary(r, org)
	summary.UpdatedAt = timeNowUTC()
	summary.Partial = !complete
	if complete {
		s.summaries.put(org.ID, version, summary)
	}
	s.renderPage(w, r, http.StatusOK, "summary", summaryView{
		View: s.viewData(r, "组织概况"), OrgName: org.Name, Summary: summary,
	})
}

// buildSummary 并行拉取概况数据；返回是否完整（不完整仍渲染但不缓存）。
func (s *Server) buildSummary(r *http.Request, org config.Org) (orgSummary, bool) {
	ctx := r.Context()
	var wg sync.WaitGroup
	complete := true
	var mu sync.Mutex // 保护 complete

	fail := func() {
		mu.Lock()
		complete = false
		mu.Unlock()
	}

	var (
		skus    []graph.SubscribedSku
		subsErr error
	)
	wg.Add(1)
	go func() {
		defer wg.Done()
		skus, subsErr = s.graph.SubscribedSkus(ctx, org)
		if subsErr != nil {
			fail()
		}
	}()

	var (
		subs     []graph.Subscription
		renewErr error
	)
	wg.Add(1)
	go func() {
		defer wg.Done()
		// 订阅续期信息失败只影响明细，不阻断概况。
		subs, renewErr = s.graph.DirectorySubscriptions(ctx, org)
		if renewErr != nil {
			fail()
		}
	}()

	var (
		gaID, praID string
		roleIDErr   error
	)
	wg.Add(1)
	go func() {
		defer wg.Done()
		gaID, roleIDErr = s.graph.RoleDefinitionID(ctx, org, roleGlobalAdmin)
		if roleIDErr != nil {
			fail()
			return
		}
	}()
	wg.Add(1)
	go func() {
		defer wg.Done()
		praID, roleIDErr = s.graph.RoleDefinitionID(ctx, org, rolePrivilegedAdmin)
		if roleIDErr != nil {
			fail()
		}
	}()

	var (
		countResult graph.ListUsersResult
		countErr    error
	)
	wg.Add(1)
	go func() {
		defer wg.Done()
		// Top=1 仅取计数。
		countResult, countErr = s.graph.ListUsers(ctx, org, graph.ListUsersParams{Top: 1})
		if countErr != nil {
			fail()
		}
	}()
	wg.Wait()

	// 角色分配计数依赖角色 ID（上一阶段完成）。
	var gaCount, praCount *int
	if gaID != "" || praID != "" {
		var inner sync.WaitGroup
		if gaID != "" {
			inner.Add(1)
			go func() {
				defer inner.Done()
				assignments, err := s.graph.RoleAssignmentsByDefinition(ctx, org, gaID)
				if err != nil {
					fail()
					return
				}
				n := len(assignments)
				gaCount = &n
			}()
		}
		if praID != "" {
			inner.Add(1)
			go func() {
				defer inner.Done()
				assignments, err := s.graph.RoleAssignmentsByDefinition(ctx, org, praID)
				if err != nil {
					fail()
					return
				}
				n := len(assignments)
				praCount = &n
			}()
		}
		inner.Wait()
	}
	if gaCount == nil || praCount == nil {
		// 角色查找失败时两个计数都置为未知，避免误导。
		gaCount, praCount = nil, nil
		complete = false
	}

	renewalBySku := map[string]string{}
	for _, sub := range subs {
		if sub.SkuID != "" && sub.NextLifecycleDateTime != "" {
			renewalBySku[sub.SkuID] = sub.NextLifecycleDateTime
		}
	}

	var totalUsers *int
	if countErr == nil {
		n := countResult.Total
		totalUsers = &n
	}

	summary := orgSummary{
		GlobalAdminCount:     gaCount,
		PrivilegedAdminCount: praCount,
		TotalUsers:           totalUsers,
	}
	for _, sku := range skus {
		total := sku.PrepaidUnits.Enabled + sku.PrepaidUnits.Warning + sku.PrepaidUnits.Suspended
		used := sku.ConsumedUnits
		available := total - used
		if available < 0 || total == 0 {
			available = 0
		}
		ratio := "—"
		if total > 0 {
			ratio = fmt.Sprintf("%d/%d", used, total)
		}
		label, badge := licenseStatus(sku.CapabilityStatus)
		renewal := "未知"
		if v, ok := renewalBySku[sku.SkuID]; ok {
			renewal = v
		}
		summary.Licences = append(summary.Licences, licenceRow{
			ProductName: s.catalog.Product(sku.SkuPartNumber),
			SkuPart:     sku.SkuPartNumber,
			Ratio:       ratio,
			Available:   available,
			StatusLabel: label,
			StatusBadge: badge,
			Renewal:     renewal,
		})
	}
	return summary, complete
}

// --- 列表视图 ---

func (s *Server) usersListViewHandler(w http.ResponseWriter, r *http.Request, org config.Org) {
	q := r.URL.Query()
	page := atoiDefault(q.Get("page"), 1)
	pageSize := clampInt(atoiDefault(q.Get("page_size"), 50), 10, 200)
	search := strings.TrimSpace(q.Get("search"))
	roleFilter := q.Get("role")

	if page < 1 {
		page = 1
	}

	var (
		rows           []userRow
		totalPages     int
		rolesAvailable = true
	)

	if roleFilter != "" {
		if roleFilter != roleGlobalAdmin && roleFilter != rolePrivilegedAdmin {
			s.redirect(w, r, "不支持的角色筛选。", "/users?view=users")
			return
		}
		built, tp, err := s.roleFilteredRows(r.Context(), org, roleFilter, search, page, pageSize)
		if err != nil {
			s.renderOrgError(w, r, org, err)
			return
		}
		rows, totalPages = built, tp
		if roleFilter == roleGlobalAdmin {
			for i := range rows {
				rows[i].IsGlobalAdmin = true
			}
		} else {
			for i := range rows {
				rows[i].IsPrivilegedAdmin = true
			}
		}
	} else {
		result, err := s.graph.ListUsers(r.Context(), org, graph.ListUsersParams{
			Search: search, Top: pageSize, Skip: (page - 1) * pageSize,
		})
		if err != nil {
			s.renderOrgError(w, r, org, err)
			return
		}
		totalPages = int(math.Max(1, math.Ceil(float64(result.Total)/float64(pageSize))))
		// 请求页超出总数（深链/数据变化）→ 回退到末页重取，保证页码与内容一致。
		if len(result.Users) == 0 && page > 1 && result.Total > 0 {
			page = totalPages
			result, err = s.graph.ListUsers(r.Context(), org, graph.ListUsersParams{
				Search: search, Top: pageSize, Skip: (page - 1) * pageSize,
			})
			if err != nil {
				s.renderOrgError(w, r, org, err)
				return
			}
		}
		if page > totalPages {
			page = totalPages
		}
		for _, u := range result.Users {
			rows = append(rows, userRow{
				ID: u.ID, DisplayName: u.DisplayName, Surname: u.Surname,
				GivenName: u.GivenName, UPN: u.UserPrincipalName, Kind: "user",
			})
		}
		rolesAvailable = s.annotateAdminBadges(r.Context(), org, rows)
	}

	view := usersListView{
		View:           s.viewData(r, "用户列表"),
		OrgName:        org.Name,
		Users:          rows,
		RolesAvailable: rolesAvailable,
		Search:         search,
		Role:           roleFilter,
		Page:           page,
		PageSize:       pageSize,
		TotalPages:     totalPages,
	}
	if page > 1 {
		view.PrevURL = usersListURL(page-1, pageSize, search, roleFilter)
	}
	if page < totalPages {
		view.NextURL = usersListURL(page+1, pageSize, search, roleFilter)
	}
	for _, n := range pageWindow(page, totalPages) {
		view.PageLinks = append(view.PageLinks, pageLink{
			Number: n, URL: usersListURL(n, pageSize, search, roleFilter), Active: n == page,
		})
	}
	s.renderPage(w, r, http.StatusOK, "users_list", view)
}

// roleFilteredRows 用角色分配（$expand=principal）构建筛选列表。
// 组/服务主体作为独立行展示而非导致整页失败；搜索在本地做前缀匹配；
// 分页在内存中进行。返回行与总页数。
func (s *Server) roleFilteredRows(ctx context.Context, org config.Org, roleFilter, search string, page, pageSize int) ([]userRow, int, error) {
	roleID, err := s.graph.RoleDefinitionID(ctx, org, roleFilter)
	if err != nil {
		return nil, 0, err
	}
	assignments, err := s.graph.RoleAssignmentsByDefinition(ctx, org, roleID)
	if err != nil {
		return nil, 0, err
	}

	var all []userRow
	query := strings.ToLower(search)
	for _, assignment := range assignments {
		row := userRow{ID: assignment.PrincipalID}
		switch principal := assignment.Principal; {
		case principal == nil:
			row.Kind = "unknown"
			row.DisplayName = assignment.PrincipalID
		default:
			row.Kind = principal.PrincipalKind()
			row.DisplayName = principal.DisplayName
			row.UPN = principal.UserPrincipalName
			row.Surname = principal.Surname
			row.GivenName = principal.GivenName
		}
		if query != "" {
			matched := strings.HasPrefix(strings.ToLower(row.DisplayName), query) ||
				strings.HasPrefix(strings.ToLower(row.UPN), query) ||
				strings.HasPrefix(strings.ToLower(row.GivenName), query) ||
				strings.HasPrefix(strings.ToLower(row.Surname), query)
			if !matched {
				continue
			}
		}
		all = append(all, row)
	}

	totalPages := int(math.Max(1, math.Ceil(float64(len(all))/float64(pageSize))))
	if page > totalPages {
		page = totalPages
	}
	start := (page - 1) * pageSize
	end := start + pageSize
	if start > len(all) {
		start = len(all)
	}
	if end > len(all) {
		end = len(all)
	}
	return all[start:end], totalPages, nil
}

// annotateAdminBadges 为当前页用户标注 GA/PRA 徽章。返回角色信息是否可用。
func (s *Server) annotateAdminBadges(ctx context.Context, org config.Org, rows []userRow) bool {
	var (
		wg        sync.WaitGroup
		gaSet     map[string]bool
		praSet    map[string]bool
		available = true
		mu        sync.Mutex
	)
	load := func(roleName string, target *map[string]bool) {
		defer wg.Done()
		roleID, err := s.graph.RoleDefinitionID(ctx, org, roleName)
		if err != nil {
			mu.Lock()
			available = false
			mu.Unlock()
			return
		}
		assignments, err := s.graph.RoleAssignmentsByDefinition(ctx, org, roleID)
		if err != nil {
			mu.Lock()
			available = false
			mu.Unlock()
			return
		}
		set := make(map[string]bool, len(assignments))
		for _, a := range assignments {
			set[a.PrincipalID] = true
		}
		*target = set
	}
	wg.Add(2)
	go load(roleGlobalAdmin, &gaSet)
	go load(rolePrivilegedAdmin, &praSet)
	wg.Wait()
	if !available {
		return false
	}
	for i := range rows {
		rows[i].IsGlobalAdmin = gaSet[rows[i].ID]
		rows[i].IsPrivilegedAdmin = praSet[rows[i].ID]
	}
	return true
}

// --- 工具 ---

func usersListURL(page, pageSize int, search, role string) string {
	q := url.Values{
		"view":      {"users"},
		"page":      {strconv.Itoa(page)},
		"page_size": {strconv.Itoa(pageSize)},
	}
	if search != "" {
		q.Set("search", search)
	}
	if role != "" {
		q.Set("role", role)
	}
	return "/users?" + q.Encode()
}

// pageWindow 返回当前页附近的页码（±2，至少 1 页）。
func pageWindow(page, totalPages int) []int {
	if totalPages < 1 {
		totalPages = 1
	}
	start := page - 2
	if start < 1 {
		start = 1
	}
	end := page + 2
	if end > totalPages {
		end = totalPages
	}
	var out []int
	for n := start; n <= end; n++ {
		out = append(out, n)
	}
	return out
}

func atoiDefault(v string, def int) int {
	n, err := strconv.Atoi(v)
	if err != nil {
		return def
	}
	return n
}

func clampInt(v, low, high int) int {
	if v < low {
		return low
	}
	if v > high {
		return high
	}
	return v
}

func timeNowUTC() string {
	return time.Now().UTC().Format("2006-01-02 15:04:05 UTC")
}

func licenseStatus(raw string) (string, string) {
	return licenses.StatusLabel(raw)
}
