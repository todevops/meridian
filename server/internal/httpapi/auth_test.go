// 认证与 RBAC API 链路测试：登录/me/登出、权限拦截、用户与角色管理。
package httpapi

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"meridian/server/internal/auth"
	"meridian/server/internal/store"
)

// setupAuthAPI 打开独立内存库、构建完整路由并返回 admin 令牌。
func setupAuthAPI(t *testing.T) (*httptest.Server, string) {
	t.Helper()
	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared", t.Name())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("打开内存库失败: %v", err)
	}
	if err := db.AutoMigrate(store.AllModels()...); err != nil {
		t.Fatalf("迁移失败: %v", err)
	}
	authSvc, err := auth.NewService(db, "test-secret", 1)
	if err != nil {
		t.Fatalf("创建认证服务失败: %v", err)
	}
	if err := authSvc.Seed("admin-pass", "collector-pass"); err != nil {
		t.Fatalf("种子认证数据失败: %v", err)
	}
	gin.SetMode(gin.TestMode)
	srv := httptest.NewServer(newTestRouter(t, db, authSvc))
	t.Cleanup(srv.Close)
	return srv, loginAs(t, srv, "admin", "admin-pass")
}

// loginAs 登录并返回 Bearer 令牌。
func loginAs(t *testing.T, srv *httptest.Server, username, password string) string {
	t.Helper()
	code, body := doJSON(t, http.MethodPost, srv.URL+"/api/v1/auth/login",
		map[string]any{"username": username, "password": password}, "")
	if code != http.StatusOK {
		t.Fatalf("登录 %s 失败（%d）: %v", username, code, body)
	}
	token, _ := body["token"].(string)
	if token == "" {
		t.Fatalf("登录响应缺少 token: %v", body)
	}
	return token
}

func TestLoginAndMe(t *testing.T) {
	srv, adminToken := setupAuthAPI(t)

	// 错误密码 → 401；未带令牌访问业务接口 → 401。
	code, _ := doJSON(t, http.MethodPost, srv.URL+"/api/v1/auth/login",
		map[string]any{"username": "admin", "password": "wrong"}, "")
	if code != http.StatusUnauthorized {
		t.Fatalf("错误密码期望 401，得到 %d", code)
	}
	if code, _ := doJSON(t, http.MethodGet, srv.URL+"/api/v1/models", nil, ""); code != http.StatusUnauthorized {
		t.Fatalf("未认证期望 401，得到 %d", code)
	}

	// me：返回角色与权限点并集。
	code, me := doJSON(t, http.MethodGet, srv.URL+"/api/v1/auth/me", nil, adminToken)
	if code != http.StatusOK {
		t.Fatalf("me 期望 200，得到 %d", code)
	}
	if me["username"] != "admin" {
		t.Fatalf("me 用户不符: %v", me)
	}
	roles, _ := me["roles"].([]any)
	if len(roles) != 1 || roles[0] != "admin" {
		t.Fatalf("admin 角色不符: %v", me["roles"])
	}
	perms, _ := me["permissions"].([]any)
	if len(perms) == 0 {
		t.Fatal("admin 权限点不应为空")
	}

	// 登出后 cookie 失效不影响 Bearer（JWT 无状态）；仅验证登出接口可用。
	if code, _ := doJSON(t, http.MethodPost, srv.URL+"/api/v1/auth/logout", nil, adminToken); code != http.StatusOK {
		t.Fatalf("登出期望 200，得到 %d", code)
	}
}

