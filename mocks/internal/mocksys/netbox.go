package mocksys

import (
	"encoding/json"
	"fmt"
	"net/http"
)

// nbPage 是 NetBox 官方风格的分页响应壳。
type nbPage struct {
	Count    int               `json:"count"`
	Next     *string           `json:"next"`
	Previous *string           `json:"previous"`
	Results  []json.RawMessage `json:"results"`
}

// netboxTable 描述一张 NetBox 资源表的路由与 fixture 对应关系。
type netboxTable struct {
	path string // API 路径（带尾部斜杠，与官方一致）
	file string // fixture 文件名
}

// newNetBox 构建 NetBox mock（:19002）：
// 7 个只读列表端点，NetBox 风格分页 {count,next,previous,results}，
// Authorization: Token 非空校验（空则 403）。
func newNetBox() (http.Handler, error) {
	tables := []netboxTable{
		{"/api/dcim/sites/", "netbox-sites.json"},
		{"/api/dcim/racks/", "netbox-racks.json"},
		{"/api/dcim/devices/", "netbox-devices.json"},
		{"/api/ipam/prefixes/", "netbox-prefixes.json"},
		{"/api/ipam/ip-addresses/", "netbox-ip-addresses.json"},
		{"/api/ipam/vlans/", "netbox-vlans.json"},
		{"/api/virtualization/virtual-machines/", "netbox-virtual-machines.json"},
	}

	mux := http.NewServeMux()
	for _, tb := range tables {
		raw, err := readFixture(tb.file)
		if err != nil {
			return nil, fmt.Errorf("读取 %s 失败: %w", tb.file, err)
		}
		var items []json.RawMessage
		if err := json.Unmarshal(raw, &items); err != nil {
			return nil, fmt.Errorf("解析 %s 失败: %w", tb.file, err)
		}
		mux.HandleFunc("GET "+tb.path, netboxListHandler(items))
	}
	return netboxAuth(mux), nil
}

// netboxAuth 对整个路由做 Token 鉴权，与官方 DRF 行为一致（失败 403）。
func netboxAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if apiToken(r) == "" {
			writeJSON(w, http.StatusForbidden, map[string]string{"detail": "Authentication credentials were not provided."})
			return
		}
		next.ServeHTTP(w, r)
	})
}

// netboxListHandler 输出一页结果；支持 limit/offset 查询参数（缺省全量单页）。
func netboxListHandler(items []json.RawMessage) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		total := len(items)
		limit := intQuery(r, "limit", 0)
		offset := intQuery(r, "offset", 0)
		if offset < 0 {
			offset = 0
		}
		if offset > total {
			offset = total
		}
		end := total
		if limit > 0 && offset+limit < end {
			end = offset + limit
		}

		page := make([]json.RawMessage, 0, end-offset)
		page = append(page, items[offset:end]...)

		var next, previous *string
		if limit > 0 {
			if end < total {
				s := fmt.Sprintf("http://%s%s?limit=%d&offset=%d", r.Host, r.URL.Path, limit, end)
				next = &s
			}
			if offset > 0 {
				prevOffset := offset - limit
				if prevOffset < 0 {
					prevOffset = 0
				}
				s := fmt.Sprintf("http://%s%s?limit=%d&offset=%d", r.Host, r.URL.Path, limit, prevOffset)
				previous = &s
			}
		}
		writeJSON(w, http.StatusOK, nbPage{Count: total, Next: next, Previous: previous, Results: page})
	}
}
