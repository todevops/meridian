// IPAM 比对：扫描结果对照 CMDB IPAM 登记，产出四类结论（回收线索/黑设备发现/MAC 变更告警）。
package ipscan

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"collectors/internal/record"
)

// ipamPrefix 是 IPAM 前缀（比对时只关心 id 与 cidr 用于日志）。
type ipamPrefix struct {
	ID   string `json:"id"`
	CIDR string `json:"cidr"`
}

// ipamIP 是 IPAM 已登记 IP。
type ipamIP struct {
	ID     string `json:"id"`
	IP     string `json:"ip"`
	Status string `json:"status"` // used/reserved
	CIID   string `json:"ci_id"`
}

// pagedResponse 对应 CMDB 列表接口的分页信封。
type pagedResponse[T any] struct {
	Items []T `json:"items"`
	Total int `json:"total"`
}

// ipamClient 是 CMDB IPAM 只读客户端。
type ipamClient struct {
	baseURL string
	token   string
	http    *http.Client
}

func newIPAMClient(apiURL, token string) *ipamClient {
	return &ipamClient{
		baseURL: record.NormalizeBaseURL(apiURL),
		token:   token,
		http:    &http.Client{Timeout: 30 * time.Second},
	}
}

func (c *ipamClient) get(ctx context.Context, path string, out any) error {
	headers := map[string]string{}
	if c.token != "" {
		headers["Authorization"] = "Bearer " + c.token
	}
	return record.DoJSON(ctx, c.http, http.MethodGet, c.baseURL+path, headers, nil, out)
}

// listAll 分页拉满一个列表接口（page_size 上限 200，与 CMDB parsePage 约束一致）。
func listAll[T any](ctx context.Context, fetch func(page, pageSize int) (items []T, total int, err error)) ([]T, error) {
	const pageSize = 200
	var out []T
	for page := 1; ; page++ {
		items, total, err := fetch(page, pageSize)
		if err != nil {
			return nil, err
		}
		out = append(out, items...)
		if len(out) >= total || len(items) == 0 {
			return out, nil
		}
	}
}

// ListPrefixes 拉取全部 IPAM 前缀。
func (c *ipamClient) ListPrefixes(ctx context.Context) ([]ipamPrefix, error) {
	return listAll(ctx, func(page, pageSize int) ([]ipamPrefix, int, error) {
		var resp pagedResponse[ipamPrefix]
		err := c.get(ctx, fmt.Sprintf("/api/v1/ipam/prefixes?page=%d&page_size=%d", page, pageSize), &resp)
		return resp.Items, resp.Total, err
	})
}

// ListIPs 逐前缀拉取已登记 IP。
func (c *ipamClient) ListIPs(ctx context.Context, prefixID string) ([]ipamIP, error) {
	return listAll(ctx, func(page, pageSize int) ([]ipamIP, int, error) {
		var resp pagedResponse[ipamIP]
		err := c.get(ctx, fmt.Sprintf("/api/v1/ipam/ips?prefix_id=%s&page=%d&page_size=%d", prefixID, page, pageSize), &resp)
		return resp.Items, resp.Total, err
	})
}

// CIMAC 取登记 IP 关联 CI 的 MAC 属性（无 CI 或无 MAC 属性时返回空串）。
func (c *ipamClient) CIMAC(ctx context.Context, ciID string) (string, error) {
	var ci struct {
		Attributes map[string]any `json:"attributes"`
	}
	if err := c.get(ctx, "/api/v1/cis/"+ciID, &ci); err != nil {
		return "", err
	}
	return record.StrField(ci.Attributes, "mac", "mac_address"), nil
}

// normalizeMAC 归一化 MAC（大写、去分隔符）用于比对。
func normalizeMAC(s string) string {
	s = strings.ToUpper(strings.TrimSpace(s))
	return strings.NewReplacer(":", "", "-", "", ".", "").Replace(s)
}

// comparison 是一次 IPAM 比对的结果统计。
type comparison struct {
	registeredAlive int      // 已登记且存活（跳过）
	recycleLeads    []ipamIP // 已登记不存活（回收线索）
	blackRecords    []record.Record
	macAlerts       []string // MAC 变更告警行
}

// compareWithIPAM 把扫描到的在线主机与 IPAM 登记比对：
//   - 已登记且存活：跳过（计数）；
//   - 已登记不存活：记入回收线索清单；
//   - 未登记存活：生成 host 发现记录（black_device_risk=true）进发现池；
//   - 已登记且存活但 MAC 与关联 CI 不一致：生成 MAC 变更告警行。
func compareWithIPAM(ctx context.Context, client *ipamClient, alive []Host, at time.Time) (*comparison, error) {
	prefixes, err := client.ListPrefixes(ctx)
	if err != nil {
		return nil, fmt.Errorf("拉取 IPAM 前缀失败: %w", err)
	}
	registered := map[string]ipamIP{}
	for _, p := range prefixes {
		ips, err := client.ListIPs(ctx, p.ID)
		if err != nil {
			return nil, fmt.Errorf("拉取前缀 %s（%s）的登记 IP 失败: %w", p.CIDR, p.ID, err)
		}
		for _, ip := range ips {
			registered[ip.IP] = ip
		}
	}

	aliveSet := make(map[string]Host, len(alive))
	for _, h := range alive {
		aliveSet[h.IP] = h
	}

	cmp := &comparison{}
	// 已登记不存活 → 回收线索
	for ip, reg := range registered {
		if _, ok := aliveSet[ip]; !ok {
			cmp.recycleLeads = append(cmp.recycleLeads, reg)
		}
	}
	// 在线主机逐个归类
	for _, h := range alive {
		reg, ok := registered[h.IP]
		if !ok {
			// 未登记存活 → 黑设备发现记录
			attrs := map[string]any{
				"ip":                h.IP,
				"source":            Source,
				"last_seen_alive":   at.Format(time.RFC3339),
				"black_device_risk": true,
			}
			if h.MAC != "" {
				attrs["mac"] = h.MAC
			}
			if h.Hostname != "" {
				attrs["hostname"] = h.Hostname
			}
			cmp.blackRecords = append(cmp.blackRecords, record.Record{
				Source:         Source,
				Collector:      CollectorName,
				ModelCandidate: "host",
				Attributes:     attrs,
				OccurredAt:     at,
			})
			continue
		}
		// 已登记且存活 → 跳过；有 CI 且双方 MAC 齐全时核对 MAC 变更
		cmp.registeredAlive++
		if reg.CIID == "" || h.MAC == "" {
			continue
		}
		ciMAC, err := client.CIMAC(ctx, reg.CIID)
		if err != nil || ciMAC == "" {
			continue // CI 无 MAC 基线时无法比对，跳过
		}
		if normalizeMAC(ciMAC) != normalizeMAC(h.MAC) {
			cmp.macAlerts = append(cmp.macAlerts, fmt.Sprintf(
				"MAC 变更告警: ip=%s 登记MAC=%s（CI %s）实测MAC=%s", h.IP, ciMAC, reg.CIID, h.MAC))
		}
	}
	return cmp, nil
}
