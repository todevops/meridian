// 认证/鉴权核心单测：bcrypt、JWT、权限点工具与 Casbin RBAC 行为。
package auth

import (
	"fmt"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"meridian/server/internal/store"
)

func TestPasswordHashAndCheck(t *testing.T) {
	hash, err := HashPassword("s3cret")
	if err != nil {
		t.Fatalf("哈希失败: %v", err)
	}
	if hash == "s3cret" {
		t.Fatal("哈希不应等于明文")
	}
	if !CheckPassword(hash, "s3cret") {
		t.Fatal("正确密码应通过校验")
	}
	if CheckPassword(hash, "wrong") {
		t.Fatal("错误密码不应通过校验")
	}
}

func TestTokenIssueAndParse(t *testing.T) {
	svc := NewTokenService("test-secret", time.Hour)
	token, err := svc.Issue("user-1")
	if err != nil {
		t.Fatalf("签发失败: %v", err)
	}
	sub, err := svc.Parse(token)
	if err != nil || sub != "user-1" {
		t.Fatalf("解析应返回 user-1，得到 %q, err=%v", sub, err)
	}
	// 篡改签名
	if _, err := svc.Parse(token + "x"); err == nil {
		t.Fatal("篡改令牌应解析失败")
	}
	// 错误密钥
	other := NewTokenService("other-secret", time.Hour)
	if _, err := other.Parse(token); err == nil {
		t.Fatal("错误密钥应解析失败")
	}
	// 过期令牌
	expiredSvc := NewTokenService("test-secret", -time.Hour)
	expired, err := expiredSvc.Issue("user-1")
	if err != nil {
		t.Fatalf("签发失败: %v", err)
	}
	if _, err := svc.Parse(expired); err == nil {
		t.Fatal("过期令牌应解析失败")
	}
}

func TestSplitPermission(t *testing.T) {
	obj, act := SplitPermission("ci:read")
	if obj != "ci" || act != "read" {
		t.Fatalf("拆分错误: %q %q", obj, act)
	}
	if obj, act := SplitPermission("nocolon"); obj != "" || act != "" {
		t.Fatal("非法编码应返回空")
	}
	if !ValidPermission("ci:read") || ValidPermission("ci:delete") {
		t.Fatal("权限点目录校验不符")
	}
}

// setupAuth 打开独立内存库并创建认证服务（含种子数据）。
func setupAuth(t *testing.T) *Service {
	t.Helper()
	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared", t.Name())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("打开内存库失败: %v", err)
	}
	if err := db.AutoMigrate(store.AllModels()...); err != nil {
		t.Fatalf("迁移失败: %v", err)
	}
	svc, err := NewService(db, "test-secret", 1)
	if err != nil {
		t.Fatalf("创建认证服务失败: %v", err)
	}
	if err := svc.Seed("admin-pass", "collector-pass"); err != nil {
		t.Fatalf("种子失败: %v", err)
	}
	return svc
}

// mustUser 创建测试用户并分配角色。
func mustUser(t *testing.T, svc *Service, username string, roleCodes ...string) store.User {
	t.Helper()
	hash, err := HashPassword("pass123")
	if err != nil {
		t.Fatalf("哈希失败: %v", err)
	}
	user := store.User{Username: username, DisplayName: username, PasswordHash: hash, Status: "active"}
	if err := svc.db.Create(&user).Error; err != nil {
		t.Fatalf("创建用户失败: %v", err)
	}
	if err := svc.SetUserRoles(user.ID, roleCodes); err != nil {
		t.Fatalf("分配角色失败: %v", err)
	}
	return user
}

func TestSeedCreatesBuiltinRolesAndUsers(t *testing.T) {
	svc := setupAuth(t)
	for _, code := range []string{"admin", "operator", "viewer", "collector"} {
		var count int64
		if err := svc.db.Model(&store.Role{}).Where("code = ?", code).Count(&count).Error; err != nil || count != 1 {
			t.Fatalf("内置角色 %s 应存在且唯一", code)
		}
	}
	// 幂等：再次 Seed 不应报错也不应重复。
	if err := svc.Seed("admin-pass", "collector-pass"); err != nil {
		t.Fatalf("重复 Seed 失败: %v", err)
	}
	var userCount int64
	svc.db.Model(&store.User{}).Where("username IN ?", []string{"admin", "collector"}).Count(&userCount)
	if userCount != 2 {
		t.Fatalf("内置账号应为 2 个，得到 %d", userCount)
	}
}

func TestRBACEnforcement(t *testing.T) {
	svc := setupAuth(t)

	// 种子 admin：拥有全部权限点。
	var admin store.User
	if err := svc.db.First(&admin, "username = ?", "admin").Error; err != nil {
		t.Fatalf("查询 admin 失败: %v", err)
	}
	for _, p := range Catalog {
		obj, act := SplitPermission(p.Code)
		ok, err := svc.Enforcer.Enforce(admin.ID, obj, act)
		if err != nil || !ok {
			t.Fatalf("admin 应拥有 %s", p.Code)
		}
	}

	// viewer：可读不可写。
	viewer := mustUser(t, svc, "v1", "viewer")
	ok, _ := svc.Enforcer.Enforce(viewer.ID, "ci", "read")
	if !ok {
		t.Fatal("viewer 应有 ci:read")
	}
	ok, _ = svc.Enforcer.Enforce(viewer.ID, "ci", "write")
	if ok {
		t.Fatal("viewer 不应有 ci:write")
	}

	// 自定义角色：替换权限后立即生效。
	if err := svc.SetRolePermissions("viewer", []string{"ci:read", "ci:write"}); err != nil {
		t.Fatalf("设置角色权限失败: %v", err)
	}
	ok, _ = svc.Enforcer.Enforce(viewer.ID, "ci", "write")
	if !ok {
		t.Fatal("角色加权限后 viewer 应有 ci:write")
	}

	// 用户角色整体替换。
	if err := svc.SetUserRoles(viewer.ID, []string{"collector"}); err != nil {
		t.Fatalf("替换用户角色失败: %v", err)
	}
	ok, _ = svc.Enforcer.Enforce(viewer.ID, "discovery", "write")
	if !ok {
		t.Fatal("collector 应有 discovery:write")
	}
	ok, _ = svc.Enforcer.Enforce(viewer.ID, "ci", "read")
	if ok {
		t.Fatal("替换角色后不应保留 viewer 权限")
	}
	if codes := svc.UserRoleCodes(viewer.ID); len(codes) != 1 || codes[0] != "collector" {
		t.Fatalf("用户角色编码不符: %v", codes)
	}
}

func TestUserPermissionCodesUnion(t *testing.T) {
	svc := setupAuth(t)
	u := mustUser(t, svc, "multi", "viewer", "collector")
	codes := svc.UserPermissionCodes(u.ID)
	set := map[string]bool{}
	for _, c := range codes {
		set[c] = true
	}
	if !set["ci:read"] || !set["discovery:write"] {
		t.Fatalf("权限并集不符: %v", codes)
	}
	// 去重：viewer/collector 都含 discovery 域权限时编码不应重复。
	seen := map[string]int{}
	for _, c := range codes {
		seen[c]++
		if seen[c] > 1 {
			t.Fatalf("权限编码重复: %s", c)
		}
	}
}
