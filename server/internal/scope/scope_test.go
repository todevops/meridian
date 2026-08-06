// 数据范围闭包（F-005）单测：一跳/命名空间两跳/多重归属并集/业务模型与共享设施可见/无归属隐藏。
package scope

import (
	"context"
	"fmt"
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/datatypes"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"meridian/server/internal/store"
)

// topo 承载测试拓扑的 CI ID。
type topo struct {
	appS, appT             string
	lineA                  string // biz_line
	hostS, hostT           string // 分别归属 S/T
	hostShared, hostOrphan string
	dbS                    string // appS depends_on
	nsS, wlS               string // 命名空间两跳：nsS -mounted_to-> appS，wlS -in_namespace-> nsS
	rack                   string // 共享基础设施
}

// setupTopo 构建两应用共享一套基础设施的测试拓扑。
func setupTopo(t *testing.T) (*gorm.DB, topo) {
	t.Helper()
	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared", t.Name())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("打开内存库失败: %v", err)
	}
	if err := db.AutoMigrate(store.AllModels()...); err != nil {
		t.Fatalf("迁移失败: %v", err)
	}
	modelIDs := map[string]string{}
	for _, code := range []string{"biz_line", "biz_app", "host", "db_instance", "k8s_namespace", "k8s_workload", "rack"} {
		m := store.Model{Name: code, Code: code,
			Attributes: datatypes.NewJSONType([]store.AttributeDefinition{})}
		if err := db.Create(&m).Error; err != nil {
			t.Fatalf("创建模型 %s 失败: %v", code, err)
		}
		modelIDs[code] = m.ID
	}
	mkCI := func(modelCode, name string) string {
		ci := store.CI{ModelID: modelIDs[modelCode], Status: "active", Source: "manual",
			Attributes:   datatypes.JSONMap{"name": name},
			FieldSources: datatypes.JSONMap{}}
		if err := db.Create(&ci).Error; err != nil {
			t.Fatalf("创建 CI %s 失败: %v", name, err)
		}
		return ci.ID
	}
	tp := topo{
		appS: mkCI("biz_app", "订单中心"), appT: mkCI("biz_app", "支付中心"),
		lineA: mkCI("biz_line", "交易平台"),
		hostS: mkCI("host", "s-host"), hostT: mkCI("host", "t-host"),
		hostShared: mkCI("host", "shared-host"), hostOrphan: mkCI("host", "orphan-host"),
		dbS: mkCI("db_instance", "s-mysql"),
		nsS: mkCI("k8s_namespace", "s-ns"), wlS: mkCI("k8s_workload", "s-deploy"),
		rack: mkCI("rack", "rack-A01"),
	}
	mkRel := func(code, src, dst string) {
		rel := store.CIRelation{RelationCode: code, SrcCIID: src, DstCIID: dst, Source: store.RelationSourceManual}
		if err := db.Create(&rel).Error; err != nil {
			t.Fatalf("创建关系 %s 失败: %v", code, err)
		}
	}
	mkRel("belongs_to", tp.appS, tp.lineA)
	mkRel("deployed_on", tp.appS, tp.hostS)
	mkRel("deployed_on", tp.appT, tp.hostT)
	mkRel("deployed_on", tp.appS, tp.hostShared) // 多重归属
	mkRel("deployed_on", tp.appT, tp.hostShared)
	mkRel("depends_on", tp.appS, tp.dbS)
	mkRel("mounted_to", tp.nsS, tp.appS)
	mkRel("in_namespace", tp.wlS, tp.nsS)
	return db, tp
}

func userWithScope(ids ...string) *store.User {
	return &store.User{ID: "u1", ScopeAppIDs: datatypes.NewJSONType(ids)}
}

