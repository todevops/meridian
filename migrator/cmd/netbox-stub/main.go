// Command netbox-stub 是迁移器端到端验证用的最小 NetBox 夹具：
// 按接口约定提供 7 个列表端点（{count,next,previous,results} 信封、
// limit/offset 分页、"Authorization: Token <非空>" 认证，空则 403）。
// 仅用于本地验证；正式 mock 平台（:19002）由并行代理开发。
//
// 环境变量：NETBOX_STUB_ADDR（默认 :19092）。
package main

import (
	"encoding/json"
	"log"
	"net/http"
	"os"
	"strconv"
)

// 夹具数据：覆盖站点/机架/设备/VLAN/虚拟机/三层嵌套前缀/含兜底的 IP。
var fixtures = map[string][]any{
	"/api/dcim/sites/": {
		site(1, "北京亦庄机房", "bj-yz", "北京市亦庄经济开发区科创街 1 号"),
		site(2, "上海漕河泾机房", "sh-chj", "上海市徐汇区漕河泾开发区 88 号"),
	},
	"/api/dcim/racks/": {
		rack(10, "A01", 1, 42),
		rack(11, "B01", 2, 48),
	},
	"/api/dcim/devices/": {
		device(100, "core-sw-01", "CE6857-48S6Q-EI", "Huawei", "HW2102355A01", "10.1.2.2/24", 1, 10, "核心交换机"),
		device(101, "access-sw-01", "S5735-L24T4S", "Huawei", "HW2102355B02", "10.1.2.3/24", 1, 10, "接入交换机"),
		device(102, "edge-fw-01", "USG6525E", "Huawei", "", "10.1.3.1/24", 2, 11, "边界防火墙"),
	},
	"/api/ipam/vlans/": {
		vlan(20, 100, "office", "办公网"),
		vlan(21, 200, "server", "服务器网"),
	},
	"/api/virtualization/virtual-machines/": {
		vm(30, "vm-web-01", "active", 4, 8192),
		vm(31, "vm-batch-01", "offline", 8, 16384),
	},
	"/api/ipam/prefixes/": {
		prefix(40, "10.0.0.0/8", "内网大段", 0, 0),
		prefix(41, "10.1.0.0/16", "北京数据中心", 20, 100),
		prefix(42, "10.1.2.0/24", "核心设备网段", 21, 200),
	},
	"/api/ipam/ip-addresses/": {
		ipAddr(50, "10.1.2.5/24", "active", "核心交换机管理", "core-sw-01.mgmt"),
		ipAddr(51, "10.1.3.9/16", "reserved", "边界防火墙管理", ""),
		ipAddr(52, "192.168.9.9/24", "active", "无对应前缀-触发自动建段", ""),
	},
}

func site(id int, name, slug, addr string) map[string]any {
	return map[string]any{
		"id": id, "name": name, "slug": slug,
		"status":           map[string]any{"value": "active", "label": "Active"},
		"physical_address": addr, "description": "",
	}
}

func rack(id int, name string, siteID int, uHeight int) map[string]any {
	return map[string]any{
		"id": id, "name": name,
		"site":     map[string]any{"id": siteID},
		"u_height": uHeight,
		"status":   map[string]any{"value": "active", "label": "Active"},
	}
}

func device(id int, name, model, vendor, serial, ip string, siteID, rackID int, role string) map[string]any {
	d := map[string]any{
		"id":   id,
		"name": name,
		"device_type": map[string]any{
			"id":           id + 1000,
			"model":        model,
			"manufacturer": map[string]any{"id": id + 2000, "name": vendor},
		},
		"site":        map[string]any{"id": siteID},
		"rack":        map[string]any{"id": rackID},
		"device_role": map[string]any{"id": id + 3000, "name": role},
		"status":      map[string]any{"value": "active", "label": "Active"},
	}
	if serial != "" {
		d["serial"] = serial
	}
	if ip != "" {
		d["primary_ip4"] = map[string]any{"id": id + 4000, "address": ip}
	}
	return d
}

func vlan(id, vid int, name, desc string) map[string]any {
	return map[string]any{
		"id": id, "vid": vid, "name": name, "description": desc,
		"status": map[string]any{"value": "active", "label": "Active"},
	}
}

func vm(id int, name, status string, vcpus, memoryMB int) map[string]any {
	return map[string]any{
		"id": id, "name": name,
		"status": map[string]any{"value": status, "label": status},
		"vcpus":  vcpus, "memory": memoryMB, "disk": 100, "comments": "",
	}
}

func prefix(id int, cidr, desc string, vlanID, vlanVID int) map[string]any {
	p := map[string]any{
		"id": id, "prefix": cidr, "description": desc,
		"status":  map[string]any{"value": "active", "label": "Active"},
		"is_pool": false,
	}
	if vlanID != 0 {
		p["vlan"] = map[string]any{"id": vlanID, "vid": vlanVID}
	}
	return p
}

func ipAddr(id int, address, status, desc, dns string) map[string]any {
	return map[string]any{
		"id": id, "address": address,
		"status":      map[string]any{"value": status, "label": status},
		"description": desc, "dns_name": dns,
	}
}

// listHandler 处理全部列表端点：Token 认证 + limit/offset 分页信封。
func listHandler(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Authorization") == "" || r.Header.Get("Authorization") == "Token " {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_ = json.NewEncoder(w).Encode(map[string]any{"detail": "Authentication credentials were not provided."})
		return
	}
	items, ok := fixtures[r.URL.Path]
	if !ok {
		http.NotFound(w, r)
		return
	}
	limit := queryInt(r, "limit", 50)
	offset := queryInt(r, "offset", 0)
	if offset > len(items) {
		offset = len(items)
	}
	end := offset + limit
	if end > len(items) {
		end = len(items)
	}
	var next *string
	if end < len(items) {
		u := "http://" + r.Host + r.URL.Path + "?limit=" + strconv.Itoa(limit) + "&offset=" + strconv.Itoa(end)
		next = &u
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"count":    len(items),
		"next":     next,
		"previous": nil,
		"results":  items[offset:end],
	})
}

// queryInt 解析整数查询参数，非法时返回默认值。
func queryInt(r *http.Request, key string, def int) int {
	if v := r.URL.Query().Get(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return def
}

func main() {
	addr := os.Getenv("NETBOX_STUB_ADDR")
	if addr == "" {
		addr = ":19092"
	}
	mux := http.NewServeMux()
	for path := range fixtures {
		mux.HandleFunc(path, listHandler)
	}
	log.Printf("NetBox stub 监听于 %s（夹具端点 %d 个）", addr, len(fixtures))
	log.Fatal(http.ListenAndServe(addr, mux))
}
