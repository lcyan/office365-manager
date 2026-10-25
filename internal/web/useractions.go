package web

import (
	"context"
	"crypto/rand"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/lcyan/office365-manager/internal/config"
	"github.com/lcyan/office365-manager/internal/graph"
)

// --- 用户更新（密码 / 启停 / 角色） ---

func (s *Server) handleUserUpdate(w http.ResponseWriter, r *http.Request) {
	org, ok := s.requireOrg(w, r)
	if !ok {
		return
	}
	userID := r.PathValue("id")
	ctx := r.Context()

	newPassword := r.PostFormValue("new_password")
	selectedRole := r.PostFormValue("role")

	// 取当前状态：只为变更字段下发 PATCH，避免覆盖并发修改。
	user, err := s.graph.GetUser(ctx, org, userID, true)
	if err != nil {
		s.redirect(w, r, "无法获取用户信息："+errMessage(err), "/user/"+userID)
		return
	}

	var (
		enableFlag *bool
		password   *string
	)
	switch v := r.PostFormValue("account_enabled"); v {
	case "true":
		if !user.AccountEnabled {
			enabled := true
			enableFlag = &enabled
		}
	case "false":
		if user.AccountEnabled {
			disabled := false
			enableFlag = &disabled
		}
	default:
		s.redirect(w, r, "账户启用状态参数无效。", "/user/"+userID)
		return
	}
	if newPassword != "" {
		if strings.TrimSpace(newPassword) == "" {
			s.redirect(w, r, "密码不能为空白。", "/user/"+userID)
			return
		}
		password = &newPassword
	}

	if enableFlag != nil || password != nil {
		if err := s.graph.UpdateUser(ctx, org, userID, graph.UserPatch{
			Password: password, AccountEnabled: enableFlag,
		}); err != nil {
			// PATCH 失败即中止：角色变更不执行（回归：Python 版继续执行）。
			s.redirect(w, r, "密码或账户状态更新失败："+errMessage(err)+"（未执行角色变更）", "/user/"+userID)
			return
		}
		if password != nil {
			s.addFlash(r, "密码已更新。")
		}
		if enableFlag != nil {
			s.addFlash(r, "账户启用状态已更新。")
		}
		s.summaries.invalidate(org.ID)
	}

	switch selectedRole {
	case "", "normal", roleGlobalAdmin, rolePrivilegedAdmin:
	default:
		s.redirect(w, r, "不支持的角色。", "/user/"+userID)
		return
	}
	if selectedRole != "" {
		msg, err := s.applyRoleChange(ctx, org, userID, selectedRole)
		if err != nil {
			s.redirect(w, r, msg, "/user/"+userID)
			return
		}
		s.addFlash(r, msg)
	}

	s.redirect(w, r, "", "/user/"+userID)
}

// applyRoleChange 按目标角色增删 GA/PRA 分配。
// 关键语义：新增失败时不再删除另一角色；每次删除结果都被检查；
// 返回给用户的消息如实反映已完成与未完成的操作。
func (s *Server) applyRoleChange(ctx context.Context, org config.Org, userID, target string) (string, error) {
	gaID, err := s.graph.RoleDefinitionID(ctx, org, roleGlobalAdmin)
	if err != nil {
		return "无法解析角色定义，未执行角色变更：" + errMessage(err), err
	}
	praID, err := s.graph.RoleDefinitionID(ctx, org, rolePrivilegedAdmin)
	if err != nil {
		return "无法解析角色定义，未执行角色变更：" + errMessage(err), err
	}
	assignments, err := s.graph.RoleAssignmentsByPrincipal(ctx, org, userID)
	if err != nil {
		return "无法获取当前角色，未执行角色变更：" + errMessage(err), err
	}

	var gaAssignment, praAssignment string
	for _, a := range assignments {
		switch a.RoleDefinitionID {
		case gaID:
			gaAssignment = a.ID
		case praID:
			praAssignment = a.ID
		}
	}

	remove := func(assignmentID, name string) error {
		if assignmentID == "" {
			return nil
		}
		if err := s.graph.DeleteRoleAssignment(ctx, org, assignmentID); err != nil {
			return fmt.Errorf("移除 %s 失败：%w", name, err)
		}
		return nil
	}

	switch target {
	case "normal":
		if err := remove(gaAssignment, "全局管理员"); err != nil {
			return "角色更新未完成：" + errMessage(err), err
		}
		if err := remove(praAssignment, "特权角色管理员"); err != nil {
			return "角色更新未完成：" + errMessage(err), err
		}
		s.summaries.invalidate(org.ID)
		return "已设置为普通用户。", nil
	case roleGlobalAdmin:
		if gaAssignment == "" {
			if err := s.graph.AddRoleAssignment(ctx, org, userID, gaID); err != nil {
				return "授予全局管理员失败：" + errMessage(err) + "（未移除原有角色）", err
			}
		}
		if err := remove(praAssignment, "特权角色管理员"); err != nil {
			return "已授予全局管理员，但" + errMessage(err), err
		}
		s.summaries.invalidate(org.ID)
		return "已设置为全局管理员。", nil
	case rolePrivilegedAdmin:
		if praAssignment == "" {
			if err := s.graph.AddRoleAssignment(ctx, org, userID, praID); err != nil {
				return "授予特权角色管理员失败：" + errMessage(err) + "（未移除原有角色）", err
			}
		}
		if err := remove(gaAssignment, "全局管理员"); err != nil {
			return "已授予特权角色管理员，但" + errMessage(err), err
		}
		s.summaries.invalidate(org.ID)
		return "已设置为特权角色管理员。", nil
	}
	return "", fmt.Errorf("不支持的角色")
}

