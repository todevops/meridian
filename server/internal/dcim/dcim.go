// Package dcim 实现 DCIM 模块核心逻辑：机柜 U 位视图与设备上下架。
// 机柜本体是模型 code=rack 的 CI，U 位容量读 CI 属性 u_capacity（默认 42）；
// 挂载关系落在独立的 rack_mounts 表，区间 [u_position, u_position+u_height) 不允许重叠。
package dcim

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"gorm.io/gorm"

	"meridian/server/internal/store"
)

// 业务错误（HTTP 层据此映射状态码）。
var (
	ErrNotRack        = errors.New("目标 CI 不是机柜（模型 code=rack）")
	ErrRackNotFound   = errors.New("机柜 CI 不存在")
	ErrDeviceNotFound = errors.New("设备 CI 不存在")
	ErrInvalidRange   = errors.New("U 位区间非法（u_position>=1 且不超出机柜容量）")
	ErrOverlap        = errors.New("U 位区间与其他设备重叠")
	ErrAlreadyMounted = errors.New("设备已挂载在该机柜")
	ErrNotMounted     = errors.New("设备未挂载在该机柜")
)

// DefaultUTotal 是机柜默认 U 位容量（CI 未配置 u_capacity 属性时使用）。
const DefaultUTotal = 42

// Service 是 DCIM 业务服务。
type Service struct {
	db *gorm.DB
}

// NewService 创建 DCIM 服务。
func NewService(db *gorm.DB) *Service {
	return &Service{db: db}
}

// RackUnit 描述一个 U 位的占用情况。
type RackUnit struct {
	U            int    `json:"u"`
	OccupantCIID string `json:"occupant_ci_id,omitempty"`
	OccupantName string `json:"occupant_name,omitempty"`
}

// RackUnits 是机柜 U 位总览。
type RackUnits struct {
	RackID string     `json:"rack_id"`
	UTotal int        `json:"u_total"`
	Units  []RackUnit `json:"units"`
}

// OverlapU 判定两个 U 位区间 [posA, posA+hA) 与 [posB, posB+hB) 是否重叠。
func OverlapU(posA, hA, posB, hB int) bool {
	return posA < posB+hB && posB < posA+hA
}

// UTotalOf 读取机柜 CI 的 U 位容量：优先 attributes.u_capacity（与 rack 种子模型一致），
// 兼容历史数据中的 u_total，均未配置时取默认值。
func UTotalOf(rack store.CI) int {
	for _, key := range []string{"u_capacity", "u_total"} {
		if v, ok := rack.Attributes[key]; ok {
			switch n := v.(type) {
			case float64:
				if n > 0 {
					return int(n)
				}
			case json.Number:
				if i, err := n.Int64(); err == nil && i > 0 {
					return int(i)
				}
			}
		}
	}
	return DefaultUTotal
}

// loadRack 加载机柜 CI 并校验其模型为 code=rack。
func (s *Service) loadRack(ctx context.Context, rackCIID string) (store.CI, error) {
	var rack store.CI
	err := s.db.WithContext(ctx).First(&rack, "id = ?", rackCIID).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return store.CI{}, ErrRackNotFound
		}
		return store.CI{}, fmt.Errorf("查询机柜 CI 失败: %w", err)
	}
	var model store.Model
	if err := s.db.WithContext(ctx).First(&model, "id = ?", rack.ModelID).Error; err != nil {
		return store.CI{}, fmt.Errorf("加载机柜模型失败: %w", err)
	}
	if model.Code != "rack" {
		return store.CI{}, fmt.Errorf("%w: CI %s 所属模型为 %s", ErrNotRack, rackCIID, model.Code)
	}
	return rack, nil
}

