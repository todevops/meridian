package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"collectors/internal/record"
	"collectors/internal/runner"
)

// TestBuildRejectsUnknown 未知采集器名应在初始化时报错。
func TestBuildRejectsUnknown(t *testing.T) {
	if _, err := build("nope", "http://localhost:8080", "", false); err == nil {
		t.Fatal("未知采集器应报错")
	}
}

// TestBuildNormalizesEnvEndpoints 环境变量端点简写应被规范化。
func TestBuildNormalizesEnvEndpoints(t *testing.T) {
	t.Setenv("ALIYUN_API_URL", ":19005")
	c, err := build("aliyun", "http://localhost:8080", "", false)
	if err != nil {
		t.Fatalf("build 失败: %v", err)
	}
	if c.Name() != "aliyun" {
		t.Errorf("采集器名不符: %s", c.Name())
	}
}

// TestDryRunEndToEnd 对 httptest 夹具跑通 -dry-run 全链路：
// env 装配 → 拉取夹具 → 映射 → DryRunSink 打印，输出可解析回标准发现记录。
func TestDryRunEndToEnd(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`[{
			"InstanceId": "i-e2e-001",
			"InstanceName": "e2e-host",
			"Status": "Running",
			"InstanceType": "ecs.g7.large",
			"PrivateIpAddress": ["10.99.0.1"],
			"ZoneId": "cn-beijing-h",
			"Tags": {"env": "e2e"}
		}]`))
	}))
	defer srv.Close()

	t.Setenv("ALIYUN_API_URL", srv.URL)

	c, err := build("aliyun", "http://localhost:8080", "", true)
	if err != nil {
		t.Fatalf("build 失败: %v", err)
	}
	var buf strings.Builder
	sink := record.NewDryRunSink(&buf)
	logs := []string{}
	var prodOut strings.Builder
	if err := runner.Run(context.Background(), []runner.Collector{c}, sink, func(f string, args ...any) {
		logs = append(logs, f)
	}, &prodOut); err != nil {
		t.Fatalf("dry-run 运行失败: %v", err)
	}
	if prodOut.String() != "CMDB_PRODUCED=1\n" {
		t.Fatalf("dry-run 也应打印 CMDB_PRODUCED: %q", prodOut.String())
	}

	out := buf.String()
	if !strings.Contains(out, "[dry-run]") {
		t.Fatalf("缺 dry-run 标记:\n%s", out)
	}
	idx := strings.Index(out, "\n[")
	var recs []record.Record
	if err := json.Unmarshal([]byte(out[idx+1:]), &recs); err != nil {
		t.Fatalf("dry-run 输出不是合法 JSON: %v\n%s", err, out)
	}
	if len(recs) != 1 {
		t.Fatalf("应打印 1 条记录: %d", len(recs))
	}
	r := recs[0]
	if r.Source != "aliyun" || r.ModelCandidate != "host" {
		t.Errorf("记录头不符: %+v", r)
	}
	if r.Attributes["cloud_instance_id"] != "i-e2e-001" || r.Attributes["ip"] != "10.99.0.1" {
		t.Errorf("属性映射不符: %+v", r.Attributes)
	}
	if r.OccurredAt.IsZero() {
		t.Error("occurred_at 不应为零值")
	}
}
