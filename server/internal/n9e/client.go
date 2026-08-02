// Package n9e 实现 n9e（Nightingale）数据消费器：
// 定时调用 n9e REST API 拉取全量 targets，按方案文档 5.1 节字段映射表
// 转换为标准发现记录，走统一摄入管道入库。
// 仅当 N9E_API_URL 与 N9E_API_TOKEN 均配置时才启动，否则跳过。
package n9e

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
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
