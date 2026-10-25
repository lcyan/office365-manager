// Package graph 封装 Microsoft Graph API 访问：令牌获取与缓存、
// 带统一 OData 转义的类型化操作、分页跟随与错误契约。
package graph

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/lcyan/office365-manager/internal/config"
)

const (
	defaultGraphBase = "https://graph.microsoft.com/v1.0"
	defaultLoginBase = "https://login.microsoftonline.com"
	defaultScope     = "https://graph.microsoft.com/.default"
	tokenEarlyWindow = time.Minute // 提前 60 秒刷新令牌
	maxPages         = 100         // nextLink 跟随上限，防御异常响应
)

// Client 是线程安全的 Graph 客户端。零值不可用，使用 New 构造。
type Client struct {
	http      *http.Client
	log       *slog.Logger
	graphBase string
	loginBase string

	mu     sync.Mutex
	tokens map[string]cachedToken // org ID → 令牌
	roles  map[string]string      // org ID + "|" + 角色名 → 角色定义 ID
}

type cachedToken struct {
	token  string
	expiry time.Time
}

// New 构造客户端。
func New(log *slog.Logger) *Client {
	return &Client{
		http:      &http.Client{Timeout: 20 * time.Second},
		log:       log,
		graphBase: defaultGraphBase,
		loginBase: defaultLoginBase,
		tokens:    map[string]cachedToken{},
		roles:     map[string]string{},
	}
}

// SetHTTPClient 替换底层 HTTP 客户端（测试注入伪传输层）。
func (c *Client) SetHTTPClient(hc *http.Client) { c.http = hc }

// SetBaseURL 覆盖 Graph 与登录端点（仅测试使用）。
func (c *Client) SetBaseURL(graphBase, loginBase string) {
	c.graphBase = strings.TrimSuffix(graphBase, "/")
	c.loginBase = strings.TrimSuffix(loginBase, "/")
}

// InvalidateOrg 清除组织的令牌与角色定义缓存（组织编辑/删除时调用）。
func (c *Client) InvalidateOrg(orgID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.tokens, orgID)
	for key := range c.roles {
		if strings.HasPrefix(key, orgID+"|") {
			delete(c.roles, key)
		}
	}
}

// --- 错误契约 ---

// Error 表示 Graph 返回的 HTTP ≥400 响应或令牌获取失败。
// 网络错误以普通 wrapped error 返回，调用方用 errors.As 提取本类型。
type Error struct {
	Op         string
	HTTPStatus int
	Code       string
	Message    string
}

func (e *Error) Error() string {
	if e.HTTPStatus == 0 {
		return fmt.Sprintf("%s: %s", e.Op, e.Message)
	}
	return fmt.Sprintf("%s: HTTP %d %s: %s", e.Op, e.HTTPStatus, e.Code, e.Message)
}

// IsNotFound 报告 err 是否为 404。
func IsNotFound(err error) bool {
	var gerr *Error
	return errors.As(err, &gerr) && gerr.HTTPStatus == http.StatusNotFound
}

// IsInsufficientPrivileges 报告 err 是否为权限不足
// （Graph 实际消息为 "Insufficient privileges to complete the operation."）。
func IsInsufficientPrivileges(err error) bool {
	var gerr *Error
	return errors.As(err, &gerr) &&
		strings.Contains(strings.ToLower(gerr.Message), "insufficient privileges")
}

// --- 令牌 ---

func (c *Client) token(ctx context.Context, org config.Org) (string, error) {
	c.mu.Lock()
	cached := c.tokens[org.ID]
	if cached.token != "" && time.Now().Add(tokenEarlyWindow).Before(cached.expiry) {
		c.mu.Unlock()
		return cached.token, nil
	}
	c.mu.Unlock()

	token, expiresIn, err := c.fetchToken(ctx, org)
	if err != nil {
		return "", err
	}

	c.mu.Lock()
	c.tokens[org.ID] = cachedToken{token: token, expiry: time.Now().Add(expiresIn)}
	c.mu.Unlock()
	return token, nil
}

func (c *Client) dropToken(orgID string) {
	c.mu.Lock()
	delete(c.tokens, orgID)
	c.mu.Unlock()
}

