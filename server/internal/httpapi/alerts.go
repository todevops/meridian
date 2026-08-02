// 告警事件（/api/v1/alerts）处理器：列表查询与确认（ack）。
package httpapi

import (
	"fmt"
	"net/http"

	"github.com/gin-gonic/gin"

	"meridian/server/internal/store"
)

// alertView 把告警事件投影为契约形状。
func alertView(a store.AlertEvent) gin.H {
	return gin.H{
		"id":           a.ID,
		"level":        a.Level,
		"title":        a.Title,
		"source":       a.Source,
		"ci_id":        a.CIID,
		"detail":       a.Detail,
		"acknowledged": a.Acknowledged,
		"created_at":   a.CreatedAt,
	}
}

// listAlerts 处理 GET /api/v1/alerts：acknowledged 过滤 + 分页，最新在前。
func (s *Server) listAlerts(c *gin.Context) {
	page, pageSize, ok := parsePage(c)
	if !ok {
		return
	}
	q := s.db.Model(&store.AlertEvent{})
	if ack := c.Query("acknowledged"); ack != "" {
		if ack != "true" && ack != "false" {
			respondError(c, http.StatusBadRequest, CodeBadRequest,
				fmt.Sprintf("acknowledged 取值 %q 非法（true/false）", ack), nil)
			return
		}
		q = q.Where("acknowledged = ?", ack == "true")
	}
	var total int64
	if err := q.Count(&total).Error; err != nil {
		respondError(c, http.StatusInternalServerError, CodeInternal, "查询告警总数失败", nil)
		return
	}
	var rows []store.AlertEvent
	if err := q.Order("created_at DESC").Offset((page - 1) * pageSize).Limit(pageSize).Find(&rows).Error; err != nil {
		respondError(c, http.StatusInternalServerError, CodeInternal, "查询告警列表失败", nil)
		return
	}
	items := []gin.H{}
	for _, row := range rows {
		items = append(items, alertView(row))
	}
	c.JSON(http.StatusOK, gin.H{"items": items, "total": total, "page": page, "page_size": pageSize})
}

// ackAlert 处理 POST /api/v1/alerts/{alert_id}/ack：确认告警（幂等，重复 ack 返回当前状态）。
func (s *Server) ackAlert(c *gin.Context) {
	var alert store.AlertEvent
	if err := s.db.First(&alert, "id = ?", c.Param("alert_id")).Error; err != nil {
		if isNotFound(err) {
			respondError(c, http.StatusNotFound, CodeNotFound, "告警事件不存在", nil)
			return
		}
		respondError(c, http.StatusInternalServerError, CodeInternal, "查询告警事件失败", nil)
		return
	}
	if !alert.Acknowledged {
		if err := s.db.Model(&store.AlertEvent{}).Where("id = ?", alert.ID).
			Update("acknowledged", true).Error; err != nil {
			respondError(c, http.StatusInternalServerError, CodeInternal, "确认告警失败", nil)
			return
		}
		alert.Acknowledged = true
	}
	c.JSON(http.StatusOK, alertView(alert))
}
