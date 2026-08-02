// CI 关系创建/删除 API 测试：模型校验、one_to_one 改挂替换、删除与 404。
package httpapi

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/gin-gonic/gin"
	"gorm.io/datatypes"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"cmdb/server/internal/auth"
	"cmdb/server/internal/discovery"
	"cmdb/server/internal/store"
)

// setupRelations 构建含 room/rack 模型（located_in one_to_one）的测试环境。
func setupRelations(t *testing.T) (*gorm.DB, *httptest.Server, string) {
	t.Helper()
	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared", t.Name())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("打开内存库失败: %v", err)
	}
	if err := db.AutoMigrate(store.AllModels()...); err != nil {
		t.Fatalf("迁移失败: %v", err)
	}
	roomModel := store.Model{Name: "机房", Code: "room"}
	rackModel := store.Model{
		Name: "机柜", Code: "rack",
		Relations: datatypes.NewJSONType([]store.RelationDefinition{
			{Name: "所在机房", Code: "located_in", TargetModel: "room", Cardinality: "one_to_one", Direction: "outgoing"},
		}),
	}
	hostModel := store.Model{Name: "主机", Code: "host"}
	for _, m := range []*store.Model{&roomModel, &rackModel, &hostModel} {
		if err := db.Create(m).Error; err != nil {
			t.Fatalf("创建模型失败: %v", err)
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
	srv := httptest.NewServer(NewRouter(db, discovery.NewPipeline(db), authSvc))
	t.Cleanup(srv.Close)
	return db, srv, loginAs(t, srv, "admin", "admin-pass")
}

func mustCICreated(t *testing.T, db *gorm.DB, modelCode, name string) store.CI {
	t.Helper()
	var model store.Model
	if err := db.First(&model, "code = ?", modelCode).Error; err != nil {
		t.Fatalf("查询模型失败: %v", err)
	}
	ci := store.CI{ModelID: model.ID, Attributes: datatypes.JSONMap{"name": name}, Status: "active", Source: "manual"}
	if err := db.Create(&ci).Error; err != nil {
		t.Fatalf("创建 CI 失败: %v", err)
	}
	return ci
}

func TestCIRelationCreateReplaceDelete(t *testing.T) {
	db, srv, token := setupRelations(t)
	roomA := mustCICreated(t, db, "room", "机房A")
	roomB := mustCICreated(t, db, "room", "机房B")
	rack := mustCICreated(t, db, "rack", "A01")
	host := mustCICreated(t, db, "host", "web-01")
	relURL := srv.URL + "/api/v1/cis/" + rack.ID + "/relations"

	// 创建 located_in → 机房A。
	code, rel := doJSON(t, http.MethodPost, relURL,
		map[string]any{"relation_code": "located_in", "peer_ci_id": roomA.ID}, token)
	if code != http.StatusOK {
		t.Fatalf("创建关系失败（%d）: %v", code, rel)
	}
	if rel["relation_code"] != "located_in" || rel["direction"] != "outgoing" {
		t.Fatalf("关系形状不符: %v", rel)
	}

	// 目标模型不匹配（host 不是 room）→ 400。
	if code, _ := doJSON(t, http.MethodPost, relURL,
		map[string]any{"relation_code": "located_in", "peer_ci_id": host.ID}, token); code != http.StatusBadRequest {
		t.Fatalf("目标模型不匹配期望 400，得到 %d", code)
	}
	// 未定义的关系编码 → 400。
	if code, _ := doJSON(t, http.MethodPost, relURL,
		map[string]any{"relation_code": "nope", "peer_ci_id": roomA.ID}, token); code != http.StatusBadRequest {
		t.Fatalf("未定义关系期望 400，得到 %d", code)
	}

	// one_to_one 改挂机房B：旧关系被替换。
	if code, _ := doJSON(t, http.MethodPost, relURL,
		map[string]any{"relation_code": "located_in", "peer_ci_id": roomB.ID}, token); code != http.StatusOK {
		t.Fatalf("改挂失败（%d）", code)
	}
	var rels []store.CIRelation
	if err := db.Where("relation_code = ?", "located_in").Find(&rels).Error; err != nil {
		t.Fatalf("查询关系失败: %v", err)
	}
	if len(rels) != 1 || rels[0].DstCIID != roomB.ID {
		t.Fatalf("one_to_one 应替换旧关系: %+v", rels)
	}

	// 删除关系 → 200；重复删除 → 404。
	if code, _ := doJSON(t, http.MethodDelete, relURL+"/located_in/"+roomB.ID, nil, token); code != http.StatusOK {
		t.Fatalf("删除关系失败（%d）", code)
	}
	if code, _ := doJSON(t, http.MethodDelete, relURL+"/located_in/"+roomB.ID, nil, token); code != http.StatusNotFound {
		t.Fatalf("重复删除期望 404，得到 %d", code)
	}
}