// Units 返回机柜全部 U 位的占用视图（含占用设备的 CI ID 与显示名）。
func (s *Service) Units(ctx context.Context, rackCIID string) (RackUnits, error) {
	rack, err := s.loadRack(ctx, rackCIID)
	if err != nil {
		return RackUnits{}, err
	}
	uTotal := UTotalOf(rack)

	var mounts []store.RackMount
	if err := s.db.WithContext(ctx).Where("rack_ci_id = ?", rack.ID).Find(&mounts).Error; err != nil {
		return RackUnits{}, fmt.Errorf("查询挂载记录失败: %w", err)
	}

	// 预取占用设备 CI，解析显示名（name → hostname → serial_no → CI ID）。
	names := map[int]store.CI{}
	for _, m := range mounts {
		var dev store.CI
		if err := s.db.WithContext(ctx).First(&dev, "id = ?", m.DeviceCIID).Error; err != nil {
			continue // 设备已被删除，U 位视为空闲（悬空挂载由治理流程清理）
		}
		for u := m.UPosition; u < m.UPosition+m.UHeight && u <= uTotal; u++ {
			if u >= 1 {
				names[u] = dev
			}
		}
	}

	units := make([]RackUnit, 0, uTotal)
	for u := 1; u <= uTotal; u++ {
		unit := RackUnit{U: u}
		if dev, ok := names[u]; ok {
			unit.OccupantCIID = dev.ID
			unit.OccupantName = displayName(dev)
		}
		units = append(units, unit)
	}
	return RackUnits{RackID: rack.ID, UTotal: uTotal, Units: units}, nil
}

// Mount 把设备挂载到机柜指定 U 位区间：校验区间合法性与重叠。
func (s *Service) Mount(ctx context.Context, rackCIID, deviceCIID string, uPosition, uHeight int) (store.RackMount, error) {
	rack, err := s.loadRack(ctx, rackCIID)
	if err != nil {
		return store.RackMount{}, err
	}
	uTotal := UTotalOf(rack)
	if uPosition < 1 || uHeight < 1 || uPosition+uHeight-1 > uTotal {
		return store.RackMount{}, fmt.Errorf("%w: [%d, %d) 超出机柜容量 %dU", ErrInvalidRange, uPosition, uPosition+uHeight, uTotal)
	}
	var dev store.CI
	err = s.db.WithContext(ctx).First(&dev, "id = ?", deviceCIID).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return store.RackMount{}, ErrDeviceNotFound
		}
		return store.RackMount{}, fmt.Errorf("查询设备 CI 失败: %w", err)
	}

	var mounts []store.RackMount
	if err := s.db.WithContext(ctx).Where("rack_ci_id = ?", rack.ID).Find(&mounts).Error; err != nil {
		return store.RackMount{}, fmt.Errorf("查询挂载记录失败: %w", err)
	}
	for _, m := range mounts {
		if m.DeviceCIID == deviceCIID {
			return store.RackMount{}, fmt.Errorf("%w: U%d 起 %dU", ErrAlreadyMounted, m.UPosition, m.UHeight)
		}
		if OverlapU(uPosition, uHeight, m.UPosition, m.UHeight) {
			return store.RackMount{}, fmt.Errorf("%w: 与 U%d 起 %dU 的设备冲突", ErrOverlap, m.UPosition, m.UHeight)
		}
	}

	mount := store.RackMount{
		RackCIID:   rack.ID,
		DeviceCIID: deviceCIID,
		UPosition:  uPosition,
		UHeight:    uHeight,
	}
	if err := s.db.WithContext(ctx).Create(&mount).Error; err != nil {
		return store.RackMount{}, fmt.Errorf("写入挂载记录失败: %w", err)
	}
	return mount, nil
}

// Unmount 把设备从机柜下架，未挂载时返回 ErrNotMounted。
func (s *Service) Unmount(ctx context.Context, rackCIID, deviceCIID string) error {
	rack, err := s.loadRack(ctx, rackCIID)
	if err != nil {
		return err
	}
	res := s.db.WithContext(ctx).Where("rack_ci_id = ? AND device_ci_id = ?", rack.ID, deviceCIID).
		Delete(&store.RackMount{})
	if res.Error != nil {
		return fmt.Errorf("删除挂载记录失败: %w", res.Error)
	}
	if res.RowsAffected == 0 {
		return ErrNotMounted
	}
	return nil
}

// displayName 取 CI 的展示名：name → hostname → ident → serial_no → CI ID。
// （host 种子模型用 ident 作主机标识，network_device 用 serial_no。）
func displayName(ci store.CI) string {
	for _, key := range []string{"name", "hostname", "ident", "serial_no"} {
		if v, ok := ci.Attributes[key].(string); ok && v != "" {
			return v
		}
	}
	return ci.ID
}