// fetchToken 走 OAuth2 client credentials 流程。
func (c *Client) fetchToken(ctx context.Context, org config.Org) (string, time.Duration, error) {
	endpoint := fmt.Sprintf("%s/%s/oauth2/v2.0/token", c.loginBase, url.PathEscape(org.TenantID))
	form := url.Values{
		"grant_type":    {"client_credentials"},
		"client_id":     {org.ClientID},
		"client_secret": {org.ClientSecret},
		"scope":         {defaultScope},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint,
		strings.NewReader(form.Encode()))
	if err != nil {
		return "", 0, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	started := time.Now()
	resp, err := c.http.Do(req)
	if err != nil {
		return "", 0, &Error{Op: "token", Message: err.Error()}
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	c.log.Info("graph token", "status", resp.StatusCode, "elapsed_ms", time.Since(started).Milliseconds())

	if resp.StatusCode != http.StatusOK {
		gerr := &Error{Op: "token", HTTPStatus: resp.StatusCode, Message: "令牌获取失败"}
		var parsed struct {
			Error            string `json:"error"`
			ErrorDescription string `json:"error_description"`
		}
		if json.Unmarshal(body, &parsed) == nil {
			gerr.Code = parsed.Error
			// error_description 可能携带密钥片段，只保留 AADSTS 码与固定片段。
			gerr.Message = "令牌获取失败：" + aadstsSummary(parsed.ErrorDescription)
		}
		return "", 0, gerr
	}
	var parsed struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil || parsed.AccessToken == "" {
		return "", 0, &Error{Op: "token", Message: "令牌响应格式无效"}
	}
	if parsed.ExpiresIn <= 0 {
		parsed.ExpiresIn = 3600
	}
	return parsed.AccessToken, time.Duration(parsed.ExpiresIn) * time.Second, nil
}

// aadstsSummary 从 error_description 提取 AADSTS 错误码，丢弃其余细节
// （描述中可能包含应用 ID 等，不落入日志）。
func aadstsSummary(description string) string {
	for _, part := range strings.Fields(description) {
		if strings.HasPrefix(part, "AADSTS") {
			return strings.TrimRight(part, ":,;")
		}
	}
	return "凭据或配置被拒绝"
}

// --- 请求核心 ---

type request struct {
	method   string
	endpoint string            // 以 / 开头，如 "/users"
	params   url.Values        // 查询参数
	body     any               // JSON 序列化的请求体
	headers  map[string]string // 附加头（如 ConsistencyLevel）
}

// resource 提取资源类别用于日志（不含 ID 与查询串）。
func (req request) resource() string {
	path := strings.SplitN(req.endpoint, "?", 2)[0]
	segments := strings.Split(strings.Trim(path, "/"), "/")
	return segments[0]
}

// do 发送一次 Graph 请求；401 时清除令牌并重试一次。
func (c *Client) do(ctx context.Context, org config.Org, req request) ([]byte, error) {
	for attempt := 0; ; attempt++ {
		token, err := c.token(ctx, org)
		if err != nil {
			return nil, err
		}
		body, status, err := c.send(ctx, org, req, token)
		if err != nil {
			return nil, err
		}
		if status == http.StatusUnauthorized && attempt == 0 {
			c.dropToken(org.ID)
			continue
		}
		if status >= 400 {
			return nil, parseError(req, status, body)
		}
		return body, nil
	}
}

func (c *Client) send(ctx context.Context, org config.Org, req request, token string) ([]byte, int, error) {
	target := c.graphBase + req.endpoint
	if len(req.params) > 0 {
		target += "?" + req.params.Encode()
	}
	var reader io.Reader
	if req.body != nil {
		encoded, err := json.Marshal(req.body)
		if err != nil {
			return nil, 0, fmt.Errorf("序列化请求体失败：%w", err)
		}
		reader = bytes.NewReader(encoded)
	}
	httpReq, err := http.NewRequestWithContext(ctx, req.method, target, reader)
	if err != nil {
		return nil, 0, err
	}
	httpReq.Header.Set("Authorization", "Bearer "+token)
	if req.body != nil {
		httpReq.Header.Set("Content-Type", "application/json")
	}
	for key, value := range req.headers {
		httpReq.Header.Set(key, value)
	}

	started := time.Now()
	resp, err := c.http.Do(httpReq)
	if err != nil {
		c.log.Warn("graph request failed", "method", req.method,
			"resource", req.resource(), "err", err)
		return nil, 0, &Error{Op: req.resource(), Message: "网络请求失败：" + err.Error()}
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	c.log.Info("graph request", "method", req.method, "resource", req.resource(),
		"status", resp.StatusCode, "elapsed_ms", time.Since(started).Milliseconds())
	return body, resp.StatusCode, nil
}

func parseError(req request, status int, body []byte) *Error {
	gerr := &Error{Op: req.resource(), HTTPStatus: status, Message: "请求失败"}
	var parsed struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(body, &parsed) == nil {
		if parsed.Error.Code != "" || parsed.Error.Message != "" {
			gerr.Code = parsed.Error.Code
			gerr.Message = parsed.Error.Message
		}
	}
	return gerr
}

// envelope 是集合响应的公共外壳。
type envelope[T any] struct {
	Value    []T    `json:"value"`
	NextLink string `json:"@odata.nextLink"`
	Count    *int   `json:"@odata.count"`
}

// getCollection 发送 GET 并解码集合；followNext 时跟随 @odata.nextLink。
func getCollection[T any](ctx context.Context, c *Client, org config.Org,
	endpoint string, params url.Values, headers map[string]string, followNext bool,
) ([]T, *int, error) {
	var all []T
	var total *int
	current := endpoint
	currentParams := params
	for page := 0; ; page++ {
		body, err := c.do(ctx, org, request{
			method:   http.MethodGet,
			endpoint: current,
			params:   currentParams,
			headers:  headers,
		})
		if err != nil {
			return nil, nil, err
		}
		var pageData envelope[T]
		if err := json.Unmarshal(body, &pageData); err != nil {
			return nil, nil, fmt.Errorf("解析集合响应失败：%w", err)
		}
		all = append(all, pageData.Value...)
		if pageData.Count != nil {
			total = pageData.Count
		}
		if !followNext || pageData.NextLink == "" || page >= maxPages {
			return all, total, nil
		}
		// nextLink 是完整 URL：剥掉 Graph 前缀还原为 path?query。
		trimmed, ok := stripBase(c.graphBase, pageData.NextLink)
		if !ok {
			return nil, nil, fmt.Errorf("nextLink 指向意外的地址")
		}
		path, query, _ := strings.Cut(trimmed, "?")
		current = path
		currentParams, _ = url.ParseQuery(query)
		headers = nil // nextLink 自带全部查询参数
	}
}

// stripBase 去除 nextLink 的 Graph 前缀。
func stripBase(base, nextLink string) (string, bool) {
	if strings.HasPrefix(nextLink, base+"/") {
		return nextLink[len(base):], true
	}
	return "", false
}
