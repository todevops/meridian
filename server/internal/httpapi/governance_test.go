// 治理后端 API 链路测试（F-080/F-081/F-026/F-004）：
// 质量看板与下钻、稽核规则 CRUD/执行/待办闭环、生命周期流转与退役联动、审计查询。
package httpapi

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"gorm.io/datatypes"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"meridian/server/internal/auth"
	"meridian/server/internal/store"
)

// setupGovernance 构建完整路由与种子数据：host/biz_app 模型 + 两台主机（一台健康一台死主机）。
// 死主机先经 API 合法建档（含 ip），再直接改库抹掉 ip 并回拨 updated_at，
// 模拟"属性不完整 + 心跳停更 + 数据陈旧"的违规形态（API 校验不允许直接建不完整 CI）。
// 返回服务、admin 令牌、死主机 CI ID、数据库句柄。
func setupGovernance(t *testing.T) (*httptest.Server, string, string, *gorm.DB) {
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
	token := loginAs(t, srv, "admin", "admin-pass")

	// 模型：host（ident/ip 必填）与 biz_app。
	post := func(path string, body any) map[string]any {
		t.Helper()
		code, resp := doJSON(t, http.MethodPost, path, body, token)
		if code != http.StatusOK {
			t.Fatalf("POST %s 失败（%d）: %v", path, code, resp)
		}
		return resp
	}
	hostModel := post(srv.URL+"/api/v1/models", map[string]any{
		"code": "host", "name": "主机",
		"attributes": []map[string]any{
			{"code": "ident", "name": "标识", "type": "string", "required": true},
			{"code": "ip", "name": "主 IP", "type": "ip", "required": true},
		},
	})
	appModel := post(srv.URL+"/api/v1/models", map[string]any{
		"code": "biz_app", "name": "应用",
		"attributes": []map[string]any{
			{"code": "code", "name": "编码", "type": "string", "required": true},
		},
		"relations": []map[string]any{
			{"code": "deployed_on", "name": "部署于", "target_model": "host", "cardinality": "many_to_many", "direction": "outgoing"},
		},
	})
	app := post(srv.URL+"/api/v1/cis", map[string]any{
		"model_id": appModel["id"], "attributes": map[string]any{"code": "mall"},
	})
	good := post(srv.URL+"/api/v1/cis", map[string]any{
		"model_id": hostModel["id"],
		"attributes": map[string]any{
			"ident": "h-good", "ip": "10.0.0.1",
			"last_heartbeat_at": time.Now().Format(time.RFC3339),
		},
	})
	dead := post(srv.URL+"/api/v1/cis", map[string]any{
		"model_id": hostModel["id"],
		"attributes": map[string]any{
			"ident":             "h-dead",
			"ip":                "10.0.0.9",
			"last_heartbeat_at": time.Now().Add(-10 * 24 * time.Hour).Format(time.RFC3339),
		},
	})
	// h-good 归属应用（deployed_on）。
	post(srv.URL+"/api/v1/cis/"+fmt.Sprint(app["id"])+"/relations", map[string]any{
		"relation_code": "deployed_on", "peer_ci_id": good["id"],
	})
	// 死主机违规形态：抹掉 ip（属性不完整）+ 回拨 updated_at（数据陈旧）。
	deadID := fmt.Sprint(dead["id"])
	if err := db.Model(&store.CI{}).Where("id = ?", deadID).Updates(map[string]any{
		"attributes": datatypes.JSONMap{
			"ident":             "h-dead",
			"last_heartbeat_at": time.Now().Add(-10 * 24 * time.Hour).Format(time.RFC3339),
		},
		"updated_at": time.Now().Add(-10 * 24 * time.Hour),
	}).Error; err != nil {
		t.Fatalf("改写死主机失败: %v", err)
	}
	return srv, token, deadID, db
}

