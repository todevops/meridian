// Package linker 实现 CI 自动关联器（2B）：调和引擎建档/更新成功后异步触发，
// 按内置规则为相关 CI 幂等 upsert 关系——
//
//	规则一 VM↔主机：host 与 virtual_machine 的 instance_uuid 相等
//	  → 建 host.instantiated_by→virtual_machine；
//	规则二 实例↔主机：db_instance 的 ip（缺省时取 instance_addr 的主机段）命中 host 的 ip
//	  → 建 db_instance.runs_on→host；
//	规则三 VM 从属：virtual_machine 的 parent_host_uuid 命中 esxi_host 的 hardware_uuid
//	  → 建 virtual_machine.runs_on→esxi_host；parent_cluster_moid 命中 esxi_cluster 的 moid
//	  → 建 esxi_host.belongs_to→esxi_cluster。
//
// 容错原则：规则涉及的模型或关系定义不存在时跳过不报错（模型由种子/人工维护，
// 关联器不得因模型未就位而阻断调和主流程）；单条规则失败仅记日志。
// 幂等：关系按 (relation_code, src_ci_id, dst_ci_id) 唯一（数据库唯一约束 +
// 建前检查双保险），同一 CI 重复触发不会产生重复关系。
package linker

import (
	"context"
	"fmt"
	"log"
	"net/netip"
	"strings"

	"gorm.io/datatypes"
	"gorm.io/gorm"

	"meridian/server/internal/store"
)

// 关联规则涉及的模型与关系编码常量。
const (
	modelHost         = "host"
	modelVM           = "virtual_machine"
	modelDBInstance   = "db_instance"
	modelEsxiHost     = "esxi_host"
	modelEsxiCluster  = "esxi_cluster"
	modelK8sNamespace = "k8s_namespace"
	modelK8sWorkload  = "k8s_workload"
	relInstantiatedBy = "instantiated_by"
	relRunsOn         = "runs_on"
	relBelongsTo      = "belongs_to"
	relMountedTo      = "mounted_to"
	relInNamespace    = "in_namespace"
)

// Linker 是自动关联器。
type Linker struct {
	db *gorm.DB
}

// New 创建自动关联器。
func New(db *gorm.DB) *Linker {
	return &Linker{db: db}
}

// Handle 是挂到调和引擎的后置钩子：CI 建档/更新成功后按模型分派关联规则。
// 签名与 reconcile.PostHook 一致；所有失败仅返回错误由调用方记日志，不阻断调和。
func (l *Linker) Handle(ctx context.Context, ciID, action string) error {
	var ci store.CI
	if err := l.db.WithContext(ctx).First(&ci, "id = ?", ciID).Error; err != nil {
		return fmt.Errorf("加载 CI %s 失败: %w", ciID, err)
	}
	var model store.Model
	if err := l.db.WithContext(ctx).First(&model, "id = ?", ci.ModelID).Error; err != nil {
		return fmt.Errorf("加载 CI %s 的模型失败: %w", ciID, err)
	}

	var errs []string
	run := func(name string, fn func(context.Context, store.CI) error) {
		if err := fn(ctx, ci); err != nil {
			log.Printf("自动关联规则 %s 执行失败（CI %s，模型 %s）: %v", name, ci.ID, model.Code, err)
			errs = append(errs, fmt.Sprintf("%s: %v", name, err))
		}
	}

	switch model.Code {
	case modelHost:
		run("host↔vm", l.linkHostVM)
		run("db↔host", l.linkInstanceHost)
	case modelVM:
		run("host↔vm", l.linkHostVM)
		run("vm↔esxi", l.linkVMInfra)
	case modelDBInstance:
		run("db↔host", l.linkInstanceHost)
	case modelEsxiHost:
		// ESXi 主机后于 VM 建档时，从 ESXi 侧反向补挂 VM 从属关系；
		// 并按自身 parent_cluster_moid 挂到所属集群（采集器把集群归属放在主机记录上）。
		run("esxi↔vm", l.linkEsxiHost)
		run("esxi→cluster", l.linkEsxiHostCluster)
	case modelEsxiCluster:
		// 集群后于 ESXi 主机建档时，从集群侧反向补挂主机从属关系。
		run("cluster→esxi", l.linkEsxiCluster)
	case modelK8sWorkload:
		// 规则四（3B，F-024 整挂继承）：工作负载按 cluster+namespace 挂到命名空间；
		// 命名空间已 mounted_to 应用时，工作负载自动继承 belongs_to 归属。
		run("workload→namespace", l.linkWorkloadNamespace)
	case modelK8sNamespace:
		// 规则四（命名空间侧触发）：命名空间后于工作负载建档时反向补挂，
		// 并把既有 mounted_to 归属传播给名下全部工作负载。
		run("namespace→workloads", func(ctx context.Context, ns store.CI) error {
			return l.PropagateNamespaceMount(ctx, ns.ID)
		})
	default:
		// 其他模型无内置关联规则，直接跳过。
	}
	if len(errs) > 0 {
		return fmt.Errorf("部分关联规则失败: %s", strings.Join(errs, "；"))
	}
	return nil
}

