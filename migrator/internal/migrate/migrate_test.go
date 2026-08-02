// 迁移编排单测：进程内 NetBox 夹具 + 进程内 CMDB 夹具（实现约定语义：
// 模型/CI 校验、IPAM CIDR 400、同级重叠 409、IP 重复 409、不在前缀内 400），
// 端到端验证迁移映射、前缀树推导、/24 兜底、失败明细与报告输出。
package migrate

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"migrator/internal/cmdb"
	"migrator/internal/netbox"
)

// ---------- 进程内 NetBox 夹具 ----------

// newNetboxFixture 返回按约定信封返回固定夹具数据的 NetBox stub。
// bad 为 true 时注入非法数据（坏 CIDR、坏 IP）用于失败路径验证。
func newNetboxFixture(t *testing.T, bad bool) *httptest.Server {
	t.Helper()
	vlan20 := map[string]any{"id": 20, "vid": 100, "name": "office"}
	data := map[string][]any{
		"/api/dcim/sites/": {
			map[string]any{"id": 1, "name": "北京机房", "slug": "bj", "physical_address": "北京"},
			map[string]any{"id": 2, "name": "无Slug机房", "slug": ""},
		},
		"/api/dcim/racks/": {
			map[string]any{"id": 10, "name": "A01", "site": map[string]any{"id": 1}, "u_height": 42},
		},
		"/api/dcim/devices/": {
			map[string]any{
				"id": 100, "name": "core-sw-01", "serial": "SN-1",
				"device_type": map[string]any{"model": "CE6857", "manufacturer": map[string]any{"name": "Huawei"}},
				"primary_ip4": map[string]any{"address": "10.1.2.2/24"},
				"rack":        map[string]any{"id": 10},
			},
			// 无序列号、无 primary_ip4、无机架的设备：验证宽容映射。
			map[string]any{"id": 101, "name": "edge-fw-01"},
		},
		"/api/ipam/vlans/": {
			map[string]any{"id": 20, "vid": 100, "name": "office", "description": "办公网"},
			map[string]any{"id": 21, "vid": 200, "name": "server"},
		},
		"/api/virtualization/virtual-machines/": {
			map[string]any{"id": 30, "name": "vm-01", "status": map[string]any{"value": "active"}, "vcpus": 4, "memory": 8192},
			map[string]any{"id": 31, "name": "vm-02", "status": map[string]any{"value": "staged"}, "vcpus": 2, "memory": 2048},
		},
		"/api/ipam/prefixes/": {
			map[string]any{"id": 42, "prefix": "10.1.2.0/24", "description": "核心网段"}, // 故意乱序：验证排序后父先建
			map[string]any{"id": 40, "prefix": "10.0.0.0/8", "description": "内网"},
			map[string]any{"id": 41, "prefix": "10.1.0.0/16", "description": "北京数据中心", "vlan": vlan20},
		},
		"/api/ipam/ip-addresses/": {
			map[string]any{"id": 50, "address": "10.1.2.5/24", "status": map[string]any{"value": "active"}, "description": "核心交换机", "dns_name": "sw.mgmt"},
			map[string]any{"id": 51, "address": "10.1.3.9/16", "status": map[string]any{"value": "reserved"}, "description": "应归属/16"},
			map[string]any{"id": 52, "address": "192.168.9.9/24", "status": map[string]any{"value": "active"}, "description": "触发/24兜底"},
		},
	}
	if bad {
		data["/api/ipam/prefixes/"] = append(data["/api/ipam/prefixes/"],
			map[string]any{"id": 43, "prefix": "999.1.2.0/24", "description": "坏CIDR"})
		data["/api/ipam/ip-addresses/"] = append(data["/api/ipam/ip-addresses/"],
			map[string]any{"id": 53, "address": "not-an-ip", "status": map[string]any{"value": "active"}})
	}
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "" {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		items, ok := data[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"count": len(items), "next": nil, "previous": nil, "results": items,
		})
	}))
}

// ---------- 进程内 CMDB 夹具（实现约定语义） ----------

