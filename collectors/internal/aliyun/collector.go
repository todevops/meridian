// ECS 实例 → 标准发现记录的映射与采集器组装。
package aliyun

import (
	"context"
	"time"

	"collectors/internal/record"
)

// MapInstance 把一台 ECS 实例映射为标准发现记录（model_candidate=host）。
// ip 取首个私网地址；tags 规范化为键值对。
func MapInstance(ins Instance, now time.Time) record.Record {
	ip := ""
	if len(ins.PrivateIPs) > 0 {
		ip = ins.PrivateIPs[0]
	}
	return record.Record{
		Source:         Source,
		Collector:      CollectorName,
		ModelCandidate: "host",
		Attributes: map[string]any{
			"host_type":         "cloud",
			"cloud_provider":    "aliyun",
			"cloud_instance_id": ins.InstanceID,
			"ip":                ip,
			"ident":             ins.InstanceName,
			"spec":              ins.InstanceType,
			"zone":              ins.ZoneID,
			"status":            ins.Status,
			"tags":              record.FormatTags(ins.Tags),
		},
		OccurredAt: now,
	}
}

// Collector 是阿里云 ECS 采集器。
type Collector struct {
	client *Client
	now    func() time.Time
}

// New 创建采集器。apiURL 支持 ":19005" 简写（默认指向 mock 平台约定端口）。
func New(apiURL string) *Collector {
	return &Collector{client: NewClient(apiURL), now: time.Now}
}

// Name 返回采集器名。
func (c *Collector) Name() string { return "aliyun" }

// Collect 拉取 ECS 清单并映射为发现记录；无实例 ID 的条目直接跳过。
func (c *Collector) Collect(ctx context.Context) ([]record.Record, error) {
	instances, err := c.client.ListInstances(ctx)
	if err != nil {
		return nil, err
	}
	recs := make([]record.Record, 0, len(instances))
	for _, ins := range instances {
		if ins.InstanceID == "" {
			continue
		}
		recs = append(recs, MapInstance(ins, c.now()))
	}
	return recs, nil
}
