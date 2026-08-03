// Package lifecycle 实现 CI 生命周期状态机（F-026）：
// 状态流转校验、退役三方会签判定（心跳停更 + 扫描不存活 + 云无实例）
// 与退役联动执行（状态→retired、n9e 摘除 target、JumpServer 禁用资产、
// IPAM 关联 IP 置闲置、写审计与告警事件）。
package lifecycle

import (
	"context"
	"fmt"
	"time"

	"gorm.io/datatypes"
	"gorm.io/gorm"

	"meridian/server/internal/jumpserver"
	"meridian/server/internal/n9e"
	"meridian/server/internal/store"
)

// 生命周期状态枚举。
const (
	StatusDiscovered    = "discovered"
	StatusPurchase      = "purchase"
	StatusStock         = "stock"
	StatusActive        = "active"
	StatusMaintenance   = "maintenance"
	StatusPendingRetire = "pending_retire"
	StatusRetired       = "retired"
)

// Transitions 定义合法的状态流转边（retired 为终态）。
// 存量数据兼容：discovered 可直接转 stock/active。
var Transitions = map[string][]string{
	StatusDiscovered:    {StatusStock, StatusActive},
	StatusPurchase:      {StatusStock},
	StatusStock:         {StatusActive},
	StatusActive:        {StatusMaintenance, StatusPendingRetire},
	StatusMaintenance:   {StatusActive, StatusPendingRetire},
	StatusPendingRetire: {StatusActive, StatusRetired},
	StatusRetired:       {},
}

// CanTransit 判定 from → to 是否为合法流转。
func CanTransit(from, to string) bool {
	for _, next := range Transitions[from] {
		if next == to {
			return true
		}
	}
	return false
}

// 会签阈值。
const (
	// RetireHeartbeatStale 为心跳停更阈值（7 天）。
	RetireHeartbeatStale = 7 * 24 * time.Hour
	// RetireScanWindow 为扫描存活回看窗口（7 天，约覆盖近 3 次日扫描）。
	RetireScanWindow = 7 * 24 * time.Hour
)

// RetireCheck 是一台 CI 的三方会签判定结果。
// 三个 *_ok 标志表示"该方仍认为资产存活/存在"，全部为非才 eligible。
type RetireCheck struct {
	CI          store.CI `json:"ci"`
	HeartbeatOK bool     `json:"heartbeat_ok"` // 心跳仍在更新（7 天内）
	ScanOK      bool     `json:"scan_ok"`      // 近窗口内有 ip_scan 存活记录
	CloudOK     bool     `json:"cloud_ok"`     // 云侧实例存在（有 cloud_instance_id）
	Eligible    bool     `json:"eligible"`
}

// RetireAction 是退役联动的一个动作结果。
type RetireAction struct {
	Type   string `json:"type"` // status/n9e_remove_target/jumpserver_disable/ipam_idle
	OK     bool   `json:"ok"`
	Detail string `json:"detail"`
}

// Service 是生命周期服务。n9eClient / jsClient 可空（未配置时对应联动动作报告失败）。
type Service struct {
	db        *gorm.DB
	n9eClient *n9e.Client
	now       func() time.Time
}

// NewService 创建生命周期服务。
func NewService(db *gorm.DB, n9eClient *n9e.Client) *Service {
	return &Service{db: db, n9eClient: n9eClient, now: time.Now}
}

// heartbeatRef 取 CI 的心跳参考时间：优先 last_heartbeat_at 属性，缺失回退 updated_at。
func heartbeatRef(ci store.CI) time.Time {
	if v, ok := ci.Attributes["last_heartbeat_at"].(string); ok && v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			return t
		}
		if t, err := time.Parse("2006-01-02", v); err == nil {
			return t
		}
	}
	return ci.UpdatedAt
}

