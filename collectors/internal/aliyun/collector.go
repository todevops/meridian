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

// Collector 是阿里云采集器（ECS/VPC/RDS/SLB）。
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

// Collect 拉取 ECS/VPC/RDS/SLB 清单并映射为发现记录；无 ID 的条目直接跳过。
func (c *Collector) Collect(ctx context.Context) ([]record.Record, error) {
	now := c.now()
	var recs []record.Record

	instances, err := c.client.ListInstances(ctx)
	if err != nil {
		return nil, err
	}
	for _, ins := range instances {
		if ins.InstanceID == "" {
			continue
		}
		recs = append(recs, MapInstance(ins, now))
	}

	vpcs, err := c.client.ListVPCs(ctx)
	if err != nil {
		return nil, err
	}
	for _, v := range vpcs {
		if v.VpcID == "" {
			continue
		}
		recs = append(recs, MapVPC(v, now))
	}

	dbs, err := c.client.ListRDSInstances(ctx)
	if err != nil {
		return nil, err
	}
	for _, d := range dbs {
		if d.DBInstanceID == "" {
			continue
		}
		recs = append(recs, MapRDS(d, now))
	}

	lbs, err := c.client.ListLoadBalancers(ctx)
	if err != nil {
		return nil, err
	}
	for _, lb := range lbs {
		if lb.LoadBalancerID == "" {
			continue
		}
		recs = append(recs, MapSLB(lb, now))
	}
	return recs, nil
}

// MapVPC 把一个 VPC 映射为标准发现记录（model_candidate=cloud_vpc）。
func MapVPC(v VPC, now time.Time) record.Record {
	return record.Record{
		Source:         Source,
		Collector:      CollectorName,
		ModelCandidate: "cloud_vpc",
		Attributes: map[string]any{
			"cloud_provider": "aliyun",
			"vpc_id":         v.VpcID,
			"name":           v.VpcName,
			"cidr":           v.CidrBlock,
			"region":         v.RegionID,
			"status":         v.Status,
		},
		OccurredAt: now,
	}
}

// MapRDS 把一个 RDS 实例映射为标准发现记录（model_candidate=cloud_rds）。
func MapRDS(d RDSInstance, now time.Time) record.Record {
	return record.Record{
		Source:         Source,
		Collector:      CollectorName,
		ModelCandidate: "cloud_rds",
		Attributes: map[string]any{
			"cloud_provider": "aliyun",
			"db_instance_id": d.DBInstanceID,
			"name":           d.Description,
			"engine":         d.Engine,
			"engine_version": d.EngineVersion,
			"spec":           d.Class,
			"region":         d.RegionID,
			"zone":           d.ZoneID,
			"status":         d.Status,
		},
		OccurredAt: now,
	}
}

// MapSLB 把一个 SLB 实例映射为标准发现记录（model_candidate=cloud_slb）。
func MapSLB(lb LoadBalancer, now time.Time) record.Record {
	return record.Record{
		Source:         Source,
		Collector:      CollectorName,
		ModelCandidate: "cloud_slb",
		Attributes: map[string]any{
			"cloud_provider": "aliyun",
			"slb_id":         lb.LoadBalancerID,
			"name":           lb.LoadBalancerName,
			"vip":            lb.Address,
			"slb_type":       lb.Spec,
			"region":         lb.RegionID,
			"status":         lb.Status,
		},
		OccurredAt: now,
	}
}
