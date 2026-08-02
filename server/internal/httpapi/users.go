package httpapi

import (
	"errors"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"meridian/server/internal/auth"
	"meridian/server/internal/store"
)

type userCreateRequest struct {
	Username    string   `json:"username" binding:"required,min=2"`
	DisplayName string   `json:"display_name" binding:"required"`
	Password    string   `json:"password" binding:"required,min=6"`
	Roles       []string `json:"roles"`
}

type userPatchRequest struct {
	DisplayName *string   `json:"display_name"`
	Status      *string   `json:"status"`
	Password    *string   `json:"password"`
	Roles       *[]string `json:"roles"`
}

// userPayload 组装 User 响应体（角色来自 Casbin 分组策略）。
func (s *Server) userPayload(user *store.User) gin.H {
	return gin.H{
		"id":           user.ID,
		"username":     user.Username,
		"display_name": user.DisplayName,
		"status":       user.Status,
		"roles":        stringSlice(s.auth.UserRoleCodes(user.ID)),
		"is_builtin":   user.IsBuiltin,
		"created_at":   user.CreatedAt,
		"updated_at":   user.UpdatedAt,
	}
}

// validateRoleCodes 校验角色编码全部存在，返回错误信息（空串表示通过）。
func (s *Server) validateRoleCodes(codes []string) string {
	for _, code := range codes {
		var count int64
		if err := s.db.Model(&store.Role{}).Where("code = ?", code).Count(&count).Error; err != nil || count == 0 {
			return "角色不存在: " + code
		}
	}
	return ""
}

// listUsers 处理 GET /api/v1/users：关键字过滤 + 分页。
func (s *Server) listUsers(c *gin.Context) {
	page, pageSize, ok := parsePage(c)
	if !ok {
		return
	}
	query := s.db.Model(&store.User{})
	if kw := strings.TrimSpace(c.Query("keyword")); kw != "" {
		like := "%" + kw + "%"
		query = query.Where("username LIKE ? OR display_name LIKE ?", like, like)
	}
	var total int64
	if err := query.Count(&total).Error; err != nil {
		respondError(c, http.StatusInternalServerError, CodeInternal, "查询用户总数失败", nil)
		return
	}
	var users []store.User
	if err := query.Order("created_at ASC").Offset((page - 1) * pageSize).Limit(pageSize).Find(&users).Error; err != nil {
		respondError(c, http.StatusInternalServerError, CodeInternal, "查询用户列表失败", nil)
		return
	}
	items := make([]gin.H, 0, len(users))
	for i := range users {
		items = append(items, s.userPayload(&users[i]))
	}
	c.JSON(http.StatusOK, gin.H{"items": items, "total": total, "page": page, "page_size": pageSize})
}

// createUser 处理 POST /api/v1/users。
func (s *Server) createUser(c *gin.Context) {
	var req userCreateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		respondError(c, http.StatusBadRequest, CodeBadRequest, "请求体不合法: "+err.Error(), nil)
		return
	}
	req.Username = strings.TrimSpace(req.Username)
	var count int64
	if err := s.db.Model(&store.User{}).Where("username = ?", req.Username).Count(&count).Error; err != nil {
		respondError(c, http.StatusInternalServerError, CodeInternal, "查询用户失败", nil)
		return
	}
	if count > 0 {
		respondError(c, http.StatusBadRequest, CodeBadRequest, "用户名已存在: "+req.Username, nil)
		return
	}
	if msg := s.validateRoleCodes(req.Roles); msg != "" {
		respondError(c, http.StatusBadRequest, CodeBadRequest, msg, nil)
		return
	}
	hash, err := auth.HashPassword(req.Password)
	if err != nil {
		respondError(c, http.StatusInternalServerError, CodeInternal, "生成密码哈希失败", nil)
		return
	}
	user := store.User{
		Username:     req.Username,
		DisplayName:  strings.TrimSpace(req.DisplayName),
		PasswordHash: hash,
		Status:       "active",
	}
	if err := s.db.Create(&user).Error; err != nil {
		respondError(c, http.StatusInternalServerError, CodeInternal, "创建用户失败", nil)
		return
	}
	if err := s.auth.SetUserRoles(user.ID, req.Roles); err != nil {
		respondError(c, http.StatusInternalServerError, CodeInternal, "分配角色失败", nil)
		return
	}
	c.JSON(http.StatusOK, s.userPayload(&user))
}