// checkRetire 对单台 host CI 执行三方会签判定。
func (s *Service) checkRetire(ctx context.Context, ci store.CI) (RetireCheck, error) {
	check := RetireCheck{CI: ci}
	// 会签一：心跳停更超 7 天（last_heartbeat_at 属性优先，回退 updated_at）。
	check.HeartbeatOK = s.now().Sub(heartbeatRef(ci)) <= RetireHeartbeatStale

	// 会签二：取该主机 IP 最近 3 次 ip_scan 存活记录，最近一次落在回看窗口内
	// 即视为扫描存活（死主机不再出现在新扫描中，窗口后自然判死）。
	ip, _ := ci.Attributes["ip"].(string)
	if ip != "" {
		var records []store.DiscoveryRawRecord
		if err := s.db.WithContext(ctx).
			Where("source = ?", "ip_scan").
			Order("occurred_at DESC").Limit(10000).Find(&records).Error; err != nil {
			return check, fmt.Errorf("查询扫描原始记录失败: %w", err)
		}
		seen := 0
		for _, rec := range records {
			if !recordHasIP(rec, ip) {
				continue
			}
			seen++
			if s.now().Sub(rec.OccurredAt) <= RetireScanWindow {
				check.ScanOK = true
				break
			}
			if seen >= 3 {
				break // 近 3 次记录均超窗，判扫描不存活
			}
		}
	}

	// 会签三：云侧实例存在性。无 cloud_instance_id 即视为云侧无实例；
	// 有 cloud_instance_id 时保守判定云侧实例存在（云 API 直连核查为真实环境动作，
	// mock 阶段以属性存在性为准）。
	if v, ok := ci.Attributes["cloud_instance_id"].(string); ok && v != "" {
		check.CloudOK = true
	}

	check.Eligible = !check.HeartbeatOK && !check.ScanOK && !check.CloudOK
	return check, nil
}

// recordHasIP 判定 ip_scan 原始记录报文是否包含指定 IP（存活记录）。
func recordHasIP(rec store.DiscoveryRawRecord, ip string) bool {
	if attrs, ok := rec.Payload["attributes"].(map[string]any); ok {
		if v, ok := attrs["ip"].(string); ok && v == ip {
			return true
		}
	}
	if v, ok := rec.Payload["ip"].(string); ok {
		return v == ip
	}
	return false
}

// RetireCandidates 返回全部未退役 host CI 的三方会签判定清单。
func (s *Service) RetireCandidates(ctx context.Context) ([]RetireCheck, error) {
	var model store.Model
	if err := s.db.WithContext(ctx).First(&model, "code = ?", "host").Error; err != nil {
		return nil, fmt.Errorf("host 模型不存在: %w", err)
	}
	var hosts []store.CI
	if err := s.db.WithContext(ctx).
		Where("model_id = ? AND status NOT IN ?", model.ID, []string{StatusRetired, StatusPendingRetire}).
		Order("created_at ASC").Find(&hosts).Error; err != nil {
		return nil, fmt.Errorf("查询 host CI 失败: %w", err)
	}
	checks := make([]RetireCheck, 0, len(hosts))
	for _, h := range hosts {
		check, err := s.checkRetire(ctx, h)
		if err != nil {
			return nil, err
		}
		checks = append(checks, check)
	}
	return checks, nil
}

// Retire 执行退役联动。jsClient 为本次请求解析到的 JumpServer 客户端（可空）。
// 各动作尽力执行、逐动作报告结果，不因单个动作失败而中断后续动作。
func (s *Service) Retire(ctx context.Context, ci store.CI, operator string, jsClient *jumpserver.Client) ([]RetireAction, error) {
	if ci.Status == StatusRetired {
		return nil, fmt.Errorf("CI 已是 retired 状态")
	}
	actions := []RetireAction{}

	// 动作一：状态 → retired（审计随状态变更写入）。
	oldStatus := ci.Status
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&store.CI{}).Where("id = ?", ci.ID).Update("status", StatusRetired).Error; err != nil {
			return err
		}
		return tx.Create(&store.AuditLog{
			CIID:     ci.ID,
			Action:   "retire",
			Source:   "lifecycle",
			Operator: operator,
			Changes: datatypes.JSONMap{
				"status": map[string]any{"old": oldStatus, "new": StatusRetired},
			},
			Message: "退役联动执行",
		}).Error
	})
	if err != nil {
		return nil, fmt.Errorf("状态置为 retired 失败: %w", err)
	}
	actions = append(actions, RetireAction{Type: "status", OK: true, Detail: oldStatus + " → retired"})

	// 动作二：n9e 摘除 target（按 ident / 主 IP 匹配）。
	actions = append(actions, s.removeN9ETarget(ctx, ci))

	// 动作三：JumpServer 禁用资产。
	actions = append(actions, disableJumpServer(ctx, ci, jsClient))

	// 动作四：IPAM 关联 IP 置闲置。
	actions = append(actions, s.idleIPs(ctx, ci))

	// 动作五：告警事件留痕（资产治理线索）。
	ident, _ := ci.Attributes["ident"].(string)
	if ident == "" {
		ident = ci.ID
	}
	if err := s.db.WithContext(ctx).Create(&store.AlertEvent{
		Level:  store.AlertLevelInfo,
		Title:  fmt.Sprintf("主机 %s 已退役", ident),
		Source: "lifecycle",
		CIID:   ci.ID,
		Detail: fmt.Sprintf("退役联动执行完成（操作者 %s），状态 %s → retired", operator, oldStatus),
	}).Error; err != nil {
		actions = append(actions, RetireAction{Type: "alert_event", OK: false, Detail: err.Error()})
	} else {
		actions = append(actions, RetireAction{Type: "alert_event", OK: true, Detail: "告警事件已写入"})
	}
	return actions, nil
}