type fakeAttrDef struct {
	Code       string   `json:"code"`
	Type       string   `json:"type"`
	Required   bool     `json:"required"`
	Unique     bool     `json:"unique"`
	EnumValues []string `json:"enum_values"`
}

type fakePrefix struct {
	ID, CIDR, Name, Description, ParentID string
	VLANID                                int // VLAN 编号（vid），0 表示未关联
	Parsed                                netip.Prefix
}

type fakeIP struct {
	ID, PrefixID, IP, Status, Description string
}

type fakeCMDB struct {
	attrs    map[string][]fakeAttrDef // 模型 code → 属性定义
	cis      []map[string]any
	prefixes []fakePrefix
	ips      []fakeIP
	nextID   int
}

func newFakeCMDB() *fakeCMDB {
	return &fakeCMDB{attrs: map[string][]fakeAttrDef{}, nextID: 1}
}

func (f *fakeCMDB) genID(prefix string) string {
	id := fmt.Sprintf("%s-%d", prefix, f.nextID)
	f.nextID++
	return id
}

func respondErr(w http.ResponseWriter, status int, code, msg string) {
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"code": code, "message": msg})
}

// handler 实现约定语义：模型/CI（required/unique/enum/ip 校验）+ IPAM（400/409）。
func (f *fakeCMDB) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/models", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Code       string        `json:"code"`
			Name       string        `json:"name"`
			Attributes []fakeAttrDef `json:"attributes"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if _, exists := f.attrs[body.Code]; exists {
			respondErr(w, http.StatusBadRequest, "VALIDATION_FAILED", "模型编码已存在")
			return
		}
		f.attrs[body.Code] = body.Attributes
		_ = json.NewEncoder(w).Encode(map[string]any{"id": f.genID("model"), "code": body.Code, "name": body.Name})
	})
	mux.HandleFunc("/api/v1/models/", func(w http.ResponseWriter, r *http.Request) {
		code := strings.TrimPrefix(r.URL.Path, "/api/v1/models/")
		if defs, ok := f.attrs[code]; ok {
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "model-" + code, "code": code, "attributes": defs})
			return
		}
		respondErr(w, http.StatusNotFound, "NOT_FOUND", "模型不存在")
	})
	mux.HandleFunc("/api/v1/cis", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			ModelID    string         `json:"model_id"`
			Attributes map[string]any `json:"attributes"`
			Status     string         `json:"status"`
			Source     string         `json:"source"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		defs, ok := f.attrs[body.ModelID]
		if !ok {
			respondErr(w, http.StatusNotFound, "NOT_FOUND", "模型不存在")
			return
		}
		// required / enum / ip 类型校验（对齐 server validation 行为）。
		for _, def := range defs {
			v, present := body.Attributes[def.Code]
			if def.Required && (!present || v == nil || v == "") {
				respondErr(w, http.StatusBadRequest, "VALIDATION_FAILED", "属性 "+def.Code+" 为必填项")
				return
			}
			if !present || v == nil {
				continue
			}
			if def.Type == "enum" && len(def.EnumValues) > 0 {
				s, _ := v.(string)
				okEnum := false
				for _, cand := range def.EnumValues {
					if s == cand {
						okEnum = true
					}
				}
				if !okEnum {
					respondErr(w, http.StatusBadRequest, "VALIDATION_FAILED", "属性 "+def.Code+" 取值不在枚举内")
					return
				}
			}
			if def.Type == "ip" {
				if _, err := netip.ParseAddr(fmt.Sprintf("%v", v)); err != nil {
					respondErr(w, http.StatusBadRequest, "VALIDATION_FAILED", "属性 "+def.Code+" 不是合法 IP")
					return
				}
			}
		}
		// unique 校验。
		for _, def := range defs {
			if !def.Unique {
				continue
			}
			v, present := body.Attributes[def.Code]
			if !present || v == nil || v == "" {
				continue
			}
			for _, ci := range f.cis {
				if ci["model_code"] == body.ModelID {
					if attrs, _ := ci["attributes"].(map[string]any); attrs[def.Code] == v {
						respondErr(w, http.StatusBadRequest, "VALIDATION_FAILED", "属性 "+def.Code+" 违反唯一性")
						return
					}
				}
			}
		}
		ci := map[string]any{
			"id": f.genID("ci"), "model_code": body.ModelID, "model_id": "model-" + body.ModelID,
			"attributes": body.Attributes, "status": body.Status, "source": body.Source,
		}
		f.cis = append(f.cis, ci)
		_ = json.NewEncoder(w).Encode(ci)
	})
	mux.HandleFunc("/api/v1/ipam/prefixes", func(w http.ResponseWriter, r *http.Request) {
		var body cmdb.PrefixCreateRequest
		_ = json.NewDecoder(r.Body).Decode(&body)
		p, err := netip.ParsePrefix(body.CIDR)
		if err != nil {
			respondErr(w, http.StatusBadRequest, "BAD_REQUEST", "CIDR 非法")
			return
		}
		// vlan_id 取值校验（对齐 server：1-4094）。
		if body.VLANID != nil && (*body.VLANID < 1 || *body.VLANID > 4094) {
			respondErr(w, http.StatusBadRequest, "BAD_REQUEST", "vlan_id 必须为 1-4094 的整数")
			return
		}
		// 同级（parent_id 相同）重叠 → 409。
		for _, existing := range f.prefixes {
			if existing.ParentID == body.ParentID && existing.Parsed.Overlaps(p) {
				respondErr(w, http.StatusConflict, "CONFLICT", "同级前缀重叠")
				return
			}
		}
		vid := 0
		if body.VLANID != nil {
			vid = *body.VLANID
		}
		fp := fakePrefix{
			ID: f.genID("prefix"), CIDR: p.Masked().String(), Name: body.Name,
			VLANID: vid, Description: body.Description, ParentID: body.ParentID,
			Parsed: p.Masked(),
		}
		f.prefixes = append(f.prefixes, fp)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": fp.ID, "cidr": fp.CIDR, "name": fp.Name,
			"vlan_id": body.VLANID, "description": fp.Description, "parent_id": fp.ParentID,
		})
	})
	mux.HandleFunc("/api/v1/ipam/ips", func(w http.ResponseWriter, r *http.Request) {
		var body cmdb.IPCreateRequest
		_ = json.NewDecoder(r.Body).Decode(&body)
		addr, err := netip.ParseAddr(body.IP)
		if err != nil {
			respondErr(w, http.StatusBadRequest, "BAD_REQUEST", "IP 非法")
			return
		}
		// status 枚举校验（对齐 server：used/reserved）。
		if body.Status != "" && body.Status != "used" && body.Status != "reserved" {
			respondErr(w, http.StatusBadRequest, "BAD_REQUEST", "status 取值非法（used/reserved）")
			return
		}
		var parent *fakePrefix
		for i := range f.prefixes {
			if f.prefixes[i].ID == body.PrefixID {
				parent = &f.prefixes[i]
			}
		}
		if parent == nil || !parent.Parsed.Contains(addr) {
			respondErr(w, http.StatusBadRequest, "BAD_REQUEST", "IP 不在前缀范围内")
			return
		}
		for _, existing := range f.ips {
			if existing.PrefixID == body.PrefixID && existing.IP == body.IP {
				respondErr(w, http.StatusConflict, "CONFLICT", "IP 已存在")
				return
			}
		}
		fip := fakeIP{ID: f.genID("ip"), PrefixID: body.PrefixID, IP: body.IP, Status: body.Status, Description: body.Description}
		f.ips = append(f.ips, fip)
		_ = json.NewEncoder(w).Encode(map[string]any{"id": fip.ID, "prefix_id": fip.PrefixID, "ip": fip.IP, "status": fip.Status})
	})
	return mux
}

