// 发现记录与调和预览（/api/v1/discovery-records、/api/v1/reconcile/preview）处理器。
package httpapi

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"cmdb/server/internal/discovery"
	"cmdb/server/internal/reconcile"
)

// discoveryBatchRequest 与 DiscoveryRecordBatchRequest 对应。
type discoveryBatchRequest struct {
	Records []reconcile.Record `json:"records"`
}

// reconcilePreviewRequest 与 ReconcilePreviewRequest 对应。
type reconcilePreviewRequest struct {
	Record reconcile.Record `json:"record"`
}

// createDiscoveryRecords 处理 POST /api/v1/discovery-records：批量摄入发现记录。
func (s *Server) createDiscoveryRecords(c *gin.Context) {
	var req discoveryBatchRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		respondError(c, http.StatusBadRequest, CodeBadRequest, "请求体解析失败: "+err.Error(), nil)
		return
	}
	if len(req.Records) == 0 {
		respondError(c, http.StatusBadRequest, CodeValidationFailed, "records 至少包含一条发现记录", nil)
		return
	}
	result := s.pipeline.Ingest(c.Request.Context(), req.Records)
	c.JSON(http.StatusOK, result)
}

// previewReconcile 处理 POST /api/v1/reconcile/preview：调和规则演练，不实际落库。
func (s *Server) previewReconcile(c *gin.Context) {
	var req reconcilePreviewRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		respondError(c, http.StatusBadRequest, CodeBadRequest, "请求体解析失败: "+err.Error(), nil)
		return
	}
	if msg := discovery.ValidateRecord(req.Record); msg != "" {
		respondError(c, http.StatusBadRequest, CodeValidationFailed, msg, nil)
		return
	}
	decision, err := s.pipeline.Engine().Evaluate(c.Request.Context(), req.Record, true)
	if err != nil {
		respondError(c, http.StatusInternalServerError, CodeInternal, "调和预览失败: "+err.Error(), nil)
		return
	}
	resp := gin.H{
		"action":  decision.Action,
		"reasons": decision.Reasons,
	}
	if decision.MatchedCIID != "" {
		resp["matched_ci_id"] = decision.MatchedCIID
	} else {
		resp["matched_ci_id"] = nil
	}
	c.JSON(http.StatusOK, resp)
}
