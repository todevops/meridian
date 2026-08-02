// Package reconcile 实现调和引擎 MVP：按模型配置的调和键（reconcile_keys，按优先级排序）
// 判定发现记录与存量 CI 的同一性——
//   - 命中主调和键（或仅提供了次要键且与存量一致）：更新（按来源优先级做字段级合并并记审计）；
//   - 未命中任何键：创建 status=discovered 的 CI 入发现池；
//   - 命中次要键但其他键不符（如同 IP 不同 ident）：判定 conflict 转入发现池待人工裁决。
package reconcile

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"time"

	"gorm.io/datatypes"
	"gorm.io/gorm"

	"cmdb/server/internal/store"
	"cmdb/server/internal/validation"
)

// 调和判定动作。
const (
	ActionCreate   = "create"
	ActionUpdate   = "update"
	ActionConflict = "conflict"
	ActionPool     = "pool"
)

// Record 是标准发现记录，与 openapi.yaml 中 DiscoveryRecord 对应。
type Record struct {
	Source         string         `json:"source"`          // 发现来源系统
	Collector      string         `json:"collector"`       // 采集器标识
	ModelCandidate string         `json:"model_candidate"` // 候选模型编码
	Attributes     map[string]any `json:"attributes"`      // 采集到的原始属性键值对
	OccurredAt     time.Time      `json:"occurred_at"`     // 采集发生时间
}

// Change 记录单字段变更。
type Change struct {
	Old any `json:"old"`
	New any `json:"new"`
}

// Decision 是一次调和判定的结果。
type Decision struct {
	Action      string            `json:"action"`                  // create/update/conflict/pool
	MatchedCIID string            `json:"matched_ci_id,omitempty"` // 命中的存量 CI（update/conflict 时给出）
	Reasons     []string          `json:"reasons"`                 // 判定依据
	Changes     map[string]Change `json:"-"`                       // 字段变更明细（审计用）
}

// sourcePriorities 为来源优先级表（数值越大越权威）。
// 人工维护的数据最权威，采集器按可信度递减；未列出的来源取默认值。
var sourcePriorities = map[string]int{
	"manual":     100,
	"n9e":        80,
	"vsphere":    70,
	"aliyun":     70,
	"volcengine": 70,
	"nmap":       60,
}

const defaultSourcePriority = 50

// PriorityOf 返回来源标识的优先级。
func PriorityOf(source string) int {
	if p, ok := sourcePriorities[source]; ok {
		return p
	}
	return defaultSourcePriority
}

// Engine 是调和引擎。
type Engine struct {
	db *gorm.DB
}

// NewEngine 创建调和引擎。
func NewEngine(db *gorm.DB) *Engine {
	return &Engine{db: db}
}

// Evaluate 对一条发现记录执行调和判定。
// dryRun 为 true 时只计算判定结果不落库（供 /reconcile/preview 使用）。
func (e *Engine) Evaluate(ctx context.Context, rec Record, dryRun bool) (Decision, error) {
	// 1. 解析候选模型。
	var model store.Model
	if err := e.db.WithContext(ctx).Where("code = ?", rec.ModelCandidate).First(&model).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			d := Decision{
				Action:  ActionPool,
				Reasons: []string{fmt.Sprintf("候选模型 %q 不存在，记录转入发现池", rec.ModelCandidate)},
			}
			return d, e.commitPool(ctx, rec, d, dryRun)
		}
		return Decision{}, fmt.Errorf("查询模型失败: %w", err)
	}

	// 2. 读取调和键配置。
	keys := model.ReconcileKeys.Data()
	if len(keys) == 0 {
		d := Decision{
			Action:  ActionPool,
			Reasons: []string{fmt.Sprintf("模型 %q 未配置调和键（reconcile_keys），无法判定同一性，记录转入发现池", model.Code)},
		}
		return d, e.commitPool(ctx, rec, d, dryRun)
	}

	// 3. 按调和键优先级依次匹配存量 CI。
	matchedID, matchedIdx, multiple, err := e.matchCI(ctx, model, rec, keys)
	if err != nil {
		return Decision{}, err
	}

	switch {
	case multiple:
		// 同一键命中多条 CI：数据质量异常，转人工。
		d := Decision{
			Action:      ActionConflict,
			MatchedCIID: matchedID,
			Reasons:     []string{fmt.Sprintf("调和键 %q 命中多条存量 CI（含 %s），数据质量异常，转入发现池待人工裁决", keys[matchedIdx], matchedID)},
		}
		return d, e.commitPool(ctx, rec, d, dryRun)

	case matchedID == "":
		// 未命中任何键：新建 CI 入发现池。
		return e.createCI(ctx, rec, model, keys, dryRun)

	default:
		// 命中存量 CI：次要键命中时需先与更优先的键交叉确认。
		var ci store.CI
		if err := e.db.WithContext(ctx).First(&ci, "id = ?", matchedID).Error; err != nil {
			return Decision{}, fmt.Errorf("加载命中 CI 失败: %w", err)
		}
		if matchedIdx > 0 {
			if reason, conflict := crossCheckKeys(ci, rec, keys, matchedIdx); conflict {
				d := Decision{
					Action:      ActionConflict,
					MatchedCIID: matchedID,
					Reasons:     []string{reason},
				}
				return d, e.commitPool(ctx, rec, d, dryRun)
			}
		}
		return e.updateCI(ctx, rec, model, ci, keys[matchedIdx], dryRun)
	}
}

