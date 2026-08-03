// Package aggregation 实现应用系统聚合查询（F-027）：
// 两级业务树（biz_line→biz_app）、应用详情一屏聚合（主机/数据库/K8s 工作负载/
// IP/云资源）、应用依赖拓扑（两跳以内）与资源影响面反查（入向两跳）。
// 性能纪律：全部按应用/关系批量预取组装（每类数据一次查询），禁止逐 CI 拉关系（N+1）。
package aggregation

import (
	"context"
	"fmt"
	"net/netip"
	"sort"
	"strings"

	"gorm.io/gorm"

	"meridian/server/internal/store"
)

// 聚合涉及的模型与关系编码常量。
const (
	modelBizLine      = "biz_line"
	modelBizApp       = "biz_app"
	modelHost         = "host"
	modelDBInstance   = "db_instance"
	modelK8sNamespace = "k8s_namespace"
	modelK8sWorkload  = "k8s_workload"

	relBelongsTo   = "belongs_to"
	relDeployedOn  = "deployed_on"
	relDependsOn   = "depends_on"
	relRunsOn      = "runs_on"
	relMountedTo   = "mounted_to"
	relInNamespace = "in_namespace"
)

// impactRelCodes 是影响面反查沿行的入向关系码集合。
var impactRelCodes = []string{relBelongsTo, relDeployedOn, relDependsOn, relRunsOn}

// Service 是应用聚合查询服务。
type Service struct {
	db *gorm.DB
}

// NewService 创建聚合查询服务。
func NewService(db *gorm.DB) *Service {
	return &Service{db: db}
}

// ---- 契约视图类型 ----

// AppBrief 是应用精简视图（树节点与聚合共用）。
type AppBrief struct {
	ID    string `json:"id"`
	Code  string `json:"code"`
	Name  string `json:"name"`
	Owner string `json:"owner"`
	Level string `json:"level"`
}

// LineNode 是业务线节点（含应用数/主机数汇总与下属应用）。
type LineNode struct {
	AppBrief
	AppCount  int        `json:"app_count"`
	HostCount int        `json:"host_count"`
	Apps      []AppBrief `json:"apps"`
}

// TreeView 是两级业务树响应。
type TreeView struct {
	Lines      []LineNode `json:"lines"`
	Unassigned []AppBrief `json:"unassigned"`
}

// HostView 是聚合页主机条目。
type HostView struct {
	ID     string `json:"id"`
	Ident  string `json:"ident"`
	IP     string `json:"ip"`
	Status string `json:"status"`
	Source string `json:"source"`
}

// DBInstanceView 是聚合页数据库实例条目。
type DBInstanceView struct {
	ID           string `json:"id"`
	InstanceAddr string `json:"instance_addr"`
	Version      string `json:"version"`
	Role         string `json:"role"`
}

// WorkloadView 是聚合页 K8s 工作负载条目。
type WorkloadView struct {
	ID           string `json:"id"`
	Kind         string `json:"kind"`
	Name         string `json:"name"`
	Namespace    string `json:"namespace"`
	ViaNamespace bool   `json:"via_namespace"` // true=经命名空间链继承，false=直接归属
}

// IPView 是聚合页占用 IP 条目（含所在网段）。
type IPView struct {
	IP     string `json:"ip"`
	Prefix string `json:"prefix"` // 所在 IPAM 网段 CIDR，未登记为空串
	HostID string `json:"host_id"`
}

// CloudView 是聚合页云资源条目（host_type=cloud 的主机）。
type CloudView struct {
	ID       string `json:"id"`
	Provider string `json:"provider"`
	Spec     string `json:"spec"`
	Zone     string `json:"zone"`
}

// AggregateView 是应用详情一屏聚合响应。
type AggregateView struct {
	App          AppBrief         `json:"app"`
	Hosts        []HostView       `json:"hosts"`
	DBInstances  []DBInstanceView `json:"db_instances"`
	K8sWorkloads []WorkloadView   `json:"k8s_workloads"`
	IPs          []IPView         `json:"ips"`
	Clouds       []CloudView      `json:"clouds"`
}

// DepNode 是依赖拓扑节点。
type DepNode struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	Type  string `json:"type"` // 模型编码（biz_app/db_instance）
}