// --- 订阅分配 ---

type skuRow struct {
	SkuID       string
	SkuPart     string
	ProductName string
	Ratio       string
	Available   int
	StatusLabel string
	StatusBadge string
	Renewal     string
	Assigned    bool
}

type assignLicenseView struct {
	View
	OrgName  string
	UserID   string
	UserName string
	Skus     []skuRow
	Warnings []string
}

func (s *Server) enrichSkus(ctx context.Context, org config.Org, assigned map[string]bool) ([]skuRow, []string, error) {
	skus, err := s.graph.SubscribedSkus(ctx, org)
	if err != nil {
		return nil, nil, err
	}
	var warnings []string
	renewalBySku := map[string]string{}
	if subs, err := s.graph.DirectorySubscriptions(ctx, org); err == nil {
		for _, sub := range subs {
			if sub.SkuID != "" && sub.NextLifecycleDateTime != "" {
				renewalBySku[sub.SkuID] = sub.NextLifecycleDateTime
			}
		}
	} else {
		warnings = append(warnings, "续订信息获取失败，对应列显示为未知。")
	}
	rows := make([]skuRow, 0, len(skus))
	for _, sku := range skus {
		total := sku.PrepaidUnits.Enabled + sku.PrepaidUnits.Warning + sku.PrepaidUnits.Suspended
		available := total - sku.ConsumedUnits
		if available < 0 || total == 0 {
			available = 0
		}
		ratio := "—"
		if total > 0 {
			ratio = fmt.Sprintf("%d/%d", sku.ConsumedUnits, total)
		}
		label, badge := licenseStatus(sku.CapabilityStatus)
		renewal := "未知"
		if v, ok := renewalBySku[sku.SkuID]; ok {
			renewal = v
		}
		rows = append(rows, skuRow{
			SkuID: sku.SkuID, SkuPart: sku.SkuPartNumber,
			ProductName: s.catalog.Product(sku.SkuPartNumber),
			Ratio:       ratio, Available: available,
			StatusLabel: label, StatusBadge: badge, Renewal: renewal,
			Assigned: assigned[sku.SkuID],
		})
	}
	return rows, warnings, nil
}

func (s *Server) handleLicenseGET(w http.ResponseWriter, r *http.Request) {
	org, ok := s.requireOrg(w, r)
	if !ok {
		return
	}
	userID := r.PathValue("id")
	user, err := s.graph.GetUser(r.Context(), org, userID, false)
	if err != nil {
		s.redirect(w, r, "无法获取用户信息："+errMessage(err), "/user/"+userID)
		return
	}
	assigned := map[string]bool{}
	var warnings []string
	if details, err := s.graph.UserLicenseDetails(r.Context(), org, userID); err == nil {
		for _, d := range details {
			assigned[d.SkuID] = true
		}
	} else {
		warnings = append(warnings, "当前订阅读取失败，已分配状态可能不准确："+errMessage(err))
	}
	rows, warns, err := s.enrichSkus(r.Context(), org, assigned)
	if err != nil {
		// 回归：Python 版此处静默渲染空表。现在明示错误。
		s.renderOrgError(w, r, org, err)
		return
	}
	warnings = append(warnings, warns...)
	s.renderPage(w, r, http.StatusOK, "assign_license", assignLicenseView{
		View: s.viewData(r, "分配订阅"), OrgName: org.Name,
		UserID: userID, UserName: user.DisplayName, Skus: rows, Warnings: warnings,
	})
}

