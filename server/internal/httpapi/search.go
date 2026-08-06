// 全局全文搜索（/api/v1/search）与 CI 关键字过滤的共享逻辑。
//
// 实现选型：PostgreSQL/SQLite 通用的 LOWER(CAST(... AS TEXT)) LIKE 子串匹配
// （生产 PG 可用 pg_trgm 索引加速），不引入 ES 等外部中间件；
// 搜索契约保持稳定，未来可在此层后替换实现。
package httpapi

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"meridian/server/internal/auth"
	"meridian/server/internal/store"
)

// escapeLike 转义 LIKE 通配符（% _ \），配合 ESCAPE '\' 使用。
func escapeLike(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return r.Replace(s)
}

// likePattern 生成大小写不敏感的子串匹配模式。
func likePattern(keyword string) string {
	return "%" + strings.ToLower(escapeLike(keyword)) + "%"
}

// ciKeywordScope 给 CI 查询追加全文关键字过滤（匹配全部属性值）。
func ciKeywordScope(q *gorm.DB, keyword string) *gorm.DB {
	return q.Where("LOWER(CAST(attributes AS TEXT)) LIKE ? ESCAPE '\\'", likePattern(keyword))
}

// textKeywordScope 给普通文本列查询追加关键字过滤（OR 多列）。
func textKeywordScope(q *gorm.DB, keyword string, columns ...string) *gorm.DB {
	pattern := likePattern(keyword)
	conds := make([]string, 0, len(columns))
	args := make([]any, 0, len(columns))
	for _, col := range columns {
		conds = append(conds, fmt.Sprintf("LOWER(%s) LIKE ? ESCAPE '\\'", col))
		args = append(args, pattern)
	}
	return q.Where(strings.Join(conds, " OR "), args...)
}

// searchItem 与契约 SearchItem 对应。
type searchItem struct {
	Kind      string `json:"kind"`
	ID        string `json:"id"`
	Title     string `json:"title"`
	Subtitle  string `json:"subtitle"`
	Matched   string `json:"matched,omitempty"`
	ModelCode string `json:"model_code,omitempty"`
}

// ciDisplayTitle 取 CI 的展示名（与 dcim.displayName 同一约定）。
func ciDisplayTitle(ci store.CI) string {
	for _, key := range []string{"name", "hostname", "ident", "serial_no", "code"} {
		if v, ok := ci.Attributes[key].(string); ok && v != "" {
			return v
		}
	}
	return ci.ID
}

// ciMatchedAttr 找到第一个包含关键字的属性，返回 "key=value" 命中说明（截断长值）。
func ciMatchedAttr(ci store.CI, keyword string) string {
	kw := strings.ToLower(keyword)
	for k, v := range ci.Attributes {
		s := fmt.Sprintf("%v", v)
		if strings.Contains(strings.ToLower(s), kw) {
			if len(s) > 60 {
				s = s[:60] + "…"
			}
			return k + "=" + s
		}
	}
	return ""
}

// globalSearch 处理 GET /api/v1/search：跨模型/CI/IPAM 的分组搜索。
// 分组按当前用户权限点裁剪：无权限的分组直接省略。
func (s *Server) globalSearch(c *gin.Context) {
	q := strings.TrimSpace(c.Query("q"))
	if q == "" {
		respondError(c, http.StatusBadRequest, CodeBadRequest, "q 为必填参数", nil)
		return
	}
	limit := 10
	if v := c.Query("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 50 {
			respondError(c, http.StatusBadRequest, CodeBadRequest, "limit 必须为 1-50 的整数", nil)
			return
		}
		limit = n
	}

	user := auth.CurrentUser(c)
	can := func(obj, act string) bool {
		ok, _ := s.auth.Enforcer.Enforce(user.ID, obj, act)
		return ok
	}

	groups := []gin.H{}

	// 模型分组：名称/编码匹配。
	if can("model", "read") {
		var models []store.Model
		if err := textKeywordScope(s.db.Model(&store.Model{}), q, "name", "code").
			Order("created_at ASC").Limit(limit).Find(&models).Error; err != nil {
			respondError(c, http.StatusInternalServerError, CodeInternal, "搜索模型失败", nil)
			return
		}
		if len(models) > 0 {
			items := make([]searchItem, 0, len(models))
			for _, m := range models {
				items = append(items, searchItem{Kind: "model", ID: m.ID, Title: m.Name, Subtitle: m.Code})
			}
			groups = append(groups, gin.H{"kind": "models", "label": "模型", "items": items})
		}
	}

	// CI 分组：属性全文匹配，副标题带所属模型名。
	if can("ci", "read") {
		// 数据范围（F-005）：受约束用户只搜归属闭包内的资产（AC-F005-02）。
		set, restricted, err := s.ciVisibleSet(c)
		if err != nil {
			respondError(c, http.StatusInternalServerError, CodeInternal, "计算数据范围失败", nil)
			return
		}
		var cis []store.CI
		if err := applyScopeFilter(ciKeywordScope(s.db.Model(&store.CI{}), q), set, restricted).
			Order("created_at ASC").Limit(limit).Find(&cis).Error; err != nil {
			respondError(c, http.StatusInternalServerError, CodeInternal, "搜索 CI 失败", nil)
			return
		}
		if len(cis) > 0 {
			// 预取涉及模型，解析名称。
			modelNames := map[string]store.Model{}
			for _, ci := range cis {
				if _, ok := modelNames[ci.ModelID]; !ok {
					var m store.Model
					if err := s.db.First(&m, "id = ?", ci.ModelID).Error; err == nil {
						modelNames[ci.ModelID] = m
					}
				}
			}
			items := make([]searchItem, 0, len(cis))
			for _, ci := range cis {
				m := modelNames[ci.ModelID]
				items = append(items, searchItem{
					Kind:      "ci",
					ID:        ci.ID,
					Title:     ciDisplayTitle(ci),
					Subtitle:  m.Name + "（" + m.Code + "）",
					Matched:   ciMatchedAttr(ci, q),
					ModelCode: m.Code,
				})
			}
			groups = append(groups, gin.H{"kind": "cis", "label": "CI 实例", "items": items})
		}
	}

	// IPAM 分组：前缀（CIDR/名称/描述）与 IP（地址/描述）。
	if can("ipam", "read") {
		items := []searchItem{}
		var prefixes []store.IPPrefix
		if err := textKeywordScope(s.db.Model(&store.IPPrefix{}), q, "cidr", "name", "description").
			Order("created_at ASC").Limit(limit).Find(&prefixes).Error; err != nil {
			respondError(c, http.StatusInternalServerError, CodeInternal, "搜索 IPAM 前缀失败", nil)
			return
		}
		for _, p := range prefixes {
			items = append(items, searchItem{Kind: "ipam_prefix", ID: p.ID, Title: p.CIDR, Subtitle: p.Name})
		}
		if len(items) < limit {
			var ips []store.IPAddress
			if err := textKeywordScope(s.db.Model(&store.IPAddress{}), q, "ip", "description").
				Order("created_at ASC").Limit(limit - len(items)).Find(&ips).Error; err != nil {
				respondError(c, http.StatusInternalServerError, CodeInternal, "搜索 IP 失败", nil)
				return
			}
			for _, ip := range ips {
				items = append(items, searchItem{Kind: "ipam_ip", ID: ip.ID, Title: ip.IP, Subtitle: ip.Description})
			}
		}
		if len(items) > 0 {
			groups = append(groups, gin.H{"kind": "ipam", "label": "IPAM", "items": items})
		}
	}

	c.JSON(http.StatusOK, gin.H{"query": q, "groups": groups})
}
