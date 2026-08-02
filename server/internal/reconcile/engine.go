// Package reconcile 实现调和引擎 MVP：按模型配置的调和键（reconcile_keys，按优先级排序）
// 判定发现记录与存量 CI 的同一性——
//   - 命中主调和键（或仅提供了次要键且与存量一致）：更新（按来源优先级做字段级合并并记审计）；
//   - 未命中任何键：创建 status=discovered 的 CI 入发现池；
//   - 命中次要键但其他键不符：判定 conflict 转入发现池待人工裁决。
//
// 主机模型的调和键链为 ["instance_uuid","cloud_instance_id","serial_no","ip","ident"]：
// 稳定 UID（vCenter UUID / 云实例 ID / 序列号）优先，保证改名、改 IP 后仍识别为同一资产；
// 主 IP 是 n9e 数据口径下的兜底锚点；ident 仅作末位兜底线索——命中但 IP 不符时
// 只会产生冲突入池（人工裁决），绝不会造成错误的自动合并。
// 调和建档/更新后自动维护 CI 与 IPAM 地址记录（ip_addresses.ci_id）的关联——
// IPAM 是 IP 的登记权威源，引擎只挂接已登记的 IP，不自动创建 IPAM 条目。
package reconcile

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/netip"
	"reflect"
	"strings"
	"time"

	"gorm.io/datatypes"
	"gorm.io/gorm"

	"meridian/server/internal/ipam"
	"meridian/server/internal/store"
	"meridian/server/internal/validation"
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
// 键名必须等于采集器实际发送的 Source 值（collectors 各包 Source 常量）。
var sourcePriorities = map[string]int{
	"manual":  100,
	"n9e":     80,
	"vsphere": 70,
	"aliyun":  70,
	"volc":    70,
	"ip_scan": 60,
}

const defaultSourcePriority = 50

// PriorityOf 返回来源标识的优先级。
func PriorityOf(source string) int {
	if p, ok := sourcePriorities[source]; ok {
		return p
	}
	return defaultSourcePriority
}

// PostHook 是 CI 建档/更新落库成功后的后置钩子（如自动关联器）。
// 钩子异步执行（独立 goroutine），失败仅记日志，不影响调和主流程。
type PostHook func(ctx context.Context, ciID, action string) error

// Engine 是调和引擎。
type Engine struct {
	db    *gorm.DB
	hooks []PostHook
}

// NewEngine 创建调和引擎。
func NewEngine(db *gorm.DB) *Engine {
	return &Engine{db: db}
}

// AddPostHook 注册 CI 建档/更新成功后的异步后置钩子（可注册多个，按序触发）。
func (e *Engine) AddPostHook(h PostHook) {
	e.hooks = append(e.hooks, h)
}

// fireHooks 在 CI 落库成功后异步触发全部后置钩子。
// 使用与请求解耦的 background 上下文（HTTP 请求结束不影响钩子执行），
// panic/错误均只记日志。
func (e *Engine) fireHooks(ciID, action string) {
	if ciID == "" || len(e.hooks) == 0 {
		return
	}
	for _, h := range e.hooks {
		hook := h
		go func() {
			defer func() {
				if r := recover(); r != nil {
					log.Printf("调和后置钩子 panic（CI %s，动作 %s）: %v", ciID, action, r)
				}
			}()
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			if err := hook(ctx, ciID, action); err != nil {
				log.Printf("调和后置钩子执行失败（CI %s，动作 %s）: %v", ciID, action, err)
			}
		}()
	}
}

