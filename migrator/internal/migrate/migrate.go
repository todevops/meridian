// 迁移编排：按方案 13.1 节顺序执行实体迁移。
// 顺序：确保模型 → sites→room → racks→rack → devices→network_device →
// vlans→vlan（同时建立 NetBox VLAN ID → CMDB CI ID 映射）→ virtual-machines →
// prefixes→IPAM 前缀（按包含关系推导 parent_id 避免同级重叠 409）→
// ip-addresses→IPAM IP（最小包含前缀归属，缺失则自动建 /24）。
package migrate

import (
	"context"
	"fmt"
	"math"
	"net/netip"
	"sort"
	"strconv"
	"strings"
	"time"

	"migrator/internal/cmdb"
	"migrator/internal/netbox"
)

// Migrator 聚合 NetBox 读取端与 CMDB 写入端。
type Migrator struct {
	nb   *netbox.Client
	cmdb *cmdb.Client
}

// New 创建迁移器。
func New(nb *netbox.Client, cm *cmdb.Client) *Migrator {
	return &Migrator{nb: nb, cmdb: cm}
}

// createdPrefix 记录已写入 CMDB 的前缀（用于 parent 推导与 IP 归属）。
type createdPrefix struct {
	id     string
	prefix netip.Prefix // 已归一化（Masked）
}

// prefixTracker 维护已创建前缀集合，提供包含关系查询。
type prefixTracker struct {
	items []createdPrefix
}

// add 登记一个已创建前缀。
func (t *prefixTracker) add(id string, p netip.Prefix) {
	t.items = append(t.items, createdPrefix{id: id, prefix: p.Masked()})
}

// findContaining 返回包含 addr 的最小（掩码最长）前缀的 CMDB ID。
func (t *prefixTracker) findContaining(addr netip.Addr) (string, bool) {
	best, bestBits := -1, -1
	for i, cp := range t.items {
		if cp.prefix.Contains(addr) && cp.prefix.Bits() > bestBits {
			best, bestBits = i, cp.prefix.Bits()
		}
	}
	if best < 0 {
		return "", false
	}
	return t.items[best].id, true
}

// findParent 返回 p 的父前缀（已创建、严格包含 p、掩码最长者）的 CMDB ID。
// CIDR 之间只存在"互不相交"或"一方包含另一方"，按此推导可保证同级不重叠。
func (t *prefixTracker) findParent(p netip.Prefix) (string, bool) {
	p = p.Masked()
	best, bestBits := -1, -1
	for i, cp := range t.items {
		if cp.prefix.Bits() < p.Bits() && cp.prefix.Contains(p.Addr()) && cp.prefix.Bits() > bestBits {
			best, bestBits = i, cp.prefix.Bits()
		}
	}
	if best < 0 {
		return "", false
	}
	return t.items[best].id, true
}

// Run 执行完整迁移流程。模型确保失败视为致命错误（无法继续）；
// 单类实体拉取失败与单条记录写入失败记入报告后继续后续实体。
func (m *Migrator) Run(ctx context.Context, netboxURL, cmdbURL string) (*Report, error) {
	report := &Report{
		StartedAt:    time.Now(),
		NetboxAPIURL: netboxURL,
		CMDBAPIURL:   cmdbURL,
	}

	// 第 1 步：确保迁移依赖的模型存在（GET 按 code 查无则 POST 创建）。
	for _, def := range cmdb.RequiredModels() {
		created, err := m.cmdb.EnsureModel(ctx, def)
		if err != nil {
			m.finish(report)
			return report, fmt.Errorf("确保模型 %q 失败: %w", def.Code, err)
		}
		status := "existing"
		if created {
			status = "created"
		}
		report.Models = append(report.Models, ModelReport{Code: def.Code, Status: status})
	}

	// 第 2 步：按依赖顺序迁移实体。
	m.migrateSites(ctx, report)
	m.migrateRacks(ctx, report)
	m.migrateDevices(ctx, report)
	m.migrateVLANs(ctx, report)
	m.migrateVMs(ctx, report)
	tracker := m.migratePrefixes(ctx, report)
	m.migrateIPs(ctx, report, tracker)

	m.finish(report)
	return report, nil
}

// finish 结算报告耗时。
func (m *Migrator) finish(report *Report) {
	report.FinishedAt = time.Now()
	report.DurationSeconds = math.Round(report.FinishedAt.Sub(report.StartedAt).Seconds()*100) / 100
}

