// UModel EntityStore REST 客户端：协议与 mocks :19011 及开源 UModel 形态对齐——
// 实体 PUT /api/v1/entitysets/{set}/entities/{pk} upsert（body 为属性 JSON，
// keep_alive_seconds 为保留字段）；关联批量 PUT /api/v1/entitysets/{set}/links
// （body 为 [{src_pk,dst_pk,link_type}]）。鉴权：Authorization: Bearer <token>。
package umodelgen

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// 环境变量默认值（开发口径）。
const (
	defaultStoreURL = ":19011"
	defaultToken    = "dev-umodel-token"
)

// Link 是一条 EntitySetLink（实体间关联）。
type Link struct {
	SrcPK    string `json:"src_pk"`
	DstPK    string `json:"dst_pk"`
	LinkType string `json:"link_type"`
}

// Client 是 UModel EntityStore 客户端。
type Client struct {
	baseURL    string
	token      string
	httpClient *http.Client
}

// NewClient 创建 EntityStore 客户端。baseURL 形如 http://umodel:19011。
func NewClient(baseURL, token string) *Client {
	return &Client{
		baseURL:    normalizeURL(baseURL),
		token:      token,
		httpClient: &http.Client{Timeout: 15 * time.Second},
	}
}

// NewClientFromEnv 按环境变量创建客户端：
// UMODEL_STORE_URL（默认 :19011）、UMODEL_TOKEN（默认 dev-umodel-token）。
func NewClientFromEnv() *Client {
	base := os.Getenv("UMODEL_STORE_URL")
	if base == "" {
		base = defaultStoreURL
	}
	token := os.Getenv("UMODEL_TOKEN")
	if token == "" {
		token = defaultToken
	}
	return NewClient(base, token)
}

// normalizeURL 把 ":19011"、"localhost:19011" 等简写规范化为完整 URL。
func normalizeURL(v string) string {
	v = strings.TrimSpace(v)
	if strings.HasPrefix(v, ":") {
		return "http://localhost" + v
	}
	if v != "" && !strings.Contains(v, "://") {
		return "http://" + v
	}
	return v
}

// UpsertEntity upsert 一个实体：attrs 为属性集，keepAliveSeconds 为保活秒数
// （作为保留字段随属性一并提交，EntityStore 据此判定下线过期）。
func (c *Client) UpsertEntity(ctx context.Context, set, pk string, attrs map[string]any, keepAliveSeconds int) error {
	body := map[string]any{"keep_alive_seconds": keepAliveSeconds}
	for k, v := range attrs {
		body[k] = v
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("序列化实体失败: %w", err)
	}
	path := fmt.Sprintf("%s/api/v1/entitysets/%s/entities/%s",
		c.baseURL, url.PathEscape(set), url.PathEscape(pk))
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, path, bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("构造请求失败: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if _, err := c.do(req); err != nil {
		return err
	}
	return nil
}

// UpsertLinks 批量 upsert 关联到指定 EntitySet。
func (c *Client) UpsertLinks(ctx context.Context, set string, links []Link) error {
	if len(links) == 0 {
		return nil
	}
	payload, err := json.Marshal(links)
	if err != nil {
		return fmt.Errorf("序列化关联失败: %w", err)
	}
	path := fmt.Sprintf("%s/api/v1/entitysets/%s/links", c.baseURL, url.PathEscape(set))
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, path, bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("构造请求失败: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if _, err := c.do(req); err != nil {
		return err
	}
	return nil
}

// do 执行请求并校验状态码（2xx 视为成功）。鉴权头：Authorization: Bearer <token>。
func (c *Client) do(req *http.Request) ([]byte, error) {
	req.Header.Set("Authorization", "Bearer "+c.token)
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("调用 UModel EntityStore 失败: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("读取 UModel EntityStore 响应失败: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("UModel EntityStore 返回状态码 %d", resp.StatusCode)
	}
	return body, nil
}
