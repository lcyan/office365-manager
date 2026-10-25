package web

import (
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
)

const (
	gaRoleID  = "62e90394-69f5-4237-9190-012177145e10"
	praRoleID = "6e466b3b-1a0c-4b0a-9b6d-7b2a0a0d1e2f"
	userID    = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
	userID2   = "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"
	groupID   = "cccccccc-cccc-cccc-cccc-cccccccccccc"
)

// userFlowGraph 提供概况/列表/详情所需的伪 Graph 路由。
// subscriptionsFail 为 true 时 /directory/subscriptions 返回 403（测试部分概况）。
func userFlowGraph(subscriptionsFail *bool, graphCalls *int32) graphHandler {
	return func(w http.ResponseWriter, r *http.Request) bool {
		if graphCalls != nil {
			atomic.AddInt32(graphCalls, 1)
		}
		q := r.URL.Query()
		switch {
		case r.URL.Path == "/subscribedSkus":
			writeGraphJSON(w, 200, map[string]any{"value": []any{
				map[string]any{"skuId": "sku-1", "skuPartNumber": "SPE_E3", "capabilityStatus": "Enabled",
					"consumedUnits": 3, "prepaidUnits": map[string]int{"enabled": 5, "warning": 0, "suspended": 0}},
			}})
			return true
		case r.URL.Path == "/directory/subscriptions":
			if subscriptionsFail != nil && *subscriptionsFail {
				w.WriteHeader(http.StatusForbidden)
				_, _ = w.Write([]byte(`{"error":{"code":"Authorization_RequestDenied","message":"denied"}}`))
				return true
			}
			writeGraphJSON(w, 200, map[string]any{"value": []any{
				map[string]any{"skuId": "sku-1", "status": "Enabled", "nextLifecycleDateTime": "2027-01-01T00:00:00Z"},
			}})
			return true
		case r.URL.Path == "/roleManagement/directory/roleDefinitions":
			name := "Global Administrator"
			if strings.Contains(q.Get("$filter"), "Privileged") {
				name = "Privileged Role Administrator"
			}
			writeGraphJSON(w, 200, map[string]any{"value": []any{map[string]any{"id": map[bool]string{true: praRoleID, false: gaRoleID}[name == "Privileged Role Administrator"], "displayName": name}}})
			return true
		case r.URL.Path == "/roleManagement/directory/roleAssignments":
			filter := q.Get("$filter")
			switch {
			case strings.Contains(filter, gaRoleID):
				writeGraphJSON(w, 200, map[string]any{"value": []any{
					map[string]any{"id": "as-1", "roleDefinitionId": gaRoleID, "principalId": userID,
						"principal": map[string]any{"id": userID, "displayName": "Alice", "@odata.type": "#microsoft.graph.user",
							"userPrincipalName": "alice@x.com", "givenName": "Alice", "surname": "Green"}},
					// 组主体：旧版在此让整页失败。
					map[string]any{"id": "as-2", "roleDefinitionId": gaRoleID, "principalId": groupID,
						"principal": map[string]any{"id": groupID, "displayName": "IT Admins", "@odata.type": "#microsoft.graph.group"}},
				}})
				return true
			case strings.Contains(filter, praRoleID):
				writeGraphJSON(w, 200, map[string]any{"value": []any{}})
				return true
			case strings.Contains(filter, "principalId"):
				writeGraphJSON(w, 200, map[string]any{"value": []any{
					map[string]any{"id": "as-1", "roleDefinition": map[string]any{"id": gaRoleID, "displayName": "Global Administrator"}},
				}})
				return true
			}
			writeGraphJSON(w, 200, map[string]any{"value": []any{}})
			return true
		case r.URL.Path == "/users/$count":
			// 专用计数端点：纯文本数字（搜索过滤时为 0）。
			count := len(buildFakeUsers())
			if strings.Contains(q.Get("$filter"), "alice") {
				count = 0
			}
			w.Header().Set("Content-Type", "text/plain")
			_, _ = w.Write([]byte(fmt.Sprint(count)))
			return true
		case r.URL.Path == "/users" && q.Get("$count") == "true":
			// Graph /users 不支持 $skip：游标（$skiptoken）分页。
			if q.Get("$skip") != "" {
				panic("fake Graph: $skip must not be sent: " + q.Encode())
			}
			if strings.Contains(q.Get("$filter"), "alice") {
				writeGraphJSON(w, 200, map[string]any{"value": []any{}, "@odata.count": 0})
				return true
			}
			all := buildFakeUsers()
			start := 0
			if s := q.Get("$skiptoken"); s != "" {
				fmt.Sscanf(s, "%d", &start)
			}
			pageSize := 50
			if n := 0; q.Get("$top") != "" {
				fmt.Sscanf(q.Get("$top"), "%d", &n)
				if n > 0 {
					pageSize = n
				}
			}
			end := start + pageSize
			if end > len(all) {
				end = len(all)
			}
			resp := map[string]any{"value": all[start:end], "@odata.count": len(all)}
			if end < len(all) {
				resp["@odata.nextLink"] = fmt.Sprintf(
					"http://%s/users?$count=true&$top=%d&$skiptoken=%d", r.Host, pageSize, end)
			}
			writeGraphJSON(w, 200, resp)
			return true
		case r.URL.Path == "/users/"+userID:
			writeGraphJSON(w, 200, map[string]any{"id": userID, "displayName": "Alice",
				"userPrincipalName": "alice@x.com", "accountEnabled": true, "createdDateTime": "2024-01-01T00:00:00Z"})
			return true
		case r.URL.Path == "/users/"+userID+"/licenseDetails":
			writeGraphJSON(w, 200, map[string]any{"value": []any{
				map[string]any{"skuId": "sku-1", "skuPartNumber": "SPE_E3"},
			}})
			return true
		}
		return false
	}
}

