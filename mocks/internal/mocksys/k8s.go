// k8s.go 实现 fake K8s apiserver（:19009）：
// 按官方 client-go discovery/List 契约输出 metav1.List 壳，
// 供 F-024 client-go 采集器（K2）联调；数据来自 fixtures/k8s-cluster.json。
package mocksys

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
)

// k8sFixture 是 fixtures/k8s-cluster.json 的解析结构：
// 资源对象保留为 map 原样输出，过滤时只读 metadata。
type k8sFixture struct {
	Cluster          string            `json:"cluster"`
	Version          map[string]any    `json:"version"`
	ResourceVersions map[string]string `json:"resource_versions"`
	Namespaces       []map[string]any  `json:"namespaces"`
	Nodes            []map[string]any  `json:"nodes"`
	Pods             []map[string]any  `json:"pods"`
	Services         []map[string]any  `json:"services"`
	PersistentVols   []map[string]any  `json:"persistentvolumes"`
	Deployments      []map[string]any  `json:"deployments"`
	StatefulSets     []map[string]any  `json:"statefulsets"`
	DaemonSets       []map[string]any  `json:"daemonsets"`
	Ingresses        []map[string]any  `json:"ingresses"`
}

// k8sResource 描述一种可 list 的资源（复用于集群级与命名空间级路由）。
type k8sResource struct {
	kind       string // List 壳的 kind（如 PodList）
	apiVersion string // List 壳的 apiVersion（v1/apps/v1/networking.k8s.io/v1）
	rvKey      string // resource_versions 中的键
	items      func(*k8sFixture) []map[string]any
}

