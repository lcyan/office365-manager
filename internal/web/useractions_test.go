package web

import (
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"
)

// actionGraph 记录写操作并按场景响应。
type actionGraph struct {
	mu            sync.Mutex
	calls         []string // "METHOD path"
	patchErr      bool
	deleteUserErr string // 空 = 成功；"insufficient" = 权限不足；其他 = 通用错误
	assignErr     string
	removeErr     string
	addRoleErr    string
	removed       []string
}

func (g *actionGraph) record(call string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.calls = append(g.calls, call)
}

func (g *actionGraph) has(call string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	for _, c := range g.calls {
		if c == call {
			return true
		}
	}
	return false
}

func (g *actionGraph) count(prefix string) int {
	g.mu.Lock()
	defer g.mu.Unlock()
	n := 0
	for _, c := range g.calls {
		if strings.HasPrefix(c, prefix) {
			n++
		}
	}
	return n
}

func (g *actionGraph) handler() graphHandler {
	return func(w http.ResponseWriter, r *http.Request) bool {
		path := r.URL.Path
		switch {
		case path == "/subscribedSkus":
			writeGraphJSON(w, 200, map[string]any{"value": []any{
				map[string]any{"skuId": "sku-1", "skuPartNumber": "SPE_E3", "capabilityStatus": "Enabled",
					"consumedUnits": 1, "prepaidUnits": map[string]int{"enabled": 5}},
			}})
			return true
		case path == "/directory/subscriptions":
			writeGraphJSON(w, 200, map[string]any{"value": []any{}})
			return true
		case path == "/domains":
			writeGraphJSON(w, 200, map[string]any{"value": []any{
				map[string]any{"id": "contoso.com", "isDefault": true, "isVerified": true, "state": "Verified"},
			}})
			return true
		case path == "/organization":
			writeGraphJSON(w, 200, map[string]any{"value": []any{
				map[string]any{"id": "org", "countryLetterCode": "CN"},
			}})
			return true
		case path == "/roleManagement/directory/roleDefinitions":
			id := gaRoleID
			if strings.Contains(r.URL.RawQuery, "Privileged") {
				id = praRoleID
			}
			writeGraphJSON(w, 200, map[string]any{"value": []any{map[string]any{"id": id}}})
			return true
		case path == "/roleManagement/directory/roleAssignments":
			if r.Method == http.MethodPost {
				g.record("POST roleAssignments")
				if g.addRoleErr != "" {
					w.WriteHeader(http.StatusForbidden)
					_, _ = w.Write([]byte(`{"error":{"code":"X","message":"` + g.addRoleErr + `"}}`))
					return true
				}
				writeGraphJSON(w, 201, map[string]any{"id": "new-assignment"})
				return true
			}
			// GET：按 principal 或 roleDefinition 过滤。
			if strings.Contains(r.URL.RawQuery, "principalId") {
				writeGraphJSON(w, 200, map[string]any{"value": []any{
					map[string]any{"id": "11111111-2222-3333-4444-555555555555", "roleDefinitionId": gaRoleID, "principalId": userID},
					map[string]any{"id": "66666666-7777-8888-9999-000000000000", "roleDefinitionId": praRoleID, "principalId": userID},
				}})
				return true
			}
			writeGraphJSON(w, 200, map[string]any{"value": []any{
				map[string]any{"id": "as-ga", "roleDefinitionId": gaRoleID, "principalId": userID,
					"principal": map[string]any{"id": userID, "displayName": "Alice", "@odata.type": "#microsoft.graph.user"}},
			}})
			return true
		case strings.HasPrefix(path, "/roleManagement/directory/roleAssignments/"):
			g.record("DELETE assignment")
			if g.removeErr != "" {
				w.WriteHeader(http.StatusForbidden)
				_, _ = w.Write([]byte(`{"error":{"code":"X","message":"` + g.removeErr + `"}}`))
				return true
			}
			g.mu.Lock()
			g.removed = append(g.removed, path)
			g.mu.Unlock()
			w.WriteHeader(http.StatusNoContent)
			return true
		case path == "/users" && r.Method == http.MethodPost:
			g.record("POST users")
			writeGraphJSON(w, 201, map[string]any{"id": userID, "userPrincipalName": "new@contoso.com"})
			return true
		case path == "/users" && r.URL.Query().Get("$count") == "true":
			writeGraphJSON(w, 200, map[string]any{"value": []any{}, "@odata.count": 0})
			return true
		case strings.HasSuffix(path, "/assignLicense"):
			g.record("POST assignLicense")
			if g.assignErr != "" {
				w.WriteHeader(http.StatusForbidden)
				_, _ = w.Write([]byte(`{"error":{"code":"X","message":"` + g.assignErr + `"}}`))
				return true
			}
			writeGraphJSON(w, 200, map[string]any{"id": userID})
			return true
		case strings.HasSuffix(path, "/licenseDetails"):
			writeGraphJSON(w, 200, map[string]any{"value": []any{
				map[string]any{"skuId": "sku-1", "skuPartNumber": "SPE_E3"},
			}})
			return true
		case path == "/users/"+userID:
			switch r.Method {
			case http.MethodGet:
				writeGraphJSON(w, 200, map[string]any{"id": userID, "displayName": "Alice",
					"userPrincipalName": "alice@x.com", "accountEnabled": true})
				return true
			case http.MethodPatch:
				g.record("PATCH user")
				if g.patchErr {
					w.WriteHeader(http.StatusForbidden)
					_, _ = w.Write([]byte(`{"error":{"code":"X","message":"密码不符合要求"}}`))
					return true
				}
				w.WriteHeader(http.StatusNoContent)
				return true
			case http.MethodDelete:
				g.record("DELETE user")
				// 首次删除返回权限不足；重试（剥离角色后）成功。
				if g.deleteUserErr == "insufficient" && g.count("DELETE user") >= 2 {
					g.mu.Lock()
					g.deleteUserErr = ""
					g.mu.Unlock()
				}
				if g.deleteUserErr == "insufficient" {
					w.WriteHeader(http.StatusForbidden)
					_, _ = w.Write([]byte(`{"error":{"code":"Authorization_RequestDenied","message":"Insufficient privileges to complete the operation."}}`))
					return true
				}
				if g.deleteUserErr != "" {
					w.WriteHeader(http.StatusBadRequest)
					_, _ = w.Write([]byte(`{"error":{"code":"X","message":"` + g.deleteUserErr + `"}}`))
					return true
				}
				w.WriteHeader(http.StatusNoContent)
				return true
			}
		}
		return false
	}
}

