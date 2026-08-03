package librenms

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// mockDevices 是 LibreNMS /api/v0/devices 夹具。
const mockDevices = `{
  "status": "ok",
  "count": 2,
  "devices": [
    {"device_id": 1, "hostname": "sw-core-01", "sysName": "sw-core-01.dc1", "ip": "10.70.0.2", "vendor": "Cisco", "hardware": "C9300-48P", "serial": "FCW1234A0BC", "os": "iosxe"},
    {"device_id": 2, "hostname": "fw-edge-01", "ip": "10.70.0.1", "vendor": "H3C", "model": "SecPath F1000", "serial": "H3C98765"}
  ]
}`

func TestCollectMapping(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v0/devices" {
			// links 邻居端点：本夹具不提供，按 404 容错跳过处理。
			w.WriteHeader(http.StatusNotFound)
			w.Write([]byte(`{"status":"error","message":"设备不存在或无邻居数据"}`))
			return
		}
		if tok := r.Header.Get("X-Auth-Token"); tok != "test-token" {
			t.Errorf("X-Auth-Token 不符: %q", tok)
		}
		w.Write([]byte(mockDevices))
	}))
	defer srv.Close()

	recs, err := New(srv.URL, "test-token").Collect(context.Background())
	if err != nil {
		t.Fatalf("Collect 失败: %v", err)
	}
	if len(recs) != 2 {
		t.Fatalf("应产出 2 条记录: %d", len(recs))
	}

	r := recs[0]
	if r.Source != "librenms" || r.Collector != "librenms-device-collector" || r.ModelCandidate != "network_device" {
		t.Errorf("记录头不符: %+v", r)
	}
	a := r.Attributes
	if a["name"] != "sw-core-01.dc1" || a["mgmt_ip"] != "10.70.0.2" {
		t.Errorf("sysname/ip 不符: %+v", a)
	}
	if a["vendor"] != "Cisco" || a["model"] != "C9300-48P" || a["serial_no"] != "FCW1234A0BC" {
		t.Errorf("vendor/model/serial 不符: %+v", a)
	}
	if a["source"] != "librenms" {
		t.Errorf("source 属性不符: %+v", a)
	}

	// 第二条：无 sysName 回退 hostname，无 hardware 取 model 字段
	a2 := recs[1].Attributes
	if a2["name"] != "fw-edge-01" || a2["model"] != "SecPath F1000" {
		t.Errorf("字段回退不符: %+v", a2)
	}
}

func TestCollectRequiresToken(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("token 为空时不应发起请求")
	}))
	defer srv.Close()

	_, err := New(srv.URL, "").Collect(context.Background())
	if err == nil || !strings.Contains(err.Error(), "LIBRENMS_API_TOKEN") {
		t.Fatalf("空 token 应返回明确错误: %v", err)
	}
}

func TestCollectUnauthorized(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	if _, err := New(srv.URL, "bad-token").Collect(context.Background()); err == nil {
		t.Fatal("401 应返回错误")
	}
}

func TestCollectErrorStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"status":"error","message":"db down"}`))
	}))
	defer srv.Close()

	_, err := New(srv.URL, "test-token").Collect(context.Background())
	if err == nil || !strings.Contains(err.Error(), "db down") {
		t.Fatalf("status!=ok 应返回错误: %v", err)
	}
}

func TestCollectSkipsGarbageDevice(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"status":"ok","devices":[{"device_id":9},{"hostname":"ok-1","ip":"10.0.0.9"}]}`))
	}))
	defer srv.Close()

	recs, err := New(srv.URL, "test-token").Collect(context.Background())
	if err != nil {
		t.Fatalf("Collect 失败: %v", err)
	}
	if len(recs) != 1 || recs[0].Attributes["name"] != "ok-1" {
		t.Fatalf("无 ip 且无 sysname 的设备应跳过: %+v", recs)
	}
}

// mockDevicesWithLinks 是含 links 邻居端点测试的设备夹具。
const mockDevicesWithLinks = `{
  "status": "ok",
  "devices": [
    {"hostname": "bj-core-sw-01", "sysName": "bj-core-sw-01.dc1", "ip": "10.70.0.2"},
    {"hostname": "ghost-sw-99", "ip": "10.70.0.99"}
  ]
}`

// mockLinks 是 bj-core-sw-01 的邻居表夹具（一条正常 + 一条缺 protocol + 一条垃圾数据）。
const mockLinks = `{
  "status": "ok",
  "links": [
    {"local_port": "Gi0/1", "remote_device": "bj-core-sw-02", "remote_port": "Gi0/1", "protocol": "lldp"},
    {"local_port": "Gi0/2", "remote_device": "sh-dist-sw-01", "remote_port": "Gi0/1"},
    {"local_port": "Gi0/9", "remote_device": "", "remote_port": "Gi0/9"}
  ]
}`

// newLinksServer 起含 links 端点的 LibreNMS 夹具：ghost-sw-99 返回 404（容错跳过用例）。
func newLinksServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if tok := r.Header.Get("X-Auth-Token"); tok != "test-token" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/api/v0/devices":
			w.Write([]byte(mockDevicesWithLinks))
		case "/api/v0/devices/bj-core-sw-01/links":
			w.Write([]byte(mockLinks))
		default:
			w.WriteHeader(http.StatusNotFound)
			w.Write([]byte(`{"status":"error","message":"设备不存在或无邻居数据"}`))
		}
	}))
}

func TestCollectLinksMapping(t *testing.T) {
	srv := newLinksServer(t)
	defer srv.Close()

	recs, err := New(srv.URL, "test-token").Collect(context.Background())
	if err != nil {
		t.Fatalf("Collect 失败: %v", err)
	}
	// 2 条设备 + 2 条链路（垃圾邻居跳过；ghost-sw-99 的 links 404 容错跳过）。
	devices, links := 0, 0
	var linkRecs []map[string]any
	for _, r := range recs {
		switch r.ModelCandidate {
		case "network_device":
			devices++
		case "network_link":
			links++
			linkRecs = append(linkRecs, r.Attributes)
		}
	}
	if devices != 2 || links != 2 {
		t.Fatalf("应产出 2 设备 + 2 链路: %d/%d（记录: %+v）", devices, links, recs)
	}

	l := linkRecs[0]
	if l["local_device"] != "bj-core-sw-01" || l["local_port"] != "Gi0/1" {
		t.Errorf("本端映射不符: %+v", l)
	}
	if l["remote_device"] != "bj-core-sw-02" || l["remote_port"] != "Gi0/1" {
		t.Errorf("对端映射不符: %+v", l)
	}
	if l["protocol"] != "lldp" || l["source"] != "lldp" {
		t.Errorf("protocol/source 不符: %+v", l)
	}

	// 第二条缺 protocol，应回退 lldp。
	l2 := linkRecs[1]
	if l2["protocol"] != "lldp" || l2["remote_device"] != "sh-dist-sw-01" {
		t.Errorf("protocol 缺省回退不符: %+v", l2)
	}
}

func TestCollectLinksServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v0/devices" {
			w.Write([]byte(mockDevicesWithLinks))
			return
		}
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	// links 端点非 404 错误不容忍，整体报错（防止邻居数据静默缺失）。
	if _, err := New(srv.URL, "test-token").Collect(context.Background()); err == nil {
		t.Fatal("links 500 应返回错误")
	}
}