func (s *Server) handleLicensePOST(w http.ResponseWriter, r *http.Request) {
	org, ok := s.requireOrg(w, r)
	if !ok {
		return
	}
	userID := r.PathValue("id")

	current, err := s.graph.UserLicenseDetails(r.Context(), org, userID)
	if err != nil {
		s.redirect(w, r, "无法获取当前订阅，未执行订阅变更："+errMessage(err), "/user/"+userID)
		return
	}
	currentSet := map[string]bool{}
	for _, d := range current {
		currentSet[d.SkuID] = true
	}
	selected := map[string]bool{}
	for _, id := range r.PostForm["sku_ids"] {
		selected[id] = true
	}

	var add []graph.AddLicense
	var remove []string
	for id := range selected {
		if !currentSet[id] {
			add = append(add, graph.AddLicense{SkuID: id, DisabledPlans: []string{}})
		}
	}
	for id := range currentSet {
		if !selected[id] {
			remove = append(remove, id)
		}
	}
	if len(add) == 0 && len(remove) == 0 {
		s.redirect(w, r, "未检测到订阅变更。", "/user/"+userID)
		return
	}
	if err := s.graph.AssignLicense(r.Context(), org, userID, graph.AssignLicensePayload{
		AddLicenses: add, RemoveLicenses: remove,
	}); err != nil {
		s.redirect(w, r, "订阅更新失败："+errMessage(err), "/user/"+userID)
		return
	}
	s.summaries.invalidate(org.ID)
	s.redirect(w, r, "订阅已成功更新。", "/user/"+userID)
}

// --- 删除用户 ---

type deleteUserView struct {
	View
	OrgName  string
	UserID   string
	UserName string
}

func (s *Server) handleDeleteUserGET(w http.ResponseWriter, r *http.Request) {
	org, ok := s.requireOrg(w, r)
	if !ok {
		return
	}
	userID := r.PathValue("id")
	user, err := s.graph.GetUser(r.Context(), org, userID, false)
	name := user.DisplayName
	if err != nil || name == "" {
		name = user.UserPrincipalName
	}
	if name == "" {
		name = userID
	}
	s.renderPage(w, r, http.StatusOK, "delete_user", deleteUserView{
		View: s.viewData(r, "删除用户"), OrgName: org.Name, UserID: userID, UserName: name,
	})
}

// handleDeleteUserPOST 先删除用户；仅当 Graph 以“权限不足”拒绝时才
// 移除角色分配并重试一次。任何路径下角色移除的结果都如实报告
// （回归：Python 版预剥离角色且失败后静默丢失管理员权限）。
func (s *Server) handleDeleteUserPOST(w http.ResponseWriter, r *http.Request) {
	org, ok := s.requireOrg(w, r)
	if !ok {
		return
	}
	userID := r.PathValue("id")
	ctx := r.Context()

	err := s.graph.DeleteUser(ctx, org, userID)
	if err == nil {
		s.summaries.invalidate(org.ID)
		s.redirect(w, r, "用户已删除。", "/users?view=users")
		return
	}

	if !graph.IsInsufficientPrivileges(err) {
		s.redirect(w, r, "删除用户失败："+errMessage(err)+"（用户角色未被改动）", "/user/"+userID)
		return
	}

	// 权限不足路径：逐个移除角色分配并检查结果。
	assignments, listErr := s.graph.RoleAssignmentsByPrincipal(ctx, org, userID)
	if listErr != nil {
		s.redirect(w, r, "删除用户失败："+errMessage(err)+"；尝试移除角色时无法读取分配："+errMessage(listErr), "/user/"+userID)
		return
	}
	var removeFailures []string
	removed := 0
	for _, a := range assignments {
		if rmErr := s.graph.DeleteRoleAssignment(ctx, org, a.ID); rmErr != nil {
			removeFailures = append(removeFailures, errMessage(rmErr))
			continue
		}
		removed++
	}

	if retryErr := s.graph.DeleteUser(ctx, org, userID); retryErr == nil {
		s.summaries.invalidate(org.ID)
		s.redirect(w, r, fmt.Sprintf("用户已删除（移除了 %d 条角色分配）。", removed), "/users?view=users")
		return
	} else {
		msg := fmt.Sprintf("删除用户失败：%s", errMessage(retryErr))
		msg += fmt.Sprintf("（已移除 %d 条角色分配", removed)
		if len(removeFailures) > 0 {
			msg += "，其中 " + strings.Join(removeFailures, "；")
		}
		msg += "）"
		s.redirect(w, r, msg, "/user/"+userID)
	}
}

