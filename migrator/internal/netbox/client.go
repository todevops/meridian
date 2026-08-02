// NetBox 只读客户端：Token 认证 + limit/offset 分页遍历。
package netbox

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// 分页大小：NetBox 默认每页 50，显式放大减少往返。
const pageLimit = 100

// Client 是 NetBox REST API 客户端。
type Client struct {
	baseURL    string
	token      string
	httpClient *http.Client
}

// NewClient 创建客户端；baseURL 末尾斜杠会被裁剪。
func NewClient(baseURL, token string) *Client {
	return &Client{
		baseURL:    strings.TrimRight(baseURL, "/"),
		token:      token,
		httpClient: &http.Client{Timeout: 30 * time.Second},
	}
}

// APIError 表示 NetBox 返回的非 2xx 响应。
type APIError struct {
	StatusCode int
	Body       string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("NetBox API 返回 HTTP %d: %s", e.StatusCode, truncate(e.Body, 200))
}

// listPath 拉取单个列表路径的全部页，逐条交给 emit 解码。
// 分页策略：limit/offset 递增，offset >= count 或本页为空时结束。
func (c *Client) listPath(ctx context.Context, path string, emit func(raw json.RawMessage) error) error {
	offset := 0
	for {
		u := fmt.Sprintf("%s%s?limit=%d&offset=%d", c.baseURL, path, pageLimit, offset)
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			return fmt.Errorf("构造请求失败: %w", err)
		}
		req.Header.Set("Authorization", "Token "+c.token)
		req.Header.Set("Accept", "application/json")

		resp, err := c.httpClient.Do(req)
		if err != nil {
			return fmt.Errorf("请求 %s 失败: %w", path, err)
		}
		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			return fmt.Errorf("读取 %s 响应失败: %w", path, err)
		}
		if resp.StatusCode != http.StatusOK {
			return &APIError{StatusCode: resp.StatusCode, Body: string(body)}
		}

		var env listEnvelope
		if err := json.Unmarshal(body, &env); err != nil {
			return fmt.Errorf("解析 %s 分页信封失败: %w", path, err)
		}
		for _, raw := range env.Results {
			if err := emit(raw); err != nil {
				return err
			}
		}
		offset += len(env.Results)
		if len(env.Results) == 0 || offset >= env.Count {
			return nil
		}
	}
}

// decodeEach 返回把逐条 JSON 解码为类型 T 并追加到切片的 emit 函数。
func decodeEach[T any](items *[]T) func(json.RawMessage) error {
	return func(raw json.RawMessage) error {
		var item T
		if err := json.Unmarshal(raw, &item); err != nil {
			return fmt.Errorf("记录解码失败: %w", err)
		}
		*items = append(*items, item)
		return nil
	}
}

// ListSites 拉取全部站点。
func (c *Client) ListSites(ctx context.Context) ([]Site, error) {
	var items []Site
	err := c.listPath(ctx, "/api/dcim/sites/", decodeEach(&items))
	return items, err
}

// ListRacks 拉取全部机架。
func (c *Client) ListRacks(ctx context.Context) ([]Rack, error) {
	var items []Rack
	err := c.listPath(ctx, "/api/dcim/racks/", decodeEach(&items))
	return items, err
}

// ListDevices 拉取全部设备。
func (c *Client) ListDevices(ctx context.Context) ([]Device, error) {
	var items []Device
	err := c.listPath(ctx, "/api/dcim/devices/", decodeEach(&items))
	return items, err
}

// ListVLANs 拉取全部 VLAN。
func (c *Client) ListVLANs(ctx context.Context) ([]VLAN, error) {
	var items []VLAN
	err := c.listPath(ctx, "/api/ipam/vlans/", decodeEach(&items))
	return items, err
}

// ListVirtualMachines 拉取全部虚拟机。
func (c *Client) ListVirtualMachines(ctx context.Context) ([]VirtualMachine, error) {
	var items []VirtualMachine
	err := c.listPath(ctx, "/api/virtualization/virtual-machines/", decodeEach(&items))
	return items, err
}

// ListPrefixes 拉取全部前缀。
func (c *Client) ListPrefixes(ctx context.Context) ([]Prefix, error) {
	var items []Prefix
	err := c.listPath(ctx, "/api/ipam/prefixes/", decodeEach(&items))
	return items, err
}

// ListIPAddresses 拉取全部 IP 地址。
func (c *Client) ListIPAddresses(ctx context.Context) ([]IPAddress, error) {
	var items []IPAddress
	err := c.listPath(ctx, "/api/ipam/ip-addresses/", decodeEach(&items))
	return items, err
}

// truncate 截断字符串用于错误信息展示。
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
