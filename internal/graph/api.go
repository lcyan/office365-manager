package graph

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"

	"github.com/lcyan/office365-manager/internal/config"
)

const userSelect = "id,displayName,userPrincipalName,mail,givenName,surname"

// ListUsersResult 是一次分页用户查询的结果。Total 来自同一响应的
// @odata.count，与当前页数据始终一致。
type ListUsersResult struct {
	Users []User
	Total int
}

// ListUsersParams 控制用户列表查询。
type ListUsersParams struct {
	Search string // 可选；按显示名/UPN/名/姓前缀匹配
	Top    int    // 页大小
	Skip   int    // 偏移
}

// ListUsers 以 $count + $top/$skip 方式直达分页（ConsistencyLevel: eventual）。
func (c *Client) ListUsers(ctx context.Context, org config.Org, p ListUsersParams) (ListUsersResult, error) {
	params := url.Values{
		"$select": {userSelect},
		"$top":    {fmt.Sprint(p.Top)},
		"$skip":   {fmt.Sprint(p.Skip)},
		"$count":  {"true"},
	}
	if p.Search != "" {
		params.Set("$filter", userSearchFilter(p.Search))
	}
	users, total, err := getCollection[User](ctx, c, org, "/users", params,
		map[string]string{"ConsistencyLevel": "eventual"}, false)
	if err != nil {
		return ListUsersResult{}, err
	}
	result := ListUsersResult{Users: users, Total: 0}
	if total != nil {
		result.Total = *total
	}
	return result, nil
}

// GetUser 按 ID 获取单个用户（detail=true 时附带账户状态等字段）。
func (c *Client) GetUser(ctx context.Context, org config.Org, userID string, detail bool) (User, error) {
	if !ValidID(userID) {
		return User{}, &Error{Op: "users", HTTPStatus: http.StatusBadRequest, Message: "无效的用户 ID"}
	}
	select_ := userSelect
	if detail {
		select_ = userSelect + ",accountEnabled,lastPasswordChangeDateTime,createdDateTime"
	}
	body, err := c.do(ctx, org, request{
		method:   http.MethodGet,
		endpoint: "/users/" + userID,
		params:   url.Values{"$select": {select_}},
	})
	if err != nil {
		return User{}, err
	}
	var user User
	if err := unmarshalStrict(body, &user); err != nil {
		return User{}, err
	}
	return user, nil
}

// CreateUser 创建用户并返回带 ID 的完整对象。
func (c *Client) CreateUser(ctx context.Context, org config.Org, payload NewUserPayload) (User, error) {
	body, err := c.do(ctx, org, request{
		method:   http.MethodPost,
		endpoint: "/users",
		body:     payload,
	})
	if err != nil {
		return User{}, err
	}
	var user User
	if err := unmarshalStrict(body, &user); err != nil {
		return User{}, err
	}
	return user, nil
}

// UserPatch 是 PATCH /users/{id} 的可更新字段；nil 表示不修改。
type UserPatch struct {
	Password       *string
	AccountEnabled *bool
}

// UpdateUser 更新用户属性（204 无内容即成功）。
func (c *Client) UpdateUser(ctx context.Context, org config.Org, userID string, patch UserPatch) error {
	if !ValidID(userID) {
		return &Error{Op: "users", HTTPStatus: http.StatusBadRequest, Message: "无效的用户 ID"}
	}
	body := map[string]any{}
	if patch.Password != nil {
		body["passwordProfile"] = map[string]any{
			"password":                      *patch.Password,
			"forceChangePasswordNextSignIn": false,
		}
	}
	if patch.AccountEnabled != nil {
		body["accountEnabled"] = *patch.AccountEnabled
	}
	if len(body) == 0 {
		return nil
	}
	_, err := c.do(ctx, org, request{
		method:   http.MethodPatch,
		endpoint: "/users/" + userID,
		body:     body,
	})
	return err
}

