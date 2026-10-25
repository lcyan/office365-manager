package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

const testHash = "$argon2id$v=19$m=65536,t=1,p=4$c2FsdA$aGFzaA"

func org(id, name string) Org {
	return Org{ID: id, Name: name, ClientID: "cid-" + id, TenantID: "tid-" + id, ClientSecret: "sec-" + id}
}

func TestLoadCreatesDefaultWhenMissing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "config.json")
	s, err := Load(path, testHash)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := s.PasswordHash(); got != testHash {
		t.Errorf("default hash = %q, want %q", got, testHash)
	}
	if orgs := s.Orgs(); len(orgs) != 0 {
		t.Errorf("default organizations = %v, want empty", orgs)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read created file: %v", err)
	}
	var data fileData
	if err := json.Unmarshal(raw, &data); err != nil {
		t.Fatalf("created file is not valid JSON: %v", err)
	}
	if data.Version == nil || *data.Version != Version {
		t.Errorf("created file version = %v, want %d", data.Version, Version)
	}
}

func TestRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	s, err := Load(path, testHash)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if err := s.AddOrg(org("org-a", "Tenant A")); err != nil {
		t.Fatalf("AddOrg: %v", err)
	}
	if err := s.SetPasswordHash("new-hash"); err != nil {
		t.Fatalf("SetPasswordHash: %v", err)
	}

	reloaded, err := Load(path, "unused-default")
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if got := reloaded.PasswordHash(); got != "new-hash" {
		t.Errorf("reloaded hash = %q, want new-hash", got)
	}
	orgs := reloaded.Orgs()
	if len(orgs) != 1 || orgs[0].ID != "org-a" || orgs[0].ClientSecret != "sec-org-a" {
		t.Errorf("reloaded orgs = %v", orgs)
	}
}

func TestAddOrgRejectsDuplicatesAndInvalid(t *testing.T) {
	s, err := Load(filepath.Join(t.TempDir(), "config.json"), testHash)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if err := s.AddOrg(org("org-a", "Tenant A")); err != nil {
		t.Fatalf("AddOrg: %v", err)
	}
	if err := s.AddOrg(org("org-b", "Tenant A")); err == nil || !strings.Contains(err.Error(), "已存在") {
		t.Errorf("duplicate name error = %v, want 已存在", err)
	}
	dupClient := org("org-b", "Tenant B")
	dupClient.ClientID = "cid-org-a"
	if err := s.AddOrg(dupClient); err == nil || !strings.Contains(err.Error(), "已存在") {
		t.Errorf("duplicate client_id error = %v, want 已存在", err)
	}
	bad := org("", "No ID")
	if err := s.AddOrg(bad); err == nil || !strings.Contains(err.Error(), "必填字段") {
		t.Errorf("invalid org error = %v, want 必填字段", err)
	}
	if orgs := s.Orgs(); len(orgs) != 1 {
		t.Errorf("organizations after failed adds = %d, want 1", len(orgs))
	}
}

func TestReplaceAndDeleteOrg(t *testing.T) {
	s, err := Load(filepath.Join(t.TempDir(), "config.json"), testHash)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if err := s.AddOrg(org("org-a", "Old Name")); err != nil {
		t.Fatalf("AddOrg: %v", err)
	}
	updated := org("ignored", "New Name")
	updated.ClientSecret = "new-secret"
	ok, err := s.ReplaceOrg("org-a", updated)
	if err != nil || !ok {
		t.Fatalf("ReplaceOrg: ok=%v err=%v", ok, err)
	}
	got, found := s.Org("org-a")
	if !found || got.Name != "New Name" || got.ClientSecret != "new-secret" || got.ID != "org-a" {
		t.Errorf("replaced org = %+v", got)
	}
	if ok, err := s.DeleteOrg("org-a"); err != nil || !ok {
		t.Fatalf("DeleteOrg: ok=%v err=%v", ok, err)
	}
	if ok, err := s.DeleteOrg("org-a"); err != nil || ok {
		t.Errorf("second DeleteOrg = ok:%v err:%v, want false nil", ok, err)
	}
	if orgs := s.Orgs(); len(orgs) != 0 {
		t.Errorf("orgs after delete = %v", orgs)
	}
}

