package licenses

import "testing"

func TestProductNames(t *testing.T) {
	c := NewCatalog(nil)
	cases := map[string]string{
		"ENTERPRISEPACK":       "Office 365 企业版 E3",
		"SPE_F1":               "Microsoft 365 F1",
		"SOME_FUTURE_SKU_NAME": "Some Future Sku Name",
		"":                     "",
	}
	for sku, want := range cases {
		if got := c.Product(sku); got != want {
			t.Errorf("Product(%q) = %q, want %q", sku, got, want)
		}
	}
}

func TestExternalOverride(t *testing.T) {
	c := NewCatalog([]byte(`{"ENTERPRISEPACK":"自定义名称","MY_SKU":"我的产品"}`))
	if got := c.Product("ENTERPRISEPACK"); got != "自定义名称" {
		t.Errorf("override not applied: %q", got)
	}
	if got := c.Product("MY_SKU"); got != "我的产品" {
		t.Errorf("new entry missing: %q", got)
	}
	// 无效 JSON 静默忽略，内置映射仍可用。
	bad := NewCatalog([]byte("{broken"))
	if got := bad.Product("ENTERPRISEPACK"); got != "Office 365 企业版 E3" {
		t.Errorf("builtin lost on bad json: %q", got)
	}
}

func TestStatusLabel(t *testing.T) {
	cases := []struct {
		in    string
		label string
		badge string
	}{
		{"Enabled", "活动", "badge-success"},
		{"Suspended", "已禁用", "badge-destructive"},
		{"", "未知", "badge-outline"},
		{"BrandNew", "BrandNew", "badge-outline"},
	}
	for _, tc := range cases {
		label, badge := StatusLabel(tc.in)
		if label != tc.label || badge != tc.badge {
			t.Errorf("StatusLabel(%q) = (%q,%q), want (%q,%q)", tc.in, label, badge, tc.label, tc.badge)
		}
	}
}