func TestPermissionEnforcement(t *testing.T) {
	srv, adminToken := setupAuthAPI(t)

	// admin 创建 viewer 用户。
	code, user := doJSON(t, http.MethodPost, srv.URL+"/api/v1/users",
		map[string]any{"username": "v1", "display_name": "只读用户", "password": "pass123", "roles": []string{"viewer"}}, adminToken)
	if code != http.StatusOK {
		t.Fatalf("创建用户失败（%d）: %v", code, user)
	}
	viewerToken := loginAs(t, srv, "v1", "pass123")

	// viewer：GET /models 放行，POST /models 403，用户管理 403。
	if code, _ := doJSON(t, http.MethodGet, srv.URL+"/api/v1/models", nil, viewerToken); code != http.StatusOK {
		t.Fatalf("viewer 查询模型期望 200，得到 %d", code)
	}
	code, body := doJSON(t, http.MethodPost, srv.URL+"/api/v1/models",
		map[string]any{"name": "x", "code": "x"}, viewerToken)
	if code != http.StatusForbidden {
		t.Fatalf("viewer 创建模型期望 403，得到 %d: %v", code, body)
	}
	if body["code"] != "FORBIDDEN" {
		t.Fatalf("错误码应为 FORBIDDEN: %v", body)
	}
	if code, _ := doJSON(t, http.MethodGet, srv.URL+"/api/v1/users", nil, viewerToken); code != http.StatusForbidden {
		t.Fatalf("viewer 访问用户管理期望 403，得到 %d", code)
	}

	// collector（D-01 收缩后）：保留 model:read 可读模型，但不可写模型，可上报发现记录。
	collectorToken := loginAs(t, srv, "collector", "collector-pass")
	if code, _ := doJSON(t, http.MethodGet, srv.URL+"/api/v1/models", nil, collectorToken); code != http.StatusOK {
		t.Fatalf("collector 查询模型期望 200，得到 %d", code)
	}
	code, body = doJSON(t, http.MethodPost, srv.URL+"/api/v1/models",
		map[string]any{"name": "x", "code": "x"}, collectorToken)
	if code != http.StatusForbidden {
		t.Fatalf("collector 创建模型期望 403，得到 %d: %v", code, body)
	}
	if body["code"] != "FORBIDDEN" {
		t.Fatalf("错误码应为 FORBIDDEN: %v", body)
	}
}

func TestRoleManagement(t *testing.T) {
	srv, adminToken := setupAuthAPI(t)

	// 权限点目录：登录即可读。
	code, body := doJSON(t, http.MethodGet, srv.URL+"/api/v1/permissions", nil, adminToken)
	if code != http.StatusOK || len(body["items"].([]any)) == 0 {
		t.Fatalf("权限点目录异常（%d）: %v", code, body)
	}

	// 创建自定义角色（仅 ci:read）。
	code, role := doJSON(t, http.MethodPost, srv.URL+"/api/v1/roles",
		map[string]any{"code": "ci-reader", "name": "CI 只读", "permissions": []string{"ci:read"}}, adminToken)
	if code != http.StatusOK {
		t.Fatalf("创建角色失败（%d）: %v", code, role)
	}
	roleID, _ := role["id"].(string)

	// 非法权限点 → 400。
	if code, _ := doJSON(t, http.MethodPost, srv.URL+"/api/v1/roles",
		map[string]any{"code": "bad", "name": "坏角色", "permissions": []string{"ci:delete"}}, adminToken); code != http.StatusBadRequest {
		t.Fatalf("非法权限点期望 400，得到 %d", code)
	}

	// 用户挂上自定义角色后：可查 CI 列表，不可查用户。
	doJSON(t, http.MethodPost, srv.URL+"/api/v1/users",
		map[string]any{"username": "r1", "display_name": "角色用户", "password": "pass123", "roles": []string{"ci-reader"}}, adminToken)
	r1Token := loginAs(t, srv, "r1", "pass123")
	if code, _ := doJSON(t, http.MethodGet, srv.URL+"/api/v1/cis", nil, r1Token); code != http.StatusOK {
		t.Fatalf("ci-reader 查询 CI 期望 200，得到 %d", code)
	}
	if code, _ := doJSON(t, http.MethodGet, srv.URL+"/api/v1/users", nil, r1Token); code != http.StatusForbidden {
		t.Fatalf("ci-reader 访问用户管理期望 403，得到 %d", code)
	}

	// 角色仍关联用户 → 删除 400。
	if code, _ := doJSON(t, http.MethodDelete, srv.URL+"/api/v1/roles/"+roleID, nil, adminToken); code != http.StatusBadRequest {
		t.Fatalf("删除在用角色期望 400，得到 %d", code)
	}

	// 内置角色不可删；admin 角色权限不可改。
	code, roles := doJSON(t, http.MethodGet, srv.URL+"/api/v1/roles", nil, adminToken)
	if code != http.StatusOK {
		t.Fatalf("查询角色列表失败（%d）", code)
	}
	var adminRoleID string
	for _, item := range roles["items"].([]any) {
		r := item.(map[string]any)
		if r["code"] == "admin" {
			adminRoleID = r["id"].(string)
		}
	}
	if adminRoleID == "" {
		t.Fatal("角色列表缺少内置 admin")
	}
	if code, _ := doJSON(t, http.MethodDelete, srv.URL+"/api/v1/roles/"+adminRoleID, nil, adminToken); code != http.StatusBadRequest {
		t.Fatalf("删除内置角色期望 400，得到 %d", code)
	}
	if code, _ := doJSON(t, http.MethodPatch, srv.URL+"/api/v1/roles/"+adminRoleID,
		map[string]any{"permissions": []string{"ci:read"}}, adminToken); code != http.StatusBadRequest {
		t.Fatalf("修改 admin 角色权限期望 400，得到 %d", code)
	}
}