func TestInvalidFilePreservedAndReported(t *testing.T) {
	cases := []struct {
		name, content string
		wantErr       string
	}{
		{"broken json", "{broken", "JSON 无效"},
		{"empty", "", "JSON 无效"},
		{"array", "[]", "JSON 无效"},
		{"no hash", `{"organizations":[]}`, "password_hash"},
		{"incomplete org", `{"password_hash":"x","organizations":[{"id":"1","name":"n"}]}`, "organizations"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.json")
			if err := os.WriteFile(path, []byte(tc.content), 0o600); err != nil {
				t.Fatal(err)
			}
			_, err := Load(path, testHash)
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("error = %v, want containing %q", err, tc.wantErr)
			}
			after, rerr := os.ReadFile(path)
			if rerr != nil || string(after) != tc.content {
				t.Fatalf("file changed on failed load: %q", after)
			}
		})
	}
}

func TestVersionTooNewRejected(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	content := `{"version":99,"password_hash":"x","organizations":[]}`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path, testHash); err == nil || !strings.Contains(err.Error(), "版本") {
		t.Fatalf("error = %v, want 版本错误", err)
	}
}

// TestLegacyFormatLoadedAndUpgraded 覆盖从旧版 Python 应用迁移：
// 无 version 字段 + 旧 SHA-256 哈希，加载成功且首次保存后升级为 version 2。
func TestLegacyFormatLoadedAndUpgraded(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	legacy := `{"password_hash":"` + strings.Repeat("ab", 32) + `","organizations":[` +
		`{"id":"org-a","name":"旧租户","client_id":"c","tenant_id":"t","client_secret":"s"}]}`
	if err := os.WriteFile(path, []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := Load(path, "unused")
	if err != nil {
		t.Fatalf("legacy Load: %v", err)
	}
	orgs := s.Orgs()
	if len(orgs) != 1 || orgs[0].Name != "旧租户" {
		t.Fatalf("legacy orgs = %v", orgs)
	}
	if err := s.SetPasswordHash("upgraded-hash"); err != nil {
		t.Fatalf("SetPasswordHash: %v", err)
	}
	raw, _ := os.ReadFile(path)
	if !strings.Contains(string(raw), `"version": 2`) {
		t.Errorf("saved file missing version 2:\n%s", raw)
	}
}

func TestSaveLeavesNoTempFiles(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	s, err := Load(path, testHash)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	for i := 0; i < 5; i++ {
		if err := s.AddOrg(org(string(rune('a'+i)), string(rune('A'+i)))); err != nil {
			t.Fatalf("AddOrg %d: %v", i, err)
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".config-") {
			t.Errorf("leftover temp file: %s", e.Name())
		}
	}
}

func TestWriteFileInPlaceOverwritesExisting(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte("old-content"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := writeFileInPlace(path, []byte("new-content")); err != nil {
		t.Fatalf("writeFileInPlace: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "new-content" {
		t.Errorf("content = %q, want new-content", got)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !info.Mode().IsRegular() {
		t.Errorf("target is no longer a regular file: %v", info.Mode())
	}
}

func TestConcurrentAccess(t *testing.T) {
	s, err := Load(filepath.Join(t.TempDir(), "config.json"), testHash)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_ = s.AddOrg(org(string(rune('a'+i)), strings.Repeat("n", i+1)))
			_ = s.Orgs()
			_, _ = s.Org("a")
			_ = s.PasswordHash()
		}(i)
	}
	wg.Wait()
	// 每个 AddOrg 字段都合法且互不重复，8 个全部成功。
	if orgs := s.Orgs(); len(orgs) != 8 {
		t.Errorf("orgs = %d, want 8", len(orgs))
	}
}
