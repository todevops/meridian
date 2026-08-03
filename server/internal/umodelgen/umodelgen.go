// Package umodelgen 实现 UModel 实体/关联生成器（F-073）：
// 订阅调和 PostHook（CI create/update）把 CI upsert 为 EntitySet 实体、
// 关系 upsert 为 EntitySetLink；每日全量对账兜底。
// 口径（阶段三已决）：CMDB 为主、EntityStore 为从，主键复用调和主键保证幂等；
// 删除走"标记下线 + 保活过期"（retired 实体写 tombstone，保活到期由 EntityStore 置 dead）。
// 连接配置：env UMODEL_STORE_URL（默认 :19011）与 UMODEL_TOKEN（默认 dev-umodel-token）。
package umodelgen

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"gorm.io/gorm"

	"meridian/server/internal/store"
)

// 保活秒数。
const (
	// KeepAliveSeconds 是在用实体的保活时长（24h，每日对账刷新）。
	KeepAliveSeconds = 86400
	// TombstoneKeepAliveSeconds 是 tombstone 实体的保活时长：
	// 极短保活使 EntityStore 随即判定过期（dead），即"标记下线 + 保活过期"。
	TombstoneKeepAliveSeconds = 1
)

// entitySetByModel 是模型 → EntitySet 映射码表（未映射的模型跳过）。
var entitySetByModel = map[string]string{
	"host":            "infra.host",
	"network_device":  "infra.network_device",
	"virtual_machine": "infra.vm",
	"db_instance":     "mw.db_instance",
	"k8s_cluster":     "k8s.cluster",
	"k8s_namespace":   "k8s.namespace",
	"k8s_workload":    "k8s.workload",
	"biz_app":         "apm.service",
	"esxi_host":       "infra.esxi",
}

// linkTypeByRelation 是关系码 → EntitySetLink link_type 映射码表（未映射的关系跳过）。
var linkTypeByRelation = map[string]string{
	"runs_on":      "runs_on",
	"contains":     "contains",
	"connected_to": "connected_to",
	"depends_on":   "depends_on",
	"deployed_on":  "deployed_on",
}

// Stats 是生成器统计计数（GET /api/v1/integrations/umodel/stats 响应）。
// 计数为进程内存态（重启清零），last_sync 为最近一次每日全量对账完成时间。
type Stats struct {
	EntityUpserts int64  `json:"entity_upserts"`
	LinkUpserts   int64  `json:"link_upserts"`
	Tombstones    int64  `json:"tombstones"`
	LastSync      string `json:"last_sync"` // RFC3339；从未对账为空串
}

// Generator 是 UModel 生成器。
type Generator struct {
	db     *gorm.DB
	client *Client

	mu           sync.Mutex
	entityUpsert int64
	linkUpsert   int64
	tombstones   int64
	lastSync     time.Time
}

// New 创建生成器。client 可空（未配置时写入全部跳过，仅统计不变）。
func New(db *gorm.DB, client *Client) *Generator {
	return &Generator{db: db, client: client}
}

// NewFromEnv 按环境变量创建生成器：UMODEL_STORE_URL（默认 :19011）、
// UMODEL_TOKEN（默认 dev-umodel-token）。
func NewFromEnv(db *gorm.DB) *Generator {
	return New(db, NewClientFromEnv())
}

// StatsSnapshot 返回当前统计计数快照。
func (g *Generator) StatsSnapshot() Stats {
	g.mu.Lock()
	defer g.mu.Unlock()
	s := Stats{
		EntityUpserts: g.entityUpsert,
		LinkUpserts:   g.linkUpsert,
		Tombstones:    g.tombstones,
	}
	if !g.lastSync.IsZero() {
		s.LastSync = g.lastSync.UTC().Format(time.RFC3339)
	}
	return s
}

// attrString 读取字符串属性（去空白）。
func attrString(attrs map[string]any, key string) string {
	v, ok := attrs[key].(string)
	if !ok {
		return ""
	}
	return strings.TrimSpace(v)
}

// primaryKey 取实体的调和主键：模型 reconcile_keys 中首个非空属性值；
// 模型未配置调和键或值全空时回退 CI ID（保证幂等一一对应）。
func primaryKey(model store.Model, ci store.CI) string {
	for _, key := range model.ReconcileKeys.Data() {
		if v := attrString(ci.Attributes, key); v != "" {
			return v
		}
	}
	return ci.ID
}

