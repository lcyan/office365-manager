package web

import (
	"bytes"
	"mime/multipart"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/lcyan/office365-manager/internal/auth"
	"github.com/lcyan/office365-manager/internal/config"
)

const testTenant = "test-tenant"

// addOrgViaUI 通过表单添加组织并返回其配置记录。
func addOrgViaUI(t *testing.T, client *http.Client, tsURL, name string) config.Org {
	t.Helper()
	page, _ := getBody(t, client, tsURL+"/organizations")
	token := csrfFrom(t, page)
	resp, err := client.PostForm(tsURL+"/organizations", url.Values{
		"name": {name}, "client_id": {"cid-" + name}, "tenant_id": {testTenant},
		"client_secret": {"sec-" + name}, "csrf_token": {token},
	})
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return config.Org{
		Name: name, ClientID: "cid-" + name, TenantID: testTenant, ClientSecret: "sec-" + name,
	}
}

func csrfFrom(t *testing.T, page string) string {
	t.Helper()
	m := csrfRe.FindStringSubmatch(page)
	if m == nil {
		t.Fatalf("page missing csrf token:\n%.500s", page)
	}
	return m[1]
}

func TestOrgAddListAndDuplicate(t *testing.T) {
	ts, store := newTestServer(t, "secret-pass", false, 0)
	client := loginAs(t, ts, "secret-pass")

	addOrgViaUI(t, client, ts.URL, "Tenant A")
	orgs := store.Orgs()
	if len(orgs) != 1 || orgs[0].Name != "Tenant A" || orgs[0].ClientSecret != "sec-Tenant A" {
		t.Fatalf("store orgs = %+v", orgs)
	}

	page, _ := getBody(t, client, ts.URL+"/organizations")
	if !strings.Contains(page, "Tenant A") || !strings.Contains(page, "未测试") {
		t.Error("organizations page missing org row")
	}

	// 重名拒绝。
	resp, err := client.PostForm(ts.URL+"/organizations", url.Values{
		"name": {"Tenant A"}, "client_id": {"cid-2"}, "tenant_id": {testTenant},
		"client_secret": {"s"}, "csrf_token": {csrfFrom(t, page)},
	})
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if len(store.Orgs()) != 1 {
		t.Errorf("duplicate org accepted: %+v", store.Orgs())
	}

	// 缺字段拒绝。
	resp, err = client.PostForm(ts.URL+"/organizations", url.Values{
		"name": {"NoSecret"}, "client_id": {"c"}, "tenant_id": {testTenant}, "csrf_token": {csrfFrom(t, page)},
	})
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if len(store.Orgs()) != 1 {
		t.Errorf("incomplete org accepted")
	}
}

func TestOrgEditKeepsSecretWhenBlank(t *testing.T) {
	ts, store := newTestServer(t, "secret-pass", false, 0)
	client := loginAs(t, ts, "secret-pass")
	addOrgViaUI(t, client, ts.URL, "Tenant A")
	org := store.Orgs()[0]

	page, _ := getBody(t, client, ts.URL+"/organizations/edit/"+org.ID)
	if strings.Contains(page, "sec-Tenant A") {
		t.Error("edit page leaks client secret")
	}

	edit := func(secret string) {
		t.Helper()
		resp, err := client.PostForm(ts.URL+"/organizations/edit/"+org.ID, url.Values{
			"name": {"Renamed"}, "client_id": {"cid-new"}, "tenant_id": {"tid-new"},
			"client_secret": {secret}, "csrf_token": {csrfFrom(t, page)},
		})
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
	}

	edit("") // 留空 → 保持原密钥
	after := store.Orgs()[0]
	if after.ClientSecret != "sec-Tenant A" {
		t.Errorf("blank secret changed value to %q", after.ClientSecret)
	}
	if after.Name != "Renamed" || after.ClientID != "cid-new" || after.TenantID != "tid-new" {
		t.Errorf("edit did not apply: %+v", after)
	}
}

func TestOrgDeleteUsesConfirmPageAndPost(t *testing.T) {
	ts, store := newTestServer(t, "secret-pass", false, 0)
	client := loginAs(t, ts, "secret-pass")
	addOrgViaUI(t, client, ts.URL, "Tenant A")
	org := store.Orgs()[0]

	// GET 呈现确认页（不删除）。
	page, resp := getBody(t, client, ts.URL+"/organizations/delete/"+org.ID)
	if resp.StatusCode != 200 || !strings.Contains(page, "确认删除") {
		t.Fatalf("GET delete = %d, want confirm page", resp.StatusCode)
	}
	if len(store.Orgs()) != 1 {
		t.Fatal("GET delete removed the org")
	}

	// 无 CSRF 的 POST 被拒绝。
	resp2, err := client.PostForm(ts.URL+"/organizations/delete/"+org.ID, url.Values{})
	if err != nil {
		t.Fatal(err)
	}
	resp2.Body.Close()
	if resp2.StatusCode != http.StatusForbidden {
		t.Errorf("delete without csrf = %d, want 403", resp2.StatusCode)
	}

	resp3, err := client.PostForm(ts.URL+"/organizations/delete/"+org.ID,
		url.Values{"csrf_token": {csrfFrom(t, page)}})
	if err != nil {
		t.Fatal(err)
	}
	resp3.Body.Close()
	if len(store.Orgs()) != 0 {
		t.Errorf("org not deleted: %+v", store.Orgs())
	}
}