// linkHostVM 规则一：host 与 virtual_machine 按 instance_uuid 互链
// （关系方向固定为 host.instantiated_by→virtual_machine，无论从哪一侧触发）。
func (l *Linker) linkHostVM(ctx context.Context, ci store.CI) error {
	uuid := attrString(ci.Attributes, "instance_uuid")
	if uuid == "" {
		return nil // 无 instance_uuid，规则不适用
	}
	var hostCI, vmCI *store.CI
	// 确定本 CI 角色并寻找对端。
	if vm, err := l.findCIByAttr(ctx, modelVM, "instance_uuid", uuid); err != nil {
		return err
	} else {
		vmCI = vm
	}
	if host, err := l.findCIByAttr(ctx, modelHost, "instance_uuid", uuid); err != nil {
		return err
	} else {
		hostCI = host
	}
	if hostCI == nil || vmCI == nil {
		return nil // 对端尚未建档，待对端触发时再链
	}
	return l.ensureRelation(ctx, modelHost, relInstantiatedBy, *hostCI, *vmCI)
}

// linkInstanceHost 规则二：db_instance 与 host 按 ip 互链
// （关系方向固定为 db_instance.runs_on→host）。
func (l *Linker) linkInstanceHost(ctx context.Context, ci store.CI) error {
	ip := dbInstanceIP(ci.Attributes)
	if ip == "" {
		return nil
	}
	var dbCI, hostCI *store.CI
	if db, err := l.findCIByAttr(ctx, modelDBInstance, "ip", ip); err != nil {
		return err
	} else {
		dbCI = db
	}
	// db_instance 可能只有 instance_addr 而无 ip 字段：按 ip 查不到时遍历比对派生 IP。
	if dbCI == nil {
		var err error
		if dbCI, err = l.findDBInstanceByDerivedIP(ctx, ip); err != nil {
			return err
		}
	}
	if host, err := l.findCIByAttr(ctx, modelHost, "ip", ip); err != nil {
		return err
	} else {
		hostCI = host
	}
	if dbCI == nil || hostCI == nil {
		return nil
	}
	return l.ensureRelation(ctx, modelDBInstance, relRunsOn, *dbCI, *hostCI)
}

// linkVMInfra 规则三（VM 侧触发）：按 parent_host_uuid / parent_cluster_moid
// 建立 virtual_machine.runs_on→esxi_host 与 esxi_host.belongs_to→esxi_cluster。
func (l *Linker) linkVMInfra(ctx context.Context, vm store.CI) error {
	var esxi *store.CI
	if hwUUID := attrString(vm.Attributes, "parent_host_uuid"); hwUUID != "" {
		host, err := l.findCIByAttr(ctx, modelEsxiHost, "hardware_uuid", hwUUID)
		if err != nil {
			return err
		}
		if host != nil {
			esxi = host
			if err := l.ensureRelation(ctx, modelVM, relRunsOn, vm, *host); err != nil {
				return err
			}
		}
	}
	if moid := attrString(vm.Attributes, "parent_cluster_moid"); moid != "" {
		cluster, err := l.findCIByAttr(ctx, modelEsxiCluster, "moid", moid)
		if err != nil {
			return err
		}
		if cluster != nil && esxi != nil {
			if err := l.ensureRelation(ctx, modelEsxiHost, relBelongsTo, *esxi, *cluster); err != nil {
				return err
			}
		}
	}
	return nil
}

