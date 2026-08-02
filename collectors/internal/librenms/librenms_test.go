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
			t.Errorf("路径不符: %s", r.URL.Path)
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
