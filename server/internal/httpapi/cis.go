// CI 实例（/api/v1/cis）处理器。
package httpapi

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"gorm.io/datatypes"
	"gorm.io/gorm"

	"meridian/server/internal/auth"
	"meridian/server/internal/reconcile"
	"meridian/server/internal/store"
	"meridian/server/internal/validation"
)

// ciCreateRequest 与 CICreateRequest 对应。
type ciCreateRequest struct {
	ModelID    string         `json:"model_id"` // 所属模型（ID 或编码）
	Attributes map[string]any `json:"attributes"`
	Status     string         `json:"status"` // 默认 active
	Source     string         `json:"source"` // 默认 manual
}

// ciPatchRequest 与 CIPatchRequest 对应：全字段可选，attributes 为增量合并。
type ciPatchRequest struct {
	Attributes map[string]any `json:"attributes"`
	Status     *string        `json:"status"`
	Source     *string        `json:"source"`
}

// listCIs 处理 GET /api/v1/cis：model_id（ID 或编码）/status 过滤 + 分页。
func (s *Server) listCIs(c *gin.Context) {
	page, pageSize, ok := parsePage(c)
	if !ok {
		return
	}
	q := s.db.Model(&store.CI{})
	if modelFilter := c.Query("model_id"); modelFilter != "" {
		model, found := s.resolveModel(c, modelFilter)
		if !found {
			return
		}
		q = q.Where("model_id = ?", model.ID)
	}
	if status := c.Query("status"); status != "" {
		if !validation.ValidCIStatus(status) {
			respondError(c, http.StatusBadRequest, CodeBadRequest, fmt.Sprintf("status 取值 %q 非法（discovered/active/retired）", status), nil)
			return
		}
		q = q.Where("status = ?", status)
	}
	// 全文关键字：匹配全部属性值（大小写不敏感）。
	if keyword := strings.TrimSpace(c.Query("keyword")); keyword != "" {
		q = ciKeywordScope(q, keyword)
	}
	// 数据范围（F-005）：受约束用户仅可见归属闭包内的 CI。
	set, restricted, err := s.ciVisibleSet(c)
	if err != nil {
		respondError(c, http.StatusInternalServerError, CodeInternal, "计算数据范围失败", nil)
		return
	}
	q = applyScopeFilter(q, set, restricted)
	var total int64
	if err := q.Count(&total).Error; err != nil {
		respondError(c, http.StatusInternalServerError, CodeInternal, "查询 CI 总数失败", nil)
		return
	}
	var items []store.CI
	if err := q.Order("created_at ASC").Offset((page - 1) * pageSize).Limit(pageSize).Find(&items).Error; err != nil {
		respondError(c, http.StatusInternalServerError, CodeInternal, "查询 CI 列表失败", nil)
		return
	}
	if items == nil {
		items = []store.CI{}
	}
	c.JSON(http.StatusOK, gin.H{"items": items, "total": total, "page": page, "page_size": pageSize})
}