// removeN9ETarget 摘除 CI 在 n9e 的监控 target。
func (s *Service) removeN9ETarget(ctx context.Context, ci store.CI) RetireAction {
	act := RetireAction{Type: "n9e_remove_target"}
	if s.n9eClient == nil {
		act.Detail = "n9e 未配置（N9E_API_URL/N9E_API_TOKEN），跳过"
		return act
	}
	targets, err := s.n9eClient.ListTargets(ctx)
	if err != nil {
		act.Detail = "拉取 n9e targets 失败: " + err.Error()
		return act
	}
	ident, _ := ci.Attributes["ident"].(string)
	ip, _ := ci.Attributes["ip"].(string)
	var ids []int64
	for _, t := range targets {
		if (ident != "" && t.Ident == ident) || (ip != "" && t.HostIP == ip) {
			ids = append(ids, t.ID)
		}
	}
	if len(ids) == 0 {
		act.OK = true
		act.Detail = "n9e 无匹配 target，无需摘除"
		return act
	}
	if err := s.n9eClient.DeleteTargets(ctx, ids); err != nil {
		act.Detail = "摘除 n9e target 失败: " + err.Error()
		return act
	}
	act.OK = true
	act.Detail = fmt.Sprintf("已摘除 %d 个 n9e target（ids=%v）", len(ids), ids)
	return act
}

// disableJumpServer 禁用 CI 在 JumpServer 的资产。
func disableJumpServer(ctx context.Context, ci store.CI, jsClient *jumpserver.Client) RetireAction {
	act := RetireAction{Type: "jumpserver_disable"}
	if jsClient == nil {
		act.Detail = "JumpServer 未配置（凭据 type=jumpserver 或 JUMPSERVER_URL/JUMPSERVER_TOKEN），跳过"
		return act
	}
	ip, _ := ci.Attributes["ip"].(string)
	assetID, err := jsClient.DisableAssetByIP(ctx, ip)
	if err != nil {
		act.Detail = "禁用 JumpServer 资产失败: " + err.Error()
		return act
	}
	if assetID == "" {
		act.OK = true
		act.Detail = fmt.Sprintf("JumpServer 无 IP %s 对应资产，无需禁用", ip)
		return act
	}
	act.OK = true
	act.Detail = fmt.Sprintf("已禁用 JumpServer 资产 %s（IP %s）", assetID, ip)
	return act
}

// idleIPs 把 CI 关联的 IPAM 地址登记置为闲置（idle）。
func (s *Service) idleIPs(ctx context.Context, ci store.CI) RetireAction {
	act := RetireAction{Type: "ipam_idle"}
	q := s.db.WithContext(ctx).Model(&store.IPAddress{}).Where("ci_id = ?", ci.ID)
	if ip, ok := ci.Attributes["ip"].(string); ok && ip != "" {
		q = q.Or("ip = ?", ip)
	}
	res := q.Where("status <> ?", "idle").Update("status", "idle")
	if res.Error != nil {
		act.Detail = "IPAM 置闲置失败: " + res.Error.Error()
		return act
	}
	act.OK = true
	act.Detail = fmt.Sprintf("已将 %d 条 IPAM 登记置为闲置", res.RowsAffected)
	return act
}
