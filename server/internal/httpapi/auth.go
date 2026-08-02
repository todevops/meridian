package httpapi

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"cmdb/server/internal/auth"
	"cmdb/server/internal/store"
)

type loginRequest struct {
	Username string `json:"username" binding:"required"`
	Password string `json:"password" binding:"required"`
}

// login 处理 POST /api/v1/auth/login：校验密码 → 签发 JWT → 写 httpOnly cookie。
func (s *Server) login(c *gin.Context) {
	var req loginRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		respondError(c, http.StatusBadRequest, CodeBadRequest, "username 与 password 均为必填", nil)
		return
	}
	var user store.User
	if err := s.db.Where("username = ?", req.Username).First(&user).Error; err != nil {
		// 用户不存在与密码错误返回同一信息，避免用户名枚举。
		respondError(c, http.StatusUnauthorized, auth.CodeUnauthorized, "用户名或密码错误", nil)
		return
	}
	if !auth.CheckPassword(user.PasswordHash, req.Password) {
		respondError(c, http.StatusUnauthorized, auth.CodeUnauthorized, "用户名或密码错误", nil)
		return
	}
	if user.Status != "active" {
		respondError(c, http.StatusUnauthorized, auth.CodeUnauthorized, "账号已被停用", nil)
		return
	}
	token, err := s.auth.Tokens.Issue(user.ID)
	if err != nil {
		respondError(c, http.StatusInternalServerError, CodeInternal, "签发会话令牌失败", nil)
		return
	}
	ttl := int(s.auth.Tokens.TTL().Seconds())
	c.SetSameSite(http.SameSiteLaxMode)
	c.SetCookie(auth.CookieName, token, ttl, "/", "", false, true)
	c.JSON(http.StatusOK, gin.H{"token": token, "user": s.currentUserPayload(&user)})
}

// logout 处理 POST /api/v1/auth/logout：清除会话 cookie（JWT 无状态，不做服务端吊销）。
func (s *Server) logout(c *gin.Context) {
	c.SetSameSite(http.SameSiteLaxMode)
	c.SetCookie(auth.CookieName, "", -1, "/", "", false, true)
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

// me 处理 GET /api/v1/auth/me：返回当前用户及其角色、权限点并集。
func (s *Server) me(c *gin.Context) {
	c.JSON(http.StatusOK, s.currentUserPayload(auth.CurrentUser(c)))
}

// currentUserPayload 组装 CurrentUser 响应体。
func (s *Server) currentUserPayload(user *store.User) gin.H {
	return gin.H{
		"id":           user.ID,
		"username":     user.Username,
		"display_name": user.DisplayName,
		"roles":        stringSlice(s.auth.UserRoleCodes(user.ID)),
		"permissions":  stringSlice(s.auth.UserPermissionCodes(user.ID)),
	}
}
