// Package vsphere 实现 vSphere 采集器：
// 经 govmomi 从 vCenter 拉取集群/ESXi 主机/虚拟机三类对象，映射为标准发现记录，
// 关系属性（parent_cluster_moid / parent_host_uuid）随记录携带。
package vsphere

import (
	"context"
	"time"

	"collectors/internal/record"
)

const (
	// Source 是发现来源系统标识。
	Source = "vsphere"
	// CollectorName 是采集器标识。
	CollectorName = "vsphere-collector"
)

// Collector 是 vSphere 采集器。
type Collector struct {
	client *Client
	now    func() time.Time
	logf   func(format string, args ...any)
}

// New 创建采集器。rawURL 支持 ":19007" 简写（默认指向 vcsim 约定端口）；
// insecure 对接自签名证书/vcsim 时传 true；logf 可为 nil（仅用于容错告警）。
func New(rawURL, username, password string, insecure bool, logf func(format string, args ...any)) (*Collector, error) {
	client, err := NewClient(rawURL, username, password, insecure)
	if err != nil {
		return nil, err
	}
	if logf == nil {
		logf = func(string, ...any) {}
	}
	return &Collector{client: client, now: time.Now, logf: logf}, nil
}

// Name 返回采集器名。
func (c *Collector) Name() string { return "vsphere" }

// Collect 拉取 vCenter 全量清单并映射为发现记录：
// esxi_cluster → esxi_host → virtual_machine 顺序输出（父在前，便于消费方按序处理）。
func (c *Collector) Collect(ctx context.Context) ([]record.Record, error) {
	inv, err := c.client.Fetch(ctx)
	if err != nil {
		return nil, err
	}
	at := c.now()

	recs := make([]record.Record, 0, len(inv.Clusters)+len(inv.Hosts)+len(inv.VMs))
	for _, cl := range inv.Clusters {
		recs = append(recs, MapCluster(cl, at))
	}

	// VM 的所属主机以 moid 引用，翻译为 hardware_uuid 随记录携带。
	uuidByMOID := make(map[string]string, len(inv.Hosts))
	for _, h := range inv.Hosts {
		uuidByMOID[h.MOID] = h.HardwareUUID
	}
	for _, h := range inv.Hosts {
		rec, ok := MapHost(h, at)
		if !ok {
			c.logf("跳过无 hardware_uuid 的 ESXi 主机 %s（moid=%s），无法调和", h.Name, h.MOID)
			continue
		}
		recs = append(recs, rec)
	}
	for _, vm := range inv.VMs {
		rec, ok := MapVM(vm, uuidByMOID[vm.ParentHostMOID], at)
		if !ok {
			c.logf("跳过无 instance_uuid 的虚拟机 %s，无法调和", vm.Name)
			continue
		}
		recs = append(recs, rec)
	}
	return recs, nil
}

// MapCluster 把一个集群映射为 esxi_cluster 发现记录（moid 作调和主键）。
func MapCluster(cl ClusterInfo, now time.Time) record.Record {
	return record.Record{
		Source:         Source,
		Collector:      CollectorName,
		ModelCandidate: "esxi_cluster",
		Attributes: map[string]any{
			"name":       cl.Name,
			"moid":       cl.MOID,
			"host_count": cl.HostCount,
			"source":     Source,
		},
		OccurredAt: now,
	}
}

// MapHost 把一台 ESXi 主机映射为 esxi_host 发现记录（hardware_uuid 作调和主键）；
// hardware_uuid 为空返回 ok=false（无法调和，由调用方跳过并告警）。
func MapHost(h HostInfo, now time.Time) (rec record.Record, ok bool) {
	if h.HardwareUUID == "" {
		return record.Record{}, false
	}
	attrs := map[string]any{
		"name":          h.Name,
		"hardware_uuid": h.HardwareUUID,
		"model":         h.Model,
		"cpu_cores":     h.CPUCores,
		"mem_gb":        (h.MemMB + 512) / 1024,
		"source":        Source,
	}
	if h.ParentClusterMOID != "" {
		attrs["parent_cluster_moid"] = h.ParentClusterMOID
	}
	return record.Record{
		Source:         Source,
		Collector:      CollectorName,
		ModelCandidate: "esxi_host",
		Attributes:     attrs,
		OccurredAt:     now,
	}, true
}

// MapVM 把一台虚拟机映射为 virtual_machine 发现记录（instance_uuid 作调和主键）；
// 关电或无 VMware Tools 的 VM 无 IP/OS，属正常情况，对应属性省略；
// instance_uuid 为空返回 ok=false（无法调和，由调用方跳过并告警）。
func MapVM(vm VMInfo, parentHostUUID string, now time.Time) (rec record.Record, ok bool) {
	if vm.InstanceUUID == "" {
		return record.Record{}, false
	}
	attrs := map[string]any{
		"name":          vm.Name,
		"instance_uuid": vm.InstanceUUID,
		"vcpu":          vm.VCPU,
		"memory_gb":     (vm.MemMB + 512) / 1024,
		"power_state":   vm.PowerState,
		"source":        Source,
	}
	if vm.IP != "" {
		attrs["ip"] = vm.IP
	}
	if vm.OS != "" {
		attrs["os"] = vm.OS
	}
	if parentHostUUID != "" {
		attrs["parent_host_uuid"] = parentHostUUID
	}
	return record.Record{
		Source:         Source,
		Collector:      CollectorName,
		ModelCandidate: "virtual_machine",
		Attributes:     attrs,
		OccurredAt:     now,
	}, true
}
