// IPAM（/api/v1/ipam）处理器：前缀树管理、IP 登记与分配、利用率统计。
package httpapi

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"

	"cmdb/server/internal/ipam"
	"cmdb/server/internal/store"
)

// prefixCreateRequest 与 IPPrefixCreateRequest 对应。
type prefixCreateRequest struct {
	CIDR        string  `json:"cidr"`
	Name        string  `json:"name"`
	VlanID      *int    `json:"vlan_id"`
	Description string  `json:"description"`
	ParentID    *string `json:"parent_id"`
}

// allocateRequest 与 IPAllocateRequest 对应。
type allocateRequest struct {
	Count       int    `json:"count"`
	Description string `json:"description"`
}

// ipCreateRequest 与 IPCreateRequest 对应。
type ipCreateRequest struct {
	PrefixID    string `json:"prefix_id"`
	IP          string `json:"ip"`
	Status      string `json:"status"`
	CIID        string `json:"ci_id"`
	Description string `json:"description"`
}

// ipPatchRequest 与 IPPatchRequest 对应：全字段可选。
type ipPatchRequest struct {
	Status      *string `json:"status"`
	CIID        *string `json:"ci_id"`
	Description *string `json:"description"`
}

// prefixView 把前缀实体投影为契约形状（附利用率）。
func (s *Server) prefixView(c *gin.Context, prefix store.IPPrefix) (gin.H, bool) {
	util, err := s.ipam.UtilizationOf(c.Request.Context(), prefix)
	if err != nil {
		respondError(c, http.StatusInternalServerError, CodeInternal, "计算利用率失败", nil)
		return nil, false
	}
	return gin.H{
		"id":          prefix.ID,
		"cidr":        prefix.CIDR,
		"name":        prefix.Name,
		"vlan_id":     prefix.VlanID,
		"description": prefix.Description,
		"parent_id":   prefix.ParentID,
		"utilization": util,
		"created_at":  prefix.CreatedAt,
		"updated_at":  prefix.UpdatedAt,
	}, true
}

// createPrefix 处理 POST /api/v1/ipam/prefixes：CIDR 非法 400、同级重叠 409。
func (s *Server) createPrefix(c *gin.Context) {
	var req prefixCreateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		respondError(c, http.StatusBadRequest, CodeBadRequest, "请求体解析失败: "+err.Error(), nil)
		return
	}
	prefix, err := s.ipam.CreatePrefix(c.Request.Context(), ipam.CreatePrefixInput{
		CIDR:        req.CIDR,
		Name:        req.Name,
		VlanID:      req.VlanID,
		Description: req.Description,
		ParentID:    req.ParentID,
	})
	if err != nil {
		respondIPAMError(c, err)
		return
	}
	view, ok := s.prefixView(c, prefix)
	if !ok {
		return
	}
	c.JSON(http.StatusOK, view)
}

// listPrefixes 处理 GET /api/v1/ipam/prefixes：keyword 过滤 + 分页，每项含利用率。
func (s *Server) listPrefixes(c *gin.Context) {
	page, pageSize, ok := parsePage(c)
	if !ok {
		return
	}
	rows, total, err := s.ipam.ListPrefixes(c.Request.Context(), c.Query("keyword"), page, pageSize)
	if err != nil {
		respondError(c, http.StatusInternalServerError, CodeInternal, err.Error(), nil)
		return
	}
	items := []gin.H{}
	for _, row := range rows {
		view, ok := s.prefixView(c, row)
		if !ok {
			return
		}
		items = append(items, view)
	}
	c.JSON(http.StatusOK, gin.H{"items": items, "total": total, "page": page, "page_size": pageSize})
}

// getPrefix 处理 GET /api/v1/ipam/prefixes/{prefix_id}：含直接子前缀。
func (s *Server) getPrefix(c *gin.Context) {
	prefix, children, err := s.ipam.GetPrefix(c.Request.Context(), c.Param("prefix_id"))
	if err != nil {
		respondIPAMError(c, err)
		return
	}
	view, ok := s.prefixView(c, prefix)
	if !ok {
		return
	}
	childViews := []gin.H{}
	for _, child := range children {
		cv, ok := s.prefixView(c, child)
		if !ok {
			return
		}
		childViews = append(childViews, cv)
	}
	view["children"] = childViews
	c.JSON(http.StatusOK, view)
}