// migrateSites 站点 → room CI。
func (m *Migrator) migrateSites(ctx context.Context, report *Report) {
	ent := report.entity("sites", "站点→机房")
	sites, err := m.nb.ListSites(ctx)
	if err != nil {
		ent.recordFetchError(err)
		return
	}
	ent.Fetched = len(sites)
	for _, s := range sites {
		code := s.Slug
		if code == "" {
			code = fmt.Sprintf("site-%d", s.ID) // slug 缺失时兜底，保证必填 code 有值
		}
		attrs := map[string]any{
			"name":      s.Name,
			"code":      code,
			"netbox_id": strconv.Itoa(s.ID),
		}
		if s.PhysicalAddress != "" {
			attrs["address"] = s.PhysicalAddress
		}
		if _, err := m.cmdb.CreateCI(ctx, "room", attrs); err != nil {
			ent.recordFailure(strconv.Itoa(s.ID), s.Name, err)
			continue
		}
		ent.recordSuccess()
	}
}

// migrateRacks 机架 → rack CI（netbox_site_id 留痕所属站点）。
func (m *Migrator) migrateRacks(ctx context.Context, report *Report) {
	ent := report.entity("racks", "机架→机柜")
	racks, err := m.nb.ListRacks(ctx)
	if err != nil {
		ent.recordFetchError(err)
		return
	}
	ent.Fetched = len(racks)
	for _, r := range racks {
		name := r.Name
		if name == "" {
			name = fmt.Sprintf("rack-%d", r.ID)
		}
		attrs := map[string]any{
			"name":      name,
			"netbox_id": strconv.Itoa(r.ID),
		}
		if r.UHeight > 0 {
			attrs["u_capacity"] = r.UHeight
		}
		if r.Site != nil {
			attrs["netbox_site_id"] = strconv.Itoa(r.Site.ID)
		}
		if _, err := m.cmdb.CreateCI(ctx, "rack", attrs); err != nil {
			ent.recordFailure(strconv.Itoa(r.ID), name, err)
			continue
		}
		ent.recordSuccess()
	}
}

// migrateDevices 设备 → network_device CI（型号/厂商/管理 IP/序列号映射）。
func (m *Migrator) migrateDevices(ctx context.Context, report *Report) {
	ent := report.entity("devices", "设备→网络设备")
	devices, err := m.nb.ListDevices(ctx)
	if err != nil {
		ent.recordFetchError(err)
		return
	}
	ent.Fetched = len(devices)
	for _, d := range devices {
		name := d.Name
		if name == "" {
			name = fmt.Sprintf("device-%d", d.ID)
		}
		attrs := map[string]any{
			"name":      name,
			"netbox_id": strconv.Itoa(d.ID),
		}
		if d.Serial != "" {
			attrs["serial_no"] = d.Serial
		}
		if d.DeviceType != nil {
			if d.DeviceType.Model != "" {
				attrs["model"] = d.DeviceType.Model
			}
			if d.DeviceType.Manufacturer != nil && d.DeviceType.Manufacturer.Name != "" {
				attrs["vendor"] = d.DeviceType.Manufacturer.Name
			}
		}
		// primary_ip4 可能缺失（NetBox 允许）；掩码部分剥离后作为管理 IP。
		if d.PrimaryIP4 != nil && d.PrimaryIP4.Address != "" {
			if p, err := netip.ParsePrefix(d.PrimaryIP4.Address); err == nil {
				attrs["mgmt_ip"] = p.Addr().String()
			}
		}
		if d.Rack != nil {
			attrs["netbox_rack_id"] = strconv.Itoa(d.Rack.ID)
		}
		if _, err := m.cmdb.CreateCI(ctx, "network_device", attrs); err != nil {
			ent.recordFailure(strconv.Itoa(d.ID), name, err)
			continue
		}
		ent.recordSuccess()
	}
}

