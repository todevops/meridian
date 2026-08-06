// 数据范围权限（F-005）的 HTTP 层收口：受范围约束的用户
// 仅可见归属闭包内的 CI；越权直访返回 404（不泄露存在性，AC-F005-03）。
package httpapi

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"meridian/server/internal/auth"
)

// ciVisibleSet 返回当前用户的可见 CI 集合。
// restricted=false 表示不受数据范围约束（scope 为空的全量角色），调用方不过滤。
func (s *Server) ciVisibleSet(c *gin.Context) (set map[string]bool, restricted bool, err error) {
	user := auth.CurrentUser(c)
	if user == nil {
		return nil, false, nil
	}
	return s.scope.VisibleSet(c.Request.Context(), user)
}

// applyScopeFilter 给 CI 查询叠加数据范围过滤（restricted 时仅可见闭包内 CI）。
func applyScopeFilter(q *gorm.DB, set map[string]bool, restricted bool) *gorm.DB {
	if !restricted {
		return q
	}
	ids := make([]string, 0, len(set))
	for id := range set {
		ids = append(ids, id)
	}
	if len(ids) == 0 {
		return q.Where("1 = 0") // 空范围：看不到任何受限资产
	}
	return q.Where("id IN ?", ids)
}

// scopeAllows 判定 CI 是否在当前用户数据范围内；越权时写 404 并返回 false。
func (s *Server) scopeAllows(c *gin.Context, ciID string) bool {
	set, restricted, err := s.ciVisibleSet(c)
	if err != nil {
		respondError(c, http.StatusInternalServerError, CodeInternal, "计算数据范围失败", nil)
		return false
	}
	if !restricted || set[ciID] {
		return true
	}
	// 与「不存在」同形 404：不泄露资产存在性。
	respondError(c, http.StatusNotFound, CodeNotFound, "CI 不存在", nil)
	return false
}