// getUser 处理 GET /api/v1/users/:user_id。
func (s *Server) getUser(c *gin.Context) {
	user, ok := s.findUser(c)
	if !ok {
		return
	}
	c.JSON(http.StatusOK, s.userPayload(user))
}

// patchUser 处理 PATCH /api/v1/users/:user_id。
// 内置账号（admin/collector）不允许停用、不允许改角色。
func (s *Server) patchUser(c *gin.Context) {
	user, ok := s.findUser(c)
	if !ok {
		return
	}
	var req userPatchRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		respondError(c, http.StatusBadRequest, CodeBadRequest, "请求体不合法: "+err.Error(), nil)
		return
	}
	updates := map[string]any{}
	if req.DisplayName != nil {
		updates["display_name"] = strings.TrimSpace(*req.DisplayName)
	}
	if req.Status != nil {
		if *req.Status != "active" && *req.Status != "disabled" {
			respondError(c, http.StatusBadRequest, CodeBadRequest, "status 仅支持 active/disabled", nil)
			return
		}
		if user.IsBuiltin && *req.Status != "active" {
			respondError(c, http.StatusBadRequest, CodeBadRequest, "内置账号不允许停用", nil)
			return
		}
		updates["status"] = *req.Status
	}
	if req.Password != nil {
		if len(*req.Password) < 6 {
			respondError(c, http.StatusBadRequest, CodeBadRequest, "密码长度至少 6 位", nil)
			return
		}
		hash, err := auth.HashPassword(*req.Password)
		if err != nil {
			respondError(c, http.StatusInternalServerError, CodeInternal, "生成密码哈希失败", nil)
			return
		}
		updates["password_hash"] = hash
	}
	if req.Roles != nil {
		if user.IsBuiltin {
			respondError(c, http.StatusBadRequest, CodeBadRequest, "内置账号不允许修改角色", nil)
			return
		}
		if msg := s.validateRoleCodes(*req.Roles); msg != "" {
			respondError(c, http.StatusBadRequest, CodeBadRequest, msg, nil)
			return
		}
	}
	if len(updates) > 0 {
		if err := s.db.Model(user).Updates(updates).Error; err != nil {
			respondError(c, http.StatusInternalServerError, CodeInternal, "更新用户失败", nil)
			return
		}
	}
	if req.Roles != nil {
		if err := s.auth.SetUserRoles(user.ID, *req.Roles); err != nil {
			respondError(c, http.StatusInternalServerError, CodeInternal, "分配角色失败", nil)
			return
		}
	}
	// 重新加载，保证返回最新字段。
	if err := s.db.First(user, "id = ?", user.ID).Error; err != nil {
		respondError(c, http.StatusInternalServerError, CodeInternal, "查询用户失败", nil)
		return
	}
	c.JSON(http.StatusOK, s.userPayload(user))
}

// findUser 按路径参数 :user_id 加载用户；不存在时写 404 并返回 ok=false。
func (s *Server) findUser(c *gin.Context) (*store.User, bool) {
	var user store.User
	if err := s.db.First(&user, "id = ?", c.Param("user_id")).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			respondError(c, http.StatusNotFound, CodeNotFound, "用户不存在", nil)
			return nil, false
		}
		respondError(c, http.StatusInternalServerError, CodeInternal, "查询用户失败", nil)
		return nil, false
	}
	return &user, true
}