// TestDashboardQuality 验证五指标汇总与下钻缺失清单。
func TestDashboardQuality(t *testing.T) {
	srv, token, deadID, _ := setupGovernance(t)

	code, body := doJSON(t, http.MethodGet, srv.URL+"/api/v1/dashboard/quality", nil, token)
	if code != http.StatusOK {
		t.Fatalf("GET /dashboard/quality 失败（%d）: %v", code, body)
	}
	models, _ := body["models"].([]any)
	var host map[string]any
	for _, m := range models {
		mm, _ := m.(map[string]any)
		if mm["code"] == "host" {
			host = mm
		}
	}
	if host == nil {
		t.Fatalf("汇总缺少 host 模型: %v", models)
	}
	// 两台主机：必填单元格 4 缺 1 → 75；归属 1/2 → 50；孤岛 1；死主机 updated_at 回拨 10 天 → 50。
	if host["completeness"] != 75.0 {
		t.Errorf("completeness 应为 75: %v", host["completeness"])
	}
	if host["relation_completeness"] != 50.0 {
		t.Errorf("relation_completeness 应为 50: %v", host["relation_completeness"])
	}
	if host["orphan_count"] != 1.0 {
		t.Errorf("orphan_count 应为 1: %v", host["orphan_count"])
	}
	if host["stale_pct"] != 50.0 {
		t.Errorf("stale_pct 应为 50: %v", host["stale_pct"])
	}
	if host["no_heartbeat_pct"] != 50.0 {
		t.Errorf("no_heartbeat_pct 应为 50: %v", host["no_heartbeat_pct"])
	}
	monitor, _ := body["monitor"].(map[string]any)
	if monitor["no_heartbeat_pct"] != 50.0 {
		t.Errorf("monitor.no_heartbeat_pct 应为 50: %v", monitor)
	}

	// 下钻：属性完整率缺失清单应只含死主机。
	code, body = doJSON(t, http.MethodGet,
		srv.URL+"/api/v1/dashboard/quality/drilldown?model_id=host&metric=completeness&page=1", nil, token)
	if code != http.StatusOK {
		t.Fatalf("drilldown 失败（%d）: %v", code, body)
	}
	if body["total"] != 1.0 {
		t.Fatalf("下钻 total 应为 1: %v", body)
	}
	items, _ := body["items"].([]any)
	first, _ := items[0].(map[string]any)
	if first["id"] != deadID {
		t.Errorf("下钻应命中死主机: %v", first["id"])
	}

	// 非法 metric 应 400。
	code, _ = doJSON(t, http.MethodGet,
		srv.URL+"/api/v1/dashboard/quality/drilldown?model_id=host&metric=bogus", nil, token)
	if code != http.StatusBadRequest {
		t.Errorf("非法 metric 应 400，实际 %d", code)
	}
}

// TestGovernanceRulesAndTodos 验证规则 CRUD + 手动执行 + 待办闭环 + 修复自动关闭。
func TestGovernanceRulesAndTodos(t *testing.T) {
	srv, token, deadID, _ := setupGovernance(t)

	// 创建规则：host 必须有 ip（死主机违规）。
	code, rule := doJSON(t, http.MethodPost, srv.URL+"/api/v1/governance/rules", map[string]any{
		"name": "主机必须有主 IP", "model_code": "host",
		"filter": map[string]any{}, "assertion": `not_empty(ip)`,
		"message": "主机缺少主 IP", "enabled": true,
	}, token)
	if code != http.StatusOK {
		t.Fatalf("创建规则失败（%d）: %v", code, rule)
	}
	ruleID := fmt.Sprint(rule["id"])

	// 非法断言应 400。
	code, _ = doJSON(t, http.MethodPost, srv.URL+"/api/v1/governance/rules", map[string]any{
		"name": "坏规则", "model_code": "host", "assertion": `not_empty(`, "message": "x",
	}, token)
	if code != http.StatusBadRequest {
		t.Errorf("非法断言应 400，实际 %d", code)
	}

	// 手动执行：1 违规、1 待办。
	code, run := doJSON(t, http.MethodPost, srv.URL+"/api/v1/governance/rules/"+ruleID+"/run", nil, token)
	if code != http.StatusOK {
		t.Fatalf("执行规则失败（%d）: %v", code, run)
	}
	if run["checked"] != 2.0 || run["todos_created"] != 1.0 {
		t.Fatalf("执行结果不符: %v", run)
	}

	// 待办列表：open 1 条，命中死主机。
	code, todos := doJSON(t, http.MethodGet, srv.URL+"/api/v1/governance/todos?status=open", nil, token)
	if code != http.StatusOK || todos["total"] != 1.0 {
		t.Fatalf("待办列表不符（%d）: %v", code, todos)
	}
	items, _ := todos["items"].([]any)
	todo, _ := items[0].(map[string]any)
	if todo["ci_id"] != deadID || todo["rule_name"] != "主机必须有主 IP" {
		t.Errorf("待办内容不符: %v", todo)
	}

	// 修复死主机（补 ip），再次执行 → 自动关闭。
	code, _ = doJSON(t, http.MethodPatch, srv.URL+"/api/v1/cis/"+deadID,
		map[string]any{"attributes": map[string]any{"ip": "10.0.0.9"}}, token)
	if code != http.StatusOK {
		t.Fatalf("修复 CI 失败（%d）", code)
	}
	code, run = doJSON(t, http.MethodPost, srv.URL+"/api/v1/governance/rules/"+ruleID+"/run", nil, token)
	if code != http.StatusOK || run["todos_closed"] != 1.0 {
		t.Fatalf("修复后执行应自动关闭待办: %v", run)
	}
	code, todos = doJSON(t, http.MethodGet, srv.URL+"/api/v1/governance/todos?status=closed", nil, token)
	if todos["total"] != 1.0 {
		t.Errorf("closed 待办应为 1 条: %v", todos)
	}

	// PATCH 规则（改 dry_run）。
	code, patched := doJSON(t, http.MethodPatch, srv.URL+"/api/v1/governance/rules/"+ruleID,
		map[string]any{"dry_run": true}, token)
	if code != http.StatusOK || patched["dry_run"] != true {
		t.Errorf("PATCH 规则失败（%d）: %v", code, patched)
	}

	// 人工关闭接口幂等。
	todoID := fmt.Sprint(todo["id"])
	code, _ = doJSON(t, http.MethodPost, srv.URL+"/api/v1/governance/todos/"+todoID+"/close", nil, token)
	if code != http.StatusOK {
		t.Errorf("关闭待办失败（%d）", code)
	}
}

