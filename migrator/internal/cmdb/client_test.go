// CMDB 客户端单测：httptest 夹具验证模型确保、CI 创建、IPAM 写入与错误类型。
package cmdb

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestEnsureModelCreate 验证模型不存在时（GET 404）走 POST 创建。
func TestEnsureModelCreate(t *testing.T) {
	var gotBody map[string]any
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/models/room":
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]any{"code": "NOT_FOUND", "message": "模型不存在"})
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/models":
			_ = json.NewDecoder(r.Body).Decode(&gotBody)
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "m-1", "code": "room"})
		default:
			t.Errorf("未预期请求: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	defer stub.Close()

	c := NewClient(stub.URL)
	def := RequiredModels()[0] // room
	created, err := c.EnsureModel(context.Background(), def)
	if err != nil {
		t.Fatalf("EnsureModel 失败: %v", err)
	}
	if !created {
		t.Fatal("期望 created=true（模型不存在时应新建）")
	}
	if gotBody["code"] != "room" {
		t.Fatalf("POST 请求体 code 异常: %v", gotBody["code"])
	}
	attrs, ok := gotBody["attributes"].([]any)
	if !ok || len(attrs) == 0 {
		t.Fatalf("POST 请求体缺少 attributes: %v", gotBody)
	}
}

// TestEnsureModelExisting 验证模型已存在时（GET 200）跳过创建。
func TestEnsureModelExisting(t *testing.T) {
	postCalled := false
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			postCalled = true
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "m-1", "code": "room"})
	}))
	defer stub.Close()

	c := NewClient(stub.URL)
	created, err := c.EnsureModel(context.Background(), RequiredModels()[0])
	if err != nil {
		t.Fatalf("EnsureModel 失败: %v", err)
	}
	if created {
		t.Fatal("期望 created=false（模型已存在）")
	}
	if postCalled {
		t.Fatal("模型已存在时不应调用 POST")
	}
}

// TestEnsureModelGetError 验证 GET 非 404 错误直接返回、不尝试创建。
func TestEnsureModelGetError(t *testing.T) {
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(map[string]any{"code": "INTERNAL", "message": "db down"})
	}))
	defer stub.Close()

	c := NewClient(stub.URL)
	if _, err := c.EnsureModel(context.Background(), RequiredModels()[0]); err == nil {
		t.Fatal("期望 GET 500 时返回错误")
	}
}

// TestCreateCIBody 验证 CI 创建请求体形状与响应解码。
func TestCreateCIBody(t *testing.T) {
	var gotBody map[string]any
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": "ci-1", "model_id": "m-room",
			"attributes": gotBody["attributes"], "status": "active", "source": MigrationSource,
		})
	}))
	defer stub.Close()

	c := NewClient(stub.URL)
	ci, err := c.CreateCI(context.Background(), "room", map[string]any{
		"name": "北京机房", "code": "bj", "netbox_id": "1",
	})
	if err != nil {
		t.Fatalf("CreateCI 失败: %v", err)
	}
	if ci.ID != "ci-1" {
		t.Fatalf("响应解码异常: %+v", ci)
	}
	if gotBody["model_id"] != "room" || gotBody["status"] != "active" || gotBody["source"] != MigrationSource {
		t.Fatalf("请求体形状异常: %v", gotBody)
	}
	attrs := gotBody["attributes"].(map[string]any)
	if attrs["netbox_id"] != "1" {
		t.Fatalf("netbox_id 留痕缺失: %v", attrs)
	}
}

// TestCreatePrefixConflict 验证 409 响应解析为可判定的 APIError。
func TestCreatePrefixConflict(t *testing.T) {
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusConflict)
		_ = json.NewEncoder(w).Encode(map[string]any{"code": "CONFLICT", "message": "同级前缀重叠"})
	}))
	defer stub.Close()

	c := NewClient(stub.URL)
	_, err := c.CreatePrefix(context.Background(), PrefixCreateRequest{CIDR: "10.0.0.0/8", Name: "x"})
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("期望 APIError，实际: %v", err)
	}
	if !IsStatus(err, http.StatusConflict) {
		t.Fatalf("期望 409，实际 %d", apiErr.StatusCode)
	}
	if apiErr.Message != "同级前缀重叠" {
		t.Fatalf("错误消息解析异常: %q", apiErr.Message)
	}
}

// TestCreateIPBadRequest 验证 400（IP 不在前缀内）错误路径。
func TestCreateIPBadRequest(t *testing.T) {
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]any{"code": "BAD_REQUEST", "message": "IP 不在前缀范围内"})
	}))
	defer stub.Close()

	c := NewClient(stub.URL)
	err := c.CreateIP(context.Background(), IPCreateRequest{PrefixID: "p-1", IP: "1.2.3.4"})
	if !IsStatus(err, http.StatusBadRequest) {
		t.Fatalf("期望 400，实际: %v", err)
	}
}

// TestLoginAndBearer 验证登录拿令牌后业务请求携带 Bearer 头。
func TestLoginAndBearer(t *testing.T) {
	var gotAuth string
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/auth/login":
			var body map[string]string
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body["username"] != "admin" || body["password"] != "admin123" {
				w.WriteHeader(http.StatusUnauthorized)
				_ = json.NewEncoder(w).Encode(map[string]any{"code": "UNAUTHORIZED", "message": "账号或密码错误"})
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"token": "jwt-xyz", "user": map[string]any{"username": "admin"}})
		case "/api/v1/models/room":
			gotAuth = r.Header.Get("Authorization")
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "m-1", "code": "room"})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer stub.Close()

	c := NewClient(stub.URL)
	if err := c.Login(context.Background(), "admin", "admin123"); err != nil {
		t.Fatalf("Login 失败: %v", err)
	}
	created, err := c.EnsureModel(context.Background(), RequiredModels()[0])
	if err != nil {
		t.Fatalf("EnsureModel 失败: %v", err)
	}
	if created {
		t.Fatal("模型已存在，期望 created=false")
	}
	if gotAuth != "Bearer jwt-xyz" {
		t.Fatalf("业务请求未携带 Bearer 头: %q", gotAuth)
	}
}

// TestLoginBadCredentials 验证登录失败返回错误。
func TestLoginBadCredentials(t *testing.T) {
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_ = json.NewEncoder(w).Encode(map[string]any{"code": "UNAUTHORIZED", "message": "账号或密码错误"})
	}))
	defer stub.Close()

	c := NewClient(stub.URL)
	if err := c.Login(context.Background(), "admin", "wrong"); err == nil {
		t.Fatal("期望登录失败返回错误")
	}
}

// TestRequiredModelsShape 验证内嵌模型定义完整性（5 个模型、均含 netbox_id 留痕属性）。
func TestRequiredModelsShape(t *testing.T) {
	defs := RequiredModels()
	want := []string{"room", "rack", "network_device", "vlan", "virtual_machine"}
	if len(defs) != len(want) {
		t.Fatalf("期望 %d 个模型，实际 %d", len(want), len(defs))
	}
	for i, code := range want {
		if defs[i].Code != code {
			t.Fatalf("模型顺序异常: 第 %d 个期望 %s，实际 %s", i, code, defs[i].Code)
		}
		found := false
		for _, a := range defs[i].Attributes {
			if a.Code == "netbox_id" && a.Unique {
				found = true
			}
		}
		if !found {
			t.Fatalf("模型 %s 缺少 unique 的 netbox_id 留痕属性", code)
		}
	}
}