// DepEdge 是依赖拓扑边（a→b，code 为关系码）。
type DepEdge struct {
	A    string `json:"a"`
	B    string `json:"b"`
	Code string `json:"code"`
}

// DependenciesView 是应用依赖拓扑响应（本应用为心两跳以内）。
type DependenciesView struct {
	Nodes []DepNode `json:"nodes"`
	Edges []DepEdge `json:"edges"`
}

// ImpactItem 是影响面反查命中的一条受影响应用。
type ImpactItem struct {
	AppID   string   `json:"app_id"`
	AppName string   `json:"app_name"`
	Path    []string `json:"path"` // 从源 CI 到应用的链路：model:label 与关系码交替
}

// ImpactView 是影响面反查响应。
type ImpactView struct {
	Affected []ImpactItem `json:"affected"`
}

// ---- 内部辅助 ----

// attrString 读取字符串属性（去空白）。
func attrString(attrs map[string]any, key string) string {
	v, ok := attrs[key].(string)
	if !ok {
		return ""
	}
	return strings.TrimSpace(v)
}

// modelIDs 批量加载模型编码 → 模型 ID 映射（一次查询）。
func (s *Service) modelIDs(ctx context.Context, codes ...string) (map[string]string, error) {
	var models []store.Model
	if err := s.db.WithContext(ctx).Where("code IN ?", codes).Find(&models).Error; err != nil {
		return nil, fmt.Errorf("加载模型失败: %w", err)
	}
	ids := make(map[string]string, len(models))
	for _, m := range models {
		ids[m.Code] = m.ID
	}
	return ids, nil
}

// loadCIs 按 ID 批量加载 CI（一次查询），返回 id→CI 映射。
func (s *Service) loadCIs(ctx context.Context, ids []string) (map[string]store.CI, error) {
	out := map[string]store.CI{}
	if len(ids) == 0 {
		return out, nil
	}
	var cis []store.CI
	if err := s.db.WithContext(ctx).Where("id IN ?", ids).Find(&cis).Error; err != nil {
		return nil, fmt.Errorf("批量加载 CI 失败: %w", err)
	}
	for _, ci := range cis {
		out[ci.ID] = ci
	}
	return out, nil
}

// loadRelations 批量加载指定关系码、且 src 或 dst 落在 ids 内的关系（一次查询）。
func (s *Service) loadRelations(ctx context.Context, codes []string, ids []string) ([]store.CIRelation, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	var rels []store.CIRelation
	if err := s.db.WithContext(ctx).
		Where("relation_code IN ? AND (src_ci_id IN ? OR dst_ci_id IN ?)", codes, ids, ids).
		Find(&rels).Error; err != nil {
		return nil, fmt.Errorf("批量加载关系失败: %w", err)
	}
	return rels, nil
}

// appBrief 由 biz_app CI 组装精简视图。
func appBrief(ci store.CI) AppBrief {
	return AppBrief{
		ID:    ci.ID,
		Code:  attrString(ci.Attributes, "code"),
		Name:  attrString(ci.Attributes, "name"),
		Owner: attrString(ci.Attributes, "owner"),
		Level: attrString(ci.Attributes, "level"),
	}
}

// sortedKeys 返回映射的排序键（保证输出确定性）。
func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// ---- 业务树 ----