// Handle 是挂到调和引擎的后置钩子：CI 建档/更新成功后 upsert 对应实体与其关系。
// 签名与 reconcile.PostHook 一致；失败仅返回错误由调用方记日志，不阻断调和。
func (g *Generator) Handle(ctx context.Context, ciID, _ string) error {
	if g.client == nil {
		return nil
	}
	ci, model, err := g.loadCIWithModel(ctx, ciID)
	if err != nil {
		return err
	}
	if err := g.upsertEntity(ctx, ci, model); err != nil {
		return err
	}
	// 本 CI 参与的全部已映射关系（出向 + 入向，一次查询）。
	var rels []store.CIRelation
	if err := g.db.WithContext(ctx).
		Where("src_ci_id = ? OR dst_ci_id = ?", ciID, ciID).Find(&rels).Error; err != nil {
		return fmt.Errorf("查询 CI %s 的关系失败: %w", ciID, err)
	}
	return g.upsertLinks(ctx, rels)
}

// loadCIWithModel 加载 CI 及其模型。
func (g *Generator) loadCIWithModel(ctx context.Context, ciID string) (store.CI, store.Model, error) {
	var ci store.CI
	if err := g.db.WithContext(ctx).First(&ci, "id = ?", ciID).Error; err != nil {
		return ci, store.Model{}, fmt.Errorf("加载 CI %s 失败: %w", ciID, err)
	}
	var model store.Model
	if err := g.db.WithContext(ctx).First(&model, "id = ?", ci.ModelID).Error; err != nil {
		return ci, model, fmt.Errorf("加载 CI %s 的模型失败: %w", ciID, err)
	}
	return ci, model, nil
}

// upsertEntity 把单个 CI upsert 到 EntityStore：
// 未映射模型跳过；retired 写 tombstone（极简属性 + 极短保活），其余写全量属性 + 24h 保活。
func (g *Generator) upsertEntity(ctx context.Context, ci store.CI, model store.Model) error {
	set, ok := entitySetByModel[model.Code]
	if !ok {
		return nil
	}
	pk := primaryKey(model, ci)
	if ci.Status == "retired" {
		if err := g.client.UpsertEntity(ctx, set, pk, map[string]any{
			"ci_id":     ci.ID,
			"model":     model.Code,
			"tombstone": true,
		}, TombstoneKeepAliveSeconds); err != nil {
			return fmt.Errorf("写入 tombstone 实体 %s/%s 失败: %w", set, pk, err)
		}
		g.mu.Lock()
		g.tombstones++
		g.mu.Unlock()
		return nil
	}
	attrs := map[string]any{"ci_id": ci.ID, "model": model.Code, "status": ci.Status}
	for k, v := range ci.Attributes {
		attrs[k] = v
	}
	if err := g.client.UpsertEntity(ctx, set, pk, attrs, KeepAliveSeconds); err != nil {
		return fmt.Errorf("upsert 实体 %s/%s 失败: %w", set, pk, err)
	}
	g.mu.Lock()
	g.entityUpsert++
	g.mu.Unlock()
	return nil
}

// upsertLinks 把关系批量 upsert 为 EntitySetLink：
// 只处理映射码表内的关系，且两端实体的模型均已映射；关联写入两端各自的
// EntitySet（同 set 只写一次），保证 mock EntityStore 图查询双向可达。
func (g *Generator) upsertLinks(ctx context.Context, rels []store.CIRelation) error {
	// 收集涉及的全部端点 CI，批量加载（避免逐端点查询）。
	endpointIDs := map[string]bool{}
	filtered := make([]store.CIRelation, 0, len(rels))
	for _, r := range rels {
		if _, ok := linkTypeByRelation[r.RelationCode]; !ok {
			continue
		}
		filtered = append(filtered, r)
		endpointIDs[r.SrcCIID] = true
		endpointIDs[r.DstCIID] = true
	}
	if len(filtered) == 0 {
		return nil
	}
	ids := make([]string, 0, len(endpointIDs))
	for id := range endpointIDs {
		ids = append(ids, id)
	}
	var cis []store.CI
	if err := g.db.WithContext(ctx).Where("id IN ?", ids).Find(&cis).Error; err != nil {
		return fmt.Errorf("批量加载关系端点 CI 失败: %w", err)
	}
	ciByID := map[string]store.CI{}
	modelIDs := map[string]bool{}
	for _, ci := range cis {
		ciByID[ci.ID] = ci
		modelIDs[ci.ModelID] = true
	}
	var models []store.Model
	midList := make([]string, 0, len(modelIDs))
	for id := range modelIDs {
		midList = append(midList, id)
	}
	if err := g.db.WithContext(ctx).Where("id IN ?", midList).Find(&models).Error; err != nil {
		return fmt.Errorf("批量加载模型失败: %w", err)
	}
	modelByID := map[string]store.Model{}
	for _, m := range models {
		modelByID[m.ID] = m
	}

	linksBySet := map[string][]Link{}
	count := 0
	for _, r := range filtered {
		src, ok1 := ciByID[r.SrcCIID]
		dst, ok2 := ciByID[r.DstCIID]
		if !ok1 || !ok2 {
			continue
		}
		srcModel, ok1 := modelByID[src.ModelID]
		dstModel, ok2 := modelByID[dst.ModelID]
		if !ok1 || !ok2 {
			continue
		}
		srcSet, ok1 := entitySetByModel[srcModel.Code]
		dstSet, ok2 := entitySetByModel[dstModel.Code]
		if !ok1 || !ok2 {
			continue // 端点模型未映射（如 biz_line、rack），跳过
		}
		link := Link{
			SrcPK:    primaryKey(srcModel, src),
			DstPK:    primaryKey(dstModel, dst),
			LinkType: linkTypeByRelation[r.RelationCode],
		}
		linksBySet[srcSet] = append(linksBySet[srcSet], link)
		if dstSet != srcSet {
			linksBySet[dstSet] = append(linksBySet[dstSet], link)
		}
		count++
	}
	for set, links := range linksBySet {
		if err := g.client.UpsertLinks(ctx, set, links); err != nil {
			return fmt.Errorf("upsert 关联（EntitySet %s）失败: %w", set, err)
		}
	}
	g.mu.Lock()
	g.linkUpsert += int64(count)
	g.mu.Unlock()
	return nil
}

