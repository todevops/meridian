// JumpServer 资产同步器单测（F-071）：创建/更新/禁用三分支 + 跳过与 dry_run 预演。
package jssync

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/datatypes"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"meridian/server/internal/jumpserver"
	"meridian/server/internal/store"
)

// fakeJS 是 JumpServer 的 httptest 假服务端：内存资产表 + 固定节点树，
// 记录全部写动作供断言。
type fakeJS struct {
	srv    *httptest.Server
	mu     sync.Mutex
	assets map[string]jumpserver.Asset // id → 资产
	nextID int
}

// newFakeJS 启动假服务端。assets 为预置资产（key 为 address）。
func newFakeJS(t *testing.T, preset []jumpserver.Asset) *fakeJS {
	t.Helper()
	f := &fakeJS{assets: map[string]jumpserver.Asset{}, nextID: 1}
	for _, a := range preset {
		a.ID = fmt.Sprintf("asset-%d", f.nextID)
		f.nextID++
		f.assets[a.ID] = a
	}
	nodes := []jumpserver.Node{
		{ID: "node-root", Key: "0", Name: "Default", Value: "Default", FullValue: "/Default"},
		{ID: "node-ec", Key: "0:1", Name: "电商平台", Value: "电商平台", FullValue: "/Default/电商平台"},
		{ID: "node-mall", Key: "0:1:1", Name: "商城前台", Value: "商城前台", FullValue: "/Default/电商平台/商城前台"},
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/assets/assets/", func(w http.ResponseWriter, _ *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		out := make([]jumpserver.Asset, 0, len(f.assets))
		for _, a := range f.assets {
			out = append(out, a)
		}
		_ = json.NewEncoder(w).Encode(out)
	})
	mux.HandleFunc("POST /api/v1/assets/assets/", func(w http.ResponseWriter, r *http.Request) {
		var in jumpserver.Asset
		_ = json.NewDecoder(r.Body).Decode(&in)
		f.mu.Lock()
		defer f.mu.Unlock()
		in.ID = fmt.Sprintf("asset-%d", f.nextID)
		f.nextID++
		in.IsActive = true
		f.assets[in.ID] = in
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(in)
	})
	mux.HandleFunc("PATCH /api/v1/assets/assets/{id}/", func(w http.ResponseWriter, r *http.Request) {
		var patch map[string]any
		_ = json.NewDecoder(r.Body).Decode(&patch)
		f.mu.Lock()
		defer f.mu.Unlock()
		a, ok := f.assets[r.PathValue("id")]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if v, ok := patch["name"].(string); ok {
			a.Name = v
		}
		if v, ok := patch["platform"].(string); ok {
			a.Platform = v
		}
		if v, ok := patch["nodes"].([]any); ok {
			a.Nodes = a.Nodes[:0]
			for _, n := range v {
				a.Nodes = append(a.Nodes, fmt.Sprint(n))
			}
		}
		if v, ok := patch["is_active"].(bool); ok {
			a.IsActive = v
		}
		f.assets[a.ID] = a
		_ = json.NewEncoder(w).Encode(a)
	})
	mux.HandleFunc("GET /api/v1/assets/nodes/", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(nodes)
	})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

// byAddress 按 address 查资产（测试断言用）。
func (f *fakeJS) byAddress(addr string) (jumpserver.Asset, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, a := range f.assets {
		if a.Address == addr {
			return a, true
		}
	}
	return jumpserver.Asset{}, false
}

