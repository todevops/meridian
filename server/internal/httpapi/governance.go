// 稽核规则与整改待办（/api/v1/governance/*）处理器（F-081）。
package httpapi

import (
	"fmt"
	"net/http"

	"github.com/gin-gonic/gin"
	"gorm.io/datatypes"

	"meridian/server/internal/auditrules"
	"meridian/server/internal/store"
)

// auditRuleRequest 与规则创建/修改请求对应。
type auditRuleRequest struct {
	Name      *string        `json:"name"`
	ModelCode *string        `json:"model_code"`
	Type      *string        `json:"type"` // audit（默认）/auto_ingest（白名单）
	Filter    map[string]any `json:"filter"`
	Assertion *string        `json:"assertion"`
	Message   *string        `json:"message"`
	Enabled   *bool          `json:"enabled"`
	DryRun    *bool          `json:"dry_run"`
}

// validRuleType 校验规则类型取值合法。
func validRuleType(t string) bool {
	return t == store.AuditRuleTypeAudit || t == store.AuditRuleTypeAutoIngest
}

// listAuditRules 处理 GET /api/v1/governance/rules。
func (s *Server) listAuditRules(c *gin.Context) {
	var rules []store.AuditRule
	if err := s.db.Order("created_at ASC").Find(&rules).Error; err != nil {
		respondError(c, http.StatusInternalServerError, CodeInternal, "查询稽核规则失败", nil)
		return
	}
	if rules == nil {
		rules = []store.AuditRule{}
	}
	c.JSON(http.StatusOK, gin.H{"items": rules})
}

// createAuditRule 处理 POST /api/v1/governance/rules。
func (s *Server) createAuditRule(c *gin.Context) {
	var req auditRuleRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		respondError(c, http.StatusBadRequest, CodeBadRequest, "请求体解析失败: "+err.Error(), nil)
		return
	}
	if req.Name == nil || *req.Name == "" || req.ModelCode == nil || *req.ModelCode == "" ||
		req.Assertion == nil || req.Message == nil || *req.Message == "" {
		respondError(c, http.StatusBadRequest, CodeValidationFailed, "name/model_code/assertion/message 为必填项", nil)
		return
	}
	if err := auditrules.ValidateAssertion(*req.Assertion); err != nil {
		respondError(c, http.StatusBadRequest, CodeValidationFailed, err.Error(), nil)
		return
	}
	filter := datatypes.JSONMap{}
	if req.Filter != nil {
		filter = datatypes.JSONMap(req.Filter)
	}
	rule := store.AuditRule{
		Name:      *req.Name,
		ModelCode: *req.ModelCode,
		Type:      store.AuditRuleTypeAudit,
		Filter:    filter,
		Assertion: *req.Assertion,
		Message:   *req.Message,
		Enabled:   true,
	}
	if req.Type != nil && *req.Type != "" {
		if !validRuleType(*req.Type) {
			respondError(c, http.StatusBadRequest, CodeValidationFailed,
				fmt.Sprintf("type 取值 %q 非法（audit/auto_ingest）", *req.Type), nil)
			return
		}
		rule.Type = *req.Type
	}
	if req.Enabled != nil {
		rule.Enabled = *req.Enabled
	}
	if req.DryRun != nil {
		rule.DryRun = *req.DryRun
	}
	if err := s.db.Create(&rule).Error; err != nil {
		respondError(c, http.StatusInternalServerError, CodeInternal, "创建稽核规则失败: "+err.Error(), nil)
		return
	}
	c.JSON(http.StatusOK, rule)
}

// patchAuditRule 处理 PATCH /api/v1/governance/rules/{id}。
func (s *Server) patchAuditRule(c *gin.Context) {
	rule, ok := s.resolveAuditRule(c, c.Param("id"))
	if !ok {
		return
	}
	var req auditRuleRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		respondError(c, http.StatusBadRequest, CodeBadRequest, "请求体解析失败: "+err.Error(), nil)
		return
	}
	updates := map[string]any{}
	if req.Name != nil && *req.Name != "" {
		updates["name"] = *req.Name
	}
	if req.ModelCode != nil && *req.ModelCode != "" {
		updates["model_code"] = *req.ModelCode
	}
	if req.Type != nil {
		if !validRuleType(*req.Type) {
			respondError(c, http.StatusBadRequest, CodeValidationFailed,
				fmt.Sprintf("type 取值 %q 非法（audit/auto_ingest）", *req.Type), nil)
			return
		}
		updates["type"] = *req.Type
	}
	if req.Filter != nil {
		updates["filter"] = datatypes.JSONMap(req.Filter)
	}
	if req.Assertion != nil {
		if err := auditrules.ValidateAssertion(*req.Assertion); err != nil {
			respondError(c, http.StatusBadRequest, CodeValidationFailed, err.Error(), nil)
			return
		}
		updates["assertion"] = *req.Assertion
	}
	if req.Message != nil && *req.Message != "" {
		updates["message"] = *req.Message
	}
	if req.Enabled != nil {
		updates["enabled"] = *req.Enabled
	}
	if req.DryRun != nil {
		updates["dry_run"] = *req.DryRun
	}
	if len(updates) > 0 {
		if err := s.db.Model(&store.AuditRule{}).Where("id = ?", rule.ID).Updates(updates).Error; err != nil {
			respondError(c, http.StatusInternalServerError, CodeInternal, "更新稽核规则失败: "+err.Error(), nil)
			return
		}
	}
	if err := s.db.First(&rule, "id = ?", rule.ID).Error; err != nil {
		respondError(c, http.StatusInternalServerError, CodeInternal, "重新加载稽核规则失败", nil)
		return
	}
	c.JSON(http.StatusOK, rule)
}

