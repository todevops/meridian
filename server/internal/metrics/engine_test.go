// 质量指标引擎（F-080）单元测试：五指标计算与下钻清单。
package metrics

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

// mkModel 快速建模型。
func mkModel(t *testing.T, db *gorm.DB, code string, attrs []store.AttributeDefinition) store.Model {
	t.Helper()
	m := store.Model{Code: code, Name: code, Attributes: datatypes.NewJSONType(attrs)}
	if err := db.Create(&m).Error; err != nil {
		t.Fatalf("创建模型失败: %v", err)
	}
	return m
}

// mkCI 快速建 CI（可指定 updated_at）。
func mkCI(t *testing.T, db *gorm.DB, modelID string, attrs map[string]any, updatedAt time.Time) store.CI {
	t.Helper()
	ci := store.CI{ModelID: modelID, Attributes: datatypes.JSONMap(attrs), Status: "active", Source: "manual"}
	if err := db.Create(&ci).Error; err != nil {
		t.Fatalf("创建 CI 失败: %v", err)
	}
	if err := db.Model(&store.CI{}).Where("id = ?", ci.ID).Update("updated_at", updatedAt).Error; err != nil {
		t.Fatalf("改写 updated_at 失败: %v", err)
	}
	ci.UpdatedAt = updatedAt
	return ci
}

// fixture 构建两台主机（一台健康一台多缺失）+ 一个应用 + 一条归属关系。
func fixture(t *testing.T, db *gorm.DB) (hostModel, appModel store.Model, good, bad store.CI) {
	t.Helper()
	now := time.Now()
	hostModel = mkModel(t, db, "host", []store.AttributeDefinition{
		{Code: "ident", Type: "string", Required: true},
		{Code: "ip", Type: "ip", Required: true},
	})
	appModel = mkModel(t, db, "biz_app", []store.AttributeDefinition{
		{Code: "code", Type: "string", Required: true},
	})
	app := mkCI(t, db, appModel.ID, map[string]any{"code": "mall"}, now)

	good = mkCI(t, db, hostModel.ID, map[string]any{
		"ident": "h-good", "ip": "10.0.0.1",
		"last_heartbeat_at": now.Add(-5 * time.Minute).Format(time.RFC3339),
	}, now)
	bad = mkCI(t, db, hostModel.ID, map[string]any{
		"ident":             "h-bad",                                  // ip 缺失（属性不完整）
		"last_heartbeat_at": now.Add(-time.Hour).Format(time.RFC3339), // 超 10 分钟无心跳
	}, now.Add(-10*24*time.Hour)) // 超 7 天未更新

	if err := db.Create(&store.CIRelation{RelationCode: "deployed_on", SrcCIID: app.ID, DstCIID: good.ID}).Error; err != nil {
		t.Fatalf("建关系失败: %v", err)
	}
	return hostModel, appModel, good, bad
}

func TestQuality(t *testing.T) {
	db := newTestDB(t)
	hostModel, _, _, _ := fixture(t, db)

	report, err := NewEngine(db, nil).Quality(context.Background())
	if err != nil {
		t.Fatalf("计算质量指标失败: %v", err)
	}
	var host *ModelQuality
	for i := range report.Models {
		if report.Models[i].ModelID == hostModel.ID {
			host = &report.Models[i]
		}
	}
	if host == nil {
		t.Fatalf("汇总缺少 host 模型: %+v", report.Models)
	}
	// 属性完整率：4 个必填单元格缺 1 个 → 75%。
	if host.Completeness != 75 {
		t.Errorf("属性完整率应为 75，实际 %v", host.Completeness)
	}
	// 关联完整率（host 特化=业务归属）：2 台中 1 台归属 → 50%。
	if host.RelationCompleteness != 50 {
		t.Errorf("关联完整率应为 50，实际 %v", host.RelationCompleteness)
	}
	// 孤岛：h-bad 无任何关系 → 1。
	if host.OrphanCount != 1 {
		t.Errorf("孤岛 CI 应为 1，实际 %v", host.OrphanCount)
	}
	// 数据鲜度：2 台中 1 台超 7 天 → 50%。
	if host.StalePct != 50 {
		t.Errorf("鲜度超期占比应为 50，实际 %v", host.StalePct)
	}
	// 无心跳：2 台中 1 台超 10 分钟 → 50%，且并入 monitor。
	if host.NoHeartbeatPct == nil || *host.NoHeartbeatPct != 50 {
		t.Errorf("无心跳占比应为 50，实际 %+v", host.NoHeartbeatPct)
	}
	if report.Monitor.NoHeartbeatPct != 50 {
		t.Errorf("monitor.no_heartbeat_pct 应为 50，实际 %v", report.Monitor.NoHeartbeatPct)
	}
	// n9e 未配置：no_ci_pct 缺省。
	if report.Monitor.NoCIPct != nil {
		t.Errorf("n9e 未配置时 no_ci_pct 应缺省，实际 %v", *report.Monitor.NoCIPct)
	}
}

