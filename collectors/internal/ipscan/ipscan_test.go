package ipscan

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// nmapXMLFixture 是内嵌的 nmap -sn -oX 输出夹具，覆盖：
// 普通在线主机、网络设备（Cisco MAC 厂商）、离线主机、无 IPv4 主机。
const nmapXMLFixture = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE nmaprun>
<nmaprun scanner="nmap" args="nmap -sn -oX - 10.80.0.0/29" start="1785000000" startstr="Fri Jul 25 12:00:00 2026" version="7.95">
  <host starttime="1785000000" endtime="1785000002">
    <status state="up" reason="arp-response" reason_ttl="64"/>
    <address addr="10.80.0.1" addrtype="ipv4"/>
    <address addr="AA:BB:CC:00:00:01" addrtype="mac" vendor="Cisco Systems"/>
    <hostnames><hostname name="sw-dist-01.dc1" type="PTR"/></hostnames>
  </host>
  <host starttime="1785000000" endtime="1785000002">
    <status state="up" reason="arp-response" reason_ttl="64"/>
    <address addr="10.80.0.5" addrtype="ipv4"/>
    <address addr="52:54:00:AB:CD:EF" addrtype="mac" vendor="QEMU Virtual NIC"/>
    <hostnames><hostname name="vm-app-01" type="PTR"/></hostnames>
  </host>
  <host starttime="1785000000" endtime="1785000002">
    <status state="down" reason="no-response"/>
    <address addr="10.80.0.6" addrtype="ipv4"/>
  </host>
  <host starttime="1785000000" endtime="1785000002">
    <status state="up" reason="arp-response"/>
    <address addr="00:11:22:33:44:55" addrtype="mac"/>
  </host>
  <runstats><finished time="1785000002" timestr="Fri Jul 25 12:00:02 2026"/><hosts up="3" down="1" total="4"/></runstats>
</nmaprun>`

func TestParseNmapXML(t *testing.T) {
	hosts, started, err := ParseNmapXML(strings.NewReader(nmapXMLFixture))
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if started.Unix() != 1785000000 {
		t.Errorf("扫描开始时间解析失败: %v", started)
	}
	// down 主机与无 IPv4 主机被过滤
	if len(hosts) != 2 {
		t.Fatalf("应解析出 2 台在线且有 IPv4 的主机: %d", len(hosts))
	}
	if hosts[0].IP != "10.80.0.1" || hosts[0].Vendor != "Cisco Systems" || hosts[0].Hostname != "sw-dist-01.dc1" {
		t.Errorf("主机字段解析失败: %+v", hosts[0])
	}
	if hosts[1].IP != "10.80.0.5" {
		t.Errorf("第二台主机解析失败: %+v", hosts[1])
	}
}

func TestIsNetworkDevice(t *testing.T) {
	if !IsNetworkDevice(Host{Vendor: "Cisco Systems"}) {
		t.Error("Cisco 应判定为网络设备")
	}
	if !IsNetworkDevice(Host{Vendor: "HUAWEI TECHNOLOGIES"}) {
		t.Error("HUAWEI 大小写不敏感应判定为网络设备")
	}
	if IsNetworkDevice(Host{Vendor: "QEMU Virtual NIC"}) {
		t.Error("QEMU 虚拟网卡不应判定为网络设备")
	}
	if IsNetworkDevice(Host{}) {
		t.Error("无厂商信息不应判定为网络设备")
	}
}

func TestCollectFromFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "scan.xml")
	if err := os.WriteFile(path, []byte(nmapXMLFixture), 0o600); err != nil {
		t.Fatal(err)
	}

	recs, err := New(path, "").Collect(context.Background())
	if err != nil {
		t.Fatalf("Collect 失败: %v", err)
	}
	// Cisco 网络设备被排除，只剩 vm-app-01
	if len(recs) != 1 {
		t.Fatalf("应产出 1 条记录: %d", len(recs))
	}
	r := recs[0]
	if r.Source != "ip_scan" || r.Collector != "ip-scanner" || r.ModelCandidate != "host" {
		t.Errorf("记录头不符: %+v", r)
	}
	a := r.Attributes
	if a["ip"] != "10.80.0.5" || a["source"] != "ip_scan" {
		t.Errorf("属性不符: %+v", a)
	}
	wantAlive := time.Unix(1785000000, 0).UTC().Format(time.RFC3339)
	if a["last_seen_alive"] != wantAlive {
		t.Errorf("last_seen_alive 应取扫描时间: %v != %v", a["last_seen_alive"], wantAlive)
	}
	if r.OccurredAt.Unix() != 1785000000 {
		t.Errorf("occurred_at 应取扫描时间: %v", r.OccurredAt)
	}
}

func TestCollectWithInjectedRunner(t *testing.T) {
	c := New("", "10.80.0.0/29")
	c.Runner = func(_ context.Context, target string) ([]byte, error) {
		if target != "10.80.0.0/29" {
			t.Errorf("扫描目标不符: %s", target)
		}
		return []byte(nmapXMLFixture), nil
	}
	recs, err := c.Collect(context.Background())
	if err != nil {
		t.Fatalf("Collect 失败: %v", err)
	}
	if len(recs) != 1 {
		t.Fatalf("应产出 1 条记录: %d", len(recs))
	}
}

func TestCollectNoInput(t *testing.T) {
	_, err := New("", "").Collect(context.Background())
	if err == nil || !strings.Contains(err.Error(), "NMAP_FROM_FILE") {
		t.Fatalf("无输入时应返回明确错误: %v", err)
	}
}

func TestCollectMissingFile(t *testing.T) {
	_, err := New(filepath.Join(t.TempDir(), "not-exist.xml"), "").Collect(context.Background())
	if err == nil || !strings.Contains(err.Error(), "读取") {
		t.Fatalf("文件不存在应返回错误: %v", err)
	}
}

func TestCollectRunnerError(t *testing.T) {
	c := New("", "10.0.0.0/24")
	c.Runner = func(context.Context, string) ([]byte, error) {
		return nil, errors.New("exit status 1")
	}
	if _, err := c.Collect(context.Background()); err == nil {
		t.Fatal("runner 失败应返回错误")
	}
}

func TestCollectInvalidXML(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bad.xml")
	if err := os.WriteFile(path, []byte("<nmaprun><host>"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := New(path, "").Collect(context.Background()); err == nil {
		t.Fatal("非法 XML 应返回错误")
	}
}

func TestExecNmapMissingBinary(t *testing.T) {
	orig := lookPath
	lookPath = func(string) (string, error) { return "", errors.New("not found") }
	defer func() { lookPath = orig }()

	_, err := execNmap(context.Background(), "10.0.0.0/24")
	if err == nil || !strings.Contains(err.Error(), "安装 nmap") {
		t.Fatalf("nmap 缺失应提示安装或改用 NMAP_FROM_FILE: %v", err)
	}
}