func newActionServer(t *testing.T, g *actionGraph) (*http.Client, string) {
	t.Helper()
	ts, store, _ := newTestServerOpts(t, serverOpts{password: "secret-pass", graph: g.handler()})
	client := loginAs(t, ts, "secret-pass")
	addOrgViaUI(t, client, ts.URL, "Tenant A")
	client.Get(ts.URL + "/select_org/" + store.Orgs()[0].ID)
	return client, ts.URL
}

// 删除直接成功：不触碰任何角色分配（回归：Python 版总是先剥离角色）。
func TestDeleteUserSuccessDoesNotTouchRoles(t *testing.T) {
	g := &actionGraph{}
	client, base := newActionServer(t, g)

	page, _ := getBody(t, client, base+"/user/"+userID+"/delete")
	resp, err := client.PostForm(base+"/user/"+userID+"/delete",
		url.Values{"csrf_token": {csrfFrom(t, page)}})
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	if !g.has("DELETE user") {
		t.Fatal("user delete not called")
	}
	if g.count("DELETE assignment") != 0 {
		t.Errorf("role assignments removed on clean delete: %v", g.calls)
	}
	feedback, _ := getBody(t, client, base+"/users?view=users")
	if !strings.Contains(feedback, "用户已删除") {
		t.Errorf("success flash missing:\n%s", feedback)
	}
}