// Tree 组装两级业务树：biz_line 节点含应用数/主机数汇总，biz_app 节点含负责人/等级；
// 无 belongs_to 归属的应用进 unassigned 组。
func (s *Service) Tree(ctx context.Context) (*TreeView, error) {
	modelIDs, err := s.modelIDs(ctx, modelBizLine, modelBizApp)
	if err != nil {
		return nil, err
	}
	lineModelID, appModelID := modelIDs[modelBizLine], modelIDs[modelBizApp]

	var lines []store.CI
	if lineModelID != "" {
		if err := s.db.WithContext(ctx).
			Where("model_id = ?", lineModelID).Order("created_at ASC").Find(&lines).Error; err != nil {
			return nil, fmt.Errorf("查询业务线失败: %w", err)
		}
	}
	var apps []store.CI
	if appModelID != "" {
		if err := s.db.WithContext(ctx).
			Where("model_id = ?", appModelID).Order("created_at ASC").Find(&apps).Error; err != nil {
			return nil, fmt.Errorf("查询应用失败: %w", err)
		}
	}
	appIDs := make([]string, 0, len(apps))
	for _, a := range apps {
		appIDs = append(appIDs, a.ID)
	}
	// 批量预取：应用→业务线归属与应用→主机部署关系（各一次查询）。
	rels, err := s.loadRelations(ctx, []string{relBelongsTo, relDeployedOn}, appIDs)
	if err != nil {
		return nil, err
	}
	appLine := map[string]string{}            // app → line
	lineHosts := map[string]map[string]bool{} // line → 去重主机集合
	for _, r := range rels {
		if r.RelationCode == relBelongsTo {
			appLine[r.SrcCIID] = r.DstCIID
		}
	}
	// 归属先就位后再汇总主机（belongs_to 与 deployed_on 同批返回，顺序不定）。
	for _, r := range rels {
		if r.RelationCode != relDeployedOn {
			continue
		}
		if lineID, ok := appLine[r.SrcCIID]; ok {
			if lineHosts[lineID] == nil {
				lineHosts[lineID] = map[string]bool{}
			}
			lineHosts[lineID][r.DstCIID] = true
		}
	}

	appsByLine := map[string][]AppBrief{}
	var unassigned []AppBrief
	for _, a := range apps {
		brief := appBrief(a)
		if lineID, ok := appLine[a.ID]; ok {
			appsByLine[lineID] = append(appsByLine[lineID], brief)
		} else {
			unassigned = append(unassigned, brief)
		}
	}
	view := &TreeView{Lines: []LineNode{}, Unassigned: unassigned}
	if view.Unassigned == nil {
		view.Unassigned = []AppBrief{}
	}
	for _, l := range lines {
		node := LineNode{AppBrief: appBrief(l), Apps: appsByLine[l.ID]}
		if node.Apps == nil {
			node.Apps = []AppBrief{}
		}
		node.AppCount = len(node.Apps)
		node.HostCount = len(lineHosts[l.ID])
		view.Lines = append(view.Lines, node)
	}
	return view, nil
}

// ---- 应用聚合 ----

