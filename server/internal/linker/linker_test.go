// 自动关联器单测：三类规则命中、幂等去重、模型/关系定义缺失容错。
package linker

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

// setup 打开独立内存库并预置 host/virtual_machine/db_instance 模型（关系定义对齐种子）。
func setup(t *testing.T) *gorm.DB {
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
		{
			Name: "主机", Code: "host",
			Relations: datatypes.NewJSONType([]store.RelationDefinition{
				{Name: "实例化于", Code: "instantiated_by", TargetModel: "virtual_machine", Cardinality: "one_to_one", Direction: "outgoing"},
			}),
		},
		{
			Name: "虚拟机", Code: "virtual_machine",
			Relations: datatypes.NewJSONType([]store.RelationDefinition{
				{Name: "运行于", Code: "runs_on", TargetModel: "esxi_host", Cardinality: "one_to_one", Direction: "outgoing"},
			}),
		},
		{
			Name: "数据库实例", Code: "db_instance",
			Relations: datatypes.NewJSONType([]store.RelationDefinition{
				{Name: "运行于", Code: "runs_on", TargetModel: "host", Cardinality: "one_to_one", Direction: "outgoing"},
			}),
		},
	}
	for i := range models {
		if err := db.Create(&models[i]).Error; err != nil {
			t.Fatalf("创建模型 %s 失败: %v", models[i].Code, err)
		}
	}
	return db
}

// mustCI 直接落库一个 CI。
func mustCI(t *testing.T, db *gorm.DB, modelCode string, attrs map[string]any) store.CI {
	t.Helper()
	var model store.Model
	if err := db.First(&model, "code = ?", modelCode).Error; err != nil {
		t.Fatalf("查询模型 %s 失败: %v", modelCode, err)
	}
	ci := store.CI{ModelID: model.ID, Attributes: datatypes.JSONMap(attrs), Status: "discovered", Source: "test"}
	if err := db.Create(&ci).Error; err != nil {
		t.Fatalf("创建 CI 失败: %v", err)
	}
	return ci
}

// countRelations 统计满足条件的关系条数。
func countRelations(t *testing.T, db *gorm.DB, code, srcID, dstID string) int64 {
	t.Helper()
	var n int64
	q := db.Model(&store.CIRelation{}).Where("relation_code = ?", code)
	if srcID != "" {
		q = q.Where("src_ci_id = ?", srcID)
	}
	if dstID != "" {
		q = q.Where("dst_ci_id = ?", dstID)
	}
	if err := q.Count(&n).Error; err != nil {
		t.Fatalf("统计关系失败: %v", err)
	}
	return n
}

func TestLinkHostVMFromHostSide(t *testing.T) {
	db := setup(t)
	vm := mustCI(t, db, "virtual_machine", map[string]any{"instance_uuid": "uuid-001", "name": "vm-web-01"})
	host := mustCI(t, db, "host", map[string]any{"instance_uuid": "uuid-001", "ident": "web-01", "ip": "10.0.0.1"})

	l := New(db)
	if err := l.Handle(context.Background(), host.ID, "create"); err != nil {
		t.Fatalf("关联失败: %v", err)
	}
	// host.instantiated_by→virtual_machine。
	if n := countRelations(t, db, "instantiated_by", host.ID, vm.ID); n != 1 {
		t.Fatalf("期望 1 条 instantiated_by 关系，实际 %d", n)
	}
	// 幂等：重复触发不产生重复关系。
	if err := l.Handle(context.Background(), host.ID, "update"); err != nil {
		t.Fatalf("重复触发失败: %v", err)
	}
	if err := l.Handle(context.Background(), vm.ID, "update"); err != nil {
		t.Fatalf("对侧重复触发失败: %v", err)
	}
	if n := countRelations(t, db, "instantiated_by", host.ID, vm.ID); n != 1 {
		t.Fatalf("幂等失败：期望仍为 1 条，实际 %d", n)
	}
}

