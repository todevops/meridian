// Package ipscan 实现 IP 网段扫描采集器：
// 解析 nmap -oX XML（NMAP_FROM_FILE 指定的已有结果，或 exec nmap -sn 现扫），
// 在线且非网络设备的主机映射为 host 标准发现记录。
package ipscan

import (
	"encoding/xml"
	"fmt"
	"io"
	"strings"
	"time"
)

// nmapRun 对应 nmap -oX 输出的根元素。
type nmapRun struct {
	XMLName xml.Name   `xml:"nmaprun"`
	Start   int64      `xml:"start,attr"`
	Hosts   []nmapHost `xml:"host"`
}

// nmapHost 对应一个被扫主机。
type nmapHost struct {
	Status    nmapStatus     `xml:"status"`
	Addresses []nmapAddress  `xml:"address"`
	Hostnames []nmapHostname `xml:"hostnames>hostname"`
}

// nmapStatus 对应状态元素。
type nmapStatus struct {
	State string `xml:"state,attr"`
}

// nmapAddress 对应地址元素（ipv4/ipv6/mac，mac 可能带 vendor）。
type nmapAddress struct {
	Addr     string `xml:"addr,attr"`
	AddrType string `xml:"addrtype,attr"`
	Vendor   string `xml:"vendor,attr"`
}

// nmapHostname 对应主机名元素。
type nmapHostname struct {
	Name string `xml:"name,attr"`
}

// Host 是解析后的一台在线主机。
type Host struct {
	IP       string // 首个 IPv4 地址
	MAC      string
	Vendor   string // MAC 地址厂商（nmap 本地网段扫描时给出）
	Hostname string
}

// networkVendors 是网络设备 MAC 厂商关键词（小写子串匹配）。
// 命中者视为网络设备，交由 LibreNMS 通道发现，本采集器不重复上报。
var networkVendors = []string{
	"cisco", "huawei", "h3c", "juniper", "arista", "ruijie",
	"fortinet", "aruba", "zte", "palo alto", "extreme networks", "mikrotik",
}

// ParseNmapXML 解析 nmap -oX XML，返回在线主机列表与扫描开始时间（缺失时为零值）。
func ParseNmapXML(r io.Reader) ([]Host, time.Time, error) {
	var run nmapRun
	if err := xml.NewDecoder(r).Decode(&run); err != nil {
		return nil, time.Time{}, fmt.Errorf("解析 nmap XML 失败: %w", err)
	}
	var started time.Time
	if run.Start > 0 {
		started = time.Unix(run.Start, 0).UTC()
	}
	hosts := make([]Host, 0, len(run.Hosts))
	for _, h := range run.Hosts {
		if h.Status.State != "up" {
			continue
		}
		var out Host
		for _, a := range h.Addresses {
			switch a.AddrType {
			case "ipv4":
				if out.IP == "" {
					out.IP = a.Addr
				}
			case "mac":
				out.MAC = a.Addr
				out.Vendor = a.Vendor
			}
		}
		if len(h.Hostnames) > 0 {
			out.Hostname = h.Hostnames[0].Name
		}
		if out.IP == "" {
			continue // 无 IPv4 地址无法入库，跳过
		}
		hosts = append(hosts, out)
	}
	return hosts, started, nil
}

// IsNetworkDevice 按 MAC 厂商关键词启发式判断主机是否为网络设备。
func IsNetworkDevice(h Host) bool {
	v := strings.ToLower(h.Vendor)
	if v == "" {
		return false
	}
	for _, kw := range networkVendors {
		if strings.Contains(v, kw) {
			return true
		}
	}
	return false
}
