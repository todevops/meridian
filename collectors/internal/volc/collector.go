// CloudControl 资源 → 标准发现记录的映射与采集器组装。
package volc

import (
	"context"
	"time"

	"collectors/internal/record"
)

// MapResource 把一个 CloudControl 资源映射为标准发现记录：
//   - ECS 类资源 → model_candidate=host，字段映射与阿里云采集器一致（cloud_provider=volc）；
//   - VKE 资源 → model_candidate=k8s_workload 占位记录，attributes 注记集群；
//   - 其他暂未建模的资源类型 → ok=false，由调用方跳过。
func MapResource(r Resource, now time.Time) (rec record.Record, ok bool) {
	cfg := r.Configuration
	if cfg == nil {
		cfg = map[string]any{}
	}
	if IsVKE(r.ResourceType) {
		cluster := record.StrField(cfg, "ClusterName", "Name", "ClusterId")
		if cluster == "" {
			cluster = r.ResourceID
		}
		return record.Record{
			Source:         Source,
			Collector:      CollectorName,
			ModelCandidate: "k8s_workload",
			Attributes: map[string]any{
				"cloud_provider": "volc",
				"resource_type":  r.ResourceType,
				"cluster":        cluster,
				"note":           "VKE 资源占位记录：集群清单经 CloudControl 登记，工作负载待 K8s 采集器接管",
			},
			OccurredAt: now,
		}, true
	}
	if IsVPC(r.ResourceType) {
		return record.Record{
			Source:         Source,
			Collector:      CollectorName,
			ModelCandidate: "cloud_vpc",
			Attributes: map[string]any{
				"cloud_provider": "volc",
				"vpc_id":         r.ResourceID,
				"name":           record.StrField(cfg, "VpcName", "VPCName", "Name", "ResourceName"),
				"cidr":           record.StrField(cfg, "CidrBlock", "CIDR", "Cidr"),
				"region":         record.StrField(cfg, "Region", "RegionId"),
				"status":         record.StrField(cfg, "Status", "State"),
				"tags":           record.FormatTags(r.Tags),
			},
			OccurredAt: now,
		}, true
	}
	if IsRDS(r.ResourceType) {
		return record.Record{
			Source:         Source,
			Collector:      CollectorName,
			ModelCandidate: "cloud_rds",
			Attributes: map[string]any{
				"cloud_provider": "volc",
				"db_instance_id": r.ResourceID,
				"name":           record.StrField(cfg, "InstanceName", "DBInstanceName", "Name"),
				"engine":         record.StrField(cfg, "DBEngine", "Engine"),
				"engine_version": record.StrField(cfg, "DBEngineVersion", "EngineVersion"),
				"spec":           record.StrField(cfg, "InstanceType", "Spec", "NodeSpec"),
				"region":         record.StrField(cfg, "Region", "RegionId"),
				"zone":           record.StrField(cfg, "ZoneId", "Zone"),
				"status":         record.StrField(cfg, "InstanceStatus", "Status", "State"),
				"tags":           record.FormatTags(r.Tags),
			},
			OccurredAt: now,
		}, true
	}
	if IsCLB(r.ResourceType) {
		return record.Record{
			Source:         Source,
			Collector:      CollectorName,
			ModelCandidate: "cloud_slb",
			Attributes: map[string]any{
				"cloud_provider": "volc",
				"slb_id":         r.ResourceID,
				"name":           record.StrField(cfg, "LoadBalancerName", "Name"),
				"vip":            record.StrField(cfg, "EipAddress", "Address", "Vip", "EniAddress"),
				"slb_type":       record.StrField(cfg, "Type", "LoadBalancerType", "Spec"),
				"region":         record.StrField(cfg, "Region", "RegionId"),
				"status":         record.StrField(cfg, "Status", "State"),
				"tags":           record.FormatTags(r.Tags),
			},
			OccurredAt: now,
		}, true
	}
	if !IsECS(r.ResourceType) {
		return record.Record{}, false
	}
	return record.Record{
		Source:         Source,
		Collector:      CollectorName,
		ModelCandidate: "host",
		Attributes: map[string]any{
			"host_type":         "cloud",
			"cloud_provider":    "volc",
			"cloud_instance_id": r.ResourceID,
			"ip":                record.StrField(cfg, "PrivateIpAddress", "PrivateIp", "MainPrivateIp"),
			"ident":             record.StrField(cfg, "InstanceName", "ResourceName", "Name"),
			"spec":              record.StrField(cfg, "InstanceType", "Spec", "Flavor"),
			"zone":              record.StrField(cfg, "ZoneId", "Zone"),
			"status":            record.StrField(cfg, "Status", "State"),
			"tags":              record.FormatTags(r.Tags),
		},
		OccurredAt: now,
	}, true
}

// Collector 是火山引擎 CloudControl 采集器。
type Collector struct {
	client *Client
	now    func() time.Time
}

// New 创建采集器。apiURL 支持 ":19006" 简写（默认指向 mock 平台约定端口）。
func New(apiURL string) *Collector {
	return &Collector{client: NewClient(apiURL), now: time.Now}
}

// Name 返回采集器名。
func (c *Collector) Name() string { return "volc" }

// Collect 拉取资源清单并映射为发现记录；暂未建模的资源类型跳过。
func (c *Collector) Collect(ctx context.Context) ([]record.Record, error) {
	resources, err := c.client.ListResources(ctx)
	if err != nil {
		return nil, err
	}
	recs := make([]record.Record, 0, len(resources))
	for _, r := range resources {
		if r.ResourceID == "" {
			continue
		}
		if rec, ok := MapResource(r, c.now()); ok {
			recs = append(recs, rec)
		}
	}
	return recs, nil
}