// TestLifecycleTransitionAndRetire 验证状态流转校验、会签清单与退役执行。
func TestLifecycleTransitionAndRetire(t *testing.T) {
	srv, token, deadID, _ := setupGovernance(t)

	// 非法流转：active → retired 必须经 pending_retire。
	code, body := doJSON(t, http.MethodPost, srv.URL+"/api/v1/cis/"+deadID+"/lifecycle",
		map[string]any{"to": "retired"}, token)
	if code != http.StatusBadRequest {
		t.Fatalf("active→retired 应被拒绝（%d）: %v", code, body)
	}
	// 合法流转：active → pending_retire。
	code, ci := doJSON(t, http.MethodPost, srv.URL+"/api/v1/cis/"+deadID+"/lifecycle",
		map[string]any{"to": "pending_retire"}, token)
	if code != http.StatusOK || ci["status"] != "pending_retire" {
		t.Fatalf("active→pending_retire 失败（%d）: %v", code, ci)
	}

	// 会签清单：死主机 eligible（心跳停更/无扫描/无云实例）；健康主机不 eligible。
	code, checks := doJSON(t, http.MethodGet, srv.URL+"/api/v1/lifecycle/retire-candidates", nil, token)
	if code != http.StatusOK {
		t.Fatalf("会签清单失败（%d）: %v", code, checks)
	}
	items, _ := checks["items"].([]any)
	// pending_retire 不再出现在候选清单；健康主机应在且 heartbeat_ok=true。
	if len(items) != 1 {
		t.Fatalf("候选清单应只含健康主机（死主机已 pending_retire）: %d", len(items))
	}
	entry, _ := items[0].(map[string]any)
	if entry["eligible"] != false || entry["heartbeat_ok"] != true {
		t.Errorf("健康主机会签判定不符: %v", entry)
	}

	// 退役执行：未 confirm 拒绝；confirm 后动作齐全且状态 retired。
	code, _ = doJSON(t, http.MethodPost, srv.URL+"/api/v1/lifecycle/retire",
		map[string]any{"ci_id": deadID, "confirm": false}, token)
	if code != http.StatusBadRequest {
		t.Fatalf("未 confirm 应 400，实际 %d", code)
	}
	code, ret := doJSON(t, http.MethodPost, srv.URL+"/api/v1/lifecycle/retire",
		map[string]any{"ci_id": deadID, "confirm": true}, token)
	if code != http.StatusOK {
		t.Fatalf("退役执行失败（%d）: %v", code, ret)
	}
	actions, _ := ret["actions"].([]any)
	actionTypes := map[string]bool{}
	for _, a := range actions {
		aa, _ := a.(map[string]any)
		actionTypes[fmt.Sprint(aa["type"])] = aa["ok"] == true
	}
	for _, want := range []string{"status", "n9e_remove_target", "jumpserver_disable", "ipam_idle"} {
		if _, ok := actionTypes[want]; !ok {
			t.Errorf("缺少联动动作 %s: %v", want, actions)
		}
	}
	if !actionTypes["status"] {
		t.Errorf("status 动作必须成功: %v", actions)
	}
	// 未配置 n9e/JumpServer：动作报告失败（跳过说明）。
	if actionTypes["n9e_remove_target"] || actionTypes["jumpserver_disable"] {
		t.Errorf("未配置集成的动作应 ok=false: %v", actions)
	}

	// 审计：lifecycle 与 retire 记录均可查。
	code, audits := doJSON(t, http.MethodGet, srv.URL+"/api/v1/audit?ci_id="+deadID, nil, token)
	if code != http.StatusOK {
		t.Fatalf("审计查询失败（%d）", code)
	}
	foundLifecycle, foundRetire := false, false
	for _, a := range audits["items"].([]any) {
		aa, _ := a.(map[string]any)
		if aa["action"] == "lifecycle" {
			foundLifecycle = true
			if aa["operator"] != "admin" {
				t.Errorf("lifecycle 审计操作者应为 admin: %v", aa)
			}
		}
		if aa["action"] == "retire" {
			foundRetire = true
		}
	}
	if !foundLifecycle || !foundRetire {
		t.Errorf("审计缺少 lifecycle/retire 记录: %v", audits["items"])
	}
}

