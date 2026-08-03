// 发现池（/api/v1/discovery-pool）处理器：列表查询与人工裁决（确认建档/忽略）。
package httpapi

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"gorm.io/datatypes"
	"gorm.io/gorm"

	"meridian/server/internal/reconcile"
	"meridian/server/internal/store"
	"meridian/server/internal/validation"
)

// poolConfirmRequest 与 DiscoveryPoolConfirmRequest 对应：全字段可选，
// 缺省时沿用池条目记录的候选模型与采集属性。
type poolConfirmRequest struct {
	ModelID    string         `json:"model_id"`
	Attributes map[string]any `json:"attributes"`
}

// poolItemView 把池条目投影为契约形状（关联原始发现记录与调和结果）。
func poolItemView(item store.PoolItem) gin.H {
	rec := item.Record
	// 判定依据按写入时的连接符还原为数组。
	reasons := []string{}
	if item.Reason != "" {
		reasons = strings.Split(item.Reason, "；")
	}
	attrs := map[string]any{}
	if a, ok := rec["attributes"].(map[string]any); ok {
		attrs = a
	}
	return gin.H{
		"id":               item.ID,
		"source":           rec["source"],
		"collector":        rec["collector"],
		"model_candidate":  item.ModelCode,
		"attributes":       attrs,
		"reconcile_action": item.ReconcileAction,
		"reasons":          reasons,
		"status":           item.Status,
		"created_at":       item.CreatedAt,
	}
}

// listPoolItems 处理 GET /api/v1/discovery-pool：status 过滤 + 分页。
func (s *Server) listPoolItems(c *gin.Context) {
	page, pageSize, ok := parsePage(c)
	if !ok {
		return
	}
	q := s.db.Model(&store.PoolItem{})
	if status := c.Query("status"); status != "" {
		if status != "pending" && status != "confirmed" && status != "ignored" {
			respondError(c, http.StatusBadRequest, CodeBadRequest,
				fmt.Sprintf("status 取值 %q 非法（pending/confirmed/ignored）", status), nil)
			return
		}
		q = q.Where("status = ?", status)
	}
	var total int64
	if err := q.Count(&total).Error; err != nil {
		respondError(c, http.StatusInternalServerError, CodeInternal, "查询发现池总数失败", nil)
		return
	}
	var rows []store.PoolItem
	if err := q.Order("created_at ASC").Offset((page - 1) * pageSize).Limit(pageSize).Find(&rows).Error; err != nil {
		respondError(c, http.StatusInternalServerError, CodeInternal, "查询发现池列表失败", nil)
		return
	}
	items := []gin.H{}
	for _, row := range rows {
		items = append(items, poolItemView(row))
	}
	c.JSON(http.StatusOK, gin.H{"items": items, "total": total, "page": page, "page_size": pageSize})
}

// confirmPoolItem 处理 POST /api/v1/discovery-pool/{id}/confirm：
// 以池条目的发现属性（可被 body 覆盖）创建 status=active 的 CI，并把池条目置 confirmed。
func (s *Server) confirmPoolItem(c *gin.Context) {
	item, ok := s.resolvePendingPoolItem(c, c.Param("id"))
	if !ok {
		return
	}
	var req poolConfirmRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		respondError(c, http.StatusBadRequest, CodeBadRequest, "请求体解析失败: "+err.Error(), nil)
		return
	}

	// 模型默认取候选模型，body 可改挂其他模型（ID 或编码）。
	modelRef := item.ModelCode
	if req.ModelID != "" {
		modelRef = req.ModelID
	}
	model, ok := s.resolveModel(c, modelRef)
	if !ok {
		return
	}

	// 属性以发现记录为底、body 增量覆盖，合并结果强制走模型校验。
	source, _ := item.Record["source"].(string)
	merged := map[string]any{}
	if attrs, ok := item.Record["attributes"].(map[string]any); ok {
		for k, v := range attrs {
			merged[k] = v
		}
	}
	for k, v := range req.Attributes {
		merged[k] = v
	}
	if errs := validation.ValidateAttributes(model.Attributes.Data(), merged); errs != nil {
		respondError(c, http.StatusBadRequest, CodeValidationFailed, "属性校验未通过", map[string]string(errs))
		return
	}
	if errs := validation.ValidateUnique(s.db, model.ID, model.Attributes.Data(), merged, ""); errs != nil {
		respondError(c, http.StatusBadRequest, CodeValidationFailed, "唯一性校验未通过", map[string]string(errs))
		return
	}

	ci := store.CI{
		ModelID:      model.ID,
		Attributes:   datatypes.JSONMap(merged),
		FieldSources: datatypes.JSONMap{},
		Status:       "active",
		Source:       source,
	}
	changes := map[string]reconcile.Change{}
	for k, v := range merged {
		ci.FieldSources[k] = source
		changes[k] = reconcile.Change{Old: nil, New: v}
	}
	// 建档与池状态翻转同事务，避免半完成状态。
	err := s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&ci).Error; err != nil {
			return err
		}
		if err := tx.Model(&store.PoolItem{}).Where("id = ?", item.ID).
			Update("status", "confirmed").Error; err != nil {
			return err
		}
		writeAuditLog(tx, ci.ID, "create", source, currentOperator(c), changes, fmt.Sprintf("发现池确认建档（池条目 %s）", item.ID))
		return nil
	})
	if err != nil {
		respondError(c, http.StatusInternalServerError, CodeInternal, "确认建档失败: "+err.Error(), nil)
		return
	}
	c.JSON(http.StatusCreated, ci)
}

// ignorePoolItem 处理 POST /api/v1/discovery-pool/{id}/ignore：把池条目置 ignored。
func (s *Server) ignorePoolItem(c *gin.Context) {
	item, ok := s.resolvePendingPoolItem(c, c.Param("id"))
	if !ok {
		return
	}
	if err := s.db.Model(&store.PoolItem{}).Where("id = ?", item.ID).
		Update("status", "ignored").Error; err != nil {
		respondError(c, http.StatusInternalServerError, CodeInternal, "更新池条目失败: "+err.Error(), nil)
		return
	}
	item.Status = "ignored"
	c.JSON(http.StatusOK, poolItemView(item))
}

// resolvePendingPoolItem 加载池条目并断言其处于 pending 待裁决状态：
// 不存在 404，已裁决（confirmed/ignored）409。
func (s *Server) resolvePendingPoolItem(c *gin.Context, id string) (store.PoolItem, bool) {
	var item store.PoolItem
	err := s.db.First(&item, "id = ?", id).Error
	if err != nil {
		if isNotFound(err) {
			respondError(c, http.StatusNotFound, CodeNotFound, fmt.Sprintf("发现池条目 %q 不存在", id), nil)
		} else {
			respondError(c, http.StatusInternalServerError, CodeInternal, "查询发现池条目失败", nil)
		}
		return store.PoolItem{}, false
	}
	if item.Status != "pending" {
		respondError(c, http.StatusConflict, CodeConflict,
			fmt.Sprintf("发现池条目 %q 已裁决（status=%s），不允许重复操作", id, item.Status), nil)
		return store.PoolItem{}, false
	}
	return item, true
}