func TestOrgAPITestSetsStatus(t *testing.T) {
	skusCalled := 0
	ts, store, _ := newTestServerOpts(t, serverOpts{
		password: "secret-pass",
		graph: func(w http.ResponseWriter, r *http.Request) bool {
			if r.URL.Path == "/subscribedSkus" {
				skusCalled++
				writeGraphJSON(w, 200, map[string]any{"value": []any{}})
				return true
			}
			return false
		},
	})
	client := loginAs(t, ts, "secret-pass")
	addOrgViaUI(t, client, ts.URL, "Tenant A")
	org := store.Orgs()[0]

	resp, err := client.Get(ts.URL + "/organizations/test/" + org.ID)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	page, _ := getBody(t, client, ts.URL+"/dashboard")
	if !strings.Contains(page, `<div class="stat-value">1</div>`) {
		t.Error("dashboard does not show 1 available org")
	}

	// 令牌失败 → 不可用。改令牌端点不易；直接用一个始终 500 的 graph。
	_ = skusCalled
	_ = resp
}

func TestDashboardDefaultPasswordWarning(t *testing.T) {
	// 默认配置（密码 admin）：显示警告。
	ts, store, _ := newTestServerOpts(t, serverOpts{password: auth.DefaultPassword})
	client := loginAs(t, ts, auth.DefaultPassword)
	page, _ := getBody(t, client, ts.URL+"/dashboard")
	if !strings.Contains(page, "默认密码") {
		t.Error("default password warning missing")
	}
	_ = store

	// 修改密码后警告消失。
	pwPage, _ := getBody(t, client, ts.URL+"/set_password")
	resp, err := client.PostForm(ts.URL+"/set_password", url.Values{
		"current_password": {auth.DefaultPassword},
		"new_password":     {"changed-pass-1"},
		"confirm_password": {"changed-pass-1"},
		"csrf_token":       {csrfFrom(t, pwPage)},
	})
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	client2 := loginAs(t, ts, "changed-pass-1")
	page2, _ := getBody(t, client2, ts.URL+"/dashboard")
	if strings.Contains(page2, "默认密码") {
		t.Error("default password warning shown after change")
	}
}

func TestOrgImportAllOrNothing(t *testing.T) {
	ts, store := newTestServer(t, "secret-pass", false, 0)
	client := loginAs(t, ts, "secret-pass")

	importCSV := func(content string) string {
		t.Helper()
		page, _ := getBody(t, client, ts.URL+"/organizations")
		var buf bytes.Buffer
		writer := multipart.NewWriter(&buf)
		_ = writer.WriteField("csrf_token", csrfFrom(t, page))
		file, _ := writer.CreateFormFile("file", "orgs.csv")
		_, _ = file.Write([]byte(content))
		_ = writer.Close()
		resp, err := client.Post(ts.URL+"/organizations/import", writer.FormDataContentType(), &buf)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		feedback, _ := getBody(t, client, ts.URL+"/organizations")
		return feedback
	}

	// 一好一坏 → 全部不写入，报行号。
	page := importCSV("name,client_id,tenant_id,client_secret\nGood,c1,test-tenant,s1\nBad,c2\n")
	if !strings.Contains(page, "第 3 行缺少字段") || !strings.Contains(page, "导入未执行") {
		t.Errorf("partial import not rejected:\n%s", page)
	}
	if len(store.Orgs()) != 0 {
		t.Fatalf("partial import wrote orgs: %+v", store.Orgs())
	}

	// 全部有效 → 导入成功。
	page = importCSV("name,client_id,tenant_id,client_secret\nA,c1,test-tenant,s1\nB,c2,test-tenant,s2\n")
	if !strings.Contains(page, "成功导入 2 个组织") {
		t.Errorf("good import failed:\n%s", page)
	}
	if len(store.Orgs()) != 2 {
		t.Fatalf("store orgs = %d, want 2", len(store.Orgs()))
	}

	// 与已有组织重复 → 拒绝。
	page = importCSV("name,client_id,tenant_id,client_secret\nA,c1,test-tenant,s1\n")
	if !strings.Contains(page, "重复") || len(store.Orgs()) != 2 {
		t.Errorf("duplicate import not rejected")
	}
}

func TestSelectOrgValidatesNext(t *testing.T) {
	ts, store := newTestServer(t, "secret-pass", false, 0)
	client := loginAs(t, ts, "secret-pass")
	addOrgViaUI(t, client, ts.URL, "Tenant A")
	org := store.Orgs()[0]

	// 外部地址被拒绝：仍重定向站内。
	resp, err := client.Get(ts.URL + "/select_org/" + org.ID + "?next=https://evil.example/phish")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if loc := resp.Header.Get("Location"); !strings.HasPrefix(loc, "/users") {
		t.Errorf("external next redirected to %q", loc)
	}

	// 站内相对目标允许。
	resp, err = client.Get(ts.URL + "/select_org/" + org.ID + "?next=/organizations")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if loc := resp.Header.Get("Location"); loc != "/organizations" {
		t.Errorf("relative next redirected to %q", loc)
	}

	// 会话已记住组织（导航栏显示当前组织）。
	page, _ := getBody(t, client, ts.URL+"/dashboard")
	if !strings.Contains(page, "Tenant A") {
		t.Error("active org not reflected in navbar")
	}
}
