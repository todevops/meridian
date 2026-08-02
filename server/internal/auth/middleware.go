package auth

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"meridian/server/internal/store"
)

// 错误码（与 httpapi 保持一致，此处独立定义避免包间循环依赖）。
const (
	CodeUnauthorized = "UNAUTHORIZED"
	CodeForbidden    = "FORBIDDEN"
)

// CookieName 是会话 cookie 名（与契约 cookieAuth 一致）。
const CookieName = "meridian_token"

const contextUserKey = "auth_user"

// abortError 按契约 Error schema 终止请求。
func abortError(c *gin.Context, status int, code, message string) {
	c.AbortWithStatusJSON(status, gin.H{"code": code, "message": message})
}

// tokenFromRequest 依次从 cookie、Authorization Bearer 提取令牌。
func tokenFromRequest(c *gin.Context) string {
	if token, err := c.Cookie(CookieName); err == nil && token != "" {
		return token
	}
	h := c.GetHeader("Authorization")
	if token, ok := strings.CutPrefix(h, "Bearer "); ok {
		return token
	}
	return ""
}

// CurrentUser 从 gin context 取已认证用户；未认证时返回 nil。
func CurrentUser(c *gin.Context) *store.User {
	if v, ok := c.Get(contextUserKey); ok {
		if u, ok := v.(*store.User); ok {
			return u
		}
	}
	return nil
}

// AuthRequired 认证中间件：解析 JWT → 查库校验用户存在且为 active。
func (s *Service) AuthRequired() gin.HandlerFunc {
	return func(c *gin.Context) {
		token := tokenFromRequest(c)
		if token == "" {
			abortError(c, http.StatusUnauthorized, CodeUnauthorized, "未登录或会话已失效")
			return
		}
		userID, err := s.Tokens.Parse(token)
		if err != nil {
			abortError(c, http.StatusUnauthorized, CodeUnauthorized, "未登录或会话已失效")
			return
		}
		var user store.User
		if err := s.db.First(&user, "id = ?", userID).Error; err != nil {
			abortError(c, http.StatusUnauthorized, CodeUnauthorized, "未登录或会话已失效")
			return
		}
		if user.Status != "active" {
			abortError(c, http.StatusUnauthorized, CodeUnauthorized, "账号已被停用")
			return
		}
		c.Set(contextUserKey, &user)
		c.Next()
	}
}

// RequirePermission 鉴权中间件：当前用户须拥有 (obj, act) 权限点。
func (s *Service) RequirePermission(obj, act string) gin.HandlerFunc {
	return func(c *gin.Context) {
		user := CurrentUser(c)
		if user == nil {
			abortError(c, http.StatusUnauthorized, CodeUnauthorized, "未登录或会话已失效")
			return
		}
		ok, err := s.Enforcer.Enforce(user.ID, obj, act)
		if err != nil || !ok {
			abortError(c, http.StatusForbidden, CodeForbidden, "缺少所需权限: "+obj+":"+act)
			return
		}
		c.Next()
	}
}
