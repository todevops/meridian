// 发现记录管道客户端：标准 DiscoveryRecord 批量上报与模型调和键确保。
// 对应服务端 POST /api/v1/discovery-records（契约 DiscoveryRecordBatchRequest/Response）
// 与 GET/PATCH /api/v1/models/{code}（reconcile_keys 字段）。
package cmdb

import (
	"context"
	"fmt"
	"net/http"
	"time"
)

// DiscoveryRecord 是标准发现记录，与服务端 reconcile.Record / openapi DiscoveryRecord 对应。
type DiscoveryRecord struct {
	Source         string         `json:"source"`          // 发现来源系统
	Collector      string         `json:"collector"`       // 采集器标识
	ModelCandidate string         `json:"model_candidate"` // 候选模型编码
	Attributes     map[string]any `json:"attributes"`      // 原始属性键值对
	OccurredAt     time.Time      `json:"occurred_at"`     // 采集发生时间
}

// RecordError 描述一条被服务端拒绝的记录。
type RecordError struct {
	Index   int    `json:"index"`
	Message string `json:"message"`
}

// IngestResult 是批量摄入结果，与服务端 discovery.IngestResult 对应。
type IngestResult struct {
	Accepted int           `json:"accepted"`
	Rejected int           `json:"rejected"`
	Errors   []RecordError `json:"errors"`
}

// IngestRecords 批量上报发现记录（POST /api/v1/discovery-records）。
// 调用方负责分批、限速与 429/5xx 重试；本方法只做单次请求。
func (c *Client) IngestRecords(ctx context.Context, records []DiscoveryRecord) (IngestResult, error) {
	var result IngestResult
	reqBody := map[string]any{"records": records}
	if err := c.do(ctx, http.MethodPost, "/api/v1/discovery-records", reqBody, &result); err != nil {
		return IngestResult{}, err
	}
	return result, nil
}

// modelView 为 GET /api/v1/models/{code} 响应中迁移关心的字段。
type modelView struct {
	ReconcileKeys []string `json:"reconcile_keys"`
}

// GetReconcileKeys 读取模型当前调和键（模型不存在时返回 404 APIError）。
func (c *Client) GetReconcileKeys(ctx context.Context, modelCode string) ([]string, error) {
	var view modelView
	if err := c.do(ctx, http.MethodGet, "/api/v1/models/"+modelCode, nil, &view); err != nil {
		return nil, fmt.Errorf("查询模型 %q 调和键失败: %w", modelCode, err)
	}
	return view.ReconcileKeys, nil
}

// PatchReconcileKeys 整体替换模型调和键（PATCH /api/v1/models/{code}）。
func (c *Client) PatchReconcileKeys(ctx context.Context, modelCode string, keys []string) error {
	reqBody := map[string]any{"reconcile_keys": keys}
	if err := c.do(ctx, http.MethodPatch, "/api/v1/models/"+modelCode, reqBody, nil); err != nil {
		return fmt.Errorf("更新模型 %q 调和键失败: %w", modelCode, err)
	}
	return nil
}

// EnsureReconcileKey 确保 key 位于模型调和键链首位（幂等）。
// 迁移记录携带 netbox_id 留痕，以其为主调和键可保证重复执行时命中存量 CI（update 而非重复建档）。
// 返回 changed 表示本次是否发生了 PATCH。
func (c *Client) EnsureReconcileKey(ctx context.Context, modelCode, key string) (changed bool, err error) {
	keys, err := c.GetReconcileKeys(ctx, modelCode)
	if err != nil {
		return false, err
	}
	if len(keys) > 0 && keys[0] == key {
		return false, nil
	}
	merged := []string{key}
	for _, k := range keys {
		if k != key {
			merged = append(merged, k)
		}
	}
	if err := c.PatchReconcileKeys(ctx, modelCode, merged); err != nil {
		return false, err
	}
	return true, nil
}
