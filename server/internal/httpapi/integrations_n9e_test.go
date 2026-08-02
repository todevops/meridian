// n9e 集成 API 测试（F-070 上行回写 + F-063 嵌入代理）：
// 凭据解析（库内凭据优先、环境变量兜底）、tags/note 回写、未归组治理待办、代理透传。
package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"gorm.io/datatypes"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"meridian/server/internal/auth"
	"meridian/server/internal/credentials"
	"meridian/server/internal/store"
)

// n9eWriteStub 是 n9e API 测试桩：targets 列表 + tags/note 回写记录 + alert-cur-events。
type n9eWriteStub struct {
	srv *httptest.Server
	mu  sync.Mutex
	// tagsWrites/noteWrites 记录回写调用：ident → 值。
	tagsWrites map[string][]string
	noteWrites map[string]string
}

// newN9EStub 启动 n9e 测试桩（Authorization Bearer 非空校验；写接口为 per-target-id 形态）。
func newN9EStub(t *testing.T) *n9eWriteStub {
	t.Helper()
	stub := &n9eWriteStub{tagsWrites: map[string][]string{}, noteWrites: map[string]string{}}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/n9e/targets", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"dat":{"list":[
			{"id":101,"ident":"web-01","host_ip":"10.30.1.11","tags":"team=web biz_group=旧值"},
			{"id":158,"ident":"db-01","host_ip":"10.30.2.11","tags":"role=db"}
		],"total":2},"err":""}`)
	})
	mux.HandleFunc("PUT /api/n9e/targets/{id}/tags", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Tags string `json:"tags"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		stub.mu.Lock()
		stub.tagsWrites[r.PathValue("id")] = strings.Fields(body.Tags)
		stub.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"dat":null,"err":""}`)
	})
	mux.HandleFunc("PUT /api/n9e/targets/{id}/note", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Note string `json:"note"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		stub.mu.Lock()
		stub.noteWrites[r.PathValue("id")] = body.Note
		stub.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"dat":null,"err":""}`)
	})
	mux.HandleFunc("GET /api/n9e/alert-cur-events", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `[{"severity":2,"trigger":"cpu_usage","first_trigger_time":1780000000,"ident":%q}]`, r.URL.Query().Get("ident"))
	})
	stub.srv = httptest.NewServer(mux)
	t.Cleanup(stub.srv.Close)
	return stub
}

// setupN9EIntegration 构建含 host 模型与四台主机（覆盖 updated/skipped/todo 分支）的测试环境。
func setupN9EIntegration(t *testing.T) (*gorm.DB, *httptest.Server, string, *n9eWriteStub) {
	t.Helper()
	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared", t.Name())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("打开内存库失败: %v", err)
	}
	if err := db.AutoMigrate(store.AllModels()...); err != nil {
		t.Fatalf("迁移失败: %v", err)
	}
	model := store.Model{Name: "主机", Code: "host"}
	if err := db.Create(&model).Error; err != nil {
		t.Fatalf("创建模型失败: %v", err)
	}
	hosts := []store.CI{
		{ModelID: model.ID, Status: "active", Source: "n9e", Attributes: datatypes.JSONMap{
			"ident": "web-01", "ip": "10.30.1.11", "biz_group": "电商前台", "owner": "张三", "env": "prod"}},
		{ModelID: model.ID, Status: "active", Source: "n9e", Attributes: datatypes.JSONMap{
			"ident": "db-01", "ip": "10.30.2.11", "owner": "李四"}},
		{ModelID: model.ID, Status: "active", Source: "manual", Attributes: datatypes.JSONMap{
			"ip": "10.30.3.11"}}, // 无 ident → skipped
		{ModelID: model.ID, Status: "active", Source: "n9e", Attributes: datatypes.JSONMap{
			"ident": "ghost-01", "ip": "10.30.9.9", "biz_group": "电商前台"}}, // n9e 无此 target → skipped
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
	return db, srv, loginAs(t, srv, "admin", "admin-pass"), newN9EStub(t)
}

// seedN9ECredential 写入 type=n9e 凭据（secret 含 api_url/token），与路由测试密钥一致。
func seedN9ECredential(t *testing.T, db *gorm.DB, apiURL string) {
	t.Helper()
	cipher, err := credentials.NewCipher("httpapi-test-key")
	if err != nil {
		t.Fatalf("创建加解密器失败: %v", err)
	}
	ct, err := cipher.Encrypt([]byte(fmt.Sprintf(`{"api_url":%q,"token":"stub-token"}`, apiURL)))
	if err != nil {
		t.Fatalf("加密 secret 失败: %v", err)
	}
	if err := db.Create(&store.Credential{
		Name: "n9e 测试", Type: store.CredentialTypeN9E, SecretCiphertext: ct,
	}).Error; err != nil {
		t.Fatalf("写入 n9e 凭据失败: %v", err)
	}
}

func TestN9EWriteback(t *testing.T) {
	db, srv, token, stub := setupN9EIntegration(t)
	seedN9ECredential(t, db, stub.srv.URL)

	code, body := doJSON(t, http.MethodPost, srv.URL+"/api/v1/integrations/n9e/writeback", map[string]any{}, token)
	if code != http.StatusOK {
		t.Fatalf("回写期望 200，得到 %d: %v", code, body)
	}
	if body["updated"].(float64) != 2 || body["skipped"].(float64) != 2 || body["todos"].(float64) != 1 {
		t.Fatalf("计数不符: %v", body)
	}
	if errs := body["errors"].([]any); len(errs) != 0 {
		t.Fatalf("errors 应为空: %v", errs)
	}

	// web-01（target id=101）：biz_group 旧值被覆盖、team=web 保留、owner/env 追加。
	stub.mu.Lock()
	tags := stub.tagsWrites["101"]
	note := stub.noteWrites["101"]
	dbTags := stub.tagsWrites["158"]
	dbNote := stub.noteWrites["158"]
	stub.mu.Unlock()
	joined := strings.Join(tags, " ")
	for _, want := range []string{"team=web", "biz_group=电商前台", "owner=张三", "env=prod"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("web-01 tags 缺少 %q: %v", want, tags)
		}
	}
	if strings.Contains(joined, "旧值") {
		t.Fatalf("同键旧值应被覆盖: %v", tags)
	}
	if note != "负责人:张三 环境:prod" {
		t.Fatalf("web-01 note 不符: %q", note)
	}
	// db-01 无 biz_group/env：只回写 owner 标签与负责人备注。
	if strings.Join(dbTags, " ") != "role=db owner=李四" || dbNote != "负责人:李四" {
		t.Fatalf("db-01 回写不符: tags=%v note=%q", dbTags, dbNote)
	}

	// 未归组主机治理待办告警，且重复执行不重复产生。
	var alerts int64
	if err := db.Model(&store.AlertEvent{}).Where("title = ?", "治理待办：未归组主机").Count(&alerts).Error; err != nil {
		t.Fatalf("统计告警失败: %v", err)
	}
	if alerts != 1 {
		t.Fatalf("期望 1 条治理待办，实际 %d", alerts)
	}
	if code, _ := doJSON(t, http.MethodPost, srv.URL+"/api/v1/integrations/n9e/writeback", map[string]any{}, token); code != http.StatusOK {
		t.Fatalf("重复回写期望 200，得到 %d", code)
	}
	if err := db.Model(&store.AlertEvent{}).Where("title = ?", "治理待办：未归组主机").Count(&alerts).Error; err != nil {
		t.Fatalf("统计告警失败: %v", err)
	}
	if alerts != 1 {
		t.Fatalf("治理待办应按未确认去重，实际 %d", alerts)
	}
}

func TestN9EWritebackDryRun(t *testing.T) {
	db, srv, token, stub := setupN9EIntegration(t)
	seedN9ECredential(t, db, stub.srv.URL)

	code, body := doJSON(t, http.MethodPost, srv.URL+"/api/v1/integrations/n9e/writeback",
		map[string]any{"dry_run": true}, token)
	if code != http.StatusOK {
		t.Fatalf("dry_run 期望 200，得到 %d: %v", code, body)
	}
	if body["updated"].(float64) != 2 || body["todos"].(float64) != 1 {
		t.Fatalf("dry_run 计数不符: %v", body)
	}
	stub.mu.Lock()
	defer stub.mu.Unlock()
	if len(stub.tagsWrites) != 0 || len(stub.noteWrites) != 0 {
		t.Fatalf("dry_run 不应调用 n9e 写接口: tags=%v note=%v", stub.tagsWrites, stub.noteWrites)
	}
	var alerts int64
	if err := db.Model(&store.AlertEvent{}).Count(&alerts).Error; err != nil {
		t.Fatalf("统计告警失败: %v", err)
	}
	if alerts != 0 {
		t.Fatalf("dry_run 不应落告警，实际 %d", alerts)
	}
}

func TestN9EDashboardURLAndAlertsProxy(t *testing.T) {
	db, srv, token, stub := setupN9EIntegration(t)
	seedN9ECredential(t, db, stub.srv.URL)

	// dashboard-url：拼 n9e base + /dashboards/host?ident=。
	code, body := doJSON(t, http.MethodGet, srv.URL+"/api/v1/integrations/n9e/dashboard-url?ident=web-01", nil, token)
	if code != http.StatusOK {
		t.Fatalf("dashboard-url 期望 200，得到 %d: %v", code, body)
	}
	if body["url"] != stub.srv.URL+"/dashboards/host?ident=web-01" {
		t.Fatalf("dashboard-url 不符: %v", body["url"])
	}
	// 缺 ident 400。
	if code, _ := doJSON(t, http.MethodGet, srv.URL+"/api/v1/integrations/n9e/dashboard-url", nil, token); code != http.StatusBadRequest {
		t.Fatalf("缺 ident 期望 400，得到 %d", code)
	}

	// alerts 代理：n9e 原始数组原样透传（数组响应，doJSON 无法解析，用裸请求）。
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/v1/integrations/n9e/alerts?ident=web-01", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("alerts 代理请求失败: %v", err)
	}
	defer resp.Body.Close()
	var arr []map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&arr); err != nil {
		t.Fatalf("alerts 响应应为原始数组: %v", err)
	}
	if resp.StatusCode != http.StatusOK || len(arr) != 1 || arr[0]["trigger"] != "cpu_usage" || arr[0]["ident"] != "web-01" {
		t.Fatalf("alerts 透传不符: %d %v", resp.StatusCode, arr)
	}

	// 未认证 401（代理端点走会话鉴权）。
	if code, _ := doJSON(t, http.MethodGet, srv.URL+"/api/v1/integrations/n9e/dashboard-url?ident=web-01", nil, ""); code != http.StatusUnauthorized {
		t.Fatalf("未认证期望 401，得到 %d", code)
	}
}

func TestN9EEnvFallback(t *testing.T) {
	db, srv, token, stub := setupN9EIntegration(t)
	// 不写凭据，走 N9E_API_URL/N9E_API_TOKEN 环境变量兜底。
	t.Setenv("N9E_API_URL", stub.srv.URL)
	t.Setenv("N9E_API_TOKEN", "env-token")

	code, body := doJSON(t, http.MethodGet, srv.URL+"/api/v1/integrations/n9e/dashboard-url?ident=web-01", nil, token)
	if code != http.StatusOK || body["url"] != stub.srv.URL+"/dashboards/host?ident=web-01" {
		t.Fatalf("环境变量兜底 dashboard-url 不符: %d %v", code, body)
	}

	// 凭据与环境变量均无时 503。
	t.Setenv("N9E_API_URL", "")
	t.Setenv("N9E_API_TOKEN", "")
	if code, _ := doJSON(t, http.MethodGet, srv.URL+"/api/v1/integrations/n9e/dashboard-url?ident=web-01", nil, token); code != http.StatusServiceUnavailable {
		t.Fatalf("无 n9e 配置期望 503，得到 %d", code)
	}
	_ = db
}