// ReconcileAll 每日全量对账：遍历全部已映射模型的 CI 重写实体（在用刷新保活、
// retired 补 tombstone），再重写全部已映射关系；完成后刷新 last_sync。
// 幂等：主键与关联键稳定，重跑产生完全相同的写入集合。
// 主键冲突纪律：调和引擎不匹配 retired CI——退役主机被重新发现会另建新 CI，
// 新旧 CI 可能共享同一调和主键；故分两遍写入（先 tombstone 后在用），
// 保证冲突时"在用实体"恒为最终态（最新现实优先）。
func (g *Generator) ReconcileAll(ctx context.Context) error {
	if g.client == nil {
		return nil
	}
	var models []store.Model
	if err := g.db.WithContext(ctx).Find(&models).Error; err != nil {
		return fmt.Errorf("加载模型失败: %w", err)
	}
	type item struct {
		ci    store.CI
		model store.Model
	}
	var tombstoned, live []item
	for _, m := range models {
		if _, ok := entitySetByModel[m.Code]; !ok {
			continue
		}
		var cis []store.CI
		if err := g.db.WithContext(ctx).Where("model_id = ?", m.ID).Find(&cis).Error; err != nil {
			return fmt.Errorf("查询模型 %s 的 CI 失败: %w", m.Code, err)
		}
		for _, ci := range cis {
			if ci.Status == "retired" {
				tombstoned = append(tombstoned, item{ci, m})
			} else {
				live = append(live, item{ci, m})
			}
		}
	}
	for _, it := range append(tombstoned, live...) {
		if err := g.upsertEntity(ctx, it.ci, it.model); err != nil {
			return err
		}
	}
	codes := make([]string, 0, len(linkTypeByRelation))
	for code := range linkTypeByRelation {
		codes = append(codes, code)
	}
	var rels []store.CIRelation
	if err := g.db.WithContext(ctx).Where("relation_code IN ?", codes).Find(&rels).Error; err != nil {
		return fmt.Errorf("查询关系失败: %w", err)
	}
	if err := g.upsertLinks(ctx, rels); err != nil {
		return err
	}
	g.mu.Lock()
	g.lastSync = time.Now()
	g.mu.Unlock()
	return nil
}

// RunDailyLoop 每日全量对账通道：启动即跑一轮，之后每 24 小时一轮，直到 ctx 取消。
func (g *Generator) RunDailyLoop(ctx context.Context) {
	run := func() {
		if err := g.ReconcileAll(ctx); err != nil {
			log.Printf("UModel 每日全量对账失败: %v", err)
			return
		}
		s := g.StatsSnapshot()
		log.Printf("UModel 每日全量对账完成: 实体 upsert 累计 %d、关联 upsert 累计 %d、tombstone 累计 %d",
			s.EntityUpserts, s.LinkUpserts, s.Tombstones)
	}
	run()
	ticker := time.NewTicker(24 * time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			run()
		}
	}
}
