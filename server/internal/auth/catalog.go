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
	PermModelRead      = "model:read"
	PermModelWrite     = "model:write"
	PermCIRead         = "ci:read"
	PermCIWrite        = "ci:write"
	PermDiscoveryRead  = "discovery:read"
	PermDiscoveryWrite = "discovery:write"
	PermIPAMRead       = "ipam:read"
	PermIPAMWrite      = "ipam:write"
	PermDCIMRead       = "dcim:read"
	PermDCIMWrite      = "dcim:write"
	PermUserManage     = "user:manage"
	PermRoleManage     = "role:manage"
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
	{"operator", "运维", "模型/CI/IPAM/DCIM 的日常维护、发现记录上报与发现池裁决", []string{
		PermModelRead, PermModelWrite, PermCIRead, PermCIWrite, PermDiscoveryRead, PermDiscoveryWrite,
		PermIPAMRead, PermIPAMWrite, PermDCIMRead, PermDCIMWrite,
	}},
	{"viewer", "只读", "仅查询模型、CI、IPAM/DCIM 与执行调和预览", []string{
		PermModelRead, PermCIRead, PermDiscoveryRead, PermIPAMRead, PermDCIMRead,
	}},
	{"collector", "采集器", "仅供采集器服务账号上报发现记录、确保模型配置", []string{
		PermModelRead, PermModelWrite, PermDiscoveryWrite,
	}},
}

// RoleSubject 返回角色在 Casbin 策略中的主体标识（"role:<code>"）。
func RoleSubject(code string) string { return "role:" + code }

// roleCodeFromSubject 从策略主体标识还原角色编码；非角色主体返回 ("", false)。
func roleCodeFromSubject(sub string) (string, bool) {
	code, ok := strings.CutPrefix(sub, "role:")
	return code, ok
}