// findCI 按模型与 netbox_id 找 CI。
func (f *fakeCMDB) findCI(modelCode, netboxID string) map[string]any {
	for _, ci := range f.cis {
		if ci["model_code"] == modelCode {
			if attrs, _ := ci["attributes"].(map[string]any); attrs["netbox_id"] == netboxID {
				return ci
			}
		}
	}
	return nil
}

// findPrefix 按 CIDR 找前缀。
func (f *fakeCMDB) findPrefix(cidr string) *fakePrefix {
	for i := range f.prefixes {
		if f.prefixes[i].CIDR == cidr {
			return &f.prefixes[i]
		}
	}
	return nil
}

// ---------- 测试 ----------

// runMigration 起夹具并执行迁移。
func runMigration(t *testing.T, bad bool) (*Report, *fakeCMDB) {
	t.Helper()
	nbStub := newNetboxFixture(t, bad)
	t.Cleanup(nbStub.Close)
	fake := newFakeCMDB()
	cmStub := httptest.NewServer(fake.handler())
	t.Cleanup(cmStub.Close)

	m := New(netbox.NewClient(nbStub.URL, "token"), cmdb.NewClient(cmStub.URL))
	report, err := m.Run(context.Background(), nbStub.URL, cmStub.URL)
	if err != nil {
		t.Fatalf("迁移致命错误: %v", err)
	}
	return report, fake
}

