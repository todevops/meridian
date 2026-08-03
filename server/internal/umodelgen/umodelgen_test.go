// UModel 生成器单测（F-073）：模型→EntitySet 映射、关系→EntitySetLink、
// retired tombstone、每日全量对账幂等。
package umodelgen

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/datatypes"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"meridian/server/internal/store"
)

// fakeStore 是 EntityStore 的 httptest 假服务端：捕获实体/关联 upsert。
type fakeStore struct {
	srv      *httptest.Server
	mu       sync.Mutex
	entities map[string]map[string]map[string]any // set → pk → attrs（含 keep_alive_seconds）
	links    map[string][]Link                    // set → 关联（按请求次序）
}

func newFakeStore(t *testing.T) *fakeStore {
	t.Helper()
	f := &fakeStore{
		entities: map[string]map[string]map[string]any{},
		links:    map[string][]Link{},
	}
	mux := http.NewServeMux()
	mux.HandleFunc("PUT /api/v1/entitysets/{set}/entities/{pk}", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.mu.Lock()
		defer f.mu.Unlock()
		set, pk := r.PathValue("set"), r.PathValue("pk")
		if f.entities[set] == nil {
			f.entities[set] = map[string]map[string]any{}
		}
		f.entities[set][pk] = body
		_ = json.NewEncoder(w).Encode(map[string]any{"set": set, "pk": pk})
	})
	mux.HandleFunc("PUT /api/v1/entitysets/{set}/links", func(w http.ResponseWriter, r *http.Request) {
		var in []Link
		_ = json.NewDecoder(r.Body).Decode(&in)
		f.mu.Lock()
		defer f.mu.Unlock()
		set := r.PathValue("set")
		f.links[set] = append(f.links[set], in...)
		_ = json.NewEncoder(w).Encode(map[string]int{"upserted": len(in)})
	})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeStore) entity(set, pk string) (map[string]any, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	e, ok := f.entities[set][pk]
	return e, ok
}

// linkCount 统计指定 set 内指定 link_type 的关联条数。
func (f *fakeStore) linkCount(set, linkType string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, l := range f.links[set] {
		if l.LinkType == linkType {
			n++
		}
	}
	return n
}

// setupDB 预置模型与 CI：
//   - host web-01（reconcile_keys=["instance_uuid","ip","ident"]，instance_uuid 为主键）
//   - db_instance（depends_on 被应用依赖 + runs_on 主机）
//   - biz_app 商城前台（deployed_on 主机 + depends_on 数据库）
//   - retired host old-01（tombstone 素材）
//   - biz_line（未映射模型，应跳过）
func setupDB(t *testing.T) (*gorm.DB, map[string]store.CI) {
	t.Helper()
	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared", t.Name())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("打开内存库失败: %v", err)
	}
	if err := db.AutoMigrate(store.AllModels()...); err != nil {
		t.Fatalf("迁移失败: %v", err)
	}
	models := []store.Model{
		{Name: "主机", Code: "host",
			ReconcileKeys: datatypes.NewJSONType([]string{"instance_uuid", "ip", "ident"})},
		{Name: "数据库实例", Code: "db_instance",
			ReconcileKeys: datatypes.NewJSONType([]string{"instance_addr"})},
		{Name: "应用系统", Code: "biz_app",
			ReconcileKeys: datatypes.NewJSONType([]string{"code"})},
		{Name: "业务线", Code: "biz_line"},
	}
	for i := range models {
		if err := db.Create(&models[i]).Error; err != nil {
			t.Fatalf("创建模型失败: %v", err)
		}
	}
	cis := map[string]store.CI{}
	mustCI := func(modelCode, status string, attrs map[string]any) store.CI {
		t.Helper()
		var model store.Model
		if err := db.First(&model, "code = ?", modelCode).Error; err != nil {
			t.Fatalf("模型 %s 不存在: %v", modelCode, err)
		}
		ci := store.CI{ModelID: model.ID, Attributes: datatypes.JSONMap(attrs), Status: status, Source: "manual"}
		if err := db.Create(&ci).Error; err != nil {
			t.Fatalf("创建 CI 失败: %v", err)
		}
		return ci
	}
	cis["host"] = mustCI("host", "active", map[string]any{
		"instance_uuid": "uuid-001", "ident": "web-01", "ip": "10.0.1.1"})
	cis["db"] = mustCI("db_instance", "active", map[string]any{"instance_addr": "10.0.1.1:3306", "version": "8.0"})
	cis["app"] = mustCI("biz_app", "active", map[string]any{"code": "mall-front", "name": "商城前台"})
	cis["retired"] = mustCI("host", "retired", map[string]any{"ident": "old-01", "ip": "10.0.1.9"})
	cis["line"] = mustCI("biz_line", "active", map[string]any{"code": "ec", "name": "电商平台"})

	mustRel := func(code string, src, dst store.CI) {
		t.Helper()
		if err := db.Create(&store.CIRelation{
			RelationCode: code, SrcCIID: src.ID, DstCIID: dst.ID, Source: "auto",
		}).Error; err != nil {
			t.Fatalf("创建关系失败: %v", err)
		}
	}
	mustRel("deployed_on", cis["app"], cis["host"]) // 映射：apm.service ↔ infra.host
	mustRel("depends_on", cis["app"], cis["db"])    // 映射：apm.service ↔ mw.db_instance
	mustRel("runs_on", cis["db"], cis["host"])      // 映射：mw.db_instance ↔ infra.host
	mustRel("belongs_to", cis["app"], cis["line"])  // 未映射关系码 + 未映射端点：跳过
	return db, cis
}