// Evaluate 对一条发现记录执行调和判定。
// dryRun 为 true 时只计算判定结果不落库（供 /reconcile/preview 使用）。
func (e *Engine) Evaluate(ctx context.Context, rec Record, dryRun bool) (Decision, error) {
	// 1. 解析候选模型。
	var model store.Model
	if err := e.db.WithContext(ctx).Where("code = ?", rec.ModelCandidate).First(&model).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			d := Decision{
				Action:  ActionPool,
				Reasons: []string{fmt.Sprintf("候选模型 %q 不存在，记录转入发现池", rec.ModelCandidate)},
			}
			return d, e.commitPool(ctx, rec, nil, d, dryRun)
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
		return d, e.commitPool(ctx, rec, nil, d, dryRun)
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
		return d, e.commitPool(ctx, rec, keys, d, dryRun)

	case matchedID == "":
		// 未命中任何键：新建 CI 入发现池。
		d, err := e.createCI(ctx, rec, model, keys, dryRun)
		if err == nil && !dryRun {
			e.fireHooks(d.MatchedCIID, d.Action)
		}
		return d, err

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
				return d, e.commitPool(ctx, rec, keys, d, dryRun)
			}
		}
		d, err := e.updateCI(ctx, rec, model, ci, keys[matchedIdx], dryRun)
		if err == nil && !dryRun {
			e.fireHooks(d.MatchedCIID, d.Action)
		}
		return d, err
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
// 不一致（如 IP 复用：同 IP 但 instance_uuid 不同）则判定冲突。
// 记录或存量 CI 未携带某更优先键时跳过——无法据此证伪
// （典型场景：n9e 建档的 CI 尚无 cloud_instance_id，云采集器首次上报
// 应按 IP 合并并补全 UID 字段，而非误判冲突）。
func crossCheckKeys(ci store.CI, rec Record, keys []string, matchedIdx int) (string, bool) {
	for j := 0; j < matchedIdx; j++ {
		key := keys[j]
		rv, ok := rec.Attributes[key]
		if !ok || rv == nil || rv == "" {
			continue // 记录未携带该更优先键，无法据此证伪
		}
		cv, exists := ci.Attributes[key]
		if !exists || cv == nil || cv == "" {
			continue // 存量 CI 尚无该键值，无法据此证伪（本次更新将补全）
		}
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
		return d, e.commitPool(ctx, rec, keys, d, dryRun)
	}
	if errs := validation.ValidateUnique(e.db.WithContext(ctx), model.ID, model.Attributes.Data(), rec.Attributes, ""); errs != nil {
		d := Decision{
			Action:  ActionPool,
			Reasons: []string{fmt.Sprintf("未命中调和键 %v，但唯一性校验未通过，转入发现池: %s", keys, errs.Error())},
		}
		return d, e.commitPool(ctx, rec, keys, d, dryRun)
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
		if ip := normalizeIP(rec.Attributes["ip"]); ip != "" {
			if err := ipam.RelinkCI(tx, ci.ID, "", ip); err != nil {
				return err
			}
		}
		// 黑设备风险（2B）：主机建档即带 black_device_risk=true 时写一条 warning 告警事件。
		if model.Code == "host" && truthy(rec.Attributes["black_device_risk"]) {
			if err := writeBlackDeviceAlert(tx, ci.ID, rec); err != nil {
				return err
			}
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

	// 4. 落库：应用变更计划并记审计；同步维护 CI↔IPAM 关联
	//（IP 变更时解除旧 IP 挂载，最终生效 IP 幂等挂接到本 CI）。
	var oldIP string
	if ch, ok := changes["ip"]; ok {
		oldIP = normalizeIP(ch.Old)
	}
	ApplyChanges(&ci, changes, rec.Source)
	newIP := normalizeIP(ci.Attributes["ip"])
	err := e.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&store.CI{}).Where("id = ?", ci.ID).
			Updates(map[string]any{"attributes": ci.Attributes, "field_sources": ci.FieldSources}).Error; err != nil {
			return fmt.Errorf("更新 CI 失败: %w", err)
		}
		if err := ipam.RelinkCI(tx, ci.ID, oldIP, newIP); err != nil {
			return err
		}
		return writeAudit(tx, ci.ID, "update", rec.Source, changes, "调和更新 CI 属性")
	})
	if err != nil {
		return Decision{}, err
	}
	return d, nil
}

