package web

import (
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
)

// TestEndToEndJourney 以一名管理员的身份完整走一遍核心流程：
// 登录 → 建组织 → 选组织 → 概况 → 列表 → 详情 → 改角色 → 分配订阅 →
// 新建用户 → 一次性凭据 → 删除用户 → 登出。伪 Graph 提供全部后端。
func TestEndToEndJourney(t *testing.T) {
	fail := false
	g := &actionGraph{}
	var graphCalls atomic.Int32
	ts, store, _ := newTestServerOpts(t, serverOpts{
		password: "secret-pass",
		graph: func(w http.ResponseWriter, r *http.Request) bool {
			graphCalls.Add(1) // 概况页会并行发请求，必须原子计数
			// 用户/概况读取走 userFlowGraph；写操作与角色分配走 actionGraph。
			readOnlyList := r.URL.Path == "/users" && r.Method == "GET"
			if !readOnlyList && g.handler()(w, r) {
				return true
			}
			return userFlowGraph(&fail, nil)(w, r)
		},
	})

	client := loginAs(t, ts, "secret-pass")

	// 组织管理：添加 → 出现在列表。
	addOrgViaUI(t, client, ts.URL, "旅程租户")
	orgs := store.Orgs()
	if len(orgs) != 1 || orgs[0].Name != "旅程租户" {
		t.Fatalf("org not added: %+v", orgs)
	}
	orgID := orgs[0].ID

	// 选择组织 → 概况渲染。
	client.Get(ts.URL + "/select_org/" + orgID)
	page, _ := getBody(t, client, ts.URL+"/users")
	if !strings.Contains(page, "组织概况") || !strings.Contains(page, "Microsoft 365 E3") {
		t.Fatalf("summary not rendered for new org")
	}

	// 用户列表 → 详情。
	page, _ = getBody(t, client, ts.URL+"/users?view=users")
	if !strings.Contains(page, "Alice") {
		t.Fatal("user list missing Alice")
	}
	page, _ = getBody(t, client, ts.URL+"/user/"+userID)
	if !strings.Contains(page, "alice@x.com") {
		t.Fatal("user detail missing UPN")
	}

	// 修改角色为普通用户 → 移除两条分配。
	resp, err := client.PostForm(ts.URL+"/user/"+userID+"/update", url.Values{
		"csrf_token":      {csrfFrom(t, page)},
		"account_enabled": {"true"},
		"role":            {"normal"},
	})
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if g.count("DELETE assignment") != 2 {
		t.Fatalf("role change did not remove assignments: %v", g.calls)
	}

	// 分配订阅页 → 取消全部勾选提交 → 移除许可。
	page, _ = getBody(t, client, ts.URL+"/user/"+userID+"/license")
	resp, err = client.PostForm(ts.URL+"/user/"+userID+"/license",
		url.Values{"csrf_token": {csrfFrom(t, page)}})
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if !g.has("POST assignLicense") {
		t.Fatal("license removal not submitted")
	}

	// 新建用户 → 一次性凭据页。
	page, _ = getBody(t, client, ts.URL+"/users/add")
	resp, err = client.PostForm(ts.URL+"/users/add", url.Values{
		"csrf_token":   {csrfFrom(t, page)},
		"display_name": {"旅程用户"},
		"local_part":   {"journey"},
		"domain":       {"contoso.com"},
		"role":         {"normal"},
	})
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	creds, _ := getBody(t, client, ts.URL+"/user/"+userID+"/created")
	if !strings.Contains(creds, "journey@contoso.com") {
		t.Fatalf("credentials page missing new UPN:\n%.500s", creds)
	}

	// 删除用户 → 成功（不触碰角色）。
	page, _ = getBody(t, client, ts.URL+"/user/"+userID+"/delete")
	resp, err = client.PostForm(ts.URL+"/user/"+userID+"/delete",
		url.Values{"csrf_token": {csrfFrom(t, page)}})
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	// 登出 → 受保护页面重定向登录。
	page, _ = getBody(t, client, ts.URL+"/dashboard")
	resp, err = client.PostForm(ts.URL+"/logout", url.Values{"csrf_token": {csrfFrom(t, page)}})
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	_, resp2 := getBody(t, client, ts.URL+"/dashboard")
	if resp2.Header.Get("Location") != "/login" {
		t.Fatalf("after logout dashboard redirects to %q", resp2.Header.Get("Location"))
	}

	if graphCalls.Load() < 10 {
		t.Logf("warning: journey made only %d graph calls", graphCalls.Load())
	}
}
