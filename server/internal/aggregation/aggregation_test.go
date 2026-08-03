// 应用聚合查询单测（F-027）：两级业务树、一屏聚合（含 K8s 两跳链路）、
// 依赖拓扑（两跳）与影响面反查（入向两跳路径）。
package aggregation

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

// fixture 预置的测试数据标识。
type fixture struct {
	db      *gorm.DB
	svc     *Service
	lineID  string // 业务线：电商
	appID   string // 应用：商城前台（归属电商）
	app2ID  string // 应用：订单中心（归属电商，depends_on 商城前台的数据库）
	app3ID  string // 应用：无归属应用（进 unassigned）
	hostID  string // 主机 web-01（商城前台 deployed_on）
	host2ID string // 云主机 cloud-01（host_type=cloud，商城前台 deployed_on）
	dbID    string // MySQL 实例（商城前台 depends_on，runs_on web-01）
	nsID    string // 命名空间 mall（mounted_to 商城前台）
	wlID    string // 工作负载 mall-deploy（in_namespace mall）
	wl2ID   string // 工作负载 direct-deploy（直接 belongs_to 商城前台）
}

// setup 打开内存库并预置模型/CI/关系：覆盖树汇总、两跳 K8s 链、依赖拓扑与影响面全路径。
func setup(t *testing.T) *fixture {
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
		{Name: "业务线", Code: "biz_line"},
		{Name: "应用系统", Code: "biz_app"},
		{Name: "主机", Code: "host"},
		{Name: "数据库实例", Code: "db_instance"},
		{Name: "K8s 命名空间", Code: "k8s_namespace"},
		{Name: "K8s 工作负载", Code: "k8s_workload"},
	}
	for i := range models {
		if err := db.Create(&models[i]).Error; err != nil {
			t.Fatalf("创建模型 %s 失败: %v", models[i].Code, err)
		}
	}
	f := &fixture{db: db, svc: NewService(db)}
	mustCI := func(modelCode string, attrs map[string]any) store.CI {
		t.Helper()
		var model store.Model
		if err := db.First(&model, "code = ?", modelCode).Error; err != nil {
			t.Fatalf("模型 %s 不存在: %v", modelCode, err)
		}
		ci := store.CI{ModelID: model.ID, Attributes: datatypes.JSONMap(attrs), Status: "active", Source: "manual"}
		if err := db.Create(&ci).Error; err != nil {
			t.Fatalf("创建 CI 失败: %v", err)
		}
		return ci
	}
	mustRel := func(code string, src, dst store.CI) {
		t.Helper()
		if err := db.Create(&store.CIRelation{
			RelationCode: code, SrcCIID: src.ID, DstCIID: dst.ID, Source: store.RelationSourceAuto,
		}).Error; err != nil {
			t.Fatalf("创建关系 %s 失败: %v", code, err)
		}
	}

	line := mustCI("biz_line", map[string]any{"code": "ec", "name": "电商平台", "owner": "张三", "level": "critical"})
	app := mustCI("biz_app", map[string]any{"code": "mall-front", "name": "商城前台", "owner": "李四", "level": "L1"})
	app2 := mustCI("biz_app", map[string]any{"code": "order-center", "name": "订单中心", "owner": "王五", "level": "L2"})
	app3 := mustCI("biz_app", map[string]any{"code": "orphan", "name": "无归属应用", "owner": "赵六", "level": "L3"})
	host := mustCI("host", map[string]any{"ident": "web-01", "ip": "10.0.1.11", "host_type": "virtual_machine"})
	host2 := mustCI("host", map[string]any{"ident": "cloud-01", "ip": "10.0.1.12", "host_type": "cloud", "provider": "aliyun", "spec": "ecs.g7.large", "zone": "cn-hangzhou-i"})
	dbi := mustCI("db_instance", map[string]any{"instance_addr": "10.0.1.11:3306", "version": "8.0.36", "role": "master"})
	ns := mustCI("k8s_namespace", map[string]any{"cluster": "prod", "name": "mall"})
	wl := mustCI("k8s_workload", map[string]any{"uid": "prod/mall/Deployment/mall-deploy", "cluster": "prod", "namespace": "mall", "kind": "Deployment", "name": "mall-deploy"})
	wl2 := mustCI("k8s_workload", map[string]any{"uid": "prod/tools/Deployment/direct-deploy", "cluster": "prod", "namespace": "tools", "kind": "Deployment", "name": "direct-deploy"})

	mustRel("belongs_to", app, line)   // 应用 → 业务线
	mustRel("belongs_to", app2, line)  // 应用2 → 业务线
	mustRel("deployed_on", app, host)  // 应用 → 主机
	mustRel("deployed_on", app, host2) // 应用 → 云主机
	mustRel("depends_on", app, dbi)    // 应用 → 数据库
	mustRel("depends_on", app2, dbi)   // 应用2 → 同一数据库（影响面两跳素材）
	mustRel("runs_on", dbi, host)      // 数据库 → 主机（影响面 host→db→app 素材）
	mustRel("mounted_to", ns, app)     // 命名空间 → 应用
	mustRel("in_namespace", wl, ns)    // 工作负载 → 命名空间
	mustRel("belongs_to", wl2, app)    // 工作负载直接归属应用

	// IPAM 前缀：10.0.1.0/24 覆盖两台主机。
	if err := db.Create(&store.IPPrefix{CIDR: "10.0.1.0/24", Name: "业务段"}).Error; err != nil {
		t.Fatalf("创建前缀失败: %v", err)
	}

	f.lineID, f.appID, f.app2ID, f.app3ID = line.ID, app.ID, app2.ID, app3.ID
	f.hostID, f.host2ID, f.dbID = host.ID, host2.ID, dbi.ID
	f.nsID, f.wlID, f.wl2ID = ns.ID, wl.ID, wl2.ID
	return f
}