// linkEsxiHost 规则三（ESXi 侧触发）：按 hardware_uuid 反查 VM 的 parent_host_uuid 补挂。
func (l *Linker) linkEsxiHost(ctx context.Context, esxi store.CI) error {
	hwUUID := attrString(esxi.Attributes, "hardware_uuid")
	if hwUUID == "" {
		return nil
	}
	vms, err := l.findCIsByAttr(ctx, modelVM, "parent_host_uuid", hwUUID)
	if err != nil {
		return err
	}
	for _, vm := range vms {
		if err := l.ensureRelation(ctx, modelVM, relRunsOn, vm, esxi); err != nil {
			return err
		}
		if moid := attrString(vm.Attributes, "parent_cluster_moid"); moid != "" {
			cluster, err := l.findCIByAttr(ctx, modelEsxiCluster, "moid", moid)
			if err != nil {
				return err
			}
			if cluster != nil {
				if err := l.ensureRelation(ctx, modelEsxiHost, relBelongsTo, esxi, *cluster); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// linkEsxiHostCluster 规则三补充（ESXi 侧触发）：按主机自身的 parent_cluster_moid
// 建立 esxi_host.belongs_to→esxi_cluster（采集器把集群归属携带在主机记录上，
// VM 记录通常不含 parent_cluster_moid，不能依赖 VM 侧推导）。
func (l *Linker) linkEsxiHostCluster(ctx context.Context, esxi store.CI) error {
	moid := attrString(esxi.Attributes, "parent_cluster_moid")
	if moid == "" {
		return nil
	}
	cluster, err := l.findCIByAttr(ctx, modelEsxiCluster, "moid", moid)
	if err != nil {
		return err
	}
	if cluster == nil {
		return nil // 集群尚未建档，待其建档时由集群侧反向补挂
	}
	return l.ensureRelation(ctx, modelEsxiHost, relBelongsTo, esxi, *cluster)
}

// linkEsxiCluster 规则三补充（集群侧触发）：集群建档时反向补挂所有
// parent_cluster_moid 指向本集群的 ESXi 主机。
func (l *Linker) linkEsxiCluster(ctx context.Context, cluster store.CI) error {
	moid := attrString(cluster.Attributes, "moid")
	if moid == "" {
		return nil
	}
	hosts, err := l.findCIsByAttr(ctx, modelEsxiHost, "parent_cluster_moid", moid)
	if err != nil {
		return err
	}
	for _, h := range hosts {
		if err := l.ensureRelation(ctx, modelEsxiHost, relBelongsTo, h, cluster); err != nil {
			return err
		}
	}
	return nil
}

// linkWorkloadNamespace 规则四（工作负载侧触发）：按 cluster+namespace 属性匹配
// k8s_namespace 建 in_namespace 关系；命名空间已挂载应用时同步继承 belongs_to。
func (l *Linker) linkWorkloadNamespace(ctx context.Context, wl store.CI) error {
	cluster := attrString(wl.Attributes, "cluster")
	nsName := attrString(wl.Attributes, "namespace")
	if cluster == "" || nsName == "" {
		return nil
	}
	ns, err := l.findNamespace(ctx, cluster, nsName)
	if err != nil || ns == nil {
		return err // 命名空间尚未建档，待其建档时由命名空间侧反向补挂
	}
	if err := l.ensureRelation(ctx, modelK8sWorkload, relInNamespace, wl, *ns); err != nil {
		return err
	}
	return l.inheritMount(ctx, wl, *ns)
}

// PropagateNamespaceMount 把命名空间的 mounted_to 应用归属传播给名下全部工作负载：
// 先补齐 in_namespace 关系，再为每个工作负载幂等建 belongs_to→biz_app；
// 命名空间改挂其它应用时，仅替换自动（source=auto）归属，人工 belongs_to 不动。
// 供两处调用：命名空间 CI 建档/更新的关联器钩子；人工创建 mounted_to 关系的 API。
func (l *Linker) PropagateNamespaceMount(ctx context.Context, nsCIID string) error {
	var ns store.CI
	if err := l.db.WithContext(ctx).First(&ns, "id = ?", nsCIID).Error; err != nil {
		return fmt.Errorf("加载命名空间 CI %s 失败: %w", nsCIID, err)
	}
	cluster := attrString(ns.Attributes, "cluster")
	nsName := attrString(ns.Attributes, "name")
	if cluster == "" || nsName == "" {
		return nil
	}
	workloads, err := l.findWorkloads(ctx, cluster, nsName)
	if err != nil {
		return err
	}
	for _, wl := range workloads {
		if err := l.ensureRelation(ctx, modelK8sWorkload, relInNamespace, wl, ns); err != nil {
			return err
		}
		if err := l.inheritMount(ctx, wl, ns); err != nil {
			return err
		}
	}
	return nil
}

// inheritMount 为单个工作负载继承命名空间的应用归属（无 mounted_to 时不动）。
func (l *Linker) inheritMount(ctx context.Context, wl, ns store.CI) error {
	appID, err := l.mountedAppID(ctx, ns.ID)
	if err != nil || appID == "" {
		return err
	}
	var app store.CI
	if err := l.db.WithContext(ctx).First(&app, "id = ?", appID).Error; err != nil {
		return fmt.Errorf("加载应用 CI %s 失败: %w", appID, err)
	}
	// 改挂场景：清除指向其它应用的自动 belongs_to（人工归属永不触碰）。
	if err := l.db.WithContext(ctx).
		Where("relation_code = ? AND src_ci_id = ? AND dst_ci_id <> ? AND source = ?",
			relBelongsTo, wl.ID, appID, store.RelationSourceAuto).
		Delete(&store.CIRelation{}).Error; err != nil {
		return fmt.Errorf("清理工作负载 %s 旧自动归属失败: %w", wl.ID, err)
	}
	return l.ensureRelation(ctx, modelK8sWorkload, relBelongsTo, wl, app)
}

// mountedAppID 取命名空间 mounted_to 关系指向的应用 CI ID（无则空串）。
func (l *Linker) mountedAppID(ctx context.Context, nsCIID string) (string, error) {
	var rels []store.CIRelation
	if err := l.db.WithContext(ctx).
		Where("relation_code = ? AND src_ci_id = ?", relMountedTo, nsCIID).
		Limit(1).Find(&rels).Error; err != nil {
		return "", fmt.Errorf("查询命名空间 %s 的 mounted_to 关系失败: %w", nsCIID, err)
	}
	if len(rels) == 0 {
		return "", nil
	}
	return rels[0].DstCIID, nil
}

// findNamespace 按 cluster+name 属性查找 k8s_namespace CI；未命中返回 (nil, nil)。
func (l *Linker) findNamespace(ctx context.Context, cluster, name string) (*store.CI, error) {
	model, err := l.findModel(ctx, modelK8sNamespace)
	if err != nil || model == nil {
		return nil, err
	}
	var cis []store.CI
	if err := l.db.WithContext(ctx).
		Where("model_id = ? AND status <> ?", model.ID, "retired").
		Where(datatypes.JSONQuery("attributes").Equals(cluster, "cluster")).
		Where(datatypes.JSONQuery("attributes").Equals(name, "name")).
		Limit(1).Find(&cis).Error; err != nil {
		return nil, fmt.Errorf("查询命名空间 %s/%s 失败: %w", cluster, name, err)
	}
	if len(cis) == 0 {
		return nil, nil
	}
	return &cis[0], nil
}

// findWorkloads 按 cluster+namespace 属性查找全部未退役 k8s_workload CI。
func (l *Linker) findWorkloads(ctx context.Context, cluster, namespace string) ([]store.CI, error) {
	model, err := l.findModel(ctx, modelK8sWorkload)
	if err != nil || model == nil {
		return nil, err
	}
	var cis []store.CI
	if err := l.db.WithContext(ctx).
		Where("model_id = ? AND status <> ?", model.ID, "retired").
		Where(datatypes.JSONQuery("attributes").Equals(cluster, "cluster")).
		Where(datatypes.JSONQuery("attributes").Equals(namespace, "namespace")).
		Find(&cis).Error; err != nil {
		return nil, fmt.Errorf("查询工作负载 %s/%s 失败: %w", cluster, namespace, err)
	}
	return cis, nil
}

// ensureRelation 幂等 upsert 一条关系：源模型的关系定义不存在、或对端模型与
// target_model 不符时跳过（记日志，不报错）；关系已存在时不重复建。
// 方向遵循关系定义：outgoing 时源模型 CI 为 src，incoming 时为 dst。
func (l *Linker) ensureRelation(ctx context.Context, srcModelCode, relCode string, srcCI, dstCI store.CI) error {
	srcModel, err := l.findModel(ctx, srcModelCode)
	if err != nil {
		return err
	}
	if srcModel == nil {
		log.Printf("自动关联跳过：模型 %s 不存在（关系 %s）", srcModelCode, relCode)
		return nil
	}
	var def *store.RelationDefinition
	for i, d := range srcModel.Relations.Data() {
		if d.Code == relCode {
			def = &srcModel.Relations.Data()[i]
			break
		}
	}
	if def == nil {
		log.Printf("自动关联跳过：模型 %s 未定义关系 %s", srcModelCode, relCode)
		return nil
	}

	// 对端模型缺失时跳过（模型缺失容错）；target_model 不符时仍按规则建链但记日志——
	// 规则三要求 vm.runs_on→esxi_host，而存量种子的 vm.runs_on 可能仍指向
	// physical_server（模型演进中的过渡态），不因定义滞后而漏链。
	dstModel, err := l.findModelByCI(ctx, dstCI)
	if err != nil {
		return err
	}
	if dstModel == nil {
		log.Printf("自动关联跳过：对端 CI %s 的模型不存在（关系 %s.%s）", dstCI.ID, srcModelCode, relCode)
		return nil
	}
	if def.TargetModel != "" && dstModel.Code != def.TargetModel {
		log.Printf("自动关联提示：关系 %s.%s 定义的目标模型为 %s，实际对端为 %s，仍按内置规则建链",
			srcModelCode, relCode, def.TargetModel, dstModel.Code)
	}

	src, dst := srcCI.ID, dstCI.ID
	if def.Direction == "incoming" {
		src, dst = dst, src
	}

	var count int64
	if err := l.db.WithContext(ctx).Model(&store.CIRelation{}).
		Where("relation_code = ? AND src_ci_id = ? AND dst_ci_id = ?", def.Code, src, dst).
		Count(&count).Error; err != nil {
		return fmt.Errorf("查询关系失败: %w", err)
	}
	if count > 0 {
		return nil // 幂等：关系已存在
	}
	rel := store.CIRelation{RelationCode: def.Code, SrcCIID: src, DstCIID: dst, Source: store.RelationSourceAuto}
	if err := l.db.WithContext(ctx).Create(&rel).Error; err != nil {
		// 并发触发撞唯一约束视为已存在（幂等），不算失败。
		if isUniqueViolation(err) {
			return nil
		}
		return fmt.Errorf("创建关系 %s（%s→%s）失败: %w", def.Code, src, dst, err)
	}
	log.Printf("自动关联：建立关系 %s（%s.%s → %s.%s）", def.Code, srcModelCode, srcCI.ID, dstModel.Code, dstCI.ID)
	return nil
}

// findModel 按编码查模型；不存在返回 (nil, nil)。
func (l *Linker) findModel(ctx context.Context, code string) (*store.Model, error) {
	var m store.Model
	err := l.db.WithContext(ctx).Where("code = ?", code).First(&m).Error
	if err == gorm.ErrRecordNotFound {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("查询模型 %s 失败: %w", code, err)
	}
	return &m, nil
}

// findModelByCI 加载 CI 所属模型；模型不存在返回 (nil, nil)。
func (l *Linker) findModelByCI(ctx context.Context, ci store.CI) (*store.Model, error) {
	var m store.Model
	err := l.db.WithContext(ctx).First(&m, "id = ?", ci.ModelID).Error
	if err == gorm.ErrRecordNotFound {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("查询 CI %s 的模型失败: %w", ci.ID, err)
	}
	return &m, nil
}

// findCIByAttr 按模型编码 + JSON 属性等值查找首个未退役 CI；未命中返回 (nil, nil)。
// 模型不存在同样返回 (nil, nil)（模型缺失容错）。
func (l *Linker) findCIByAttr(ctx context.Context, modelCode, attr, value string) (*store.CI, error) {
	cis, err := l.findCIsByAttr(ctx, modelCode, attr, value)
	if err != nil || len(cis) == 0 {
		return nil, err
	}
	return &cis[0], nil
}

// findCIsByAttr 按模型编码 + JSON 属性等值查找全部未退役 CI（模型缺失返回空）。
func (l *Linker) findCIsByAttr(ctx context.Context, modelCode, attr, value string) ([]store.CI, error) {
	model, err := l.findModel(ctx, modelCode)
	if err != nil || model == nil {
		return nil, err
	}
	var cis []store.CI
	if err := l.db.WithContext(ctx).
		Where("model_id = ? AND status <> ?", model.ID, "retired").
		Where(datatypes.JSONQuery("attributes").Equals(value, attr)).
		Find(&cis).Error; err != nil {
		return nil, fmt.Errorf("按属性 %s=%s 查询 %s CI 失败: %w", attr, value, modelCode, err)
	}
	return cis, nil
}

// findDBInstanceByDerivedIP 遍历 db_instance，按 instance_addr 派生 IP 匹配
// （采集器只上报 instance_addr 而未单列 ip 的场景）。
func (l *Linker) findDBInstanceByDerivedIP(ctx context.Context, ip string) (*store.CI, error) {
	model, err := l.findModel(ctx, modelDBInstance)
	if err != nil || model == nil {
		return nil, err
	}
	var cis []store.CI
	if err := l.db.WithContext(ctx).
		Where("model_id = ? AND status <> ?", model.ID, "retired").
		Find(&cis).Error; err != nil {
		return nil, fmt.Errorf("查询 %s CI 失败: %w", modelDBInstance, err)
	}
	for i := range cis {
		if dbInstanceIP(cis[i].Attributes) == ip {
			return &cis[i], nil
		}
	}
	return nil, nil
}

// dbInstanceIP 取 db_instance 的匹配 IP：优先 ip 属性，缺省时取 instance_addr 的主机段。
func dbInstanceIP(attrs map[string]any) string {
	if ip := normalizeIPAttr(attrString(attrs, "ip")); ip != "" {
		return ip
	}
	addr := attrString(attrs, "instance_addr")
	if addr == "" {
		return ""
	}
	addrPort, err := netip.ParseAddrPort(addr)
	if err != nil {
		// 无端口等非标准形式：尝试整体按 IP 解析。
		return normalizeIPAttr(addr)
	}
	return addrPort.Addr().String()
}

// attrString 读取字符串属性（去空白）；非字符串或空返回 ""。
func attrString(attrs map[string]any, key string) string {
	v, ok := attrs[key].(string)
	if !ok {
		return ""
	}
	return strings.TrimSpace(v)
}

// normalizeIPAttr 把 IP 字符串归一为 netip 形式；解析失败返回原串（仍可做等值匹配）。
func normalizeIPAttr(s string) string {
	if s == "" {
		return ""
	}
	if addr, err := netip.ParseAddr(s); err == nil {
		return addr.String()
	}
	return s
}

// isUniqueViolation 粗略判定唯一约束冲突（SQLite 与 PostgreSQL 文案均覆盖）。
func isUniqueViolation(err error) bool {
	msg := err.Error()
	return strings.Contains(msg, "UNIQUE constraint failed") ||
		strings.Contains(msg, "duplicate key value violates unique constraint")
}