// runAuditRule 处理 POST /api/v1/governance/rules/{id}/run：手动执行一次稽核。
// auto_ingest 白名单规则不产生稽核待办（由调和引擎 create 分支消费），拒绝手动执行。
func (s *Server) runAuditRule(c *gin.Context) {
	rule, ok := s.resolveAuditRule(c, c.Param("id"))
	if !ok {
		return
	}
	if rule.Type == store.AuditRuleTypeAutoIngest {
		respondError(c, http.StatusBadRequest, CodeBadRequest, "白名单规则（auto_ingest）不支持手动稽核执行", nil)
		return
	}
	result, err := auditrules.NewEngine(s.db).RunRule(c.Request.Context(), rule)
	if err != nil {
		respondError(c, http.StatusBadRequest, CodeBadRequest, "执行稽核规则失败: "+err.Error(), nil)
		return
	}
	c.JSON(http.StatusOK, result)
}

// listGovernanceTodos 处理 GET /api/v1/governance/todos：status 过滤 + 分页，最新在前。
func (s *Server) listGovernanceTodos(c *gin.Context) {
	page, pageSize, ok := parsePage(c)
	if !ok {
		return
	}
	q := s.db.Model(&store.GovernanceTodo{})
	if status := c.Query("status"); status != "" {
		if status != store.TodoStatusOpen && status != store.TodoStatusClosed {
			respondError(c, http.StatusBadRequest, CodeBadRequest,
				fmt.Sprintf("status 取值 %q 非法（open/closed）", status), nil)
			return
		}
		q = q.Where("status = ?", status)
	}
	var total int64
	if err := q.Count(&total).Error; err != nil {
		respondError(c, http.StatusInternalServerError, CodeInternal, "查询待办总数失败", nil)
		return
	}
	var rows []store.GovernanceTodo
	if err := q.Order("created_at DESC").Offset((page - 1) * pageSize).Limit(pageSize).Find(&rows).Error; err != nil {
		respondError(c, http.StatusInternalServerError, CodeInternal, "查询待办列表失败", nil)
		return
	}
	// 附带规则名，便于前端展示。
	ruleNames := map[string]string{}
	items := []gin.H{}
	for _, t := range rows {
		name, cached := ruleNames[t.RuleID]
		if !cached {
			var rule store.AuditRule
			if err := s.db.First(&rule, "id = ?", t.RuleID).Error; err == nil {
				name = rule.Name
			}
			ruleNames[t.RuleID] = name
		}
		items = append(items, gin.H{
			"id":         t.ID,
			"rule_id":    t.RuleID,
			"rule_name":  name,
			"ci_id":      t.CIID,
			"title":      t.Title,
			"status":     t.Status,
			"created_at": t.CreatedAt,
			"closed_at":  t.ClosedAt,
		})
	}
	c.JSON(http.StatusOK, gin.H{"items": items, "total": total, "page": page, "page_size": pageSize})
}

// closeGovernanceTodo 处理 POST /api/v1/governance/todos/{id}/close：人工关闭（幂等）。
func (s *Server) closeGovernanceTodo(c *gin.Context) {
	var todo store.GovernanceTodo
	if err := s.db.First(&todo, "id = ?", c.Param("id")).Error; err != nil {
		if isNotFound(err) {
			respondError(c, http.StatusNotFound, CodeNotFound, "整改待办不存在", nil)
		} else {
			respondError(c, http.StatusInternalServerError, CodeInternal, "查询整改待办失败", nil)
		}
		return
	}
	if todo.Status == store.TodoStatusOpen {
		if err := s.db.Model(&store.GovernanceTodo{}).Where("id = ?", todo.ID).
			Updates(map[string]any{"status": store.TodoStatusClosed, "closed_at": s.db.NowFunc()}).Error; err != nil {
			respondError(c, http.StatusInternalServerError, CodeInternal, "关闭待办失败: "+err.Error(), nil)
			return
		}
	}
	if err := s.db.First(&todo, "id = ?", todo.ID).Error; err != nil {
		respondError(c, http.StatusInternalServerError, CodeInternal, "重新加载待办失败", nil)
		return
	}
	c.JSON(http.StatusOK, todo)
}

// resolveAuditRule 按 ID 解析稽核规则，未找到时响应 404。
func (s *Server) resolveAuditRule(c *gin.Context, id string) (store.AuditRule, bool) {
	var rule store.AuditRule
	err := s.db.First(&rule, "id = ?", id).Error
	if err != nil {
		if isNotFound(err) {
			respondError(c, http.StatusNotFound, CodeNotFound, fmt.Sprintf("稽核规则 %q 不存在", id), nil)
		} else {
			respondError(c, http.StatusInternalServerError, CodeInternal, "查询稽核规则失败", nil)
		}
		return store.AuditRule{}, false
	}
	return rule, true
}
