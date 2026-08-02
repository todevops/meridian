// Package librenms 实现 LibreNMS 采集器：
// 调用 /api/v0/devices 拉取网络设备清单，映射为 network_device 标准发现记录。
package librenms

import (
	"context"
	"errors"
	"net/http"
	"time"

	"collectors/internal/record"
)

const (
	// Source 是发现来源系统标识。
	Source = "librenms"
	// CollectorName 是采集器标识。
	CollectorName = "librenms-device-collector"
)

// devicesResponse 对应 LibreNMS /api/v0/devices 响应。
type devicesResponse struct {
	Status  string           `json:"status"`
	Message string           `json:"message"`
	Devices []map[string]any `json:"devices"`
}

// Client 是 LibreNMS API 客户端。
type Client struct {
	baseURL string
	token   string
	http    *http.Client
}

// NewClient 创建客户端。apiURL 支持 ":19003" 简写；token 即 X-Auth-Token。
func NewClient(apiURL, token string) *Client {
	return &Client{
		baseURL: record.NormalizeBaseURL(apiURL),
		token:   token,
		http:    &http.Client{Timeout: 30 * time.Second},
	}
}

// ListDevices 拉取设备清单。token 未配置时直接报错（mock 亦要求 X-Auth-Token 非空）。
func (c *Client) ListDevices(ctx context.Context) ([]map[string]any, error) {
	if c.token == "" {
		return nil, errors.New("LIBRENMS_API_TOKEN 未配置（对接 mock 平台时任意非空值即可）")
	}
	var resp devicesResponse
	headers := map[string]string{"X-Auth-Token": c.token}
	if err := record.DoJSON(ctx, c.http, http.MethodGet, c.baseURL+"/api/v0/devices", headers, nil, &resp); err != nil {
		return nil, err
	}
	if resp.Status != "" && resp.Status != "ok" {
		return nil, errors.New("LibreNMS 返回状态 " + resp.Status + ": " + resp.Message)
	}
	return resp.Devices, nil
}

// MapDevice 把一台 LibreNMS 设备映射为标准发现记录（model_candidate=network_device）。
// 属性编码对齐种子模型：sysName→name、ip→mgmt_ip、serial→serial_no；
// 字段命名容忍 sysName/sysname 等差异；model 缺省回退 hardware。
// ip 与 sysname 均为空时返回 ok=false（无法调和的垃圾数据，跳过）。
func MapDevice(d map[string]any, now time.Time) (rec record.Record, ok bool) {
	sysname := record.StrField(d, "sysName", "sysname", "hostname")
	ip := record.StrField(d, "ip")
	if sysname == "" && ip == "" {
		return record.Record{}, false
	}
	model := record.StrField(d, "model")
	if model == "" {
		model = record.StrField(d, "hardware")
	}
	return record.Record{
		Source:         Source,
		Collector:      CollectorName,
		ModelCandidate: "network_device",
		Attributes: map[string]any{
			"name":      sysname,
			"mgmt_ip":   ip,
			"vendor":    record.StrField(d, "vendor"),
			"model":     model,
			"serial_no": record.StrField(d, "serial"),
			"source":    "librenms",
		},
		OccurredAt: now,
	}, true
}

// Collector 是 LibreNMS 网络设备采集器。
type Collector struct {
	client *Client
	now    func() time.Time
}

// New 创建采集器。apiURL 支持 ":19003" 简写（默认指向 mock 平台约定端口）。
func New(apiURL, token string) *Collector {
	return &Collector{client: NewClient(apiURL, token), now: time.Now}
}

// Name 返回采集器名。
func (c *Collector) Name() string { return "librenms" }

// Collect 拉取设备清单并映射为发现记录。
func (c *Collector) Collect(ctx context.Context) ([]record.Record, error) {
	devices, err := c.client.ListDevices(ctx)
	if err != nil {
		return nil, err
	}
	recs := make([]record.Record, 0, len(devices))
	for _, d := range devices {
		if rec, ok := MapDevice(d, c.now()); ok {
			recs = append(recs, rec)
		}
	}
	return recs, nil
}
