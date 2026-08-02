package httpapi

import (
	"errors"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"cmdb/server/internal/auth"
	"cmdb/server/internal/store"
)

type roleCreateRequest struct {
	Code        string   `json:"code" binding:"required"`
	Name        string   `json:"name" binding:"required"`
	Description string   `json:"description"`
	Permissions []string `json:"permissions"`
}

type rolePatchRequest struct {
	Name        *string   `json:"name"`
	Description *string   `json:"description"`
	Permissions *[]string `json:"permissions"`
}

// rolePayload 组装 Role 响应体（权限与用户数来自 Casbin 策略）。
func (s *Server) rolePayload(role *store.Role) gin.H {
	return gin.H{
		"id":          role.ID,
		"code":        role.Code,
		"name":        role.Name,
		"description": role.Description,
		"permissions": stringSlice(s.auth.RolePermissionCodes(role.Code)),
		"user_count":  s.auth.RoleUserCount(role.Code),
		"is_builtin":  role.IsBuiltin,
		"created_at":  role.CreatedAt,
		"updated_at":  role.UpdatedAt,
	}
}

// validatePermissionCodes 校验权限点编码全部在目录内，返回错误信息（空串表示通过）。
func validatePermissionCodes(codes []string) string {
	for _, code := range codes {
		if !auth.ValidPermission(code) {
			return "非法权限点: " + code
		}
	}
	return ""
}

// listRoles 处理 GET /api/v1/roles。
func (s *Server) listRoles(c *gin.Context) {
	var roles []store.Role
	if err := s.db.Order("created_at ASC").Find(&roles).Error; err != nil {
		respondError(c, http.StatusInternalServerError, CodeInternal, "查询角色列表失败", nil)
		return
	}
	items := make([]gin.H, 0, len(roles))
	for i := range roles {
		items = append(items, s.rolePayload(&roles[i]))
	}
	c.JSON(http.StatusOK, gin.H{"items": items})
}

// createRole 处理 POST /api/v1/roles。
func (s *Server) createRole(c *gin.Context) {
	var req roleCreateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		respondError(c, http.StatusBadRequest, CodeBadRequest, "请求体不合法: "+err.Error(), nil)
		return
	}
	req.Code = strings.TrimSpace(req.Code)
	if msg := validatePermissionCodes(req.Permissions); msg != "" {
		respondError(c, http.StatusBadRequest, CodeBadRequest, msg, nil)
		return
	}
	var count int64
	if err := s.db.Model(&store.Role{}).Where("code = ?", req.Code).Count(&count).Error; err != nil {
		respondError(c, http.StatusInternalServerError, CodeInternal, "查询角色失败", nil)
		return
	}
	if count > 0 {
		respondError(c, http.StatusBadRequest, CodeBadRequest, "角色编码已存在: "+req.Code, nil)
		return
	}
	role := store.Role{
		Code:        req.Code,
		Name:        strings.TrimSpace(req.Name),
		Description: req.Description,
	}
	if err := s.db.Create(&role).Error; err != nil {
		respondError(c, http.StatusInternalServerError, CodeInternal, "创建角色失败", nil)
		return
	}
	if err := s.auth.SetRolePermissions(role.Code, req.Permissions); err != nil {
		respondError(c, http.StatusInternalServerError, CodeInternal, "写入角色权限失败", nil)
		return
	}
	c.JSON(http.StatusOK, s.rolePayload(&role))
}

// getRole 处理 GET /api/v1/roles/:role_id。
func (s *Server) getRole(c *gin.Context) {
	role, ok := s.findRole(c)
	if !ok {
		return
	}
	c.JSON(http.StatusOK, s.rolePayload(role))
}

// patchRole 处理 PATCH /api/v1/roles/:role_id。
// code 不可修改；内置 admin 角色的权限点不可修改（防止把管理员锁死）。
func (s *Server) patchRole(c *gin.Context) {
	role, ok := s.findRole(c)
	if !ok {
		return
	}
	var req rolePatchRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		respondError(c, http.StatusBadRequest, CodeBadRequest, "请求体不合法: "+err.Error(), nil)
		return
	}
	updates := map[string]any{}
	if req.Name != nil {
		updates["name"] = strings.TrimSpace(*req.Name)
	}
	if req.Description != nil {
		updates["description"] = *req.Description
	}
	if req.Permissions != nil {
		if role.Code == "admin" {
			respondError(c, http.StatusBadRequest, CodeBadRequest, "内置 admin 角色的权限点不可修改", nil)
			return
		}
		if msg := validatePermissionCodes(*req.Permissions); msg != "" {
			respondError(c, http.StatusBadRequest, CodeBadRequest, msg, nil)
			return
		}
	}
	if len(updates) > 0 {
		if err := s.db.Model(role).Updates(updates).Error; err != nil {
			respondError(c, http.StatusInternalServerError, CodeInternal, "更新角色失败", nil)
			return
		}
	}
	if req.Permissions != nil {
		if err := s.auth.SetRolePermissions(role.Code, *req.Permissions); err != nil {
			respondError(c, http.StatusInternalServerError, CodeInternal, "写入角色权限失败", nil)
			return
		}
	}
	if err := s.db.First(role, "id = ?", role.ID).Error; err != nil {
		respondError(c, http.StatusInternalServerError, CodeInternal, "查询角色失败", nil)
		return
	}
	c.JSON(http.StatusOK, s.rolePayload(role))
}

// deleteRole 处理 DELETE /api/v1/roles/:role_id。
// 内置角色不可删除；仍有用户关联的角色不可删除。
func (s *Server) deleteRole(c *gin.Context) {
	role, ok := s.findRole(c)
	if !ok {
		return
	}
	if role.IsBuiltin {
		respondError(c, http.StatusBadRequest, CodeBadRequest, "内置角色不可删除", nil)
		return
	}
	if n := s.auth.RoleUserCount(role.Code); n > 0 {
		respondError(c, http.StatusBadRequest, CodeBadRequest, "角色仍关联用户，不可删除", nil)
		return
	}
	if err := s.auth.DeleteRolePolicies(role.Code); err != nil {
		respondError(c, http.StatusInternalServerError, CodeInternal, "清除角色策略失败", nil)
		return
	}
	if err := s.db.Delete(role).Error; err != nil {
		respondError(c, http.StatusInternalServerError, CodeInternal, "删除角色失败", nil)
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

// listPermissions 处理 GET /api/v1/permissions：返回权限点目录（登录即可读）。
func (s *Server) listPermissions(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"items": auth.Catalog})
}

// findRole 按路径参数 :role_id 加载角色；不存在时写 404 并返回 ok=false。
func (s *Server) findRole(c *gin.Context) (*store.Role, bool) {
	var role store.Role
	if err := s.db.First(&role, "id = ?", c.Param("role_id")).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			respondError(c, http.StatusNotFound, CodeNotFound, "角色不存在", nil)
			return nil, false
		}
		respondError(c, http.StatusInternalServerError, CodeInternal, "查询角色失败", nil)
		return nil, false
	}
	return &role, true
}