// Aggregate 组装应用详情一屏聚合：部署主机（deployed_on 出向）、依赖数据库
// （depends_on 出向）、K8s 工作负载（in_namespace→mounted_to 两跳反查 ∪ 直接 belongs_to）、
// 占用 IP（host.ip 去重 + 所在前缀）与云资源（host_type=cloud 的主机）。
func (s *Service) Aggregate(ctx context.Context, appID string) (*AggregateView, error) {
	app, err := s.loadApp(ctx, appID)
	if err != nil {
		return nil, err
	}

	// 批量预取本应用的全部出向/入向关系（一次查询）。
	rels, err := s.loadRelations(ctx,
		[]string{relDeployedOn, relDependsOn, relMountedTo, relBelongsTo}, []string{app.ID})
	if err != nil {
		return nil, err
	}
	var hostIDs, dbIDs, nsIDs, directWlIDs []string
	for _, r := range rels {
		switch {
		case r.RelationCode == relDeployedOn && r.SrcCIID == app.ID:
			hostIDs = append(hostIDs, r.DstCIID)
		case r.RelationCode == relDependsOn && r.SrcCIID == app.ID:
			dbIDs = append(dbIDs, r.DstCIID)
		case r.RelationCode == relMountedTo && r.DstCIID == app.ID:
			nsIDs = append(nsIDs, r.SrcCIID)
		case r.RelationCode == relBelongsTo && r.DstCIID == app.ID:
			directWlIDs = append(directWlIDs, r.SrcCIID)
		}
	}

	// K8s 两跳反查第二跳：命名空间 → 工作负载（in_namespace 入向，一次查询）。
	nsRels, err := s.loadRelations(ctx, []string{relInNamespace}, nsIDs)
	if err != nil {
		return nil, err
	}
	nsSet := map[string]bool{}
	for _, id := range nsIDs {
		nsSet[id] = true
	}
	viaNsWlIDs := map[string]bool{}
	var wlIDs []string
	for _, r := range nsRels {
		if r.RelationCode == relInNamespace && nsSet[r.DstCIID] {
			viaNsWlIDs[r.SrcCIID] = true
			wlIDs = append(wlIDs, r.SrcCIID)
		}
	}
	// 直接 belongs_to 归属的工作负载并入（去重）。
	for _, id := range directWlIDs {
		if !viaNsWlIDs[id] {
			wlIDs = append(wlIDs, id)
		}
	}

	// 批量加载对端 CI（主机/数据库/工作负载各一次查询）。
	hosts, err := s.loadCIs(ctx, hostIDs)
	if err != nil {
		return nil, err
	}
	dbs, err := s.loadCIs(ctx, dbIDs)
	if err != nil {
		return nil, err
	}
	wls, err := s.loadCIs(ctx, wlIDs)
	if err != nil {
		return nil, err
	}
	// 模型过滤：depends_on 可能指向非 db_instance（如应用↔应用），只保留数据库实例。
	modelIDs, err := s.modelIDs(ctx, modelDBInstance, modelK8sWorkload)
	if err != nil {
		return nil, err
	}

	view := &AggregateView{
		App:          appBrief(app),
		Hosts:        []HostView{},
		DBInstances:  []DBInstanceView{},
		K8sWorkloads: []WorkloadView{},
		IPs:          []IPView{},
		Clouds:       []CloudView{},
	}
	for _, id := range sortedKeys(hosts) {
		h := hosts[id]
		view.Hosts = append(view.Hosts, HostView{
			ID:     h.ID,
			Ident:  attrString(h.Attributes, "ident"),
			IP:     attrString(h.Attributes, "ip"),
			Status: h.Status,
			Source: h.Source,
		})
		if attrString(h.Attributes, "host_type") == "cloud" {
			view.Clouds = append(view.Clouds, CloudView{
				ID:       h.ID,
				Provider: attrString(h.Attributes, "provider"),
				Spec:     attrString(h.Attributes, "spec"),
				Zone:     attrString(h.Attributes, "zone"),
			})
		}
	}
	for _, id := range sortedKeys(dbs) {
		d := dbs[id]
		if modelIDs[modelDBInstance] != "" && d.ModelID != modelIDs[modelDBInstance] {
			continue
		}
		view.DBInstances = append(view.DBInstances, DBInstanceView{
			ID:           d.ID,
			InstanceAddr: attrString(d.Attributes, "instance_addr"),
			Version:      attrString(d.Attributes, "version"),
			Role:         attrString(d.Attributes, "role"),
		})
	}
	for _, id := range sortedKeys(wls) {
		w := wls[id]
		if modelIDs[modelK8sWorkload] != "" && w.ModelID != modelIDs[modelK8sWorkload] {
			continue
		}
		view.K8sWorkloads = append(view.K8sWorkloads, WorkloadView{
			ID:           w.ID,
			Kind:         attrString(w.Attributes, "kind"),
			Name:         attrString(w.Attributes, "name"),
			Namespace:    attrString(w.Attributes, "namespace"),
			ViaNamespace: viaNsWlIDs[id],
		})
	}

	// 占用 IP：host.ip 去重 + 所在前缀（前缀一次全量加载，内存匹配最具体网段）。
	ips, err := s.buildIPViews(ctx, hosts)
	if err != nil {
		return nil, err
	}
	view.IPs = ips
	return view, nil
}

// buildIPViews 由主机集合组装 IP 视图：按 IP 去重，匹配所在 IPAM 前缀（最具体优先）。
func (s *Service) buildIPViews(ctx context.Context, hosts map[string]store.CI) ([]IPView, error) {
	var prefixes []store.IPPrefix
	if err := s.db.WithContext(ctx).Find(&prefixes).Error; err != nil {
		return nil, fmt.Errorf("查询 IPAM 前缀失败: %w", err)
	}
	type parsedPrefix struct {
		cidr   string
		prefix netip.Prefix
	}
	parsed := make([]parsedPrefix, 0, len(prefixes))
	for _, p := range prefixes {
		if fx, err := netip.ParsePrefix(p.CIDR); err == nil {
			parsed = append(parsed, parsedPrefix{cidr: p.CIDR, prefix: fx})
		}
	}
	seen := map[string]bool{}
	out := []IPView{}
	for _, id := range sortedKeys(hosts) {
		h := hosts[id]
		ip := attrString(h.Attributes, "ip")
		if ip == "" || seen[ip] {
			continue
		}
		seen[ip] = true
		v := IPView{IP: ip, HostID: h.ID}
		if addr, err := netip.ParseAddr(ip); err == nil {
			best := -1
			for i, pp := range parsed {
				if pp.prefix.Contains(addr) && (best < 0 || pp.prefix.Bits() > parsed[best].prefix.Bits()) {
					best = i
				}
			}
			if best >= 0 {
				v.Prefix = parsed[best].cidr
			}
		}
		out = append(out, v)
	}
	return out, nil
}

