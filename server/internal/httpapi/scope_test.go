// 数据范围权限（F-005）HTTP 级验收：AC-F005-01~07 七条场景。
package httpapi

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"gorm.io/datatypes"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"meridian/server/internal/auth"
	"meridian/server/internal/store"
)

// scopeTopo 承载范围测试拓扑。
type scopeTopo struct {
	models                 map[string]string
	appS, appT             string
	hostS, hostT           string
	hostShared, hostOrphan string
	dbS, dbT               string
	nsS, wlS               string
	rack                   string
}

// setupScope 构建完整路由 + 两应用拓扑，返回 admin 令牌。
func setupScope(t *testing.T) (*gorm.DB, *httptest.Server, scopeTopo, string) {
	t.Helper()
	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared", t.Name())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("打开内存库失败: %v", err)
	}
	if err := db.AutoMigrate(store.AllModels()...); err != nil {
		t.Fatalf("迁移失败: %v", err)
	}
	strAttr := func(code string) store.AttributeDefinition {
		return store.AttributeDefinition{Name: code, Code: code, Type: "string"}
	}
	tp := scopeTopo{models: map[string]string{}}
	for code, attrs := range map[string][]store.AttributeDefinition{
		"biz_line":      {strAttr("name")},
		"biz_app":       {strAttr("name"), strAttr("owner")},
		"host":          {strAttr("ident")},
		"db_instance":   {strAttr("instance_addr"), strAttr("component_type"), strAttr("version"), strAttr("role"), strAttr("cluster_name")},
		"k8s_namespace": {strAttr("name")},
		"k8s_workload":  {strAttr("name")},
		"rack":          {strAttr("name")},
	} {
		m := store.Model{Name: code, Code: code, Attributes: datatypes.NewJSONType(attrs)}
		if err := db.Create(&m).Error; err != nil {
			t.Fatalf("创建模型 %s 失败: %v", code, err)
		}
		tp.models[code] = m.ID
	}
	mkCI := func(modelCode string, attrs map[string]any) string {
		ci := store.CI{ModelID: tp.models[modelCode], Status: "active", Source: "manual",
			Attributes: datatypes.JSONMap(attrs), FieldSources: datatypes.JSONMap{}}
		if err := db.Create(&ci).Error; err != nil {
			t.Fatalf("创建 CI 失败: %v", err)
		}
		return ci.ID
	}
	tp.appS = mkCI("biz_app", map[string]any{"name": "订单中心", "owner": "张三"})
	tp.appT = mkCI("biz_app", map[string]any{"name": "支付中心", "owner": "李四"})
	tp.hostS = mkCI("host", map[string]any{"ident": "s-host"})
	tp.hostT = mkCI("host", map[string]any{"ident": "t-host"})
	tp.hostShared = mkCI("host", map[string]any{"ident": "shared-host"})
	tp.hostOrphan = mkCI("host", map[string]any{"ident": "orphan-host"})
	tp.dbS = mkCI("db_instance", map[string]any{"instance_addr": "10.1.0.1:3306", "component_type": "mysql", "version": "5.7.44", "role": "master", "cluster_name": "order-mysql"})
	tp.dbT = mkCI("db_instance", map[string]any{"instance_addr": "10.1.0.2:3306", "component_type": "mysql", "version": "8.0.36", "role": "master", "cluster_name": "pay-mysql"})
	tp.nsS = mkCI("k8s_namespace", map[string]any{"name": "order-ns"})
	tp.wlS = mkCI("k8s_workload", map[string]any{"name": "order-deploy"})
	tp.rack = mkCI("rack", map[string]any{"name": "rack-A01"})
	mkRel := func(code, src, dst string) {
		rel := store.CIRelation{RelationCode: code, SrcCIID: src, DstCIID: dst, Source: store.RelationSourceManual}
		if err := db.Create(&rel).Error; err != nil {
			t.Fatalf("创建关系失败: %v", err)
		}
	}
	mkRel("deployed_on", tp.appS, tp.hostS)
	mkRel("deployed_on", tp.appT, tp.hostT)
	mkRel("deployed_on", tp.appS, tp.hostShared)
	mkRel("deployed_on", tp.appT, tp.hostShared)
	mkRel("depends_on", tp.appS, tp.dbS)
	mkRel("depends_on", tp.appT, tp.dbT)
	mkRel("mounted_to", tp.nsS, tp.appS)
	mkRel("in_namespace", tp.wlS, tp.nsS)

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
	return db, srv, tp, loginAs(t, srv, "admin", "admin-pass")
}

