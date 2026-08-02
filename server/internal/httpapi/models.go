// 模型定义（/api/v1/models）处理器。
package httpapi

import (
	"fmt"
	"net/http"
	"regexp"

	"github.com/gin-gonic/gin"
	"gorm.io/datatypes"

	"meridian/server/internal/store"
	"meridian/server/internal/validation"
)

// 关系基数与方向枚举。
var (
	cardinalities = map[string]bool{"one_to_one": true, "one_to_many": true, "many_to_many": true}
	directions    = map[string]bool{"outgoing": true, "incoming": true}
)

// modelCreateRequest 与 ModelCreateRequest 对应，reconcile_keys 为模型引擎扩展字段。
type modelCreateRequest struct {
	Name          string                      `json:"name"`
	Code          string                      `json:"code"`
	Attributes    []store.AttributeDefinition `json:"attributes"`
	Relations     []store.RelationDefinition  `json:"relations"`
	ReconcileKeys []string                    `json:"reconcile_keys"`
}

// modelPatchRequest 与 ModelPatchRequest 对应：全字段可选，仅更新传入字段。
type modelPatchRequest struct {
	Name          *string                      `json:"name"`
	Attributes    *[]store.AttributeDefinition `json:"attributes"`
	Relations     *[]store.RelationDefinition  `json:"relations"`
	ReconcileKeys *[]string                    `json:"reconcile_keys"`
}

// listModels 处理 GET /api/v1/models：keyword 模糊过滤（名称或编码）+ 分页。
func (s *Server) listModels(c *gin.Context) {
	page, pageSize, ok := parsePage(c)
	if !ok {
		return
	}
	q := s.db.Model(&store.Model{})
	if kw := c.Query("keyword"); kw != "" {
		like := "%" + kw + "%"
		q = q.Where("LOWER(name) LIKE LOWER(?) OR LOWER(code) LIKE LOWER(?)", like, like)
	}
	var total int64
	if err := q.Count(&total).Error; err != nil {
		respondError(c, http.StatusInternalServerError, CodeInternal, "查询模型总数失败", nil)
		return
	}
	var items []store.Model
	if err := q.Order("created_at ASC").Offset((page - 1) * pageSize).Limit(pageSize).Find(&items).Error; err != nil {
		respondError(c, http.StatusInternalServerError, CodeInternal, "查询模型列表失败", nil)
		return
	}
	if items == nil {
		items = []store.Model{}
	}
	c.JSON(http.StatusOK, gin.H{"items": items, "total": total, "page": page, "page_size": pageSize})
}

// createModel 处理 POST /api/v1/models。
func (s *Server) createModel(c *gin.Context) {
	var req modelCreateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		respondError(c, http.StatusBadRequest, CodeBadRequest, "请求体解析失败: "+err.Error(), nil)
		return
	}
	if req.Name == "" || req.Code == "" {
		respondError(c, http.StatusBadRequest, CodeValidationFailed, "name 与 code 为必填项", nil)
		return
	}
	if errs := validateModelDefs(req.Attributes, req.Relations); errs != nil {
		respondError(c, http.StatusBadRequest, CodeValidationFailed, "模型定义校验未通过", errs)
		return
	}
	// code 全局唯一。
	var count int64
	s.db.Model(&store.Model{}).Where("code = ?", req.Code).Count(&count)
	if count > 0 {
		respondError(c, http.StatusBadRequest, CodeValidationFailed, fmt.Sprintf("模型编码 %q 已存在", req.Code), nil)
		return
	}
	attrs := req.Attributes
	if attrs == nil {
		attrs = []store.AttributeDefinition{}
	}
	rels := req.Relations
	if rels == nil {
		rels = []store.RelationDefinition{}
	}
	keys := req.ReconcileKeys
	if keys == nil {
		keys = []string{}
	}
	model := store.Model{
		Name:          req.Name,
		Code:          req.Code,
		Attributes:    datatypes.NewJSONType(attrs),
		Relations:     datatypes.NewJSONType(rels),
		ReconcileKeys: datatypes.NewJSONType(keys),
	}
	if err := s.db.Create(&model).Error; err != nil {
		respondError(c, http.StatusInternalServerError, CodeInternal, "创建模型失败: "+err.Error(), nil)
		return
	}
	c.JSON(http.StatusOK, model)
}

// getModel 处理 GET /api/v1/models/{model_id}（支持按 ID 或编码解析）。
func (s *Server) getModel(c *gin.Context) {
	model, ok := s.resolveModel(c, c.Param("model_id"))
	if !ok {
		return
	}
	c.JSON(http.StatusOK, model)
}

