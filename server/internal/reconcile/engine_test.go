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

// TestIPAMLink 验证调和建档/更新后自动维护 CI↔IPAM 关联（ip_addresses.ci_id）：
// 建档挂接已登记 IP；IP 变更时旧 IP 解除挂载、新 IP 挂接；未登记 IP 不自动建条目。
func TestIPAMLink(t *testing.T) {
	db, engine, model := setup(t)
	ctx := context.Background()

	// 预置 IPAM 数据：前缀 10.0.0.0/24 + 两个已登记 IP。
	prefix := store.IPPrefix{CIDR: "10.0.0.0/24", Name: "办公网"}
	if err := db.Create(&prefix).Error; err != nil {
		t.Fatalf("创建前缀失败: %v", err)
	}
	ip1 := store.IPAddress{PrefixID: prefix.ID, IP: "10.0.0.1", Status: "used"}
	ip9 := store.IPAddress{PrefixID: prefix.ID, IP: "10.0.0.9", Status: "used"}
	if err := db.Create(&ip1).Error; err != nil {
		t.Fatalf("创建 IP 失败: %v", err)
	}
	if err := db.Create(&ip9).Error; err != nil {
		t.Fatalf("创建 IP 失败: %v", err)
	}

	// 1. 调和建档后，10.0.0.1 的 ci_id 挂到新 CI。
	if _, err := engine.Evaluate(ctx, hostRecord("web-01", "10.0.0.1", "Rocky Linux 9"), false); err != nil {
		t.Fatalf("首次调和失败: %v", err)
	}
	var ci store.CI
	if err := db.First(&ci, "model_id = ?", model.ID).Error; err != nil {
		t.Fatalf("CI 未建档: %v", err)
	}
	// 注意：每次查询须用全新结构体——复用已带主键的结构体时
	// GORM 会把旧主键附加为查询条件，导致后续查询查不到。
	var linked store.IPAddress
	if err := db.First(&linked, "ip = ?", "10.0.0.1").Error; err != nil {
		t.Fatalf("读取 IPAM IP 失败: %v", err)
	}
	if linked.CIID != ci.ID {
		t.Fatalf("建档后 IPAM 未挂接: ci_id=%q 期望 %q", linked.CIID, ci.ID)
	}

	// 2. 主键为 ip 的模型（keys=["ident","ip"] 中 ident 命中）改 IP：
	// 旧 IP 解除挂载，新 IP 挂接。
	if _, err := engine.Evaluate(ctx, hostRecord("web-01", "10.0.0.9", "Rocky Linux 9"), false); err != nil {
		t.Fatalf("二次调和失败: %v", err)
	}
	linked = store.IPAddress{}
	if err := db.First(&linked, "ip = ?", "10.0.0.1").Error; err != nil {
		t.Fatalf("读取旧 IP 失败: %v", err)
	}
	if linked.CIID != "" {
		t.Fatalf("旧 IP 应解除挂载，实际 ci_id=%q", linked.CIID)
	}
	linked = store.IPAddress{}
	if err := db.First(&linked, "ip = ?", "10.0.0.9").Error; err != nil {
		t.Fatalf("读取新 IP 失败: %v", err)
	}
	if linked.CIID != ci.ID {
		t.Fatalf("新 IP 应挂接本 CI，实际 ci_id=%q", linked.CIID)
	}

	// 3. 未登记的 IP（10.0.0.99）不产生 IPAM 条目。
	if _, err := engine.Evaluate(ctx, hostRecord("web-01", "10.0.0.99", "Rocky Linux 9"), false); err != nil {
		t.Fatalf("三次调和失败: %v", err)
	}
	var n int64
	if err := db.Model(&store.IPAddress{}).Where("ip = ?", "10.0.0.99").Count(&n).Error; err != nil {
		t.Fatalf("统计失败: %v", err)
	}
	if n != 0 {
		t.Fatalf("未登记 IP 不应自动创建 IPAM 条目，实际 %d 条", n)
	}
}

