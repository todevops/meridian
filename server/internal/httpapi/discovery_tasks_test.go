// 凭据纳管（F-005）与采集任务（F-033）API 链路测试。
package httpapi

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"meridian/server/internal/auth"
	"meridian/server/internal/store"
)

// setupCredTaskAPI 打开独立内存库、构建完整路由，返回测试服务、DB 句柄与 admin 令牌。
func setupCredTaskAPI(t *testing.T) (*httptest.Server, *gorm.DB, string) {
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
	return srv, db, loginAs(t, srv, "admin", "admin-pass")
}

// createTestCredential 创建凭据并断言 201，返回凭据视图。
func createTestCredential(t *testing.T, srv *httptest.Server, token, name, typ string, secret map[string]any) map[string]any {
	t.Helper()
	code, body := doJSON(t, http.MethodPost, srv.URL+"/api/v1/credentials",
		map[string]any{"name": name, "type": typ, "description": "测试凭据", "secret": secret}, token)
	if code != http.StatusCreated {
		t.Fatalf("创建凭据失败（%d）: %v", code, body)
	}
	return body
}

func TestCredentialLifecycle(t *testing.T) {
	srv, db, adminToken := setupCredTaskAPI(t)
	plainSecret := "top-secret-ak-12345"

	// 创建：201，响应不含任何 secret 字段。
	body := createTestCredential(t, srv, adminToken, "阿里云AK", "aliyun",
		map[string]any{"access_key": plainSecret, "secret_key": "sk-6789"})
	credID, _ := body["id"].(string)
	if credID == "" {
		t.Fatalf("响应缺少 id: %v", body)
	}
	for _, banned := range []string{"secret", "secret_ciphertext", "access_key", "secret_key"} {
		if _, exists := body[banned]; exists {
			t.Fatalf("响应不应包含字段 %s: %v", banned, body)
		}
	}

	// 密文落库：直接查库断言无明文。
	var cred store.Credential
	if err := db.First(&cred, "id = ?", credID).Error; err != nil {
		t.Fatalf("查询凭据失败: %v", err)
	}
	if strings.Contains(cred.SecretCiphertext, plainSecret) || strings.Contains(cred.SecretCiphertext, "sk-6789") {
		t.Fatal("密文列不应包含明文片段")
	}
	if cred.SecretCiphertext == "" {
		t.Fatal("密文列不应为空")
	}

	// 列表：type 过滤生效，响应不含明文。
	code, list := doJSON(t, http.MethodGet, srv.URL+"/api/v1/credentials?type=aliyun", nil, adminToken)
	if code != http.StatusOK {
		t.Fatalf("查询凭据列表失败（%d）", code)
	}
	if list["total"].(float64) != 1 {
		t.Fatalf("type=aliyun 应命中 1 条: %v", list)
	}
	code, list = doJSON(t, http.MethodGet, srv.URL+"/api/v1/credentials?type=snmp", nil, adminToken)
	if code != http.StatusOK || list["total"].(float64) != 0 {
		t.Fatalf("type=snmp 应为空: %v", list)
	}
	if code, _ := doJSON(t, http.MethodGet, srv.URL+"/api/v1/credentials?type=bad", nil, adminToken); code != http.StatusBadRequest {
		t.Fatalf("非法 type 期望 400，得到 %d", code)
	}

	// PATCH 非密字段 + 审计。
	code, _ = doJSON(t, http.MethodPatch, srv.URL+"/api/v1/credentials/"+credID,
		map[string]any{"description": "改名后的凭据"}, adminToken)
	if code != http.StatusOK {
		t.Fatalf("PATCH 凭据失败（%d）", code)
	}

	// 轮换：last_rotated_at 刷新，密文变化，审计累计。
	oldCiphertext := cred.SecretCiphertext
	code, rotated := doJSON(t, http.MethodPost, srv.URL+"/api/v1/credentials/"+credID+"/rotate",
		map[string]any{"secret": map[string]any{"access_key": "new-ak-999"}}, adminToken)
	if code != http.StatusOK {
		t.Fatalf("轮换凭据失败（%d）: %v", code, rotated)
	}
	if rotated["last_rotated_at"] == nil {
		t.Fatalf("轮换后应有 last_rotated_at: %v", rotated)
	}
	var after store.Credential
	db.First(&after, "id = ?", credID)
	if after.SecretCiphertext == oldCiphertext {
		t.Fatal("轮换后密文应变化")
	}

	// 审计：create + update + rotate 共 3 条（同毫秒时间戳下顺序不稳定，按集合断言）。
	code, audits := doJSON(t, http.MethodGet, srv.URL+"/api/v1/credentials/"+credID+"/audits", nil, adminToken)
	if code != http.StatusOK {
		t.Fatalf("查询审计失败（%d）", code)
	}
	if audits["total"].(float64) != 3 {
		t.Fatalf("应有 3 条审计，实际 %v", audits["total"])
	}
	actions := map[string]bool{}
	for _, item := range audits["items"].([]any) {
		a := item.(map[string]any)
		actions[a["action"].(string)] = true
		if a["operator"] != "admin" || a["source"] != "manual" {
			t.Fatalf("审计操作者/来源不符: %v", a)
		}
	}
	for _, want := range []string{"create", "update", "rotate"} {
		if !actions[want] {
			t.Fatalf("缺少 %s 审计: %v", want, audits["items"])
		}
	}

	// 不存在的凭据：404。
	if code, _ := doJSON(t, http.MethodPatch, srv.URL+"/api/v1/credentials/nope",
		map[string]any{"description": "x"}, adminToken); code != http.StatusNotFound {
		t.Fatalf("不存在凭据期望 404，得到 %d", code)
	}

	// RBAC：viewer 可读不可写。
	doJSON(t, http.MethodPost, srv.URL+"/api/v1/users",
		map[string]any{"username": "v2", "display_name": "只读", "password": "pass123", "roles": []string{"viewer"}}, adminToken)
	viewerToken := loginAs(t, srv, "v2", "pass123")
	if code, _ := doJSON(t, http.MethodGet, srv.URL+"/api/v1/credentials", nil, viewerToken); code != http.StatusOK {
		t.Fatalf("viewer 查询凭据期望 200，得到 %d", code)
	}
	code, errBody := doJSON(t, http.MethodPost, srv.URL+"/api/v1/credentials",
		map[string]any{"name": "x", "type": "snmp", "secret": map[string]any{"community": "public"}}, viewerToken)
	if code != http.StatusForbidden {
		t.Fatalf("viewer 创建凭据期望 403，得到 %d", code)
	}
	// 错误响应同样不得回明文。
	if raw := fmt.Sprintf("%v", errBody); strings.Contains(raw, "public") {
		t.Fatalf("错误响应不应包含明文: %v", errBody)
	}
}