// entityStat 从报告取实体统计。
func entityStat(t *testing.T, r *Report, key string) EntityReport {
	t.Helper()
	for _, e := range r.Entities {
		if e.Entity == key {
			return e
		}
	}
	t.Fatalf("报告缺少实体 %s", key)
	return EntityReport{}
}

// TestMigrationHappyPath 验证全量映射、前缀树、兜底与报告计数。
func TestMigrationHappyPath(t *testing.T) {
	report, fake := runMigration(t, false)

	// 模型全部新建。
	if len(report.Models) != 5 {
		t.Fatalf("期望 5 个模型确保记录，实际 %d", len(report.Models))
	}
	for _, mr := range report.Models {
		if mr.Status != "created" {
			t.Fatalf("模型 %s 期望 created，实际 %s", mr.Code, mr.Status)
		}
	}

	// 实体计数（fetched/succeeded/failed）。
	cases := map[string][3]int{
		"sites": {2, 2, 0}, "racks": {1, 1, 0}, "devices": {2, 2, 0},
		"vlans": {2, 2, 0}, "virtual_machines": {2, 2, 0},
		"prefixes": {3, 3, 0}, "ip_addresses": {3, 3, 0},
	}
	for key, want := range cases {
		got := entityStat(t, report, key)
		if got.Fetched != want[0] || got.Succeeded != want[1] || got.Failed != want[2] {
			t.Errorf("%s 计数异常: 期望 %v，实际 fetched=%d succeeded=%d failed=%d",
				key, want, got.Fetched, got.Succeeded, got.Failed)
		}
	}

	// room：slug 兜底与地址映射。
	room2 := fake.findCI("room", "2")
	if room2 == nil {
		t.Fatal("缺少 netbox_id=2 的 room CI")
	}
	if attrs := room2["attributes"].(map[string]any); attrs["code"] != "site-2" {
		t.Errorf("slug 为空时 code 应兜底为 site-2，实际 %v", attrs["code"])
	}

	// rack：netbox_site_id 留痕。
	rack := fake.findCI("rack", "10")
	if attrs := rack["attributes"].(map[string]any); attrs["netbox_site_id"] != "1" || attrs["u_capacity"] != float64(42) {
		t.Errorf("rack 属性异常: %v", attrs)
	}

	// device：全量映射与宽容映射（无 IP/序列号）。
	dev1 := fake.findCI("network_device", "100")
	attrs1 := dev1["attributes"].(map[string]any)
	if attrs1["mgmt_ip"] != "10.1.2.2" || attrs1["vendor"] != "Huawei" || attrs1["model"] != "CE6857" ||
		attrs1["serial_no"] != "SN-1" || attrs1["netbox_rack_id"] != "10" {
		t.Errorf("device 100 映射异常: %v", attrs1)
	}
	dev2 := fake.findCI("network_device", "101")
	attrs2 := dev2["attributes"].(map[string]any)
	if _, hasIP := attrs2["mgmt_ip"]; hasIP {
		t.Errorf("无 primary_ip4 的设备不应带 mgmt_ip: %v", attrs2)
	}

	// VM：MB→GB 换算、枚举映射、合成 UUID。
	vm1 := fake.findCI("virtual_machine", "30")
	vattrs1 := vm1["attributes"].(map[string]any)
	if vattrs1["memory_gb"] != float64(8) || vattrs1["power_state"] != "poweredOn" ||
		vattrs1["instance_uuid"] != "netbox-vm-30" || vattrs1["vcpu"] != float64(4) {
		t.Errorf("vm 30 映射异常: %v", vattrs1)
	}
	vm2 := fake.findCI("virtual_machine", "31")
	if vattrs2 := vm2["attributes"].(map[string]any); vattrs2["power_state"] != "suspended" {
		t.Errorf("staged 应映射 suspended，实际 %v", vattrs2["power_state"])
	}

	// 前缀树：/8 无父，/16 父为 /8，/24 父为 /16；/16 携带 VLAN 编号（vid=100）。
	p8 := fake.findPrefix("10.0.0.0/8")
	p16 := fake.findPrefix("10.1.0.0/16")
	p24 := fake.findPrefix("10.1.2.0/24")
	if p8 == nil || p16 == nil || p24 == nil {
		t.Fatalf("前缀缺失: %+v", fake.prefixes)
	}
	if p8.ParentID != "" {
		t.Errorf("/8 不应有父前缀，实际 %s", p8.ParentID)
	}
	if p16.ParentID != p8.ID {
		t.Errorf("/16 父应为 /8（%s），实际 %s", p8.ID, p16.ParentID)
	}
	if p24.ParentID != p16.ID {
		t.Errorf("/24 父应为 /16（%s），实际 %s", p16.ID, p24.ParentID)
	}
	if p16.VLANID != 100 {
		t.Errorf("/16 vlan_id 应为 vid 100，实际 %d", p16.VLANID)
	}
	if p24.VLANID != 0 {
		t.Errorf("/24 未关联 VLAN，vid 应为 0，实际 %d", p24.VLANID)
	}

	// IP 归属：最小包含前缀（10.1.3.9 → /16 而非 /8）。
	var ip139 *fakeIP
	for i := range fake.ips {
		if fake.ips[i].IP == "10.1.3.9" {
			ip139 = &fake.ips[i]
		}
	}
	if ip139 == nil || ip139.PrefixID != p16.ID {
		t.Errorf("10.1.3.9 应归属 /16（%s），实际 %+v", p16.ID, ip139)
	}

	// /24 兜底：192.168.9.9 无对应前缀 → 自动建 192.168.9.0/24。
	fallback := fake.findPrefix("192.168.9.0/24")
	if fallback == nil {
		t.Fatal("缺少自动兜底前缀 192.168.9.0/24")
	}
	if fallback.Name != "auto-192.168.9.0/24" {
		t.Errorf("兜底前缀命名异常: %s", fallback.Name)
	}

	// 状态映射：NetBox active→used、reserved→reserved；原始状态与 netbox_id 写入描述留痕。
	statusByIP := map[string]string{}
	for _, fip := range fake.ips {
		statusByIP[fip.IP] = fip.Status
		if !strings.Contains(fip.Description, "netbox_id=") {
			t.Errorf("IP %s 描述缺少 netbox_id 留痕: %q", fip.IP, fip.Description)
		}
	}
	if statusByIP["10.1.2.5"] != "used" || statusByIP["10.1.3.9"] != "reserved" || statusByIP["192.168.9.9"] != "used" {
		t.Errorf("IP 状态映射异常: %v", statusByIP)
	}
	for _, fip := range fake.ips {
		if fip.IP == "10.1.3.9" && !strings.Contains(fip.Description, "netbox_status=reserved") {
			t.Errorf("IP 描述应含原始状态留痕，实际 %q", fip.Description)
		}
	}

	// 全部 CI 均带 netbox_id 且来源为迁移。
	for _, ci := range fake.cis {
		attrs := ci["attributes"].(map[string]any)
		if attrs["netbox_id"] == nil || attrs["netbox_id"] == "" {
			t.Errorf("CI 缺少 netbox_id 留痕: %v", attrs)
		}
		if ci["source"] != cmdb.MigrationSource {
			t.Errorf("CI source 应为 %s，实际 %v", cmdb.MigrationSource, ci["source"])
		}
	}
}

