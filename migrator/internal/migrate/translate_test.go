// 实体→属性/记录映射单测：属性编码对齐 scripts/seed/ 种子模型
// （room.json / rack.json / network_device.json / virtual_machine.json；
// vlan 无种子文件，以迁移器自建模型 cmdb.RequiredModels 为准）。
package migrate

import (
	"encoding/json"
	"testing"
	"time"

	"migrator/internal/cmdb"
	"migrator/internal/netbox"
)

// decode 从 JSON 构造 netbox 实体（其嵌套类型未导出，只能经解码构造）。
func decode[T any](t *testing.T, raw string) T {
	t.Helper()
	var v T
	if err := json.Unmarshal([]byte(raw), &v); err != nil {
		t.Fatalf("夹具解码失败: %v", err)
	}
	return v
}

// assertAttrCodes 断言属性键全部落在允许集合内（种子属性编码 + 留痕字段）。
func assertAttrCodes(t *testing.T, attrs map[string]any, allowed ...string) {
	t.Helper()
	set := map[string]bool{}
	for _, c := range allowed {
		set[c] = true
	}
	for k := range attrs {
		if !set[k] {
			t.Errorf("属性 %q 不在种子模型属性编码及留痕字段集合 %v 内", k, allowed)
		}
	}
}

// TestSiteAttrs 验证站点映射（种子 room.json：name/code/address）。
func TestSiteAttrs(t *testing.T) {
	s := decode[netbox.Site](t, `{"id":1,"name":"北京机房","slug":"bj-yz","physical_address":"北京亦庄"}`)
	label, attrs := siteAttrs(s)
	if label != "北京机房" {
		t.Errorf("label 异常: %q", label)
	}
	if attrs["name"] != "北京机房" || attrs["code"] != "bj-yz" || attrs["address"] != "北京亦庄" || attrs["netbox_id"] != "1" {
		t.Errorf("站点映射异常: %v", attrs)
	}
	assertAttrCodes(t, attrs, "name", "code", "address", "netbox_id")

	// slug 缺失兜底 site-<id>；address 缺失则不携带。
	s2 := decode[netbox.Site](t, `{"id":2,"name":"无Slug机房","slug":""}`)
	_, attrs2 := siteAttrs(s2)
	if attrs2["code"] != "site-2" {
		t.Errorf("slug 兜底异常: %v", attrs2["code"])
	}
	if _, has := attrs2["address"]; has {
		t.Errorf("空 address 不应携带: %v", attrs2)
	}
}

// TestRackAttrs 验证机架映射（种子 rack.json：name/u_capacity；netbox_site_id 留痕）。
func TestRackAttrs(t *testing.T) {
	r := decode[netbox.Rack](t, `{"id":10,"name":"A01","site":{"id":1},"u_height":42}`)
	label, attrs := rackAttrs(r)
	if label != "A01" {
		t.Errorf("label 异常: %q", label)
	}
	if attrs["name"] != "A01" || attrs["u_capacity"] != float64(42) ||
		attrs["netbox_id"] != "10" || attrs["netbox_site_id"] != "1" {
		t.Errorf("机架映射异常: %v", attrs)
	}
	assertAttrCodes(t, attrs, "name", "u_capacity", "power_capacity_kw", "netbox_id", "netbox_site_id")

	// 名称缺失兜底 rack-<id>；无站点则不携带 netbox_site_id。
	r2 := decode[netbox.Rack](t, `{"id":11,"name":""}`)
	_, attrs2 := rackAttrs(r2)
	if attrs2["name"] != "rack-11" {
		t.Errorf("机架名兜底异常: %v", attrs2["name"])
	}
	if _, has := attrs2["netbox_site_id"]; has {
		t.Errorf("无站点不应携带 netbox_site_id: %v", attrs2)
	}
}

