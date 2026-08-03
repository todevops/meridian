// Package aliyun 实现阿里云 ECS 采集器：
// 调用 DescribeInstances 风格接口拉取 ECS 清单，映射为标准发现记录（model_candidate=host）。
// 解析兼容官方响应包装（{"Instances":{"Instance":[...]}}）与 mock 简化形状（顶层数组）。
package aliyun

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
	Source = "aliyun"
	// CollectorName 是采集器标识。
	CollectorName = "aliyun-cloud-collector"
)

// Instance 对应一台 ECS 实例（字段名与阿里云 API 一致）。
type Instance struct {
	InstanceID   string
	InstanceName string
	Status       string
	InstanceType string
	ZoneID       string
	PrivateIPs   []string
	Tags         map[string]string
}

// UnmarshalJSON 兼容 PrivateIpAddress 的三种形状：
// 字符串、"IpAddress" 数组包装（官方）、字符串数组；Tags 兼容
// {"Tag":[{"TagKey","TagValue"}]}（官方）、{"k":"v"}、[{"Key","Value"}] 与 ["k=v"]。
func (i *Instance) UnmarshalJSON(data []byte) error {
	var raw struct {
		InstanceID   string          `json:"InstanceId"`
		InstanceName string          `json:"InstanceName"`
		Status       string          `json:"Status"`
		InstanceType string          `json:"InstanceType"`
		ZoneID       string          `json:"ZoneId"`
		PrivateIP    json.RawMessage `json:"PrivateIpAddress"`
		Tags         json.RawMessage `json:"Tags"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	i.InstanceID = raw.InstanceID
	i.InstanceName = raw.InstanceName
	i.Status = raw.Status
	i.InstanceType = raw.InstanceType
	i.ZoneID = raw.ZoneID
	i.PrivateIPs = parseStringList(raw.PrivateIP)
	i.Tags = parseTags(raw.Tags)
	return nil
}

// parseStringList 把多种形状的 IP 字段解析为字符串数组。
func parseStringList(raw json.RawMessage) []string {
	trim := bytes.TrimSpace(raw)
	if len(trim) == 0 || string(trim) == "null" {
		return nil
	}
	var single string
	if err := json.Unmarshal(trim, &single); err == nil {
		if single == "" {
			return nil
		}
		return []string{single}
	}
	var list []string
	if err := json.Unmarshal(trim, &list); err == nil {
		return list
	}
	var wrap struct {
		IPAddress []string `json:"IpAddress"`
	}
	if err := json.Unmarshal(trim, &wrap); err == nil {
		return wrap.IPAddress
	}
	return nil
}

// parseTags 把多种形状的标签字段解析为键值对。
func parseTags(raw json.RawMessage) map[string]string {
	trim := bytes.TrimSpace(raw)
	if len(trim) == 0 || string(trim) == "null" {
		return nil
	}
	var kv map[string]string
	if err := json.Unmarshal(trim, &kv); err == nil {
		return kv
	}
	var official struct {
		Tag []struct {
			TagKey   string `json:"TagKey"`
			TagValue string `json:"TagValue"`
		} `json:"Tag"`
	}
	if err := json.Unmarshal(trim, &official); err == nil && official.Tag != nil {
		out := map[string]string{}
		for _, t := range official.Tag {
			out[t.TagKey] = t.TagValue
		}
		return out
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
	var strs []string
	if err := json.Unmarshal(trim, &strs); err == nil {
		out := map[string]string{}
		for _, s := range strs {
			k, v, _ := strings.Cut(s, "=")
			out[k] = v
		}
		return out
	}
	return nil
}

// Client 是阿里云 ECS API 客户端。
type Client struct {
	baseURL string
	http    *http.Client
}

// NewClient 创建客户端。apiURL 支持 ":19005" 简写。
func NewClient(apiURL string) *Client {
	return &Client{
		baseURL: record.NormalizeBaseURL(apiURL),
		http:    &http.Client{Timeout: 30 * time.Second},
	}
}

// ListInstances 拉取 ECS 实例清单（单页拉取，PageSize 500 覆盖 mock 与中小规模环境）。
func (c *Client) ListInstances(ctx context.Context) ([]Instance, error) {
	url := c.baseURL + "/?Action=DescribeInstances&PageSize=500&PageNumber=1"
	var raw json.RawMessage
	if err := record.DoJSON(ctx, c.http, http.MethodGet, url, nil, nil, &raw); err != nil {
		return nil, fmt.Errorf("调用 DescribeInstances 失败: %w", err)
	}
	instances, err := parseInstances(raw)
	if err != nil {
		return nil, fmt.Errorf("解析 DescribeInstances 响应失败: %w", err)
	}
	return instances, nil
}

// parseInstances 兼容顶层数组（mock 简化形状）与官方 Instances.Instance 双层包装。
func parseInstances(raw json.RawMessage) ([]Instance, error) {
	trim := bytes.TrimSpace(raw)
	if len(trim) == 0 {
		return nil, nil
	}
	if trim[0] == '[' {
		var out []Instance
		if err := json.Unmarshal(trim, &out); err != nil {
			return nil, err
		}
		return out, nil
	}
	var wrap struct {
		Instances json.RawMessage `json:"Instances"`
	}
	if err := json.Unmarshal(trim, &wrap); err != nil {
		return nil, err
	}
	inner := bytes.TrimSpace(wrap.Instances)
	if len(inner) == 0 || string(inner) == "null" {
		return nil, nil
	}
	if inner[0] == '[' {
		var out []Instance
		if err := json.Unmarshal(inner, &out); err != nil {
			return nil, err
		}
		return out, nil
	}
	var nest struct {
		Instance []Instance `json:"Instance"`
	}
	if err := json.Unmarshal(inner, &nest); err != nil {
		return nil, err
	}
	return nest.Instance, nil
}

// VPC 对应一个私有网络（字段名与阿里云 API 一致）。
type VPC struct {
	VpcID     string `json:"VpcId"`
	VpcName   string `json:"VpcName"`
	CidrBlock string `json:"CidrBlock"`
	RegionID  string `json:"RegionId"`
	Status    string `json:"Status"`
}

// RDSInstance 对应一个云数据库实例。
type RDSInstance struct {
	DBInstanceID  string `json:"DBInstanceId"`
	Description   string `json:"DBInstanceDescription"`
	Engine        string `json:"Engine"`
	EngineVersion string `json:"EngineVersion"`
	Class         string `json:"DBInstanceClass"`
	RegionID      string `json:"RegionId"`
	ZoneID        string `json:"ZoneId"`
	Status        string `json:"DBInstanceStatus"`
}

// LoadBalancer 对应一个负载均衡实例。
type LoadBalancer struct {
	LoadBalancerID   string `json:"LoadBalancerId"`
	LoadBalancerName string `json:"LoadBalancerName"`
	Address          string `json:"Address"`
	Spec             string `json:"LoadBalancerSpec"`
	RegionID         string `json:"RegionId"`
	Status           string `json:"LoadBalancerStatus"`
}

// list 通用拉取：按 Action 调阿里云 RPC 风格接口，解包 wrapperKey.wrapperItem 数组。
func (c *Client) list(ctx context.Context, action, wrapperKey, wrapperItem string, out any) error {
	url := c.baseURL + "/?Action=" + action + "&PageSize=500&PageNumber=1"
	var raw json.RawMessage
	if err := record.DoJSON(ctx, c.http, http.MethodGet, url, nil, nil, &raw); err != nil {
		return fmt.Errorf("调用 %s 失败: %w", action, err)
	}
	trim := bytes.TrimSpace(raw)
	if len(trim) == 0 {
		return nil
	}
	if trim[0] == '[' { // mock 简化形状：顶层数组
		return json.Unmarshal(trim, out)
	}
	var wrap map[string]json.RawMessage
	if err := json.Unmarshal(trim, &wrap); err != nil {
		return err
	}
	inner, ok := wrap[wrapperKey]
	if !ok {
		return nil
	}
	trim = bytes.TrimSpace(inner)
	if len(trim) == 0 || string(trim) == "null" {
		return nil
	}
	if trim[0] == '[' {
		return json.Unmarshal(trim, out)
	}
	var itemWrap map[string]json.RawMessage
	if err := json.Unmarshal(trim, &itemWrap); err != nil {
		return err
	}
	items, ok := itemWrap[wrapperItem]
	if !ok {
		return nil
	}
	return json.Unmarshal(items, out)
}

// ListVPCs 拉取 VPC 清单。
func (c *Client) ListVPCs(ctx context.Context) ([]VPC, error) {
	var out []VPC
	if err := c.list(ctx, "DescribeVpcs", "Vpcs", "Vpc", &out); err != nil {
		return nil, err
	}
	return out, nil
}

// ListRDSInstances 拉取 RDS 实例清单。
func (c *Client) ListRDSInstances(ctx context.Context) ([]RDSInstance, error) {
	var out []RDSInstance
	if err := c.list(ctx, "DescribeDBInstances", "Items", "DBInstance", &out); err != nil {
		return nil, err
	}
	return out, nil
}

// ListLoadBalancers 拉取 SLB 实例清单。
func (c *Client) ListLoadBalancers(ctx context.Context) ([]LoadBalancer, error) {
	var out []LoadBalancer
	if err := c.list(ctx, "DescribeLoadBalancers", "LoadBalancers", "LoadBalancer", &out); err != nil {
		return nil, err
	}
	return out, nil
}