// createCI 处理 POST /api/v1/cis：强制执行模型校验规则。
func (s *Server) createCI(c *gin.Context) {
	var req ciCreateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		respondError(c, http.StatusBadRequest, CodeBadRequest, "请求体解析失败: "+err.Error(), nil)
		return
	}
	if req.ModelID == "" || req.Attributes == nil {
		respondError(c, http.StatusBadRequest, CodeValidationFailed, "model_id 与 attributes 为必填项", nil)
		return
	}
	model, ok := s.resolveModel(c, req.ModelID)
	if !ok {
		return
	}
	status, source := req.Status, req.Source
	if status == "" {
		status = "active"
	}
	if source == "" {
		source = "manual"
	}
	if !validation.ValidCIStatus(status) {
		respondError(c, http.StatusBadRequest, CodeValidationFailed, fmt.Sprintf("status 取值 %q 非法（discovered/active/retired）", status), nil)
		return
	}
	if errs := validation.ValidateAttributes(model.Attributes.Data(), req.Attributes); errs != nil {
		respondError(c, http.StatusBadRequest, CodeValidationFailed, "属性校验未通过", map[string]string(errs))
		return
	}
	if errs := validation.ValidateUnique(s.db, model.ID, model.Attributes.Data(), req.Attributes, ""); errs != nil {
		respondError(c, http.StatusBadRequest, CodeValidationFailed, "唯一性校验未通过", map[string]string(errs))
		return
	}
	ci := store.CI{
		ModelID:      model.ID,
		Attributes:   datatypes.JSONMap(req.Attributes),
		FieldSources: datatypes.JSONMap{},
		Status:       status,
		Source:       source,
	}
	for k := range req.Attributes {
		ci.FieldSources[k] = source
	}
	// 建档与审计同事务写入。
	changes := map[string]reconcile.Change{}
	for k, v := range req.Attributes {
		changes[k] = reconcile.Change{Old: nil, New: v}
	}
	err := s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&ci).Error; err != nil {
			return err
		}
		writeAuditLog(tx, ci.ID, "create", source, currentOperator(c), changes, "接口创建 CI")
		return nil
	})
	if err != nil {
		respondError(c, http.StatusInternalServerError, CodeInternal, "创建 CI 失败: "+err.Error(), nil)
		return
	}
	c.JSON(http.StatusOK, ci)
}

// getCI 处理 GET /api/v1/cis/{ci_id}。
func (s *Server) getCI(c *gin.Context) {
	ci, ok := s.resolveCI(c, c.Param("ci_id"))
	if !ok {
		return
	}
	if !s.scopeAllows(c, ci.ID) {
		return
	}
	c.JSON(http.StatusOK, ci)
}

// patchCI 处理 PATCH /api/v1/cis/{ci_id}：attributes 增量合并，合并结果强制校验。
func (s *Server) patchCI(c *gin.Context) {
	ci, ok := s.resolveCI(c, c.Param("ci_id"))
	if !ok {
		return
	}
	var req ciPatchRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		respondError(c, http.StatusBadRequest, CodeBadRequest, "请求体解析失败: "+err.Error(), nil)
		return
	}
	var model store.Model
	if err := s.db.First(&model, "id = ?", ci.ModelID).Error; err != nil {
		respondError(c, http.StatusInternalServerError, CodeInternal, "加载所属模型失败", nil)
		return
	}

	// 字段级合并计划：PATCH 视为人工维护（除非显式指定 source），优先级最高。
	source := "manual"
	if req.Source != nil && *req.Source != "" {
		source = *req.Source
	}
	changes, _ := reconcile.PlanMerge(&ci, req.Attributes, source)

	// 合并后的完整属性集须通过模型校验。
	if len(changes) > 0 {
		merged := map[string]any{}
		for k, v := range ci.Attributes {
			merged[k] = v
		}
		for k, ch := range changes {
			merged[k] = ch.New
		}
		if errs := validation.ValidateAttributes(model.Attributes.Data(), merged); errs != nil {
			respondError(c, http.StatusBadRequest, CodeValidationFailed, "属性校验未通过", map[string]string(errs))
			return
		}
		if errs := validation.ValidateUnique(s.db, model.ID, model.Attributes.Data(), merged, ci.ID); errs != nil {
			respondError(c, http.StatusBadRequest, CodeValidationFailed, "唯一性校验未通过", map[string]string(errs))
			return
		}
		reconcile.ApplyChanges(&ci, changes, source)
	}

	updates := map[string]any{}
	if len(changes) > 0 {
		updates["attributes"] = ci.Attributes
		updates["field_sources"] = ci.FieldSources
	}
	if req.Status != nil {
		if !validation.ValidCIStatus(*req.Status) {
			respondError(c, http.StatusBadRequest, CodeValidationFailed, fmt.Sprintf("status 取值 %q 非法（discovered/active/retired）", *req.Status), nil)
			return
		}
		updates["status"] = *req.Status
	}
	if req.Source != nil && *req.Source != "" {
		updates["source"] = *req.Source
	}
	if len(updates) > 0 {
		if err := s.db.Model(&store.CI{}).Where("id = ?", ci.ID).Updates(updates).Error; err != nil {
			respondError(c, http.StatusInternalServerError, CodeInternal, "更新 CI 失败: "+err.Error(), nil)
			return
		}
	}
	if len(changes) > 0 {
		writeAuditLog(s.db, ci.ID, "update", source, currentOperator(c), changes, "接口更新 CI")
	}
	if err := s.db.First(&ci, "id = ?", ci.ID).Error; err != nil {
		respondError(c, http.StatusInternalServerError, CodeInternal, "重新加载 CI 失败", nil)
		return
	}
	c.JSON(http.StatusOK, ci)
}

