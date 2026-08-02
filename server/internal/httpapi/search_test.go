// 全局搜索与 CI 关键字过滤测试：分组形状、命中说明、权限裁剪、/cis keyword。
package httpapi

import (
	"net/http"
	"testing"

	"gorm.io/datatypes"

	"meridian/server/internal/store"
)

func TestGlobalSearch(t *testing.T) {
	db, srv, token := setupRelations(t)

	host := mustCICreated(t, db, "host", "web-01")
	db.Model(&host).Update("attributes", datatypes.JSONMap{"name": "web-01", "ip": "10.0.1.11", "os": "Rocky Linux 9.4"})
	mustCICreated(t, db, "rack", "BJ-A01")
	// IPAM 数据
	prefix := store.IPPrefix{CIDR: "10.0.1.0/24", Name: "办公网段"}
	if err := db.Create(&prefix).Error; err != nil {
		t.Fatalf("创建前缀失败: %v", err)
	}
	ip := store.IPAddress{PrefixID: prefix.ID, IP: "10.0.1.11", Status: "used", Description: "web-01 内网口"}
	if err := db.Create(&ip).Error; err != nil {
		t.Fatalf("创建 IP 失败: %v", err)
	}

	// 缺 q → 400。
	if code, _ := doJSON(t, http.MethodGet, srv.URL+"/api/v1/search", nil, token); code != http.StatusBadRequest {
		t.Fatalf("缺 q 期望 400，得到 %d", code)
	}

	// "web-01"：命中模型组（host 名称不含，但 CI 组命中）、CI 组、IPAM 组（description）。
	code, body := doJSON(t, http.MethodGet, srv.URL+"/api/v1/search?q=web-01", nil, token)
	if code != http.StatusOK {
		t.Fatalf("搜索失败（%d）: %v", code, body)
	}
	groups := map[string][]any{}
	for _, g := range body["groups"].([]any) {
		gm := g.(map[string]any)
		groups[gm["kind"].(string)] = gm["items"].([]any)
	}
	ciItems, ok := groups["cis"]
	if !ok || len(ciItems) == 0 {
		t.Fatalf("CI 组应命中: %v", body)
	}
	first := ciItems[0].(map[string]any)
	if first["title"] != "web-01" || first["model_code"] != "host" {
		t.Fatalf("CI 命中项不符: %v", first)
	}
	if first["matched"] == "" {
		t.Fatalf("应给出命中说明: %v", first)
	}
	if _, ok := groups["ipam"]; !ok {
		t.Fatalf("IPAM 组应命中（description 含 web-01）: %v", body)
	}

	// "10.0.1.0/24"：IPAM 前缀命中。
	code, body = doJSON(t, http.MethodGet, srv.URL+"/api/v1/search?q=10.0.1.0/24", nil, token)
	if code != http.StatusOK {
		t.Fatalf("搜索失败（%d）", code)
	}
	found := false
	for _, g := range body["groups"].([]any) {
		gm := g.(map[string]any)
		if gm["kind"] == "ipam" {
			found = true
		}
	}
	if !found {
		t.Fatalf("CIDR 应命中 IPAM 组: %v", body)
	}

	// 无命中 → groups 为空数组。
	code, body = doJSON(t, http.MethodGet, srv.URL+"/api/v1/search?q=zzz-not-exist", nil, token)
	if code != http.StatusOK || len(body["groups"].([]any)) != 0 {
		t.Fatalf("无命中应返回空分组: %v", body)
	}
}

func TestListCIsKeyword(t *testing.T) {
	db, srv, token := setupRelations(t)
	ci := mustCICreated(t, db, "host", "db-01")
	db.Model(&ci).Update("attributes", datatypes.JSONMap{"name": "db-01", "ip": "10.0.2.20"})
	mustCICreated(t, db, "host", "web-02")

	// keyword 命中 1 条；大小写不敏感。
	code, body := doJSON(t, http.MethodGet, srv.URL+"/api/v1/cis?keyword=10.0.2.20", nil, token)
	if code != http.StatusOK {
		t.Fatalf("查询失败（%d）", code)
	}
	if body["total"].(float64) != 1 {
		t.Fatalf("keyword 应命中 1 条: %v", body)
	}
	code, body = doJSON(t, http.MethodGet, srv.URL+"/api/v1/cis?keyword=DB-01", nil, token)
	if code != http.StatusOK || body["total"].(float64) != 1 {
		t.Fatalf("大小写不敏感命中失败: %v", body)
	}
	// LIKE 通配符按字面量处理，不应放大结果。
	code, body = doJSON(t, http.MethodGet, srv.URL+"/api/v1/cis?keyword=%25", nil, token)
	if code != http.StatusOK || body["total"].(float64) != 0 {
		t.Fatalf("%% 应被转义: %v", body)
	}
}
