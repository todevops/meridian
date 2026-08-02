// 应用归属引擎 API 测试（F-028）：三条规则按序命中、幂等、dry_run、group_map 覆盖。
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

// setupAttribution 构建 biz_app/host 模型与测试数据：
// mall-front/mall-order 两个应用；三台主机分别命中 标签继承/业务组映射/命名规范，一台不命中。
func setupAttribution(t *testing.T) (*gorm.DB, *httptest.Server, string) {
	t.Helper()
	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared", t.Name())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("打开内存库失败: %v", err)
	}
	if err := db.AutoMigrate(store.AllModels()...); err != nil {
		t.Fatalf("迁移失败: %v", err)
	}
	appModel := store.Model{Name: "应用系统", Code: "biz_app"}
	hostModel := store.Model{Name: "主机", Code: "host"}
	for _, m := range []*store.Model{&appModel, &hostModel} {
		if err := db.Create(m).Error; err != nil {
			t.Fatalf("创建模型失败: %v", err)
		}
	}
	apps := []store.CI{
		{ModelID: appModel.ID, Status: "active", Source: "manual", Attributes: datatypes.JSONMap{"code": "mall-front", "name": "商城前台"}},
		{ModelID: appModel.ID, Status: "active", Source: "manual", Attributes: datatypes.JSONMap{"code": "mall-order", "name": "订单中心"}},
	}
	for i := range apps {
		if err := db.Create(&apps[i]).Error; err != nil {
			t.Fatalf("创建应用失败: %v", err)
		}
	}
	hosts := []store.CI{
		// a) 标签继承：tags 含 app=mall-front（同时带 biz_group，验证规则 a 优先）。
		{ModelID: hostModel.ID, Status: "active", Source: "n9e", Attributes: datatypes.JSONMap{
			"ident": "web-01", "tags": "team=web,app=mall-front", "biz_group": "订单中台"}},
		// b) 业务组映射：电商前台 → mall-front。
		{ModelID: hostModel.ID, Status: "active", Source: "n9e", Attributes: datatypes.JSONMap{
			"ident": "web-02", "biz_group": "电商前台"}},
		// c) 命名规范：ident 第二段 order 经自定义 group_map → mall-order（见覆盖用例）。
		{ModelID: hostModel.ID, Status: "active", Source: "n9e", Attributes: datatypes.JSONMap{
			"ident": "svc-order-01"}},
		// 不命中：无 tags/biz_group，ident 第二段 bff 不在默认 group_map。
		{ModelID: hostModel.ID, Status: "active", Source: "n9e", Attributes: datatypes.JSONMap{
			"ident": "svc-bff-01"}},
	}
	for i := range hosts {
		if err := db.Create(&hosts[i]).Error; err != nil {
			t.Fatalf("创建主机失败: %v", err)
		}
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
	return db, srv, loginAs(t, srv, "admin", "admin-pass")
}

// countDeployedOn 统计 deployed_on 关系条数。
func countDeployedOn(t *testing.T, db *gorm.DB) int64 {
	t.Helper()
	var n int64
	if err := db.Model(&store.CIRelation{}).Where("relation_code = ?", "deployed_on").Count(&n).Error; err != nil {
		t.Fatalf("统计关系失败: %v", err)
	}
	return n
}

func TestAttributionRun(t *testing.T) {
	db, srv, token := setupAttribution(t)

	// 覆盖 group_map：补 order → mall-order，使命名规范命中。
	body := map[string]any{"rules": map[string]any{"group_map": map[string]string{
		"电商前台": "mall-front", "订单中台": "mall-order", "order": "mall-order",
	}}}
	code, resp := doJSON(t, http.MethodPost, srv.URL+"/api/v1/attribution/run", body, token)
	if code != http.StatusOK {
		t.Fatalf("归属运行期望 200，得到 %d: %v", code, resp)
	}
	if resp["matched"].(float64) != 3 {
		t.Fatalf("期望 matched=3，实际 %v", resp["matched"])
	}
	hits := resp["rules_hit"].(map[string]any)
	if hits["tag_inherit"].(float64) != 1 || hits["group_map"].(float64) != 1 || hits["naming"].(float64) != 1 {
		t.Fatalf("rules_hit 不符: %v", hits)
	}
	unmatched := resp["unmatched"].([]any)
	if len(unmatched) != 1 || unmatched[0] != "svc-bff-01" {
		t.Fatalf("unmatched 不符: %v", unmatched)
	}
	// 关系落库：3 条 deployed_on。
	if n := countDeployedOn(t, db); n != 3 {
		t.Fatalf("期望 3 条 deployed_on 关系，实际 %d", n)
	}
	// 规则 a 优先于 b：web-01 挂到 mall-front（tags 指定），而非 biz_group 映射的 mall-order。
	var appModel store.Model
	if err := db.First(&appModel, "code = ?", "biz_app").Error; err != nil {
		t.Fatalf("查询 biz_app 模型失败: %v", err)
	}
	var frontApp store.CI
	if err := db.Model(&store.CI{}).Where("model_id = ?", appModel.ID).
		Where(datatypes.JSONQuery("attributes").Equals("mall-front", "code")).First(&frontApp).Error; err != nil {
		t.Fatalf("查询 mall-front 失败: %v", err)
	}
	var hostModel store.Model
	if err := db.First(&hostModel, "code = ?", "host").Error; err != nil {
		t.Fatalf("查询 host 模型失败: %v", err)
	}
	var web01 store.CI
	if err := db.Model(&store.CI{}).Where("model_id = ?", hostModel.ID).
		Where(datatypes.JSONQuery("attributes").Equals("web-01", "ident")).First(&web01).Error; err != nil {
		t.Fatalf("查询 web-01 失败: %v", err)
	}
	var rel store.CIRelation
	if err := db.First(&rel, "relation_code = ? AND src_ci_id = ? AND dst_ci_id = ?", "deployed_on", frontApp.ID, web01.ID).Error; err != nil {
		t.Fatalf("web-01 应挂到 mall-front: %v", err)
	}

	// 幂等：再跑一次（同参数），关系不翻倍，matched 仍计 3。
	code, resp = doJSON(t, http.MethodPost, srv.URL+"/api/v1/attribution/run", body, token)
	if code != http.StatusOK || resp["matched"].(float64) != 3 {
		t.Fatalf("幂等重跑不符: %d %v", code, resp)
	}
	if n := countDeployedOn(t, db); n != 3 {
		t.Fatalf("幂等重跑关系不应翻倍，实际 %d", n)
	}
}

func TestAttributionDryRun(t *testing.T) {
	db, srv, token := setupAttribution(t)

	code, resp := doJSON(t, http.MethodPost, srv.URL+"/api/v1/attribution/run",
		map[string]any{"dry_run": true}, token)
	if code != http.StatusOK {
		t.Fatalf("dry_run 期望 200，得到 %d: %v", code, resp)
	}
	// 默认 group_map：web-01（tag）与 web-02（group_map）命中，svc-order-01 的 order 不在默认映射。
	if resp["matched"].(float64) != 2 {
		t.Fatalf("默认映射期望 matched=2，实际 %v", resp["matched"])
	}
	if n := countDeployedOn(t, db); n != 0 {
		t.Fatalf("dry_run 不应落库，实际 %d 条关系", n)
	}
}
