// 拓扑域单测：network_link 内建调和（四元组幂等/auto 建链/manual 保护）、
// 拓扑图双向互证合并去重、主机接入定位正反例。
package topology

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/datatypes"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"meridian/server/internal/reconcile"
	"meridian/server/internal/store"
)

// setup 打开独立内存库并预置 network_device/host/rack/room 模型。
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
	for _, m := range []store.Model{
		{Name: "网络设备", Code: "network_device"},
		{Name: "主机", Code: "host"},
		{Name: "机柜", Code: "rack"},
		{Name: "机房", Code: "room"},
	} {
		if err := db.Create(&m).Error; err != nil {
			t.Fatalf("创建模型 %s 失败: %v", m.Code, err)
		}
	}
	return db
}

// mustCI 直接落库一个 CI。
func mustCI(t *testing.T, db *gorm.DB, modelCode string, attrs map[string]any) store.CI {
	t.Helper()
	var model store.Model
	if err := db.First(&model, "code = ?", modelCode).Error; err != nil {
		t.Fatalf("加载模型 %s 失败: %v", modelCode, err)
	}
	ci := store.CI{ModelID: model.ID, Attributes: datatypes.JSONMap(attrs), Status: "active", Source: "manual"}
	if err := db.Create(&ci).Error; err != nil {
		t.Fatalf("创建 CI 失败: %v", err)
	}
	return ci
}

// ingestLink 模拟摄入管道：内建调和 + 原始层落库（顺序与 discovery.Pipeline 一致）。
func ingestLink(t *testing.T, db *gorm.DB, attrs map[string]any) reconcile.Decision {
	t.Helper()
	rec := reconcile.Record{
		Source:         "librenms",
		Collector:      "librenms-link-collector",
		ModelCandidate: "network_link",
		Attributes:     attrs,
		OccurredAt:     time.Now(),
	}
	d, err := New(db).HandleLinkRecord(context.Background(), rec, false)
	if err != nil {
		t.Fatalf("链路记录调和失败: %v", err)
	}
	raw, _ := json.Marshal(rec)
	rawRec := store.DiscoveryRawRecord{
		Source: rec.Source, Collector: rec.Collector, ModelCandidate: rec.ModelCandidate,
		OccurredAt: rec.OccurredAt, ReceivedAt: time.Now(), ResultAction: d.Action,
	}
	_ = json.Unmarshal(raw, &rawRec.Payload)
	if err := db.Create(&rawRec).Error; err != nil {
		t.Fatalf("写入原始层失败: %v", err)
	}
	return d
}

// linkAttrsOf 构造一条 LLDP 链路记录属性。
func linkAttrsOf(local, lport, remote, rport string) map[string]any {
	return map[string]any{
		"local_device": local, "local_port": lport,
		"remote_device": remote, "remote_port": rport, "protocol": "lldp",
	}
}

// countRelations 统计两端间 connected_to 关系条数（任一方向）。
func countRelations(t *testing.T, db *gorm.DB, a, b string) []store.CIRelation {
	t.Helper()
	var rels []store.CIRelation
	if err := db.Where(
		"relation_code = ? AND ((src_ci_id = ? AND dst_ci_id = ?) OR (src_ci_id = ? AND dst_ci_id = ?))",
		"connected_to", a, b, b, a).Find(&rels).Error; err != nil {
		t.Fatalf("查询关系失败: %v", err)
	}
	return rels
}

// TestLinkRecordAutoRelation 验证链路记录自动建 auto 关系且四元组幂等。
func TestLinkRecordAutoRelation(t *testing.T) {
	db := setup(t)
	sw1 := mustCI(t, db, "network_device", map[string]any{"name": "sw-01", "mgmt_ip": "10.0.0.1"})
	sw2 := mustCI(t, db, "network_device", map[string]any{"name": "sw-02", "mgmt_ip": "10.0.0.2"})

	d := ingestLink(t, db, linkAttrsOf("sw-01", "Gi0/1", "sw-02", "Gi0/1"))
	if d.Action != reconcile.ActionCreate {
		t.Fatalf("首次上报应为 create，实际 %s", d.Action)
	}
	rels := countRelations(t, db, sw1.ID, sw2.ID)
	if len(rels) != 1 || rels[0].Source != store.RelationSourceAuto {
		t.Fatalf("应有 1 条 auto 关系，实际 %+v", rels)
	}

	// 重复上报：四元组命中存量原始记录 → update 幂等，不重复建关系。
	d = ingestLink(t, db, linkAttrsOf("sw-01", "Gi0/1", "sw-02", "Gi0/1"))
	if d.Action != reconcile.ActionUpdate {
		t.Fatalf("重复上报应为 update，实际 %s", d.Action)
	}
	if rels := countRelations(t, db, sw1.ID, sw2.ID); len(rels) != 1 {
		t.Fatalf("重复上报不应新增关系，实际 %d 条", len(rels))
	}
}

