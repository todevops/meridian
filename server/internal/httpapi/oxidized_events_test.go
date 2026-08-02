// Oxidized 事件回写 API 测试（F-062）：共享密钥鉴权、backup 幂等、change 告警、匹配失败。
package httpapi

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"gorm.io/datatypes"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"meridian/server/internal/auth"
	"meridian/server/internal/store"
)

// setupOxidizedEvents 构建含 network_device 模型与一台设备的测试环境。
func setupOxidizedEvents(t *testing.T) (*gorm.DB, *httptest.Server, store.CI) {
	t.Helper()
	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared", t.Name())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("打开内存库失败: %v", err)
	}
	if err := db.AutoMigrate(store.AllModels()...); err != nil {
		t.Fatalf("迁移失败: %v", err)
	}
	model := store.Model{Name: "网络设备", Code: "network_device"}
	if err := db.Create(&model).Error; err != nil {
		t.Fatalf("创建模型失败: %v", err)
	}
	ci := store.CI{
		ModelID:    model.ID,
		Attributes: datatypes.JSONMap{"name": "bj-core-sw-01", "mgmt_ip": "10.30.0.1"},
		Status:     "active",
		Source:     "snmp",
	}
	if err := db.Create(&ci).Error; err != nil {
		t.Fatalf("创建设备 CI 失败: %v", err)
	}
	authSvc, err := auth.NewService(db, "test-secret", 1)
	if err != nil {
		t.Fatalf("创建认证服务失败: %v", err)
	}
	if err := authSvc.Seed("admin-pass", "collector-pass"); err != nil {
		t.Fatalf("种子认证数据失败: %v", err)
	}
	gin.SetMode(gin.TestMode)
	srv := httptest.NewServer(newTestRouter(t, db, authSvc))
	t.Cleanup(srv.Close)
	return db, srv, ci
}