// TestHostPrimaryIPKey 模拟生产 host 模型配置（reconcile_keys=["ip"]）：
// 同 IP 不同 ident 的记录按主 IP 合并为一条 CI，ident 作为普通属性被更新，
// 不判冲突、不进发现池。
func TestHostPrimaryIPKey(t *testing.T) {
	db, engine, _ := setup(t)
	// 改用生产口径的调和键：仅主 IP。
	var model store.Model
	if err := db.First(&model, "code = ?", "host").Error; err != nil {
		t.Fatalf("读取模型失败: %v", err)
	}
	model.ReconcileKeys = datatypes.NewJSONType([]string{"ip"})
	if err := db.Save(&model).Error; err != nil {
		t.Fatalf("更新调和键失败: %v", err)
	}
	ctx := context.Background()

	if _, err := engine.Evaluate(ctx, hostRecord("web-01", "10.0.0.1", "Rocky Linux 9"), false); err != nil {
		t.Fatalf("首次调和失败: %v", err)
	}
	// 同 IP、不同 ident（主机改名/ident 漂移）：按主 IP 合并，不产生第二条 CI。
	d, err := engine.Evaluate(ctx, hostRecord("web-01-renamed", "10.0.0.1", "Rocky Linux 9"), false)
	if err != nil {
		t.Fatalf("二次调和失败: %v", err)
	}
	if d.Action != ActionUpdate {
		t.Fatalf("同主 IP 应合并更新，实际动作 %s（%v）", d.Action, d.Reasons)
	}
	if got := countCIs(t, db, model.ID); got != 1 {
		t.Fatalf("应仅存在 1 条 CI，实际 %d", got)
	}
	if got := countPool(t, db); got != 0 {
		t.Fatalf("同主 IP 不应进发现池，实际 %d 条", got)
	}
}

// uidKeys 是生产口径的主机调和键链（与 scripts/seed/host.json 一致）。
var uidKeys = []string{"instance_uuid", "cloud_instance_id", "serial_no", "ip", "ident"}

// useUIDKeys 把测试模型的调和键切换为生产口径的 UID 优先链。
func useUIDKeys(t *testing.T, db *gorm.DB) store.Model {
	t.Helper()
	var model store.Model
	if err := db.First(&model, "code = ?", "host").Error; err != nil {
		t.Fatalf("读取模型失败: %v", err)
	}
	model.ReconcileKeys = datatypes.NewJSONType(uidKeys)
	if err := db.Save(&model).Error; err != nil {
		t.Fatalf("更新调和键失败: %v", err)
	}
	return model
}

// TestStableUIDKey 稳定 UID 是同一性判定的第一键：
// 同一台 VM 改名 + 换 IP 后，凭 instance_uuid 仍识别为同一资产并同步变更。
func TestStableUIDKey(t *testing.T) {
	db, engine, _ := setup(t)
	model := useUIDKeys(t, db)
	ctx := context.Background()

	rec1 := hostRecord("web-01", "10.0.0.1", "Rocky Linux 9")
	rec1.Source = "vsphere"
	rec1.Attributes["instance_uuid"] = "uuid-aaa"
	if _, err := engine.Evaluate(ctx, rec1, false); err != nil {
		t.Fatalf("首次调和失败: %v", err)
	}

	// 改名 + 换 IP，UUID 不变 → 命中 UUID 键，更新为一条 CI。
	rec2 := hostRecord("web-01-renamed", "10.0.0.5", "Rocky Linux 9")
	rec2.Source = "vsphere"
	rec2.Attributes["instance_uuid"] = "uuid-aaa"
	d, err := engine.Evaluate(ctx, rec2, false)
	if err != nil {
		t.Fatalf("二次调和失败: %v", err)
	}
	if d.Action != ActionUpdate {
		t.Fatalf("同 UUID 应合并更新，实际动作 %s（%v）", d.Action, d.Reasons)
	}
	if got := countCIs(t, db, model.ID); got != 1 {
		t.Fatalf("应仅存在 1 条 CI，实际 %d", got)
	}
	var ci store.CI
	if err := db.First(&ci, "model_id = ?", model.ID).Error; err != nil {
		t.Fatalf("读取 CI 失败: %v", err)
	}
	if ci.Attributes["ip"] != "10.0.0.5" || ci.Attributes["ident"] != "web-01-renamed" {
		t.Fatalf("改名/换 IP 应同步生效: %v", ci.Attributes)
	}
	if got := countPool(t, db); got != 0 {
		t.Fatalf("同 UUID 不应进发现池，实际 %d 条", got)
	}
}