// 权限不足路径：移除角色后重试成功，并如实报告移除数量。
func TestDeleteUserInsufficientPrivilegesStripsThenRetries(t *testing.T) {
	g := &actionGraph{deleteUserErr: "insufficient"}
	client, base := newActionServer(t, g)

	page, _ := getBody(t, client, base+"/user/"+userID+"/delete")
	resp, _ := client.PostForm(base+"/user/"+userID+"/delete",
		url.Values{"csrf_token": {csrfFrom(t, page)}})
	resp.Body.Close()

	if g.count("DELETE user") != 2 {
		t.Errorf("delete attempts = %d, want 2 (retry after stripping)", g.count("DELETE user"))
	}
	if g.count("DELETE assignment") != 2 {
		t.Errorf("assignment removals = %d, want 2", g.count("DELETE assignment"))
	}
	feedback, _ := getBody(t, client, base+"/users?view=users")
	if !strings.Contains(feedback, "移除了 2 条角色分配") {
		t.Errorf("honest removal count missing:\n%s", feedback)
	}
}

// 其他错误路径：角色完全不被触碰，消息明确说明。
func TestDeleteUserOtherErrorKeepsRoles(t *testing.T) {
	g := &actionGraph{deleteUserErr: "最后一个全局管理员不能删除"}
	client, base := newActionServer(t, g)

	page, _ := getBody(t, client, base+"/user/"+userID+"/delete")
	resp, _ := client.PostForm(base+"/user/"+userID+"/delete",
		url.Values{"csrf_token": {csrfFrom(t, page)}})
	resp.Body.Close()

	if g.count("DELETE assignment") != 0 {
		t.Errorf("assignments touched on non-privilege error: %v", g.calls)
	}
	feedback, _ := getBody(t, client, base+"/user/"+userID)
	if !strings.Contains(feedback, "角色未被改动") {
		t.Errorf("message should state roles untouched:\n%.600s", feedback)
	}
}

// PATCH 失败 → 中止角色变更（回归：Python 版继续执行角色切换）。
func TestUpdateUserPatchFailureAbortsRoleChange(t *testing.T) {
	g := &actionGraph{patchErr: true}
	client, base := newActionServer(t, g)

	page, _ := getBody(t, client, base+"/user/"+userID)
	resp2, err2 := client.PostForm(base+"/user/"+userID+"/update", url.Values{
		"csrf_token":      {csrfFrom(t, page)},
		"new_password":    {"BadPolicy!1"},
		"account_enabled": {"true"},
		"role":            {"normal"},
	})
	if err2 != nil {
		t.Fatal(err2)
	}
	resp2.Body.Close()

	if !g.has("PATCH user") {
		t.Fatal("PATCH not attempted")
	}
	if g.count("DELETE assignment") != 0 || g.has("POST roleAssignments") {
		t.Errorf("role change executed despite PATCH failure: %v", g.calls)
	}
	feedback, _ := getBody(t, client, base+"/user/"+userID)
	if !strings.Contains(feedback, "未执行角色变更") {
		t.Errorf("abort message missing:\n%.600s", feedback)
	}
}

// 普通切换：GA→normal 移除两条分配。
func TestUpdateUserRoleToNormal(t *testing.T) {
	g := &actionGraph{}
	client, base := newActionServer(t, g)

	page, _ := getBody(t, client, base+"/user/"+userID)
	resp, err := client.PostForm(base+"/user/"+userID+"/update", url.Values{
		"csrf_token":      {csrfFrom(t, page)},
		"account_enabled": {"true"},
		"role":            {"normal"},
	})
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	if g.count("DELETE assignment") != 2 {
		t.Errorf("assignment removals = %d, want 2", g.count("DELETE assignment"))
	}
	if g.has("PATCH user") {
		t.Error("PATCH sent although nothing changed")
	}
	feedback, _ := getBody(t, client, base+"/user/"+userID)
	if !strings.Contains(feedback, "已设置为普通用户") {
		t.Errorf("role flash missing:\n%.600s", feedback)
	}
}

