// NetBox 实体 → CMDB 属性映射（direct 与 pipeline 两种模式共用的单一映射来源）。
// 属性编码对齐 scripts/seed/ 对应种子模型（room/rack/network_device/virtual_machine），
// 另附 netbox_id（及 netbox_*_id）留痕属性支撑对账回查与管道模式幂等调和。
// vlan 无种子模型（由迁移器自建），属性编码见 cmdb.RequiredModels。
package migrate

import (
	"fmt"
	"math"
	"net/netip"
	"strconv"

	"migrator/internal/netbox"
)

// siteAttrs 站点 → room 属性（种子 room.json：name/code/address）。
// slug 缺失时以 site-<id> 兜底，保证必填且唯一的 code 有值。
func siteAttrs(s netbox.Site) (string, map[string]any) {
	code := s.Slug
	if code == "" {
		code = fmt.Sprintf("site-%d", s.ID)
	}
	attrs := map[string]any{
		"name":      s.Name,
		"code":      code,
		"netbox_id": strconv.Itoa(s.ID),
	}
	if s.PhysicalAddress != "" {
		attrs["address"] = s.PhysicalAddress
	}
	return s.Name, attrs
}

// rackAttrs 机架 → rack 属性（种子 rack.json：name/u_capacity；netbox_site_id 留痕所属站点）。
func rackAttrs(r netbox.Rack) (string, map[string]any) {
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
	return name, attrs
}

// deviceAttrs 设备 → network_device 属性（种子 network_device.json：serial_no/model/vendor/mgmt_ip，
// 另加 name 提升列表可读性；primary_ip4 剥离掩码作为管理 IP，缺失时宽容跳过）。
func deviceAttrs(d netbox.Device) (string, map[string]any) {
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
	if d.PrimaryIP4 != nil && d.PrimaryIP4.Address != "" {
		if p, err := netip.ParsePrefix(d.PrimaryIP4.Address); err == nil {
			attrs["mgmt_ip"] = p.Addr().String()
		}
	}
	if d.Rack != nil {
		attrs["netbox_rack_id"] = strconv.Itoa(d.Rack.ID)
	}
	return name, attrs
}

// vlanAttrs VLAN → vlan 属性（迁移器自建模型：vid/name/description）。
func vlanAttrs(v netbox.VLAN) (string, map[string]any) {
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
	return name, attrs
}

// vmAttrs 虚拟机 → virtual_machine 属性（种子 virtual_machine.json：
// instance_uuid/name/vcpu/memory_gb/power_state）。
// instance_uuid 用 netbox-vm-<id> 合成（NetBox VM 无 UUID 概念，迁移后由 vCenter 采集器接管）；
// memory 单位 MB → GB；status 映射到模型枚举 poweredOn/poweredOff/suspended。
func vmAttrs(v netbox.VirtualMachine) (string, map[string]any) {
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
	return name, attrs
}
