package mocksys

import (
	"encoding/json"
	"fmt"
	"net/http"
)

// newLibreNMS 构建 LibreNMS mock（:19003）：
// GET /api/v0/devices 返回设备清单；GET /api/v0/devices/{hostname}/ports 返回端口清单；
// X-Auth-Token 非空校验（空则 401）；响应包装为 {status:"ok",...}。
func newLibreNMS() (http.Handler, error) {
	devRaw, err := readFixture("librenms-devices.json")
	if err != nil {
		return nil, fmt.Errorf("读取 librenms-devices.json 失败: %w", err)
	}
	if !json.Valid(devRaw) {
		return nil, fmt.Errorf("librenms-devices.json 不是合法 JSON")
	}
	portsRaw, err := readFixture("librenms-ports.json")
	if err != nil {
		return nil, fmt.Errorf("读取 librenms-ports.json 失败: %w", err)
	}
	var ports map[string]json.RawMessage
	if err := json.Unmarshal(portsRaw, &ports); err != nil {
		return nil, fmt.Errorf("解析 librenms-ports.json 失败: %w", err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v0/devices", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"status":  "ok",
			"devices": json.RawMessage(devRaw),
		})
	})
	mux.HandleFunc("GET /api/v0/devices/{hostname}/ports", func(w http.ResponseWriter, r *http.Request) {
		hostname := r.PathValue("hostname")
		p, ok := ports[hostname]
		if !ok {
			writeJSON(w, http.StatusNotFound, map[string]string{
				"status":  "error",
				"message": fmt.Sprintf("设备 %s 不存在", hostname),
			})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"status": "ok",
			"ports":  p,
		})
	})
	return librenmsAuth(mux), nil
}

// librenmsAuth 对整个路由做 X-Auth-Token 校验（官方缺失凭据返回 401）。
func librenmsAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Auth-Token") == "" {
			writeJSON(w, http.StatusUnauthorized, map[string]string{
				"status":  "error",
				"message": "API Token is missing",
			})
			return
		}
		next.ServeHTTP(w, r)
	})
}
