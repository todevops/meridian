// db_instance 模型调和键确保：GET 模型，reconcile_keys 缺失或不符则 PATCH。
package dbdiscover

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"slices"

	"collectors/internal/record"
)

// wantReconcileKeys 是 db_instance 模型应配置的有序调和键：
// 以原始 instance 标签（ip:port）为唯一主键——component_type/ip/port 任一单字段都无法唯一标识实例
// （同类型多实例、同机多服务场景），故取三者组合地址。
var wantReconcileKeys = []string{"instance_addr"}

// model 是 CMDB 模型（仅取本采集器关心的字段）。
type model struct {
	ID            string   `json:"id"`
	Code          string   `json:"code"`
	ReconcileKeys []string `json:"reconcile_keys"`
}

// modelListResponse 与契约 ModelListResponse 对应（仅取关心字段）。
type modelListResponse struct {
	Items []model `json:"items"`
}

// authHeaders 构造带 Bearer 令牌的请求头（无令牌时为 nil）。
func (c *Collector) authHeaders() map[string]string {
	if c.cmdbToken == "" {
		return nil
	}
	return map[string]string{"Authorization": "Bearer " + c.cmdbToken}
}

// ensureModel 确保 db_instance 模型配置了调和键 ["type","ip","port"]：
// 查询模型 → 键已一致则不动 → 不一致则 PATCH；模型不存在（应由种子数据创建）只告警不失败。
// dryRun 模式下只打印意图，不做任何写操作；查询失败降级为告警（便于离线 dry-run）。
func (c *Collector) ensureModel(ctx context.Context) error {
	u := c.apiURL + "/api/v1/models?keyword=" + url.QueryEscape("db_instance") + "&page_size=100"
	var list modelListResponse
	if err := record.DoJSON(ctx, c.client.http, http.MethodGet, u, c.authHeaders(), nil, &list); err != nil {
		if c.dryRun {
			c.logf("[dry-run] 查询 db_instance 模型失败（忽略，不影响记录打印）: %v", err)
			return nil
		}
		return fmt.Errorf("查询 db_instance 模型失败: %w", err)
	}
	var found *model
	for i := range list.Items {
		if list.Items[i].Code == "db_instance" {
			found = &list.Items[i]
			break
		}
	}
	if found == nil {
		c.logf("未找到 db_instance 模型（应由种子数据创建），跳过调和键检查")
		return nil
	}
	if slices.Equal(found.ReconcileKeys, wantReconcileKeys) {
		return nil
	}
	if c.dryRun {
		c.logf("[dry-run] db_instance 模型调和键为 %v，应 PATCH 为 %v（dry-run 跳过变更）", found.ReconcileKeys, wantReconcileKeys)
		return nil
	}
	patch := map[string]any{"reconcile_keys": wantReconcileKeys}
	if err := record.DoJSON(ctx, c.client.http, http.MethodPatch, c.apiURL+"/api/v1/models/"+found.ID, c.authHeaders(), patch, nil); err != nil {
		return fmt.Errorf("更新 db_instance 模型调和键失败: %w", err)
	}
	c.logf("已将 db_instance 模型调和键更新为 %v", wantReconcileKeys)
	return nil
}
