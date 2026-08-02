// 容量总览聚合单测：机房分组、未分配机柜、U 位/电力合计、空模型容错。
package dcim

import (
	"context"
	"fmt"
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/datatypes"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"cmdb/server/internal/store"
)

func setupOverviewDB(t *testing.T) *gorm.DB {
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

func mustModel(t *testing.T, db *gorm.DB, code string) store.Model {
	t.Helper()
	m := store.Model{Name: code, Code: code}
	if err := db.Create(&m).Error; err != nil {
		t.Fatalf("创建模型失败: %v", err)
	}
	return m
}

func mustOverviewCI(t *testing.T, db *gorm.DB, modelID string, attrs map[string]any) store.CI {
	t.Helper()
	ci := store.CI{ModelID: modelID, Attributes: datatypes.JSONMap(attrs), Status: "active", Source: "manual"}
	if err := db.Create(&ci).Error; err != nil {
		t.Fatalf("创建 CI 失败: %v", err)
	}
	return ci
}

func TestOverviewEmptyWithoutModels(t *testing.T) {
	svc := NewService(setupOverviewDB(t))
	ov, err := svc.Overview(context.Background())
	if err != nil {
		t.Fatalf("模型缺失时不应报错: %v", err)
	}
	if ov.RackCount != 0 || len(ov.Rooms) != 0 {
		t.Fatalf("应为空总览: %+v", ov)
	}
}

func TestOverviewAggregation(t *testing.T) {
	db := setupOverviewDB(t)
	svc := NewService(db)
	roomModel := mustModel(t, db, "room")
	rackModel := mustModel(t, db, "rack")

	roomA := mustOverviewCI(t, db, roomModel.ID, map[string]any{"name": "亦庄机房", "code": "bj-yz", "address": "北京亦庄"})
	mustOverviewCI(t, db, roomModel.ID, map[string]any{"name": "空机房", "code": "empty"})

	// 机柜 1：机房 A，u_capacity=42U/10kW，挂 2U+4U（验证 u_capacity 优先）
	rack1 := mustOverviewCI(t, db, rackModel.ID, map[string]any{"name": "A01", "u_capacity": 42.0, "power_capacity_kw": 10.0})
	// 机柜 2：机房 A，默认 42U，无电力属性
	rack2 := mustOverviewCI(t, db, rackModel.ID, map[string]any{"name": "A02"})
	// 机柜 3：未分配机房，历史属性名 u_total=24U（验证兼容回退）
	rack3 := mustOverviewCI(t, db, rackModel.ID, map[string]any{"name": "X01", "u_total": 24.0, "power_capacity_kw": 5.0})
	// 退役机柜不计入
	old := mustOverviewCI(t, db, rackModel.ID, map[string]any{"name": "OLD"})
	db.Model(&old).Update("status", "retired")

	for _, rel := range []store.CIRelation{
		{RelationCode: "located_in", SrcCIID: rack1.ID, DstCIID: roomA.ID},
		{RelationCode: "located_in", SrcCIID: rack2.ID, DstCIID: roomA.ID},
	} {
		if err := db.Create(&rel).Error; err != nil {
			t.Fatalf("创建关系失败: %v", err)
		}
	}
	for _, m := range []store.RackMount{
		{RackCIID: rack1.ID, DeviceCIID: rack3.ID, UPosition: 1, UHeight: 2},
		{RackCIID: rack1.ID, DeviceCIID: rack3.ID, UPosition: 3, UHeight: 4},
		{RackCIID: rack3.ID, DeviceCIID: rack1.ID, UPosition: 1, UHeight: 1},
	} {
		if err := db.Create(&m).Error; err != nil {
			t.Fatalf("创建挂载失败: %v", err)
		}
	}

	ov, err := svc.Overview(context.Background())
	if err != nil {
		t.Fatalf("总览失败: %v", err)
	}

	if ov.RoomCount != 2 || ov.RackCount != 3 {
		t.Fatalf("机房/机柜计数不符: %+v", ov)
	}
	// 全局：42+42+24=108U，占用 6+1=7U，电力 15kW
	if ov.UTotal != 108 || ov.UUsed != 7 || ov.PowerCapacityKW != 15 {
		t.Fatalf("全局合计不符: %+v", ov)
	}

	var roomAStat *RoomStat
	emptySeen := false
	for i := range ov.Rooms {
		r := &ov.Rooms[i]
		if r.RoomID == roomA.ID {
			roomAStat = r
		}
		if r.Name == "空机房" && r.RackCount == 0 {
			emptySeen = true
		}
	}
	if roomAStat == nil {
		t.Fatal("缺少机房 A 聚合")
	}
	if roomAStat.Name != "亦庄机房" || roomAStat.Code != "bj-yz" || roomAStat.Address != "北京亦庄" {
		t.Fatalf("机房属性不符: %+v", roomAStat)
	}
	if roomAStat.RackCount != 2 || roomAStat.UTotal != 84 || roomAStat.UUsed != 6 || roomAStat.PowerCapacityKW != 10 {
		t.Fatalf("机房 A 聚合不符: %+v", roomAStat)
	}
	if !emptySeen {
		t.Fatal("空机房应出现在列表中")
	}

	// 逐机柜明细：rack1/rack2 归属机房 A，rack3 的 room_id 为 null。
	if len(ov.Racks) != 3 {
		t.Fatalf("机柜明细应为 3 条: %+v", ov.Racks)
	}
	var rack1Stat, rack3Stat *RackStat
	for i := range ov.Racks {
		r := &ov.Racks[i]
		switch r.RackID {
		case rack1.ID:
			rack1Stat = r
		case rack3.ID:
			rack3Stat = r
		}
	}
	if rack1Stat == nil || rack1Stat.RoomID == nil || *rack1Stat.RoomID != roomA.ID || rack1Stat.UUsed != 6 {
		t.Fatalf("rack1 明细不符: %+v", rack1Stat)
	}
	if rack3Stat == nil || rack3Stat.RoomID != nil || rack3Stat.UTotal != 24 {
		t.Fatalf("rack3 明细不符: %+v", rack3Stat)
	}

	if ov.Unassigned.RackCount != 1 || ov.Unassigned.UTotal != 24 || ov.Unassigned.UUsed != 1 || ov.Unassigned.PowerCapacityKW != 5 {
		t.Fatalf("未分配机柜聚合不符: %+v", ov.Unassigned)
	}
}
