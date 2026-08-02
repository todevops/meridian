// DCIM 单测：U 位区间重叠纯函数 + 机柜容量读取 + 上下架分支（含 409 冲突）。
package dcim

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/datatypes"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"cmdb/server/internal/store"
)

// setup 打开独立内存库，预置 rack / server 两个模型。
func setup(t *testing.T) (*gorm.DB, *Service, store.Model, store.Model) {
	t.Helper()
	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared", t.Name())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("打开内存库失败: %v", err)
	}
	if err := db.AutoMigrate(store.AllModels()...); err != nil {
		t.Fatalf("迁移失败: %v", err)
	}
	rackModel := store.Model{
		Name: "机柜", Code: "rack",
		Attributes: datatypes.NewJSONType([]store.AttributeDefinition{
			{Name: "机柜编号", Code: "name", Type: "string", Required: true},
		}),
	}
	serverModel := store.Model{
		Name: "物理机", Code: "physical_server",
		Attributes: datatypes.NewJSONType([]store.AttributeDefinition{
			{Name: "主机名", Code: "hostname", Type: "string"},
		}),
	}
	if err := db.Create(&rackModel).Error; err != nil {
		t.Fatalf("创建机柜模型失败: %v", err)
	}
	if err := db.Create(&serverModel).Error; err != nil {
		t.Fatalf("创建物理机模型失败: %v", err)
	}
	return db, NewService(db), rackModel, serverModel
}

// mustCI 创建测试用 CI。
func mustCI(t *testing.T, db *gorm.DB, modelID string, attrs map[string]any) store.CI {
	t.Helper()
	ci := store.CI{
		ModelID:      modelID,
		Attributes:   datatypes.JSONMap(attrs),
		FieldSources: datatypes.JSONMap{},
		Status:       "active",
		Source:       "manual",
	}
	if err := db.Create(&ci).Error; err != nil {
		t.Fatalf("创建 CI 失败: %v", err)
	}
	return ci
}

func TestOverlapU(t *testing.T) {
	cases := []struct {
		posA, hA, posB, hB int
		want               bool
	}{
		{1, 2, 2, 2, true},   // 区间相交
		{1, 2, 3, 2, false},  // 相邻不重叠
		{3, 2, 1, 2, false},  // 相邻（反向）
		{5, 4, 6, 1, true},   // 包含
		{6, 1, 5, 4, true},   // 被包含
		{10, 1, 10, 1, true}, // 同位
		{1, 1, 42, 1, false}, // 两端
	}
	for _, tc := range cases {
		if got := OverlapU(tc.posA, tc.hA, tc.posB, tc.hB); got != tc.want {
			t.Errorf("OverlapU(%d,%d,%d,%d) = %v，期望 %v", tc.posA, tc.hA, tc.posB, tc.hB, got, tc.want)
		}
	}
}

func TestUTotalOf(t *testing.T) {
	// 未配置 u_total → 默认 42；配置后读属性。
	rack := store.CI{Attributes: datatypes.JSONMap{}}
	if got := UTotalOf(rack); got != DefaultUTotal {
		t.Fatalf("期望默认 %d，得到 %d", DefaultUTotal, got)
	}
	rack.Attributes["u_total"] = float64(48)
	if got := UTotalOf(rack); got != 48 {
		t.Fatalf("期望 48，得到 %d", got)
	}
}