// newK8s 构建 fake K8s apiserver mock（:19009）：
// Authorization Bearer 非空否则 401；discovery（/api、/apis、各 APIResourceList、/version）
// 与全部 list 端点均按官方 metav1 结构输出，list 支持
// ?namespace=、?labelSelector= 与 ?resourceVersion= 增量语义。
func newK8s() (http.Handler, error) {
	raw, err := readFixture("k8s-cluster.json")
	if err != nil {
		return nil, fmt.Errorf("读取 k8s-cluster.json 失败: %w", err)
	}
	var fx k8sFixture
	if err := json.Unmarshal(raw, &fx); err != nil {
		return nil, fmt.Errorf("解析 k8s-cluster.json 失败: %w", err)
	}
	if fx.Cluster == "" || len(fx.Nodes) == 0 {
		return nil, fmt.Errorf("k8s-cluster.json 缺少 cluster 或 nodes")
	}

	resources := map[string]k8sResource{
		"namespaces":        {"NamespaceList", "v1", "namespaces", func(f *k8sFixture) []map[string]any { return f.Namespaces }},
		"nodes":             {"NodeList", "v1", "nodes", func(f *k8sFixture) []map[string]any { return f.Nodes }},
		"pods":              {"PodList", "v1", "pods", func(f *k8sFixture) []map[string]any { return f.Pods }},
		"services":          {"ServiceList", "v1", "services", func(f *k8sFixture) []map[string]any { return f.Services }},
		"persistentvolumes": {"PersistentVolumeList", "v1", "persistentvolumes", func(f *k8sFixture) []map[string]any { return f.PersistentVols }},
		"deployments":       {"DeploymentList", "apps/v1", "deployments", func(f *k8sFixture) []map[string]any { return f.Deployments }},
		"statefulsets":      {"StatefulSetList", "apps/v1", "statefulsets", func(f *k8sFixture) []map[string]any { return f.StatefulSets }},
		"daemonsets":        {"DaemonSetList", "apps/v1", "daemonsets", func(f *k8sFixture) []map[string]any { return f.DaemonSets }},
		"ingresses":         {"IngressList", "networking.k8s.io/v1", "ingresses", func(f *k8sFixture) []map[string]any { return f.Ingresses }},
	}
	serve := func(res k8sResource) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			serveK8sList(w, r, &fx, res, r.PathValue("ns"))
		}
	}

	mux := http.NewServeMux()

	// ---- discovery：client-go 据此协商可用 API ----
	mux.HandleFunc("GET /api", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"kind": "APIVersions", "apiVersion": "v1", "versions": []string{"v1"},
			"serverAddressByClientCIDRs": []map[string]string{
				{"clientCIDR": "0.0.0.0/0", "serverAddress": r.Host},
			},
		})
	})
	group := func(name, version string) map[string]any {
		gv := name + "/" + version
		return map[string]any{
			"name":             name,
			"versions":         []map[string]string{{"groupVersion": gv, "version": version}},
			"preferredVersion": map[string]string{"groupVersion": gv, "version": version},
		}
	}
	mux.HandleFunc("GET /apis", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"kind": "APIGroupList", "apiVersion": "v1",
			"groups": []map[string]any{group("apps", "v1"), group("networking.k8s.io", "v1")},
		})
	})
	apiResource := func(name string, namespaced bool, kind string) map[string]any {
		return map[string]any{
			"name": name, "singularName": "", "namespaced": namespaced, "kind": kind,
			"verbs": []string{"get", "list", "watch"},
		}
	}
	mux.HandleFunc("GET /api/v1", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"kind": "APIResourceList", "apiVersion": "v1", "groupVersion": "v1",
			"resources": []map[string]any{
				apiResource("namespaces", false, "Namespace"),
				apiResource("nodes", false, "Node"),
				apiResource("pods", true, "Pod"),
				apiResource("services", true, "Service"),
				apiResource("persistentvolumes", false, "PersistentVolume"),
			},
		})
	})
	mux.HandleFunc("GET /apis/apps/v1", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"kind": "APIResourceList", "apiVersion": "v1", "groupVersion": "apps/v1",
			"resources": []map[string]any{
				apiResource("deployments", true, "Deployment"),
				apiResource("statefulsets", true, "StatefulSet"),
				apiResource("daemonsets", true, "DaemonSet"),
			},
		})
	})
	mux.HandleFunc("GET /apis/networking.k8s.io/v1", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"kind": "APIResourceList", "apiVersion": "v1", "groupVersion": "networking.k8s.io/v1",
			"resources": []map[string]any{
				apiResource("ingresses", true, "Ingress"),
			},
		})
	})

	// ---- 版本信息 ----
	mux.HandleFunc("GET /version", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, fx.Version)
	})

	// ---- core v1 list 端点（集群级 + 命名空间级）----
	mux.HandleFunc("GET /api/v1/namespaces", serve(resources["namespaces"]))
	mux.HandleFunc("GET /api/v1/nodes", serve(resources["nodes"]))
	mux.HandleFunc("GET /api/v1/pods", serve(resources["pods"]))
	mux.HandleFunc("GET /api/v1/namespaces/{ns}/pods", serve(resources["pods"]))
	mux.HandleFunc("GET /api/v1/services", serve(resources["services"]))
	mux.HandleFunc("GET /api/v1/namespaces/{ns}/services", serve(resources["services"]))
	mux.HandleFunc("GET /api/v1/persistentvolumes", serve(resources["persistentvolumes"]))

	// ---- apps v1 list 端点 ----
	mux.HandleFunc("GET /apis/apps/v1/deployments", serve(resources["deployments"]))
	mux.HandleFunc("GET /apis/apps/v1/namespaces/{ns}/deployments", serve(resources["deployments"]))
	mux.HandleFunc("GET /apis/apps/v1/statefulsets", serve(resources["statefulsets"]))
	mux.HandleFunc("GET /apis/apps/v1/namespaces/{ns}/statefulsets", serve(resources["statefulsets"]))
	mux.HandleFunc("GET /apis/apps/v1/daemonsets", serve(resources["daemonsets"]))
	mux.HandleFunc("GET /apis/apps/v1/namespaces/{ns}/daemonsets", serve(resources["daemonsets"]))

	// ---- networking v1 list 端点 ----
	mux.HandleFunc("GET /apis/networking.k8s.io/v1/ingresses", serve(resources["ingresses"]))
	mux.HandleFunc("GET /apis/networking.k8s.io/v1/namespaces/{ns}/ingresses", serve(resources["ingresses"]))

	return k8sAuth(mux), nil
}