func TestLinkHostVMFromVMSide(t *testing.T) {
	db := setup(t)
	// 主机先建档（此时无 VM，链接为空）；VM 后建档时从 VM 侧触发互链。
	host := mustCI(t, db, "host", map[string]any{"instance_uuid": "uuid-002", "ident": "db-01", "ip": "10.0.0.2"})
	l := New(db)
	if err := l.Handle(context.Background(), host.ID, "create"); err != nil {
		t.Fatalf("主机侧关联失败: %v", err)
	}
	if n := countRelations(t, db, "instantiated_by", "", ""); n != 0 {
		t.Fatalf("VM 未建档时不应有关系，实际 %d", n)
	}
	vm := mustCI(t, db, "virtual_machine", map[string]any{"instance_uuid": "uuid-002", "name": "vm-db-01"})
	if err := l.Handle(context.Background(), vm.ID, "create"); err != nil {
		t.Fatalf("VM 侧关联失败: %v", err)
	}
	if n := countRelations(t, db, "instantiated_by", host.ID, vm.ID); n != 1 {
		t.Fatalf("期望 1 条 instantiated_by 关系，实际 %d", n)
	}
}

func TestLinkInstanceHost(t *testing.T) {
	db := setup(t)
	host := mustCI(t, db, "host", map[string]any{"ident": "mysql-01", "ip": "10.0.0.11"})
	dbCI := mustCI(t, db, "db_instance", map[string]any{"instance_addr": "10.0.0.11:3306", "ip": "10.0.0.11", "port": 3306})

	l := New(db)
	if err := l.Handle(context.Background(), dbCI.ID, "create"); err != nil {
		t.Fatalf("实例侧关联失败: %v", err)
	}
	// db_instance.runs_on→host。
	if n := countRelations(t, db, "runs_on", dbCI.ID, host.ID); n != 1 {
		t.Fatalf("期望 1 条 runs_on 关系，实际 %d", n)
	}
	// 幂等 + 主机侧反向触发同一关系。
	if err := l.Handle(context.Background(), host.ID, "update"); err != nil {
		t.Fatalf("主机侧触发失败: %v", err)
	}
	if n := countRelations(t, db, "runs_on", dbCI.ID, host.ID); n != 1 {
		t.Fatalf("幂等失败：期望仍为 1 条，实际 %d", n)
	}
}

func TestLinkInstanceHostByInstanceAddr(t *testing.T) {
	db := setup(t)
	host := mustCI(t, db, "host", map[string]any{"ident": "redis-01", "ip": "10.0.0.12"})
	// 采集器只上报 instance_addr（无独立 ip 字段）时按主机段派生匹配。
	dbCI := mustCI(t, db, "db_instance", map[string]any{"instance_addr": "10.0.0.12:6379", "port": 6379})

	l := New(db)
	if err := l.Handle(context.Background(), dbCI.ID, "create"); err != nil {
		t.Fatalf("关联失败: %v", err)
	}
	if n := countRelations(t, db, "runs_on", dbCI.ID, host.ID); n != 1 {
		t.Fatalf("期望按 instance_addr 派生 IP 建 1 条 runs_on，实际 %d", n)
	}
}

