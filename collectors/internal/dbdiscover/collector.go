// Package dbdiscover 实现数据库/中间件实例发现采集器：
// 查询 TSDB（Prometheus 协议）instance 标签值，解析 host:port，
// 映射为 db_instance 标准发现记录；启动时确保 db_instance 模型调和键已配置。
package dbdiscover

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"collectors/internal/record"
)

const (
	// Source 是发现来源系统标识。
	Source = "tsdb"
	// CollectorName 是采集器标识。
	CollectorName = "db-discoverer"
)

// metrics 是要探测的指标 → 实例类型映射（顺序固定，保证输出确定性）。
var metrics = []struct {
	Metric string
	Type   string
}{
	{"mysql_up", "mysql"},
	{"redis_up", "redis"},
	{"kafka_brokers", "kafka"},
	{"elasticsearch_cluster_health", "elasticsearch"},
}

// labelValuesResponse 对应 Prometheus label values API 响应。
type labelValuesResponse struct {
	Status string   `json:"status"`
	Data   []string `json:"data"`
	Error  string   `json:"error"`
}

// Client 是 TSDB（Prometheus 协议）客户端。
type Client struct {
	baseURL string
	http    *http.Client
}

// NewClient 创建客户端。apiURL 支持 ":19004" 简写。
func NewClient(apiURL string) *Client {
	return &Client{
		baseURL: record.NormalizeBaseURL(apiURL),
		http:    &http.Client{Timeout: 30 * time.Second},
	}
}

// InstanceValues 查询指定指标的 instance 标签值列表（形如 host:port）。
func (c *Client) InstanceValues(ctx context.Context, metric string) ([]string, error) {
	u := fmt.Sprintf("%s/api/v1/label/instance/values?match[]=%s", c.baseURL, url.QueryEscape(metric))
	var resp labelValuesResponse
	if err := record.DoJSON(ctx, c.http, http.MethodGet, u, nil, nil, &resp); err != nil {
		return nil, err
	}
	// 官方返回 status=success；容忍 mock 简写 ok；两种之外的非空状态视为错误。
	if resp.Status != "" && resp.Status != "success" && resp.Status != "ok" {
		return nil, fmt.Errorf("TSDB 返回状态 %q: %s", resp.Status, resp.Error)
	}
	return resp.Data, nil
}

// splitInstance 把 instance 标签值解析为 ip 与 port；无端口时 port 为空串。
func splitInstance(v string) (ip, port string) {
	host, p, err := net.SplitHostPort(v)
	if err != nil {
		return v, ""
	}
	return host, p
}

// Collector 是数据库/中间件实例发现采集器。
type Collector struct {
	client    *Client
	apiURL    string // CMDB API 地址（模型调和键确保用）
	cmdbToken string // CMDB Bearer 令牌（无认证环境为空串）
	dryRun    bool
	now       func() time.Time
	logf      func(format string, args ...any)
}

// New 创建采集器。tsdbURL 支持 ":19004" 简写；apiURL 为 CMDB API 地址；
// cmdbToken 为 CMDB 会话令牌（无认证环境传空串）；dryRun 为 true 时模型确保只打印不变更。
func New(tsdbURL, apiURL, cmdbToken string, dryRun bool, logf func(format string, args ...any)) *Collector {
	return &Collector{
		client:    NewClient(tsdbURL),
		apiURL:    record.NormalizeBaseURL(apiURL),
		cmdbToken: cmdbToken,
		dryRun:    dryRun,
		now:       time.Now,
		logf:      logf,
	}
}

// Name 返回采集器名。
func (c *Collector) Name() string { return "dbdiscover" }

// Collect 先确保 db_instance 模型调和键，再逐指标拉取实例并映射为发现记录。
func (c *Collector) Collect(ctx context.Context) ([]record.Record, error) {
	if err := c.ensureModel(ctx); err != nil {
		return nil, err
	}
	var recs []record.Record
	for _, m := range metrics {
		values, err := c.client.InstanceValues(ctx, m.Metric)
		if err != nil {
			return nil, fmt.Errorf("查询指标 %s 的实例失败: %w", m.Metric, err)
		}
		for _, v := range values {
			ip, port := splitInstance(v)
			if ip == "" {
				continue
			}
			attrs := map[string]any{
				"instance_addr":  v, // 原始 instance 标签（ip:port），实例级唯一，作调和主键
				"component_type": m.Type,
				"ip":             ip,
				"source":         "tsdb_label",
			}
			// 模型 port 为数值类型，解析失败（无端口）则不写该属性
			if n, err := strconv.Atoi(port); err == nil && n > 0 {
				attrs["port"] = n
			}
			recs = append(recs, record.Record{
				Source:         Source,
				Collector:      CollectorName,
				ModelCandidate: "db_instance",
				Attributes:     attrs,
				OccurredAt:     c.now(),
			})
		}
	}
	return recs, nil
}
