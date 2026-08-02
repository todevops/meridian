// 发现池 API 单测：列表形状 + confirm/ignore 状态机（含重复裁决 409）。
package httpapi

import (
	"bytes"
	"encoding/json"
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
	"meridian/server/internal/discovery"
	"meridian/server/internal/store"
)

// setupPool 打开独立内存库，预置主机模型，构建完整路由并返回 admin 会话令牌。
func setupPool(t *testing.T) (*gorm.DB, *httptest.Server, store.Model, string) {
	t.Helper()
	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared", t.Name())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("打开内存库失败: %v", err)
	}
	if err := db.AutoMigrate(store.AllModels()...); err != nil {
		t.Fatalf("迁移失败: %v", err)
	}
	model := store.Model{
		Name: "主机", Code: "host",
		Attributes: datatypes.NewJSONType([]store.AttributeDefinition{
			{Name: "主机标识", Code: "ident", Type: "string", Required: true},
			{Name: "内网IP", Code: "ip", Type: "ip"},
		}),
	}
	if err := db.Create(&model).Error; err != nil {
		t.Fatalf("创建模型失败: %v", err)
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

	// 登录 admin 取会话令牌（响应体 token 字段，Bearer 方式携带）。
	code, body := doJSON(t, http.MethodPost, srv.URL+"/api/v1/auth/login",
		map[string]any{"username": "admin", "password": "admin-pass"}, "")
	if code != http.StatusOK {
		t.Fatalf("admin 登录失败（%d）: %v", code, body)
	}
	token, _ := body["token"].(string)
	if token == "" {
		t.Fatalf("登录响应缺少 token: %v", body)
	}
	return db, srv, model, token
}

// mustPoolItem 写入一条 pending 池条目（模拟调和 conflict/pool 判定的产物）。
func mustPoolItem(t *testing.T, db *gorm.DB, attrs map[string]any) store.PoolItem {
	t.Helper()
	item := store.PoolItem{
		ModelCode: "host",
		Record: datatypes.JSONMap{
			"source":          "nmap",
			"collector":       "nmap-scanner",
			"model_candidate": "host",
			"attributes":      attrs,
			"occurred_at":     time.Now().Format(time.RFC3339),
		},
		ReconcileAction: "pool",
		Reason:          "未命中调和键 [ident]，但属性校验未通过，转入发现池: 测试构造",
		Status:          "pending",
	}
	if err := db.Create(&item).Error; err != nil {
		t.Fatalf("写入池条目失败: %v", err)
	}
	return item
}

// doJSON 发起 JSON 请求并解析响应；token 非空时以 Bearer 携带。
func doJSON(t *testing.T, method, url string, body any, token string) (int, map[string]any) {
	t.Helper()
	var reader *bytes.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("序列化请求体失败: %v", err)
		}
		reader = bytes.NewReader(raw)
	} else {
		reader = bytes.NewReader(nil)
	}
	req, err := http.NewRequest(method, url, reader)
	if err != nil {
		t.Fatalf("构造请求失败: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("请求失败: %v", err)
	}
	defer resp.Body.Close()
	var out map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}
	return resp.StatusCode, out
}

func TestListDiscoveryPool(t *testing.T) {
	db, srv, _, token := setupPool(t)
	item := mustPoolItem(t, db, map[string]any{"ident": "host-001", "ip": "10.0.0.1"})

	code, body := doJSON(t, http.MethodGet, srv.URL+"/api/v1/discovery-pool?status=pending", nil, token)
	if code != http.StatusOK {
		t.Fatalf("期望 200，得到 %d: %v", code, body)
	}
	if body["total"].(float64) != 1 {
		t.Fatalf("期望 total=1，得到 %v", body["total"])
	}
	first := body["items"].([]any)[0].(map[string]any)
	if first["id"] != item.ID || first["source"] != "nmap" || first["collector"] != "nmap-scanner" ||
		first["model_candidate"] != "host" || first["reconcile_action"] != "pool" || first["status"] != "pending" {
		t.Fatalf("池条目形状不符: %v", first)
	}
	attrs := first["attributes"].(map[string]any)
	if attrs["ident"] != "host-001" {
		t.Fatalf("attributes 未关联原始记录: %v", attrs)
	}
	reasons := first["reasons"].([]any)
	if len(reasons) != 1 {
		t.Fatalf("reasons 应为数组，得到 %v", first["reasons"])
	}

	// 非法 status → 400；未认证 → 401。
	if code, _ := doJSON(t, http.MethodGet, srv.URL+"/api/v1/discovery-pool?status=done", nil, token); code != http.StatusBadRequest {
		t.Fatalf("非法 status 期望 400，得到 %d", code)
	}
	if code, _ := doJSON(t, http.MethodGet, srv.URL+"/api/v1/discovery-pool", nil, ""); code != http.StatusUnauthorized {
		t.Fatalf("未认证期望 401，得到 %d", code)
	}
}

