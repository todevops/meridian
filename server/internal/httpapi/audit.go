// 审计日志查询（/api/v1/audit）处理器（F-004）。
package httpapi

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"meridian/server/internal/store"
)

// listAuditLogs 处理 GET /api/v1/audit：ci_id/operator/source 过滤 + 倒序分页。
func (s *Server) listAuditLogs(c *gin.Context) {
	page, pageSize, ok := parsePage(c)
	if !ok {
		return
	}
	q := s.db.Model(&store.AuditLog{})
	if ciID := c.Query("ci_id"); ciID != "" {
		q = q.Where("ci_id = ?", ciID)
	}
	if operator := c.Query("operator"); operator != "" {
		if operator == "system" {
			// 调和引擎等系统通道的历史写入 operator 为空串，归一按 system 匹配。
			q = q.Where("operator = ? OR operator = ''", operator)
		} else {
			q = q.Where("operator = ?", operator)
		}
	}
	if source := c.Query("source"); source != "" {
		q = q.Where("source = ?", source)
	}
	var total int64
	if err := q.Count(&total).Error; err != nil {
		respondError(c, http.StatusInternalServerError, CodeInternal, "查询审计日志总数失败", nil)
		return
	}
	var rows []store.AuditLog
	if err := q.Order("created_at DESC").Offset((page - 1) * pageSize).Limit(pageSize).Find(&rows).Error; err != nil {
		respondError(c, http.StatusInternalServerError, CodeInternal, "查询审计日志失败", nil)
		return
	}
	items := []gin.H{}
	for _, r := range rows {
		operator := r.Operator
		if operator == "" {
			operator = "system" // 调和引擎等系统通道的历史写入无操作者
		}
		items = append(items, gin.H{
			"id":         r.ID,
			"ci_id":      r.CIID,
			"action":     r.Action,
			"source":     r.Source,
			"operator":   operator,
			"changes":    r.Changes,
			"message":    r.Message,
			"created_at": r.CreatedAt,
		})
	}
	c.JSON(http.StatusOK, gin.H{"items": items, "total": total, "page": page, "page_size": pageSize})
}