// matchCI 按调和键顺序查找存量 CI。
// 返回命中的 CI ID、命中键下标、是否命中多条。
func (e *Engine) matchCI(ctx context.Context, model store.Model, rec Record, keys []string) (ciID string, idx int, multiple bool, err error) {
	for i, key := range keys {
		v, ok := rec.Attributes[key]
		if !ok || v == nil || v == "" {
			continue // 记录未携带该键，跳过
		}
		var ids []string
		err := e.db.WithContext(ctx).Model(&store.CI{}).
			Where("model_id = ? AND status <> ?", model.ID, "retired").
			Where(datatypes.JSONQuery("attributes").Equals(v, key)).
			Limit(2).Pluck("id", &ids).Error
		if err != nil {
			return "", 0, false, fmt.Errorf("按调和键 %q 查询 CI 失败: %w", key, err)
		}
		if len(ids) > 1 {
			return ids[0], i, true, nil
		}
		if len(ids) == 1 {
			return ids[0], i, false, nil
		}
	}
	return "", 0, false, nil
}

// crossCheckKeys 在次要键命中时，检查更优先的键是否与存量 CI 一致。
// 不一致（如同 IP 但其他键不符）则判定冲突。
func crossCheckKeys(ci store.CI, rec Record, keys []string, matchedIdx int) (string, bool) {
	for j := 0; j < matchedIdx; j++ {
		key := keys[j]
		rv, ok := rec.Attributes[key]
		if !ok || rv == nil || rv == "" {
			continue // 记录未携带该更优先键，无法据此证伪
		}
		cv := ci.Attributes[key]
		if !equalJSON(cv, rv) {
			return fmt.Sprintf("辅助键 %q 命中存量 CI %s，但调和键 %q 不一致（存量 %v，上报 %v），判定冲突转入发现池",
				keys[matchedIdx], ci.ID, key, cv, rv), true
		}
	}
	return "", false
}