// mustScopedUser 创建 system_owner 用户并绑定数据范围，返回其登录令牌与用户 ID。
func mustScopedUser(t *testing.T, srv *httptest.Server, adminToken, username string, scopeIDs []string) (string, string) {
	t.Helper()
	code, body := doJSON(t, http.MethodPost, srv.URL+"/api/v1/users",
		map[string]any{"username": username, "display_name": username, "password": "pass123", "roles": []string{"system_owner"}}, adminToken)
	if code != http.StatusOK {
		t.Fatalf("创建用户失败（%d）: %v", code, body)
	}
	uid, _ := body["id"].(string)
	code, body = doJSON(t, http.MethodPatch, srv.URL+"/api/v1/users/"+uid,
		map[string]any{"scope_app_ids": scopeIDs}, adminToken)
	if code != http.StatusOK {
		t.Fatalf("绑定数据范围失败（%d）: %v", code, body)
	}
	return loginAs(t, srv, username, "pass123"), uid
}

// listCIIDs 拉取某模型全部可见 CI 的 ID 集合。
func listCIIDs(t *testing.T, srv *httptest.Server, token, modelID string) map[string]bool {
	t.Helper()
	code, body := doJSON(t, http.MethodGet,
		fmt.Sprintf("%s/api/v1/cis?model_id=%s&page_size=100", srv.URL, modelID), nil, token)
	if code != http.StatusOK {
		t.Fatalf("查询 CI 列表失败（%d）: %v", code, body)
	}
	ids := map[string]bool{}
	for _, it := range body["items"].([]any) {
		ids[it.(map[string]any)["id"].(string)] = true
	}
	return ids
}

// AC-F005-01 全类型可读且仅返回范围内 CI（含命名空间两跳工作负载）。
func TestScopeListOnlyInScopeCIs(t *testing.T) {
	_, srv, tp, adminToken := setupScope(t)
	ownerToken, _ := mustScopedUser(t, srv, adminToken, "owner-s", []string{tp.appS})

	hosts := listCIIDs(t, srv, ownerToken, tp.models["host"])
	if !hosts[tp.hostS] || !hosts[tp.hostShared] {
		t.Fatalf("范围内主机不可见: %v", hosts)
	}
	if hosts[tp.hostT] || hosts[tp.hostOrphan] {
		t.Fatalf("范围外/无归属主机不应可见: %v", hosts)
	}
	dbs := listCIIDs(t, srv, ownerToken, tp.models["db_instance"])
	if !dbs[tp.dbS] || dbs[tp.dbT] {
		t.Fatalf("数据库实例范围裁剪错误: %v", dbs)
	}
	wls := listCIIDs(t, srv, ownerToken, tp.models["k8s_workload"])
	if !wls[tp.wlS] {
		t.Fatalf("命名空间两跳工作负载应可见: %v", wls)
	}
	// 业务模型本身可见（只读导航）。
	apps := listCIIDs(t, srv, ownerToken, tp.models["biz_app"])
	if !apps[tp.appS] || !apps[tp.appT] {
		t.Fatalf("biz_app 应对范围内用户全量可见: %v", apps)
	}
}

// AC-F005-02 搜索不越权。
func TestScopeSearchNotLeaking(t *testing.T) {
	_, srv, tp, adminToken := setupScope(t)
	ownerToken, _ := mustScopedUser(t, srv, adminToken, "owner-s", []string{tp.appS})

	code, body := doJSON(t, http.MethodGet, srv.URL+"/api/v1/search?q=t-host", nil, ownerToken)
	if code != http.StatusOK {
		t.Fatalf("搜索失败（%d）: %v", code, body)
	}
	for _, g := range body["groups"].([]any) {
		items := g.(map[string]any)["items"].([]any)
		if len(items) > 0 {
			t.Fatalf("搜索泄露他系统资产: %v", items)
		}
	}
	// 范围内关键字可搜到。
	_, body = doJSON(t, http.MethodGet, srv.URL+"/api/v1/search?q=s-host", nil, ownerToken)
	found := false
	for _, g := range body["groups"].([]any) {
		if g.(map[string]any)["kind"] == "cis" && len(g.(map[string]any)["items"].([]any)) > 0 {
			found = true
		}
	}
	if !found {
		t.Fatalf("范围内资产应可搜到: %v", body)
	}
}

// AC-F005-03 越权直访 404（详情与关系），AC-F005-04 多重归属双方可见，AC-F005-05 全量角色不受限。
func TestScopeDirectAccess404AndMultiOwnership(t *testing.T) {
	_, srv, tp, adminToken := setupScope(t)
	ownerS, _ := mustScopedUser(t, srv, adminToken, "owner-s", []string{tp.appS})
	ownerT, _ := mustScopedUser(t, srv, adminToken, "owner-t", []string{tp.appT})

	code, _ := doJSON(t, http.MethodGet, srv.URL+"/api/v1/cis/"+tp.hostT, nil, ownerS)
	if code != http.StatusNotFound {
		t.Fatalf("越权直访详情应 404，实际 %d", code)
	}
	code, _ = doJSON(t, http.MethodGet, srv.URL+"/api/v1/cis/"+tp.hostT+"/relations", nil, ownerS)
	if code != http.StatusNotFound {
		t.Fatalf("越权直访关系应 404，实际 %d", code)
	}
	// 多重归属：S 与 T 用户均可见共享主机。
	for name, token := range map[string]string{"S": ownerS, "T": ownerT} {
		code, _ = doJSON(t, http.MethodGet, srv.URL+"/api/v1/cis/"+tp.hostShared, nil, token)
		if code != http.StatusOK {
			t.Fatalf("多重归属主机对 %s 用户应可见，实际 %d", name, code)
		}
	}
	// 全量角色（admin，scope 为空）不受限。
	code, _ = doJSON(t, http.MethodGet, srv.URL+"/api/v1/cis/"+tp.hostT, nil, adminToken)
	if code != http.StatusOK {
		t.Fatalf("admin 应不受数据范围限制，实际 %d", code)
	}
	if hosts := listCIIDs(t, srv, adminToken, tp.models["host"]); len(hosts) != 4 {
		t.Fatalf("admin 应见全部 4 台主机，实际 %d", len(hosts))
	}
}

