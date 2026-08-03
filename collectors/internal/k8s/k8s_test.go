package k8s

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// 以下夹具均为官方 list 壳（apiVersion/kind + items），与真实 apiserver 响应形态一致。

const versionFixture = `{
  "major": "1",
  "minor": "29",
  "gitVersion": "v1.29.2",
  "platform": "linux/amd64"
}`

const nodeListFixture = `{
  "kind": "NodeList",
  "apiVersion": "v1",
  "metadata": {"resourceVersion": "1024"},
  "items": [
    {
      "metadata": {"name": "k8s-node-01", "labels": {"env": "prod", "biz": "pay", "owner": "sre", "kubernetes.io/hostname": "k8s-node-01"}},
      "status": {
        "addresses": [
          {"type": "InternalIP", "address": "10.70.8.11"},
          {"type": "Hostname", "address": "k8s-node-01"}
        ],
        "conditions": [{"type": "Ready", "status": "True"}]
      }
    },
    {
      "metadata": {"name": "k8s-node-02", "labels": {"env": "prod"}},
      "status": {
        "addresses": [{"type": "InternalIP", "address": "10.70.8.12"}],
        "conditions": [{"type": "Ready", "status": "False", "reason": "KubeletNotReady"}]
      }
    }
  ]
}`

const namespaceListFixture = `{
  "kind": "NamespaceList",
  "apiVersion": "v1",
  "metadata": {"resourceVersion": "1024"},
  "items": [
    {"metadata": {"name": "default"}},
    {"metadata": {"name": "pay-app"}}
  ]
}`

const deploymentListFixture = `{
  "kind": "DeploymentList",
  "apiVersion": "apps/v1",
  "metadata": {"resourceVersion": "1024"},
  "items": [
    {
      "metadata": {"name": "pay-api", "namespace": "pay-app", "labels": {"app": "pay-api", "env": "prod", "helm.sh/chart": "pay-api-1.2.3"}},
      "spec": {
        "replicas": 3,
        "template": {"spec": {"containers": [{"name": "pay-api", "image": "registry.local/pay-api:v1.2.3"}]}}
      }
    }
  ]
}`

const statefulSetListFixture = `{
  "kind": "StatefulSetList",
  "apiVersion": "apps/v1",
  "metadata": {"resourceVersion": "1024"},
  "items": [
    {
      "metadata": {"name": "pay-db", "namespace": "pay-app", "labels": {"app": "pay-db"}},
      "spec": {
        "replicas": 1,
        "template": {"spec": {"containers": [{"name": "mysql", "image": "mysql:8.0"}]}}
      }
    }
  ]
}`

const daemonSetListFixture = `{
  "kind": "DaemonSetList",
  "apiVersion": "apps/v1",
  "metadata": {"resourceVersion": "1024"},
  "items": [
    {
      "metadata": {"name": "node-exporter", "namespace": "kube-system", "labels": {"app": "node-exporter"}},
      "spec": {
        "template": {"spec": {"containers": [{"name": "node-exporter", "image": "prom/node-exporter:v1.7.0"}]}}
      },
      "status": {"desiredNumberScheduled": 2}
    }
  ]
}`

const serviceListFixture = `{
  "kind": "ServiceList",
  "apiVersion": "v1",
  "metadata": {"resourceVersion": "1024"},
  "items": [
    {
      "metadata": {"name": "pay-api", "namespace": "pay-app"},
      "spec": {"type": "ClusterIP", "selector": {"app": "pay-api"}}
    }
  ]
}`

const ingressListFixture = `{
  "kind": "IngressList",
  "apiVersion": "networking.k8s.io/v1",
  "metadata": {"resourceVersion": "1024"},
  "items": [
    {
      "metadata": {"name": "pay-ingress", "namespace": "pay-app"},
      "spec": {"rules": [{"host": "pay.example.com"}, {"host": "api.pay.example.com"}]}
    }
  ]
}`

