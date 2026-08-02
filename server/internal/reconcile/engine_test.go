// 调和引擎单测：create/update/conflict 三分支 + pool 兜底 + dryRun 预览 + 来源优先级。
package reconcile

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/datatypes"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"meridian/server/internal/store"
)

// setup 打开独立内存库，预置带调和键 ["ident","ip"] 的主机模型。
func setup(t *testing.T) (*gorm.DB, *Engine, store.Model) {
	t.Helper()
	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared", t.Name())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("打开内存库失败: %v", err)
	}
	if err := db.AutoMigrate(store.AllModels()...); err != nil {
		t.Fatalf("迁移失败: %v", err)
	}
	model := store.Model{
		Name: "主机", Code: "host",
		Attributes: datatypes.NewJSONType([]store.AttributeDefinition{
			{Name: "主机标识", Code: "ident", Type: "string"},
			{Name: "内网IP", Code: "ip", Type: "ip"},
			{Name: "操作系统", Code: "os", Type: "string"},
			{Name: "CPU核数", Code: "cpu_num", Type: "number"},
		}),
		ReconcileKeys: datatypes.NewJSONType([]string{"ident", "ip"}),
	}
	if err := db.Create(&model).Error; err != nil {
		t.Fatalf("创建模型失败: %v", err)
	}
	return db, NewEngine(db), model
}

// hostRecord 构造一条 n9e 主机发现记录。
func hostRecord(ident, ip, os string) Record {
	return Record{
		Source:         "n9e",
		Collector:      "n9e-target-puller",
		ModelCandidate: "host",
		Attributes:     map[string]any{"ident": ident, "ip": ip, "os": os, "cpu_num": 8},
		OccurredAt:     time.Now(),
	}
}

// countCIs 统计模型下的 CI 数量。
func countCIs(t *testing.T, db *gorm.DB, modelID string) int64 {
	t.Helper()
	var n int64
	if err := db.Model(&store.CI{}).Where("model_id = ?", modelID).Count(&n).Error; err != nil {
		t.Fatalf("统计 CI 失败: %v", err)
	}
	return n
}

// countPool 统计发现池待裁决条目数量。
func countPool(t *testing.T, db *gorm.DB) int64 {
	t.Helper()
	var n int64
	if err := db.Model(&store.PoolItem{}).Where("status = ?", "pending").Count(&n).Error; err != nil {
		t.Fatalf("统计发现池失败: %v", err)
	}
	return n
}

// countAudits 统计某 CI 的审计条数。
func countAudits(t *testing.T, db *gorm.DB, ciID string) int64 {
	t.Helper()
	var n int64
	if err := db.Model(&store.AuditLog{}).Where("ci_id = ?", ciID).Count(&n).Error; err != nil {
		t.Fatalf("统计审计失败: %v", err)
	}
	return n
}

func TestCreateBranch(t *testing.T) {
	db, engine, model := setup(t)
	d, err := engine.Evaluate(context.Background(), hostRecord("web-01", "10.0.0.1", "Rocky Linux 9"), false)
	if err != nil {
		t.Fatalf("调和失败: %v", err)
	}
	if d.Action != ActionCreate {
		t.Fatalf("期望 create，实际 %s（%v）", d.Action, d.Reasons)
	}
	// 落库验证：CI 建档 status=discovered，记一条 create 审计。
	var ci store.CI
	if err := db.First(&ci, "model_id = ?", model.ID).Error; err != nil {
		t.Fatalf("CI 未建档: %v", err)
	}
	if ci.Status != "discovered" || ci.Source != "n9e" {
		t.Fatalf("CI 状态/来源不符: status=%s source=%s", ci.Status, ci.Source)
	}
	if ci.Attributes["ident"] != "web-01" {
		t.Fatalf("CI 属性不符: %v", ci.Attributes)
	}
	if got := countAudits(t, db, ci.ID); got != 1 {
		t.Fatalf("期望 1 条 create 审计，实际 %d", got)
	}
}

