// K8s Pod 代理（/api/v1/k8s/pods）链路测试：字段精简映射、namespace/cluster 过滤、上游失败。
package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// fakeKubeAPI 启动一个假 apiserver：校验 Bearer token，返回两个 Pod。
func fakeKubeAPI(t *testing.T, expectToken string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+expectToken {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if r.URL.Path != "/api/v1/pods" && r.URL.Path != "/api/v1/namespaces/mall/pods" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		created := time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)
		fmt.Fprintf(w, `{"items":[
			{"metadata":{"name":"web-7d9c","namespace":"mall","creationTimestamp":"%s"},
			 "spec":{"nodeName":"k8s-worker-01"},
			 "status":{"phase":"Running","containerStatuses":[{"restartCount":2},{"restartCount":1}]}},
			{"metadata":{"name":"db-0","namespace":"mall","creationTimestamp":"%s"},
			 "spec":{"nodeName":"k8s-worker-02"},
			 "status":{"phase":"Pending","containerStatuses":[]}}
		]}`, created, created)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// doJSONList 发起 JSON GET 并解析数组响应；token 非空时以 Bearer 携带。
func doJSONList(t *testing.T, method, url string, token string) (int, []map[string]any) {
	t.Helper()
	req, err := http.NewRequest(method, url, nil)
	if err != nil {
		t.Fatalf("构造请求失败: %v", err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("请求失败: %v", err)
	}
	defer resp.Body.Close()
	var out []map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil && resp.StatusCode != http.StatusOK {
		return resp.StatusCode, nil
	} else if err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}
	return resp.StatusCode, out
}

// TestK8SPodsProxy 验证 Pod 代理的精简字段映射与查询参数透传。
func TestK8SPodsProxy(t *testing.T) {
	kube := fakeKubeAPI(t, "tok-3b")
	t.Setenv("K8S_API_URL", kube.URL)
	t.Setenv("K8S_TOKEN", "tok-3b")
	t.Setenv("K8S_CLUSTER_NAME", "volc-prod-k8s")
	srv, token := setupAuthAPI(t)

	code, body := doJSONList(t, http.MethodGet, srv.URL+"/api/v1/k8s/pods", token)
	if code != http.StatusOK {
		t.Fatalf("pods 代理失败（%d）: %v", code, body)
	}
	if len(body) != 2 {
		t.Fatalf("应返回 2 个 Pod，实际 %d", len(body))
	}
	web := body[0]
	if web["name"] != "web-7d9c" || web["namespace"] != "mall" || web["phase"] != "Running" || web["node"] != "k8s-worker-01" {
		t.Errorf("Pod 字段映射不符: %+v", web)
	}
	if web["restart_count"].(float64) != 3 {
		t.Errorf("restart_count 应为容器之和 3，实际 %v", web["restart_count"])
	}
	if age := web["age_seconds"].(float64); age < 3500 || age > 3700 {
		t.Errorf("age_seconds 应约 3600，实际 %v", age)
	}

	// namespace 过滤：走 /api/v1/namespaces/{ns}/pods 路径。
	code, body = doJSONList(t, http.MethodGet, srv.URL+"/api/v1/k8s/pods?namespace=mall", token)
	if code != http.StatusOK || len(body) != 2 {
		t.Errorf("namespace 过滤失败（%d）: %v", code, body)
	}
	// cluster 不符：返回空列表。
	code, body = doJSONList(t, http.MethodGet, srv.URL+"/api/v1/k8s/pods?cluster=other-cluster", token)
	if code != http.StatusOK || len(body) != 0 {
		t.Errorf("cluster 不符应返回空列表（%d）: %v", code, body)
	}
	// 未认证。
	if code, _ := doJSONList(t, http.MethodGet, srv.URL+"/api/v1/k8s/pods", ""); code != http.StatusUnauthorized {
		t.Errorf("未认证应 401，实际 %d", code)
	}
}

// TestK8SPodsUpstreamDown 验证 apiserver 不可达时返回 502。
func TestK8SPodsUpstreamDown(t *testing.T) {
	t.Setenv("K8S_API_URL", "http://127.0.0.1:1") // 不可达
	t.Setenv("K8S_TOKEN", "tok")
	srv, token := setupAuthAPI(t)
	code, body := doJSON(t, http.MethodGet, srv.URL+"/api/v1/k8s/pods", nil, token)
	if code != http.StatusBadGateway {
		t.Fatalf("上游不可达应 502，实际 %d", code)
	}
	if !strings.Contains(fmt.Sprint(body["message"]), "apiserver") {
		t.Errorf("错误信息应指明 apiserver: %v", body)
	}
}
