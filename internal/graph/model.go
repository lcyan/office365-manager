package graph

import "strings"

// User 是 Graph user 对象的应用内子集。字段用值而非指针：
// Graph 省略的字段解码为零值，模板按零值渲染"—"。
type User struct {
	ID                   string `json:"id"`
	DisplayName          string `json:"displayName"`
	UserPrincipalName    string `json:"userPrincipalName"`
	GivenName            string `json:"givenName"`
	Surname              string `json:"surname"`
	Mail                 string `json:"mail"`
	AccountEnabled       bool   `json:"accountEnabled"`
	LastPasswordChangeAt string `json:"lastPasswordChangeDateTime"`
	CreatedAt            string `json:"createdDateTime"`
	UsageLocation        string `json:"usageLocation"`
	UserType             string `json:"userType"`
}

// PasswordProfile 是创建用户时的初始密码设置。
type PasswordProfile struct {
	Password                      string `json:"password"`
	ForceChangePasswordNextSignIn bool   `json:"forceChangePasswordNextSignIn"`
}

// NewUserPayload 是 POST /users 的请求体。
type NewUserPayload struct {
	AccountEnabled    bool            `json:"accountEnabled"`
	UserType          string          `json:"userType,omitempty"`
	DisplayName       string          `json:"displayName"`
	MailNickname      string          `json:"mailNickname"`
	UserPrincipalName string          `json:"userPrincipalName"`
	UsageLocation     string          `json:"usageLocation,omitempty"`
	PasswordProfile   PasswordProfile `json:"passwordProfile"`
}

// SubscribedSku 是租户已订阅的 SKU。
type SubscribedSku struct {
	SkuID            string `json:"skuId"`
	SkuPartNumber    string `json:"skuPartNumber"`
	CapabilityStatus string `json:"capabilityStatus"`
	ConsumedUnits    int    `json:"consumedUnits"`
	PrepaidUnits     struct {
		Enabled   int `json:"enabled"`
		Warning   int `json:"warning"`
		Suspended int `json:"suspended"`
	} `json:"prepaidUnits"`
}

// LicenseDetail 是用户已分配的许可。
type LicenseDetail struct {
	SkuID         string `json:"skuId"`
	SkuPartNumber string `json:"skuPartNumber"`
}

// AssignLicensePayload 是 POST /users/{id}/assignLicense 的请求体。
type AssignLicensePayload struct {
	AddLicenses    []AddLicense `json:"addLicenses"`
	RemoveLicenses []string     `json:"removeLicenses"`
}

// AddLicense 指定要添加的 SKU（不禁用任何服务计划）。
type AddLicense struct {
	SkuID         string   `json:"skuId"`
	DisabledPlans []string `json:"disabledPlans"`
}

// Subscription 是 /directory/subscriptions 的商务订阅信息。
type Subscription struct {
	SkuID                 string `json:"skuId"`
	Status                string `json:"status"`
	NextLifecycleDateTime string `json:"nextLifecycleDateTime"`
}

// RoleAssignment 是目录角色分配。Principal 通过 $expand 填充，
// 可能是用户、组或服务主体。
type RoleAssignment struct {
	ID               string     `json:"id"`
	RoleDefinitionID string     `json:"roleDefinitionId"`
	PrincipalID      string     `json:"principalId"`
	Principal        *Principal `json:"principal,omitempty"`
}

// Principal 是 $expand=principal 返回的目录对象。
type Principal struct {
	ID                string `json:"id"`
	DisplayName       string `json:"displayName"`
	ODataType         string `json:"@odata.type"`
	UserPrincipalName string `json:"userPrincipalName"`
	GivenName         string `json:"givenName"`
	Surname           string `json:"surname"`
}

// IsUser 报告主体是否为用户对象。
func (p *Principal) IsUser() bool {
	return p != nil && strings.Contains(p.ODataType, "graph.user")
}

// IsGroup 报告主体是否为组对象。
func (p *Principal) IsGroup() bool {
	return p != nil && strings.Contains(p.ODataType, "graph.group")
}

// PrincipalKind 返回用于展示的主体类型（user/group/servicePrincipal/unknown）。
func (p *Principal) PrincipalKind() string {
	switch {
	case p == nil:
		return "unknown"
	case p.IsUser():
		return "user"
	case p.IsGroup():
		return "group"
	case strings.Contains(p.ODataType, "graph.servicePrincipal"):
		return "servicePrincipal"
	default:
		return "unknown"
	}
}

// Domain 是租户域名。
type Domain struct {
	ID         string `json:"id"`
	IsDefault  bool   `json:"isDefault"`
	IsVerified bool   `json:"isVerified"`
	State      string `json:"state"`
}

// IsAvailable 报告域名是否可用于创建用户（已验证）。
func (d Domain) IsAvailable() bool {
	return d.IsVerified || d.State == "Verified"
}

// OrganizationInfo 是 /organization 返回的租户信息子集。
type OrganizationInfo struct {
	ID                string `json:"id"`
	DisplayName       string `json:"displayName"`
	CountryLetterCode string `json:"countryLetterCode"`
}
