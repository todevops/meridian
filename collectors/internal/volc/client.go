// Package volc 实现火山引擎采集器：
// 调用 CloudControl ListResources 拉取云资源清单，ECS 类资源映射为 host 发现记录，
// VKE 资源映射为 k8s_workload 占位记录（注记集群，待 K8s 采集器接管）。
package volc

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"collectors/internal/record"
)

const (
	// Source 是发现来源系统标识。
	Source = "volc"
	// CollectorName 是采集器标识。
	CollectorName = "volc-cloud-collector"
)

// Resource 对应 CloudControl ListResources 返回的一个资源项。
type Resource struct {
	ResourceType  string
	ResourceID    string
	Configuration map[string]any
	Tags          map[string]string
}

// UnmarshalJSON 兼容 Configuration 为 JSON 字符串（官方）或直接对象（mock），
// Tags 兼容 [{"Key","Value"}] 与 {"k":"v"} 两种形状。
func (r *Resource) UnmarshalJSON(data []byte) error {
	var raw struct {
		ResourceType  string          `json:"ResourceType"`
		ResourceID    string          `json:"ResourceId"`
		Configuration json.RawMessage `json:"Configuration"`
		Tags          json.RawMessage `json:"Tags"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	r.ResourceType = raw.ResourceType
	r.ResourceID = raw.ResourceID
	r.Configuration = parseConfiguration(raw.Configuration)
	r.Tags = parseTags(raw.Tags)
	return nil
}

// parseConfiguration 解析资源配置：官方为字符串内嵌 JSON，mock 可能直接给对象。
func parseConfiguration(raw json.RawMessage) map[string]any {
	trim := bytes.TrimSpace(raw)
	if len(trim) == 0 || string(trim) == "null" {
		return nil
	}
	var embedded string
	if err := json.Unmarshal(trim, &embedded); err == nil {
		trim = bytes.TrimSpace([]byte(embedded))
		if len(trim) == 0 {
			return nil
		}
	}
	var out map[string]any
	if err := json.Unmarshal(trim, &out); err != nil {
		return nil
	}
	return out
}

// parseTags 解析标签：兼容 [{"Key","Value"}] 列表与 {"k":"v"} 字典。
func parseTags(raw json.RawMessage) map[string]string {
	trim := bytes.TrimSpace(raw)
	if len(trim) == 0 || string(trim) == "null" {
		return nil
	}
	var kv map[string]string
	if err := json.Unmarshal(trim, &kv); err == nil {
		return kv
	}
	var list []struct {
		Key   string `json:"Key"`
		Value string `json:"Value"`
	}
	if err := json.Unmarshal(trim, &list); err == nil {
		out := map[string]string{}
		for _, t := range list {
			out[t.Key] = t.Value
		}
		return out
	}
	return nil
}

// listResponse 对应 CloudControl ListResources 响应。
type listResponse struct {
	ResponseMetadata struct {
		Error *struct {
			Code    string `json:"Code"`
			Message string `json:"Message"`
		} `json:"Error"`
	} `json:"ResponseMetadata"`
	Result struct {
		Resources  []Resource `json:"Resources"`
		TotalCount int        `json:"TotalCount"`
	} `json:"Result"`
}

// Client 是火山引擎 CloudControl API 客户端。
type Client struct {
	baseURL string
	http    *http.Client
}

// NewClient 创建客户端。apiURL 支持 ":19006" 简写。
func NewClient(apiURL string) *Client {
	return &Client{
		baseURL: record.NormalizeBaseURL(apiURL),
		http:    &http.Client{Timeout: 30 * time.Second},
	}
}

// ListResources 调用 CloudControl ListResources 拉取全量资源清单。
func (c *Client) ListResources(ctx context.Context) ([]Resource, error) {
	url := c.baseURL + "/?Action=ListResources&Version=2021-01-01"
	var resp listResponse
	if err := record.DoJSON(ctx, c.http, http.MethodPost, url, nil, map[string]any{}, &resp); err != nil {
		return nil, fmt.Errorf("调用 ListResources 失败: %w", err)
	}
	if resp.ResponseMetadata.Error != nil {
		return nil, fmt.Errorf("ListResources 返回错误 %s: %s", resp.ResponseMetadata.Error.Code, resp.ResponseMetadata.Error.Message)
	}
	return resp.Result.Resources, nil
}

// IsVKE 判断资源类型是否为火山托管 K8s（VKE）。
func IsVKE(resourceType string) bool {
	return strings.Contains(strings.ToUpper(resourceType), "VKE")
}

// IsECS 判断资源类型是否为云服务器（ECS）类。
func IsECS(resourceType string) bool {
	return strings.Contains(strings.ToUpper(resourceType), "ECS")
}

// IsVPC 判断资源类型是否为私有网络。
func IsVPC(resourceType string) bool {
	return strings.Contains(strings.ToUpper(resourceType), "VPC")
}

// IsRDS 判断资源类型是否为云数据库（RDS_MySQL/PostgreSQL 等）。
func IsRDS(resourceType string) bool {
	return strings.Contains(strings.ToUpper(resourceType), "RDS")
}

// IsCLB 判断资源类型是否为负载均衡（CLB/ALB/NLB）。
func IsCLB(resourceType string) bool {
	u := strings.ToUpper(resourceType)
	return strings.Contains(u, "CLB") || strings.Contains(u, "ALB") || strings.Contains(u, "NLB")
}
