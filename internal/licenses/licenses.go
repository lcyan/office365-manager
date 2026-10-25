// Package licenses 提供 SKU 到产品名的映射与订阅能力状态的中文标签。
package licenses

import (
	"encoding/json"
	"strings"
)

// builtinNames 是内置 SKU 映射；外部 sku_product_mapping.json 可覆盖或扩充。
var builtinNames = map[string]string{
	"ENTERPRISEPACK":           "Office 365 企业版 E3",
	"ENTERPRISEPREMIUM":        "Office 365 企业版 E5",
	"STANDARDPACK":             "Office 365 企业版 E1",
	"SPE_E3":                   "Microsoft 365 E3",
	"SPE_E5":                   "Microsoft 365 E5",
	"SPE_F3":                   "Microsoft 365 F3",
	"SPE_F1":                   "Microsoft 365 F1",
	"M365_BUSINESS_PREMIUM":    "Microsoft 365 商业高级版",
	"M365_BUSINESS_STANDARD":   "Microsoft 365 商业标准版",
	"M365_BUSINESS_BASIC":      "Microsoft 365 商业基础版",
	"O365_BUSINESS_PREMIUM":    "Office 365 商业高级版",
	"O365_BUSINESS_ESSENTIALS": "Office 365 商业基础版",
	"EMS":                      "企业移动性与安全性套件",
	"STANDARDWOFFPACK_STUDENT": "面向学生的 Office 365 A1",
	"STANDARDWOFFPACK_FACULTY": "面向教师的 Office 365 A1",
}

// Catalog 保存合并后的 SKU 名称映射。
type Catalog struct {
	names map[string]string
}

// NewCatalog 以内置映射为基础，合并 external JSON（"SKU":"产品名"）。
// external 为空或解析失败时静默使用内置映射。
func NewCatalog(external []byte) *Catalog {
	c := &Catalog{names: map[string]string{}}
	for k, v := range builtinNames {
		c.names[k] = v
	}
	if len(external) > 0 {
		var extra map[string]string
		if err := json.Unmarshal(external, &extra); err == nil {
			for k, v := range extra {
				if k != "" && v != "" {
					c.names[k] = v
				}
			}
		}
	}
	return c
}

// Product 返回 SKU 的产品名；未知 SKU 退化为下划线换空格 + 首字母大写。
func (c *Catalog) Product(skuPart string) string {
	if skuPart == "" {
		return ""
	}
	if name, ok := c.names[skuPart]; ok {
		return name
	}
	words := strings.Split(strings.ReplaceAll(skuPart, "_", " "), " ")
	for i, word := range words {
		if word == "" {
			continue
		}
		words[i] = strings.ToUpper(word[:1]) + strings.ToLower(word[1:])
	}
	return strings.Join(words, " ")
}

// statusMeta 是订阅能力状态的展示元数据。
type statusMeta struct {
	label string
	badge string // CSS badge 变体类名
}

var statusLabels = map[string]statusMeta{
	"Enabled":   {"活动", "badge-success"},
	"Warning":   {"警告", "badge-warning"},
	"Suspended": {"已禁用", "badge-destructive"},
	"Deleted":   {"已删除", "badge-destructive"},
	"LockedOut": {"已锁定", "badge-outline"},
	"Expired":   {"已过期", "badge-warning"},
	"Unknown":   {"未知", "badge-outline"},
}

// StatusLabel 把 capabilityStatus 翻译为中文标签与徽章变体。
// 未知状态原样展示（首字母大写处理仅对 ASCII）。
func StatusLabel(status string) (label, badge string) {
	if meta, ok := statusLabels[status]; ok {
		return meta.label, meta.badge
	}
	if status == "" {
		return "未知", "badge-outline"
	}
	return status, "badge-outline"
}
