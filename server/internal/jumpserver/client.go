// Package jumpserver 实现 JumpServer 堡垒机的最小客户端：
// F-026 退役联动按主机 IP 定位资产并禁用（PATCH is_active=false）；
// F-071 资产同步需要资产/节点清单读回与资产创建/更新/禁用写路径。
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

// Asset 是 JumpServer 资产对象的最小字段集。
type Asset struct {
	ID       string   `json:"id"`
	Name     string   `json:"name"`
	Address  string   `json:"address"`
	Platform string   `json:"platform"`
	Nodes    []string `json:"nodes"`
	IsActive bool     `json:"is_active"`
}

// Node 是 JumpServer 节点对象的最小字段集（FullValue 体现层级路径）。
type Node struct {
	ID        string `json:"id"`
	Key       string `json:"key"`
	Name      string `json:"name"`
	Value     string `json:"value"`
	FullValue string `json:"full_value"`
}

// assetsPage 是官方分页壳（部分部署启用分页时的响应形态）。
type assetsPage struct {
	Results []Asset `json:"results"`
}

// ListAssets 拉取全部资产（响应形态兼容纯数组与官方分页壳）。
func (c *Client) ListAssets(ctx context.Context) ([]Asset, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		c.baseURL+"/api/v1/assets/assets/", nil)
	if err != nil {
		return nil, fmt.Errorf("构造请求失败: %w", err)
	}
	body, err := c.do(req)
	if err != nil {
		return nil, err
	}
	var assets []Asset
	if err := json.Unmarshal(body, &assets); err != nil {
		var page assetsPage
		if err2 := json.Unmarshal(body, &page); err2 != nil {
			return nil, fmt.Errorf("解析资产列表响应失败: %w", err)
		}
		assets = page.Results
	}
	return assets, nil
}

// ListNodes 拉取全部节点（扁平列表，FullValue 形如 /Default/业务线/应用）。
func (c *Client) ListNodes(ctx context.Context) ([]Node, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		c.baseURL+"/api/v1/assets/nodes/", nil)
	if err != nil {
		return nil, fmt.Errorf("构造请求失败: %w", err)
	}
	body, err := c.do(req)
	if err != nil {
		return nil, err
	}
	var nodes []Node
	if err := json.Unmarshal(body, &nodes); err != nil {
		return nil, fmt.Errorf("解析节点列表响应失败: %w", err)
	}
	return nodes, nil
}

// CreateAsset 创建资产（name/address/platform 必填，nodes 为归属节点 ID 列表），
// 返回创建后的资产（含服务端分配的 ID）。
func (c *Client) CreateAsset(ctx context.Context, asset Asset) (*Asset, error) {
	payload, err := json.Marshal(asset)
	if err != nil {
		return nil, fmt.Errorf("序列化请求体失败: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.baseURL+"/api/v1/assets/assets/", bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("构造请求失败: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	body, err := c.do(req)
	if err != nil {
		return nil, err
	}
	var created Asset
	if err := json.Unmarshal(body, &created); err != nil {
		return nil, fmt.Errorf("解析创建资产响应失败: %w", err)
	}
	return &created, nil
}

// UpdateAsset 部分更新资产（PATCH，fields 只含要改的字段，如 name/platform/nodes/is_active）。
func (c *Client) UpdateAsset(ctx context.Context, id string, fields map[string]any) error {
	payload, err := json.Marshal(fields)
	if err != nil {
		return fmt.Errorf("序列化请求体失败: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPatch,
		c.baseURL+"/api/v1/assets/assets/"+id+"/", bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("构造请求失败: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if _, err := c.do(req); err != nil {
		return err
	}
	return nil
}

// DisableAsset 禁用指定资产（PATCH is_active=false，幂等：已禁用直接返回）。
func (c *Client) DisableAsset(ctx context.Context, id string, active bool) error {
	if !active {
		return nil // 已是禁用状态，幂等
	}
	return c.UpdateAsset(ctx, id, map[string]any{"is_active": false})
}

// DisableAssetByIP 按 IP 定位资产并禁用：返回禁用的资产 ID。
// 查无资产返回 ("", nil)（视为无可禁用对象，不算失败）。
func (c *Client) DisableAssetByIP(ctx context.Context, ip string) (string, error) {
	if ip == "" {
		return "", fmt.Errorf("IP 为空，无法定位 JumpServer 资产")
	}
	assets, err := c.ListAssets(ctx)
	if err != nil {
		return "", err
	}
	for _, a := range assets {
		if a.Address == ip {
			if err := c.DisableAsset(ctx, a.ID, a.IsActive); err != nil {
				return "", err
			}
			return a.ID, nil
		}
	}
	return "", nil
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