// --- 新建用户 ---

type addUserView struct {
	View
	OrgName   string
	Domains   []domainOption
	Skus      []skuRow
	Warnings  []string
	FormName  string
	FormLocal string
}

type domainOption struct {
	Name      string
	IsDefault bool
}

func (s *Server) handleAddUserGET(w http.ResponseWriter, r *http.Request) {
	org, ok := s.requireOrg(w, r)
	if !ok {
		return
	}
	var warnings []string

	domains, err := s.graph.Domains(r.Context(), org)
	if err != nil {
		// 回归：Python 版静默空下拉。现在明示错误。
		s.renderOrgError(w, r, org, err)
		return
	}
	var options []domainOption
	var defaultDomain string
	for _, d := range domains {
		if !d.IsAvailable() {
			continue // 未验证域名不可用于创建用户
		}
		options = append(options, domainOption{Name: d.ID, IsDefault: d.IsDefault})
		if d.IsDefault {
			defaultDomain = d.ID
		}
	}
	if defaultDomain == "" {
		for _, d := range domains {
			if !strings.HasSuffix(d.ID, ".onmicrosoft.com") {
				defaultDomain = d.ID
				break
			}
		}
	}
	for i := range options {
		options[i].IsDefault = options[i].Name == defaultDomain
	}

	rows, warns, err := s.enrichSkus(r.Context(), org, nil)
	if err != nil {
		s.renderOrgError(w, r, org, err)
		return
	}
	warnings = append(warnings, warns...)

	s.renderPage(w, r, http.StatusOK, "add_user", addUserView{
		View: s.viewData(r, "添加用户"), OrgName: org.Name,
		Domains: options, Skus: rows, Warnings: warnings,
	})
}

func localPartOK(s string) bool {
	for _, c := range s {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9',
			c == '.', c == '-', c == '_', c == '\'', c == '+':
		default:
			return false
		}
	}
	return s != ""
}

type createdCreds struct {
	UPN      string `json:"upn"`
	Password string `json:"password"`
	UserID   string `json:"user_id"`
}

const sessionCreatedKey = "created_creds"

