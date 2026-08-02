// Package n9e 实现 n9e（Nightingale）数据消费器：
// 定时调用 n9e REST API 拉取全量 targets，按方案文档 5.1 节字段映射表
// 转换为标准发现记录，走统一摄入管道入库。
// 仅当 N9E_API_URL 与 N9E_API_TOKEN 均配置时才启动，否则跳过。
package n9e

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// FlexString 容忍官方 API 中"字符串"与"字符串数组"两种形态
// （n9e 不同版本 host_tags 分别为空格分隔字符串与数组，统一归一为空格分隔字符串）。
type FlexString string

// UnmarshalJSON 同时兼容 "a b" 与 ["a","b"] 两种 JSON 形态。
func (f *FlexString) UnmarshalJSON(data []byte) error {
	var s string
	if err := json.Unmarshal(data, &s); err == nil {
		*f = FlexString(s)
		return nil
	}
	var arr []string
	if err := json.Unmarshal(data, &arr); err == nil {
		*f = FlexString(strings.Join(arr, " "))
		return nil
	}
	return fmt.Errorf("无法解析为字符串或字符串数组: %s", string(data))
}

// Target 对应 n9e targets API 返回的监控目标对象（字段名与 n9e API 一致）。
type Target struct {
	ID           int64      `json:"id"`            // target 主键（tags/note 回写路径参数）
	Ident        string     `json:"ident"`         // 主机标识（辅助键）
	HostIP       string     `json:"host_ip"`       // 内网 IP
	OS           string     `json:"os"`            // 操作系统
	CPUNum       int        `json:"cpu_num"`       // CPU 核数
	Arch         string     `json:"arch"`          // 架构
	AgentVersion string     `json:"agent_version"` // categraf 版本
	UpdateAt     int64      `json:"update_at"`     // 最近心跳时间（Unix 秒）
	GroupName    string     `json:"group_name"`    // 业务组名
	Tags         FlexString `json:"tags"`          // 标签（字符串或数组，FlexString 兼容）
	HostTags     FlexString `json:"host_tags"`     // categraf 上报标签（字符串或数组，FlexString 兼容）
}

// targetsResponse 对应 n9e API 的通用响应包装 {"dat": {...}, "err": ""}。
type targetsResponse struct {
	Dat struct {
		List  []Target `json:"list"`
		Total int      `json:"total"`
	} `json:"dat"`
	Err string `json:"err"`
}

// Client 是 n9e REST API 客户端。
type Client struct {
	apiURL     string
	token      string
	httpClient *http.Client
}

// NewClient 创建 n9e 客户端。apiURL 为 n9e-webapi 地址（如 http://n9e:17000）。
func NewClient(apiURL, token string) *Client {
	return &Client{
		apiURL:     strings.TrimRight(apiURL, "/"),
		token:      token,
		httpClient: &http.Client{Timeout: 30 * time.Second},
	}
}

// ListTargets 拉取全量监控目标。
func (c *Client) ListTargets(ctx context.Context) ([]Target, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.apiURL+"/api/n9e/targets?limit=10000", nil)
	if err != nil {
		return nil, fmt.Errorf("构造请求失败: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.token)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("调用 n9e targets API 失败: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("n9e targets API 返回状态码 %d", resp.StatusCode)
	}
	var body targetsResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, fmt.Errorf("解析 n9e 响应失败: %w", err)
	}
	if body.Err != "" {
		return nil, fmt.Errorf("n9e 返回错误: %s", body.Err)
	}
	return body.Dat.List, nil
}

// BaseURL 返回 n9e-webapi 基础地址（已去尾斜杠），用于拼接仪表盘链接。
func (c *Client) BaseURL() string { return c.apiURL }

// UpdateTargetTags 回写 target 标签（PUT /api/n9e/targets/{id}/tags）：
// tags 为完整标签列表（调用方负责与存量合并），按空格连接写入。
func (c *Client) UpdateTargetTags(ctx context.Context, targetID int64, tags []string) error {
	return c.putJSON(ctx, fmt.Sprintf("/api/n9e/targets/%d/tags", targetID),
		map[string]any{"tags": strings.Join(tags, " ")})
}

// UpdateTargetNote 回写 target 备注（PUT /api/n9e/targets/{id}/note）。
func (c *Client) UpdateTargetNote(ctx context.Context, targetID int64, note string) error {
	return c.putJSON(ctx, fmt.Sprintf("/api/n9e/targets/%d/note", targetID),
		map[string]any{"note": note})
}

// AlertCurEvents 拉取指定 ident 的当前告警（GET /api/n9e/alert-cur-events?ident=），
// 返回 n9e 原始响应体（代理场景原样透传，不解析）。
func (c *Client) AlertCurEvents(ctx context.Context, ident string) (json.RawMessage, error) {
	path := "/api/n9e/alert-cur-events?ident=" + url.QueryEscape(ident)
	body, err := c.do(ctx, http.MethodGet, path, nil)
	if err != nil {
		return nil, err
	}
	return json.RawMessage(body), nil
}

// putJSON 以 JSON 请求体调用 n9e PUT 接口，并校验官方响应壳 {"dat":..., "err":""}。
func (c *Client) putJSON(ctx context.Context, path string, payload map[string]any) error {
	raw, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("序列化请求体失败: %w", err)
	}
	body, err := c.do(ctx, http.MethodPut, path, raw)
	if err != nil {
		return err
	}
	// 响应壳中 err 非空视为失败（dat 内容对本场景无意义）。
	var shell struct {
		Err string `json:"err"`
	}
	if len(body) > 0 {
		if err := json.Unmarshal(body, &shell); err == nil && shell.Err != "" {
			return fmt.Errorf("n9e 返回错误: %s", shell.Err)
		}
	}
	return nil
}

// do 是 n9e API 调用的公共通道：Bearer 鉴权 + 状态码校验，返回原始响应体。
func (c *Client) do(ctx context.Context, method, path string, payload []byte) ([]byte, error) {
	var reader io.Reader
	if payload != nil {
		reader = bytes.NewReader(payload)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.apiURL+path, reader)
	if err != nil {
		return nil, fmt.Errorf("构造请求失败: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("调用 n9e API 失败: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("读取 n9e 响应失败: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("n9e API 返回状态码 %d", resp.StatusCode)
	}
	return body, nil
}