// TestHandleMapping 验证事件式 upsert：实体集合/主键/保活与关联映射。
func TestHandleMapping(t *testing.T) {
	db, cis := setupDB(t)
	fs := newFakeStore(t)
	gen := New(db, NewClient(fs.srv.URL, "dev-umodel-token"))

	if err := gen.Handle(context.Background(), cis["app"].ID, "create"); err != nil {
		t.Fatalf("Handle 失败: %v", err)
	}
	// 应用实体：apm.service，主键复用调和主键 code。
	e, ok := fs.entity("apm.service", "mall-front")
	if !ok {
		t.Fatalf("应用实体未写入: %+v", fs.entities)
	}
	if e["name"] != "商城前台" || e["status"] != "active" {
		t.Errorf("应用实体属性不符: %+v", e)
	}
	if ka, _ := e["keep_alive_seconds"].(float64); int(ka) != KeepAliveSeconds {
		t.Errorf("保活秒数 = %v，期望 %d", e["keep_alive_seconds"], KeepAliveSeconds)
	}
	// 关联：deployed_on 写入两端 set（apm.service 与 infra.host）。
	if fs.linkCount("apm.service", "deployed_on") != 1 || fs.linkCount("infra.host", "deployed_on") != 1 {
		t.Errorf("deployed_on 关联应写入两端 set: %+v", fs.links)
	}
	if fs.linkCount("apm.service", "depends_on") != 1 || fs.linkCount("mw.db_instance", "depends_on") != 1 {
		t.Errorf("depends_on 关联应写入两端 set: %+v", fs.links)
	}
	// belongs_to（未映射码表）不应产生关联。
	for set, links := range fs.links {
		for _, l := range links {
			if l.LinkType == "belongs_to" {
				t.Errorf("belongs_to 不应映射（set %s）", set)
			}
		}
	}
	// 统计计数。
	stats := gen.StatsSnapshot()
	if stats.EntityUpserts != 1 || stats.LinkUpserts != 2 || stats.Tombstones != 0 {
		t.Errorf("统计计数不符: %+v", stats)
	}
}

// TestHandleHost 验证主机实体：调和主键取 instance_uuid（首键优先）。
func TestHandleHost(t *testing.T) {
	db, cis := setupDB(t)
	fs := newFakeStore(t)
	gen := New(db, NewClient(fs.srv.URL, "dev-umodel-token"))

	if err := gen.Handle(context.Background(), cis["host"].ID, "create"); err != nil {
		t.Fatalf("Handle 失败: %v", err)
	}
	e, ok := fs.entity("infra.host", "uuid-001")
	if !ok {
		t.Fatalf("主机实体应以 instance_uuid 为主键: %+v", fs.entities["infra.host"])
	}
	if e["ident"] != "web-01" {
		t.Errorf("主机实体属性不符: %+v", e)
	}
	// 主机侧入向关系（deployed_on/depends_on 的 runs_on）也一并 upsert。
	if fs.linkCount("infra.host", "runs_on") != 1 {
		t.Errorf("runs_on 关联应写入 infra.host: %+v", fs.links)
	}
}

// TestTombstone 验证 retired CI 写 tombstone：极简属性 + 极短保活。
func TestTombstone(t *testing.T) {
	db, cis := setupDB(t)
	fs := newFakeStore(t)
	gen := New(db, NewClient(fs.srv.URL, "dev-umodel-token"))

	if err := gen.Handle(context.Background(), cis["retired"].ID, "update"); err != nil {
		t.Fatalf("Handle 失败: %v", err)
	}
	// retired 主机无 instance_uuid：主键回退次键 ip。
	e, ok := fs.entity("infra.host", "10.0.1.9")
	if !ok {
		t.Fatalf("tombstone 实体未写入: %+v", fs.entities["infra.host"])
	}
	if e["tombstone"] != true {
		t.Errorf("tombstone 标记缺失: %+v", e)
	}
	if ka, _ := e["keep_alive_seconds"].(float64); int(ka) != TombstoneKeepAliveSeconds {
		t.Errorf("tombstone 保活 = %v，期望 %d（保活过期即下线）", e["keep_alive_seconds"], TombstoneKeepAliveSeconds)
	}
	if _, hasIdent := e["ident"]; hasIdent {
		t.Errorf("tombstone 应为极简属性（不含业务属性）: %+v", e)
	}
	if gen.StatsSnapshot().Tombstones != 1 {
		t.Errorf("tombstone 计数 = %d，期望 1", gen.StatsSnapshot().Tombstones)
	}
}

