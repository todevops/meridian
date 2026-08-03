package mocksys

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"
)

// jsProtocol 是 JumpServer 资产的协议条目（如 ssh/22）。
type jsProtocol struct {
	Name string `json:"name"`
	Port int    `json:"port"`
}

// jsAsset 是 JumpServer 风格的资产条目（/api/v1/assets/assets/ 响应元素）。
type jsAsset struct {
	ID          string       `json:"id"`
	Name        string       `json:"name"`
	Address     string       `json:"address"`
	Platform    string       `json:"platform"`
	Protocols   []jsProtocol `json:"protocols,omitempty"`
	Nodes       []string     `json:"nodes"` // 节点 id 列表
	IsActive    bool         `json:"is_active"`
	Comment     string       `json:"comment,omitempty"`
	DateCreated string       `json:"date_created"`
	DateUpdated string       `json:"date_updated"`
}

// jsAssetPatch 是 PATCH 请求体：指针字段区分"未传"与"置空"。
type jsAssetPatch struct {
	Name     *string   `json:"name"`
	Address  *string   `json:"address"`
	Platform *string   `json:"platform"`
	Nodes    *[]string `json:"nodes"`
	IsActive *bool     `json:"is_active"`
	Comment  *string   `json:"comment"`
}

// jsNode 是 JumpServer 风格的节点条目（/api/v1/assets/nodes/ 响应元素）。
type jsNode struct {
	ID        string `json:"id"`
	Key       string `json:"key"`
	Name      string `json:"name"`
	Value     string `json:"value"`
	FullValue string `json:"full_value"`
}

// jsState 是 JumpServer mock 的内存态：资产清单可被创建/更新/禁用，写后 GET 可读回。
type jsState struct {
	mu     sync.RWMutex
	assets []jsAsset
}

// jsNodes 是节点树 fixture：/Default/电商平台/商城前台（扁平列表，full_value 体现层级）。
var jsNodes = []jsNode{
	{ID: "11111111-1111-1111-1111-111111111111", Key: "0", Name: "Default", Value: "Default", FullValue: "/Default"},
	{ID: "22222222-2222-2222-2222-222222222222", Key: "0:1", Name: "电商平台", Value: "电商平台", FullValue: "/Default/电商平台"},
	{ID: "33333333-3333-3333-3333-333333333333", Key: "0:1:1", Name: "商城前台", Value: "商城前台", FullValue: "/Default/电商平台/商城前台"},
}

// newJumpServer 构建 JumpServer mock（:19010）：
// Authorization: Token 非空校验（空则 401）；资产 CRUD 走内存态，
// 存量数据来自 fixtures/jumpserver-assets.json（2 台），写后 GET 可读回。
func newJumpServer() (http.Handler, error) {
	raw, err := readFixture("jumpserver-assets.json")
	if err != nil {
		return nil, fmt.Errorf("读取 jumpserver-assets.json 失败: %w", err)
	}
	var assets []jsAsset
	if err := json.Unmarshal(raw, &assets); err != nil {
		return nil, fmt.Errorf("解析 jumpserver-assets.json 失败: %w", err)
	}
	st := &jsState{assets: assets}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/assets/assets/", func(w http.ResponseWriter, r *http.Request) {
		st.mu.RLock()
		defer st.mu.RUnlock()
		writeJSON(w, http.StatusOK, st.assets)
	})
	mux.HandleFunc("POST /api/v1/assets/assets/", func(w http.ResponseWriter, r *http.Request) {
		var in jsAsset
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": fmt.Sprintf("请求体不是合法 JSON: %v", err)})
			return
		}
		if strings.TrimSpace(in.Name) == "" || strings.TrimSpace(in.Address) == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "name 与 address 不能为空"})
			return
		}
		now := time.Now().UTC().Format(time.RFC3339)
		in.ID = jsUUID()
		in.IsActive = true // 新建资产默认启用，禁用须走 disable 端点
		in.DateCreated = now
		in.DateUpdated = now
		if in.Nodes == nil {
			in.Nodes = []string{}
		}
		st.mu.Lock()
		st.assets = append(st.assets, in)
		st.mu.Unlock()
		writeJSON(w, http.StatusCreated, in)
	})
	mux.HandleFunc("GET /api/v1/assets/assets/{id}/", func(w http.ResponseWriter, r *http.Request) {
		st.mu.RLock()
		defer st.mu.RUnlock()
		if a := st.find(r.PathValue("id")); a != nil {
			writeJSON(w, http.StatusOK, a)
			return
		}
		writeJSON(w, http.StatusNotFound, map[string]string{"detail": "Not found."})
	})
	mux.HandleFunc("PATCH /api/v1/assets/assets/{id}/", func(w http.ResponseWriter, r *http.Request) {
		var patch jsAssetPatch
		if err := json.NewDecoder(r.Body).Decode(&patch); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": fmt.Sprintf("请求体不是合法 JSON: %v", err)})
			return
		}
		st.mu.Lock()
		defer st.mu.Unlock()
		a := st.find(r.PathValue("id"))
		if a == nil {
			writeJSON(w, http.StatusNotFound, map[string]string{"detail": "Not found."})
			return
		}
		if patch.Name != nil {
			a.Name = *patch.Name
		}
		if patch.Address != nil {
			a.Address = *patch.Address
		}
		if patch.Platform != nil {
			a.Platform = *patch.Platform
		}
		if patch.Nodes != nil {
			a.Nodes = *patch.Nodes
		}
		if patch.IsActive != nil {
			a.IsActive = *patch.IsActive
		}
		if patch.Comment != nil {
			a.Comment = *patch.Comment
		}
		a.DateUpdated = time.Now().UTC().Format(time.RFC3339)
		writeJSON(w, http.StatusOK, a)
	})
	mux.HandleFunc("POST /api/v1/assets/assets/{id}/disable/", func(w http.ResponseWriter, r *http.Request) {
		st.mu.Lock()
		defer st.mu.Unlock()
		a := st.find(r.PathValue("id"))
		if a == nil {
			writeJSON(w, http.StatusNotFound, map[string]string{"detail": "Not found."})
			return
		}
		a.IsActive = false
		a.DateUpdated = time.Now().UTC().Format(time.RFC3339)
		writeJSON(w, http.StatusOK, a)
	})
	mux.HandleFunc("GET /api/v1/assets/nodes/", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, jsNodes)
	})
	return jumpserverAuth(mux), nil
}

// find 按 id 定位资产（返回指向切片元素的可写指针）；调用方须持锁。
func (st *jsState) find(id string) *jsAsset {
	for i := range st.assets {
		if st.assets[i].ID == id {
			return &st.assets[i]
		}
	}
	return nil
}

// jumpserverAuth 对整个路由做 Token 鉴权（官方缺失凭据返回 401）。
func jumpserverAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if apiToken(r) == "" {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"detail": "Authentication credentials were not provided."})
			return
		}
		next.ServeHTTP(w, r)
	})
}

// jsUUID 生成 UUID 形态的随机资产 id。
func jsUUID() string {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return fmt.Sprintf("fallback-%d", time.Now().UnixNano())
	}
	s := hex.EncodeToString(buf)
	return fmt.Sprintf("%s-%s-%s-%s-%s", s[0:8], s[8:12], s[12:16], s[16:20], s[20:32])
}