// PlanMerge 计算 incoming 相对 CI 现状的字段级合并计划（不修改 ci）：
//   - 字段在 CI 上已有值时，仅当上报来源优先级不低于字段既有来源才产生变更；
//   - 字段在 CI 上尚无值（如 n9e 建档的 CI 缺少 cloud_instance_id）时，
//     任何来源都可补全——空槽位不存在来源权威冲突。
func PlanMerge(ci *store.CI, incoming map[string]any, source string) (changes map[string]Change, skipped []string) {
	changes = map[string]Change{}
	for k, v := range incoming {
		if _, hasValue := ci.Attributes[k]; hasValue {
			existingSrc, _ := ci.FieldSources[k].(string)
			if existingSrc == "" {
				existingSrc = ci.Source
			}
			if PriorityOf(source) < PriorityOf(existingSrc) {
				skipped = append(skipped, k)
				continue
			}
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
// D-02：写入前按 (model_candidate + 记录同一性哈希) 查询 status=ignored 条目，
// 命中说明同一记录已被人工忽略，本次静默丢弃（原始层落库不受本函数影响）。
func (e *Engine) commitPool(ctx context.Context, rec Record, keys []string, d Decision, dryRun bool) error {
	if dryRun {
		return nil
	}
	hash := recordHash(rec, keys)
	var ignored int64
	if err := e.db.WithContext(ctx).Model(&store.PoolItem{}).
		Where("model_code = ? AND record_hash = ? AND status = ?", rec.ModelCandidate, hash, "ignored").
		Count(&ignored).Error; err != nil {
		return fmt.Errorf("查询已忽略池条目失败: %w", err)
	}
	if ignored > 0 {
		// 同一记录已被人工忽略：静默丢弃，不再入 pending。
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
		RecordHash:      hash,
		ReconcileAction: d.Action,
		Reason:          strings.Join(d.Reasons, "；"),
		Status:          "pending",
	}
	_ = json.Unmarshal(raw, &item.Record)
	if err := e.db.WithContext(ctx).Create(&item).Error; err != nil {
		return fmt.Errorf("写入发现池失败: %w", err)
	}
	// 黑设备风险（AC-F043-01）：host 记录带 black_device_risk=true 入池时同步产生告警事件；
	// 按相同 detail 且未确认去重，避免扫描周期反复刷屏。
	if rec.ModelCandidate == "host" && truthy(rec.Attributes["black_device_risk"]) {
		ident, _ := rec.Attributes["ident"].(string)
		ip, _ := rec.Attributes["ip"].(string)
		detail := fmt.Sprintf("扫描发现未登记存活主机（ident=%s，ip=%s，来源=%s/%s）进入发现池，疑似黑设备，请核查",
			ident, ip, rec.Source, rec.Collector)
		var existing int64
		if err := e.db.WithContext(ctx).Model(&store.AlertEvent{}).
			Where("detail = ? AND acknowledged = ?", detail, false).
			Count(&existing).Error; err != nil {
			return fmt.Errorf("查询存量告警失败: %w", err)
		}
		if existing == 0 {
			if err := e.db.WithContext(ctx).Create(&store.AlertEvent{
				Level:  store.AlertLevelWarning,
				Title:  "发现疑似黑设备（未登记存活 IP）",
				Source: rec.Source,
				Detail: detail,
			}).Error; err != nil {
				return fmt.Errorf("写入黑设备告警失败: %w", err)
			}
		}
	}
	return nil
}

// recordHash 计算发现记录的同一性哈希（D-02）：
// 哈希输入 = model_candidate + 模型调和键（reconcile_keys）对应属性值按序拼接；
// 模型无调和键时退化为 model_candidate + 全属性 JSON 哈希
// （Go json.Marshal 对 map 键排序，输出确定）。
func recordHash(rec Record, keys []string) string {
	h := sha256.New()
	h.Write([]byte(rec.ModelCandidate))
	h.Write([]byte{0})
	if len(keys) > 0 {
		for _, k := range keys {
			b, _ := json.Marshal(rec.Attributes[k])
			h.Write([]byte(k))
			h.Write([]byte{0})
			h.Write(b)
			h.Write([]byte{0})
		}
	} else {
		b, _ := json.Marshal(rec.Attributes)
		h.Write(b)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// normalizeIP 把属性值归一化为 netip 字符串形式（与 IPAM 存储口径一致）；
// 非字符串或解析失败时返回空串（跳过 IPAM 挂接，不影响调和主流程）。
func normalizeIP(v any) string {
	s, ok := v.(string)
	if !ok || s == "" {
		return ""
	}
	addr, err := netip.ParseAddr(strings.TrimSpace(s))
	if err != nil {
		return ""
	}
	return addr.String()
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

// truthy 判定属性值是否为真（bool true 或字符串 "true"，兼容 JSON 上报的两种形态）。
func truthy(v any) bool {
	switch t := v.(type) {
	case bool:
		return t
	case string:
		return strings.EqualFold(strings.TrimSpace(t), "true")
	default:
		return false
	}
}

// writeBlackDeviceAlert 为黑设备风险主机写入一条 warning 级告警事件。
func writeBlackDeviceAlert(tx *gorm.DB, ciID string, rec Record) error {
	ident, _ := rec.Attributes["ident"].(string)
	ip, _ := rec.Attributes["ip"].(string)
	detail := fmt.Sprintf("调和建档主机（ident=%s，ip=%s，来源=%s/%s）携带 black_device_risk=true 标记，疑似未登记黑设备，请核查",
		ident, ip, rec.Source, rec.Collector)
	return tx.Create(&store.AlertEvent{
		Level:  store.AlertLevelWarning,
		Title:  "发现黑设备风险主机",
		Source: rec.Source,
		CIID:   ciID,
		Detail: detail,
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
