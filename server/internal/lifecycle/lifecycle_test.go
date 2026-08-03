// 生命周期状态机与退役联动（F-026）单元测试。
package lifecycle

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/datatypes"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"meridian/server/internal/jumpserver"
	"meridian/server/internal/n9e"
	"meridian/server/internal/store"
)

// newTestDB 打开独立内存库并迁移全部实体。
func newTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared", t.Name())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("打开内存库失败: %v", err)
	}
	if err := db.AutoMigrate(store.AllModels()...); err != nil {
		t.Fatalf("迁移失败: %v", err)
	}
	return db
}

// mkHostCI 建 host 模型与一台 CI。
func mkHostCI(t *testing.T, db *gorm.DB, attrs map[string]any, updatedAt time.Time) store.CI {
	t.Helper()
	var model store.Model
	if err := db.Where("code = ?", "host").First(&model).Error; err != nil {
		model = store.Model{Code: "host", Name: "主机",
			Attributes: datatypes.NewJSONType([]store.AttributeDefinition{{Code: "ident", Type: "string"}})}
		if err := db.Create(&model).Error; err != nil {
			t.Fatalf("创建 host 模型失败: %v", err)
		}
	}
	ci := store.CI{ModelID: model.ID, Attributes: datatypes.JSONMap(attrs), Status: StatusActive, Source: "manual"}
	if err := db.Create(&ci).Error; err != nil {
		t.Fatalf("创建 CI 失败: %v", err)
	}
	db.Model(&store.CI{}).Where("id = ?", ci.ID).Update("updated_at", updatedAt)
	ci.UpdatedAt = updatedAt
	return ci
}

// addScanRecord 写一条 ip_scan 原始记录（attributes.ip）。
func addScanRecord(t *testing.T, db *gorm.DB, ip string, at time.Time) {
	t.Helper()
	rec := store.DiscoveryRawRecord{
		Source: "ip_scan", Collector: "ipscan", ModelCandidate: "host",
		Payload:    datatypes.JSONMap{"attributes": map[string]any{"ip": ip}},
		OccurredAt: at, ReceivedAt: at,
	}
	if err := db.Create(&rec).Error; err != nil {
		t.Fatalf("写扫描记录失败: %v", err)
	}
}

func TestCanTransit(t *testing.T) {
	valid := [][2]string{
		{StatusDiscovered, StatusStock},
		{StatusDiscovered, StatusActive},
		{StatusPurchase, StatusStock},
		{StatusStock, StatusActive},
		{StatusActive, StatusMaintenance},
		{StatusMaintenance, StatusActive},
		{StatusActive, StatusPendingRetire},
		{StatusMaintenance, StatusPendingRetire},
		{StatusPendingRetire, StatusActive},
		{StatusPendingRetire, StatusRetired},
	}
	for _, edge := range valid {
		if !CanTransit(edge[0], edge[1]) {
			t.Errorf("应允许 %s → %s", edge[0], edge[1])
		}
	}
	invalid := [][2]string{
		{StatusRetired, StatusActive},     // 终态不可逆
		{StatusActive, StatusRetired},     // 退役必须经 pending_retire
		{StatusStock, StatusMaintenance},  // 未在用不能维修
		{StatusDiscovered, StatusRetired}, // 发现态不能直接退役
		{StatusPurchase, StatusActive},
	}
	for _, edge := range invalid {
		if CanTransit(edge[0], edge[1]) {
			t.Errorf("不应允许 %s → %s", edge[0], edge[1])
		}
	}
}