// TestReconcileIdempotent 验证每日全量对账幂等：
// 两轮对账后实体集合完全一致（主键稳定），未映射模型/关系不产生写入。
func TestReconcileIdempotent(t *testing.T) {
	db, _ := setupDB(t)
	fs := newFakeStore(t)
	gen := New(db, NewClient(fs.srv.URL, "dev-umodel-token"))

	if err := gen.ReconcileAll(context.Background()); err != nil {
		t.Fatalf("首轮对账失败: %v", err)
	}
	firstEntities := fmt.Sprintf("%v", fs.entities)
	firstLinkCounts := map[string]int{}
	for set, links := range fs.links {
		firstLinkCounts[set] = len(links)
	}
	firstStats := gen.StatsSnapshot()

	if err := gen.ReconcileAll(context.Background()); err != nil {
		t.Fatalf("次轮对账失败: %v", err)
	}
	// 实体集合不变（同主键覆盖写）。
	if fmt.Sprintf("%v", fs.entities) != firstEntities {
		t.Error("两轮对账后实体集合不一致")
	}
	// 关联按 set 计数翻倍（mock 按键去重，本假服务端按调用计数——
	// 断言写入集合形状一致：次轮每 set 写入条数与首轮相同）。
	for set, links := range fs.links {
		if len(links) != 2*firstLinkCounts[set] {
			t.Errorf("set %s 关联写入条数 = %d，期望首轮的 2 倍（%d）", set, len(links), 2*firstLinkCounts[set])
		}
	}
	// 计数累计翻倍、last_sync 已刷新。
	stats := gen.StatsSnapshot()
	if stats.EntityUpserts != 2*firstStats.EntityUpserts ||
		stats.LinkUpserts != 2*firstStats.LinkUpserts ||
		stats.Tombstones != 2*firstStats.Tombstones {
		t.Errorf("两轮对账计数未翻倍: 首轮 %+v 次轮 %+v", firstStats, stats)
	}
	if stats.LastSync == "" {
		t.Error("last_sync 应已刷新")
	}
	// 未映射模型（biz_line）不产生实体。
	if len(fs.entities["biz_line"]) != 0 {
		t.Errorf("biz_line 未映射，不应有实体: %+v", fs.entities)
	}
	// 对账覆盖 retired tombstone：old-01 以 ip 为主键写墓碑。
	if _, ok := fs.entity("infra.host", "10.0.1.9"); !ok {
		t.Error("对账应补写 retired 主机 tombstone")
	}
}

// TestReconcilePKCollision 验证主键冲突纪律：退役 CI 与再发现新 CI 共享调和主键
// （引擎不匹配 retired，同 ip 两 CI 并存）时，对账后"在用实体"恒为最终态。
func TestReconcilePKCollision(t *testing.T) {
	db, _ := setupDB(t)
	// 再发现：与 retired old-01 同 ip 的新 host（instance_uuid 为空 → 主键同为 ip）。
	var model store.Model
	if err := db.First(&model, "code = ?", "host").Error; err != nil {
		t.Fatalf("模型不存在: %v", err)
	}
	twin := store.CI{ModelID: model.ID, Status: "discovered", Source: "n9e",
		Attributes: datatypes.JSONMap{"ident": "old-01-new", "ip": "10.0.1.9"}}
	if err := db.Create(&twin).Error; err != nil {
		t.Fatalf("创建再发现 CI 失败: %v", err)
	}
	fs := newFakeStore(t)
	gen := New(db, NewClient(fs.srv.URL, "dev-umodel-token"))
	if err := gen.ReconcileAll(context.Background()); err != nil {
		t.Fatalf("对账失败: %v", err)
	}
	e, ok := fs.entity("infra.host", "10.0.1.9")
	if !ok {
		t.Fatal("冲突主键实体不存在")
	}
	if e["tombstone"] == true {
		t.Errorf("冲突时在用实体应覆盖 tombstone: %+v", e)
	}
	if ka, _ := e["keep_alive_seconds"].(float64); int(ka) != KeepAliveSeconds {
		t.Errorf("最终保活 = %v，期望 %d（在用优先）", e["keep_alive_seconds"], KeepAliveSeconds)
	}
	if gen.StatsSnapshot().Tombstones != 1 {
		t.Errorf("tombstone 计数 = %d，期望 1（退役侧仍计入）", gen.StatsSnapshot().Tombstones)
	}
}