// postOxidizedEvent 以指定共享密钥头发送 Oxidized 事件。
func postOxidizedEvent(t *testing.T, srv *httptest.Server, token string, body map[string]any) (int, map[string]any) {
	t.Helper()
	raw, _ := json.Marshal(body)
	req, err := http.NewRequest(http.MethodPost, srv.URL+"/api/v1/integrations/oxidized/events", bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("构造请求失败: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("X-Oxidized-Token", token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("请求失败: %v", err)
	}
	defer resp.Body.Close()
	out := map[string]any{}
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

func TestOxidizedEventTokenGuard(t *testing.T) {
	_, srv, _ := setupOxidizedEvents(t)
	body := map[string]any{"node": "bj-core-sw-01", "event": "backup", "time": "2026-08-01T10:00:00Z"}
	// 无头与错误密钥均 401。
	if code, _ := postOxidizedEvent(t, srv, "", body); code != http.StatusUnauthorized {
		t.Fatalf("无密钥头期望 401，得到 %d", code)
	}
	if code, _ := postOxidizedEvent(t, srv, "wrong-token", body); code != http.StatusUnauthorized {
		t.Fatalf("错误密钥期望 401，得到 %d", code)
	}
}

func TestOxidizedBackupEventAndIdempotency(t *testing.T) {
	db, srv, ci := setupOxidizedEvents(t)
	body := map[string]any{"node": "bj-core-sw-01", "event": "backup", "time": "2026-08-01T10:00:00Z"}

	code, resp := postOxidizedEvent(t, srv, "dev-oxidized-token", body)
	if code != http.StatusOK || resp["ci_id"] != ci.ID || resp["idempotent"] != false {
		t.Fatalf("backup 期望 200 非幂等，得到 %d: %v", code, resp)
	}
	var got store.CI
	if err := db.First(&got, "id = ?", ci.ID).Error; err != nil {
		t.Fatalf("重查 CI 失败: %v", err)
	}
	if got.Attributes["last_backup_at"] != "2026-08-01T10:00:00Z" ||
		fmt.Sprint(got.Attributes["backup_count"]) != "1" ||
		got.Attributes["config_source"] != "oxidized" {
		t.Fatalf("备份元数据不符: %v (backup_count 类型 %T)", got.Attributes, got.Attributes["backup_count"])
	}
	if got.FieldSources["last_backup_at"] != "oxidized" {
		t.Fatalf("field_sources 应记 oxidized: %v", got.FieldSources)
	}
	// 审计留痕。
	var audits int64
	if err := db.Model(&store.AuditLog{}).Where("ci_id = ? AND source = ?", ci.ID, "oxidized").Count(&audits).Error; err != nil {
		t.Fatalf("统计审计失败: %v", err)
	}
	if audits != 1 {
		t.Fatalf("期望 1 条审计，实际 %d", audits)
	}

	// 同时间戳重复推送：幂等，不累加 backup_count。
	code, resp = postOxidizedEvent(t, srv, "dev-oxidized-token", body)
	if code != http.StatusOK || resp["idempotent"] != true {
		t.Fatalf("重复 backup 期望幂等，得到 %d: %v", code, resp)
	}
	if err := db.First(&got, "id = ?", ci.ID).Error; err != nil {
		t.Fatalf("重查 CI 失败: %v", err)
	}
	if fmt.Sprint(got.Attributes["backup_count"]) != "1" {
		t.Fatalf("幂等重推不应累加计数: %v", got.Attributes["backup_count"])
	}

	// 新时间戳：计数自增到 2。
	body["time"] = "2026-08-01T11:00:00Z"
	if code, _ := postOxidizedEvent(t, srv, "dev-oxidized-token", body); code != http.StatusOK {
		t.Fatalf("第二次 backup 期望 200，得到 %d", code)
	}
	if err := db.First(&got, "id = ?", ci.ID).Error; err != nil {
		t.Fatalf("重查 CI 失败: %v", err)
	}
	if fmt.Sprint(got.Attributes["backup_count"]) != "2" {
		t.Fatalf("backup_count 应为 2: %v", got.Attributes["backup_count"])
	}
}

func TestOxidizedChangeEvent(t *testing.T) {
	db, srv, ci := setupOxidizedEvents(t)
	body := map[string]any{"node": "bj-core-sw-01", "event": "change", "time": "2026-08-01T12:00:00Z", "user": "ops-li"}
	code, _ := postOxidizedEvent(t, srv, "dev-oxidized-token", body)
	if code != http.StatusOK {
		t.Fatalf("change 期望 200，得到 %d", code)
	}
	var got store.CI
	if err := db.First(&got, "id = ?", ci.ID).Error; err != nil {
		t.Fatalf("重查 CI 失败: %v", err)
	}
	if got.Attributes["last_change_at"] != "2026-08-01T12:00:00Z" {
		t.Fatalf("last_change_at 不符: %v", got.Attributes)
	}
	var alert store.AlertEvent
	if err := db.First(&alert, "ci_id = ? AND title = ?", ci.ID, "配置变更").Error; err != nil {
		t.Fatalf("应写入配置变更告警: %v", err)
	}
	if alert.Level != store.AlertLevelInfo || alert.Source != "oxidized" {
		t.Fatalf("告警内容不符: %+v", alert)
	}
}

func TestOxidizedEventBadRequests(t *testing.T) {
	_, srv, _ := setupOxidizedEvents(t)
	// 未匹配设备 404。
	code, _ := postOxidizedEvent(t, srv, "dev-oxidized-token",
		map[string]any{"node": "no-such-device", "event": "backup", "time": "2026-08-01T10:00:00Z"})
	if code != http.StatusNotFound {
		t.Fatalf("未匹配设备期望 404，得到 %d", code)
	}
	// 非法 event 400。
	code, _ = postOxidizedEvent(t, srv, "dev-oxidized-token",
		map[string]any{"node": "bj-core-sw-01", "event": "explode", "time": "2026-08-01T10:00:00Z"})
	if code != http.StatusBadRequest {
		t.Fatalf("非法 event 期望 400，得到 %d", code)
	}
	// 非法时间 400。
	code, _ = postOxidizedEvent(t, srv, "dev-oxidized-token",
		map[string]any{"node": "bj-core-sw-01", "event": "backup", "time": "昨天"})
	if code != http.StatusBadRequest {
		t.Fatalf("非法时间期望 400，得到 %d", code)
	}
	// 空 node 400。
	code, _ = postOxidizedEvent(t, srv, "dev-oxidized-token",
		map[string]any{"node": "", "event": "backup", "time": "2026-08-01T10:00:00Z"})
	if code != http.StatusBadRequest {
		t.Fatalf("空 node 期望 400，得到 %d", code)
	}
}