// TestQualityMonitorReverse 验证反向指标：n9e targets 无 CMDB CI 占比。
func TestQualityMonitorReverse(t *testing.T) {
	db := newTestDB(t)
	_, _, good, _ := fixture(t, db)

	n9eSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 两个 target：一个 ident 命中 h-good，一个无对应 CI。
		_ = json.NewEncoder(w).Encode(map[string]any{
			"dat": map[string]any{"list": []map[string]any{
				{"id": 1, "ident": "h-good", "host_ip": "10.0.0.1"},
				{"id": 2, "ident": "ghost", "host_ip": "10.0.9.9"},
			}, "total": 2},
			"err": "",
		})
	}))
	t.Cleanup(n9eSrv.Close)
	_ = good

	report, err := NewEngine(db, n9e.NewClient(n9eSrv.URL, "tok")).Quality(context.Background())
	if err != nil {
		t.Fatalf("计算质量指标失败: %v", err)
	}
	if report.Monitor.NoCIPct == nil || *report.Monitor.NoCIPct != 50 {
		t.Fatalf("no_ci_pct 应为 50，实际 %+v", report.Monitor.NoCIPct)
	}
}

// TestDrilldown 验证各指标下钻清单与分页。
func TestDrilldown(t *testing.T) {
	db := newTestDB(t)
	hostModel, _, good, bad := fixture(t, db)
	eng := NewEngine(db, nil)
	ctx := context.Background()

	cases := []struct {
		metric string
		wantID string
	}{
		{DrillCompleteness, bad.ID},         // 缺 ip
		{DrillRelationCompleteness, bad.ID}, // host 未归属
		{DrillOrphan, bad.ID},               // 无任何关系
		{DrillStale, bad.ID},                // 超 7 天未更新
		{DrillNoHeartbeat, bad.ID},          // 超 10 分钟无心跳
	}
	for _, tc := range cases {
		items, total, err := eng.Drilldown(ctx, hostModel, tc.metric, 1, 20)
		if err != nil {
			t.Fatalf("下钻 %s 失败: %v", tc.metric, err)
		}
		if total != 1 || len(items) != 1 || items[0].ID != tc.wantID {
			t.Errorf("下钻 %s 应只命中 %s，实际 total=%d items=%v", tc.metric, tc.wantID, total, items)
		}
	}
	// 健康 CI 不应出现在任何下钻清单中。
	for _, metric := range []string{DrillCompleteness, DrillOrphan, DrillStale, DrillNoHeartbeat} {
		items, _, _ := eng.Drilldown(ctx, hostModel, metric, 1, 20)
		for _, it := range items {
			if it.ID == good.ID {
				t.Errorf("健康 CI 不应出现在下钻 %s 清单", metric)
			}
		}
	}
	// 分页：page_size=1 时第二页为空（缺失集只有 1 条）。
	items, total, err := eng.Drilldown(ctx, hostModel, DrillStale, 2, 1)
	if err != nil || total != 1 || len(items) != 0 {
		t.Errorf("分页行为不符: total=%d len=%d err=%v", total, len(items), err)
	}
}