// TestTree 验证两级业务树：应用数/主机数汇总与 unassigned 组。
func TestTree(t *testing.T) {
	f := setup(t)
	view, err := f.svc.Tree(context.Background())
	if err != nil {
		t.Fatalf("Tree 失败: %v", err)
	}
	if len(view.Lines) != 1 {
		t.Fatalf("业务线数 = %d，期望 1", len(view.Lines))
	}
	line := view.Lines[0]
	if line.Name != "电商平台" || line.Owner != "张三" || line.Level != "critical" {
		t.Errorf("业务线字段不符: %+v", line.AppBrief)
	}
	if line.AppCount != 2 || len(line.Apps) != 2 {
		t.Errorf("应用数 = %d（%d），期望 2", line.AppCount, len(line.Apps))
	}
	if line.HostCount != 2 {
		t.Errorf("主机数汇总 = %d，期望 2（web-01 + cloud-01）", line.HostCount)
	}
	if len(view.Unassigned) != 1 || view.Unassigned[0].Code != "orphan" {
		t.Errorf("unassigned 组不符: %+v", view.Unassigned)
	}
}

// TestAggregate 验证一屏聚合：主机/数据库/K8s 两跳链/IP 去重含前缀/云资源。
func TestAggregate(t *testing.T) {
	f := setup(t)
	view, err := f.svc.Aggregate(context.Background(), f.appID)
	if err != nil {
		t.Fatalf("Aggregate 失败: %v", err)
	}
	if view.App.Code != "mall-front" || view.App.Owner != "李四" || view.App.Level != "L1" {
		t.Errorf("应用字段不符: %+v", view.App)
	}
	if len(view.Hosts) != 2 {
		t.Fatalf("主机数 = %d，期望 2", len(view.Hosts))
	}
	if view.Hosts[0].Ident != "cloud-01" && view.Hosts[1].Ident != "cloud-01" {
		t.Errorf("主机清单缺 cloud-01: %+v", view.Hosts)
	}
	if len(view.DBInstances) != 1 || view.DBInstances[0].InstanceAddr != "10.0.1.11:3306" {
		t.Errorf("数据库清单不符: %+v", view.DBInstances)
	}
	// K8s 两跳：mall-deploy 经命名空间链（via_namespace=true），
	// direct-deploy 直接 belongs_to（via_namespace=false）。
	if len(view.K8sWorkloads) != 2 {
		t.Fatalf("工作负载数 = %d，期望 2", len(view.K8sWorkloads))
	}
	via := map[string]bool{}
	for _, w := range view.K8sWorkloads {
		via[w.Name] = w.ViaNamespace
	}
	if !via["mall-deploy"] {
		t.Errorf("mall-deploy 应经命名空间链（via_namespace=true）: %+v", view.K8sWorkloads)
	}
	if via["direct-deploy"] {
		t.Errorf("direct-deploy 应为直接归属（via_namespace=false）: %+v", view.K8sWorkloads)
	}
	// IP：两台主机两个 IP，均落在 10.0.1.0/24。
	if len(view.IPs) != 2 {
		t.Fatalf("IP 数 = %d，期望 2", len(view.IPs))
	}
	for _, ip := range view.IPs {
		if ip.Prefix != "10.0.1.0/24" {
			t.Errorf("IP %s 前缀 = %q，期望 10.0.1.0/24", ip.IP, ip.Prefix)
		}
	}
	// 云资源：仅 cloud-01。
	if len(view.Clouds) != 1 || view.Clouds[0].Provider != "aliyun" || view.Clouds[0].Zone != "cn-hangzhou-i" {
		t.Errorf("云资源清单不符: %+v", view.Clouds)
	}
}

