// 调和引擎 2B 增量单测：黑设备告警事件写入 + 后置钩子（自动关联触发点）。
package reconcile

import (
	"context"
	"testing"
	"time"

	"gorm.io/gorm"

	"meridian/server/internal/store"
)

// countAlerts 统计告警事件条数。
func countAlerts(t *testing.T, db *gorm.DB) int64 {
	t.Helper()
	var n int64
	if err := db.Model(&store.AlertEvent{}).Count(&n).Error; err != nil {
		t.Fatalf("统计告警失败: %v", err)
	}
	return n
}

func TestBlackDeviceAlertOnCreate(t *testing.T) {
	db, engine, _ := setup(t)
	rec := hostRecord("rogue-01", "10.66.0.1", "unknown")
	rec.Attributes["black_device_risk"] = true
	d, err := engine.Evaluate(context.Background(), rec, false)
	if err != nil {
		t.Fatalf("调和失败: %v", err)
	}
	if d.Action != ActionCreate {
		t.Fatalf("期望 create，实际 %s", d.Action)
	}
	if n := countAlerts(t, db); n != 1 {
		t.Fatalf("期望 1 条告警事件，实际 %d", n)
	}
	var alert store.AlertEvent
	if err := db.First(&alert).Error; err != nil {
		t.Fatalf("加载告警失败: %v", err)
	}
	if alert.Level != store.AlertLevelWarning {
		t.Fatalf("告警级别应为 warning，实际 %s", alert.Level)
	}
	if alert.CIID != d.MatchedCIID {
		t.Fatalf("告警应关联建档 CI %s，实际 %s", d.MatchedCIID, alert.CIID)
	}
	if alert.Acknowledged {
		t.Fatalf("新告警不应已确认")
	}
	if alert.Source != "n9e" || alert.Title == "" || alert.Detail == "" {
		t.Fatalf("告警字段不完整: %+v", alert)
	}
}

func TestBlackDeviceAlertNotOnUpdate(t *testing.T) {
	db, engine, _ := setup(t)
	rec := hostRecord("rogue-02", "10.66.0.2", "unknown")
	rec.Attributes["black_device_risk"] = true
	if _, err := engine.Evaluate(context.Background(), rec, false); err != nil {
		t.Fatalf("首次调和失败: %v", err)
	}
	// 再次上报（同 ident 不同 ip）判定 update：不得重复告警。
	rec2 := hostRecord("rogue-02", "10.66.0.3", "unknown")
	rec2.Attributes["black_device_risk"] = true
	d, err := engine.Evaluate(context.Background(), rec2, false)
	if err != nil {
		t.Fatalf("二次调和失败: %v", err)
	}
	if d.Action != ActionUpdate {
		t.Fatalf("期望 update，实际 %s", d.Action)
	}
	if n := countAlerts(t, db); n != 1 {
		t.Fatalf("update 不应新增告警，期望 1 条，实际 %d", n)
	}
}

func TestNoAlertWithoutRiskFlag(t *testing.T) {
	db, engine, _ := setup(t)
	if _, err := engine.Evaluate(context.Background(), hostRecord("web-01", "10.66.1.1", "Rocky Linux 9"), false); err != nil {
		t.Fatalf("调和失败: %v", err)
	}
	// 标记为 false 同样不告警。
	rec := hostRecord("web-02", "10.66.1.2", "Rocky Linux 9")
	rec.Attributes["black_device_risk"] = false
	if _, err := engine.Evaluate(context.Background(), rec, false); err != nil {
		t.Fatalf("调和失败: %v", err)
	}
	if n := countAlerts(t, db); n != 0 {
		t.Fatalf("无风险标记不应告警，实际 %d 条", n)
	}
}

func TestPostHookFiredAsync(t *testing.T) {
	_, engine, _ := setup(t)
	type call struct{ ciID, action string }
	fired := make(chan call, 4)
	engine.AddPostHook(func(_ context.Context, ciID, action string) error {
		fired <- call{ciID, action}
		return nil
	})

	d, err := engine.Evaluate(context.Background(), hostRecord("hook-01", "10.66.2.1", "Rocky Linux 9"), false)
	if err != nil {
		t.Fatalf("调和失败: %v", err)
	}
	select {
	case c := <-fired:
		if c.ciID != d.MatchedCIID || c.action != ActionCreate {
			t.Fatalf("钩子参数不符: %+v（期望 CI %s / create）", c, d.MatchedCIID)
		}
	case <-time.After(3 * time.Second):
		t.Fatalf("后置钩子未在超时内触发")
	}

	// dryRun 不触发钩子。
	if _, err := engine.Evaluate(context.Background(), hostRecord("hook-02", "10.66.2.2", "Rocky Linux 9"), true); err != nil {
		t.Fatalf("dryRun 调和失败: %v", err)
	}
	select {
	case c := <-fired:
		t.Fatalf("dryRun 不应触发钩子，实际收到 %+v", c)
	case <-time.After(300 * time.Millisecond):
	}
}

func TestPostHookErrorDoesNotBreakReconcile(t *testing.T) {
	_, engine, _ := setup(t)
	engine.AddPostHook(func(_ context.Context, _, _ string) error {
		return context.DeadlineExceeded // 模拟钩子失败
	})
	d, err := engine.Evaluate(context.Background(), hostRecord("hook-03", "10.66.3.1", "Rocky Linux 9"), false)
	if err != nil {
		t.Fatalf("钩子失败不应影响调和: %v", err)
	}
	if d.Action != ActionCreate {
		t.Fatalf("期望 create，实际 %s", d.Action)
	}
	time.Sleep(200 * time.Millisecond) // 等待异步钩子执行完毕（仅记日志）
}

// TestBlackDeviceAlertOnPool 黑设备记录因冲突/校验入发现池时同样产生告警（AC-F043-01），
// 且按 detail 去重——周期扫描反复上报同一记录不重复告警。
func TestBlackDeviceAlertOnPool(t *testing.T) {
	db, engine, _ := setup(t)
	// 先建档一台主机（ident=web-01，ip=10.66.9.9）
	if _, err := engine.Evaluate(context.Background(), hostRecord("web-01", "10.66.9.9", "unknown"), false); err != nil {
		t.Fatalf("建档失败: %v", err)
	}
	// 扫描上报：同 IP 但 ident 不同且带黑设备标记 → 冲突入池
	rec := hostRecord("web-01-clone", "10.66.9.9", "unknown")
	rec.Source = "ip_scan"
	rec.Attributes["black_device_risk"] = true
	d, err := engine.Evaluate(context.Background(), rec, false)
	if err != nil {
		t.Fatalf("调和失败: %v", err)
	}
	if d.Action != ActionConflict {
		t.Fatalf("期望 conflict 入池，实际 %s", d.Action)
	}
	if n := countAlerts(t, db); n != 1 {
		t.Fatalf("入池黑设备应产生 1 条告警，实际 %d", n)
	}
	var alert store.AlertEvent
	if err := db.First(&alert).Error; err != nil {
		t.Fatalf("加载告警失败: %v", err)
	}
	if alert.Title == "" || alert.Source != "ip_scan" || alert.Acknowledged {
		t.Fatalf("告警字段不符: %+v", alert)
	}
	// 周期扫描再次上报同一记录：告警去重仍为 1 条
	if _, err := engine.Evaluate(context.Background(), rec, false); err != nil {
		t.Fatalf("重复调和失败: %v", err)
	}
	if n := countAlerts(t, db); n != 1 {
		t.Fatalf("重复扫描不应重复告警，实际 %d 条", n)
	}
}