// loadApp 加载 biz_app CI；CI 不存在返回 gorm.ErrRecordNotFound，模型不符返回 ErrNotApp。
func (s *Service) loadApp(ctx context.Context, appID string) (store.CI, error) {
	var app store.CI
	if err := s.db.WithContext(ctx).First(&app, "id = ?", appID).Error; err != nil {
		return app, err
	}
	modelIDs, err := s.modelIDs(ctx, modelBizApp)
	if err != nil {
		return app, err
	}
	if modelIDs[modelBizApp] == "" || app.ModelID != modelIDs[modelBizApp] {
		return app, ErrNotApp
	}
	return app, nil
}

// ErrNotApp 表示目标 CI 存在但不是 biz_app 模型。
var ErrNotApp = fmt.Errorf("CI 不是应用（biz_app）")

// ---- 依赖拓扑 ----

// Dependencies 组装应用依赖拓扑：以本应用为心两跳以内，节点限定 biz_app 与
// db_instance 模型（应用↔应用、应用↔DB），边保留关系码。
func (s *Service) Dependencies(ctx context.Context, appID string) (*DependenciesView, error) {
	app, err := s.loadApp(ctx, appID)
	if err != nil {
		return nil, err
	}
	modelIDs, err := s.modelIDs(ctx, modelBizApp, modelDBInstance)
	if err != nil {
		return nil, err
	}
	allowed := map[string]string{} // ci id → 模型编码（仅 biz_app/db_instance）

	nodes := map[string]DepNode{}
	edges := map[string]DepEdge{} // code|a|b 去重
	frontier := []string{app.ID}
	expanded := map[string]bool{app.ID: true}

	for hop := 0; hop < 2 && len(frontier) > 0; hop++ {
		rels, err := s.loadRelations(ctx, []string{relDependsOn}, frontier)
		if err != nil {
			return nil, err
		}
		// 收集候选对端，批量加载并过滤模型（一次查询）。
		candidateIDs := map[string]bool{}
		for _, r := range rels {
			candidateIDs[r.SrcCIID] = true
			candidateIDs[r.DstCIID] = true
		}
		cis, err := s.loadCIs(ctx, sortedKeys(candidateIDs))
		if err != nil {
			return nil, err
		}
		for id, ci := range cis {
			for code, mid := range modelIDs {
				if mid != "" && ci.ModelID == mid {
					allowed[id] = code
				}
			}
		}
		var next []string
		for _, r := range rels {
			aModel, aOK := allowed[r.SrcCIID]
			bModel, bOK := allowed[r.DstCIID]
			if !aOK || !bOK {
				continue // 端点非应用/数据库（如主机），不入依赖图
			}
			key := r.RelationCode + "|" + r.SrcCIID + "|" + r.DstCIID
			edges[key] = DepEdge{A: r.SrcCIID, B: r.DstCIID, Code: r.RelationCode}
			nodes[r.SrcCIID] = DepNode{ID: r.SrcCIID, Label: nodeLabel(cis[r.SrcCIID], aModel), Type: aModel}
			nodes[r.DstCIID] = DepNode{ID: r.DstCIID, Label: nodeLabel(cis[r.DstCIID], bModel), Type: bModel}
			for _, peer := range []string{r.SrcCIID, r.DstCIID} {
				if !expanded[peer] {
					expanded[peer] = true
					next = append(next, peer)
				}
			}
		}
		frontier = next
	}

	// 孤立应用（无任何依赖边）也要作为单节点出现。
	if _, ok := nodes[app.ID]; !ok {
		nodes[app.ID] = DepNode{ID: app.ID, Label: nodeLabel(app, modelBizApp), Type: modelBizApp}
	}
	view := &DependenciesView{Nodes: []DepNode{}, Edges: []DepEdge{}}
	for _, id := range sortedKeys(nodes) {
		view.Nodes = append(view.Nodes, nodes[id])
	}
	for _, key := range sortedKeys(edges) {
		view.Edges = append(view.Edges, edges[key])
	}
	return view, nil
}

