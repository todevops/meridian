// DCIM 容量总览（/api/v1/dcim/overview）处理器。
package httpapi

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// getDCIMOverview 处理 GET /api/v1/dcim/overview：按机房聚合 U 位/电力容量。
func (s *Server) getDCIMOverview(c *gin.Context) {
	overview, err := s.dcim.Overview(c.Request.Context())
	if err != nil {
		respondError(c, http.StatusInternalServerError, CodeInternal, err.Error(), nil)
		return
	}
	c.JSON(http.StatusOK, overview)
}