// 新建用户：订阅分配失败必须明示（回归：Python 版对错误体沉默），
// 一次性凭据页只展示一次（回归：临时密码曾随 flash 进入 cookie）。
func TestAddUserLicenseFailureAndOneTimeCreds(t *testing.T) {
	g := &actionGraph{assignErr: "没有可用许可"}
	client, base := newActionServer(t, g)

	page, _ := getBody(t, client, base+"/users/add")
	resp, err := client.PostForm(base+"/users/add", url.Values{
		"csrf_token":   {csrfFrom(t, page)},
		"display_name": {"New User"},
		"local_part":   {"new"},
		"domain":       {"contoso.com"},
		"sku_ids":      {"sku-1"},
		"role":         {"normal"},
	})
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	if resp.Header.Get("Location") != "/user/"+userID+"/created" {
		t.Fatalf("redirect = %s, want created page", resp.Header.Get("Location"))
	}

	// 一次性凭据页：显示 UPN 与密码；订阅分配失败的闪存也在该页呈现
	//（创建后的第一站就是凭据页）。
	creds, _ := getBody(t, client, base+"/user/"+userID+"/created")
	if !strings.Contains(creds, "new@contoso.com") || !strings.Contains(creds, "只显示一次") {
		t.Errorf("credentials page wrong:\n%.600s", creds)
	}
	if !strings.Contains(creds, "订阅分配失败") || !strings.Contains(creds, "没有可用许可") {
		t.Errorf("license failure not surfaced on credentials page:\n%.600s", creds)
	}

	// 第二次访问凭据页：不再显示密码。
	again, _ := getBody(t, client, base+"/user/"+userID+"/created")
	if strings.Contains(again, "临时密码") {
		t.Error("credentials shown a second time")
	}
}

// 非法用户名（OData/路径敏感字符）在应用层即被拒绝。
func TestAddUserRejectsBadLocalPart(t *testing.T) {
	g := &actionGraph{}
	client, base := newActionServer(t, g)

	page, _ := getBody(t, client, base+"/users/add")
	for _, bad := range []string{"a'b or x", "a b", "a/b", "@evil"} {
		resp, err := client.PostForm(base+"/users/add", url.Values{
			"csrf_token":   {csrfFrom(t, page)},
			"display_name": {"X"},
			"local_part":   {bad},
			"domain":       {"contoso.com"},
			"role":         {"normal"},
		})
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if g.has("POST users") {
			t.Fatalf("create attempted for local_part %q", bad)
		}
	}
}

// 订阅页：已分配勾选、无变化提交提示、变更提交调用 assignLicense。
func TestLicenseGetAndPost(t *testing.T) {
	g := &actionGraph{}
	client, base := newActionServer(t, g)

	page, resp := getBody(t, client, base+"/user/"+userID+"/license")
	if resp.StatusCode != 200 {
		t.Fatalf("license page = %d", resp.StatusCode)
	}
	if !strings.Contains(page, "checked") || !strings.Contains(page, "Microsoft 365 E3") {
		t.Errorf("assigned checkbox missing:\n%.600s", page)
	}

	// 无变化。
	resp2, err := client.PostForm(base+"/user/"+userID+"/license",
		url.Values{"csrf_token": {csrfFrom(t, page)}, "sku_ids": {"sku-1"}})
	if err != nil {
		t.Fatal(err)
	}
	resp2.Body.Close()
	if g.has("POST assignLicense") {
		t.Error("assignLicense called for no-op")
	}
	feedback, _ := getBody(t, client, base+"/user/"+userID)
	if !strings.Contains(feedback, "未检测到订阅变更") {
		t.Errorf("no-op message missing:\n%.600s", feedback)
	}

	// 取消勾选 → 移除。
	page, _ = getBody(t, client, base+"/user/"+userID+"/license")
	resp3, err := client.PostForm(base+"/user/"+userID+"/license",
		url.Values{"csrf_token": {csrfFrom(t, page)}})
	if err != nil {
		t.Fatal(err)
	}
	resp3.Body.Close()
	if !g.has("POST assignLicense") {
		t.Error("assignLicense not called for removal")
	}
	feedback2, _ := getBody(t, client, base+"/user/"+userID)
	if !strings.Contains(feedback2, "订阅已成功更新") {
		t.Errorf("success message missing:\n%.600s", feedback2)
	}
}
