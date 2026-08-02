package auth

import (
	"fmt"
	"time"

	"github.com/casbin/casbin/v3"
	"github.com/casbin/casbin/v3/model"
	gormadapter "github.com/casbin/gorm-adapter/v3"
	"gorm.io/gorm"
)

// rbacModelText 是 Casbin RBAC 模型：请求 (sub, obj, act)，
// g 承载"用户→角色"分组，p 承载"角色→权限点(obj, act)"。
const rbacModelText = `
[request_definition]
r = sub, obj, act

[policy_definition]
p = sub, obj, act

[role_definition]
g = _, _

[policy_effect]
e = some(where (p.eft == allow))

[matchers]
m = g(r.sub, p.sub) && r.obj == p.obj && r.act == p.act
`

// Service 聚合认证/鉴权依赖：JWT 令牌、Casbin 执行器与业务库连接。
type Service struct {
	db       *gorm.DB
	Tokens   *TokenService
	Enforcer *casbin.Enforcer
}

// NewService 构建认证服务：以业务库为存储创建 Casbin 执行器
// （gorm-adapter 自动建 casbin_rule 表并加载已有策略）。
func NewService(db *gorm.DB, jwtSecret string, tokenTTLHours int) (*Service, error) {
	adapter, err := gormadapter.NewAdapterByDB(db)
	if err != nil {
		return nil, fmt.Errorf("创建 Casbin GORM 适配器失败: %w", err)
	}
	m, err := model.NewModelFromString(rbacModelText)
	if err != nil {
		return nil, fmt.Errorf("解析 Casbin 模型失败: %w", err)
	}
	enforcer, err := casbin.NewEnforcer(m, adapter)
	if err != nil {
		return nil, fmt.Errorf("创建 Casbin 执行器失败: %w", err)
	}
	return &Service{
		db:       db,
		Tokens:   NewTokenService(jwtSecret, time.Duration(tokenTTLHours)*time.Hour),
		Enforcer: enforcer,
	}, nil
}

// UserRoleCodes 返回用户的角色编码列表。
func (s *Service) UserRoleCodes(userID string) []string {
	roles, err := s.Enforcer.GetRolesForUser(userID)
	if err != nil {
		return nil
	}
	codes := make([]string, 0, len(roles))
	for _, sub := range roles {
		if code, ok := roleCodeFromSubject(sub); ok {
			codes = append(codes, code)
		}
	}
	return codes
}

// UserPermissionCodes 返回用户经角色继承得到的权限点并集（去重、有序）。
func (s *Service) UserPermissionCodes(userID string) []string {
	policies, err := s.Enforcer.GetImplicitPermissionsForUser(userID)
	if err != nil {
		return nil
	}
	seen := make(map[string]bool, len(policies))
	codes := make([]string, 0, len(policies))
	for _, p := range policies {
		// p 形如 [sub, obj, act]
		if len(p) < 3 {
			continue
		}
		code := p[1] + ":" + p[2]
		if !seen[code] {
			seen[code] = true
			codes = append(codes, code)
		}
	}
	return codes
}

// SetUserRoles 整体替换用户的角色分配。
func (s *Service) SetUserRoles(userID string, roleCodes []string) error {
	if _, err := s.Enforcer.RemoveFilteredGroupingPolicy(0, userID); err != nil {
		return fmt.Errorf("清除用户角色失败: %w", err)
	}
	for _, code := range roleCodes {
		if _, err := s.Enforcer.AddGroupingPolicy(userID, RoleSubject(code)); err != nil {
			return fmt.Errorf("分配角色 %s 失败: %w", code, err)
		}
	}
	return nil
}

// RolePermissionCodes 返回角色当前授予的权限点编码。
func (s *Service) RolePermissionCodes(roleCode string) []string {
	policies, err := s.Enforcer.GetFilteredPolicy(0, RoleSubject(roleCode))
	if err != nil {
		return nil
	}
	codes := make([]string, 0, len(policies))
	for _, p := range policies {
		if len(p) < 3 {
			continue
		}
		codes = append(codes, p[1]+":"+p[2])
	}
	return codes
}

// SetRolePermissions 整体替换角色的权限点。
func (s *Service) SetRolePermissions(roleCode string, codes []string) error {
	sub := RoleSubject(roleCode)
	if _, err := s.Enforcer.RemoveFilteredPolicy(0, sub); err != nil {
		return fmt.Errorf("清除角色权限失败: %w", err)
	}
	for _, code := range codes {
		obj, act := SplitPermission(code)
		if obj == "" {
			return fmt.Errorf("非法权限点编码: %s", code)
		}
		if _, err := s.Enforcer.AddPolicy(sub, obj, act); err != nil {
			return fmt.Errorf("授予权限 %s 失败: %w", code, err)
		}
	}
	return nil
}

// RoleUserCount 返回当前关联该角色的用户数。
func (s *Service) RoleUserCount(roleCode string) int {
	users, err := s.Enforcer.GetUsersForRole(RoleSubject(roleCode))
	if err != nil {
		return 0
	}
	return len(users)
}

// DeleteRolePolicies 删除角色的全部权限策略与用户分配关系。
func (s *Service) DeleteRolePolicies(roleCode string) error {
	sub := RoleSubject(roleCode)
	if _, err := s.Enforcer.RemoveFilteredPolicy(0, sub); err != nil {
		return err
	}
	_, err := s.Enforcer.RemoveFilteredGroupingPolicy(1, sub)
	return err
}