// TestRetireCandidates 会签正/反例：全死可退役；任一方存活即回退。
func TestRetireCandidates(t *testing.T) {
	db := newTestDB(t)
	now := time.Now()

	// 正例：心跳停更 10 天、近 3 次扫描无记录、无云实例 → eligible。
	dead := mkHostCI(t, db, map[string]any{
		"ident": "dead-1", "ip": "10.0.0.1",
		"last_heartbeat_at": now.Add(-10 * 24 * time.Hour).Format(time.RFC3339),
	}, now.Add(-10*24*time.Hour))
	addScanRecord(t, db, "10.0.0.1", now.Add(-30*24*time.Hour)) // 扫描记录也久远

	// 反例一：心跳仍活 → 回退。
	aliveHB := mkHostCI(t, db, map[string]any{
		"ident": "alive-hb", "ip": "10.0.0.2",
		"last_heartbeat_at": now.Add(-time.Hour).Format(time.RFC3339),
	}, now.Add(-30*24*time.Hour))

	// 反例二：心跳死但近窗口内有扫描存活记录 → 回退。
	aliveScan := mkHostCI(t, db, map[string]any{
		"ident": "alive-scan", "ip": "10.0.0.3",
		"last_heartbeat_at": now.Add(-10 * 24 * time.Hour).Format(time.RFC3339),
	}, now.Add(-10*24*time.Hour))
	addScanRecord(t, db, "10.0.0.3", now.Add(-24*time.Hour))

	// 反例三：心跳死、扫描死但有云实例 → 回退。
	cloudy := mkHostCI(t, db, map[string]any{
		"ident": "cloudy", "ip": "10.0.0.4", "cloud_instance_id": "i-abc123",
		"last_heartbeat_at": now.Add(-10 * 24 * time.Hour).Format(time.RFC3339),
	}, now.Add(-10*24*time.Hour))

	checks, err := NewService(db, nil).RetireCandidates(context.Background())
	if err != nil {
		t.Fatalf("会签判定失败: %v", err)
	}
	byID := map[string]RetireCheck{}
	for _, c := range checks {
		byID[c.CI.ID] = c
	}
	if len(checks) != 4 {
		t.Fatalf("应判定 4 台，实际 %d", len(checks))
	}
	if c := byID[dead.ID]; !c.Eligible || c.HeartbeatOK || c.ScanOK || c.CloudOK {
		t.Errorf("死主机应 eligible: %+v", c)
	}
	if c := byID[aliveHB.ID]; c.Eligible || !c.HeartbeatOK {
		t.Errorf("心跳存活不应 eligible: %+v", c)
	}
	if c := byID[aliveScan.ID]; c.Eligible || !c.ScanOK {
		t.Errorf("扫描存活不应 eligible: %+v", c)
	}
	if c := byID[cloudy.ID]; c.Eligible || !c.CloudOK {
		t.Errorf("云实例存在不应 eligible: %+v", c)
	}
}

