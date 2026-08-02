package dcim

import (
	"context"
	"encoding/json"
	"fmt"

	"meridian/server/internal/store"
)

// RoomStat 是一个机房的容量聚合。
type RoomStat struct {
	RoomID          string  `json:"room_id"`
	Name            string  `json:"name"`
	Code            string  `json:"code"`
	Address         string  `json:"address"`
	RackCount       int     `json:"rack_count"`
	UTotal          int     `json:"u_total"`
	UUsed           int     `json:"u_used"`
	PowerCapacityKW float64 `json:"power_capacity_kw"`
}

// RackStat 是逐机柜明细（前端分组展示用，避免 N+1 请求）。
type RackStat struct {
	RackID          string  `json:"rack_id"`
	RoomID          *string `json:"room_id"` // 未分配机房时为 null
	Name            string  `json:"name"`
	UTotal          int     `json:"u_total"`
	UUsed           int     `json:"u_used"`
	PowerCapacityKW float64 `json:"power_capacity_kw"`
}

// UnassignedStat 是未分配机房的机柜聚合。
type UnassignedStat struct {
	RackCount       int     `json:"rack_count"`
	UTotal          int     `json:"u_total"`
	UUsed           int     `json:"u_used"`
	PowerCapacityKW float64 `json:"power_capacity_kw"`
}

// Overview 是 DCIM 全局容量总览。
type Overview struct {
	RoomCount       int            `json:"room_count"`
	RackCount       int            `json:"rack_count"`
	UTotal          int            `json:"u_total"`
	UUsed           int            `json:"u_used"`
	PowerCapacityKW float64        `json:"power_capacity_kw"`
	Rooms           []RoomStat     `json:"rooms"`
	Racks           []RackStat     `json:"racks"`
	Unassigned      UnassignedStat `json:"unassigned"`
}

// Overview 按机房聚合机柜数量、U 位容量/占用与电力容量。
// 机房/机柜本体是模型 code=room / code=rack 的 CI，归属关系为 located_in；
// 模型尚未种子导入时返回空总览而非报错（与 Oxidized 清单同一纪律，便于先接通）。
func (s *Service) Overview(ctx context.Context) (Overview, error) {
	var rackModel store.Model
	if err := s.db.WithContext(ctx).First(&rackModel, "code = ?", "rack").Error; err != nil {
		return Overview{Rooms: []RoomStat{}, Racks: []RackStat{}}, nil
	}
	var roomModel store.Model
	roomModelFound := s.db.WithContext(ctx).First(&roomModel, "code = ?", "room").Error == nil

	var racks []store.CI
	if err := s.db.WithContext(ctx).
		Where("model_id = ? AND status <> ?", rackModel.ID, "retired").
		Order("created_at ASC").Find(&racks).Error; err != nil {
		return Overview{}, fmt.Errorf("查询机柜 CI 失败: %w", err)
	}

	// 机柜→机房归属关系。
	var relations []store.CIRelation
	if err := s.db.WithContext(ctx).Where("relation_code = ?", "located_in").Find(&relations).Error; err != nil {
		return Overview{}, fmt.Errorf("查询机柜归属关系失败: %w", err)
	}
	rackRoom := map[string]string{} // rack_ci_id → room_ci_id
	for _, rel := range relations {
		rackRoom[rel.SrcCIID] = rel.DstCIID
	}

	// 每机柜 U 位占用（挂载记录 u_height 之和）。
	var mounts []store.RackMount
	if err := s.db.WithContext(ctx).Find(&mounts).Error; err != nil {
		return Overview{}, fmt.Errorf("查询挂载记录失败: %w", err)
	}
	usedByRack := map[string]int{}
	for _, m := range mounts {
		usedByRack[m.RackCIID] += m.UHeight
	}

	ov := Overview{
		RackCount: len(racks),
		Rooms:     []RoomStat{},
		Racks:     []RackStat{},
	}
	statByRoom := map[string]*RoomStat{}
	unassigned := RoomStat{} // 借用 RoomStat 累加，最后转换

	addRack := func(stat *RoomStat, rack store.CI) {
		stat.RackCount++
		stat.UTotal += UTotalOf(rack)
		stat.UUsed += usedByRack[rack.ID]
		stat.PowerCapacityKW += numberAttr(rack, "power_capacity_kw")
	}

	for _, rack := range racks {
		roomID, ok := rackRoom[rack.ID]
		assigned := ok && roomModelFound
		if assigned {
			stat, exists := statByRoom[roomID]
			if !exists {
				stat = &RoomStat{RoomID: roomID}
				statByRoom[roomID] = stat
			}
			addRack(stat, rack)
		} else {
			addRack(&unassigned, rack)
		}
		rs := RackStat{
			RackID:          rack.ID,
			Name:            displayName(rack),
			UTotal:          UTotalOf(rack),
			UUsed:           usedByRack[rack.ID],
			PowerCapacityKW: numberAttr(rack, "power_capacity_kw"),
		}
		if assigned {
			rs.RoomID = &roomID
		}
		ov.Racks = append(ov.Racks, rs)
		ov.UTotal += rs.UTotal
		ov.UUsed += rs.UUsed
		ov.PowerCapacityKW += rs.PowerCapacityKW
	}
	ov.Unassigned = UnassignedStat{
		RackCount:       unassigned.RackCount,
		UTotal:          unassigned.UTotal,
		UUsed:           unassigned.UUsed,
		PowerCapacityKW: unassigned.PowerCapacityKW,
	}

	// 机房列表：含无机柜的机房（便于展示空机房）。
	if roomModelFound {
		var rooms []store.CI
		if err := s.db.WithContext(ctx).
			Where("model_id = ? AND status <> ?", roomModel.ID, "retired").
			Order("created_at ASC").Find(&rooms).Error; err != nil {
			return Overview{}, fmt.Errorf("查询机房 CI 失败: %w", err)
		}
		ov.RoomCount = len(rooms)
		for _, room := range rooms {
			stat, ok := statByRoom[room.ID]
			if !ok {
				stat = &RoomStat{RoomID: room.ID}
			}
			stat.Name = stringAttrOf(room, "name")
			stat.Code = stringAttrOf(room, "code")
			stat.Address = stringAttrOf(room, "address")
			ov.Rooms = append(ov.Rooms, *stat)
		}
	}
	return ov, nil
}

// stringAttrOf 读取 CI 字符串属性，缺失或非字符串时返回空串。
func stringAttrOf(ci store.CI, key string) string {
	if v, ok := ci.Attributes[key].(string); ok {
		return v
	}
	return ""
}

// numberAttr 读取 CI 数值属性（兼容 float64 与 json.Number），缺失时返回 0。
func numberAttr(ci store.CI, key string) float64 {
	switch n := ci.Attributes[key].(type) {
	case float64:
		return n
	case json.Number:
		f, _ := n.Float64()
		return f
	}
	return 0
}