// setupDB 预置 CMDB 侧数据：
//   - web-01（在用、归属商城前台、JS 无资产 → 创建分支）
//   - web-02（在用、归属商城前台、JS 资产漂移 → 更新分支）
//   - web-03（在用、归属商城前台、JS 资产一致 → 跳过）
//   - web-04（已退役、JS 资产启用 → 禁用分支）
//   - web-05（在用、无归属 → 不同步；其 JS 资产因失归属 → 禁用分支）
//   - JS 外部资产 192.168.9.9（非本环境主机 → 不动）
func setupDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared", t.Name())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("打开内存库失败: %v", err)
	}
	if err := db.AutoMigrate(store.AllModels()...); err != nil {
		t.Fatalf("迁移失败: %v", err)
	}
	for _, m := range []store.Model{
		{Name: "业务线", Code: "biz_line"},
		{Name: "应用系统", Code: "biz_app"},
		{Name: "主机", Code: "host"},
	} {
		if err := db.Create(&m).Error; err != nil {
			t.Fatalf("创建模型失败: %v", err)
		}
	}
	mustCI := func(modelCode string, status string, attrs map[string]any) store.CI {
		t.Helper()
		var model store.Model
		if err := db.First(&model, "code = ?", modelCode).Error; err != nil {
			t.Fatalf("模型 %s 不存在: %v", modelCode, err)
		}
		ci := store.CI{ModelID: model.ID, Attributes: datatypes.JSONMap(attrs), Status: status, Source: "manual"}
		if err := db.Create(&ci).Error; err != nil {
			t.Fatalf("创建 CI 失败: %v", err)
		}
		return ci
	}
	line := mustCI("biz_line", "active", map[string]any{"code": "ec", "name": "电商平台"})
	app := mustCI("biz_app", "active", map[string]any{"code": "mall-front", "name": "商城前台"})
	if err := db.Create(&store.CIRelation{RelationCode: "belongs_to", SrcCIID: app.ID, DstCIID: line.ID, Source: "manual"}).Error; err != nil {
		t.Fatalf("建应用归属失败: %v", err)
	}
	deploy := func(ident, ip, status string) {
		h := mustCI("host", status, map[string]any{"ident": ident, "ip": ip})
		if err := db.Create(&store.CIRelation{RelationCode: "deployed_on", SrcCIID: app.ID, DstCIID: h.ID, Source: "auto"}).Error; err != nil {
			t.Fatalf("建部署关系失败: %v", err)
		}
	}
	deploy("web-01", "10.0.1.1", "active")
	deploy("web-02", "10.0.1.2", "active")
	deploy("web-03", "10.0.1.3", "active")
	deploy("web-04", "10.0.1.4", "retired")
	// web-05：在用但无归属。
	mustCI("host", "active", map[string]any{"ident": "web-05", "ip": "10.0.1.5"})
	return db
}

// presetAssets 预置 JumpServer 侧资产。
func presetAssets() []jumpserver.Asset {
	return []jumpserver.Asset{
		// web-02：名称漂移且节点缺失 → 更新。
		{Name: "old-name", Address: "10.0.1.2", Platform: "linux", Nodes: []string{}, IsActive: true},
		// web-03：完全一致 → 跳过。
		{Name: "web-03", Address: "10.0.1.3", Platform: "linux", Nodes: []string{"node-mall"}, IsActive: true},
		// web-04：主机已退役 → 禁用。
		{Name: "web-04", Address: "10.0.1.4", Platform: "linux", Nodes: []string{"node-mall"}, IsActive: true},
		// web-05：主机失归属 → 禁用。
		{Name: "web-05", Address: "10.0.1.5", Platform: "linux", Nodes: []string{"node-mall"}, IsActive: true},
		// 外部资产：非本环境主机 → 不动。
		{Name: "external", Address: "192.168.9.9", Platform: "linux", IsActive: true},
	}
}