func (s *Server) handleAddUserPOST(w http.ResponseWriter, r *http.Request) {
	org, ok := s.requireOrg(w, r)
	if !ok {
		return
	}
	displayName := strings.TrimSpace(r.PostFormValue("display_name"))
	localPart := strings.TrimSpace(r.PostFormValue("local_part"))
	domain := r.PostFormValue("domain")
	selected := r.PostForm["sku_ids"]
	role := r.PostFormValue("role")
	ctx := r.Context()

	if displayName == "" || localPart == "" {
		s.redirect(w, r, "显示名称和用户名为必填。", "/users/add")
		return
	}
	if !localPartOK(localPart) {
		s.redirect(w, r, "用户名只能包含字母、数字和 . - _ ' + 。", "/users/add")
		return
	}
	if domain == "" {
		s.redirect(w, r, "请选择域名。", "/users/add")
		return
	}
	switch role {
	case "normal", roleGlobalAdmin, rolePrivilegedAdmin:
	default:
		s.redirect(w, r, "不支持的角色。", "/users/add")
		return
	}

	usageLocation := "US"
	if info, err := s.graph.Organization(ctx, org); err == nil && info.CountryLetterCode != "" {
		usageLocation = info.CountryLetterCode
	}

	tempPassword, err := generateTempPassword()
	if err != nil {
		s.redirect(w, r, "生成临时密码失败，请重试。", "/users/add")
		return
	}
	upn := localPart + "@" + domain

	user, err := s.graph.CreateUser(ctx, org, graph.NewUserPayload{
		AccountEnabled:    true,
		UserType:          "Member",
		DisplayName:       displayName,
		MailNickname:      localPart,
		UserPrincipalName: upn,
		UsageLocation:     usageLocation,
		PasswordProfile: graph.PasswordProfile{
			Password: tempPassword, ForceChangePasswordNextSignIn: true,
		},
	})
	if err != nil {
		s.redirect(w, r, "创建用户失败："+errMessage(err), "/users/add")
		return
	}
	s.summaries.invalidate(org.ID)

	if len(selected) > 0 {
		add := make([]graph.AddLicense, 0, len(selected))
		for _, id := range selected {
			add = append(add, graph.AddLicense{SkuID: id, DisabledPlans: []string{}})
		}
		if err := s.graph.AssignLicense(ctx, org, user.ID, graph.AssignLicensePayload{AddLicenses: add}); err != nil {
			// 回归：Python 版此处对 Graph 错误体保持沉默。
			s.addFlash(r, "用户已创建，但订阅分配失败："+errMessage(err)+"，请在用户详情页重试。")
		}
	}

	if role != "normal" {
		// 后台带重试授予角色（新用户传播延迟）；最终结果写结构化日志。
		roleName := role
		userID := user.ID
		s.background.Retry("为新建用户授予角色 "+roleName, 5, 5*time.Second, func() error {
			roleID, err := s.graph.RoleDefinitionID(context.Background(), org, roleName)
			if err != nil {
				return err
			}
			return s.graph.AddRoleAssignment(context.Background(), org, userID, roleID)
		})
		s.addFlash(r, "角色分配正在后台处理，请稍后在用户详情页确认。")
	}

	// 一次性凭据：存会话，仅在专门页面展示一次（回归：不再走 flash cookie）。
	s.sessSet(r, sessionCreatedKey, map[string]string{
		"upn": upn, "password": tempPassword, "user_id": user.ID,
	})
	s.redirect(w, r, "", "/user/"+user.ID+"/created")
}

func (s *Server) handleUserCreated(w http.ResponseWriter, r *http.Request) {
	// 会话数据经 JSON 往返，值类型统一为 any。
	creds, _ := s.sessGet(r, sessionCreatedKey).(map[string]any)
	str := func(key string) string {
		v, _ := creds[key].(string)
		return v
	}
	if str("user_id") == "" || str("user_id") != r.PathValue("id") {
		// 已消费或不匹配 → 回详情页。
		s.redirect(w, r, "", "/user/"+r.PathValue("id"))
		return
	}
	s.sessRemove(r, sessionCreatedKey) // 渲染即失效，凭据仅展示一次
	s.renderPage(w, r, http.StatusOK, "user_created", struct {
		View
		UPN      string
		Password string
		UserID   string
	}{View: s.viewData(r, "用户已创建"), UPN: str("upn"), Password: str("password"), UserID: str("user_id")})
}

// generateTempPassword 生成满足 Graph 复杂度要求的 12 位临时密码
// （至少各含一个小写、大写、数字与特殊字符，crypto/rand 随机）。
func generateTempPassword() (string, error) {
	const (
		lower   = "abcdefghijklmnopqrstuvwxyz"
		upper   = "ABCDEFGHIJKLMNOPQRSTUVWXYZ"
		digits  = "0123456789"
		special = "!@#$%^&*()"
	)
	all := lower + upper + digits + special
	classes := []string{lower, upper, digits, special}
	buf := make([]byte, 12)
	// 前四位各取一个字符类，再随机补齐，最后 Fisher–Yates 洗牌。
	for i, class := range classes {
		c, err := cryptoRandInt(len(class))
		if err != nil {
			return "", err
		}
		buf[i] = class[c]
	}
	for i := 4; i < len(buf); i++ {
		c, err := cryptoRandInt(len(all))
		if err != nil {
			return "", err
		}
		buf[i] = all[c]
	}
	for i := len(buf) - 1; i > 0; i-- {
		j, err := cryptoRandInt(i + 1)
		if err != nil {
			return "", err
		}
		buf[i], buf[j] = buf[j], buf[i]
	}
	return string(buf), nil
}

func cryptoRandInt(n int) (int, error) {
	limit := (256 / n) * n // 拒绝采样消除取模偏差
	buf := make([]byte, 1)
	for {
		if _, err := rand.Read(buf); err != nil {
			return 0, err
		}
		if int(buf[0]) < limit {
			return int(buf[0]) % n, nil
		}
	}
}