// n9eStub 起一个 n9e targets API 桩，返回 2 个监控目标。
func n9eStub(t *testing.T) *httptest.Server {
	t.Helper()
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/api/n9e/targets") {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"dat":{"list":[
			{"ident":"web-01","host_ip":"10.20.0.11","os":"linux","cpu_num":4,"update_at":1700000000},
			{"ident":"web-02","host_ip":"10.20.0.12","os":"linux","cpu_num":8,"update_at":1700000000}
		],"total":2},"err":""}`)
	}))
	t.Cleanup(stub.Close)
	return stub
}

func TestDiscoveryTaskLifecycle(t *testing.T) {
	srv, db, adminToken := setupCredTaskAPI(t)
	stub := n9eStub(t)

	// 凭据：n9e token（builtin 执行器注入）。
	credBody := createTestCredential(t, srv, adminToken, "n9e只读账号", "n9e", map[string]any{"token": "n9e-token-abc"})
	credID := credBody["id"].(string)

	// 创建 builtin 任务。
	code, task := doJSON(t, http.MethodPost, srv.URL+"/api/v1/discovery/tasks",
		map[string]any{
			"name": "n9e主机同步", "collector_type": "builtin:n9e-consumer",
			"credential_id": credID, "interval_seconds": 60, "enabled": true,
			"config": map[string]any{"api_url": stub.URL},
		}, adminToken)
	if code != http.StatusCreated {
		t.Fatalf("创建任务失败（%d）: %v", code, task)
	}
	taskID := task["id"].(string)
	if task["status"] != "idle" || task["interval_seconds"].(float64) != 60 {
		t.Fatalf("任务初始字段不符: %v", task)
	}

	// 手动触发：produced=2，执行记录 success=true。
	code, run := doJSON(t, http.MethodPost, srv.URL+"/api/v1/discovery/tasks/"+taskID+"/run", nil, adminToken)
	if code != http.StatusOK {
		t.Fatalf("手动触发失败（%d）: %v", code, run)
	}
	if run["success"] != true || run["produced"].(float64) != 2 {
		t.Fatalf("执行记录不符: %v", run)
	}
	if run["trigger"] != "manual" {
		t.Fatalf("触发方式应为 manual: %v", run)
	}

	// 任务状态回写。
	code, runs := doJSON(t, http.MethodGet, srv.URL+"/api/v1/discovery/tasks/"+taskID+"/runs", nil, adminToken)
	if code != http.StatusOK || runs["total"].(float64) != 1 {
		t.Fatalf("执行记录列表异常（%d）: %v", code, runs)
	}
	var updated store.DiscoveryTask
	db.First(&updated, "id = ?", taskID)
	if updated.RunCount != 1 || updated.FailCount != 0 || updated.LastSuccessAt == nil || updated.Status != "idle" {
		t.Fatalf("任务状态回写异常: %+v", updated)
	}

	// 凭据 use_count=1 且有 use 审计（来源任务）。
	var cred store.Credential
	db.First(&cred, "id = ?", credID)
	if cred.UseCount != 1 {
		t.Fatalf("凭据使用计数应为 1，得到 %d", cred.UseCount)
	}
	code, audits := doJSON(t, http.MethodGet, srv.URL+"/api/v1/credentials/"+credID+"/audits", nil, adminToken)
	if code != http.StatusOK {
		t.Fatalf("查询审计失败（%d）", code)
	}
	foundUse := false
	for _, item := range audits["items"].([]any) {
		a := item.(map[string]any)
		if a["action"] == "use" && a["source"] == "task:n9e主机同步" {
			foundUse = true
		}
	}
	if !foundUse {
		t.Fatalf("缺少来源任务的 use 审计: %v", audits["items"])
	}

	// PATCH：停用任务。
	code, patched := doJSON(t, http.MethodPatch, srv.URL+"/api/v1/discovery/tasks/"+taskID,
		map[string]any{"enabled": false, "interval_seconds": 120}, adminToken)
	if code != http.StatusOK || patched["enabled"] != false {
		t.Fatalf("PATCH 任务失败（%d）: %v", code, patched)
	}

	// 任务列表过滤。
	code, list := doJSON(t, http.MethodGet, srv.URL+"/api/v1/discovery/tasks?enabled=false", nil, adminToken)
	if code != http.StatusOK || list["total"].(float64) != 1 {
		t.Fatalf("enabled=false 过滤异常: %v", list)
	}
}

func TestDiscoveryTaskValidation(t *testing.T) {
	srv, _, adminToken := setupCredTaskAPI(t)

	// 非法 collector_type。
	if code, _ := doJSON(t, http.MethodPost, srv.URL+"/api/v1/discovery/tasks",
		map[string]any{"name": "x", "collector_type": "builtin:nope"}, adminToken); code != http.StatusBadRequest {
		t.Fatalf("非法 collector_type 期望 400，得到 %d", code)
	}
	// interval 过小。
	if code, _ := doJSON(t, http.MethodPost, srv.URL+"/api/v1/discovery/tasks",
		map[string]any{"name": "x", "collector_type": "exec:c1", "interval_seconds": 5}, adminToken); code != http.StatusBadRequest {
		t.Fatalf("interval<10 期望 400，得到 %d", code)
	}
	// 凭据不存在。
	if code, _ := doJSON(t, http.MethodPost, srv.URL+"/api/v1/discovery/tasks",
		map[string]any{"name": "x", "collector_type": "exec:c1", "credential_id": "nope"}, adminToken); code != http.StatusNotFound {
		t.Fatalf("凭据不存在期望 404，得到 %d", code)
	}
	// 触发不存在的任务。
	if code, _ := doJSON(t, http.MethodPost, srv.URL+"/api/v1/discovery/tasks/nope/run", nil, adminToken); code != http.StatusNotFound {
		t.Fatalf("不存在任务期望 404，得到 %d", code)
	}
	// viewer：可读任务不可写。
	doJSON(t, http.MethodPost, srv.URL+"/api/v1/users",
		map[string]any{"username": "v3", "display_name": "只读", "password": "pass123", "roles": []string{"viewer"}}, adminToken)
	viewerToken := loginAs(t, srv, "v3", "pass123")
	if code, _ := doJSON(t, http.MethodGet, srv.URL+"/api/v1/discovery/tasks", nil, viewerToken); code != http.StatusOK {
		t.Fatalf("viewer 查询任务期望 200，得到 %d", code)
	}
	if code, _ := doJSON(t, http.MethodPost, srv.URL+"/api/v1/discovery/tasks",
		map[string]any{"name": "x", "collector_type": "exec:c1"}, viewerToken); code != http.StatusForbidden {
		t.Fatalf("viewer 创建任务期望 403，得到 %d", code)
	}
}