// allocateIPs 处理 POST /api/v1/ipam/prefixes/{prefix_id}/allocate：顺序分配首个空闲起的 IP。
func (s *Server) allocateIPs(c *gin.Context) {
	var req allocateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		respondError(c, http.StatusBadRequest, CodeBadRequest, "请求体解析失败: "+err.Error(), nil)
		return
	}
	ips, err := s.ipam.Allocate(c.Request.Context(), c.Param("prefix_id"), req.Count, req.Description)
	if err != nil {
		respondIPAMError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"items": ips})
}

// createIP 处理 POST /api/v1/ipam/ips：重复 409、不在前缀内 400。
func (s *Server) createIP(c *gin.Context) {
	var req ipCreateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		respondError(c, http.StatusBadRequest, CodeBadRequest, "请求体解析失败: "+err.Error(), nil)
		return
	}
	if req.PrefixID == "" || req.IP == "" {
		respondError(c, http.StatusBadRequest, CodeValidationFailed, "prefix_id 与 ip 为必填项", nil)
		return
	}
	// 关联 CI 显式校验，避免悬空引用。
	if req.CIID != "" {
		if _, ok := s.resolveCI(c, req.CIID); !ok {
			return
		}
	}
	ip, err := s.ipam.CreateIP(c.Request.Context(), ipam.CreateIPInput{
		PrefixID:    req.PrefixID,
		IP:          req.IP,
		Status:      req.Status,
		CIID:        req.CIID,
		Description: req.Description,
	})
	if err != nil {
		respondIPAMError(c, err)
		return
	}
	c.JSON(http.StatusOK, ip)
}

// listIPs 处理 GET /api/v1/ipam/ips：prefix_id/status/keyword 过滤 + 分页。
func (s *Server) listIPs(c *gin.Context) {
	page, pageSize, ok := parsePage(c)
	if !ok {
		return
	}
	items, total, err := s.ipam.ListIPs(c.Request.Context(),
		c.Query("prefix_id"), c.Query("status"), c.Query("keyword"), page, pageSize)
	if err != nil {
		respondIPAMError(c, err)
		return
	}
	if items == nil {
		items = []store.IPAddress{}
	}
	c.JSON(http.StatusOK, gin.H{"items": items, "total": total, "page": page, "page_size": pageSize})
}

// patchIP 处理 PATCH /api/v1/ipam/ips/{ip_id}：更新状态/关联 CI/描述。
func (s *Server) patchIP(c *gin.Context) {
	var req ipPatchRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		respondError(c, http.StatusBadRequest, CodeBadRequest, "请求体解析失败: "+err.Error(), nil)
		return
	}
	if req.CIID != nil && *req.CIID != "" {
		if _, ok := s.resolveCI(c, *req.CIID); !ok {
			return
		}
	}
	ip, err := s.ipam.PatchIP(c.Request.Context(), c.Param("ip_id"), ipam.PatchIPInput{
		Status:      req.Status,
		CIID:        req.CIID,
		Description: req.Description,
	})
	if err != nil {
		respondIPAMError(c, err)
		return
	}
	c.JSON(http.StatusOK, ip)
}

// respondIPAMError 把 IPAM 业务错误映射为 HTTP 响应。
func respondIPAMError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, ipam.ErrPrefixNotFound), errors.Is(err, ipam.ErrIPNotFound):
		respondError(c, http.StatusNotFound, CodeNotFound, err.Error(), nil)
	case errors.Is(err, ipam.ErrOverlap), errors.Is(err, ipam.ErrDuplicateIP), errors.Is(err, ipam.ErrInsufficientIPs):
		respondError(c, http.StatusConflict, CodeConflict, err.Error(), nil)
	case errors.Is(err, ipam.ErrInvalidCIDR), errors.Is(err, ipam.ErrParentNotFound),
		errors.Is(err, ipam.ErrNotContained), errors.Is(err, ipam.ErrInvalidIP),
		errors.Is(err, ipam.ErrIPNotInPrefix), errors.Is(err, ipam.ErrInvalidStatus),
		errors.Is(err, ipam.ErrInvalidCount), errors.Is(err, ipam.ErrInvalidVLAN):
		respondError(c, http.StatusBadRequest, CodeBadRequest, err.Error(), nil)
	default:
		respondError(c, http.StatusInternalServerError, CodeInternal, err.Error(), nil)
	}
}