// TestMigrationFailures 验证坏数据进入失败明细且不影响其余记录。
func TestMigrationFailures(t *testing.T) {
	report, fake := runMigration(t, true)

	prefixes := entityStat(t, report, "prefixes")
	if prefixes.Fetched != 4 || prefixes.Succeeded != 3 || prefixes.Failed != 1 {
		t.Fatalf("prefixes 计数异常: %+v", prefixes)
	}
	if len(prefixes.Failures) != 1 || prefixes.Failures[0].NetboxID != "43" {
		t.Fatalf("prefixes 失败明细异常: %+v", prefixes.Failures)
	}

	ips := entityStat(t, report, "ip_addresses")
	if ips.Fetched != 4 || ips.Succeeded != 3 || ips.Failed != 1 {
		t.Fatalf("ip_addresses 计数异常: %+v", ips)
	}
	if len(ips.Failures) != 1 || !strings.Contains(ips.Failures[0].Error, "地址解析失败") {
		t.Fatalf("ip_addresses 失败明细异常: %+v", ips.Failures)
	}

	// 坏数据不影响好数据：三层前缀树仍在。
	if fake.findPrefix("10.1.2.0/24") == nil {
		t.Fatal("坏 CIDR 不应影响正常前缀迁移")
	}
	if report.TotalFailed() != 2 {
		t.Fatalf("失败合计应为 2，实际 %d", report.TotalFailed())
	}
}

