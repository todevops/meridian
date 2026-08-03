// 数据质量看板（/api/v1/dashboard/quality）处理器（F-080）。
package httpapi

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"meridian/server/internal/metrics"
)

// getQualityDashboard 处理 GET /api/v1/dashboard/quality：全模型五指标汇总 + 监控双指标。
func (s *Server) getQualityDashboard(c *gin.Context) {
	report, err := metrics.NewEngine(s.db, s.n9eClient).Quality(c.Request.Context())
	if err != nil {
		respondError(c, http.StatusInternalServerError, CodeInternal, "计算质量指标失败: "+err.Error(), nil)
		return
	}
	c.JSON(http.StatusOK, report)
}

// getQualityDrilldown 处理 GET /api/v1/dashboard/quality/drilldown：
// 按 model_id（ID 或编码）+ metric 下钻缺失 CI 清单（标准分页）。
func (s *Server) getQualityDrilldown(c *gin.Context) {
	page, pageSize, ok := parsePage(c)
	if !ok {
		return
	}
	modelFilter := c.Query("model_id")
	if modelFilter == "" {
		respondError(c, http.StatusBadRequest, CodeBadRequest, "model_id 为必填参数", nil)
		return
	}
	model, ok := s.resolveModel(c, modelFilter)
	if !ok {
		return
	}
	metric := c.Query("metric")
	if !metrics.DrillMetrics[metric] {
		respondError(c, http.StatusBadRequest, CodeBadRequest,
			"metric 取值非法（completeness/relation_completeness/orphan/stale/no_heartbeat）", nil)
		return
	}
	items, total, err := metrics.NewEngine(s.db, s.n9eClient).
		Drilldown(c.Request.Context(), model, metric, page, pageSize)
	if err != nil {
		respondError(c, http.StatusInternalServerError, CodeInternal, "查询下钻清单失败: "+err.Error(), nil)
		return
	}
	c.JSON(http.StatusOK, gin.H{"items": items, "total": total, "page": page, "page_size": pageSize})
}
