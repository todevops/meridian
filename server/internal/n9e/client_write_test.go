// n9e Client 写方法与告警拉取测试：请求方法/路径/报文/Bearer 头与响应壳错误处理。
package n9e

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

// newWriteStub 启动记录请求的 n9e 写接口测试桩（per-target-id 形态）。
func newWriteStub(t *testing.T, failErr string) (*httptest.Server, *map[string]any) {
	t.Helper()
	last := map[string]any{}
	mux := http.NewServeMux()
	mux.HandleFunc("PUT /api/n9e/targets/{id}/tags", func(w http.ResponseWriter, r *http.Request) {
		last["method"] = r.Method
		last["auth"] = r.Header.Get("Authorization")
		last["id"] = r.PathValue("id")
		_ = json.NewDecoder(r.Body).Decode(&last)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"dat":null,"err":%q}`, failErr)
	})
	mux.HandleFunc("PUT /api/n9e/targets/{id}/note", func(w http.ResponseWriter, r *http.Request) {
		last["method"] = r.Method
		last["id"] = r.PathValue("id")
		_ = json.NewDecoder(r.Body).Decode(&last)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"dat":null,"err":%q}`, failErr)
	})
	mux.HandleFunc("GET /api/n9e/alert-cur-events", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `[{"severity":1,"trigger":"disk_full","ident":%q}]`, r.URL.Query().Get("ident"))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, &last
}

func TestUpdateTargetTagsAndNote(t *testing.T) {
	srv, last := newWriteStub(t, "")
	client := NewClient(srv.URL, "tk")

	if err := client.UpdateTargetTags(context.Background(), 101, []string{"biz_group=电商前台", "env=prod"}); err != nil {
		t.Fatalf("回写 tags 失败: %v", err)
	}
	if (*last)["auth"] != "Bearer tk" {
		t.Fatalf("Bearer 头不符: %v", (*last)["auth"])
	}
	if (*last)["id"] != "101" || (*last)["tags"] != "biz_group=电商前台 env=prod" {
		t.Fatalf("tags 请求报文不符: %v", *last)
	}

	if err := client.UpdateTargetNote(context.Background(), 101, "负责人:张三"); err != nil {
		t.Fatalf("回写 note 失败: %v", err)
	}
	if (*last)["note"] != "负责人:张三" {
		t.Fatalf("note 请求报文不符: %v", *last)
	}
	if client.BaseURL() != srv.URL {
		t.Fatalf("BaseURL 不符: %s", client.BaseURL())
	}
}

func TestWriteShellError(t *testing.T) {
	srv, _ := newWriteStub(t, "target 不存在")
	client := NewClient(srv.URL, "tk")
	if err := client.UpdateTargetTags(context.Background(), 999, []string{"a=b"}); err == nil {
		t.Fatal("响应壳 err 非空应返回错误")
	}
}

func TestAlertCurEventsRaw(t *testing.T) {
	srv, _ := newWriteStub(t, "")
	client := NewClient(srv.URL, "tk")
	raw, err := client.AlertCurEvents(context.Background(), "db-01")
	if err != nil {
		t.Fatalf("拉取当前告警失败: %v", err)
	}
	var arr []map[string]any
	if err := json.Unmarshal(raw, &arr); err != nil || len(arr) != 1 || arr[0]["ident"] != "db-01" {
		t.Fatalf("原始告警不符: %s", string(raw))
	}
}
