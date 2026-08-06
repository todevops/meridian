// DBMS EOL 清单导出（US-3.3）单测：JSON 过滤/CSV BOM/应用反查/数据范围裁剪。
package httpapi

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

// doRaw 发起 GET 请求并返回原始响应体（CSV 等非 JSON 场景）。
func doRaw(t *testing.T, url, token string) (int, []byte, http.Header) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		t.Fatalf("构造请求失败: %v", err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("请求失败: %v", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("读取响应失败: %v", err)
	}
	return resp.StatusCode, raw, resp.Header
}

func TestEOLReportJSONFilters(t *testing.T) {
	_, srv, _, adminToken := setupScope(t)

	code, body := doJSON(t, http.MethodGet, srv.URL+"/api/v1/dbms/eol-report", nil, adminToken)
	if code != http.StatusOK {
		t.Fatalf("EOL 查询失败（%d）: %v", code, body)
	}
	items := body["items"].([]any)
	if len(items) != 2 {
		t.Fatalf("期望 2 条实例，实际 %d", len(items))
	}
	first := items[0].(map[string]any)
	if first["instance_addr"] != "10.1.0.1:3306" || first["cluster_name"] != "order-mysql" {
		t.Fatalf("实例字段错误: %v", first)
	}
	// 沿 depends_on 入向反查应用与负责人。
	if first["app_name"] != "订单中心" || first["app_owner"] != "张三" {
		t.Fatalf("应用反查错误: %v", first)
	}
	// component + version_prefix 过滤。
	_, body = doJSON(t, http.MethodGet, srv.URL+"/api/v1/dbms/eol-report?component=mysql&version_prefix=5.7", nil, adminToken)
	items = body["items"].([]any)
	if len(items) != 1 || items[0].(map[string]any)["version"] != "5.7.44" {
		t.Fatalf("版本前缀过滤错误: %v", items)
	}
	_, body = doJSON(t, http.MethodGet, srv.URL+"/api/v1/dbms/eol-report?component=redis", nil, adminToken)
	if len(body["items"].([]any)) != 0 {
		t.Fatalf("component 过滤错误: %v", body)
	}
	// 非法 format → 400。
	code, _ = doJSON(t, http.MethodGet, srv.URL+"/api/v1/dbms/eol-report?format=xls", nil, adminToken)
	if code != http.StatusBadRequest {
		t.Fatalf("非法 format 应 400，实际 %d", code)
	}
}

func TestEOLReportCSVWithBOM(t *testing.T) {
	_, srv, _, adminToken := setupScope(t)

	code, raw, header := doRaw(t, srv.URL+"/api/v1/dbms/eol-report?format=csv", adminToken)
	if code != http.StatusOK {
		t.Fatalf("CSV 导出失败（%d）: %s", code, raw)
	}
	if ct := header.Get("Content-Type"); !strings.HasPrefix(ct, "text/csv") {
		t.Fatalf("Content-Type 应为 text/csv，实际 %s", ct)
	}
	// BOM + 与 JSON 同名的表头。
	if len(raw) < 3 || raw[0] != 0xEF || raw[1] != 0xBB || raw[2] != 0xBF {
		t.Fatalf("CSV 缺少 UTF-8 BOM")
	}
	text := string(raw[3:])
	lines := strings.Split(strings.TrimRight(text, "\r\n"), "\n")
	if lines[0] != "instance_addr,component_type,version,role,cluster_name,app_name,app_owner" {
		t.Fatalf("CSV 表头错误: %q", lines[0])
	}
	if len(lines) != 3 {
		t.Fatalf("期望表头+2 行数据，实际 %d 行: %q", len(lines), text)
	}
	if !strings.Contains(lines[1], "10.1.0.1:3306,mysql,5.7.44,master,order-mysql,订单中心,张三") {
		t.Fatalf("CSV 数据行错误: %q", lines[1])
	}
}

// 数据范围（F-005）：受约束用户仅导出归属实例。
func TestEOLReportScoped(t *testing.T) {
	_, srv, tp, adminToken := setupScope(t)
	ownerToken, _ := mustScopedUser(t, srv, adminToken, "owner-s", []string{tp.appS})

	_, body := doJSON(t, http.MethodGet, srv.URL+"/api/v1/dbms/eol-report", nil, ownerToken)
	items := body["items"].([]any)
	if len(items) != 1 || items[0].(map[string]any)["instance_addr"] != "10.1.0.1:3306" {
		t.Fatalf("范围内用户仅见归属实例: %v", items)
	}
}
