// Package scope 实现数据范围权限（F-005）的查询层收口组件：
// 给定用户，预计算其归属应用（scope_app_ids 指向的 biz_app CI）的资产闭包——
// 沿 belongs_to/deployed_on/runs_on/depends_on/in_namespace/contains/mounted_to
// 关系双向一跳，命名空间（k8s_namespace）再向外两跳；多重归属取并集。
//
// 范围在查询层实施，不污染存储层：CI 不打归属标签副本。
// 闭包按 (用户, 范围) 做 10 秒短缓存（请求级突发合并），范围变更因缓存键
// 含范围内容而即时生效（AC-F005-06）。
//
// 业务模型（biz_line/biz_app）与共享基础设施（机柜/机房/网络设备）对范围内
// 用户全量只读可见（一期不裁剪，AC-F005-07）；无归属 CI 仅全量角色可见。
package scope

import (
	"context"
	"strings"
	"sync"
	"time"

	"gorm.io/gorm"

	"meridian/server/internal/store"
)

// cacheTTL 为闭包短缓存 TTL（请求级缓存，禁长缓存——范围变更经缓存键即时生效）。
const cacheTTL = 10 * time.Second

// trackedRelations 为归属闭包遍历的关系编码（双向）。
// mounted_to 为命名空间→应用的挂接关系，纳入以支撑命名空间两跳。
var trackedRelations = map[string]bool{
	"belongs_to":   true,
	"deployed_on":  true,
	"runs_on":      true,
	"depends_on":   true,
	"in_namespace": true,
	"contains":     true,
	"mounted_to":   true,
}

// bizModels 为业务模型：范围内用户全量只读可见（应用/业务线本身是导航入口）。
var bizModels = map[string]bool{"biz_app": true, "biz_line": true}

// sharedInfraModels 为共享基础设施模型（IPAM/DCIM/机柜域）：一期全量只读不裁剪。
var sharedInfraModels = map[string]bool{"rack": true, "room": true, "network_device": true}

// namespaceModel 为命名空间模型编码：闭包到达命名空间后再向外扩一跳。
const namespaceModel = "k8s_namespace"

// Resolver 预计算并缓存用户的资产可见闭包。
type Resolver struct {
	db *gorm.DB

	mu      sync.Mutex
	cache   map[string]cacheEntry
	nowFunc func() time.Time // 可注入时钟（测试用）
}

type cacheEntry struct {
	set     map[string]bool
	expires time.Time
}

// New 创建数据范围解析器。
func New(db *gorm.DB) *Resolver {
	return &Resolver{db: db, cache: map[string]cacheEntry{}, nowFunc: time.Now}
}

// VisibleSet 返回用户的可见 CI 集合。
// restricted=false 表示用户不受数据范围约束（scope_app_ids 为空），调用方不应过滤；
// restricted=true 时 set 为可见 CI ID 闭包（可能为空集——此时用户看不到任何受限资产）。
func (r *Resolver) VisibleSet(ctx context.Context, user *store.User) (set map[string]bool, restricted bool, err error) {
	appIDs := user.ScopeAppIDs.Data()
	if len(appIDs) == 0 {
		return nil, false, nil
	}
	// 缓存键含范围内容：范围变更即时生效（不等 TTL 过期）。
	key := user.ID + "|" + strings.Join(appIDs, ",")
	r.mu.Lock()
	if ent, ok := r.cache[key]; ok && r.nowFunc().Before(ent.expires) {
		r.mu.Unlock()
		return ent.set, true, nil
	}
	r.mu.Unlock()

	set, err = r.compute(ctx, appIDs)
	if err != nil {
		return nil, false, err
	}
	r.mu.Lock()
	r.cache[key] = cacheEntry{set: set, expires: r.nowFunc().Add(cacheTTL)}
	r.mu.Unlock()
	return set, true, nil
}

// compute 全量加载模型/CI/关系后在内存中计算闭包。
// ponytail: 全表扫描 + 内存图遍历，CEILING 为 CI/关系规模十万级以内；
// 超出后升级为按 scope_app_ids 起点的递归 SQL（PG recursive CTE）。
func (r *Resolver) compute(ctx context.Context, appIDs []string) (map[string]bool, error) {
	var models []store.Model
	if err := r.db.WithContext(ctx).Find(&models).Error; err != nil {
		return nil, err
	}
	codeByID := make(map[string]string, len(models))
	for _, m := range models {
		codeByID[m.ID] = m.Code
	}

	var cis []store.CI
	if err := r.db.WithContext(ctx).Select("id", "model_id").Find(&cis).Error; err != nil {
		return nil, err
	}

	// 邻接表：仅保留 trackedRelations 内的关系（双向）。
	var rels []store.CIRelation
	if err := r.db.WithContext(ctx).Select("relation_code", "src_ci_id", "dst_ci_id").Find(&rels).Error; err != nil {
		return nil, err
	}
	adj := map[string][]string{}
	for _, rel := range rels {
		if !trackedRelations[rel.RelationCode] {
			continue
		}
		adj[rel.SrcCIID] = append(adj[rel.SrcCIID], rel.DstCIID)
		adj[rel.DstCIID] = append(adj[rel.DstCIID], rel.SrcCIID)
	}

	set := map[string]bool{}
	for _, id := range appIDs {
		set[id] = true
	}
	// 一跳：归属应用直接关联的资产（含命名空间）。
	queue := append([]string{}, appIDs...)
	namespaces := []string{}
	for _, id := range queue {
		for _, peer := range adj[id] {
			if set[peer] {
				continue
			}
			set[peer] = true
			namespaces = append(namespaces, peer)
		}
	}
	// 两跳：仅从命名空间再向外扩一跳（命名空间内的工作负载/服务等）。
	ciModel := make(map[string]string, len(cis))
	for _, ci := range cis {
		ciModel[ci.ID] = codeByID[ci.ModelID]
	}
	for _, id := range namespaces {
		if ciModel[id] != namespaceModel {
			continue
		}
		for _, peer := range adj[id] {
			set[peer] = true
		}
	}
	// 业务模型与共享基础设施全量只读可见。
	for _, ci := range cis {
		code := ciModel[ci.ID]
		if bizModels[code] || sharedInfraModels[code] {
			set[ci.ID] = true
		}
	}
	return set, nil
}
