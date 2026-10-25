package graph

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/lcyan/office365-manager/internal/config"
)

const (
	testOrgID = "11111111-1111-1111-1111-111111111111"
	testUserA = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
	testUserB = "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"
	roleGA    = "62e90394-69f5-4237-9190-012177145e10" // Global Administrator 模板 ID
)

func testOrg() config.Org {
	return config.Org{
		ID: testOrgID, Name: "Test", ClientID: "cid", TenantID: "tid", ClientSecret: "sec",
	}
}

type fakeGraph struct {
	mu         sync.Mutex
	tokenCalls int
	calls      []string // "METHOD path" 记录
	handler    func(w http.ResponseWriter, r *http.Request) bool
}

// newFakeGraph 启动同时承载登录与 Graph 端点的伪服务器。
func newFakeGraph(t *testing.T, handler func(w http.ResponseWriter, r *http.Request) bool) (*fakeGraph, *Client) {
	t.Helper()
	fg := &fakeGraph{handler: handler}
	mux := http.NewServeMux()
	mux.HandleFunc("/tid/oauth2/v2.0/token", func(w http.ResponseWriter, r *http.Request) {
		fg.mu.Lock()
		fg.tokenCalls++
		fg.mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "tok", "expires_in": 3600})
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		fg.mu.Lock()
		fg.calls = append(fg.calls, r.Method+" "+r.URL.Path)
		fg.mu.Unlock()
		if handler == nil || !handler(w, r) {
			http.Error(w, `{"error":{"code":"NotFound","message":"no route"}}`, http.StatusNotFound)
		}
	})
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)

	client := New(slog.New(slog.NewTextHandler(io.Discard, nil)))
	client.SetHTTPClient(&http.Client{Transport: ts.Client().Transport})
	client.SetBaseURL(ts.URL, ts.URL)
	return fg, client
}

func (fg *fakeGraph) recorded() []string {
	fg.mu.Lock()
	defer fg.mu.Unlock()
	return append([]string(nil), fg.calls...)
}