// createCI 执行新建分支：创建 status=discovered 的 CI 入发现池。
func (e *Engine) createCI(ctx context.Context, rec Record, model store.Model, keys []string, dryRun bool) (Decision, error) {
	// 入库前强制执行模型校验规则。
	if errs := validation.ValidateAttributes(model.Attributes.Data(), rec.Attributes); errs != nil {
		d := Decision{
			Action:  ActionPool,
			Reasons: []string{fmt.Sprintf("未命中调和键 %v，但属性校验未通过，转入发现池: %s", keys, errs.Error())},
		}
		return d, e.commitPool(ctx, rec, d, dryRun)
	}
	if errs := validation.ValidateUnique(e.db.WithContext(ctx), model.ID, model.Attributes.Data(), rec.Attributes, ""); errs != nil {
		d := Decision{
			Action:  ActionPool,
			Reasons: []string{fmt.Sprintf("未命中调和键 %v，但唯一性校验未通过，转入发现池: %s", keys, errs.Error())},
		}
		return d, e.commitPool(ctx, rec, d, dryRun)
	}

	d := Decision{
		Action:  ActionCreate,
		Reasons: []string{fmt.Sprintf("未命中调和键 %v，新建 CI 入发现池（status=discovered）", keys)},
	}
	if dryRun {
		return d, nil
	}

	ci := store.CI{
		ModelID:      model.ID,
		Attributes:   datatypes.JSONMap(rec.Attributes),
		FieldSources: fieldSourcesFor(rec.Attributes, rec.Source),
		Status:       "discovered",
		Source:       rec.Source,
	}
	changes := map[string]Change{}
	for k, v := range rec.Attributes {
		changes[k] = Change{Old: nil, New: v}
	}
	err := e.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&ci).Error; err != nil {
			return fmt.Errorf("创建 CI 失败: %w", err)
		}
		return writeAudit(tx, ci.ID, "create", rec.Source, changes, "调和新建 CI 入发现池")
	})
	if err != nil {
		return Decision{}, err
	}
	d.MatchedCIID = ci.ID
	d.Changes = changes
	return d, nil
}

// updateCI 执行更新分支：按来源优先级做字段级合并，变更记审计。
func (e *Engine) updateCI(ctx context.Context, rec Record, model store.Model, ci store.CI, hitKey string, dryRun bool) (Decision, error) {
	d := Decision{
		Action:      ActionUpdate,
		MatchedCIID: ci.ID,
		Reasons:     []string{fmt.Sprintf("调和键 %q 命中存量 CI %s", hitKey, ci.ID)},
	}

	// 1. 计算合并计划：仅当上报来源优先级不低于字段既有来源时才产生变更。
	changes, skipped := PlanMerge(&ci, rec.Attributes, rec.Source)
	for _, k := range skipped {
		d.Reasons = append(d.Reasons, fmt.Sprintf("属性 %s 由更高优先级来源维护，跳过更新", k))
	}

	// 2. 合并后的完整属性集须通过模型校验；不合规的变更字段剔除并说明。
	if len(changes) > 0 {
		merged := mergedView(ci.Attributes, newValuesOf(changes))
		if errs := validation.ValidateAttributes(model.Attributes.Data(), merged); errs != nil {
			for field, msg := range errs {
				if _, changing := changes[field]; changing {
					delete(changes, field)
					d.Reasons = append(d.Reasons, fmt.Sprintf("属性 %s 校验未通过，本次不更新: %s", field, msg))
				}
			}
		}
	}

	// 3. 唯一性校验（排除自身）。
	if len(changes) > 0 {
		merged := mergedView(ci.Attributes, newValuesOf(changes))
		if errs := validation.ValidateUnique(e.db.WithContext(ctx), model.ID, model.Attributes.Data(), merged, ci.ID); errs != nil {
			for field, msg := range errs {
				if _, changing := changes[field]; changing {
					delete(changes, field)
					d.Reasons = append(d.Reasons, fmt.Sprintf("属性 %s 唯一性校验未通过，本次不更新: %s", field, msg))
				}
			}
		}
	}

	if len(changes) == 0 {
		d.Reasons = append(d.Reasons, "无字段变更")
		return d, nil
	}
	for k := range changes {
		d.Reasons = append(d.Reasons, fmt.Sprintf("属性 %s 发生变更", k))
	}
	d.Changes = changes
	if dryRun {
		return d, nil
	}

	// 4. 落库：应用变更计划并记审计。
	ApplyChanges(&ci, changes, rec.Source)
	err := e.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&store.CI{}).Where("id = ?", ci.ID).
			Updates(map[string]any{"attributes": ci.Attributes, "field_sources": ci.FieldSources}).Error; err != nil {
			return fmt.Errorf("更新 CI 失败: %w", err)
		}
		return writeAudit(tx, ci.ID, "update", rec.Source, changes, "调和更新 CI 属性")
	})
	if err != nil {
		return Decision{}, err
	}
	return d, nil
}

