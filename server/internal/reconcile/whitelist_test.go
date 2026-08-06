// 自动入库白名单（F-034）单测：命中/不命中/dry_run/禁用/update 分支不受影响。
package reconcile

import (
	"context"
	"testing"
	"time"

	"gorm.io/datatypes"
	"gorm.io/gorm"

	"meridian/server/internal/store"
)

// mustRule 写入一条规则（默认 enabled、非 dry_run）。
func mustRule(t *testing.T, db *gorm.DB, ruleType, assertion string, filter map[string]any) store.AuditRule {
	t.Helper()
	rule := store.AuditRule{
		Name:      "测试规则-" + ruleType,
		ModelCode: "host",
		Type:      ruleType,
		Filter:    datatypes.JSONMap(filter),
		Assertion: assertion,
		Message:   "测试",
		Enabled:   true,
	}
	if err := db.Create(&rule).Error; err != nil {
		t.Fatalf("创建规则失败: %v", err)
	}
	return rule
}

// lastCIStatus 取最新创建的 CI 状态与审计来源。
func lastCI(t *testing.T, db *gorm.DB, modelID string) store.CI {
	t.Helper()
	var ci store.CI
	if err := db.Where("model_id = ?", modelID).Order("created_at DESC").First(&ci).Error; err != nil {
		t.Fatalf("查询新建 CI 失败: %v", err)
	}
	return ci
}

func TestAutoIngestHitCreatesActiveCI(t *testing.T) {
	db, engine, model := setup(t)
	mustRule(t, db, store.AuditRuleTypeAutoIngest, "cpu_num >= 8", map[string]any{"os": "Rocky Linux 9"})

	d, err := engine.Evaluate(context.Background(), Record{
		Source: "n9e", Collector: "c", ModelCandidate: "host",
		Attributes: map[string]any{"ident": "web-wl-01", "ip": "10.9.0.1", "os": "Rocky Linux 9", "cpu_num": 16},
		OccurredAt: time.Now(),
	}, false)
	if err != nil {
		t.Fatalf("调和失败: %v", err)
	}
	if d.Action != ActionCreate {
		t.Fatalf("期望 create，实际 %s（%v）", d.Action, d.Reasons)
	}
	ci := lastCI(t, db, model.ID)
	if ci.Status != "active" {
		t.Fatalf("白名单命中应直接建档 active，实际 %s", ci.Status)
	}
	// 审计来源标 auto_ingest。
	var audit store.AuditLog
	if err := db.Where("ci_id = ? AND action = ?", ci.ID, "create").First(&audit).Error; err != nil {
		t.Fatalf("缺少建档审计: %v", err)
	}
	if audit.Source != "auto_ingest" {
		t.Fatalf("审计来源期望 auto_ingest，实际 %s", audit.Source)
	}
	if n := countPool(t, db); n != 0 {
		t.Fatalf("白名单命中不应写发现池，实际 %d 条", n)
	}
}

func TestAutoIngestMissKeepsDiscovered(t *testing.T) {
	db, engine, model := setup(t)
	mustRule(t, db, store.AuditRuleTypeAutoIngest, "cpu_num >= 8", map[string]any{"os": "Rocky Linux 9"})

	// 断言论不满足（cpu_num=4）：维持既有 create 语义（discovered）。
	if _, err := engine.Evaluate(context.Background(), Record{
		Source: "n9e", Collector: "c", ModelCandidate: "host",
		Attributes: map[string]any{"ident": "web-wl-02", "ip": "10.9.0.2", "os": "Rocky Linux 9", "cpu_num": 4},
		OccurredAt: time.Now(),
	}, false); err != nil {
		t.Fatalf("调和失败: %v", err)
	}
	if ci := lastCI(t, db, model.ID); ci.Status != "discovered" {
		t.Fatalf("未命中白名单应入发现池 discovered，实际 %s", ci.Status)
	}

	// filter 不满足（os 不同）：同样维持 discovered。
	if _, err := engine.Evaluate(context.Background(), Record{
		Source: "n9e", Collector: "c", ModelCandidate: "host",
		Attributes: map[string]any{"ident": "web-wl-03", "ip": "10.9.0.3", "os": "Ubuntu 22.04", "cpu_num": 16},
		OccurredAt: time.Now(),
	}, false); err != nil {
		t.Fatalf("调和失败: %v", err)
	}
	if ci := lastCI(t, db, model.ID); ci.Status != "discovered" {
		t.Fatalf("filter 不满足应入发现池 discovered，实际 %s", ci.Status)
	}
}

func TestAutoIngestDryRunAndDisabledNotApplied(t *testing.T) {
	db, engine, model := setup(t)
	dry := mustRule(t, db, store.AuditRuleTypeAutoIngest, "cpu_num >= 8", map[string]any{})
	if err := db.Model(&dry).Update("dry_run", true).Error; err != nil {
		t.Fatalf("更新规则失败: %v", err)
	}

	if _, err := engine.Evaluate(context.Background(), Record{
		Source: "n9e", Collector: "c", ModelCandidate: "host",
		Attributes: map[string]any{"ident": "web-wl-04", "ip": "10.9.0.4", "os": "Rocky", "cpu_num": 16},
		OccurredAt: time.Now(),
	}, false); err != nil {
		t.Fatalf("调和失败: %v", err)
	}
	if ci := lastCI(t, db, model.ID); ci.Status != "discovered" {
		t.Fatalf("dry_run 白名单只出报告不参与放行，实际 %s", ci.Status)
	}

	// 禁用规则同样不参与。
	if err := db.Model(&dry).Updates(map[string]any{"dry_run": false, "enabled": false}).Error; err != nil {
		t.Fatalf("更新规则失败: %v", err)
	}
	if _, err := engine.Evaluate(context.Background(), Record{
		Source: "n9e", Collector: "c", ModelCandidate: "host",
		Attributes: map[string]any{"ident": "web-wl-05", "ip": "10.9.0.5", "os": "Rocky", "cpu_num": 16},
		OccurredAt: time.Now(),
	}, false); err != nil {
		t.Fatalf("调和失败: %v", err)
	}
	if ci := lastCI(t, db, model.ID); ci.Status != "discovered" {
		t.Fatalf("禁用白名单不参与放行，实际 %s", ci.Status)
	}
}

func TestAutoIngestDoesNotAffectUpdateBranch(t *testing.T) {
	db, engine, model := setup(t)
	mustRule(t, db, store.AuditRuleTypeAutoIngest, "cpu_num >= 8", map[string]any{})
	// 存量 CI：ident=web-wl-06。
	if _, err := engine.Evaluate(context.Background(), hostRecord("web-wl-06", "10.9.0.6", "Rocky"), false); err != nil {
		t.Fatalf("建档失败: %v", err)
	}
	// 同 ident 再上报（属性满足白名单）：仍走 update 分支，白名单不干预。
	d, err := engine.Evaluate(context.Background(), Record{
		Source: "n9e", Collector: "c", ModelCandidate: "host",
		Attributes: map[string]any{"ident": "web-wl-06", "ip": "10.9.0.6", "os": "Rocky", "cpu_num": 32},
		OccurredAt: time.Now(),
	}, false)
	if err != nil {
		t.Fatalf("调和失败: %v", err)
	}
	if d.Action != ActionUpdate {
		t.Fatalf("命中存量应走 update（白名单仅 create 分支生效），实际 %s", d.Action)
	}
	if n := countCIs(t, db, model.ID); n != 1 {
		t.Fatalf("不应重复建档，实际 CI 数 %d", n)
	}
}
