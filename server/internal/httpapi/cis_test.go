// CI 写路径 HTTP 级行为钉住测试：patchCI 的合并/校验/唯一性/审计语义。
// 是后续「CI 写入深模块」重构的防回归网——钉住的是现有语义，重构后这些测试应原样通过。
package httpapi

import (
	"net/http"
	"testing"

	"gorm.io/datatypes"
	"gorm.io/gorm"

	"meridian/server/internal/store"
)

// mustCI 直接落库一条 CI（绕过 create 接口，聚焦 patch 行为）。
func mustCI(t *testing.T, db *gorm.DB, modelID string, attrs map[string]any) store.CI {
	t.Helper()
	ci := store.CI{
		ModelID:      modelID,
		Attributes:   datatypes.JSONMap(attrs),
		FieldSources: datatypes.JSONMap{},
		Status:       "active",
		Source:       "n9e",
	}
	if err := db.Create(&ci).Error; err != nil {
		t.Fatalf("创建 CI 失败: %v", err)
	}
	return ci
}

func TestPatchCIMergesAttributesAndAudits(t *testing.T) {
	db, srv, model, token := setupPool(t)
	ci := mustCI(t, db, model.ID, map[string]any{"ident": "web-01", "ip": "10.0.0.1"})

	code, body := doJSON(t, http.MethodPatch, srv.URL+"/api/v1/cis/"+ci.ID,
		map[string]any{"attributes": map[string]any{"ip": "10.0.0.2"}}, token)
	if code != http.StatusOK {
		t.Fatalf("PATCH 失败（%d）: %v", code, body)
	}

	var reloaded store.CI
	if err := db.First(&reloaded, "id = ?", ci.ID).Error; err != nil {
		t.Fatalf("重新加载 CI 失败: %v", err)
	}
	if got := reloaded.Attributes["ip"]; got != "10.0.0.2" {
		t.Fatalf("ip 未更新，实际 %v", got)
	}
	// PATCH 默认视为人工维护：field_sources 标记 manual。
	if got := reloaded.FieldSources["ip"]; got != "manual" {
		t.Fatalf("field_sources[ip] 期望 manual，实际 %v", got)
	}
	// 未变更字段不动。
	if got := reloaded.Attributes["ident"]; got != "web-01" {
		t.Fatalf("ident 不应变化，实际 %v", got)
	}
	// 审计落库。
	var audits int64
	db.Model(&store.AuditLog{}).Where("ci_id = ? AND action = ?", ci.ID, "update").Count(&audits)
	if audits != 1 {
		t.Fatalf("期望 1 条 update 审计，实际 %d", audits)
	}
}

func TestPatchCIValidationFailureRejectsWholePatch(t *testing.T) {
	db, srv, model, token := setupPool(t)
	ci := mustCI(t, db, model.ID, map[string]any{"ident": "web-01", "ip": "10.0.0.1"})

	// ip 类型非法 → 整个 PATCH 拒绝（不是剔除该字段继续），其余字段也不应用。
	code, body := doJSON(t, http.MethodPatch, srv.URL+"/api/v1/cis/"+ci.ID,
		map[string]any{"attributes": map[string]any{"ip": "not-an-ip", "ident": "web-02"}}, token)
	if code != http.StatusBadRequest {
		t.Fatalf("非法属性期望 400，实际（%d）: %v", code, body)
	}

	var reloaded store.CI
	db.First(&reloaded, "id = ?", ci.ID)
	if got := reloaded.Attributes["ident"]; got != "web-01" {
		t.Fatalf("校验失败时整个 PATCH 应被拒绝，ident 被改为 %v", got)
	}
	if got := reloaded.Attributes["ip"]; got != "10.0.0.1" {
		t.Fatalf("校验失败时 ip 不应变化，实际 %v", got)
	}
}

func TestPatchCIUniqueConflict(t *testing.T) {
	db, srv, _, token := setupPool(t)
	// 追加一个带唯一属性的模型。
	uniqModel := store.Model{
		Name: "网络设备", Code: "network_device",
		Attributes: datatypes.NewJSONType([]store.AttributeDefinition{
			{Name: "名称", Code: "name", Type: "string", Required: true},
			{Name: "序列号", Code: "serial_no", Type: "string", Unique: true},
		}),
	}
	if err := db.Create(&uniqModel).Error; err != nil {
		t.Fatalf("创建模型失败: %v", err)
	}
	mustCI(t, db, uniqModel.ID, map[string]any{"name": "sw-01", "serial_no": "SN001"})
	ci2 := mustCI(t, db, uniqModel.ID, map[string]any{"name": "sw-02", "serial_no": "SN002"})

	code, body := doJSON(t, http.MethodPatch, srv.URL+"/api/v1/cis/"+ci2.ID,
		map[string]any{"attributes": map[string]any{"serial_no": "SN001"}}, token)
	if code != http.StatusBadRequest {
		t.Fatalf("唯一性冲突期望 400，实际（%d）: %v", code, body)
	}

	var reloaded store.CI
	db.First(&reloaded, "id = ?", ci2.ID)
	if got := reloaded.Attributes["serial_no"]; got != "SN002" {
		t.Fatalf("冲突时 serial_no 不应变化，实际 %v", got)
	}
}