// newFixtureServer 起 fake apiserver：校验 Bearer Token，按官方路径返回 list 壳。
func newFixtureServer(t *testing.T) *httptest.Server {
	t.Helper()
	fixtures := map[string]string{
		"/version":                             versionFixture,
		"/api/v1/nodes":                        nodeListFixture,
		"/api/v1/namespaces":                   namespaceListFixture,
		"/api/v1/services":                     serviceListFixture,
		"/apis/apps/v1/deployments":            deploymentListFixture,
		"/apis/apps/v1/statefulsets":           statefulSetListFixture,
		"/apis/apps/v1/daemonsets":             daemonSetListFixture,
		"/apis/networking.k8s.io/v1/ingresses": ingressListFixture,
	}
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer dev-k8s-token" {
			w.WriteHeader(http.StatusUnauthorized)
			fmt.Fprint(w, `{"kind":"Status","apiVersion":"v1","status":"Failure","reason":"Unauthorized"}`)
			return
		}
		body, ok := fixtures[r.URL.Path]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprintf(w, `{"kind":"Status","apiVersion":"v1","status":"Failure","reason":"NotFound","message":"%s"}`, r.URL.Path)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, body)
	}))
}

func collectFixture(t *testing.T) map[string][]map[string]any {
	t.Helper()
	srv := newFixtureServer(t)
	defer srv.Close()

	c, err := New(srv.URL, "dev-k8s-token", "volc-prod-k8s", "", true)
	if err != nil {
		t.Fatalf("创建采集器失败: %v", err)
	}
	recs, err := c.Collect(context.Background())
	if err != nil {
		t.Fatalf("Collect 失败: %v", err)
	}
	// 按 model_candidate 归组，便于断言。
	byModel := map[string][]map[string]any{}
	for _, r := range recs {
		if r.Source != "kubernetes" || r.Collector != "k8s-metadata-collector" {
			t.Errorf("记录头不符: %+v", r)
		}
		byModel[r.ModelCandidate] = append(byModel[r.ModelCandidate], r.Attributes)
	}
	return byModel
}

func TestCollectClusterAndNodes(t *testing.T) {
	byModel := collectFixture(t)

	clusters := byModel["k8s_cluster"]
	if len(clusters) != 1 {
		t.Fatalf("应产出 1 条 k8s_cluster: %d", len(clusters))
	}
	c := clusters[0]
	if c["name"] != "volc-prod-k8s" || c["version"] != "v1.29.2" {
		t.Errorf("集群名/版本不符: %+v", c)
	}
	if c["node_count"] != 2 {
		t.Errorf("node_count 不符: %+v", c)
	}

	hosts := byModel["host"]
	if len(hosts) != 2 {
		t.Fatalf("应产出 2 条 host（含关电节点）: %d", len(hosts))
	}
	h := hosts[0]
	if h["ident"] != "k8s-node-01" || h["ip"] != "10.70.8.11" || h["host_type"] != "k8s_node" {
		t.Errorf("节点映射不符: %+v", h)
	}
	if h["ready"] != true {
		t.Errorf("就绪节点 ready 应为 true: %+v", h)
	}
	labels, ok := h["labels"].(map[string]string)
	if !ok {
		t.Fatalf("labels 类型不符: %T", h["labels"])
	}
	if labels["env"] != "prod" || labels["biz"] != "pay" || labels["owner"] != "sre" {
		t.Errorf("节点标签白名单不符: %+v", labels)
	}
	if _, junk := labels["kubernetes.io/hostname"]; junk {
		t.Errorf("白名单外标签应被过滤: %+v", labels)
	}

	// 关电节点容错：仍建档、ready=false，采集中断不应发生。
	h2 := hosts[1]
	if h2["ident"] != "k8s-node-02" || h2["ready"] != false {
		t.Errorf("关电节点应建档且 ready=false: %+v", h2)
	}
}