// TestLinkRecordManualProtection 验证 manual 关系永不被自动链路覆盖。
func TestLinkRecordManualProtection(t *testing.T) {
	db := setup(t)
	sw1 := mustCI(t, db, "network_device", map[string]any{"name": "sw-01", "mgmt_ip": "10.0.0.1"})
	sw2 := mustCI(t, db, "network_device", map[string]any{"name": "sw-02", "mgmt_ip": "10.0.0.2"})
	// 人工建联（反向，source=manual）。
	if err := db.Create(&store.CIRelation{
		RelationCode: "connected_to", SrcCIID: sw2.ID, DstCIID: sw1.ID, Source: store.RelationSourceManual,
	}).Error; err != nil {
		t.Fatalf("创建人工关系失败: %v", err)
	}

	d := ingestLink(t, db, linkAttrsOf("sw-01", "Gi0/1", "sw-02", "Gi0/1"))
	rels := countRelations(t, db, sw1.ID, sw2.ID)
	if len(rels) != 1 || rels[0].Source != store.RelationSourceManual {
		t.Fatalf("manual 关系不得被覆盖或新增 auto 关系，实际 %+v", rels)
	}
	joined := ""
	for _, r := range d.Reasons {
		joined += r
	}
	if !strings.Contains(joined, "不覆盖") {
		t.Fatalf("判定理由应说明不覆盖 manual 关系，实际 %v", d.Reasons)
	}
}

// TestGraphMergesBidirectional 验证双向互证的两条记录合并为一条边。
func TestGraphMergesBidirectional(t *testing.T) {
	db := setup(t)
	mustCI(t, db, "network_device", map[string]any{"name": "sw-01", "mgmt_ip": "10.0.0.1"})
	mustCI(t, db, "network_device", map[string]any{"name": "sw-02", "mgmt_ip": "10.0.0.2"})
	mustCI(t, db, "network_device", map[string]any{"name": "sw-03", "mgmt_ip": "10.0.0.3"})
	ingestLink(t, db, linkAttrsOf("sw-01", "Gi0/1", "sw-02", "Gi0/1"))
	ingestLink(t, db, linkAttrsOf("sw-02", "Gi0/1", "sw-01", "Gi0/1")) // 反向互证
	ingestLink(t, db, linkAttrsOf("sw-02", "Gi0/2", "sw-03", "Gi0/9")) // 单向未互证

	g, err := New(db).Graph(context.Background())
	if err != nil {
		t.Fatalf("组装拓扑图失败: %v", err)
	}
	if len(g.Edges) != 2 {
		t.Fatalf("双向互证应合并为一条边（共 2 条），实际 %d 条: %+v", len(g.Edges), g.Edges)
	}
	for _, e := range g.Edges {
		if e.Source != store.RelationSourceAuto {
			t.Errorf("链路记录边 source 应为 auto，实际 %s", e.Source)
		}
	}
	// 节点含全部三台设备，名称/模型编码正确。
	if len(g.Nodes) != 3 {
		t.Fatalf("应有 3 个节点，实际 %d", len(g.Nodes))
	}
	names := map[string]Node{}
	for _, n := range g.Nodes {
		names[n.Name] = n
		if n.ModelCode != "network_device" {
			t.Errorf("节点 model_code 应为 network_device，实际 %s", n.ModelCode)
		}
	}
	if _, ok := names["sw-01"]; !ok {
		t.Errorf("缺少节点 sw-01: %+v", g.Nodes)
	}
}