// patchModel 处理 PATCH /api/v1/models/{model_id}：仅更新传入字段。
func (s *Server) patchModel(c *gin.Context) {
	model, ok := s.resolveModel(c, c.Param("model_id"))
	if !ok {
		return
	}
	var req modelPatchRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		respondError(c, http.StatusBadRequest, CodeBadRequest, "请求体解析失败: "+err.Error(), nil)
		return
	}
	attrs, rels := model.Attributes.Data(), model.Relations.Data()
	if req.Attributes != nil {
		attrs = *req.Attributes
	}
	if req.Relations != nil {
		rels = *req.Relations
	}
	if req.Attributes != nil || req.Relations != nil {
		if errs := validateModelDefs(attrs, rels); errs != nil {
			respondError(c, http.StatusBadRequest, CodeValidationFailed, "模型定义校验未通过", errs)
			return
		}
	}
	updates := map[string]any{}
	if req.Name != nil {
		if *req.Name == "" {
			respondError(c, http.StatusBadRequest, CodeValidationFailed, "name 不能为空", nil)
			return
		}
		updates["name"] = *req.Name
	}
	if req.Attributes != nil {
		updates["attributes"] = datatypes.NewJSONType(attrs)
	}
	if req.Relations != nil {
		updates["relations"] = datatypes.NewJSONType(rels)
	}
	if req.ReconcileKeys != nil {
		updates["reconcile_keys"] = datatypes.NewJSONType(*req.ReconcileKeys)
	}
	if len(updates) > 0 {
		if err := s.db.Model(&store.Model{}).Where("id = ?", model.ID).Updates(updates).Error; err != nil {
			respondError(c, http.StatusInternalServerError, CodeInternal, "更新模型失败: "+err.Error(), nil)
			return
		}
	}
	if err := s.db.First(&model, "id = ?", model.ID).Error; err != nil {
		respondError(c, http.StatusInternalServerError, CodeInternal, "重新加载模型失败", nil)
		return
	}
	c.JSON(http.StatusOK, model)
}

// resolveModel 按 ID 或编码解析模型，未找到时响应 404。
func (s *Server) resolveModel(c *gin.Context, idOrCode string) (store.Model, bool) {
	var model store.Model
	err := s.db.Where("id = ? OR code = ?", idOrCode, idOrCode).First(&model).Error
	if err != nil {
		if isNotFound(err) {
			respondError(c, http.StatusNotFound, CodeNotFound, fmt.Sprintf("模型 %q 不存在", idOrCode), nil)
		} else {
			respondError(c, http.StatusInternalServerError, CodeInternal, "查询模型失败", nil)
		}
		return store.Model{}, false
	}
	return model, true
}

// validateModelDefs 校验属性/关系定义的合法性，返回逐字段错误。
func validateModelDefs(attrs []store.AttributeDefinition, rels []store.RelationDefinition) map[string]string {
	errs := map[string]string{}
	seen := map[string]bool{}
	for i, a := range attrs {
		key := fmt.Sprintf("attributes[%d]", i)
		if a.Name == "" || a.Code == "" {
			errs[key] = "属性 name 与 code 为必填项"
			continue
		}
		if !validation.ValidAttrType(a.Type) {
			errs[key] = fmt.Sprintf("属性 %s 类型 %q 非法（string/number/bool/enum/ip/date）", a.Code, a.Type)
			continue
		}
		if seen[a.Code] {
			errs[key] = fmt.Sprintf("属性编码 %q 重复", a.Code)
			continue
		}
		seen[a.Code] = true
		if a.Type == "enum" && len(a.EnumValues) == 0 {
			errs[key] = fmt.Sprintf("属性 %s 类型为 enum 但未提供 enum_values", a.Code)
			continue
		}
		if a.Regex != "" {
			if _, err := regexp.Compile(a.Regex); err != nil {
				errs[key] = fmt.Sprintf("属性 %s 正则无效: %v", a.Code, err)
			}
		}
	}
	seen = map[string]bool{}
	for i, r := range rels {
		key := fmt.Sprintf("relations[%d]", i)
		if r.Name == "" || r.Code == "" || r.TargetModel == "" {
			errs[key] = "关系 name、code、target_model 为必填项"
			continue
		}
		if !cardinalities[r.Cardinality] {
			errs[key] = fmt.Sprintf("关系 %s 基数 %q 非法", r.Code, r.Cardinality)
			continue
		}
		if !directions[r.Direction] {
			errs[key] = fmt.Sprintf("关系 %s 方向 %q 非法", r.Code, r.Direction)
			continue
		}
		if seen[r.Code] {
			errs[key] = fmt.Sprintf("关系编码 %q 重复", r.Code)
			continue
		}
		seen[r.Code] = true
	}
	if len(errs) == 0 {
		return nil
	}
	return errs
}
