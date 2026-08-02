// 扫描执行（from-file / exec nmap）与发现记录映射。
package ipscan

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"time"

	"collectors/internal/record"
)

const (
	// Source 是发现来源系统标识。
	Source = "ip_scan"
	// CollectorName 是采集器标识。
	CollectorName = "ip-scanner"
)

// lookPath 依赖注入点：测试可替换以模拟 nmap 缺失。
var lookPath = exec.LookPath

// execNmap 执行 nmap -sn -oX - <网段> 并返回 XML 输出。
func execNmap(ctx context.Context, target string) ([]byte, error) {
	path, err := lookPath("nmap")
	if err != nil {
		return nil, errors.New("未找到 nmap：请安装 nmap（https://nmap.org/download），或设置 NMAP_FROM_FILE 使用已有扫描结果文件")
	}
	cmd := exec.CommandContext(ctx, path, "-sn", "-oX", "-", target)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("执行 nmap 扫描 %s 失败: %w（%s）", target, err, bytes.TrimSpace(stderr.Bytes()))
	}
	return out, nil
}

// Collector 是 IP 网段扫描采集器。
type Collector struct {
	FromFile string                                                   // NMAP_FROM_FILE：已有 nmap -oX 结果文件，优先级最高
	Target   string                                                   // NMAP_SCAN_TARGET：无 from-file 时 exec nmap 扫描的网段
	Runner   func(ctx context.Context, target string) ([]byte, error) // 默认 execNmap，测试可注入
	cmdbAPI  string                                                   // CMDB API 地址（空串则不做 IPAM 比对，全量存活上报）
	cmdbTok  string                                                   // CMDB Bearer 令牌
	logf     func(format string, args ...any)
	now      func() time.Time
}

// New 创建采集器。cmdbAPI 非空时启用 IPAM 比对（未配置则退化为全量存活上报）；
// logf 可为 nil（比对摘要/回收线索/MAC 告警日志）。
func New(fromFile, target, cmdbAPI, cmdbToken string, logf func(format string, args ...any)) *Collector {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	return &Collector{FromFile: fromFile, Target: target, cmdbAPI: cmdbAPI, cmdbTok: cmdbToken, logf: logf, now: time.Now}
}

// Name 返回采集器名。
func (c *Collector) Name() string { return "ipscan" }

// Collect 获取扫描结果（文件或现扫），解析为在线主机列表后：
// 配置 CMDB 时与 IPAM 比对——已登记且存活的跳过、已登记不存活打印回收线索、
// 未登记存活生成黑设备发现记录、MAC 变更打印告警；IPAM 不可达降级为全量存活上报。
func (c *Collector) Collect(ctx context.Context) ([]record.Record, error) {
	data, err := c.load(ctx)
	if err != nil {
		return nil, err
	}
	hosts, started, err := ParseNmapXML(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	at := started
	if at.IsZero() {
		at = c.now()
	}
	// 网络设备交由 LibreNMS 通道发现，不参与 IPAM 比对与上报
	alive := make([]Host, 0, len(hosts))
	for _, h := range hosts {
		if !IsNetworkDevice(h) {
			alive = append(alive, h)
		}
	}
	if c.cmdbAPI == "" {
		return buildAliveRecords(alive, at), nil
	}
	cmp, err := compareWithIPAM(ctx, newIPAMClient(c.cmdbAPI, c.cmdbTok), alive, at)
	if err != nil {
		c.logf("IPAM 比对不可用（%v），降级为全量存活上报", err)
		return buildAliveRecords(alive, at), nil
	}
	for _, lead := range cmp.recycleLeads {
		c.logf("回收线索: ip=%s status=%s ci_id=%s 已登记但扫描不存活，建议核查回收", lead.IP, lead.Status, lead.CIID)
	}
	for _, alert := range cmp.macAlerts {
		c.logf("%s", alert)
	}
	c.logf("IPAM 比对摘要: 在线主机 %d 台，已登记且存活 %d（跳过），已登记不存活 %d（回收线索），未登记存活 %d（黑设备进发现池），MAC 变更 %d（告警）",
		len(alive), cmp.registeredAlive, len(cmp.recycleLeads), len(cmp.blackRecords), len(cmp.macAlerts))
	return cmp.blackRecords, nil
}

// buildAliveRecords 把在线主机列表全量映射为 host 发现记录（无 IPAM 比对时的原始行为）。
func buildAliveRecords(alive []Host, at time.Time) []record.Record {
	recs := make([]record.Record, 0, len(alive))
	for _, h := range alive {
		recs = append(recs, record.Record{
			Source:         Source,
			Collector:      CollectorName,
			ModelCandidate: "host",
			Attributes: map[string]any{
				"ip":              h.IP,
				"source":          "ip_scan",
				"last_seen_alive": at.Format(time.RFC3339),
			},
			OccurredAt: at,
		})
	}
	return recs
}

// load 按优先级取扫描结果：NMAP_FROM_FILE 文件 → exec nmap 现扫。
func (c *Collector) load(ctx context.Context) ([]byte, error) {
	if c.FromFile != "" {
		data, err := os.ReadFile(c.FromFile)
		if err != nil {
			return nil, fmt.Errorf("读取 NMAP_FROM_FILE %s 失败: %w", c.FromFile, err)
		}
		return data, nil
	}
	if c.Target == "" {
		return nil, errors.New("未配置 NMAP_FROM_FILE 且未指定扫描网段 NMAP_SCAN_TARGET，ipscan 无输入可用")
	}
	runner := c.Runner
	if runner == nil {
		runner = execNmap
	}
	return runner(ctx, c.Target)
}