func TestUpdateBranch(t *testing.T) {
	db, engine, model := setup(t)
	if _, err := engine.Evaluate(context.Background(), hostRecord("web-01", "10.0.0.1", "Rocky Linux 9"), false); err != nil {
		t.Fatalf("首次调和失败: %v", err)
	}
	// 同 ident 上报：os 不变、ip 变更、cpu_num 变更 → update。
	rec := hostRecord("web-01", "10.0.0.9", "Rocky Linux 9")
	rec.Attributes["cpu_num"] = 16
	d, err := engine.Evaluate(context.Background(), rec, false)
	if err != nil {
		t.Fatalf("二次调和失败: %v", err)
	}
	if d.Action != ActionUpdate {
		t.Fatalf("期望 update，实际 %s（%v）", d.Action, d.Reasons)
	}
	if d.MatchedCIID == "" {
		t.Fatal("update 未给出 matched_ci_id")
	}
	if _, ok := d.Changes["ip"]; !ok {
		t.Fatalf("期望 ip 变更入审计明细，实际: %v", d.Changes)
	}
	// CI 数仍为 1，属性已更新，审计新增一条 update。
	if got := countCIs(t, db, model.ID); got != 1 {
		t.Fatalf("更新不应新增 CI，实际共 %d 条", got)
	}
	var ci store.CI
	db.First(&ci, "id = ?", d.MatchedCIID)
	if ci.Attributes["ip"] != "10.0.0.9" {
		t.Fatalf("ip 未更新: %v", ci.Attributes["ip"])
	}
	if got := countAudits(t, db, ci.ID); got != 2 {
		t.Fatalf("期望 2 条审计（create+update），实际 %d", got)
	}

	// 完全相同记录再报 → update 但无字段变更、不新增审计。
	d2, err := engine.Evaluate(context.Background(), rec, false)
	if err != nil {
		t.Fatalf("三次调和失败: %v", err)
	}
	if d2.Action != ActionUpdate || len(d2.Changes) != 0 {
		t.Fatalf("期望无变更 update，实际 %s changes=%v", d2.Action, d2.Changes)
	}
	if got := countAudits(t, db, ci.ID); got != 2 {
		t.Fatalf("无变更不应新增审计，实际 %d", got)
	}
}

func TestConflictBranch(t *testing.T) {
	db, engine, model := setup(t)
	if _, err := engine.Evaluate(context.Background(), hostRecord("web-01", "10.0.0.1", "Rocky Linux 9"), false); err != nil {
		t.Fatalf("首次调和失败: %v", err)
	}
	var existing store.CI
	db.First(&existing, "model_id = ?", model.ID)

	// 同 IP 不同 ident → 键冲突，判定 conflict 入池，不产生新 CI、不改存量。
	d, err := engine.Evaluate(context.Background(), hostRecord("web-02", "10.0.0.1", "Rocky Linux 9"), false)
	if err != nil {
		t.Fatalf("调和失败: %v", err)
	}
	if d.Action != ActionConflict {
		t.Fatalf("期望 conflict，实际 %s（%v）", d.Action, d.Reasons)
	}
	if d.MatchedCIID != existing.ID {
		t.Fatalf("conflict 应命中存量 CI %s，实际 %s", existing.ID, d.MatchedCIID)
	}
	if got := countCIs(t, db, model.ID); got != 1 {
		t.Fatalf("冲突不应新增 CI，实际共 %d 条", got)
	}
	if got := countPool(t, db); got != 1 {
		t.Fatalf("冲突应入发现池，实际 %d 条", got)
	}
}

