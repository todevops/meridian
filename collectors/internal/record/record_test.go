package record

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestNormalizeBaseURL(t *testing.T) {
	cases := map[string]string{
		":19005":                  "http://localhost:19005",
		"localhost:19005":         "http://localhost:19005",
		"http://localhost:19005/": "http://localhost:19005",
		"https://tsdb.internal":   "https://tsdb.internal",
		"":                        "",
	}
	for in, want := range cases {
		if got := NormalizeBaseURL(in); got != want {
			t.Errorf("NormalizeBaseURL(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestStrField(t *testing.T) {
	m := map[string]any{
		"name":  "web-01",
		"ips":   []any{"10.0.0.1", "10.0.0.2"},
		"empty": "",
		"num":   3.0,
	}
	if got := StrField(m, "missing", "name"); got != "web-01" {
		t.Errorf("回退取键失败: %q", got)
	}
	if got := StrField(m, "ips"); got != "10.0.0.1" {
		t.Errorf("数组取首元素失败: %q", got)
	}
	if got := StrField(m, "empty", "num"); got != "" {
		t.Errorf("非标量应返回空串: %q", got)
	}
	if got := StrField(m, "missing"); got != "" {
		t.Errorf("缺失键应返回空串: %q", got)
	}
}

func sampleRecords() []Record {
	return []Record{
		{
			Source:         "aliyun",
			Collector:      "aliyun-ecs-collector",
			ModelCandidate: "host",
			Attributes:     map[string]any{"ip": "10.0.0.1"},
			OccurredAt:     time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC),
		},
	}
}

func TestHTTPSinkSubmitAccepted(t *testing.T) {
	var gotReq batchRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/discovery-records" {
			t.Errorf("上报路径错误: %s", r.URL.Path)
		}
		if r.Method != http.MethodPost {
			t.Errorf("上报方法错误: %s", r.Method)
		}
		if ct := r.Header.Get("Content-Type"); ct != "application/json" {
			t.Errorf("Content-Type 错误: %s", ct)
		}
		if err := json.NewDecoder(r.Body).Decode(&gotReq); err != nil {
			t.Errorf("请求体解码失败: %v", err)
		}
		w.Write([]byte(`{"accepted":1,"rejected":0,"errors":[]}`))
	}))
	defer srv.Close()

	sink := NewHTTPSink(srv.URL, "")
	if err := sink.Submit(context.Background(), sampleRecords()); err != nil {
		t.Fatalf("Submit 应成功: %v", err)
	}
	if len(gotReq.Records) != 1 || gotReq.Records[0].ModelCandidate != "host" {
		t.Fatalf("上报内容不符: %+v", gotReq)
	}
}

func TestHTTPSinkSubmitRejected(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"accepted":0,"rejected":1,"errors":[{"index":0,"message":"缺少 source"}]}`))
	}))
	defer srv.Close()

	sink := NewHTTPSink(srv.URL, "")
	err := sink.Submit(context.Background(), sampleRecords())
	if err == nil {
		t.Fatal("rejected>0 应返回错误")
	}
	if !strings.Contains(err.Error(), "缺少 source") {
		t.Fatalf("错误应带拒收明细: %v", err)
	}
}

func TestHTTPSinkSubmitHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	sink := NewHTTPSink(srv.URL, "")
	if err := sink.Submit(context.Background(), sampleRecords()); err == nil {
		t.Fatal("500 应返回错误")
	}
}

func TestHTTPSinkSubmitEmptySkipsCall(t *testing.T) {
	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	}))
	defer srv.Close()

	sink := NewHTTPSink(srv.URL, "")
	if err := sink.Submit(context.Background(), nil); err != nil {
		t.Fatalf("空批次不应报错: %v", err)
	}
	if called {
		t.Fatal("空批次不应发起 HTTP 请求（契约要求 records 至少一条）")
	}
}

func TestDryRunSinkPrintsJSON(t *testing.T) {
	var buf strings.Builder
	sink := NewDryRunSink(&buf)
	if err := sink.Submit(context.Background(), sampleRecords()); err != nil {
		t.Fatalf("Submit 失败: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "[dry-run]") || !strings.Contains(out, `"model_candidate": "host"`) {
		t.Fatalf("dry-run 输出不符:\n%s", out)
	}
	// 输出中的 JSON 部分（独占一行）应可解析回记录
	idx := strings.Index(out, "\n[")
	var recs []Record
	if err := json.Unmarshal([]byte(out[idx+1:]), &recs); err != nil {
		t.Fatalf("dry-run 输出 JSON 无法解析: %v", err)
	}
	if len(recs) != 1 || recs[0].Attributes["ip"] != "10.0.0.1" {
		t.Fatalf("dry-run 解析结果不符: %+v", recs)
	}
}

func TestDoJSONNon2xx(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		w.Write([]byte(`{"error":"forbidden"}`))
	}))
	defer srv.Close()

	err := DoJSON(context.Background(), srv.Client(), http.MethodGet, srv.URL+"/x", nil, nil, nil)
	if err == nil || !strings.Contains(err.Error(), "403") {
		t.Fatalf("非 2xx 应返回含状态码的错误: %v", err)
	}
}

func TestDoJSONBadResponseBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`not-json`))
	}))
	defer srv.Close()

	var out map[string]any
	err := DoJSON(context.Background(), srv.Client(), http.MethodGet, srv.URL, nil, nil, &out)
	if err == nil || !strings.Contains(err.Error(), "解析") {
		t.Fatalf("非法 JSON 应返回解析错误: %v", err)
	}
}