// TestDeviceAttrs 验证设备映射（种子 network_device.json：serial_no/model/vendor/mgmt_ip，另加 name）。
func TestDeviceAttrs(t *testing.T) {
	d := decode[netbox.Device](t, `{
		"id":100,"name":"core-sw-01","serial":"SN-1",
		"device_type":{"model":"CE6857","manufacturer":{"name":"Huawei"}},
		"primary_ip4":{"address":"10.1.2.2/24"},"rack":{"id":10}}`)
	label, attrs := deviceAttrs(d)
	if label != "core-sw-01" {
		t.Errorf("label 异常: %q", label)
	}
	if attrs["serial_no"] != "SN-1" || attrs["model"] != "CE6857" || attrs["vendor"] != "Huawei" ||
		attrs["mgmt_ip"] != "10.1.2.2" || attrs["netbox_rack_id"] != "10" || attrs["netbox_id"] != "100" {
		t.Errorf("设备映射异常: %v", attrs)
	}
	assertAttrCodes(t, attrs, "name", "serial_no", "model", "vendor", "mgmt_ip", "netbox_id", "netbox_rack_id")

	// 宽容映射：无序列号/无 primary_ip4/无机架的设备。
	d2 := decode[netbox.Device](t, `{"id":101,"name":"edge-fw-01"}`)
	_, attrs2 := deviceAttrs(d2)
	for _, k := range []string{"serial_no", "mgmt_ip", "netbox_rack_id", "model", "vendor"} {
		if _, has := attrs2[k]; has {
			t.Errorf("缺失字段不应携带 %s: %v", k, attrs2)
		}
	}
}

// TestVLANAttrs 验证 VLAN 映射（vlan 无种子模型，编码以迁移器自建模型为准：vid/name/description）。
func TestVLANAttrs(t *testing.T) {
	v := decode[netbox.VLAN](t, `{"id":20,"vid":100,"name":"office","description":"办公网"}`)
	label, attrs := vlanAttrs(v)
	if label != "office" {
		t.Errorf("label 异常: %q", label)
	}
	// vid 必须是数值类型（模型定义 type=number）。
	if attrs["vid"] != float64(100) {
		t.Errorf("vid 应为数值 100，实际 %v（%T）", attrs["vid"], attrs["vid"])
	}
	if attrs["name"] != "office" || attrs["description"] != "办公网" || attrs["netbox_id"] != "20" {
		t.Errorf("VLAN 映射异常: %v", attrs)
	}
	assertAttrCodes(t, attrs, "vid", "name", "description", "netbox_id")

	// 名称缺失兜底 vlan-<vid>。
	v2 := decode[netbox.VLAN](t, `{"id":21,"vid":200,"name":""}`)
	_, attrs2 := vlanAttrs(v2)
	if attrs2["name"] != "vlan-200" {
		t.Errorf("VLAN 名兜底异常: %v", attrs2["name"])
	}
}

// TestVMAttrs 验证虚拟机映射（种子 virtual_machine.json：instance_uuid/name/vcpu/memory_gb/power_state）。
func TestVMAttrs(t *testing.T) {
	v := decode[netbox.VirtualMachine](t, `{"id":30,"name":"vm-01","status":{"value":"active"},"vcpus":4,"memory":8192}`)
	label, attrs := vmAttrs(v)
	if label != "vm-01" {
		t.Errorf("label 异常: %q", label)
	}
	if attrs["instance_uuid"] != "netbox-vm-30" || attrs["power_state"] != "poweredOn" ||
		attrs["vcpu"] != float64(4) || attrs["memory_gb"] != float64(8) || attrs["netbox_id"] != "30" {
		t.Errorf("VM 映射异常: %v", attrs)
	}
	assertAttrCodes(t, attrs, "instance_uuid", "name", "vcpu", "memory_gb", "power_state", "netbox_id")

	// 状态映射全覆盖：active→poweredOn，staged→suspended，offline 及未知→poweredOff。
	for raw, want := range map[string]string{"active": "poweredOn", "staged": "suspended", "offline": "poweredOff", "weird": "poweredOff"} {
		if got := mapVMPowerState(raw); got != want {
			t.Errorf("状态 %q 应映射 %q，实际 %q", raw, want, got)
		}
	}
}

// TestToRecord 验证标准发现记录包装（source/collector/model_candidate/occurred_at）。
func TestToRecord(t *testing.T) {
	now := time.Now()
	rec := toRecord("room", map[string]any{"netbox_id": "1"}, now)
	if rec.Source != cmdb.MigrationSource || rec.Collector != CollectorID ||
		rec.ModelCandidate != "room" || !rec.OccurredAt.Equal(now) {
		t.Errorf("记录包装异常: %+v", rec)
	}
}
