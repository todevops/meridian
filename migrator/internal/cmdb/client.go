// CMDB API 客户端：模型确保、CI 创建、IPAM 前缀/IP 写入。
// IPAM 接口形状按并行开发约定（见 pkg/openapi 契约与各代理任务约定）：
//
//	POST /api/v1/ipam/prefixes {cidr,name,vlan_id?,description?,parent_id?}（非法 400 / 同级重叠 409）
//	POST /api/v1/ipam/ips {prefix_id,ip,status?,ci_id?,description?}（重复 409 / 不在前缀内 400）
package cmdb

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Client 是 CMDB REST API 客户端；token 非空时携带 Bearer 认证头。
type Client struct {
	baseURL    string
	token      string
	httpClient *http.Client
}

// NewClient 创建客户端；baseURL 末尾斜杠会被裁剪。
func NewClient(baseURL string) *Client {
	return &Client{
		baseURL:    strings.TrimRight(baseURL, "/"),
		httpClient: &http.Client{Timeout: 30 * time.Second},
	}
}

// SetToken 设置 Bearer 令牌（服务端启用认证时使用）。
func (c *Client) SetToken(token string) { c.token = token }

// Login 通过 /api/v1/auth/login 登录并持有令牌（服务端启用认证时调用）。
func (c *Client) Login(ctx context.Context, username, password string) error {
	var resp struct {
		Token string `json:"token"`
	}
	body := map[string]string{"username": username, "password": password}
	if err := c.do(ctx, http.MethodPost, "/api/v1/auth/login", body, &resp); err != nil {
		return fmt.Errorf("CMDB 登录失败: %w", err)
	}
	if resp.Token == "" {
		return fmt.Errorf("CMDB 登录响应缺少 token")
	}
	c.token = resp.Token
	return nil
}

// APIError 表示 CMDB 返回的非 2xx 响应（契约 Error schema：{code,message,details?}）。
type APIError struct {
	StatusCode int
	Code       string
	Message    string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("CMDB API 返回 HTTP %d（%s）: %s", e.StatusCode, e.Code, e.Message)
}

// CI 为 CMDB 返回的 CI 实例（仅解码迁移关心的字段）。
type CI struct {
	ID         string         `json:"id"`
	ModelID    string         `json:"model_id"`
	Attributes map[string]any `json:"attributes"`
	Status     string         `json:"status"`
	Source     string         `json:"source"`
}

// PrefixRef 为 IPAM 前缀创建响应（仅需要 id 供后续 parent/归属引用）。
type PrefixRef struct {
	ID string `json:"id"`
}

// PrefixCreateRequest 对应 POST /api/v1/ipam/prefixes 请求体。
// vlan_id 为 VLAN 编号（vid，整数），与服务端实现（*int）对齐。
type PrefixCreateRequest struct {
	CIDR        string `json:"cidr"`
	Name        string `json:"name"`
	VLANID      *int   `json:"vlan_id,omitempty"`
	Description string `json:"description,omitempty"`
	ParentID    string `json:"parent_id,omitempty"`
}

// IPCreateRequest 对应 POST /api/v1/ipam/ips 请求体。
type IPCreateRequest struct {
	PrefixID    string `json:"prefix_id"`
	IP          string `json:"ip"`
	Status      string `json:"status,omitempty"`
	CIID        string `json:"ci_id,omitempty"`
	Description string `json:"description,omitempty"`
}

// do 执行一次 JSON 请求：method+path，reqBody 可空；2xx 时解码到 out（可空）。
func (c *Client) do(ctx context.Context, method, path string, reqBody, out any) error {
	var reader io.Reader
	if reqBody != nil {
		payload, err := json.Marshal(reqBody)
		if err != nil {
			return fmt.Errorf("请求体编码失败: %w", err)
		}
		reader = bytes.NewReader(payload)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, reader)
	if err != nil {
		return fmt.Errorf("构造请求失败: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	if reqBody != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("请求 %s %s 失败: %w", method, path, err)
	}
	body, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		return fmt.Errorf("读取 %s %s 响应失败: %w", method, path, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		apiErr := &APIError{StatusCode: resp.StatusCode, Code: "UNKNOWN", Message: truncate(string(body), 300)}
		var parsed struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		}
		if json.Unmarshal(body, &parsed) == nil && (parsed.Code != "" || parsed.Message != "") {
			apiErr.Code = parsed.Code
			apiErr.Message = parsed.Message
		}
		return apiErr
	}
	if out != nil {
		if err := json.Unmarshal(body, out); err != nil {
			return fmt.Errorf("解析 %s %s 响应失败: %w（响应: %s）", method, path, err, truncate(string(body), 200))
		}
	}
	return nil
}

// EnsureModel 确保模型存在：GET 按 code 查询，404 则 POST 创建。
// 返回 created 表示本次是否新建。
func (c *Client) EnsureModel(ctx context.Context, def ModelDefinition) (created bool, err error) {
	err = c.do(ctx, http.MethodGet, "/api/v1/models/"+def.Code, nil, nil)
	if err == nil {
		return false, nil
	}
	var apiErr *APIError
	if !asAPIError(err, &apiErr) || apiErr.StatusCode != http.StatusNotFound {
		return false, fmt.Errorf("查询模型 %q 失败: %w", def.Code, err)
	}
	if err := c.do(ctx, http.MethodPost, "/api/v1/models", def, nil); err != nil {
		return false, fmt.Errorf("创建模型 %q 失败: %w", def.Code, err)
	}
	return true, nil
}

// CreateCI 创建 CI（status=active，source=netbox-migration），返回建档后的 CI。
func (c *Client) CreateCI(ctx context.Context, modelCode string, attributes map[string]any) (CI, error) {
	reqBody := map[string]any{
		"model_id":   modelCode,
		"attributes": attributes,
		"status":     "active",
		"source":     MigrationSource,
	}
	var ci CI
	if err := c.do(ctx, http.MethodPost, "/api/v1/cis", reqBody, &ci); err != nil {
		return CI{}, err
	}
	return ci, nil
}

// CreatePrefix 创建 IPAM 前缀，返回前缀引用（id）。
func (c *Client) CreatePrefix(ctx context.Context, req PrefixCreateRequest) (PrefixRef, error) {
	var ref PrefixRef
	if err := c.do(ctx, http.MethodPost, "/api/v1/ipam/prefixes", req, &ref); err != nil {
		return PrefixRef{}, err
	}
	if ref.ID == "" {
		return PrefixRef{}, fmt.Errorf("创建前缀 %s 成功但响应缺少 id", req.CIDR)
	}
	return ref, nil
}

// CreateIP 创建 IPAM IP 地址记录。
func (c *Client) CreateIP(ctx context.Context, req IPCreateRequest) error {
	return c.do(ctx, http.MethodPost, "/api/v1/ipam/ips", req, nil)
}

// asAPIError 判定错误（含包装链）是否为 APIError 并取出。
func asAPIError(err error, target **APIError) bool {
	return errors.As(err, target)
}

// IsStatus 判定错误是否为指定 HTTP 状态码的 API 错误。
func IsStatus(err error, status int) bool {
	var apiErr *APIError
	return asAPIError(err, &apiErr) && apiErr.StatusCode == status
}

// truncate 截断字符串用于错误信息展示。
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