// DeleteUser 删除用户。
func (c *Client) DeleteUser(ctx context.Context, org config.Org, userID string) error {
	if !ValidID(userID) {
		return &Error{Op: "users", HTTPStatus: http.StatusBadRequest, Message: "无效的用户 ID"}
	}
	_, err := c.do(ctx, org, request{
		method:   http.MethodDelete,
		endpoint: "/users/" + userID,
	})
	return err
}

// UserLicenseDetails 返回用户已分配的许可。
func (c *Client) UserLicenseDetails(ctx context.Context, org config.Org, userID string) ([]LicenseDetail, error) {
	if !ValidID(userID) {
		return nil, &Error{Op: "users", HTTPStatus: http.StatusBadRequest, Message: "无效的用户 ID"}
	}
	items, _, err := getCollection[LicenseDetail](ctx, c, org,
		"/users/"+userID+"/licenseDetails", nil, nil, true)
	return items, err
}

// AssignLicense 增删用户的许可分配。
func (c *Client) AssignLicense(ctx context.Context, org config.Org, userID string, payload AssignLicensePayload) error {
	if !ValidID(userID) {
		return &Error{Op: "users", HTTPStatus: http.StatusBadRequest, Message: "无效的用户 ID"}
	}
	_, err := c.do(ctx, org, request{
		method:   http.MethodPost,
		endpoint: "/users/" + userID + "/assignLicense",
		body:     payload,
	})
	return err
}

// SubscribedSkus 返回租户全部 SKU。
func (c *Client) SubscribedSkus(ctx context.Context, org config.Org) ([]SubscribedSku, error) {
	items, _, err := getCollection[SubscribedSku](ctx, c, org, "/subscribedSkus", nil, nil, true)
	return items, err
}

// DirectorySubscriptions 返回商务订阅（续订日期）。
func (c *Client) DirectorySubscriptions(ctx context.Context, org config.Org) ([]Subscription, error) {
	items, _, err := getCollection[Subscription](ctx, c, org, "/directory/subscriptions", nil, nil, true)
	return items, err
}

// Domains 返回租户域名。
func (c *Client) Domains(ctx context.Context, org config.Org) ([]Domain, error) {
	items, _, err := getCollection[Domain](ctx, c, org, "/domains", nil, nil, true)
	return items, err
}

// Organization 返回租户信息（取第一个）。
func (c *Client) Organization(ctx context.Context, org config.Org) (OrganizationInfo, error) {
	items, _, err := getCollection[OrganizationInfo](ctx, c, org, "/organization", nil, nil, true)
	if err != nil {
		return OrganizationInfo{}, err
	}
	if len(items) == 0 {
		return OrganizationInfo{}, &Error{Op: "organization", Message: "租户信息为空"}
	}
	return items[0], nil
}

// RoleDefinitionID 按显示名查找角色定义 ID，结果按组织缓存。
func (c *Client) RoleDefinitionID(ctx context.Context, org config.Org, displayName string) (string, error) {
	key := org.ID + "|" + displayName
	c.mu.Lock()
	if id, ok := c.roles[key]; ok {
		c.mu.Unlock()
		return id, nil
	}
	c.mu.Unlock()

	params := url.Values{
		"$filter": {"displayName eq '" + Quote(displayName) + "'"},
		"$select": {"id,displayName"},
	}
	items, _, err := getCollection[struct {
		ID string `json:"id"`
	}](ctx, c, org, "/roleManagement/directory/roleDefinitions", params, nil, false)
	if err != nil {
		return "", err
	}
	if len(items) == 0 {
		return "", &Error{Op: "roleDefinitions", Message: fmt.Sprintf("未找到角色 %q", displayName)}
	}
	c.mu.Lock()
	c.roles[key] = items[0].ID
	c.mu.Unlock()
	return items[0].ID, nil
}

// RoleAssignments 按可选 filter 查询角色分配，跟随全部分页。
func (c *Client) RoleAssignments(ctx context.Context, org config.Org, filter string, expandPrincipal bool) ([]RoleAssignment, error) {
	params := url.Values{"$top": {"999"}}
	if filter != "" {
		params.Set("$filter", filter)
	}
	if expandPrincipal {
		params.Set("$expand", "principal")
	}
	items, _, err := getCollection[RoleAssignment](ctx, c, org,
		"/roleManagement/directory/roleAssignments", params, nil, true)
	return items, err
}