// TestRetireActions 验证退役联动五动作：状态/审计/告警、n9e 摘除、JumpServer 禁用、IPAM 闲置。
func TestRetireActions(t *testing.T) {
	db := newTestDB(t)
	now := time.Now()
	ci := mkHostCI(t, db, map[string]any{
		"ident": "dead-1", "ip": "10.0.0.1",
		"last_heartbeat_at": now.Add(-10 * 24 * time.Hour).Format(time.RFC3339),
	}, now.Add(-10*24*time.Hour))

	// IPAM：登记一条关联 IP。
	prefix := store.IPPrefix{CIDR: "10.0.0.0/24", Name: "办公网"}
	if err := db.Create(&prefix).Error; err != nil {
		t.Fatalf("建前缀失败: %v", err)
	}
	ipRec := store.IPAddress{PrefixID: prefix.ID, IP: "10.0.0.1", Status: "used", CIID: ci.ID}
	if err := db.Create(&ipRec).Error; err != nil {
		t.Fatalf("登记 IP 失败: %v", err)
	}

	// n9e mock：一个匹配 target，记录 DELETE ids。
	var deletedIDs []int64
	n9eSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/n9e/targets":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"dat": map[string]any{"list": []map[string]any{
					{"id": 7, "ident": "dead-1", "host_ip": "10.0.0.1"},
				}, "total": 1},
				"err": "",
			})
		case r.Method == http.MethodDelete && r.URL.Path == "/api/n9e/targets":
			var body struct {
				IDs []int64 `json:"ids"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			deletedIDs = body.IDs
			_ = json.NewEncoder(w).Encode(map[string]any{"dat": nil, "err": ""})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(n9eSrv.Close)

	// JumpServer mock：按 IP 查到资产，PATCH 禁用。
	jumpserverDisabled := false
	jsSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/assets/assets/":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"count": 1, "results": []map[string]any{{"id": "asset-1", "address": "10.0.0.1", "is_active": true}},
			})
		case r.Method == http.MethodPatch && r.URL.Path == "/api/v1/assets/assets/asset-1/":
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body["is_active"] == false {
				jumpserverDisabled = true
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "asset-1", "is_active": false})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(jsSrv.Close)

	svc := NewService(db, n9e.NewClient(n9eSrv.URL, "tok"))
	actions, err := svc.Retire(context.Background(), ci, "admin", jumpserver.NewClient(jsSrv.URL, "js-token"))
	if err != nil {
		t.Fatalf("退役执行失败: %v", err)
	}
	byType := map[string]RetireAction{}
	for _, a := range actions {
		byType[a.Type] = a
		if !a.OK {
			t.Errorf("动作 %s 未成功: %s", a.Type, a.Detail)
		}
	}
	for _, want := range []string{"status", "n9e_remove_target", "jumpserver_disable", "ipam_idle", "alert_event"} {
		if _, ok := byType[want]; !ok {
			t.Errorf("缺少动作 %s", want)
		}
	}

	// 状态已 retired。
	var after store.CI
	if err := db.First(&after, "id = ?", ci.ID).Error; err != nil {
		t.Fatalf("重查 CI 失败: %v", err)
	}
	if after.Status != StatusRetired {
		t.Errorf("状态应为 retired，实际 %s", after.Status)
	}
	// n9e 摘除调用命中。
	if len(deletedIDs) != 1 || deletedIDs[0] != 7 {
		t.Errorf("n9e 摘除应删 target 7，实际 %v", deletedIDs)
	}
	// JumpServer 禁用调用命中。
	if !jumpserverDisabled {
		t.Errorf("JumpServer 资产未被禁用")
	}
	// IPAM 置闲置。
	var ipAfter store.IPAddress
	if err := db.First(&ipAfter, "id = ?", ipRec.ID).Error; err != nil {
		t.Fatalf("重查 IP 失败: %v", err)
	}
	if ipAfter.Status != "idle" {
		t.Errorf("IPAM 状态应为 idle，实际 %s", ipAfter.Status)
	}
	// 审计与告警留痕。
	var audit store.AuditLog
	if err := db.Where("ci_id = ? AND action = ?", ci.ID, "retire").First(&audit).Error; err != nil {
		t.Errorf("缺少退役审计记录: %v", err)
	} else if audit.Operator != "admin" || audit.Source != "lifecycle" {
		t.Errorf("审计记录字段不符: %+v", audit)
	}
	var alert store.AlertEvent
	if err := db.Where("ci_id = ? AND source = ?", ci.ID, "lifecycle").First(&alert).Error; err != nil {
		t.Errorf("缺少退役告警事件: %v", err)
	}
}

// TestRetireWithoutIntegrations 验证未配置集成时动作报告跳过但不中断。
func TestRetireWithoutIntegrations(t *testing.T) {
	db := newTestDB(t)
	ci := mkHostCI(t, db, map[string]any{"ident": "h1", "ip": "10.0.0.1"}, time.Now())

	actions, err := NewService(db, nil).Retire(context.Background(), ci, "admin", nil)
	if err != nil {
		t.Fatalf("退役执行失败: %v", err)
	}
	byType := map[string]RetireAction{}
	for _, a := range actions {
		byType[a.Type] = a
	}
	if !byType["status"].OK {
		t.Errorf("状态动作必须成功: %+v", byType["status"])
	}
	if byType["n9e_remove_target"].OK || byType["jumpserver_disable"].OK {
		t.Errorf("未配置集成的动作应报告失败并说明跳过: %+v", actions)
	}

	// 已 retired 的 CI 再次退役应报错。
	var after store.CI
	db.First(&after, "id = ?", ci.ID)
	if _, err := NewService(db, nil).Retire(context.Background(), after, "admin", nil); err == nil {
		t.Errorf("重复退役应报错")
	}
}
