// Package record 定义采集器上报 CMDB 的标准发现记录结构，
// 字段与 pkg/openapi/openapi.yaml 中 DiscoveryRecord 契约一一对应。
package record

import "time"

// Record 是标准发现记录。
type Record struct {
	Source         string         `json:"source"`          // 发现来源系统
	Collector      string         `json:"collector"`       // 采集器标识
	ModelCandidate string         `json:"model_candidate"` // 候选模型编码，调和引擎据此匹配
	Attributes     map[string]any `json:"attributes"`      // 采集到的原始属性键值对
	OccurredAt     time.Time      `json:"occurred_at"`     // 采集发生时间
}

// StrField 从属性字典中按候选键顺序取第一个非空字符串值；
// 兼容字符串、字符串数组（取首个元素）与其他标量（fmt 转写），用于容忍不同数据源的字段命名差异。
func StrField(m map[string]any, keys ...string) string {
	for _, k := range keys {
		v, ok := m[k]
		if !ok || v == nil {
			continue
		}
		switch t := v.(type) {
		case string:
			if t != "" {
				return t
			}
		case []any:
			for _, e := range t {
				if s, ok := e.(string); ok && s != "" {
					return s
				}
			}
		case []string:
			for _, s := range t {
				if s != "" {
					return s
				}
			}
		}
	}
	return ""
}
