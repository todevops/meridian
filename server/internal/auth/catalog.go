// Package auth 提供 CMDB 的身份认证与 RBAC 鉴权能力。
//
// 认证：用户名+密码（bcrypt 哈希）登录，签发 JWT（golang-jwt/v5，HS256），
// 经 httpOnly cookie 或 Authorization Bearer 携带，会话无状态。
// 鉴权：Casbin（casbin/v3）RBAC 模型，策略经 gorm-adapter 持久化到业务库
// （casbin_rule 表，PG/SQLite 均支持）。权限点为代码内固定目录（catalog.go），
// 角色→权限点、用户→角色的关系全部由 Casbin 策略承载。
package auth

import "strings"

// Permission 描述一个权限点。Code 形如 "obj:act"（如 ci:read），
// Casbin 策略按 (obj, act) 二元组存储。
type Permission struct {
	Code string `json:"code"`
	Name string `json:"name"`
	Desc string `json:"description"`
}

// 权限点编码常量。
const (
	PermModelRead       = "model:read"
	PermModelWrite      = "model:write"
	PermCIRead          = "ci:read"
	PermCIWrite         = "ci:write"
	PermDiscoveryRead   = "discovery:read"
	PermDiscoveryWrite  = "discovery:write"
	PermIPAMRead        = "ipam:read"
	PermIPAMWrite       = "ipam:write"
	PermDCIMRead        = "dcim:read"
	PermDCIMWrite       = "dcim:write"
	PermUserManage      = "user:manage"
	PermRoleManage      = "role:manage"
	PermCredentialRead  = "credential:read"
	PermCredentialWrite = "credential:write"
	PermTaskRead        = "task:read"
	PermTaskWrite       = "task:write"
	PermAlertRead       = "alert:read"
	PermAlertWrite      = "alert:write"
	PermDashboardRead   = "dashboard:read"
	PermGovernanceRead  = "governance:read"
	PermGovernanceWrite = "governance:write"
	PermLifecycleWrite  = "lifecycle:write"
	PermAuditRead       = "audit:read"
)

// Catalog 是系统全部权限点的固定目录，/api/v1/permissions 接口据此返回。
var Catalog = []Permission{
	{PermModelRead, "模型查询", "查询模型列表与详情"},
	{PermModelWrite, "模型维护", "创建与修改模型定义"},
	{PermCIRead, "CI 查询", "查询 CI 列表、详情与关系"},
	{PermCIWrite, "CI 维护", "创建与修改 CI 实例"},
	{PermDiscoveryRead, "调和预览", "执行调和规则演练"},
	{PermDiscoveryWrite, "发现记录上报", "采集器批量写入发现记录"},
	{PermIPAMRead, "IPAM 查询", "查询前缀、IP 与利用率"},
	{PermIPAMWrite, "IPAM 维护", "创建前缀、登记与分配 IP"},
	{PermDCIMRead, "DCIM 查询", "查询机柜 U 位占用"},
	{PermDCIMWrite, "DCIM 维护", "设备上下架操作"},
	{PermUserManage, "用户管理", "用户的新建、修改、停用与角色分配"},
	{PermRoleManage, "角色管理", "角色与权限点的维护"},
	{PermCredentialRead, "凭据查询", "查询凭据元数据与审计记录（永不返回明文）"},
	{PermCredentialWrite, "凭据维护", "新建、修改与轮换凭据"},
	{PermTaskRead, "采集任务查询", "查询采集任务与执行记录"},
	{PermTaskWrite, "采集任务维护", "新建、修改与手动触发采集任务"},
	{PermAlertRead, "告警查询", "查询告警事件列表"},
	{PermAlertWrite, "告警维护", "确认（ack）告警事件"},
	{PermDashboardRead, "质量看板查询", "查询数据质量看板指标与下钻清单"},
	{PermGovernanceRead, "稽核查询", "查询稽核规则与整改待办"},
	{PermGovernanceWrite, "稽核维护", "维护稽核规则、手动执行与关闭整改待办"},
	{PermLifecycleWrite, "生命周期维护", "CI 状态流转与退役联动执行"},
	{PermAuditRead, "审计查询", "查询 CI 变更审计日志"},
}

// catalogSet 用于校验权限点编码合法性。
var catalogSet = func() map[string]bool {
	m := make(map[string]bool, len(Catalog))
	for _, p := range Catalog {
		m[p.Code] = true
	}
	return m
}()

// ValidPermission 判定权限点编码是否在目录内。
func ValidPermission(code string) bool { return catalogSet[code] }

// SplitPermission 将 "obj:act" 拆为 (obj, act)；非法编码返回 ("", "")。
func SplitPermission(code string) (string, string) {
	obj, act, ok := strings.Cut(code, ":")
	if !ok || obj == "" || act == "" {
		return "", ""
	}
	return obj, act
}

// builtinRole 描述一个内置角色：元数据 + 权限点集合。
type builtinRole struct {
	Code        string
	Name        string
	Description string
	Permissions []string
}

// allPermissionCodes 返回目录内全部权限点编码。
func allPermissionCodes() []string {
	codes := make([]string, 0, len(Catalog))
	for _, p := range Catalog {
		codes = append(codes, p.Code)
	}
	return codes
}

// builtinRoles 是内置角色定义，Seed 时幂等写入。
var builtinRoles = []builtinRole{
	{"admin", "管理员", "拥有全部权限", allPermissionCodes()},
	{"operator", "运维", "模型/CI/IPAM/DCIM 的日常维护、发现记录上报与发现池裁决、凭据与采集任务管理、告警确认、质量看板与稽核治理、生命周期流转、审计查询", []string{
		PermModelRead, PermModelWrite, PermCIRead, PermCIWrite, PermDiscoveryRead, PermDiscoveryWrite,
		PermIPAMRead, PermIPAMWrite, PermDCIMRead, PermDCIMWrite,
		PermCredentialRead, PermCredentialWrite, PermTaskRead, PermTaskWrite,
		PermAlertRead, PermAlertWrite,
		PermDashboardRead, PermGovernanceRead, PermGovernanceWrite, PermLifecycleWrite, PermAuditRead,
	}},
	{"viewer", "只读", "仅查询模型、CI、IPAM/DCIM、凭据元数据、采集任务、告警与执行调和预览、质量看板/稽核/审计只读", []string{
		PermModelRead, PermCIRead, PermDiscoveryRead, PermIPAMRead, PermDCIMRead,
		PermCredentialRead, PermTaskRead, PermAlertRead,
		PermDashboardRead, PermGovernanceRead, PermAuditRead,
	}},
	// collector 仅供采集器服务账号上报发现记录（D-01）：收缩为 discovery:write + model:read。
	// 注意：需要写模型的场景（如 NetBox 迁移器）必须使用独立服务账号并单独授权
	// model:write，不得复用 collector 角色。
	{"collector", "采集器", "仅供采集器服务账号上报发现记录、读取模型定义", []string{
		PermModelRead, PermDiscoveryWrite,
	}},
}

// RoleSubject 返回角色在 Casbin 策略中的主体标识（"role:<code>"）。
func RoleSubject(code string) string { return "role:" + code }

// roleCodeFromSubject 从策略主体标识还原角色编码；非角色主体返回 ("", false)。
func roleCodeFromSubject(sub string) (string, bool) {
	code, ok := strings.CutPrefix(sub, "role:")
	return code, ok
}