func TestVisibleSetClosure(t *testing.T) {
	db, tp := setupTopo(t)
	r := New(db)
	set, restricted, err := r.VisibleSet(context.Background(), userWithScope(tp.appS))
	if err != nil || !restricted {
		t.Fatalf("受限用户闭包计算失败: restricted=%v err=%v", restricted, err)
	}
	for name, id := range map[string]string{
		"应用自身": tp.appS, "一跳主机": tp.hostS, "一跳数据库": tp.dbS,
		"命名空间": tp.nsS, "命名空间两跳工作负载": tp.wlS,
		"多重归属主机": tp.hostShared, "业务线": tp.lineA, "共享机柜": tp.rack,
		"其他应用（业务模型全量可见）": tp.appT,
	} {
		if !set[id] {
			t.Errorf("%s 应在闭包内", name)
		}
	}
	for name, id := range map[string]string{
		"他系统主机": tp.hostT, "无归属主机": tp.hostOrphan,
	} {
		if set[id] {
			t.Errorf("%s 不应在闭包内", name)
		}
	}
}

func TestVisibleSetMultiOwnershipUnion(t *testing.T) {
	db, tp := setupTopo(t)
	r := New(db)
	// 绑定 T 的用户同样可见共享主机（多重归属并集，AC-F005-04）。
	setT, _, err := r.VisibleSet(context.Background(), userWithScope(tp.appT))
	if err != nil {
		t.Fatalf("闭包计算失败: %v", err)
	}
	if !setT[tp.hostShared] || !setT[tp.hostT] {
		t.Fatalf("T 用户应见共享主机与 T 主机")
	}
	if setT[tp.hostS] || setT[tp.dbS] {
		t.Fatalf("T 用户不应见 S 独占资产")
	}
	// 同时绑定 S 与 T：并集可见。
	setBoth, _, err := r.VisibleSet(context.Background(), userWithScope(tp.appS, tp.appT))
	if err != nil {
		t.Fatalf("闭包计算失败: %v", err)
	}
	for _, id := range []string{tp.hostS, tp.hostT, tp.hostShared, tp.dbS} {
		if !setBoth[id] {
			t.Errorf("双范围用户应见 %s", id)
		}
	}
	if setBoth[tp.hostOrphan] {
		t.Errorf("无归属主机仅全量角色可见")
	}
}

func TestVisibleSetUnrestrictedWhenScopeEmpty(t *testing.T) {
	db, _ := setupTopo(t)
	r := New(db)
	if _, restricted, err := r.VisibleSet(context.Background(), userWithScope()); err != nil || restricted {
		t.Fatalf("空范围应不受限: restricted=%v err=%v", restricted, err)
	}
	if _, restricted, err := r.VisibleSet(context.Background(), &store.User{ID: "u2"}); err != nil || restricted {
		t.Fatalf("scope 为 nil 应不受限: restricted=%v err=%v", restricted, err)
	}
}

func TestVisibleSetCacheHit(t *testing.T) {
	db, tp := setupTopo(t)
	r := New(db)
	u := userWithScope(tp.appS)
	set1, _, err := r.VisibleSet(context.Background(), u)
	if err != nil {
		t.Fatalf("闭包计算失败: %v", err)
	}
	set2, _, err := r.VisibleSet(context.Background(), u)
	if err != nil {
		t.Fatalf("闭包计算失败: %v", err)
	}
	// 缓存命中：两次返回同一闭包内容。
	if len(set1) != len(set2) {
		t.Fatalf("缓存命中应返回同一闭包内容")
	}
	for id := range set1 {
		if !set2[id] {
			t.Fatalf("缓存闭包内容不一致")
		}
	}
	// 范围变更 → 缓存键变化 → 即时生效（不等 TTL）。
	u.ScopeAppIDs = datatypes.NewJSONType([]string{tp.appT})
	set3, _, err := r.VisibleSet(context.Background(), u)
	if err != nil {
		t.Fatalf("闭包计算失败: %v", err)
	}
	if set3[tp.hostS] || !set3[tp.hostT] {
		t.Fatalf("范围变更应即时生效")
	}
}