func TestLinkVMInfra(t *testing.T) {
	db := setup(t)
	// 补齐 esxi_host/esxi_cluster 模型（关系定义齐备）。
	esxiHostModel := store.Model{
		Name: "ESXi 主机", Code: "esxi_host",
		Relations: datatypes.NewJSONType([]store.RelationDefinition{
			{Name: "属于", Code: "belongs_to", TargetModel: "esxi_cluster", Cardinality: "one_to_one", Direction: "outgoing"},
		}),
	}
	esxiClusterModel := store.Model{Name: "ESXi 集群", Code: "esxi_cluster"}
	for _, m := range []*store.Model{&esxiHostModel, &esxiClusterModel} {
		if err := db.Create(m).Error; err != nil {
			t.Fatalf("创建模型失败: %v", err)
		}
	}
	cluster := mustCI(t, db, "esxi_cluster", map[string]any{"moid": "domain-c100", "name": "生产集群"})
	esxi := mustCI(t, db, "esxi_host", map[string]any{"hardware_uuid": "hw-uuid-01", "name": "esxi-01"})
	vm := mustCI(t, db, "virtual_machine", map[string]any{
		"instance_uuid": "uuid-003", "name": "vm-app-01",
		"parent_host_uuid": "hw-uuid-01", "parent_cluster_moid": "domain-c100",
	})

	l := New(db)
	if err := l.Handle(context.Background(), vm.ID, "create"); err != nil {
		t.Fatalf("VM 侧关联失败: %v", err)
	}
	// vm.runs_on→esxi_host。
	if n := countRelations(t, db, "runs_on", vm.ID, esxi.ID); n != 1 {
		t.Fatalf("期望 1 条 vm.runs_on→esxi_host，实际 %d", n)
	}
	// esxi_host.belongs_to→esxi_cluster。
	if n := countRelations(t, db, "belongs_to", esxi.ID, cluster.ID); n != 1 {
		t.Fatalf("期望 1 条 esxi_host.belongs_to→esxi_cluster，实际 %d", n)
	}
	// 幂等：重复触发 + ESXi 侧反向触发均不产生重复。
	if err := l.Handle(context.Background(), vm.ID, "update"); err != nil {
		t.Fatalf("重复触发失败: %v", err)
	}
	if err := l.Handle(context.Background(), esxi.ID, "create"); err != nil {
		t.Fatalf("ESXi 侧触发失败: %v", err)
	}
	if n := countRelations(t, db, "runs_on", vm.ID, esxi.ID); n != 1 {
		t.Fatalf("幂等失败：vm.runs_on 期望 1 条，实际 %d", n)
	}
	if n := countRelations(t, db, "belongs_to", esxi.ID, cluster.ID); n != 1 {
		t.Fatalf("幂等失败：belongs_to 期望 1 条，实际 %d", n)
	}
}

func TestLinkMissingModelTolerated(t *testing.T) {
	db := setup(t) // 不建 esxi_host/esxi_cluster 模型
	vm := mustCI(t, db, "virtual_machine", map[string]any{
		"instance_uuid": "uuid-004", "name": "vm-x",
		"parent_host_uuid": "hw-absent", "parent_cluster_moid": "moid-absent",
	})
	l := New(db)
	// 目标模型缺失：跳过不报错，也不应产生任何关系。
	if err := l.Handle(context.Background(), vm.ID, "create"); err != nil {
		t.Fatalf("模型缺失时不应报错，实际: %v", err)
	}
	if n := countRelations(t, db, "runs_on", "", ""); n != 0 {
		t.Fatalf("模型缺失时不应建关系，实际 %d", n)
	}
	if n := countRelations(t, db, "belongs_to", "", ""); n != 0 {
		t.Fatalf("模型缺失时不应建关系，实际 %d", n)
	}
}

func TestLinkMissingRelationDefTolerated(t *testing.T) {
	db := setup(t)
	// esxi_host 模型存在但未定义 belongs_to 关系。
	esxiHostModel := store.Model{Name: "ESXi 主机", Code: "esxi_host"}
	esxiClusterModel := store.Model{Name: "ESXi 集群", Code: "esxi_cluster"}
	for _, m := range []*store.Model{&esxiHostModel, &esxiClusterModel} {
		if err := db.Create(m).Error; err != nil {
			t.Fatalf("创建模型失败: %v", err)
		}
	}
	mustCI(t, db, "esxi_cluster", map[string]any{"moid": "domain-c200"})
	mustCI(t, db, "esxi_host", map[string]any{"hardware_uuid": "hw-uuid-02"})
	vm := mustCI(t, db, "virtual_machine", map[string]any{
		"instance_uuid": "uuid-005", "parent_host_uuid": "hw-uuid-02", "parent_cluster_moid": "domain-c200",
	})
	l := New(db)
	// belongs_to 关系定义缺失：跳过不报错；runs_on（vm 模型已定义）正常建。
	if err := l.Handle(context.Background(), vm.ID, "create"); err != nil {
		t.Fatalf("关系定义缺失时不应报错，实际: %v", err)
	}
	if n := countRelations(t, db, "belongs_to", "", ""); n != 0 {
		t.Fatalf("关系定义缺失时不应建 belongs_to，实际 %d", n)
	}
	if n := countRelations(t, db, "runs_on", vm.ID, ""); n != 1 {
		t.Fatalf("runs_on 应正常建立，实际 %d", n)
	}
}