// TestSyncBranches 验证创建/更新/禁用/跳过四分支与外部资产保护。
func TestSyncBranches(t *testing.T) {
	db := setupDB(t)
	js := newFakeJS(t, presetAssets())
	res, err := New(db, jumpserver.NewClient(js.srv.URL, "dev-js-token")).Sync(context.Background(), false)
	if err != nil {
		t.Fatalf("Sync 失败: %v", err)
	}
	if len(res.Errors) != 0 {
		t.Fatalf("同步出现错误: %v", res.Errors)
	}
	if res.Created != 1 || res.Updated != 1 || res.Disabled != 2 || res.Skipped != 1 {
		t.Fatalf("计数不符: %+v（期望 created=1 updated=1 disabled=2 skipped=1）", res)
	}
	// 创建分支：web-01 按节点路径 /Default/电商平台/商城前台 建档。
	a1, ok := js.byAddress("10.0.1.1")
	if !ok || a1.Name != "web-01" || a1.Platform != "linux" || !a1.IsActive {
		t.Fatalf("web-01 创建结果不符: %+v", a1)
	}
	if len(a1.Nodes) != 1 || a1.Nodes[0] != "node-mall" {
		t.Errorf("web-01 节点归属 = %v，期望 [node-mall]", a1.Nodes)
	}
	// 更新分支：web-02 名称与节点被纠正。
	a2, _ := js.byAddress("10.0.1.2")
	if a2.Name != "web-02" || len(a2.Nodes) != 1 || a2.Nodes[0] != "node-mall" {
		t.Errorf("web-02 更新结果不符: %+v", a2)
	}
	// 禁用分支：web-04/web-05 置禁用。
	for _, ip := range []string{"10.0.1.4", "10.0.1.5"} {
		a, _ := js.byAddress(ip)
		if a.IsActive {
			t.Errorf("资产 %s 应已禁用", ip)
		}
	}
	// 跳过分支与外部资产：保持启用不变。
	a3, _ := js.byAddress("10.0.1.3")
	if !a3.IsActive || a3.Name != "web-03" {
		t.Errorf("web-03 不应被改动: %+v", a3)
	}
	ext, _ := js.byAddress("192.168.9.9")
	if !ext.IsActive {
		t.Error("外部资产不应被禁用")
	}
}

// TestSyncDryRun 验证 dry_run：计数照算但不写入。
func TestSyncDryRun(t *testing.T) {
	db := setupDB(t)
	js := newFakeJS(t, presetAssets())
	res, err := New(db, jumpserver.NewClient(js.srv.URL, "dev-js-token")).Sync(context.Background(), true)
	if err != nil {
		t.Fatalf("Sync dry_run 失败: %v", err)
	}
	if res.Created != 1 || res.Updated != 1 || res.Disabled != 2 || res.Skipped != 1 {
		t.Fatalf("dry_run 计数不符: %+v", res)
	}
	if _, ok := js.byAddress("10.0.1.1"); ok {
		t.Error("dry_run 不应创建资产")
	}
	a4, _ := js.byAddress("10.0.1.4")
	if !a4.IsActive {
		t.Error("dry_run 不应禁用资产")
	}
}

// TestSyncIdempotent 验证二次同步全跳过（幂等）。
func TestSyncIdempotent(t *testing.T) {
	db := setupDB(t)
	js := newFakeJS(t, presetAssets())
	client := jumpserver.NewClient(js.srv.URL, "dev-js-token")
	if _, err := New(db, client).Sync(context.Background(), false); err != nil {
		t.Fatalf("首轮 Sync 失败: %v", err)
	}
	res, err := New(db, client).Sync(context.Background(), false)
	if err != nil {
		t.Fatalf("次轮 Sync 失败: %v", err)
	}
	if res.Created != 0 || res.Updated != 0 || res.Disabled != 0 {
		t.Errorf("次轮同步应全跳过: %+v", res)
	}
	if res.Skipped != 3 { // web-01/02/03 均在用已归属且一致
		t.Errorf("次轮 skipped = %d，期望 3", res.Skipped)
	}
}

// TestSyncNoClient 验证未配置 JumpServer 时的错误。
func TestSyncNoClient(t *testing.T) {
	db := setupDB(t)
	if _, err := New(db, nil).Sync(context.Background(), false); err == nil ||
		!strings.Contains(err.Error(), "未配置") {
		t.Errorf("err = %v，期望未配置错误", err)
	}
}