func TestCollectNamespaces(t *testing.T) {
	byModel := collectFixture(t)
	nss := byModel["k8s_namespace"]
	if len(nss) != 2 {
		t.Fatalf("应产出 2 条 k8s_namespace: %d", len(nss))
	}
	if nss[0]["cluster"] != "volc-prod-k8s" || nss[0]["name"] != "default" {
		t.Errorf("命名空间映射不符: %+v", nss[0])
	}
	if nss[1]["name"] != "pay-app" {
		t.Errorf("命名空间映射不符: %+v", nss[1])
	}
}

func TestCollectWorkloads(t *testing.T) {
	byModel := collectFixture(t)
	wls := byModel["k8s_workload"]
	if len(wls) != 3 {
		t.Fatalf("应产出 3 条 k8s_workload: %d", len(wls))
	}
	byName := map[string]map[string]any{}
	for _, w := range wls {
		byName[w["name"].(string)] = w
		if w["cluster"] != "volc-prod-k8s" {
			t.Errorf("工作负载 cluster 不符: %+v", w)
		}
	}
	dep := byName["pay-api"]
	if dep == nil || dep["kind"] != "Deployment" || dep["namespace"] != "pay-app" {
		t.Fatalf("Deployment 映射不符: %+v", dep)
	}
	if dep["replicas"] != int32(3) || dep["image"] != "registry.local/pay-api:v1.2.3" {
		t.Errorf("Deployment replicas/image 不符: %+v", dep)
	}
	labels := dep["labels"].(map[string]string)
	if labels["app"] != "pay-api" || labels["env"] != "prod" {
		t.Errorf("工作负载标签白名单不符: %+v", labels)
	}
	if _, junk := labels["helm.sh/chart"]; junk {
		t.Errorf("白名单外标签应被过滤: %+v", labels)
	}
	sts := byName["pay-db"]
	if sts == nil || sts["kind"] != "StatefulSet" || sts["image"] != "mysql:8.0" {
		t.Errorf("StatefulSet 映射不符: %+v", sts)
	}
	ds := byName["node-exporter"]
	if ds == nil || ds["kind"] != "DaemonSet" || ds["replicas"] != int32(2) {
		t.Errorf("DaemonSet 映射不符（replicas 取 desiredNumberScheduled）: %+v", ds)
	}
}

func TestCollectServices(t *testing.T) {
	byModel := collectFixture(t)
	svcs := byModel["k8s_service"]
	if len(svcs) != 2 {
		t.Fatalf("应产出 2 条 k8s_service: %d", len(svcs))
	}
	byName := map[string]map[string]any{}
	for _, s := range svcs {
		byName[s["name"].(string)] = s
	}
	svc := byName["pay-api"]
	if svc == nil || svc["kind"] != "service" || svc["namespace"] != "pay-app" {
		t.Fatalf("Service 映射不符: %+v", svc)
	}
	if svc["selector"] != "app=pay-api" {
		t.Errorf("Service selector 不符: %+v", svc["selector"])
	}
	ing := byName["pay-ingress"]
	if ing == nil || ing["kind"] != "ingress" {
		t.Fatalf("Ingress 映射不符: %+v", ing)
	}
	if ing["host"] != "pay.example.com,api.pay.example.com" {
		t.Errorf("Ingress host 拼接不符: %+v", ing)
	}
}

func TestCollectUnauthorized(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprint(w, `{"kind":"Status","apiVersion":"v1","status":"Failure","reason":"Unauthorized"}`)
	}))
	defer srv.Close()

	c, err := New(srv.URL, "bad-token", "volc-prod-k8s", "", true)
	if err != nil {
		t.Fatalf("创建采集器失败: %v", err)
	}
	if _, err := c.Collect(context.Background()); err == nil || !strings.Contains(err.Error(), "nodes") {
		t.Fatalf("401 应返回带资源名的错误: %v", err)
	}
}
