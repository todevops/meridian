package mocksys

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"
)

// tsdbSeries 是一条时序样本：标签集合 + 字符串形式的样本值（Prometheus 协议）。
type tsdbSeries struct {
	Labels map[string]string `json:"labels"`
	Value  string            `json:"value"`
}

// vectorItem 是 Prometheus vector 结果元素：metric 标签 + [时间戳, 值]。
type vectorItem struct {
	Metric map[string]string `json:"metric"`
	Value  []any             `json:"value"`
}

// newTSDB 构建 TSDB（Prometheus 兼容）mock（:19004）：
// GET /api/v1/query?query=<metric> 返回 vector（时间戳取当前时间）；
// GET /api/v1/label/instance/values?match[]=<metric> 返回去重后的 instance 标签值。
func newTSDB() (http.Handler, error) {
	raw, err := readFixture("tsdb-metrics.json")
	if err != nil {
		return nil, fmt.Errorf("读取 tsdb-metrics.json 失败: %w", err)
	}
	var metrics map[string][]tsdbSeries
	if err := json.Unmarshal(raw, &metrics); err != nil {
		return nil, fmt.Errorf("解析 tsdb-metrics.json 失败: %w", err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/query", func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query().Get("query")
		if query == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{
				"status":    "error",
				"errorType": "bad_data",
				"error":     "invalid parameter \"query\": unknown position: parse error: no expression found in input",
			})
			return
		}
		series := metrics[metricName(query)]
		result := make([]vectorItem, 0, len(series))
		now := float64(time.Now().Unix())
		for _, s := range series {
			result = append(result, vectorItem{Metric: s.Labels, Value: []any{now, s.Value}})
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"status": "success",
			"data":   map[string]any{"resultType": "vector", "result": result},
		})
	})
	mux.HandleFunc("GET /api/v1/label/instance/values", func(w http.ResponseWriter, r *http.Request) {
		set := make(map[string]struct{})
		for _, match := range r.URL.Query()["match[]"] {
			for _, s := range metrics[metricName(match)] {
				if v, ok := s.Labels["instance"]; ok {
					set[v] = struct{}{}
				}
			}
		}
		values := make([]string, 0, len(set))
		for v := range set {
			values = append(values, v)
		}
		sort.Strings(values)
		writeJSON(w, http.StatusOK, map[string]any{"status": "success", "data": values})
	})
	return mux, nil
}

// metricName 从 PromQL 选择器中提取指标名（截断首个 { 之前的部分）。
func metricName(selector string) string {
	name, _, _ := strings.Cut(selector, "{")
	return strings.TrimSpace(name)
}
