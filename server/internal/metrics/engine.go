// Package metrics 实现数据质量指标引擎（F-080）：
// 按模型计算五指标——属性完整率（Core=必填属性非空占比）、关联完整率
// （有任一关系的 CI 占比；host 特化为有业务归属占比）、孤岛 CI 数、
// 数据鲜度（updated_at 超 7 天占比）、监控覆盖率（host：心跳超 10 分钟占比；
// 反向：n9e targets 无 CMDB CI 占比）。
package metrics

import (
	"context"
	"fmt"
	"math"
	"time"

	"gorm.io/gorm"

	"meridian/server/internal/n9e"
	"meridian/server/internal/store"
)

// 阈值常量。
const (
	// StaleThreshold 为数据鲜度阈值（7 天）。
	StaleThreshold = 7 * 24 * time.Hour
	// HeartbeatThreshold 为无心跳阈值（10 分钟）。
	HeartbeatThreshold = 10 * time.Minute
)

// ModelQuality 是单模型的质量指标快照。
type ModelQuality struct {
	ModelID              string   `json:"model_id"`
	Code                 string   `json:"code"`
	Name                 string   `json:"name"`
	Completeness         float64  `json:"completeness"`               // 属性完整率（0-100）
	RelationCompleteness float64  `json:"relation_completeness"`      // 关联完整率（0-100）
	OrphanCount          int64    `json:"orphan_count"`               // 孤岛 CI 数
	StalePct             float64  `json:"stale_pct"`                  // 超 7 天未更新占比（0-100）
	NoHeartbeatPct       *float64 `json:"no_heartbeat_pct,omitempty"` // 仅 host：超 10 分钟无心跳占比
}

// MonitorQuality 是监控覆盖率双指标（host 域）。
type MonitorQuality struct {
	NoHeartbeatPct float64  `json:"no_heartbeat_pct"`    // CMDB 在用主机无心跳占比（0-100）
	NoCIPct        *float64 `json:"no_ci_pct,omitempty"` // n9e targets 无 CMDB CI 占比（0-100，n9e 未配置时缺省）
}

// QualityReport 是 /dashboard/quality 的完整响应。
type QualityReport struct {
	Models  []ModelQuality `json:"models"`
	Monitor MonitorQuality `json:"monitor"`
}

// 下钻指标编码。
const (
	DrillCompleteness         = "completeness"
	DrillRelationCompleteness = "relation_completeness"
	DrillOrphan               = "orphan"
	DrillStale                = "stale"
	DrillNoHeartbeat          = "no_heartbeat"
)

// DrillMetrics 是合法的下钻指标集合。
var DrillMetrics = map[string]bool{
	DrillCompleteness: true, DrillRelationCompleteness: true, DrillOrphan: true,
	DrillStale: true, DrillNoHeartbeat: true,
}

// Engine 是质量指标引擎。n9eClient 可空（未配置时反向监控指标缺省）。
type Engine struct {
	db        *gorm.DB
	n9eClient *n9e.Client
	now       func() time.Time // 可注入时钟（测试用）
}

// NewEngine 创建指标引擎。
func NewEngine(db *gorm.DB, n9eClient *n9e.Client) *Engine {
	return &Engine{db: db, n9eClient: n9eClient, now: time.Now}
}

// ciFacts 是参与指标计算的 CI 事实集。
type ciFacts struct {
	model      store.Model
	cis        []store.CI
	related    map[string]bool // 有任一关系的 CI
	attributed map[string]bool // 已归属 biz_app 的 CI（一跳）
	coreAttrs  []string        // Core 属性编码（必填项）
}

