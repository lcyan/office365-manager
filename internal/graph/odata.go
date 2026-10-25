package graph

import (
	"regexp"
	"strings"
)

// Quote 对 OData $filter 中的字符串字面量转义（单引号翻倍）。
// 所有进入 $filter 的用户输入必须经过它。
func Quote(s string) string {
	return strings.ReplaceAll(s, "'", "''")
}

// guidRe 匹配标准 GUID。Graph 的 user/roleAssignment ID 都是 GUID；
// 拒绝其它形态可同时防御路径注入与 OData 注入。
var guidRe = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// ValidID 报告 s 是否为 GUID 形态的对象 ID。
func ValidID(s string) bool {
	return guidRe.MatchString(s)
}

// userSearchFilter 构造按显示名/UPN/名/姓前缀搜索的 $filter。
func userSearchFilter(search string) string {
	q := Quote(search)
	return "startswith(displayName,'" + q + "') or startswith(userPrincipalName,'" + q + "') " +
		"or startswith(givenName,'" + q + "') or startswith(surname,'" + q + "')"
}