func TestBuiltinUserProtection(t *testing.T) {
	srv, adminToken := setupAuthAPI(t)

	// 找到内置 admin 用户 ID。
	code, users := doJSON(t, http.MethodGet, srv.URL+"/api/v1/users?keyword=admin", nil, adminToken)
	if code != http.StatusOK {
		t.Fatalf("查询用户失败（%d）", code)
	}
	adminUser := users["items"].([]any)[0].(map[string]any)
	adminID := adminUser["id"].(string)
	if adminUser["is_builtin"] != true {
		t.Fatal("admin 应为内置账号")
	}

	// 停用内置账号 → 400；改内置账号角色 → 400。
	if code, _ := doJSON(t, http.MethodPatch, srv.URL+"/api/v1/users/"+adminID,
		map[string]any{"status": "disabled"}, adminToken); code != http.StatusBadRequest {
		t.Fatalf("停用内置账号期望 400，得到 %d", code)
	}
	if code, _ := doJSON(t, http.MethodPatch, srv.URL+"/api/v1/users/"+adminID,
		map[string]any{"roles": []string{"viewer"}}, adminToken); code != http.StatusBadRequest {
		t.Fatalf("改内置账号角色期望 400，得到 %d", code)
	}

	// 重置普通用户密码后新密码可登录。
	doJSON(t, http.MethodPost, srv.URL+"/api/v1/users",
		map[string]any{"username": "p1", "display_name": "密码用户", "password": "old-pass", "roles": []string{"viewer"}}, adminToken)
	code, users = doJSON(t, http.MethodGet, srv.URL+"/api/v1/users?keyword=p1", nil, adminToken)
	if code != http.StatusOK {
		t.Fatalf("查询用户失败（%d）", code)
	}
	p1 := users["items"].([]any)[0].(map[string]any)
	if code, _ := doJSON(t, http.MethodPatch, srv.URL+"/api/v1/users/"+p1["id"].(string),
		map[string]any{"password": "new-pass"}, adminToken); code != http.StatusOK {
		t.Fatalf("重置密码失败（%d）", code)
	}
	loginAs(t, srv, "p1", "new-pass")

	// 停用后登录 → 401。
	if code, _ := doJSON(t, http.MethodPatch, srv.URL+"/api/v1/users/"+p1["id"].(string),
		map[string]any{"status": "disabled"}, adminToken); code != http.StatusOK {
		t.Fatalf("停用用户失败（%d）", code)
	}
	if code, _ := doJSON(t, http.MethodPost, srv.URL+"/api/v1/auth/login",
		map[string]any{"username": "p1", "password": "new-pass"}, ""); code != http.StatusUnauthorized {
		t.Fatalf("停用账号登录期望 401，得到 %d", code)
	}
}

// 防止未使用告警：store 仅用于类型确认。
var _ = store.User{}