// migrateVLANs VLAN → vlan CI。
func (m *Migrator) migrateVLANs(ctx context.Context, report *Report) {
	ent := report.entity("vlans", "VLAN→VLAN CI")
	vlans, err := m.nb.ListVLANs(ctx)
	if err != nil {
		ent.recordFetchError(err)
		return
	}
	ent.Fetched = len(vlans)
	for _, v := range vlans {
		name := v.Name
		if name == "" {
			name = fmt.Sprintf("vlan-%d", v.VID)
		}
		attrs := map[string]any{
			"vid":       float64(v.VID),
			"name":      name,
			"netbox_id": strconv.Itoa(v.ID),
		}
		if v.Description != "" {
			attrs["description"] = v.Description
		}
		if _, err := m.cmdb.CreateCI(ctx, "vlan", attrs); err != nil {
			ent.recordFailure(strconv.Itoa(v.ID), name, err)
			continue
		}
		ent.recordSuccess()
	}
}

// migrateVMs 虚拟机 → virtual_machine CI。
// instance_uuid 用 netbox-vm-<id> 合成（NetBox VM 无 UUID 概念，迁移后由 vCenter 采集器接管）。
// memory 单位 MB → GB；status 映射到模型枚举 poweredOn/poweredOff/suspended。
func (m *Migrator) migrateVMs(ctx context.Context, report *Report) {
	ent := report.entity("virtual_machines", "虚拟机→VM CI")
	vms, err := m.nb.ListVirtualMachines(ctx)
	if err != nil {
		ent.recordFetchError(err)
		return
	}
	ent.Fetched = len(vms)
	for _, v := range vms {
		name := v.Name
		if name == "" {
			name = fmt.Sprintf("vm-%d", v.ID)
		}
		attrs := map[string]any{
			"instance_uuid": fmt.Sprintf("netbox-vm-%d", v.ID),
			"name":          name,
			"power_state":   mapVMPowerState(v.Status.Value),
			"netbox_id":     strconv.Itoa(v.ID),
		}
		if v.VCPUs > 0 {
			attrs["vcpu"] = v.VCPUs
		}
		if v.Memory > 0 {
			attrs["memory_gb"] = math.Round(v.Memory/1024*100) / 100
		}
		if _, err := m.cmdb.CreateCI(ctx, "virtual_machine", attrs); err != nil {
			ent.recordFailure(strconv.Itoa(v.ID), name, err)
			continue
		}
		ent.recordSuccess()
	}
}

// mapVMPowerState 把 NetBox VM 状态映射为 CMDB 枚举值。
func mapVMPowerState(status string) string {
	switch status {
	case "active":
		return "poweredOn"
	case "staged":
		return "suspended"
	default: // offline 及其他未知状态按关机处理
		return "poweredOff"
	}
}

// migratePrefixes 前缀 → IPAM 前缀。
// 先按掩码升序排序（父先于子创建），逐个推导 parent_id，避免同级重叠 409；
// NetBox 前缀引用的 VLAN 以 vid（整数）透传给 CMDB（与服务端 vlan_id *int 对齐）。
func (m *Migrator) migratePrefixes(ctx context.Context, report *Report) *prefixTracker {
	ent := report.entity("prefixes", "前缀→IPAM 前缀")
	tracker := &prefixTracker{}
	prefixes, err := m.nb.ListPrefixes(ctx)
	if err != nil {
		ent.recordFetchError(err)
		return tracker
	}
	ent.Fetched = len(prefixes)

	// 掩码升序（父在前），同掩码按地址排序保证确定性。
	sort.SliceStable(prefixes, func(i, j int) bool {
		pi, pj := prefixes[i], prefixes[j]
		bi, bj := prefixBits(pi.Prefix), prefixBits(pj.Prefix)
		if bi != bj {
			return bi < bj
		}
		return pi.Prefix < pj.Prefix
	})

	for _, np := range prefixes {
		p, err := netip.ParsePrefix(np.Prefix)
		if err != nil {
			ent.recordFailure(strconv.Itoa(np.ID), np.Prefix, fmt.Errorf("CIDR 解析失败: %w", err))
			continue
		}
		p = p.Masked()
		name := np.Description
		if name == "" {
			name = p.String() // 无描述时以 CIDR 为名，保证必填
		}
		req := cmdb.PrefixCreateRequest{
			CIDR:        p.String(),
			Name:        name,
			Description: np.Description,
		}
		if np.VLAN != nil {
			vid := np.VLAN.VID
			req.VLANID = &vid // 服务端 vlan_id 为 *int（VLAN 编号）
		}
		if parentID, ok := tracker.findParent(p); ok {
			req.ParentID = parentID
		}
		ref, err := m.cmdb.CreatePrefix(ctx, req)
		if err != nil {
			ent.recordFailure(strconv.Itoa(np.ID), np.Prefix, err)
			continue
		}
		tracker.add(ref.ID, p)
		ent.recordSuccess()
	}
	return tracker
}