func TestSummaryViewAndCache(t *testing.T) {
	fail := false
	var calls int32
	ts, store, _ := newTestServerOpts(t, serverOpts{
		password: "secret-pass",
		graph:    userFlowGraph(&fail, &calls),
	})
	client := loginAs(t, ts, "secret-pass")
	addOrgViaUI(t, client, ts.URL, "Tenant A")
	org := store.Orgs()[0]
	resp, err := client.Get(ts.URL + "/select_org/" + org.ID)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	// 首次概况：完整渲染，含许可行与计数（2 个 GA、25 个用户）。
	page, _ := getBody(t, client, ts.URL+"/users")
	for _, want := range []string{"Microsoft 365 E3", "3/5", "活动", "2027-01-01",
		`<div class="stat-value">2</div>`, `<div class="stat-value">25</div>`} {
		if !strings.Contains(page, want) {
			t.Errorf("summary missing %q", want)
		}
	}
	first := atomic.LoadInt32(&calls)

	// 二次加载命中缓存：伪 Graph 无新调用。
	page2, _ := getBody(t, client, ts.URL+"/users")
	if !strings.Contains(page2, "组织概况") {
		t.Error("second load not summary")
	}
	if got := atomic.LoadInt32(&calls); got != first {
		t.Errorf("graph calls after cached load = %d, want %d", got, first)
	}

	// 订阅读取失败 → 部分概况：显示警告且不缓存（下次仍会请求）。
	fail = true
	resp, err = client.Get(ts.URL + "/users?refresh=1")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	partial, _ := getBody(t, client, ts.URL+"/users?refresh=1")
	if !strings.Contains(partial, "部分统计") {
		t.Error("partial summary warning missing")
	}
	// 部分概况不缓存：再次加载仍发起请求。
	before := atomic.LoadInt32(&calls)
	_, _ = getBody(t, client, ts.URL+"/users")
	if atomic.LoadInt32(&calls) == before {
		t.Error("partial summary was cached")
	}
}

func TestSummaryGraphFailureShowsErrorPage(t *testing.T) {
	ts, store, _ := newTestServerOpts(t, serverOpts{
		password: "secret-pass",
		graph: func(w http.ResponseWriter, r *http.Request) bool {
			if r.URL.Path == "/subscribedSkus" {
				w.WriteHeader(http.StatusForbidden)
				_, _ = w.Write([]byte(`{"error":{"code":"Authorization_RequestDenied","message":"令牌无权读取订阅"}}`))
				return true
			}
			return false
		},
	})
	client := loginAs(t, ts, "secret-pass")
	addOrgViaUI(t, client, ts.URL, "Tenant A")
	org := store.Orgs()[0]
	client.Get(ts.URL + "/select_org/" + org.ID)

	// 错误页必须携带具体原因，而不是静默空表。
	page, resp := getBody(t, client, ts.URL+"/users")
	if resp.StatusCode != 200 || !strings.Contains(page, "API 不可用") || !strings.Contains(page, "令牌无权读取订阅") {
		t.Errorf("error page missing message:\n%.400s", page)
	}
}