// TestCloudUIDEnrichment n9e 建档的 CI 没有云实例 ID：
// 云采集器首次上报同 IP 记录时按 IP 合并并补全 UID（来源优先级不阻挡空槽位），
// 后续云记录即可凭 cloud_instance_id 直接命中。
func TestCloudUIDEnrichment(t *testing.T) {
	db, engine, _ := setup(t)
	model := useUIDKeys(t, db)
	ctx := context.Background()

	// n9e 建档（无 UID）。
	if _, err := engine.Evaluate(ctx, hostRecord("ecs-01", "10.0.0.2", "Anolis OS"), false); err != nil {
		t.Fatalf("n9e 调和失败: %v", err)
	}

	// 阿里云首次上报同 IP：按 IP 合并，补全 cloud_instance_id（不判冲突）。
	cloud := hostRecord("ecs-01", "10.0.0.2", "")
	cloud.Source = "aliyun"
	cloud.Attributes["cloud_instance_id"] = "i-abc123"
	d, err := engine.Evaluate(ctx, cloud, false)
	if err != nil {
		t.Fatalf("云首次调和失败: %v", err)
	}
	if d.Action != ActionUpdate {
		t.Fatalf("同 IP 应合并，实际动作 %s（%v）", d.Action, d.Reasons)
	}
	var ci store.CI
	if err := db.First(&ci, "model_id = ?", model.ID).Error; err != nil {
		t.Fatalf("读取 CI 失败: %v", err)
	}
	if ci.Attributes["cloud_instance_id"] != "i-abc123" {
		t.Fatalf("云实例 ID 应被补全: %v", ci.Attributes)
	}
	if got := countPool(t, db); got != 0 {
		t.Fatalf("首次补全 UID 不应误判冲突，实际入池 %d 条", got)
	}

	// 云实例换 IP 后：凭 cloud_instance_id 命中，IP 同步更新。
	cloud2 := hostRecord("ecs-01", "10.0.0.22", "")
	cloud2.Source = "aliyun"
	cloud2.Attributes["cloud_instance_id"] = "i-abc123"
	d2, err := engine.Evaluate(ctx, cloud2, false)
	if err != nil {
		t.Fatalf("云换 IP 调和失败: %v", err)
	}
	if d2.Action != ActionUpdate {
		t.Fatalf("同云实例 ID 应合并，实际动作 %s（%v）", d2.Action, d2.Reasons)
	}
	if got := countCIs(t, db, model.ID); got != 1 {
		t.Fatalf("应仅存在 1 条 CI，实际 %d", got)
	}
}

