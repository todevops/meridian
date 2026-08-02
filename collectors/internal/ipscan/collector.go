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
	now      func() time.Time
}

// New 创建采集器。
func New(fromFile, target string) *Collector {
	return &Collector{FromFile: fromFile, Target: target, now: time.Now}
}

// Name 返回采集器名。
func (c *Collector) Name() string { return "ipscan" }

// Collect 获取扫描结果（文件或现扫），解析并映射为发现记录：
// 仅保留在线且非网络设备的主机。
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
	recs := make([]record.Record, 0, len(hosts))
	for _, h := range hosts {
		if IsNetworkDevice(h) {
			continue
		}
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
	return recs, nil
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
