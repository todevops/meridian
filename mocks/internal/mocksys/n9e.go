package mocksys

import (
	"encoding/json"
	"fmt"
	"html"
	"net/http"
	"strconv"
	"sync"
	"time"
)

// n9eState 是 n9e mock 的内存态：targets 与当前告警均可在运行期被写端点修改。
type n9eState struct {
	mu      sync.RWMutex
	targets []map[string]any // 与 fixture 同形的 Target 对象（tags/note 可被 PUT 覆写）
	alerts  []map[string]any // 当前告警 fixture（first_trigger_time 已平移到启动时刻附近）
}

// findTarget 按 id 定位 Target，未命中返回 nil。
func (st *n9eState) findTarget(id int64) map[string]any {
	for _, t := range st.targets {
		if v, ok := t["id"].(float64); ok && int64(v) == id {
			return t
		}
	}
	return nil
}

// newN9E 构建 n9e mock（:19001），读写端点一览：
//   - GET  /api/n9e/targets                     返回 Target 数组（官方壳 {"dat":{"list","total"},"err":""}）
//   - PUT  /api/n9e/targets/{id}/tags           body {"tags":"a b"}，写内存态，GET 读回可见
//   - PUT  /api/n9e/targets/{id}/note           body {"note":"..."}，写内存态，GET 读回可见
//   - GET  /api/n9e/alert-cur-events?ident=     当前告警（官方壳 {"dat":[...],"err":""}，ident 过滤）
//   - GET  /dashboards/host?ident=              简易 HTML 仪表盘页（标题含 ident，供 iframe 嵌入）
//
// 除 dashboards 外均需 Authorization Bearer 非空，否则 401。
func newN9E() (http.Handler, error) {
	st, err := loadN9EState()
	if err != nil {
		return nil, err
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/n9e/targets", func(w http.ResponseWriter, r *http.Request) {
		if bearerToken(r) == "" {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "missing or empty bearer token"})
			return
		}
		st.mu.RLock()
		defer st.mu.RUnlock()
		writeJSON(w, http.StatusOK, map[string]any{
			"dat": map[string]any{"list": st.targets, "total": len(st.targets)},
			"err": "",
		})
	})
	// 摘除监控目标（退役联动）：DELETE /api/n9e/targets，body {"ids":[...]}
	mux.HandleFunc("DELETE /api/n9e/targets", func(w http.ResponseWriter, r *http.Request) {
		if bearerToken(r) == "" {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "missing or empty bearer token"})
			return
		}
		var req struct {
			IDs []int64 `json:"ids"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"err": "invalid body: " + err.Error()})
			return
		}
		want := make(map[int64]bool, len(req.IDs))
		for _, id := range req.IDs {
			want[id] = true
		}
		st.mu.Lock()
		kept := st.targets[:0]
		removed := 0
		for _, t := range st.targets {
			idf, _ := t["id"].(float64)
			if want[int64(idf)] {
				removed++
				continue
			}
			kept = append(kept, t)
		}
		st.targets = kept
		st.mu.Unlock()
		writeJSON(w, http.StatusOK, map[string]any{"dat": map[string]any{"removed": removed}, "err": ""})
	})
	mux.HandleFunc("PUT /api/n9e/targets/{id}/tags", func(w http.ResponseWriter, r *http.Request) {
		st.handleTargetWrite(w, r, "tags")
	})
	mux.HandleFunc("PUT /api/n9e/targets/{id}/note", func(w http.ResponseWriter, r *http.Request) {
		st.handleTargetWrite(w, r, "note")
	})
	mux.HandleFunc("GET /api/n9e/alert-cur-events", func(w http.ResponseWriter, r *http.Request) {
		if bearerToken(r) == "" {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "missing or empty bearer token"})
			return
		}
		ident := r.URL.Query().Get("ident")
		st.mu.RLock()
		defer st.mu.RUnlock()
		matched := make([]map[string]any, 0, len(st.alerts))
		for _, a := range st.alerts {
			if ident == "" || a["target_ident"] == ident {
				matched = append(matched, a)
			}
		}
		writeJSON(w, http.StatusOK, map[string]any{"dat": matched, "err": ""})
	})
	mux.HandleFunc("GET /dashboards/host", func(w http.ResponseWriter, r *http.Request) {
		ident := r.URL.Query().Get("ident")
		if ident == "" {
			ident = "（未指定 ident）"
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprintf(w, `<!DOCTYPE html>
<html lang="zh-CN">
<head><meta charset="utf-8"><title>n9e 仪表盘 - %s</title></head>
<body style="font-family:sans-serif;margin:24px">
  <h1>主机监控仪表盘：%s</h1>
  <p>本页由 n9e mock（%s）渲染，用于 CMDB 详情页 iframe 嵌入联调。</p>
  <ul>
    <li>CPU 使用率：23.5%%（近 1 小时平稳）</li>
    <li>内存使用率：61.2%%</li>
    <li>agent 心跳：正常（categraf v0.3.80）</li>
  </ul>
</body>
</html>`, html.EscapeString(ident), html.EscapeString(ident), html.EscapeString(r.Host))
	})
	return mux, nil
}

// handleTargetWrite 处理 PUT /api/n9e/targets/{id}/{field}：
// 校验 Bearer → 解析路径 id 与 body → 覆写内存态 → 返回官方壳（dat 为更新后的 Target）。
func (st *n9eState) handleTargetWrite(w http.ResponseWriter, r *http.Request, field string) {
	if bearerToken(r) == "" {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "missing or empty bearer token"})
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"dat": nil, "err": "id 须为整数"})
		return
	}
	var body map[string]string
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"dat": nil, "err": "请求体须为 JSON 对象"})
		return
	}
	value, ok := body[field]
	if !ok {
		writeJSON(w, http.StatusBadRequest, map[string]any{"dat": nil, "err": "缺少字段 " + field})
		return
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	t := st.findTarget(id)
	if t == nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"dat": nil, "err": fmt.Sprintf("target %d 不存在", id)})
		return
	}
	t[field] = value
	writeJSON(w, http.StatusOK, map[string]any{"dat": t, "err": ""})
}

// loadN9EState 读取 targets 与告警 fixture：
// update_at / first_trigger_time 均整体平移到启动时刻附近（fixture 内只维护相对时间间隔），
// 保证无论何时启动 mock，心跳新鲜度与告警时机的演示效果恒定。
func loadN9EState() (*n9eState, error) {
	raw, err := readFixture("n9e-targets.json")
	if err != nil {
		return nil, fmt.Errorf("读取 n9e-targets.json 失败: %w", err)
	}
	var targets []map[string]any
	if err := json.Unmarshal(raw, &targets); err != nil {
		return nil, fmt.Errorf("解析 n9e-targets.json 失败: %w", err)
	}
	shiftUnixFields(targets, "update_at")

	raw, err = readFixture("n9e-alerts.json")
	if err != nil {
		return nil, fmt.Errorf("读取 n9e-alerts.json 失败: %w", err)
	}
	var alerts []map[string]any
	if err := json.Unmarshal(raw, &alerts); err != nil {
		return nil, fmt.Errorf("解析 n9e-alerts.json 失败: %w", err)
	}
	shiftUnixFields(alerts, "first_trigger_time", "trigger_time")
	return &n9eState{targets: targets, alerts: alerts}, nil
}

// shiftUnixFields 把对象数组中指定 Unix 秒字段整体平移：以各对象该字段的最大值为锚点对齐当前时间。
func shiftUnixFields(objs []map[string]any, fields ...string) {
	var maxV int64
	for _, o := range objs {
		for _, f := range fields {
			if v, ok := o[f].(float64); ok && int64(v) > maxV {
				maxV = int64(v)
			}
		}
	}
	if maxV == 0 {
		return
	}
	delta := time.Now().Unix() - maxV
	for _, o := range objs {
		for _, f := range fields {
			if v, ok := o[f].(float64); ok {
				o[f] = int64(v) + delta
			}
		}
	}
}
