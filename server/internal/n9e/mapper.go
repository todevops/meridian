// n9e Target → 标准发现记录的字段映射（方案文档 5.1 节映射表）。
package n9e

import (
	"strings"
	"time"

	"cmdb/server/internal/reconcile"
)

// Source 是 n9e 数据来源标识。
const Source = "n9e"

// Collector 是 n9e 消费器的采集器标识。
const Collector = "n9e-target-puller"

// MapTarget 把一个 n9e Target 映射为标准发现记录：
//
//	Ident → ident（调和辅助键）、HostIp → ip、OS → os、CpuNum → cpu_num、
//	Arch → arch、AgentVersion → agent_version、UpdateAt → last_seen_at（RFC3339）、
//	GroupName → biz_group、Tags/HostTags → tags（合并去重）。
func MapTarget(t Target, now time.Time) reconcile.Record {
	occurredAt := now
	var lastSeen string
	if t.UpdateAt > 0 {
		ts := time.Unix(t.UpdateAt, 0).UTC()
		lastSeen = ts.Format(time.RFC3339)
		occurredAt = ts
	}
	attrs := map[string]any{
		"ident":         t.Ident,
		"ip":            t.HostIP,
		"os":            t.OS,
		"cpu_num":       t.CPUNum,
		"arch":          t.Arch,
		"agent_version": t.AgentVersion,
		"biz_group":     t.GroupName,
		"tags":          mergeTags(string(t.Tags), string(t.HostTags)),
	}
	if lastSeen != "" {
		attrs["last_seen_at"] = lastSeen
	}
	return reconcile.Record{
		Source:         Source,
		Collector:      Collector,
		ModelCandidate: "host",
		Attributes:     attrs,
		OccurredAt:     occurredAt,
	}
}

// mergeTags 合并 n9e 的 tags 与 host_tags（均为空格分隔字符串），去重后以空格连接输出。
func mergeTags(tags, hostTags string) string {
	seen := map[string]bool{}
	out := []string{}
	for _, s := range strings.Fields(tags + " " + hostTags) {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return strings.Join(out, " ")
}
