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
	relInstantiatedBy = "instantiated_by"
	relRunsOn         = "runs_on"
	relBelongsTo      = "belongs_to"
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
	rel := store.CIRelation{RelationCode: def.Code, SrcCIID: src, DstCIID: dst}
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