// loadFacts 加载全部模型的 CI 与关系事实。
func (e *Engine) loadFacts(ctx context.Context) ([]ciFacts, map[string]store.Model, error) {
	var models []store.Model
	if err := e.db.WithContext(ctx).Find(&models).Error; err != nil {
		return nil, nil, fmt.Errorf("查询模型失败: %w", err)
	}
	modelByID := map[string]store.Model{}
	for _, m := range models {
		modelByID[m.ID] = m
	}

	// 关系端点集合（有任一关系的 CI）。
	related := map[string]bool{}
	var rels []store.CIRelation
	if err := e.db.WithContext(ctx).Find(&rels).Error; err != nil {
		return nil, nil, fmt.Errorf("查询关系失败: %w", err)
	}
	for _, r := range rels {
		related[r.SrcCIID] = true
		related[r.DstCIID] = true
	}

	// 业务归属集合：biz_app CI 的全部对端（一跳）。
	attributed := map[string]bool{}
	for _, r := range rels {
		for _, pair := range [][2]string{{r.SrcCIID, r.DstCIID}, {r.DstCIID, r.SrcCIID}} {
			// pair[0] 为候选归属 CI，pair[1] 为对端
			var peer store.CI
			if err := e.db.WithContext(ctx).First(&peer, "id = ?", pair[1]).Error; err != nil {
				continue
			}
			if m, ok := modelByID[peer.ModelID]; ok && m.Code == "biz_app" {
				attributed[pair[0]] = true
			}
		}
	}

	facts := make([]ciFacts, 0, len(models))
	for _, m := range models {
		var cis []store.CI
		if err := e.db.WithContext(ctx).Where("model_id = ? AND status <> ?", m.ID, "retired").
			Find(&cis).Error; err != nil {
			return nil, nil, fmt.Errorf("查询模型 %s 的 CI 失败: %w", m.Code, err)
		}
		core := []string{}
		for _, a := range m.Attributes.Data() {
			if a.Required {
				core = append(core, a.Code)
			}
		}
		facts = append(facts, ciFacts{model: m, cis: cis, related: related, attributed: attributed, coreAttrs: core})
	}
	return facts, modelByID, nil
}

// Quality 计算全模型质量汇总。
func (e *Engine) Quality(ctx context.Context) (QualityReport, error) {
	facts, _, err := e.loadFacts(ctx)
	if err != nil {
		return QualityReport{}, err
	}
	report := QualityReport{Models: []ModelQuality{}}
	for _, f := range facts {
		mq := e.computeModel(f)
		if f.model.Code == "host" {
			pct := e.noHeartbeatPct(f.cis)
			mq.NoHeartbeatPct = &pct
			report.Monitor.NoHeartbeatPct = pct
		}
		report.Models = append(report.Models, mq)
	}
	// 反向指标：n9e targets 无 CMDB CI 占比。
	if e.n9eClient != nil {
		if pct, err := e.noCIPct(ctx, facts); err == nil {
			report.Monitor.NoCIPct = &pct
		} else {
			// n9e 不可达不阻断看板，仅缺省该指标。
			report.Monitor.NoCIPct = nil
		}
	}
	return report, nil
}