func writeJSON(t *testing.T, w http.ResponseWriter, status int, v any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func TestTokenCachedAcrossRequests(t *testing.T) {
	fg, client := newFakeGraph(t, func(w http.ResponseWriter, r *http.Request) bool {
		writeJSON(t, w, 200, map[string]any{"value": []any{}})
		return true
	})
	ctx := context.Background()
	org := testOrg()

	for i := 0; i < 3; i++ {
		if _, err := client.SubscribedSkus(ctx, org); err != nil {
			t.Fatalf("SubscribedSkus: %v", err)
		}
	}
	if fg.tokenCalls != 1 {
		t.Errorf("token calls = %d, want 1", fg.tokenCalls)
	}
	client.InvalidateOrg(org.ID)
	if _, err := client.SubscribedSkus(ctx, org); err != nil {
		t.Fatal(err)
	}
	if fg.tokenCalls != 2 {
		t.Errorf("token calls after invalidate = %d, want 2", fg.tokenCalls)
	}
}

func TestUnauthorizedRetriesOnceWithNewToken(t *testing.T) {
	authCount := 0
	fg, client := newFakeGraph(t, func(w http.ResponseWriter, r *http.Request) bool {
		if r.Header.Get("Authorization") == "Bearer tok" && authCount == 0 {
			authCount++
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":{"code":"InvalidAuthenticationToken","message":"token expired"}}`))
			return true
		}
		writeJSON(t, w, 200, map[string]any{"value": []any{}})
		return true
	})
	if _, err := client.SubscribedSkus(context.Background(), testOrg()); err != nil {
		t.Fatalf("expected retry to succeed: %v", err)
	}
	if fg.tokenCalls != 2 {
		t.Errorf("token calls = %d, want 2 (refresh after 401)", fg.tokenCalls)
	}
}

func TestErrorContract(t *testing.T) {
	_, client := newFakeGraph(t, func(w http.ResponseWriter, r *http.Request) bool {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":{"code":"Authorization_RequestDenied","message":"Insufficient privileges to complete the operation."}}`))
		return true
	})
	_, err := client.SubscribedSkus(context.Background(), testOrg())

	var gerr *Error
	if !asError(err, &gerr) {
		t.Fatalf("error type = %T, want *graph.Error", err)
	}
	if gerr.HTTPStatus != http.StatusForbidden || gerr.Code != "Authorization_RequestDenied" {
		t.Errorf("gerr = %+v", gerr)
	}
	if !IsInsufficientPrivileges(err) {
		t.Error("IsInsufficientPrivileges = false")
	}
	if IsNotFound(err) {
		t.Error("IsNotFound = true for 403")
	}
	if !strings.Contains(err.Error(), "Authorization_RequestDenied") {
		t.Errorf("message lacks code: %v", err)
	}
}

func asError(err error, target **Error) bool {
	if e, ok := err.(*Error); ok {
		*target = e
		return true
	}
	return false
}

func TestTokenFailureCarriesAADSTS(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{
			"error":             "invalid_client",
			"error_description": "AADSTS7000215: Invalid client secret provided. Trace ID: secret-trace",
		})
	}))
	// 非 200：状态码补上
	ts2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"invalid_client","error_description":"AADSTS7000215: Invalid client secret. Trace: xyz"}`))
	}))
	defer ts.Close()
	defer ts2.Close()

	client := New(slog.New(slog.NewTextHandler(io.Discard, nil)))
	client.SetHTTPClient(&http.Client{Transport: ts2.Client().Transport})
	client.SetBaseURL("http://unused", ts2.URL)

	_, err := client.SubscribedSkus(context.Background(), testOrg())
	if err == nil || !strings.Contains(err.Error(), "AADSTS7000215") {
		t.Fatalf("err = %v, want AADSTS7000215", err)
	}
	if strings.Contains(err.Error(), "xyz") {
		t.Error("trace id leaked into error")
	}
}

func TestListUsersPaginationAndCount(t *testing.T) {
	fg, client := newFakeGraph(t, func(w http.ResponseWriter, r *http.Request) bool {
		if r.URL.Path != "/users" {
			return false
		}
		q := r.URL.Query()
		if q.Get("$count") != "true" || q.Get("$top") != "50" || q.Get("$skip") != "50" {
			t.Errorf("query = %s", q)
		}
		if r.Header.Get("ConsistencyLevel") != "eventual" {
			t.Error("missing ConsistencyLevel header")
		}
		writeJSON(t, w, 200, map[string]any{
			"value":        []map[string]any{{"id": testUserB, "displayName": "B"}},
			"@odata.count": 120,
		})
		return true
	})
	result, err := client.ListUsers(context.Background(), testOrg(), ListUsersParams{Top: 50, Skip: 50})
	if err != nil {
		t.Fatal(err)
	}
	if result.Total != 120 || len(result.Users) != 1 || result.Users[0].ID != testUserB {
		t.Errorf("result = %+v", result)
	}
	_ = fg
}

func TestListUsersSearchFilterEscaped(t *testing.T) {
	_, client := newFakeGraph(t, func(w http.ResponseWriter, r *http.Request) bool {
		filter := r.URL.Query().Get("$filter")
		// 单引号必须翻倍，否则 OData 语法被破坏 / 可注入。
		if !strings.Contains(filter, "startswith(displayName,'o''brien')") {
			t.Errorf("filter not escaped: %s", filter)
		}
		writeJSON(t, w, 200, map[string]any{"value": []any{}, "@odata.count": 0})
		return true
	})
	_, err := client.ListUsers(context.Background(), testOrg(), ListUsersParams{Search: "o'brien", Top: 50})
	if err != nil {
		t.Fatal(err)
	}
}

func TestRoleAssignmentsFollowNextLink(t *testing.T) {
	// 两页响应：首页带 nextLink（指向同一测试服务器），第二页无 nextLink。
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		switch {
		case strings.HasSuffix(req.URL.Path, "/token"):
			_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "tok", "expires_in": 3600})
		case req.URL.Query().Get("$skipToken") != "":
			_, _ = w.Write([]byte(`{"value":[{"id":"a2","principalId":"` + testUserB + `"}]}`))
		default:
			next := "http://" + req.Host + "/v1.0/roleManagement/directory/roleAssignments?$top=999&$skipToken=abc"
			_, _ = fmt.Fprintf(w, `{"value":[{"id":"a1","principalId":"%s"}],"@odata.nextLink":"%s"}`, testUserA, next)
		}
	}))
	defer server.Close()
	client := New(slog.New(slog.NewTextHandler(io.Discard, nil)))
	client.SetHTTPClient(server.Client())
	client.SetBaseURL(server.URL, server.URL)

	items, err := client.RoleAssignments(context.Background(), testOrg(), "", false)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 || items[0].ID != "a1" || items[1].ID != "a2" {
		t.Errorf("items = %+v, want both pages", items)
	}
}

func TestRoleDefinitionIDCached(t *testing.T) {
	lookups := 0
	_, client := newFakeGraph(t, func(w http.ResponseWriter, r *http.Request) bool {
		if r.URL.Path != "/roleManagement/directory/roleDefinitions" {
			return false
		}
		lookups++
		writeJSON(t, w, 200, map[string]any{"value": []map[string]any{{"id": roleGA}}})
		return true
	})
	ctx := context.Background()
	org := testOrg()
	for i := 0; i < 3; i++ {
		id, err := client.RoleDefinitionID(ctx, org, "Global Administrator")
		if err != nil || id != roleGA {
			t.Fatalf("RoleDefinitionID = %q, %v", id, err)
		}
	}
	if lookups != 1 {
		t.Errorf("lookups = %d, want 1 (cached)", lookups)
	}
	client.InvalidateOrg(org.ID)
	if _, err := client.RoleDefinitionID(ctx, org, "Global Administrator"); err != nil {
		t.Fatal(err)
	}
	if lookups != 2 {
		t.Errorf("lookups after invalidate = %d, want 2", lookups)
	}
}

func TestInvalidIDsRejectedBeforeNetwork(t *testing.T) {
	fg, client := newFakeGraph(t, func(w http.ResponseWriter, r *http.Request) bool {
		t.Errorf("network reached: %s %s", r.Method, r.URL.Path)
		return false
	})
	ctx := context.Background()
	org := testOrg()
	// OData 注入形态：带引号/空格的 user_id。
	injected := "x' or principalId eq '" + testUserB
	if _, err := client.GetUser(ctx, org, injected, false); err == nil {
		t.Error("GetUser accepted injected ID")
	}
	if _, err := client.RoleAssignmentsByPrincipal(ctx, org, injected); err == nil {
		t.Error("RoleAssignmentsByPrincipal accepted injected ID")
	}
	if err := client.DeleteUser(ctx, org, "../users"); err == nil {
		t.Error("DeleteUser accepted path traversal")
	}
	if calls := fg.recorded(); len(calls) != 0 {
		t.Errorf("unexpected network calls: %v", calls)
	}
}

func TestQuote(t *testing.T) {
	cases := map[string]string{
		"plain":   "plain",
		"o'brien": "o''brien",
		"'":       "''",
		"":        "",
	}
	for in, want := range cases {
		if got := Quote(in); got != want {
			t.Errorf("Quote(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestValidID(t *testing.T) {
	valid := []string{testUserA, strings.ToUpper(testUserA)}
	invalid := []string{"", "abc", "x' or 1 eq 1", "../users", testUserA + "'"}
	for _, id := range valid {
		if !ValidID(id) {
			t.Errorf("ValidID(%q) = false", id)
		}
	}
	for _, id := range invalid {
		if ValidID(id) {
			t.Errorf("ValidID(%q) = true", id)
		}
	}
}

func TestUpdateUserPayloadShape(t *testing.T) {
	var captured []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		switch {
		case strings.HasSuffix(req.URL.Path, "/token"):
			_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "tok", "expires_in": 3600})
		case req.Method == http.MethodPatch:
			captured, _ = io.ReadAll(req.Body)
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected call %s %s", req.Method, req.URL.Path)
		}
	}))
	defer server.Close()
	client := New(slog.New(slog.NewTextHandler(io.Discard, nil)))
	client.SetHTTPClient(server.Client())
	client.SetBaseURL(server.URL, server.URL)

	enabled := true
	pw := "NewP@ssw0rd"
	if err := client.UpdateUser(context.Background(), testOrg(), testUserA,
		UserPatch{Password: &pw, AccountEnabled: &enabled}); err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	if err := json.Unmarshal(captured, &payload); err != nil {
		t.Fatal(err)
	}
	profile, _ := payload["passwordProfile"].(map[string]any)
	if profile == nil || profile["password"] != pw || profile["forceChangePasswordNextSignIn"] != false {
		t.Errorf("passwordProfile = %v", profile)
	}
	if payload["accountEnabled"] != true {
		t.Errorf("accountEnabled = %v", payload["accountEnabled"])
	}
}