// serveK8sList 输出官方 metav1.List 壳：kind/apiVersion/metadata.resourceVersion + items。
// 支持 ?namespace= 与 ?labelSelector= 过滤（命名空间级路径自带 ns 约束），
// 以及 ?resourceVersion= 增量语义：请求 RV >= 当前 RV 时视为无变化，
// 返回空 items 与相同 resourceVersion；RV 为 0/缺省/落后时返回全量。
func serveK8sList(w http.ResponseWriter, r *http.Request, fx *k8sFixture, res k8sResource, pathNS string) {
	rv := fx.ResourceVersions[res.rvKey]
	q := r.URL.Query()
	ns := pathNS
	if ns == "" {
		ns = q.Get("namespace")
	}
	selector := q.Get("labelSelector")

	items := res.items(fx)
	filtered := make([]map[string]any, 0, len(items))
	for _, it := range items {
		if ns != "" && k8sMetaString(it, "namespace") != ns {
			continue
		}
		if !k8sMatchSelector(it, selector) {
			continue
		}
		filtered = append(filtered, it)
	}

	// resourceVersion 增量语义（fixture 静态数据，RV 永不递增，故
	// "请求 RV >= 当前 RV" 即"自上次 list 以来无变化"）。
	if rvParam := q.Get("resourceVersion"); rvParam != "" && rvParam != "0" {
		if n, err := strconv.Atoi(rvParam); err == nil {
			if cur, _ := strconv.Atoi(rv); n >= cur {
				filtered = []map[string]any{}
			}
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"kind":       res.kind,
		"apiVersion": res.apiVersion,
		"metadata":   map[string]string{"resourceVersion": rv},
		"items":      filtered,
	})
}

// k8sMetaString 读取资源对象 metadata 下的字符串字段。
func k8sMetaString(item map[string]any, key string) string {
	meta, _ := item["metadata"].(map[string]any)
	s, _ := meta[key].(string)
	return s
}

// k8sMatchSelector 匹配 labelSelector。
//
// ponytail: 只支持官方 labels 语法的等值子集（逗号分隔的 k=v 与裸 k 存在性），
// 覆盖采集器与 curl 联调的全部场景；不支持 in/notin/!= 集合运算，
// 需要时在 metav1.ParseToLabelSelector 依赖引入后升级。
func k8sMatchSelector(item map[string]any, selector string) bool {
	selector = strings.TrimSpace(selector)
	if selector == "" {
		return true
	}
	meta, _ := item["metadata"].(map[string]any)
	labels, _ := meta["labels"].(map[string]any)
	for _, term := range strings.Split(selector, ",") {
		term = strings.TrimSpace(term)
		if term == "" {
			continue
		}
		key, val, hasEq := strings.Cut(term, "=")
		lv, ok := labels[key]
		if !ok {
			return false
		}
		if hasEq {
			s, _ := lv.(string)
			if s != val {
				return false
			}
		}
	}
	return true
}

// k8sAuth 对整个路由做 Bearer 鉴权：非空即放行（官方缺失凭据返回 401 + metav1.Status）。
func k8sAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if bearerToken(r) == "" {
			writeJSON(w, http.StatusUnauthorized, map[string]any{
				"kind": "Status", "apiVersion": "v1", "status": "Failure",
				"message": "Unauthorized", "reason": "Unauthorized", "code": 401,
			})
			return
		}
		next.ServeHTTP(w, r)
	})
}