// migrateIPs IP 地址 → IPAM IP。
// 归属规则：解析 address 掩码取 IP，找包含它的最小已建前缀；
// 找不到则先自动创建 /24（IPv6 为 /64）前缀再写入。
func (m *Migrator) migrateIPs(ctx context.Context, report *Report, tracker *prefixTracker) {
	ent := report.entity("ip_addresses", "IP→IPAM IP")
	ips, err := m.nb.ListIPAddresses(ctx)
	if err != nil {
		ent.recordFetchError(err)
		return
	}
	ent.Fetched = len(ips)
	for _, ip := range ips {
		label := ip.Address
		p, err := netip.ParsePrefix(ip.Address)
		if err != nil {
			ent.recordFailure(strconv.Itoa(ip.ID), label, fmt.Errorf("地址解析失败: %w", err))
			continue
		}
		addr := p.Addr()
		prefixID, err := m.findOrCreatePrefix(ctx, tracker, addr)
		if err != nil {
			ent.recordFailure(strconv.Itoa(ip.ID), label, err)
			continue
		}
		req := cmdb.IPCreateRequest{
			PrefixID:    prefixID,
			IP:          addr.String(),
			Status:      mapIPStatus(ip.Status.Value),
			Description: composeIPDescription(ip),
		}
		if err := m.cmdb.CreateIP(ctx, req); err != nil {
			ent.recordFailure(strconv.Itoa(ip.ID), label, err)
			continue
		}
		ent.recordSuccess()
	}
}

// findOrCreatePrefix 为 IP 找最小包含前缀；找不到时自动创建 /24（IPv6 /64）兜底前缀。
func (m *Migrator) findOrCreatePrefix(ctx context.Context, tracker *prefixTracker, addr netip.Addr) (string, error) {
	if id, ok := tracker.findContaining(addr); ok {
		return id, nil
	}
	bits := 24
	if addr.Is6() {
		bits = 64 // IPv6 无 /24 惯例，按 /64 兜底
	}
	fallback := netip.PrefixFrom(addr, bits).Masked()
	req := cmdb.PrefixCreateRequest{
		CIDR:        fallback.String(),
		Name:        "auto-" + fallback.String(),
		Description: "IP 迁移自动归属创建（NetBox 无对应前缀）",
	}
	if parentID, ok := tracker.findParent(fallback); ok {
		req.ParentID = parentID
	}
	ref, err := m.cmdb.CreatePrefix(ctx, req)
	if err != nil {
		return "", fmt.Errorf("自动创建归属前缀 %s 失败: %w", fallback, err)
	}
	tracker.add(ref.ID, fallback)
	return ref.ID, nil
}

// mapIPStatus 把 NetBox IP 状态映射为 CMDB IPAM 状态枚举（used/reserved）：
// active/dhcp/slaac 及未知状态按"在用"登记，reserved/deprecated 按"保留"登记；
// 原始状态经 composeIPDescription 写入 description 留痕。
func mapIPStatus(status string) string {
	switch status {
	case "reserved", "deprecated":
		return "reserved"
	default:
		return "used"
	}
}

// composeIPDescription 合并 NetBox IP 的 description/dns_name，
// 并附加 netbox_id 与原始状态留痕（IPAM IP 无独立扩展字段，经描述留痕支撑对账回查）。
func composeIPDescription(ip netbox.IPAddress) string {
	parts := []string{}
	if ip.Description != "" {
		parts = append(parts, ip.Description)
	}
	if ip.DNSName != "" {
		parts = append(parts, "dns: "+ip.DNSName)
	}
	parts = append(parts, fmt.Sprintf("netbox_id=%d", ip.ID))
	if ip.Status.Value != "" {
		parts = append(parts, "netbox_status="+ip.Status.Value)
	}
	return strings.Join(parts, "；")
}

// prefixBits 解析 CIDR 的掩码位数；解析失败返回 -1（排序时沉底由后续失败明细处理）。
func prefixBits(cidr string) int {
	p, err := netip.ParsePrefix(cidr)
	if err != nil {
		return -1
	}
	return p.Bits()
}