// RoleAssignmentsByPrincipal 返回某主体的全部角色分配。
func (c *Client) RoleAssignmentsByPrincipal(ctx context.Context, org config.Org, principalID string) ([]RoleAssignment, error) {
	if !ValidID(principalID) {
		return nil, &Error{Op: "roleAssignments", HTTPStatus: http.StatusBadRequest, Message: "无效的主体 ID"}
	}
	return c.RoleAssignments(ctx, org, "principalId eq '"+Quote(principalID)+"'", false)
}

// RoleAssignmentsByDefinition 返回某角色的全部分配（展开 principal）。
func (c *Client) RoleAssignmentsByDefinition(ctx context.Context, org config.Org, roleDefinitionID string) ([]RoleAssignment, error) {
	if !ValidID(roleDefinitionID) {
		return nil, &Error{Op: "roleAssignments", HTTPStatus: http.StatusBadRequest, Message: "无效的角色 ID"}
	}
	return c.RoleAssignments(ctx, org, "roleDefinitionId eq '"+Quote(roleDefinitionID)+"'", true)
}

// AddRoleAssignment 为主体授予目录角色。
func (c *Client) AddRoleAssignment(ctx context.Context, org config.Org, principalID, roleDefinitionID string) error {
	if !ValidID(principalID) || !ValidID(roleDefinitionID) {
		return &Error{Op: "roleAssignments", HTTPStatus: http.StatusBadRequest, Message: "无效的主体或角色 ID"}
	}
	_, err := c.do(ctx, org, request{
		method:   http.MethodPost,
		endpoint: "/roleManagement/directory/roleAssignments",
		body: map[string]string{
			"principalId":      principalID,
			"roleDefinitionId": roleDefinitionID,
			"directoryScopeId": "/",
		},
	})
	return err
}

// DeleteRoleAssignment 删除一条角色分配。
func (c *Client) DeleteRoleAssignment(ctx context.Context, org config.Org, assignmentID string) error {
	if !ValidID(assignmentID) {
		return &Error{Op: "roleAssignments", HTTPStatus: http.StatusBadRequest, Message: "无效的分配 ID"}
	}
	_, err := c.do(ctx, org, request{
		method:   http.MethodDelete,
		endpoint: "/roleManagement/directory/roleAssignments/" + assignmentID,
	})
	return err
}

// NamedRoleAssignment 携带角色显示名的分配记录。
type NamedRoleAssignment struct {
	ID             string `json:"id"`
	RoleDefinition struct {
		ID          string `json:"id"`
		DisplayName string `json:"displayName"`
	} `json:"roleDefinition"`
}

// RoleAssignmentsForPrincipalNamed 返回某主体的全部角色分配，
// 通过 $expand=roleDefinition 携带角色显示名（跟随全部分页）。
func (c *Client) RoleAssignmentsForPrincipalNamed(ctx context.Context, org config.Org, principalID string) ([]NamedRoleAssignment, error) {
	if !ValidID(principalID) {
		return nil, &Error{Op: "roleAssignments", HTTPStatus: http.StatusBadRequest, Message: "无效的主体 ID"}
	}
	params := url.Values{
		"$top":    {"999"},
		"$filter": {"principalId eq '" + Quote(principalID) + "'"},
		"$expand": {"roleDefinition($select=id,displayName)"},
	}
	items, _, err := getCollection[NamedRoleAssignment](ctx, c, org,
		"/roleManagement/directory/roleAssignments", params, nil, true)
	return items, err
}

// unmarshalStrict 解码单对象响应，空体视为格式错误。
func unmarshalStrict(body []byte, v any) error {
	if len(bytes.TrimSpace(body)) == 0 {
		return &Error{Message: "响应体为空"}
	}
	if err := json.Unmarshal(body, v); err != nil {
		return fmt.Errorf("解析响应失败：%w", err)
	}
	return nil
}