func TestConfirmPoolItemStateMachine(t *testing.T) {
	db, srv, model, token := setupPool(t)
	item := mustPoolItem(t, db, map[string]any{"ident": "host-002", "ip": "10.0.0.2"})

	// 确认建档：201 返回 status=active 的 CI，池条目置 confirmed。
	code, ci := doJSON(t, http.MethodPost, srv.URL+"/api/v1/discovery-pool/"+item.ID+"/confirm", map[string]any{}, token)
	if code != http.StatusCreated {
		t.Fatalf("期望 201，得到 %d: %v", code, ci)
	}
	if ci["status"] != "active" || ci["model_id"] != model.ID {
		t.Fatalf("建档 CI 不符: %v", ci)
	}
	var updated store.PoolItem
	if err := db.First(&updated, "id = ?", item.ID).Error; err != nil {
		t.Fatalf("重新加载池条目失败: %v", err)
	}
	if updated.Status != "confirmed" {
		t.Fatalf("池条目应置 confirmed，得到 %s", updated.Status)
	}

	// 重复确认 → 409（状态机不可逆）。
	if code, _ := doJSON(t, http.MethodPost, srv.URL+"/api/v1/discovery-pool/"+item.ID+"/confirm", map[string]any{}, token); code != http.StatusConflict {
		t.Fatalf("重复确认期望 409，得到 %d", code)
	}
	// 已确认条目再忽略 → 409。
	if code, _ := doJSON(t, http.MethodPost, srv.URL+"/api/v1/discovery-pool/"+item.ID+"/ignore", nil, token); code != http.StatusConflict {
		t.Fatalf("确认后忽略期望 409，得到 %d", code)
	}
	// 不存在的条目 → 404。
	if code, _ := doJSON(t, http.MethodPost, srv.URL+"/api/v1/discovery-pool/nope/confirm", map[string]any{}, token); code != http.StatusNotFound {
		t.Fatalf("期望 404，得到 %d", code)
	}
}

func TestConfirmPoolItemWithOverride(t *testing.T) {
	db, srv, _, token := setupPool(t)
	// 记录缺必填 ident：直接确认应 400（属性校验未通过）；body 补上后成功。
	item := mustPoolItem(t, db, map[string]any{"ip": "10.0.0.3"})
	if code, body := doJSON(t, http.MethodPost, srv.URL+"/api/v1/discovery-pool/"+item.ID+"/confirm", map[string]any{}, token); code != http.StatusBadRequest {
		t.Fatalf("缺必填属性期望 400，得到 %d: %v", code, body)
	}
	code, ci := doJSON(t, http.MethodPost, srv.URL+"/api/v1/discovery-pool/"+item.ID+"/confirm",
		map[string]any{"attributes": map[string]any{"ident": "host-003"}}, token)
	if code != http.StatusCreated {
		t.Fatalf("属性覆盖后期望 201，得到 %d: %v", code, ci)
	}
	attrs := ci["attributes"].(map[string]any)
	if attrs["ident"] != "host-003" || attrs["ip"] != "10.0.0.3" {
		t.Fatalf("属性合并结果不符: %v", attrs)
	}
}

func TestIgnorePoolItemStateMachine(t *testing.T) {
	db, srv, _, token := setupPool(t)
	item := mustPoolItem(t, db, map[string]any{"ident": "host-004"})

	// 忽略：200 且状态翻转；忽略后再确认 → 409；重复忽略 → 409。
	code, body := doJSON(t, http.MethodPost, srv.URL+"/api/v1/discovery-pool/"+item.ID+"/ignore", nil, token)
	if code != http.StatusOK {
		t.Fatalf("期望 200，得到 %d: %v", code, body)
	}
	if body["status"] != "ignored" {
		t.Fatalf("期望 ignored，得到 %v", body["status"])
	}
	if code, _ := doJSON(t, http.MethodPost, srv.URL+"/api/v1/discovery-pool/"+item.ID+"/confirm", map[string]any{}, token); code != http.StatusConflict {
		t.Fatalf("忽略后确认期望 409，得到 %d", code)
	}
	if code, _ := doJSON(t, http.MethodPost, srv.URL+"/api/v1/discovery-pool/"+item.ID+"/ignore", nil, token); code != http.StatusConflict {
		t.Fatalf("重复忽略期望 409，得到 %d", code)
	}
}