// TestDependencies 验证依赖拓扑：两跳以内、节点限定应用与数据库、边含关系码。
func TestDependencies(t *testing.T) {
	f := setup(t)
	view, err := f.svc.Dependencies(context.Background(), f.appID)
	if err != nil {
		t.Fatalf("Dependencies 失败: %v", err)
	}
	// 一跳：商城前台→MySQL；二跳：订单中心→MySQL（经 MySQL 展开）。
	nodeTypes := map[string]string{}
	for _, n := range view.Nodes {
		nodeTypes[n.Label] = n.Type
	}
	if nodeTypes["商城前台"] != "biz_app" || nodeTypes["订单中心"] != "biz_app" || nodeTypes["10.0.1.11:3306"] != "db_instance" {
		t.Errorf("节点集合不符: %+v", view.Nodes)
	}
	if len(view.Edges) != 2 {
		t.Fatalf("边数 = %d，期望 2", len(view.Edges))
	}
	for _, e := range view.Edges {
		if e.Code != "depends_on" {
			t.Errorf("边关系码 = %q，期望 depends_on", e.Code)
		}
	}
	// 孤立应用：单节点无边。
	solo, err := f.svc.Dependencies(context.Background(), f.app3ID)
	if err != nil {
		t.Fatalf("孤立应用 Dependencies 失败: %v", err)
	}
	if len(solo.Nodes) != 1 || len(solo.Edges) != 0 {
		t.Errorf("孤立应用应为单节点空边: %+v", solo)
	}
}

// TestImpact 验证影响面反查：主机一跳直达应用、经数据库两跳到达另一应用，路径完整。
func TestImpact(t *testing.T) {
	f := setup(t)
	// 从主机 web-01 反查：deployed_on 一跳到商城前台；runs_on 一跳到 MySQL，
	// 再 depends_on 两跳到商城前台与订单中心。
	view, err := f.svc.Impact(context.Background(), f.hostID)
	if err != nil {
		t.Fatalf("Impact 失败: %v", err)
	}
	names := map[string][]string{}
	for _, item := range view.Affected {
		names[item.AppName] = item.Path
	}
	if len(names) != 2 {
		t.Fatalf("受影响应用数 = %d，期望 2: %+v", len(names), view.Affected)
	}
	p1 := names["商城前台"]
	if len(p1) != 3 || p1[0] != "host:web-01" || p1[1] != "deployed_on" || p1[2] != "biz_app:商城前台" {
		t.Errorf("商城前台一跳路径不符: %v", p1)
	}
	p2 := names["订单中心"]
	if len(p2) != 5 || p2[0] != "host:web-01" || p2[1] != "runs_on" ||
		p2[2] != "db_instance:10.0.1.11:3306" || p2[3] != "depends_on" || p2[4] != "biz_app:订单中心" {
		t.Errorf("订单中心两跳路径不符: %v", p2)
	}

	// 从数据库反查：两应用各一跳。
	dbView, err := f.svc.Impact(context.Background(), f.dbID)
	if err != nil {
		t.Fatalf("数据库 Impact 失败: %v", err)
	}
	if len(dbView.Affected) != 2 {
		t.Errorf("数据库受影响应用数 = %d，期望 2", len(dbView.Affected))
	}
}

// TestAggregateNotApp 验证非应用 CI 的聚合请求被拒绝。
func TestAggregateNotApp(t *testing.T) {
	f := setup(t)
	if _, err := f.svc.Aggregate(context.Background(), f.hostID); err != ErrNotApp {
		t.Errorf("err = %v，期望 ErrNotApp", err)
	}
	if _, err := f.svc.Aggregate(context.Background(), "no-such-id"); err == nil {
		t.Error("不存在的 CI 应返回错误")
	}
}
