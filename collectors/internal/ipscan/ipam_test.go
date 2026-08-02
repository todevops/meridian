package ipscan

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ipamFixtureXML 覆盖四类比对分支的扫描输入：
//   - 10.80.0.5 在线（MAC 52:54:00:AB:CD:EF），已登记且 CI MAC 一致 → 跳过
//   - 10.80.0.9 在线（MAC 52:54:00:00:00:09），已登记但 CI MAC 不一致 → MAC 变更告警
//   - 10.80.0.10 在线，未登记 → 黑设备发现记录
//   - 10.80.0.6 已登记但不在扫描结果 → 回收线索（由下方 IPAM 夹具提供）
const ipamFixtureXML = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE nmaprun>
<nmaprun scanner="nmap" args="nmap -sn -oX - 10.80.0.0/24" start="1785000000" version="7.95">
  <host><status state="up"/><address addr="10.80.0.5" addrtype="ipv4"/><address addr="52:54:00:AB:CD:EF" addrtype="mac" vendor="QEMU"/></host>
  <host><status state="up"/><address addr="10.80.0.9" addrtype="ipv4"/><address addr="52:54:00:00:00:09" addrtype="mac" vendor="QEMU"/><hostnames><hostname name="vm-db-01" type="PTR"/></hostnames></host>
  <host><status state="up"/><address addr="10.80.0.10" addrtype="ipv4"/><address addr="52:54:00:00:00:0A" addrtype="mac" vendor="QEMU"/></host>
</nmaprun>`

// newIPAMFixture 起 CMDB IPAM 夹具：1 个前缀，3 个登记 IP，2 个关联 CI（含 MAC 基线）。
func newIPAMFixture(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/ipam/prefixes", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"items": []map[string]any{{"id": "p1", "cidr": "10.80.0.0/24"}},
			"total": 1, "page": 1, "page_size": 500,
		})
	})
	mux.HandleFunc("/api/v1/ipam/ips", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("prefix_id") != "p1" {
			t.Errorf("prefix_id 不符: %s", r.URL.RawQuery)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"items": []map[string]any{
				{"id": "ip1", "ip": "10.80.0.5", "status": "used", "ci_id": "ci-1"},
				{"id": "ip2", "ip": "10.80.0.6", "status": "used", "ci_id": ""},
				{"id": "ip3", "ip": "10.80.0.9", "status": "used", "ci_id": "ci-2"},
			},
			"total": 3, "page": 1, "page_size": 500,
		})
	})
	mux.HandleFunc("/api/v1/cis/", func(w http.ResponseWriter, r *http.Request) {
		macs := map[string]string{
			"/api/v1/cis/ci-1": "52:54:00:ab:cd:ef", // 与实测一致（大小写不同，归一化后相同）
			"/api/v1/cis/ci-2": "00:16:3e:99:99:99", // 与实测不一致 → MAC 变更
		}
		mac, ok := macs[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"id": r.URL.Path, "attributes": map[string]any{"mac": mac}})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func writeScanFile(t *testing.T, xml string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "scan.xml")
	if err := os.WriteFile(path, []byte(xml), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestIPAMCompareFourBranches(t *testing.T) {
	srv := newIPAMFixture(t)
	logs := []string{}
	c := New(writeScanFile(t, ipamFixtureXML), "", srv.URL, "token", func(f string, args ...any) {
		logs = append(logs, fmt.Sprintf(f, args...))
	})

	recs, err := c.Collect(context.Background())
	if err != nil {
		t.Fatalf("Collect 失败: %v", err)
	}

	// 只有未登记存活的 10.80.0.10 产出黑设备发现记录
	if len(recs) != 1 {
		t.Fatalf("应只产出 1 条黑设备记录: %+v", recs)
	}
	a := recs[0].Attributes
	if a["ip"] != "10.80.0.10" || a["black_device_risk"] != true {
		t.Errorf("黑设备记录属性不符: %+v", a)
	}
	if a["mac"] != "52:54:00:00:00:0A" {
		t.Errorf("黑设备记录应携带实测 MAC: %+v", a)
	}
	if recs[0].ModelCandidate != "host" || recs[0].Source != Source {
		t.Errorf("记录头不符: %+v", recs[0])
	}

	joined := strings.Join(logs, "\n")
	// 回收线索：已登记不存活的 10.80.0.6
	if !strings.Contains(joined, "回收线索") || !strings.Contains(joined, "10.80.0.6") {
		t.Errorf("缺 10.80.0.6 回收线索:\n%s", joined)
	}
	// MAC 变更：10.80.0.9 登记 MAC 与实测不一致
	if !strings.Contains(joined, "MAC 变更告警") || !strings.Contains(joined, "10.80.0.9") {
		t.Errorf("缺 10.80.0.9 MAC 变更告警:\n%s", joined)
	}
	// MAC 一致的 10.80.0.5 不应出现在任何 MAC 变更告警行
	for _, line := range logs {
		if strings.Contains(line, "MAC 变更告警") && strings.Contains(line, "10.80.0.5") {
			t.Errorf("MAC 一致的 10.80.0.5 不应被告警: %s", line)
		}
	}
	// 比对摘要
	if !strings.Contains(joined, "已登记且存活 2") || !strings.Contains(joined, "未登记存活 1") {
		t.Errorf("比对摘要不符:\n%s", joined)
	}
}

func TestIPAMUnavailableDegrades(t *testing.T) {
	// 起即关的服务器，保证连接被拒
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	srv.Close()

	logs := []string{}
	c := New(writeScanFile(t, ipamFixtureXML), "", srv.URL, "", func(f string, args ...any) {
		logs = append(logs, fmt.Sprintf(f, args...))
	})
	recs, err := c.Collect(context.Background())
	if err != nil {
		t.Fatalf("IPAM 不可达应降级而非失败: %v", err)
	}
	// 降级为全量存活上报（3 台在线主机全部产出）
	if len(recs) != 3 {
		t.Fatalf("降级应全量上报 3 条: %d", len(recs))
	}
	for _, r := range recs {
		if _, risky := r.Attributes["black_device_risk"]; risky {
			t.Errorf("降级模式不应标 black_device_risk: %+v", r.Attributes)
		}
	}
	joined := strings.Join(logs, "\n")
	if !strings.Contains(joined, "降级为全量存活上报") {
		t.Errorf("应注明降级:\n%s", joined)
	}
}