// listCIRelations 处理 GET /api/v1/cis/{ci_id}/relations：返回含对端 CI 摘要的关系列表。
func (s *Server) listCIRelations(c *gin.Context) {
	ci, ok := s.resolveCI(c, c.Param("ci_id"))
	if !ok {
		return
	}
	if !s.scopeAllows(c, ci.ID) {
		return
	}
	var rels []store.CIRelation
	if err := s.db.Where("src_ci_id = ? OR dst_ci_id = ?", ci.ID, ci.ID).Find(&rels).Error; err != nil {
		respondError(c, http.StatusInternalServerError, CodeInternal, "查询关系列表失败", nil)
		return
	}
	items := []gin.H{}
	for _, rel := range rels {
		direction, peerID := "outgoing", rel.DstCIID
		if rel.DstCIID == ci.ID && rel.SrcCIID != ci.ID {
			direction, peerID = "incoming", rel.SrcCIID
		}
		var peer store.CI
		if err := s.db.First(&peer, "id = ?", peerID).Error; err != nil {
			continue // 对端已被删除，跳过悬空关系
		}
		source := rel.Source
		if source == "" {
			source = store.RelationSourceManual
		}
		items = append(items, gin.H{
			"relation_code": rel.RelationCode,
			"direction":     direction,
			"peer_ci":       peer,
			"source":        source,
		})
	}
	c.JSON(http.StatusOK, gin.H{"items": items})
}

// resolveCI 按 ID 解析 CI，未找到时响应 404。
func (s *Server) resolveCI(c *gin.Context, id string) (store.CI, bool) {
	var ci store.CI
	err := s.db.First(&ci, "id = ?", id).Error
	if err != nil {
		if isNotFound(err) {
			respondError(c, http.StatusNotFound, CodeNotFound, fmt.Sprintf("CI %q 不存在", id), nil)
		} else {
			respondError(c, http.StatusInternalServerError, CodeInternal, "查询 CI 失败", nil)
		}
		return store.CI{}, false
	}
	return ci, true
}

// writeAuditLog 写一条 CI 审计记录（失败仅记日志级忽略，不阻断主流程）。
// operator 为操作者用户名；系统通道（采集器/webhook）传入空串时落 system。
func writeAuditLog(db *gorm.DB, ciID, action, source, operator string, changes map[string]reconcile.Change, message string) {
	if operator == "" {
		operator = "system"
	}
	jsonChanges := datatypes.JSONMap{}
	for k, ch := range changes {
		jsonChanges[k] = map[string]any{"old": ch.Old, "new": ch.New}
	}
	_ = db.Create(&store.AuditLog{
		CIID:     ciID,
		Action:   action,
		Source:   source,
		Operator: operator,
		Changes:  jsonChanges,
		Message:  message,
	}).Error
}

// currentOperator 从请求上下文取当前用户名；无会话上下文时返回 system。
func currentOperator(c *gin.Context) string {
	if user := auth.CurrentUser(c); user != nil {
		return user.Username
	}
	return "system"
}
