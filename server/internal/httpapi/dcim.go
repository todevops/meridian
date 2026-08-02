// DCIM（/api/v1/dcim）处理器：机柜 U 位视图与设备上下架。
package httpapi

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"

	"cmdb/server/internal/dcim"
)

// mountRequest 与 RackMountRequest 对应。
type mountRequest struct {
	CIID      string `json:"ci_id"`
	UPosition int    `json:"u_position"`
	UHeight   int    `json:"u_height"`
}

// unmountRequest 与 RackUnmountRequest 对应。
type unmountRequest struct {
	CIID string `json:"ci_id"`
}

// getRackUnits 处理 GET /api/v1/dcim/racks/{ci_id}/units：机柜 U 位占用总览。
func (s *Server) getRackUnits(c *gin.Context) {
	units, err := s.dcim.Units(c.Request.Context(), c.Param("ci_id"))
	if err != nil {
		respondDCIMError(c, err)
		return
	}
	c.JSON(http.StatusOK, units)
}

// mountRackUnit 处理 POST /api/v1/dcim/racks/{ci_id}/mount：U 位区间重叠 409。
func (s *Server) mountRackUnit(c *gin.Context) {
	var req mountRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		respondError(c, http.StatusBadRequest, CodeBadRequest, "请求体解析失败: "+err.Error(), nil)
		return
	}
	if req.CIID == "" {
		respondError(c, http.StatusBadRequest, CodeValidationFailed, "ci_id 为必填项", nil)
		return
	}
	mount, err := s.dcim.Mount(c.Request.Context(), c.Param("ci_id"), req.CIID, req.UPosition, req.UHeight)
	if err != nil {
		respondDCIMError(c, err)
		return
	}
	c.JSON(http.StatusOK, mount)
}

// unmountRackUnit 处理 POST /api/v1/dcim/racks/{ci_id}/unmount：设备下架。
func (s *Server) unmountRackUnit(c *gin.Context) {
	var req unmountRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		respondError(c, http.StatusBadRequest, CodeBadRequest, "请求体解析失败: "+err.Error(), nil)
		return
	}
	if req.CIID == "" {
		respondError(c, http.StatusBadRequest, CodeValidationFailed, "ci_id 为必填项", nil)
		return
	}
	if err := s.dcim.Unmount(c.Request.Context(), c.Param("ci_id"), req.CIID); err != nil {
		respondDCIMError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

// respondDCIMError 把 DCIM 业务错误映射为 HTTP 响应。
func respondDCIMError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, dcim.ErrRackNotFound):
		respondError(c, http.StatusNotFound, CodeNotFound, err.Error(), nil)
	case errors.Is(err, dcim.ErrOverlap), errors.Is(err, dcim.ErrAlreadyMounted):
		respondError(c, http.StatusConflict, CodeConflict, err.Error(), nil)
	case errors.Is(err, dcim.ErrNotRack), errors.Is(err, dcim.ErrDeviceNotFound),
		errors.Is(err, dcim.ErrInvalidRange), errors.Is(err, dcim.ErrNotMounted):
		respondError(c, http.StatusBadRequest, CodeBadRequest, err.Error(), nil)
	default:
		respondError(c, http.StatusInternalServerError, CodeInternal, err.Error(), nil)
	}
}