// PlanMerge 计算 incoming 相对 CI 现状的字段级合并计划（不修改 ci）：
// 仅当上报来源优先级不低于字段既有来源时产生变更项。
func PlanMerge(ci *store.CI, incoming map[string]any, source string) (changes map[string]Change, skipped []string) {
	changes = map[string]Change{}
	for k, v := range incoming {
		existingSrc, _ := ci.FieldSources[k].(string)
		if existingSrc == "" {
			existingSrc = ci.Source
		}
		if PriorityOf(source) < PriorityOf(existingSrc) {
			skipped = append(skipped, k)
			continue
		}
		old, exists := ci.Attributes[k]
		if !exists || !equalJSON(old, v) {
			changes[k] = Change{Old: old, New: v}
		}
	}
	return changes, skipped
}

// ApplyChanges 把合并计划落到 CI 对象上（就地修改）。
func ApplyChanges(ci *store.CI, changes map[string]Change, source string) {
	if ci.Attributes == nil {
		ci.Attributes = datatypes.JSONMap{}
	}
	if ci.FieldSources == nil {
		ci.FieldSources = datatypes.JSONMap{}
	}
	for k, ch := range changes {
		ci.Attributes[k] = ch.New
		ci.FieldSources[k] = source
	}
}

// commitPool 处理 conflict/pool 判定的副作用：写入发现池待人工裁决。
func (e *Engine) commitPool(ctx context.Context, rec Record, d Decision, dryRun bool) error {
	if dryRun {
		return nil
	}
	raw, err := json.Marshal(rec)
	if err != nil {
		return fmt.Errorf("序列化发现记录失败: %w", err)
	}
	item := store.PoolItem{
		ModelCode:       rec.ModelCandidate,
		Record:          datatypes.JSONMap{},
		ConflictCIID:    d.MatchedCIID,
		ReconcileAction: d.Action,
		Reason:          strings.Join(d.Reasons, "；"),
		Status:          "pending",
	}
	_ = json.Unmarshal(raw, &item.Record)
	if err := e.db.WithContext(ctx).Create(&item).Error; err != nil {
		return fmt.Errorf("写入发现池失败: %w", err)
	}
	return nil
}

// fieldSourcesFor 为新建 CI 的全部字段标记来源。
func fieldSourcesFor(attrs map[string]any, source string) datatypes.JSONMap {
	fs := datatypes.JSONMap{}
	for k := range attrs {
		fs[k] = source
	}
	return fs
}

// writeAudit 写入一条 CI 审计记录。
func writeAudit(tx *gorm.DB, ciID, action, source string, changes map[string]Change, message string) error {
	return tx.Create(&store.AuditLog{
		CIID:    ciID,
		Action:  action,
		Source:  source,
		Changes: datatypes.JSONMap(changesToJSON(changes)),
		Message: message,
	}).Error
}

// changesToJSON 把变更明细转为可 JSON 序列化的通用结构。
func changesToJSON(changes map[string]Change) map[string]any {
	out := map[string]any{}
	for k, ch := range changes {
		out[k] = map[string]any{"old": ch.Old, "new": ch.New}
	}
	return out
}

// newValuesOf 提取变更计划中的新值集合。
func newValuesOf(changes map[string]Change) map[string]any {
	out := map[string]any{}
	for k, ch := range changes {
		out[k] = ch.New
	}
	return out
}

// mergedView 返回 CI 属性叠加 overlay 后的视图（不修改原对象）。
func mergedView(base datatypes.JSONMap, overlay map[string]any) map[string]any {
	out := map[string]any{}
	for k, v := range base {
		out[k] = v
	}
	for k, v := range overlay {
		out[k] = v
	}
	return out
}

// equalJSON 比较两个 JSON 值是否相等（数值统一归一为 float64 后比较）。
func equalJSON(a, b any) bool {
	return reflect.DeepEqual(normalizeNumber(a), normalizeNumber(b))
}

// normalizeNumber 把各类数值统一为 float64（含 datatypes.JSONMap 反序列化产生的
// json.Number），无法转换时原样返回。
func normalizeNumber(v any) any {
	switch n := v.(type) {
	case int:
		return float64(n)
	case int32:
		return float64(n)
	case int64:
		return float64(n)
	case float32:
		return float64(n)
	case json.Number:
		if f, err := n.Float64(); err == nil {
			return f
		}
		return v
	default:
		return v
	}
}