func TestPoolBranches(t *testing.T) {
	db, engine, _ := setup(t)

	// 候选模型不存在 → pool。
	d, err := engine.Evaluate(context.Background(), Record{
		Source: "n9e", Collector: "c", ModelCandidate: "nonexistent",
		Attributes: map[string]any{"ident": "x"}, OccurredAt: time.Now(),
	}, false)
	if err != nil || d.Action != ActionPool {
		t.Fatalf("未知模型期望 pool，实际 %s err=%v", d.Action, err)
	}

	// 模型未配置调和键 → pool。
	noKeys := store.Model{Name: "孤儿模型", Code: "orphan"}
	if err := db.Create(&noKeys).Error; err != nil {
		t.Fatalf("创建无键模型失败: %v", err)
	}
	d2, err := engine.Evaluate(context.Background(), Record{
		Source: "n9e", Collector: "c", ModelCandidate: "orphan",
		Attributes: map[string]any{"ident": "x"}, OccurredAt: time.Now(),
	}, false)
	if err != nil || d2.Action != ActionPool {
		t.Fatalf("无调和键模型期望 pool，实际 %s err=%v", d2.Action, err)
	}
	if got := countPool(t, db); got != 2 {
		t.Fatalf("两次 pool 判定应各入池一条，实际 %d 条", got)
	}
}

func TestPreviewDryRun(t *testing.T) {
	db, engine, model := setup(t)
	if _, err := engine.Evaluate(context.Background(), hostRecord("web-01", "10.0.0.1", "Rocky Linux 9"), false); err != nil {
		t.Fatalf("首次调和失败: %v", err)
	}
	// dryRun 预览一条会触发 update 的记录：判定给出，但库无任何变化。
	d, err := engine.Evaluate(context.Background(), hostRecord("web-01", "10.0.0.9", "Rocky Linux 9"), true)
	if err != nil {
		t.Fatalf("预览失败: %v", err)
	}
	if d.Action != ActionUpdate {
		t.Fatalf("期望预览 update，实际 %s", d.Action)
	}
	var ci store.CI
	db.First(&ci, "model_id = ?", model.ID)
	if ci.Attributes["ip"] != "10.0.0.1" {
		t.Fatalf("dryRun 不应修改属性，实际 ip=%v", ci.Attributes["ip"])
	}
	if got := countAudits(t, db, ci.ID); got != 1 {
		t.Fatalf("dryRun 不应新增审计，实际 %d", got)
	}

	// dryRun 预览冲突：不入池。
	d2, err := engine.Evaluate(context.Background(), hostRecord("web-02", "10.0.0.1", "Rocky Linux 9"), true)
	if err != nil || d2.Action != ActionConflict {
		t.Fatalf("期望预览 conflict，实际 %s err=%v", d2.Action, err)
	}
	if got := countPool(t, db); got != 0 {
		t.Fatalf("dryRun 不应入池，实际 %d 条", got)
	}
}

func TestSourcePriorityMerge(t *testing.T) {
	// 字段由 manual（100）维护时，n9e（80）上报不覆盖；manual 上报可覆盖。
	ci := &store.CI{
		Source:       "manual",
		Attributes:   datatypes.JSONMap{"os": "Rocky Linux 9", "ip": "10.0.0.1"},
		FieldSources: datatypes.JSONMap{"os": "manual", "ip": "n9e"},
	}
	changes, skipped := PlanMerge(ci, map[string]any{"os": "Ubuntu 24.04", "ip": "10.0.0.2"}, "n9e")
	if len(changes) != 1 {
		t.Fatalf("期望仅 ip 可变更，实际 changes=%v", changes)
	}
	if _, ok := changes["ip"]; !ok {
		t.Fatalf("ip 由 n9e 维护，n9e 可更新: %v", changes)
	}
	if len(skipped) != 1 || skipped[0] != "os" {
		t.Fatalf("os 由 manual 维护应被跳过，实际 skipped=%v", skipped)
	}

	changes2, skipped2 := PlanMerge(ci, map[string]any{"os": "Ubuntu 24.04"}, "manual")
	if len(changes2) != 1 || len(skipped2) != 0 {
		t.Fatalf("manual 应可覆盖 manual 字段，changes=%v skipped=%v", changes2, skipped2)
	}
}