// TestIPReuseConflict IP 复用保护：
// 存量 CI（cloud_instance_id=i-1）的 IP 被分配给新实例（i-2）后，
// 同 IP 但 UID 不符 → 判定冲突入池人工裁决，绝不错误合并。
func TestIPReuseConflict(t *testing.T) {
	db, engine, _ := setup(t)
	model := useUIDKeys(t, db)
	ctx := context.Background()

	rec1 := hostRecord("ecs-old", "10.0.0.3", "")
	rec1.Source = "aliyun"
	rec1.Attributes["cloud_instance_id"] = "i-old"
	if _, err := engine.Evaluate(ctx, rec1, false); err != nil {
		t.Fatalf("首次调和失败: %v", err)
	}

	rec2 := hostRecord("ecs-new", "10.0.0.3", "")
	rec2.Source = "aliyun"
	rec2.Attributes["cloud_instance_id"] = "i-new"
	d, err := engine.Evaluate(ctx, rec2, false)
	if err != nil {
		t.Fatalf("二次调和失败: %v", err)
	}
	if d.Action != ActionConflict {
		t.Fatalf("同 IP 不同云实例 ID 应判冲突，实际动作 %s（%v）", d.Action, d.Reasons)
	}
	if got := countCIs(t, db, model.ID); got != 1 {
		t.Fatalf("冲突不应改动 CI，实际 %d 条", got)
	}
	if got := countPool(t, db); got != 1 {
		t.Fatalf("冲突应入池待裁决，实际 %d 条", got)
	}
}

// TestIdentFallbackPool 主机换 IP 的兜底线索：
// n9e 记录 ident 相同但 IP 不符（无 UID 可用的裸机换 IP 场景）→
// ident 末位键命中但交叉键不符，入池人工裁决而非静默新建重复 CI。
func TestIdentFallbackPool(t *testing.T) {
	db, engine, _ := setup(t)
	model := useUIDKeys(t, db)
	ctx := context.Background()

	if _, err := engine.Evaluate(ctx, hostRecord("bare-01", "10.0.0.4", "Rocky Linux 9"), false); err != nil {
		t.Fatalf("首次调和失败: %v", err)
	}
	d, err := engine.Evaluate(ctx, hostRecord("bare-01", "10.0.0.44", "Rocky Linux 9"), false)
	if err != nil {
		t.Fatalf("二次调和失败: %v", err)
	}
	if d.Action != ActionConflict {
		t.Fatalf("同 ident 不同 IP 应判冲突入池，实际动作 %s（%v）", d.Action, d.Reasons)
	}
	if got := countCIs(t, db, model.ID); got != 1 {
		t.Fatalf("不应静默新建重复 CI，实际 %d 条", got)
	}
	if got := countPool(t, db); got != 1 {
		t.Fatalf("应入池待人工裁决，实际 %d 条", got)
	}
}

// ignoreLatestPool 把最新入池的 pending 条目标记为 ignored，返回条目 ID。
func ignoreLatestPool(t *testing.T, db *gorm.DB) string {
	t.Helper()
	var item store.PoolItem
	if err := db.Where("status = ?", "pending").Order("created_at DESC").First(&item).Error; err != nil {
		t.Fatalf("查询池条目失败: %v", err)
	}
	if item.RecordHash == "" {
		t.Fatal("池条目应写入记录同一性哈希（RecordHash）")
	}
	if err := db.Model(&store.PoolItem{}).Where("id = ?", item.ID).
		Update("status", "ignored").Error; err != nil {
		t.Fatalf("标记 ignored 失败: %v", err)
	}
	return item.ID
}