// AC-F005-06 范围变更即时生效（无需重新登录）。
func TestScopeChangeEffectiveImmediately(t *testing.T) {
	_, srv, tp, adminToken := setupScope(t)
	ownerToken, uid := mustScopedUser(t, srv, adminToken, "owner-s", []string{tp.appS})

	code, _ := doJSON(t, http.MethodGet, srv.URL+"/api/v1/cis/"+tp.hostS, nil, ownerToken)
	if code != http.StatusOK {
		t.Fatalf("变更前应可见 S 主机，实际 %d", code)
	}
	// 管理员把范围从 S 调整为 T。
	code, body := doJSON(t, http.MethodPatch, srv.URL+"/api/v1/users/"+uid,
		map[string]any{"scope_app_ids": []string{tp.appT}}, adminToken)
	if code != http.StatusOK {
		t.Fatalf("调整数据范围失败（%d）: %v", code, body)
	}
	// 同一会话下立即生效。
	code, _ = doJSON(t, http.MethodGet, srv.URL+"/api/v1/cis/"+tp.hostT, nil, ownerToken)
	if code != http.StatusOK {
		t.Fatalf("变更后应立即可见 T 主机，实际 %d", code)
	}
	code, _ = doJSON(t, http.MethodGet, srv.URL+"/api/v1/cis/"+tp.hostS, nil, ownerToken)
	if code != http.StatusNotFound {
		t.Fatalf("变更后应立即不可见 S 主机，实际 %d", code)
	}
}

// AC-F005-07 共享基础设施不裁剪（机柜 CI 与 IPAM 只读全量可见）。
func TestScopeSharedInfraNotTrimmed(t *testing.T) {
	_, srv, tp, adminToken := setupScope(t)
	ownerToken, _ := mustScopedUser(t, srv, adminToken, "owner-s", []string{tp.appS})

	racks := listCIIDs(t, srv, ownerToken, tp.models["rack"])
	if !racks[tp.rack] {
		t.Fatalf("机柜（共享基础设施）不裁剪: %v", racks)
	}
	code, body := doJSON(t, http.MethodGet, srv.URL+"/api/v1/ipam/prefixes", nil, ownerToken)
	if code != http.StatusOK {
		t.Fatalf("IPAM 前缀应只读全量可见（%d）: %v", code, body)
	}
}

// 用户响应契约：scope_app_ids 与 scope_app_names；非法范围 ID 拒绝。
func TestUserScopePayloadAndValidation(t *testing.T) {
	_, srv, tp, adminToken := setupScope(t)
	_, uid := mustScopedUser(t, srv, adminToken, "owner-s", []string{tp.appS})

	code, body := doJSON(t, http.MethodGet, srv.URL+"/api/v1/users/"+uid, nil, adminToken)
	if code != http.StatusOK {
		t.Fatalf("查询用户失败（%d）: %v", code, body)
	}
	ids := body["scope_app_ids"].([]any)
	names := body["scope_app_names"].([]any)
	if len(ids) != 1 || ids[0] != tp.appS {
		t.Fatalf("scope_app_ids 错误: %v", ids)
	}
	if len(names) != 1 || names[0] != "订单中心" {
		t.Fatalf("scope_app_names 错误: %v", names)
	}
	// 列表同样携带。
	_, body = doJSON(t, http.MethodGet, srv.URL+"/api/v1/users?keyword=owner-s", nil, adminToken)
	item := body["items"].([]any)[0].(map[string]any)
	if len(item["scope_app_ids"].([]any)) != 1 {
		t.Fatalf("用户列表应含 scope_app_ids: %v", item)
	}
	// 非 biz_app CI 作为范围 → 400。
	code, _ = doJSON(t, http.MethodPatch, srv.URL+"/api/v1/users/"+uid,
		map[string]any{"scope_app_ids": []string{tp.hostS}}, adminToken)
	if code != http.StatusBadRequest {
		t.Fatalf("非 biz_app 范围应 400，实际 %d", code)
	}
}