func TestLinkUnknownModelNoop(t *testing.T) {
	db := setup(t)
	ci := mustCI(t, db, "host", map[string]any{"ident": "bare", "ip": "10.9.9.9"}) // 无 instance_uuid
	l := New(db)
	if err := l.Handle(context.Background(), ci.ID, "create"); err != nil {
		t.Fatalf("无匹配属性时不应报错: %v", err)
	}
	if n := countRelations(t, db, "instantiated_by", "", ""); n != 0 {
		t.Fatalf("不应建关系，实际 %d", n)
	}
}

// TestLinkEsxiClusterHostFirst 采集器真实顺序：ESXi 主机先于集群建档（主机记录携带
// parent_cluster_moid，VM 记录不携带）——集群建档时必须反向补挂 belongs_to。
func TestLinkEsxiClusterHostFirst(t *testing.T) {
	db := setup(t)
	esxiHostModel := store.Model{
		Name: "ESXi 主机", Code: "esxi_host",
		Relations: datatypes.NewJSONType([]store.RelationDefinition{
			{Name: "属于", Code: "belongs_to", TargetModel: "esxi_cluster", Cardinality: "one_to_one", Direction: "outgoing"},
		}),
	}
	esxiClusterModel := store.Model{Name: "ESXi 集群", Code: "esxi_cluster"}
	for _, m := range []*store.Model{&esxiHostModel, &esxiClusterModel} {
		if err := db.Create(m).Error; err != nil {
			t.Fatalf("创建模型失败: %v", err)
		}
	}
	// 主机先建档（携带集群归属），此时集群不存在
	esxi := mustCI(t, db, "esxi_host", map[string]any{"hardware_uuid": "hw-uuid-02", "name": "esxi-02", "parent_cluster_moid": "domain-c200"})
	l := New(db)
	if err := l.Handle(context.Background(), esxi.ID, "create"); err != nil {
		t.Fatalf("ESXi 侧关联失败: %v", err)
	}
	// 集群后建档 → 反向补挂
	cluster := mustCI(t, db, "esxi_cluster", map[string]any{"moid": "domain-c200", "name": "容灾集群"})
	if err := l.Handle(context.Background(), cluster.ID, "create"); err != nil {
		t.Fatalf("集群侧关联失败: %v", err)
	}
	if n := countRelations(t, db, "belongs_to", esxi.ID, cluster.ID); n != 1 {
		t.Fatalf("集群后建档应反向补挂 belongs_to，实际 %d", n)
	}
}

// TestLinkEsxiHostClusterDirect 集群先建档、主机后建档：主机侧按自身
// parent_cluster_moid 直接挂接。
func TestLinkEsxiHostClusterDirect(t *testing.T) {
	db := setup(t)
	esxiHostModel := store.Model{
		Name: "ESXi 主机", Code: "esxi_host",
		Relations: datatypes.NewJSONType([]store.RelationDefinition{
			{Name: "属于", Code: "belongs_to", TargetModel: "esxi_cluster", Cardinality: "one_to_one", Direction: "outgoing"},
		}),
	}
	esxiClusterModel := store.Model{Name: "ESXi 集群", Code: "esxi_cluster"}
	for _, m := range []*store.Model{&esxiHostModel, &esxiClusterModel} {
		if err := db.Create(m).Error; err != nil {
			t.Fatalf("创建模型失败: %v", err)
		}
	}
	cluster := mustCI(t, db, "esxi_cluster", map[string]any{"moid": "domain-c300", "name": "边缘集群"})
	esxi := mustCI(t, db, "esxi_host", map[string]any{"hardware_uuid": "hw-uuid-03", "name": "esxi-03", "parent_cluster_moid": "domain-c300"})
	l := New(db)
	if err := l.Handle(context.Background(), esxi.ID, "create"); err != nil {
		t.Fatalf("ESXi 侧关联失败: %v", err)
	}
	if n := countRelations(t, db, "belongs_to", esxi.ID, cluster.ID); n != 1 {
		t.Fatalf("主机应按自身 parent_cluster_moid 挂接集群，实际 %d", n)
	}
}
