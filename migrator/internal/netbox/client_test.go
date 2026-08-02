// NetBox 客户端单测：httptest 夹具验证认证头、分页遍历、信封解析与错误路径。
package netbox

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
)

// newPaginatedStub 返回一个分页夹具：按 limit/offset 切片，并记录每次请求的认证头。
func newPaginatedStub(t *testing.T, total int, auths *[]string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*auths = append(*auths, r.Header.Get("Authorization"))
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
		if limit <= 0 {
			limit = 50
		}
		end := offset + limit
		if end > total {
			end = total
		}
		if offset > total {
			offset = total
		}
		results := make([]map[string]any, 0, limit)
		for i := offset; i < end; i++ {
			results = append(results, map[string]any{
				"id": i + 1, "name": fmt.Sprintf("site-%d", i+1), "slug": fmt.Sprintf("site-%d", i+1),
				"status": map[string]any{"value": "active"},
			})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"count": total, "next": nil, "previous": nil, "results": results,
		})
	}))
}

// TestListSitesPagination 验证跨页拉取与认证头携带。
func TestListSitesPagination(t *testing.T) {
	var auths []string
	// 总数 250 > pageLimit 100，强制翻 3 页。
	stub := newPaginatedStub(t, 250, &auths)
	defer stub.Close()

	c := NewClient(stub.URL, "test-token")
	sites, err := c.ListSites(context.Background())
	if err != nil {
		t.Fatalf("ListSites 失败: %v", err)
	}
	if len(sites) != 250 {
		t.Fatalf("期望 250 条站点，实际 %d", len(sites))
	}
	if sites[0].Slug != "site-1" || sites[249].ID != 250 {
		t.Fatalf("分页拼接结果异常: 首=%+v 尾=%+v", sites[0], sites[249])
	}
	if len(auths) != 3 {
		t.Fatalf("期望 3 次分页请求，实际 %d", len(auths))
	}
	for i, a := range auths {
		if a != "Token test-token" {
			t.Fatalf("第 %d 次请求认证头异常: %q", i, a)
		}
	}
}

// TestListForbidden 验证 403 时返回 APIError。
func TestListForbidden(t *testing.T) {
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"detail":"Authentication credentials were not provided."}`))
	}))
	defer stub.Close()

	c := NewClient(stub.URL, "")
	_, err := c.ListRacks(context.Background())
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("期望 APIError，实际: %v", err)
	}
	if apiErr.StatusCode != http.StatusForbidden {
		t.Fatalf("期望 403，实际 %d", apiErr.StatusCode)
	}
}

// TestListBadEnvelope 验证非法 JSON 信封返回解析错误。
func TestListBadEnvelope(t *testing.T) {
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`not-json`))
	}))
	defer stub.Close()

	c := NewClient(stub.URL, "t")
	if _, err := c.ListVLANs(context.Background()); err == nil {
		t.Fatal("期望解析错误，实际为 nil")
	}
}

// TestEmptyResults 验证空列表正常返回零条。
func TestEmptyResults(t *testing.T) {
	stub := newPaginatedStub(t, 0, &[]string{})
	defer stub.Close()

	c := NewClient(stub.URL, "t")
	prefixes, err := c.ListPrefixes(context.Background())
	if err != nil {
		t.Fatalf("空列表不应报错: %v", err)
	}
	if len(prefixes) != 0 {
		t.Fatalf("期望 0 条，实际 %d", len(prefixes))
	}
}
