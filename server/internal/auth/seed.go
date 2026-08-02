package auth

import (
	"errors"
	"fmt"
	"log"

	"gorm.io/gorm"

	"meridian/server/internal/store"
)

// Seed 幂等初始化认证数据：
// 1. 内置角色元数据与角色→权限点策略；
// 2. 内置 admin / collector 账号（不存在时才创建，初始密码由参数给定）。
func (s *Service) Seed(adminPassword, collectorPassword string) error {
	// 内置角色：元数据入库 + 权限策略补齐（AddPolicy 对已存在策略返回 false，天然幂等）。
	for _, def := range builtinRoles {
		var role store.Role
		err := s.db.Where("code = ?", def.Code).First(&role).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			role = store.Role{
				Code:        def.Code,
				Name:        def.Name,
				Description: def.Description,
				IsBuiltin:   true,
			}
			if err := s.db.Create(&role).Error; err != nil {
				return fmt.Errorf("创建内置角色 %s 失败: %w", def.Code, err)
			}
		} else if err != nil {
			return fmt.Errorf("查询内置角色 %s 失败: %w", def.Code, err)
		}
		if err := s.ensureRolePermissions(def.Code, def.Permissions); err != nil {
			return err
		}
	}

	// 内置账号：admin（全权限）与 collector（采集上报）。
	if err := s.seedBuiltinUser("admin", "系统管理员", adminPassword, "admin"); err != nil {
		return err
	}
	if err := s.seedBuiltinUser("collector", "采集器服务账号", collectorPassword, "collector"); err != nil {
		return err
	}
	return nil
}

// ensureRolePermissions 补齐角色缺失的权限点（不移除人工额外授予的权限）。
func (s *Service) ensureRolePermissions(roleCode string, codes []string) error {
	sub := RoleSubject(roleCode)
	for _, code := range codes {
		obj, act := SplitPermission(code)
		if obj == "" {
			return fmt.Errorf("内置角色 %s 配置了非法权限点: %s", roleCode, code)
		}
		if _, err := s.Enforcer.AddPolicy(sub, obj, act); err != nil {
			return fmt.Errorf("写入内置角色 %s 权限 %s 失败: %w", roleCode, code, err)
		}
	}
	return nil
}

// seedBuiltinUser 在内置账号不存在时创建之并分配内置角色。
func (s *Service) seedBuiltinUser(username, displayName, password, roleCode string) error {
	var count int64
	if err := s.db.Model(&store.User{}).Where("username = ?", username).Count(&count).Error; err != nil {
		return fmt.Errorf("查询用户 %s 失败: %w", username, err)
	}
	if count > 0 {
		return nil
	}
	hash, err := HashPassword(password)
	if err != nil {
		return fmt.Errorf("生成密码哈希失败: %w", err)
	}
	user := store.User{
		Username:     username,
		DisplayName:  displayName,
		PasswordHash: hash,
		Status:       "active",
		IsBuiltin:    true,
	}
	if err := s.db.Create(&user).Error; err != nil {
		return fmt.Errorf("创建内置账号 %s 失败: %w", username, err)
	}
	if _, err := s.Enforcer.AddGroupingPolicy(user.ID, RoleSubject(roleCode)); err != nil {
		return fmt.Errorf("为内置账号 %s 分配角色失败: %w", username, err)
	}
	log.Printf("已创建内置账号 %s（角色 %s）", username, roleCode)
	return nil
}