// TestGraphRoomResolution 验证节点机房沿 located_in 两跳解析。
func TestGraphRoomResolution(t *testing.T) {
	db := setup(t)
	room := mustCI(t, db, "room", map[string]any{"name": "北京-DC1"})
	rack := mustCI(t, db, "rack", map[string]any{"name": "R01"})
	sw := mustCI(t, db, "network_device", map[string]any{"name": "sw-01", "mgmt_ip": "10.0.0.1"})
	db.Create(&store.CIRelation{RelationCode: "located_in", SrcCIID: rack.ID, DstCIID: room.ID, Source: "manual"})
	db.Create(&store.CIRelation{RelationCode: "located_in", SrcCIID: sw.ID, DstCIID: rack.ID, Source: "manual"})

	g, err := New(db).Graph(context.Background())
	if err != nil {
		t.Fatalf("组装拓扑图失败: %v", err)
	}
	if len(g.Nodes) != 1 || g.Nodes[0].Room != "北京-DC1" {
		t.Fatalf("节点机房应为 北京-DC1，实际 %+v", g.Nodes)
	}
}

// TestHostLocation 验证主机接入定位正例与反例。
func TestHostLocation(t *testing.T) {
	db := setup(t)
	mustCI(t, db, "network_device", map[string]any{"name": "sw-01", "mgmt_ip": "10.0.0.1"})
	mustCI(t, db, "host", map[string]any{"ident": "vm-web-01", "ip": "10.1.0.5", "mac": "52:54:00:AB:CD:EF"})
	attrs := linkAttrsOf("sw-01", "Gi0/5", "vm-web-01", "eth0")
	attrs["remote_mac"] = "5254.00ab.cdef" // 不同书写形态，归一化后应命中
	ingestLink(t, db, attrs)

	svc := New(db)
	// 正例：IP 命中。
	loc, err := svc.HostLocation(context.Background(), "10.1.0.5")
	if err != nil {
		t.Fatalf("定位失败: %v", err)
	}
	if loc == nil {
		t.Fatal("应定位成功，实际无命中")
	}
	if loc.Switch != "sw-01" || loc.Port != "Gi0/5" || loc.Protocol != "lldp" {
		t.Errorf("定位结果不符: %+v", loc)
	}
	if loc.MAC != "52:54:00:AB:CD:EF" {
		t.Errorf("MAC 应保留 CI 原始形态，实际 %s", loc.MAC)
	}

	// 反例一：未知 IP。
	if loc, _ := svc.HostLocation(context.Background(), "10.9.9.9"); loc != nil {
		t.Errorf("未知 IP 应无命中，实际 %+v", loc)
	}
	// 反例二：IP 有 MAC 但无链路记录。
	mustCI(t, db, "host", map[string]any{"ident": "vm-db-01", "ip": "10.1.0.6", "mac": "52:54:00:00:00:06"})
	if loc, _ := svc.HostLocation(context.Background(), "10.1.0.6"); loc != nil {
		t.Errorf("无链路记录应无命中，实际 %+v", loc)
	}
}

// TestHostLocationViaRawRecord 验证经 ip_scan 原始记录（无 host CI）定位。
func TestHostLocationViaRawRecord(t *testing.T) {
	db := setup(t)
	mustCI(t, db, "network_device", map[string]any{"name": "sw-01", "mgmt_ip": "10.0.0.1"})
	// ip_scan 原始记录：model_candidate=host，payload 携带 ip+mac。
	raw, _ := json.Marshal(reconcile.Record{
		Source: "ip_scan", Collector: "ip-scanner", ModelCandidate: "host",
		Attributes: map[string]any{"ip": "10.80.0.9", "mac": "52:54:00:00:00:09"},
		OccurredAt: time.Now(),
	})
	rawRec := store.DiscoveryRawRecord{Source: "ip_scan", Collector: "ip-scanner", ModelCandidate: "host", OccurredAt: time.Now(), ReceivedAt: time.Now()}
	_ = json.Unmarshal(raw, &rawRec.Payload)
	if err := db.Create(&rawRec).Error; err != nil {
		t.Fatalf("写入原始记录失败: %v", err)
	}
	attrs := linkAttrsOf("sw-01", "Gi0/7", "black-box", "eth0")
	attrs["remote_mac"] = "52:54:00:00:00:09"
	ingestLink(t, db, attrs)

	loc, err := New(db).HostLocation(context.Background(), "10.80.0.9")
	if err != nil || loc == nil {
		t.Fatalf("经原始记录应定位成功: loc=%+v err=%v", loc, err)
	}
	if loc.Switch != "sw-01" || loc.Port != "Gi0/7" {
		t.Errorf("定位结果不符: %+v", loc)
	}
}