// TestMigrationFetchError 验证某类实体拉取失败记入明细、其余实体继续。
func TestMigrationFetchError(t *testing.T) {
	nbStub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/dcim/devices/" {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"detail":"boom"}`))
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"count": 0, "next": nil, "previous": nil, "results": []any{}})
	}))
	defer nbStub.Close()
	fake := newFakeCMDB()
	cmStub := httptest.NewServer(fake.handler())
	defer cmStub.Close()

	m := New(netbox.NewClient(nbStub.URL, "token"), cmdb.NewClient(cmStub.URL))
	report, err := m.Run(context.Background(), nbStub.URL, cmStub.URL)
	if err != nil {
		t.Fatalf("拉取失败不应是致命错误: %v", err)
	}
	devices := entityStat(t, report, "devices")
	if devices.Fetched != 0 || len(devices.Failures) != 1 || devices.Failures[0].NetboxID != "-" {
		t.Fatalf("拉取失败明细异常: %+v", devices)
	}
	// 其余实体正常走完（均为空集，但存在统计块）。
	entityStat(t, report, "ip_addresses")
}

// TestReportWriteAndSummary 验证报告落盘与摘要内容。
func TestReportWriteAndSummary(t *testing.T) {
	report, _ := runMigration(t, true)

	path := filepath.Join(t.TempDir(), "migration-report.json")
	if err := report.WriteJSON(path); err != nil {
		t.Fatalf("报告写入失败: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("报告读取失败: %v", err)
	}
	var decoded Report
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("报告 JSON 非法: %v", err)
	}
	if len(decoded.Entities) != 7 || len(decoded.Models) != 5 {
		t.Fatalf("报告结构异常: entities=%d models=%d", len(decoded.Entities), len(decoded.Models))
	}

	summary := report.Summary()
	for _, want := range []string{"迁移完成", "模型确保", "sites", "ip_addresses", "失败合计：2 条"} {
		if !strings.Contains(summary, want) {
			t.Errorf("摘要缺少 %q:\n%s", want, summary)
		}
	}
}

// TestFailureDetailCap 验证失败明细只保留前 5 条、计数完整。
func TestFailureDetailCap(t *testing.T) {
	ent := &EntityReport{Entity: "x"}
	for i := 0; i < 8; i++ {
		ent.recordFailure(strconv.Itoa(i), "n", fmt.Errorf("e%d", i))
	}
	if ent.Failed != 8 || len(ent.Failures) != 5 {
		t.Fatalf("明细截断异常: failed=%d details=%d", ent.Failed, len(ent.Failures))
	}
}