func TestMountUnitsUnmountFlow(t *testing.T) {
	db, s, rackModel, serverModel := setup(t)
	ctx := context.Background()
	rack := mustCI(t, db, rackModel.ID, map[string]any{"name": "A01"})
	srv1 := mustCI(t, db, serverModel.ID, map[string]any{"hostname": "srv-01"})
	srv2 := mustCI(t, db, serverModel.ID, map[string]any{"hostname": "srv-02"})

	// 非机柜 CI 作为路径目标 → ErrNotRack。
	if _, err := s.Mount(ctx, srv1.ID, srv2.ID, 1, 1); !errors.Is(err, ErrNotRack) {
		t.Fatalf("期望 ErrNotRack，得到 %v", err)
	}
	// 机柜不存在 → ErrRackNotFound。
	if _, err := s.Units(ctx, "nope"); !errors.Is(err, ErrRackNotFound) {
		t.Fatalf("期望 ErrRackNotFound，得到 %v", err)
	}
	// 设备不存在 → ErrDeviceNotFound。
	if _, err := s.Mount(ctx, rack.ID, "nope", 1, 1); !errors.Is(err, ErrDeviceNotFound) {
		t.Fatalf("期望 ErrDeviceNotFound，得到 %v", err)
	}
	// 区间越界（默认 42U）→ ErrInvalidRange。
	if _, err := s.Mount(ctx, rack.ID, srv1.ID, 42, 2); !errors.Is(err, ErrInvalidRange) {
		t.Fatalf("期望 ErrInvalidRange，得到 %v", err)
	}
	if _, err := s.Mount(ctx, rack.ID, srv1.ID, 0, 1); !errors.Is(err, ErrInvalidRange) {
		t.Fatalf("期望 ErrInvalidRange，得到 %v", err)
	}

	// 正常上架 srv1: U10 起 2U → 占 U10、U11。
	if _, err := s.Mount(ctx, rack.ID, srv1.ID, 10, 2); err != nil {
		t.Fatalf("上架失败: %v", err)
	}
	// 同设备重复上架 → ErrAlreadyMounted（HTTP 409）。
	if _, err := s.Mount(ctx, rack.ID, srv1.ID, 20, 1); !errors.Is(err, ErrAlreadyMounted) {
		t.Fatalf("期望 ErrAlreadyMounted，得到 %v", err)
	}
	// 区间重叠 → ErrOverlap（HTTP 409）：U11 起与 [10,12) 相交。
	if _, err := s.Mount(ctx, rack.ID, srv2.ID, 11, 2); !errors.Is(err, ErrOverlap) {
		t.Fatalf("期望 ErrOverlap，得到 %v", err)
	}
	// 相邻不重叠：U12 起 1U 正常。
	if _, err := s.Mount(ctx, rack.ID, srv2.ID, 12, 1); err != nil {
		t.Fatalf("相邻上架失败: %v", err)
	}

	// U 位视图：默认 42U，U10/U11 被 srv-01 占、U12 被 srv-02 占。
	units, err := s.Units(ctx, rack.ID)
	if err != nil {
		t.Fatalf("Units 失败: %v", err)
	}
	if units.RackID != rack.ID || units.UTotal != DefaultUTotal || len(units.Units) != DefaultUTotal {
		t.Fatalf("U 位总览不符: rack=%s total=%d len=%d", units.RackID, units.UTotal, len(units.Units))
	}
	assertUnit := func(u int, wantCI, wantName string) {
		t.Helper()
		unit := units.Units[u-1]
		if unit.U != u || unit.OccupantCIID != wantCI || unit.OccupantName != wantName {
			t.Fatalf("U%d 期望 (%s, %s)，得到 %+v", u, wantCI, wantName, unit)
		}
	}
	assertUnit(10, srv1.ID, "srv-01")
	assertUnit(11, srv1.ID, "srv-01")
	assertUnit(12, srv2.ID, "srv-02")
	assertUnit(13, "", "") // 空闲位

	// 下架 srv1 后 U10/U11 释放；重复下架 → ErrNotMounted。
	if err := s.Unmount(ctx, rack.ID, srv1.ID); err != nil {
		t.Fatalf("下架失败: %v", err)
	}
	if err := s.Unmount(ctx, rack.ID, srv1.ID); !errors.Is(err, ErrNotMounted) {
		t.Fatalf("期望 ErrNotMounted，得到 %v", err)
	}
	units, err = s.Units(ctx, rack.ID)
	if err != nil {
		t.Fatalf("Units 失败: %v", err)
	}
	assertUnit(10, "", "")
	assertUnit(12, srv2.ID, "srv-02")
}

func TestUnitsRespectsCustomUTotal(t *testing.T) {
	db, s, rackModel, _ := setup(t)
	ctx := context.Background()
	// 机柜 CI 配置 u_total=8：视图只覆盖 8U，且上架受该容量约束。
	rack := mustCI(t, db, rackModel.ID, map[string]any{"name": "B01", "u_total": float64(8)})
	dev := mustCI(t, db, rackModel.ID, map[string]any{"name": "B02"}) // 机柜也能作被挂设备（如 PDU）
	units, err := s.Units(ctx, rack.ID)
	if err != nil {
		t.Fatalf("Units 失败: %v", err)
	}
	if units.UTotal != 8 || len(units.Units) != 8 {
		t.Fatalf("期望 8U，得到 %+v", units)
	}
	if _, err := s.Mount(ctx, rack.ID, dev.ID, 8, 1); err != nil {
		t.Fatalf("U8 上架应成功: %v", err)
	}
	other := mustCI(t, db, rackModel.ID, map[string]any{"name": "B03"})
	if _, err := s.Mount(ctx, rack.ID, other.ID, 9, 1); !errors.Is(err, ErrInvalidRange) {
		t.Fatalf("期望 ErrInvalidRange，得到 %v", err)
	}
}
