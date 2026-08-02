package mocksys

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// newN9E 构建 n9e mock（:19001）：
// GET /api/n9e/targets 返回 Target 数组，Authorization Bearer 非空校验（空则 401）。
func newN9E() (http.Handler, error) {
	raw, err := readFixture("n9e-targets.json")
	if err != nil {
		return nil, fmt.Errorf("读取 n9e-targets.json 失败: %w", err)
	}

	// 解析为通用数组，把 update_at 整体平移到当前时间附近：
	// fixture 内只维护相对时间间隔（web-dup×2 新鲜、db-mysql-01 停滞 9 天），
	// 保证无论何时启动 mock，心跳新鲜度演示效果恒定。
	var targets []map[string]any
	if err := json.Unmarshal(raw, &targets); err != nil {
		return nil, fmt.Errorf("解析 n9e-targets.json 失败: %w", err)
	}
	var maxUpdateAt int64
	for _, t := range targets {
		if v, ok := t["update_at"].(float64); ok && int64(v) > maxUpdateAt {
			maxUpdateAt = int64(v)
		}
	}
	delta := time.Now().Unix() - maxUpdateAt
	for _, t := range targets {
		if v, ok := t["update_at"].(float64); ok {
			t["update_at"] = int64(v) + delta
		}
	}
	// 按官方响应壳包装：{"dat":{"list":[...],"total":N},"err":""}
	body, err := json.Marshal(map[string]any{
		"dat": map[string]any{"list": targets, "total": len(targets)},
		"err": "",
	})
	if err != nil {
		return nil, fmt.Errorf("序列化 n9e targets 失败: %w", err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/n9e/targets", func(w http.ResponseWriter, r *http.Request) {
		if bearerToken(r) == "" {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "missing or empty bearer token"})
			return
		}
		writeRawJSON(w, http.StatusOK, body)
	})
	return mux, nil
}