// D-02：ignore 后同一记录（同 model_candidate + 调和键值）不再入 pending，
// 不同记录（调和键值不同）仍正常入池。
func TestIgnoredRecordNotRePooled(t *testing.T) {
	db, engine, _ := setup(t)
	ctx := context.Background()

	// 建档基准 CI：ident=x1 / ip=10.9.0.1。
	if _, err := engine.Evaluate(ctx, hostRecord("x1", "10.9.0.1", "Rocky Linux 9"), false); err != nil {
		t.Fatalf("建档失败: %v", err)
	}
	// 冲突记录 B：同 IP 不同 ident → 次要键命中 + 主键不符 → conflict 入池。
	recB := hostRecord("x2", "10.9.0.1", "Rocky Linux 9")
	d, err := engine.Evaluate(ctx, recB, false)
	if err != nil || d.Action != ActionConflict {
		t.Fatalf("B 应判冲突入池: action=%s err=%v", d.Action, err)
	}
	if got := countPool(t, db); got != 1 {
		t.Fatalf("B 入池后 pending 应为 1，实际 %d", got)
	}
	ignoreLatestPool(t, db)

	// 同一记录 B 再次调和：仍判 conflict，但不再产生新的 pending 条目。
	d, err = engine.Evaluate(ctx, recB, false)
	if err != nil || d.Action != ActionConflict {
		t.Fatalf("B 重复调和仍应判 conflict: action=%s err=%v", d.Action, err)
	}
	if got := countPool(t, db); got != 0 {
		t.Fatalf("ignore 后同一记录不应再入 pending，实际 %d", got)
	}

	// 不同记录 B'（ident 不同 → 哈希不同）：仍应入池。
	d, err = engine.Evaluate(ctx, hostRecord("x3", "10.9.0.1", "Rocky Linux 9"), false)
	if err != nil || d.Action != ActionConflict {
		t.Fatalf("B' 应判冲突入池: action=%s err=%v", d.Action, err)
	}
	if got := countPool(t, db); got != 1 {
		t.Fatalf("不同记录应正常入 pending，实际 %d", got)
	}
}

// D-02 退化路径：模型无调和键时按 model_candidate + 全属性 JSON 哈希去重。
func TestIgnoredRecordNotRePooledWithoutKeys(t *testing.T) {
	db, engine, _ := setup(t)
	ctx := context.Background()

	// 无调和键的模型。
	widget := store.Model{Name: "部件", Code: "widget"}
	if err := db.Create(&widget).Error; err != nil {
		t.Fatalf("创建模型失败: %v", err)
	}
	rec := Record{
		Source: "n9e", Collector: "c1", ModelCandidate: "widget",
		Attributes: map[string]any{"a": "1", "b": "2"}, OccurredAt: time.Now(),
	}
	d, err := engine.Evaluate(ctx, rec, false)
	if err != nil || d.Action != ActionPool {
		t.Fatalf("无调和键应转入发现池: action=%s err=%v", d.Action, err)
	}
	if got := countPool(t, db); got != 1 {
		t.Fatalf("入池后 pending 应为 1，实际 %d", got)
	}
	ignoreLatestPool(t, db)

	// 同一记录再入：静默丢弃。
	if _, err := engine.Evaluate(ctx, rec, false); err != nil {
		t.Fatalf("重复调和失败: %v", err)
	}
	if got := countPool(t, db); got != 0 {
		t.Fatalf("ignore 后同一记录不应再入 pending，实际 %d", got)
	}
	// 属性不同 → 哈希不同 → 仍入池。
	rec2 := rec
	rec2.Attributes = map[string]any{"a": "1", "b": "3"}
	if _, err := engine.Evaluate(ctx, rec2, false); err != nil {
		t.Fatalf("调和失败: %v", err)
	}
	if got := countPool(t, db); got != 1 {
		t.Fatalf("不同记录应正常入 pending，实际 %d", got)
	}
}

// 来源优先级表的键必须等于采集器实际发送的 Source 值（collectors 各包 Source 常量），
// 死键/错键会让对应来源静默落到默认分——本测试钉住这张表。
func TestSourcePrioritiesMatchCollectorKeys(t *testing.T) {
	want := map[string]int{
		"manual":  100,
		"n9e":     80,
		"vsphere": 70,
		"aliyun":  70,
		"volc":    70, // 采集器发送 "volc"，不是 "volcengine"
		"ip_scan": 60, // 采集器发送 "ip_scan"，不是 "nmap"
	}
	for source, p := range want {
		if got := PriorityOf(source); got != p {
			t.Errorf("PriorityOf(%q) = %d, 期望 %d", source, got, p)
		}
	}
	// 未建模的来源落默认分。
	for _, source := range []string{"librenms", "tsdb", "unknown"} {
		if got := PriorityOf(source); got != defaultSourcePriority {
			t.Errorf("PriorityOf(%q) = %d, 期望默认 %d", source, got, defaultSourcePriority)
		}
	}
}