// TestAuditQuery 验证审计查询的过滤与倒序分页。
func TestAuditQuery(t *testing.T) {
	srv, token, deadID, _ := setupGovernance(t)

	// 全部记录（含建模/建档），倒序。
	code, all := doJSON(t, http.MethodGet, srv.URL+"/api/v1/audit", nil, token)
	if code != http.StatusOK {
		t.Fatalf("审计查询失败（%d）", code)
	}
	total := all["total"].(float64)
	if total < 3 { // 至少 2 台主机 + 1 应用建档
		t.Fatalf("审计记录过少: %v", total)
	}

	// 按 ci_id 过滤。
	code, byCI := doJSON(t, http.MethodGet, srv.URL+"/api/v1/audit?ci_id="+deadID, nil, token)
	if code != http.StatusOK || byCI["total"] != 1.0 {
		t.Fatalf("ci_id 过滤不符: %v", byCI)
	}
	// 按 operator 过滤：建档操作者为 admin。
	code, byOp := doJSON(t, http.MethodGet, srv.URL+"/api/v1/audit?operator=admin", nil, token)
	if byOp["total"] != total {
		t.Errorf("operator=admin 应覆盖全部记录: %v vs %v", byOp["total"], total)
	}
	code, none := doJSON(t, http.MethodGet, srv.URL+"/api/v1/audit?operator=nobody", nil, token)
	if none["total"] != 0.0 {
		t.Errorf("operator=nobody 应为空: %v", none)
	}
	// 按 source 过滤。
	code, bySrc := doJSON(t, http.MethodGet, srv.URL+"/api/v1/audit?source=manual", nil, token)
	if bySrc["total"] != total {
		t.Errorf("source=manual 应覆盖全部记录: %v vs %v", bySrc["total"], total)
	}
}

// TestGovernancePermissions 验证新权限点挂载：viewer 只读可过、写操作 403。
func TestGovernancePermissions(t *testing.T) {
	srv, adminToken, _, _ := setupGovernance(t)

	// 建 viewer 账号并登录。
	code, viewer := doJSON(t, http.MethodPost, srv.URL+"/api/v1/users", map[string]any{
		"username": "v1", "display_name": "只读用户", "password": "viewer-pass-1", "roles": []string{"viewer"},
	}, adminToken)
	if code != http.StatusOK {
		t.Fatalf("创建 viewer 失败（%d）: %v", code, viewer)
	}
	viewerToken := loginAs(t, srv, "v1", "viewer-pass-1")

	// 只读端点放行。
	for _, path := range []string{
		"/api/v1/dashboard/quality",
		"/api/v1/governance/rules",
		"/api/v1/governance/todos",
		"/api/v1/audit",
	} {
		code, body := doJSON(t, http.MethodGet, srv.URL+path, nil, viewerToken)
		if code != http.StatusOK {
			t.Errorf("viewer GET %s 应放行（%d）: %v", path, code, body)
		}
	}
	// 写端点拦截。
	code, _ = doJSON(t, http.MethodPost, srv.URL+"/api/v1/governance/rules", map[string]any{
		"name": "x", "model_code": "host", "assertion": "not_empty(ip)", "message": "x",
	}, viewerToken)
	if code != http.StatusForbidden {
		t.Errorf("viewer 创建规则应 403，实际 %d", code)
	}
	code, _ = doJSON(t, http.MethodPost, srv.URL+"/api/v1/lifecycle/retire",
		map[string]any{"ci_id": "any", "confirm": true}, viewerToken)
	if code != http.StatusForbidden {
		t.Errorf("viewer 退役执行应 403，实际 %d", code)
	}
}