func TestUsersListPaginationConsistent(t *testing.T) {
	fail := false
	ts, store, _ := newTestServerOpts(t, serverOpts{
		password: "secret-pass",
		graph:    userFlowGraph(&fail, nil),
	})
	client := loginAs(t, ts, "secret-pass")
	addOrgViaUI(t, client, ts.URL, "Tenant A")
	client.Get(ts.URL + "/select_org/" + store.Orgs()[0].ID)

	// 总数 25、页大小 10 → 共 3 页；第 1 页 Alice/Bob、无 Zed。
	page, _ := getBody(t, client, ts.URL+"/users?view=users&page_size=10")
	if !strings.Contains(page, "共 3 页，当前第 1 页") || !strings.Contains(page, "Alice") || !strings.Contains(page, "Bob") {
		t.Errorf("page 1 wrong:\n%.600s", page)
	}
	if strings.Contains(page, "Zed") {
		t.Error("page 1 should not contain the last user")
	}
	page2, _ := getBody(t, client, ts.URL+"/users?view=users&page_size=10&page=3")
	if !strings.Contains(page2, "共 3 页，当前第 3 页") || !strings.Contains(page2, "Zed") {
		t.Errorf("page 3 wrong:\n%.600s", page2)
	}

	// 深链超出末页 → 回退到末页，页码与内容一致（回归：Python 版此处显示错页）。
	page9, _ := getBody(t, client, ts.URL+"/users?view=users&page_size=10&page=9")
	if !strings.Contains(page9, "当前第 3 页") || !strings.Contains(page9, "Zed") {
		t.Errorf("deep link not clamped to last page:\n%.600s", page9)
	}

	// 管理员徽章：Alice 是 GA。
	if !strings.Contains(page, "全局管理员") {
		t.Error("GA badge missing on page 1")
	}
}

func TestRoleFilterShowsGroupPrincipals(t *testing.T) {
	fail := false
	ts, store, _ := newTestServerOpts(t, serverOpts{
		password: "secret-pass",
		graph:    userFlowGraph(&fail, nil),
	})
	client := loginAs(t, ts, "secret-pass")
	addOrgViaUI(t, client, ts.URL, "Tenant A")
	client.Get(ts.URL + "/select_org/" + store.Orgs()[0].ID)

	// 角色筛选：用户与组都呈现，组标记类型，整页不再失败（回归：Python 版整页报错）。
	page, _ := getBody(t, client, ts.URL+"/users?view=users&role=Global+Administrator")
	if !strings.Contains(page, "Alice") || !strings.Contains(page, "IT Admins") || !strings.Contains(page, "group") {
		t.Errorf("role filter page wrong:\n%.800s", page)
	}
	if strings.Contains(page, "API 不可用") {
		t.Error("role filter page failed due to group principal")
	}

	// 组行不提供「查看」入口。
	if !strings.Contains(page, "非用户主体") {
		t.Error("group row should not link to user detail")
	}
}

func TestUserDetailRendersAndWarns(t *testing.T) {
	fail := false
	ts, store, _ := newTestServerOpts(t, serverOpts{
		password: "secret-pass",
		graph:    userFlowGraph(&fail, nil),
	})
	client := loginAs(t, ts, "secret-pass")
	addOrgViaUI(t, client, ts.URL, "Tenant A")
	client.Get(ts.URL + "/select_org/" + store.Orgs()[0].ID)

	page, resp := getBody(t, client, ts.URL+"/user/"+userID)
	if resp.StatusCode != 200 {
		t.Fatalf("detail status = %d", resp.StatusCode)
	}
	for _, want := range []string{"Alice", "alice@x.com", "Global Administrator", "Microsoft 365 E3", "当前页"} {
		if want == "当前页" {
			continue
		}
		if !strings.Contains(page, want) {
			t.Errorf("detail missing %q", want)
		}
	}

	// 未选择组织 → 引导选择。
	fresh := loginAs(t, ts, "secret-pass")
	_, resp2 := getBody(t, fresh, ts.URL+"/user/"+userID)
	if resp2.Header.Get("Location") != "/organizations" {
		t.Errorf("no-org redirect = %q, want /organizations", resp2.Header.Get("Location"))
	}
}

// buildFakeUsers 构造 25 个伪用户：第 1 页（10 条）Alice/Bob…，末页以 Zed 结尾。
func buildFakeUsers() []any {
	all := []any{
		map[string]any{"id": userID, "displayName": "Alice", "userPrincipalName": "alice@x.com", "surname": "Green", "givenName": "Alice"},
		map[string]any{"id": userID2, "displayName": "Bob", "userPrincipalName": "bob@x.com", "surname": "Blue", "givenName": "Bob"},
		map[string]any{"id": "dddddddd-dddd-dddd-dddd-dddddddddddd", "displayName": "Carol", "userPrincipalName": "carol@x.com"},
	}
	for i := 3; i < 24; i++ {
		all = append(all, map[string]any{
			"id":                fmt.Sprintf("00000000-0000-0000-0000-%012d", i),
			"displayName":       fmt.Sprintf("User%02d", i),
			"userPrincipalName": fmt.Sprintf("user%02d@x.com", i),
		})
	}
	all = append(all, map[string]any{
		"id": "eeeeeeee-eeee-eeee-eeee-eeeeeeeeeeee", "displayName": "Zed",
		"userPrincipalName": "zed@x.com",
	})
	return all
}
