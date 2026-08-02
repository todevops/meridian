// 告警事件 API 测试：列表过滤/分页、ack 确认、权限点（viewer 可读不可写）。
package httpapi

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"meridian/server/internal/auth"
	"meridian/server/internal/store"
)

// setupAlerts 构建测试环境并预置两条告警（一条已确认）。
func setupAlerts(t *testing.T) (*gorm.DB, *httptest.Server, string) {
	t.Helper()
	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared", t.Name())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("打开内存库失败: %v", err)
	}
	if err := db.AutoMigrate(store.AllModels()...); err != nil {
		t.Fatalf("迁移失败: %v", err)
	}
	authSvc, err := auth.NewService(db, "test-secret", 1)
	if err != nil {
		t.Fatalf("创建认证服务失败: %v", err)
	}
	if err := authSvc.Seed("admin-pass", "collector-pass"); err != nil {
		t.Fatalf("种子认证数据失败: %v", err)
	}
	alerts := []store.AlertEvent{
		{Level: "warning", Title: "发现黑设备风险主机", Source: "n9e", CIID: "ci-1", Detail: "ident=rogue-01"},
		{Level: "info", Title: "网段扫描完成", Source: "ipscan", Detail: "10.0.0.0/24", Acknowledged: true},
	}
	for i := range alerts {
		if err := db.Create(&alerts[i]).Error; err != nil {
			t.Fatalf("预置告警失败: %v", err)
		}
	}
	gin.SetMode(gin.TestMode)
	srv := httptest.NewServer(newTestRouter(t, db, authSvc))
	t.Cleanup(srv.Close)
	return db, srv, loginAs(t, srv, "admin", "admin-pass")
}

func TestAlertListFilterAndPagination(t *testing.T) {
	_, srv, token := setupAlerts(t)

	// 全量：2 条，最新在前（第二条预置的后创建）。
	code, body := doJSON(t, http.MethodGet, srv.URL+"/api/v1/alerts", nil, token)
	if code != http.StatusOK {
		t.Fatalf("列表期望 200，得到 %d: %v", code, body)
	}
	if body["total"].(float64) != 2 {
		t.Fatalf("期望 total=2，实际 %v", body["total"])
	}

	// acknowledged=false 过滤：仅黑设备告警。
	code, body = doJSON(t, http.MethodGet, srv.URL+"/api/v1/alerts?acknowledged=false", nil, token)
	if code != http.StatusOK {
		t.Fatalf("过滤列表期望 200，得到 %d: %v", code, body)
	}
	if body["total"].(float64) != 1 {
		t.Fatalf("期望 total=1，实际 %v", body["total"])
	}
	item := body["items"].([]any)[0].(map[string]any)
	if item["title"] != "发现黑设备风险主机" || item["level"] != "warning" || item["acknowledged"] != false {
		t.Fatalf("告警内容不符: %v", item)
	}

	// acknowledged=true 过滤：仅已确认。
	_, body = doJSON(t, http.MethodGet, srv.URL+"/api/v1/alerts?acknowledged=true", nil, token)
	if body["total"].(float64) != 1 {
		t.Fatalf("期望 total=1，实际 %v", body["total"])
	}

	// 非法过滤值 400。
	if code, _ := doJSON(t, http.MethodGet, srv.URL+"/api/v1/alerts?acknowledged=maybe", nil, token); code != http.StatusBadRequest {
		t.Fatalf("非法过滤值期望 400，得到 %d", code)
	}
}

func TestAlertAck(t *testing.T) {
	db, srv, token := setupAlerts(t)
	var alert store.AlertEvent
	if err := db.First(&alert, "acknowledged = ?", false).Error; err != nil {
		t.Fatalf("加载告警失败: %v", err)
	}

	// ack 成功，返回更新后的告警。
	code, body := doJSON(t, http.MethodPost, srv.URL+"/api/v1/alerts/"+alert.ID+"/ack", nil, token)
	if code != http.StatusOK {
		t.Fatalf("ack 期望 200，得到 %d: %v", code, body)
	}
	if body["acknowledged"] != true {
		t.Fatalf("ack 后 acknowledged 应为 true: %v", body)
	}
	// 重复 ack 幂等。
	if code, _ := doJSON(t, http.MethodPost, srv.URL+"/api/v1/alerts/"+alert.ID+"/ack", nil, token); code != http.StatusOK {
		t.Fatalf("重复 ack 期望 200，得到 %d", code)
	}
	// 不存在的告警 404。
	if code, _ := doJSON(t, http.MethodPost, srv.URL+"/api/v1/alerts/nonexistent/ack", nil, token); code != http.StatusNotFound {
		t.Fatalf("不存在告警 ack 期望 404，得到 %d", code)
	}
}

func TestAlertPermissions(t *testing.T) {
	db, srv, adminToken := setupAlerts(t)
	// admin 创建 viewer 用户。
	code, _ := doJSON(t, http.MethodPost, srv.URL+"/api/v1/users",
		map[string]any{"username": "v-alert", "display_name": "只读", "password": "pass123", "roles": []string{"viewer"}}, adminToken)
	if code != http.StatusOK && code != http.StatusCreated {
		t.Fatalf("创建 viewer 用户失败: %d", code)
	}
	viewerToken := loginAs(t, srv, "v-alert", "pass123")

	// viewer 可读告警列表。
	if code, _ := doJSON(t, http.MethodGet, srv.URL+"/api/v1/alerts", nil, viewerToken); code != http.StatusOK {
		t.Fatalf("viewer 读告警期望 200，得到 %d", code)
	}
	// viewer 不可 ack（无 alert:write）。
	var alert store.AlertEvent
	if err := db.First(&alert).Error; err != nil {
		t.Fatalf("加载告警失败: %v", err)
	}
	if code, _ := doJSON(t, http.MethodPost, srv.URL+"/api/v1/alerts/"+alert.ID+"/ack", nil, viewerToken); code != http.StatusForbidden {
		t.Fatalf("viewer ack 期望 403，得到 %d", code)
	}
	// 未认证 401。
	if code, _ := doJSON(t, http.MethodGet, srv.URL+"/api/v1/alerts", nil, ""); code != http.StatusUnauthorized {
		t.Fatalf("未认证期望 401，得到 %d", code)
	}
}