// computeModel 计算单模型指标。
func (e *Engine) computeModel(f ciFacts) ModelQuality {
	mq := ModelQuality{
		ModelID: f.model.ID, Code: f.model.Code, Name: f.model.Name,
		Completeness: 100, RelationCompleteness: 100,
	}
	total := len(f.cis)
	if total == 0 {
		return mq
	}
	// 属性完整率：Core 属性（必填项）非空单元格占比；无必填项视为 100%。
	if len(f.coreAttrs) > 0 {
		filled, cells := 0, 0
		for _, ci := range f.cis {
			for _, code := range f.coreAttrs {
				cells++
				if !isEmpty(ci.Attributes[code]) {
					filled++
				}
			}
		}
		mq.Completeness = pct(filled, cells)
	}
	// 关联完整率：host 特化为业务归属占比，其余模型为有任一关系的占比。
	linked, orphans := 0, 0
	for _, ci := range f.cis {
		ok := f.related[ci.ID]
		if f.model.Code == "host" {
			ok = f.attributed[ci.ID]
		}
		if ok {
			linked++
		}
		if !f.related[ci.ID] {
			orphans++
		}
	}
	mq.RelationCompleteness = pct(linked, total)
	mq.OrphanCount = int64(orphans)
	// 数据鲜度：updated_at 超 7 天占比。
	stale := 0
	for _, ci := range f.cis {
		if e.now().Sub(ci.UpdatedAt) > StaleThreshold {
			stale++
		}
	}
	mq.StalePct = pct(stale, total)
	return mq
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

// noHeartbeatPct 计算超 10 分钟无心跳的 CI 占比。
func (e *Engine) noHeartbeatPct(cis []store.CI) float64 {
	if len(cis) == 0 {
		return 0
	}
	dead := 0
	for _, ci := range cis {
		if e.now().Sub(heartbeatRef(ci)) > HeartbeatThreshold {
			dead++
		}
	}
	return pct(dead, len(cis))
}

// noCIPct 计算 n9e targets 中无对应 CMDB host CI 的占比（按 ident 或主 IP 匹配）。
func (e *Engine) noCIPct(ctx context.Context, facts []ciFacts) (float64, error) {
	targets, err := e.n9eClient.ListTargets(ctx)
	if err != nil {
		return 0, err
	}
	if len(targets) == 0 {
		return 0, nil
	}
	// 收集 host CI 的 ident 与 ip 键集合。
	idents, ips := map[string]bool{}, map[string]bool{}
	for _, f := range facts {
		if f.model.Code != "host" {
			continue
		}
		for _, ci := range f.cis {
			if v, ok := ci.Attributes["ident"].(string); ok && v != "" {
				idents[v] = true
			}
			if v, ok := ci.Attributes["ip"].(string); ok && v != "" {
				ips[v] = true
			}
		}
	}
	missing := 0
	for _, t := range targets {
		if idents[t.Ident] || ips[t.HostIP] {
			continue
		}
		missing++
	}
	return pct(missing, len(targets)), nil
}

// Drilldown 返回指定模型指定指标的缺失 CI 分页清单。
func (e *Engine) Drilldown(ctx context.Context, model store.Model, metric string, page, pageSize int) ([]store.CI, int64, error) {
	var cis []store.CI
	if err := e.db.WithContext(ctx).Where("model_id = ? AND status <> ?", model.ID, "retired").
		Order("created_at ASC").Find(&cis).Error; err != nil {
		return nil, 0, fmt.Errorf("查询 CI 失败: %w", err)
	}

	var related, attributed map[string]bool
	needRel := metric == DrillRelationCompleteness || metric == DrillOrphan
	if needRel {
		related, attributed = map[string]bool{}, map[string]bool{}
		var rels []store.CIRelation
		if err := e.db.WithContext(ctx).Find(&rels).Error; err != nil {
			return nil, 0, fmt.Errorf("查询关系失败: %w", err)
		}
		modelByID := map[string]store.Model{}
		for _, r := range rels {
			related[r.SrcCIID] = true
			related[r.DstCIID] = true
			if model.Code != "host" {
				continue
			}
			for _, pair := range [][2]string{{r.SrcCIID, r.DstCIID}, {r.DstCIID, r.SrcCIID}} {
				var peer store.CI
				if err := e.db.WithContext(ctx).First(&peer, "id = ?", pair[1]).Error; err != nil {
					continue
				}
				pm, ok := modelByID[peer.ModelID]
				if !ok {
					if err := e.db.WithContext(ctx).First(&pm, "id = ?", peer.ModelID).Error; err != nil {
						continue
					}
					modelByID[pm.ID] = pm
				}
				if pm.Code == "biz_app" {
					attributed[pair[0]] = true
				}
			}
		}
	}

	core := []string{}
	for _, a := range model.Attributes.Data() {
		if a.Required {
			core = append(core, a.Code)
		}
	}

	missing := []store.CI{}
	for _, ci := range cis {
		if e.isMissing(ci, model, metric, core, related, attributed) {
			missing = append(missing, ci)
		}
	}
	total := int64(len(missing))
	start := (page - 1) * pageSize
	if start >= len(missing) {
		return []store.CI{}, total, nil
	}
	end := int(math.Min(float64(start+pageSize), float64(len(missing))))
	return missing[start:end], total, nil
}

// isMissing 判定 CI 是否落入指定指标的缺失集。
func (e *Engine) isMissing(ci store.CI, model store.Model, metric string, core []string, related, attributed map[string]bool) bool {
	switch metric {
	case DrillCompleteness:
		for _, code := range core {
			if isEmpty(ci.Attributes[code]) {
				return true
			}
		}
		return false
	case DrillRelationCompleteness:
		if model.Code == "host" {
			return !attributed[ci.ID]
		}
		return !related[ci.ID]
	case DrillOrphan:
		return !related[ci.ID]
	case DrillStale:
		return e.now().Sub(ci.UpdatedAt) > StaleThreshold
	case DrillNoHeartbeat:
		if model.Code != "host" {
			return false
		}
		return e.now().Sub(heartbeatRef(ci)) > HeartbeatThreshold
	}
	return false
}

// pct 计算百分比（0-100），分母为 0 时返回 0。
func pct(part, total int) float64 {
	if total == 0 {
		return 0
	}
	return math.Round(float64(part)*10000/float64(total)) / 100
}

// isEmpty 判定属性值是否为空。
func isEmpty(v any) bool {
	if v == nil {
		return true
	}
	if s, ok := v.(string); ok {
		return s == ""
	}
	return false
}