// nodeLabel 取节点显示名：应用用 name，数据库用 instance_addr，主机用 ident，
// 其余按 name → ident → instance_addr 顺序回退，最终回退 CI ID。
func nodeLabel(ci store.CI, modelCode string) string {
	if modelCode == modelDBInstance {
		if v := attrString(ci.Attributes, "instance_addr"); v != "" {
			return v
		}
	}
	for _, key := range []string{"name", "ident", "instance_addr"} {
		if v := attrString(ci.Attributes, key); v != "" {
			return v
		}
	}
	return ci.ID
}

// ---- 影响面反查 ----

// Impact 资源影响面反查：从任意 CI 出发，沿 belongs_to/deployed_on/depends_on/runs_on
// 入向最多两跳，列出受影响的 biz_app 及到达路径。
func (s *Service) Impact(ctx context.Context, ciID string) (*ImpactView, error) {
	var origin store.CI
	if err := s.db.WithContext(ctx).First(&origin, "id = ?", ciID).Error; err != nil {
		return nil, err
	}
	modelIDs, err := s.modelIDs(ctx, modelBizApp)
	if err != nil {
		return nil, err
	}
	appModelID := modelIDs[modelBizApp]

	originModel, err := s.modelCodeOf(ctx, origin)
	if err != nil {
		return nil, err
	}
	originLabel := originModel + ":" + nodeLabel(origin, originModel)
	// 模型编码按模型 ID 缓存（避免逐 CI 查模型表）。
	modelCache := map[string]string{origin.ModelID: originModel}
	modelCode := func(ci store.CI) (string, error) {
		if code, ok := modelCache[ci.ModelID]; ok {
			return code, nil
		}
		code, err := s.modelCodeOf(ctx, ci)
		if err != nil {
			return "", err
		}
		modelCache[ci.ModelID] = code
		return code, nil
	}

	// BFS：paths[node] = 从源到 node 的链路段（model:label 与关系码交替）。
	paths := map[string][]string{origin.ID: {originLabel}}
	frontier := []string{origin.ID}
	affected := map[string]ImpactItem{}

	for hop := 0; hop < 2 && len(frontier) > 0; hop++ {
		var rels []store.CIRelation
		if err := s.db.WithContext(ctx).
			Where("relation_code IN ? AND dst_ci_id IN ?", impactRelCodes, frontier).
			Find(&rels).Error; err != nil {
			return nil, fmt.Errorf("查询入向关系失败: %w", err)
		}
		srcIDs := map[string]bool{}
		for _, r := range rels {
			srcIDs[r.SrcCIID] = true
		}
		cis, err := s.loadCIs(ctx, sortedKeys(srcIDs))
		if err != nil {
			return nil, err
		}
		var next []string
		for _, r := range rels {
			if _, visited := paths[r.SrcCIID]; visited {
				continue
			}
			src, ok := cis[r.SrcCIID]
			if !ok {
				continue
			}
			srcModel, err := modelCode(src)
			if err != nil {
				return nil, err
			}
			path := append(append([]string{}, paths[r.DstCIID]...), r.RelationCode, srcModel+":"+nodeLabel(src, srcModel))
			paths[r.SrcCIID] = path
			if src.ModelID == appModelID {
				affected[src.ID] = ImpactItem{
					AppID:   src.ID,
					AppName: attrString(src.Attributes, "name"),
					Path:    path,
				}
			}
			next = append(next, r.SrcCIID)
		}
		frontier = next
	}

	view := &ImpactView{Affected: []ImpactItem{}}
	for _, id := range sortedKeys(affected) {
		view.Affected = append(view.Affected, affected[id])
	}
	return view, nil
}

// modelCodeOf 取 CI 所属模型编码（按模型 ID 一次查询）。
func (s *Service) modelCodeOf(ctx context.Context, ci store.CI) (string, error) {
	var m store.Model
	if err := s.db.WithContext(ctx).First(&m, "id = ?", ci.ModelID).Error; err != nil {
		return "", fmt.Errorf("加载 CI %s 的模型失败: %w", ci.ID, err)
	}
	return m.Code, nil
}
