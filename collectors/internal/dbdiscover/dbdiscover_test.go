package dbdiscover

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// tsdbFixture 按 match[] 参数返回 Prometheus 风格的 instance 标签值。
func tsdbFixture(t *testing.T) *httptest.Server {
	t.Helper()
	values := map[string][]string{
		"mysql_up":                     {"10.60.0.1:3306", "10.60.0.2:3307"},
		"redis_up":                     {"10.60.0.3:6379"},
		"kafka_brokers":                {},
		"elasticsearch_cluster_health": {"es-node-1:9200"},
	}
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/label/instance/values" {
			t.Errorf("TSDB 路径不符: %s", r.URL.Path)
		}
		metric := r.URL.Query().Get("match[]")
		data, ok := values[metric]
		if !ok {
			t.Errorf("未预期的指标: %q", metric)
			data = []string{}
		}
		json.NewEncoder(w).Encode(map[string]any{"status": "success", "data": data})
	}))
}

// cmdbFixture 模拟 CMDB 模型 API；patchBody 记录 PATCH 请求体。
func cmdbFixture(t *testing.T, modelsBody string, patchBody *string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/models":
			w.Write([]byte(modelsBody))
		case r.Method == http.MethodPatch && strings.HasPrefix(r.URL.Path, "/api/v1/models/"):
			b, _ := io.ReadAll(r.Body)
			*patchBody = string(b)
			w.Write([]byte(`{"id":"m-1","name":"数据库实例","code":"db_instance","attributes":[],"relations":[],"reconcile_keys":["instance_addr"],"created_at":"2026-08-01T00:00:00Z","updated_at":"2026-08-01T00:00:00Z"}`))
		default:
			t.Errorf("未预期的 CMDB 请求: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
}

const modelWithKeys = `{"items":[{"id":"m-1","code":"db_instance","reconcile_keys":["instance_addr"]}],"total":1,"page":1,"page_size":100}`
const modelWithoutKeys = `{"items":[{"id":"m-1","code":"db_instance"}],"total":1,"page":1,"page_size":100}`
const modelAbsent = `{"items":[],"total":0,"page":1,"page_size":100}`

func TestCollectMapping(t *testing.T) {
	tsdb := tsdbFixture(t)
	defer tsdb.Close()
	var patch string
	cmdb := cmdbFixture(t, modelWithKeys, &patch)
	defer cmdb.Close()

	logs := []string{}
	c := New(tsdb.URL, cmdb.URL, "", false, func(f string, args ...any) { logs = append(logs, f) })
	recs, err := c.Collect(context.Background())
	if err != nil {
		t.Fatalf("Collect 失败: %v", err)
	}
	if len(recs) != 4 {
		t.Fatalf("应产出 4 条记录: %d", len(recs))
	}
	if patch != "" {
		t.Errorf("调和键已一致时不应 PATCH: %s", patch)
	}

	first := recs[0]
	if first.Source != "tsdb" || first.Collector != "db-discoverer" || first.ModelCandidate != "db_instance" {
		t.Errorf("记录头不符: %+v", first)
	}
	a := first.Attributes
	if a["component_type"] != "mysql" || a["ip"] != "10.60.0.1" || a["port"] != 3306 || a["source"] != "tsdb_label" {
		t.Errorf("mysql 实例映射不符: %+v", a)
	}
	// 最后一条是 elasticsearch
	last := recs[3].Attributes
	if last["component_type"] != "elasticsearch" || last["ip"] != "es-node-1" || last["port"] != 9200 {
		t.Errorf("elasticsearch 实例映射不符: %+v", last)
	}
}

func TestCollectPatchesMissingReconcileKeys(t *testing.T) {
	tsdb := tsdbFixture(t)
	defer tsdb.Close()
	var patch string
	cmdb := cmdbFixture(t, modelWithoutKeys, &patch)
	defer cmdb.Close()

	logs := []string{}
	c := New(tsdb.URL, cmdb.URL, "", false, func(f string, args ...any) { logs = append(logs, f) })
	if _, err := c.Collect(context.Background()); err != nil {
		t.Fatalf("Collect 失败: %v", err)
	}
	if patch == "" {
		t.Fatal("调和键缺失时应 PATCH 模型")
	}
	var body map[string]any
	if err := json.Unmarshal([]byte(patch), &body); err != nil {
		t.Fatalf("PATCH 体非法: %v", err)
	}
	keys, _ := body["reconcile_keys"].([]any)
	if len(keys) != 1 || keys[0] != "instance_addr" {
		t.Errorf("PATCH 调和键不符: %s", patch)
	}
}

func TestCollectModelAbsentOnlyWarns(t *testing.T) {
	tsdb := tsdbFixture(t)
	defer tsdb.Close()
	var patch string
	cmdb := cmdbFixture(t, modelAbsent, &patch)
	defer cmdb.Close()

	logs := []string{}
	c := New(tsdb.URL, cmdb.URL, "", false, func(f string, args ...any) { logs = append(logs, f) })
	recs, err := c.Collect(context.Background())
	if err != nil {
		t.Fatalf("模型不存在不应导致采集失败: %v", err)
	}
	if len(recs) != 4 {
		t.Errorf("采集应继续: %d 条", len(recs))
	}
	if patch != "" {
		t.Errorf("模型不存在时不应 PATCH: %s", patch)
	}
}

func TestCollectDryRunSkipsPatch(t *testing.T) {
	tsdb := tsdbFixture(t)
	defer tsdb.Close()
	var patch string
	cmdb := cmdbFixture(t, modelWithoutKeys, &patch)
	defer cmdb.Close()

	logs := []string{}
	c := New(tsdb.URL, cmdb.URL, "", true, func(f string, args ...any) { logs = append(logs, f) })
	if _, err := c.Collect(context.Background()); err != nil {
		t.Fatalf("Collect 失败: %v", err)
	}
	if patch != "" {
		t.Errorf("dry-run 不应 PATCH: %s", patch)
	}
	joined := strings.Join(logs, "")
	if !strings.Contains(joined, "dry-run") {
		t.Errorf("dry-run 应打印跳过提示: %v", logs)
	}
}

func TestCollectDryRunToleratesCMDBDown(t *testing.T) {
	tsdb := tsdbFixture(t)
	defer tsdb.Close()
	// CMDB 地址指向一个立即关闭的服务器，模拟离线 dry-run
	dead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	dead.Close()

	c := New(tsdb.URL, dead.URL, "", true, func(string, ...any) {})
	recs, err := c.Collect(context.Background())
	if err != nil {
		t.Fatalf("dry-run 下 CMDB 不可达应降级为告警: %v", err)
	}
	if len(recs) != 4 {
		t.Errorf("记录应照常产出: %d", len(recs))
	}
}

func TestCollectCMDBDownFailsWhenNotDryRun(t *testing.T) {
	tsdb := tsdbFixture(t)
	defer tsdb.Close()
	dead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	dead.Close()

	c := New(tsdb.URL, dead.URL, "", false, func(string, ...any) {})
	if _, err := c.Collect(context.Background()); err == nil {
		t.Fatal("非 dry-run 下 CMDB 不可达应报错")
	}
}

func TestCollectTSDBErrorStatus(t *testing.T) {
	tsdb := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"status": "error", "error": "parse error"})
	}))
	defer tsdb.Close()
	var patch string
	cmdb := cmdbFixture(t, modelWithKeys, &patch)
	defer cmdb.Close()

	c := New(tsdb.URL, cmdb.URL, "", false, func(string, ...any) {})
	_, err := c.Collect(context.Background())
	if err == nil || !strings.Contains(err.Error(), "parse error") {
		t.Fatalf("TSDB 错误状态应返回错误: %v", err)
	}
}

func TestSplitInstance(t *testing.T) {
	cases := []struct {
		in, ip, port string
	}{
		{"10.0.0.1:3306", "10.0.0.1", "3306"},
		{"db.internal:5432", "db.internal", "5432"},
		{"10.0.0.2", "10.0.0.2", ""},
		{"[::1]:9200", "::1", "9200"},
	}
	for _, c := range cases {
		ip, port := splitInstance(c.in)
		if ip != c.ip || port != c.port {
			t.Errorf("splitInstance(%q) = (%q,%q), want (%q,%q)", c.in, ip, port, c.ip, c.port)
		}
	}
}
