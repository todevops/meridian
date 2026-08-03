// Package jumpserver 实现 JumpServer 堡垒机的最小客户端（F-026 退役联动）：
// 退役时按主机 IP 定位资产并禁用（PATCH is_active=false）。
// 协议对齐官方 REST API（与 mocks :19010 一致）：
// Authorization: Token <token>；GET /api/v1/assets/assets/ 返回资产数组
// （兼容官方分页壳 {"count","results"}），按 address 客户端过滤。
// 连接配置来源优先级：凭据库 type=jumpserver 的凭据（secret {"url","token"}）
// > 环境变量 JUMPSERVER_URL / JUMPSERVER_TOKEN；两者都没有则联动跳过。
package jumpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Client 是 JumpServer REST API 客户端。
type Client struct {
	baseURL    string
	token      string
	httpClient *http.Client
}

// NewClient 创建 JumpServer 客户端。baseURL 形如 http://jumpserver:8080。
func NewClient(baseURL, token string) *Client {
	return &Client{
		baseURL:    strings.TrimRight(baseURL, "/"),
		token:      token,
		httpClient: &http.Client{Timeout: 15 * time.Second},
	}
}

// asset 是 JumpServer 资产对象的最小字段集。
type asset struct {
	ID       string `json:"id"`
	Address  string `json:"address"`
	IsActive bool   `json:"is_active"`
}

// assetsPage 是官方分页壳（部分部署启用分页时的响应形态）。
type assetsPage struct {
	Results []asset `json:"results"`
}

// DisableAssetByIP 按 IP 定位资产并禁用：返回禁用的资产 ID。
// 查无资产返回 ("", nil)（视为无可禁用对象，不算失败）。
func (c *Client) DisableAssetByIP(ctx context.Context, ip string) (string, error) {
	if ip == "" {
		return "", fmt.Errorf("IP 为空，无法定位 JumpServer 资产")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		c.baseURL+"/api/v1/assets/assets/", nil)
	if err != nil {
		return "", fmt.Errorf("构造请求失败: %w", err)
	}
	body, err := c.do(req)
	if err != nil {
		return "", err
	}
	// 响应形态兼容：纯数组（mock/部分版本）或分页壳。
	var assets []asset
	if err := json.Unmarshal(body, &assets); err != nil {
		var page assetsPage
		if err2 := json.Unmarshal(body, &page); err2 != nil {
			return "", fmt.Errorf("解析资产列表响应失败: %w", err)
		}
		assets = page.Results
	}
	found := ""
	for _, a := range assets {
		if a.Address == ip {
			found = a.ID
			break
		}
	}
	if found == "" {
		return "", nil
	}
	if !c.assetsActive(assets, found) {
		return found, nil // 已是禁用状态，幂等
	}

	payload, err := json.Marshal(map[string]any{"is_active": false})
	if err != nil {
		return "", fmt.Errorf("序列化请求体失败: %w", err)
	}
	req, err = http.NewRequestWithContext(ctx, http.MethodPatch,
		c.baseURL+"/api/v1/assets/assets/"+found+"/", bytes.NewReader(payload))
	if err != nil {
		return "", fmt.Errorf("构造请求失败: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if _, err := c.do(req); err != nil {
		return "", err
	}
	return found, nil
}

// assetsActive 判定指定资产当前是否启用。
func (c *Client) assetsActive(assets []asset, id string) bool {
	for _, a := range assets {
		if a.ID == id {
			return a.IsActive
		}
	}
	return false
}

// do 执行请求并校验状态码（2xx 视为成功），返回响应体。
// 鉴权头按官方协议：Authorization: Token <token>。
func (c *Client) do(req *http.Request) ([]byte, error) {
	req.Header.Set("Authorization", "Token "+c.token)
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("调用 JumpServer API 失败: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("读取 JumpServer 响应失败: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("JumpServer API 返回状态码 %d", resp.StatusCode)
	}
	return body, nil
}
