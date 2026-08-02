// 凭据纳管（/api/v1/credentials）处理器（F-005）：
// secret 只在写入（创建/轮换）时接收并立即加密落库，任何响应（含错误响应）永不回明文。
package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"meridian/server/internal/auth"
	"meridian/server/internal/store"
)

// credentialCreateRequest 与 CredentialCreateRequest 对应。
type credentialCreateRequest struct {
	Name        string         `json:"name"`
	Type        string         `json:"type"`
	Description string         `json:"description"`
	Secret      map[string]any `json:"secret"`
}

// credentialPatchRequest 与 CredentialPatchRequest 对应：仅非密字段可改。
type credentialPatchRequest struct {
	Name        *string `json:"name"`
	Description *string `json:"description"`
}

// credentialRotateRequest 与 CredentialRotateRequest 对应。
type credentialRotateRequest struct {
	Secret map[string]any `json:"secret"`
}

// validCredentialType 校验凭据类型枚举。
func validCredentialType(t string) bool {
	for _, v := range store.CredentialTypes {
		if v == t {
			return true
		}
	}
	return false
}

// credentialView 把凭据投影为契约形状（绝不包含密文/明文）。
func credentialView(c store.Credential) gin.H {
	return gin.H{
		"id":              c.ID,
		"name":            c.Name,
		"type":            c.Type,
		"description":     c.Description,
		"last_rotated_at": c.LastRotatedAt,
		"use_count":       c.UseCount,
		"created_at":      c.CreatedAt,
		"updated_at":      c.UpdatedAt,
	}
}

// writeCredentialAudit 记录凭据操作审计。
func (s *Server) writeCredentialAudit(credentialID, action, operator, source string) {
	_ = s.db.Create(&store.CredentialAudit{
		CredentialID: credentialID,
		Action:       action,
		Operator:     operator,
		Source:       source,
	}).Error
}

// encryptSecret 序列化并加密 secret；失败时返回错误说明（不含明文内容）。
func (s *Server) encryptSecret(secret map[string]any) (string, error) {
	raw, err := json.Marshal(secret)
	if err != nil {
		return "", fmt.Errorf("secret 序列化失败")
	}
	ct, err := s.credCipher.Encrypt(raw)
	if err != nil {
		return "", fmt.Errorf("secret 加密失败")
	}
	return ct, nil
}

// listCredentials 处理 GET /api/v1/credentials：type 过滤 + 分页。
func (s *Server) listCredentials(c *gin.Context) {
	page, pageSize, ok := parsePage(c)
	if !ok {
		return
	}
	q := s.db.Model(&store.Credential{})
	if typ := c.Query("type"); typ != "" {
		if !validCredentialType(typ) {
			respondError(c, http.StatusBadRequest, CodeBadRequest,
				fmt.Sprintf("type 取值 %q 非法（%v）", typ, store.CredentialTypes), nil)
			return
		}
		q = q.Where("type = ?", typ)
	}
	var total int64
	if err := q.Count(&total).Error; err != nil {
		respondError(c, http.StatusInternalServerError, CodeInternal, "查询凭据总数失败", nil)
		return
	}
	var rows []store.Credential
	if err := q.Order("created_at DESC").Offset((page - 1) * pageSize).Limit(pageSize).Find(&rows).Error; err != nil {
		respondError(c, http.StatusInternalServerError, CodeInternal, "查询凭据列表失败", nil)
		return
	}
	items := []gin.H{}
	for _, row := range rows {
		items = append(items, credentialView(row))
	}
	c.JSON(http.StatusOK, gin.H{"items": items, "total": total, "page": page, "page_size": pageSize})
}

// createCredential 处理 POST /api/v1/credentials：加密落库并记 create 审计。
func (s *Server) createCredential(c *gin.Context) {
	var req credentialCreateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		respondError(c, http.StatusBadRequest, CodeBadRequest, "请求体解析失败: "+err.Error(), nil)
		return
	}
	if req.Name == "" {
		respondError(c, http.StatusBadRequest, CodeValidationFailed, "name 不能为空", map[string]string{"name": "不能为空"})
		return
	}
	if !validCredentialType(req.Type) {
		respondError(c, http.StatusBadRequest, CodeValidationFailed,
			fmt.Sprintf("type 取值 %q 非法（%v）", req.Type, store.CredentialTypes), map[string]string{"type": "非法凭据类型"})
		return
	}
	if len(req.Secret) == 0 {
		respondError(c, http.StatusBadRequest, CodeValidationFailed, "secret 不能为空", map[string]string{"secret": "不能为空"})
		return
	}
	ciphertext, err := s.encryptSecret(req.Secret)
	if err != nil {
		respondError(c, http.StatusInternalServerError, CodeInternal, err.Error(), nil)
		return
	}
	cred := store.Credential{
		Name:             req.Name,
		Type:             req.Type,
		Description:      req.Description,
		SecretCiphertext: ciphertext,
	}
	if err := s.db.Create(&cred).Error; err != nil {
		respondError(c, http.StatusInternalServerError, CodeInternal, "创建凭据失败", nil)
		return
	}
	user := auth.CurrentUser(c)
	s.writeCredentialAudit(cred.ID, store.CredentialAuditCreate, user.Username, "manual")
	c.JSON(http.StatusCreated, credentialView(cred))
}

// patchCredential 处理 PATCH /api/v1/credentials/{credential_id}：仅非密字段。
func (s *Server) patchCredential(c *gin.Context) {
	cred, ok := s.resolveCredential(c, c.Param("credential_id"))
	if !ok {
		return
	}
	var req credentialPatchRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		respondError(c, http.StatusBadRequest, CodeBadRequest, "请求体解析失败: "+err.Error(), nil)
		return
	}
	updates := map[string]any{}
	if req.Name != nil {
		if *req.Name == "" {
			respondError(c, http.StatusBadRequest, CodeValidationFailed, "name 不能为空", map[string]string{"name": "不能为空"})
			return
		}
		updates["name"] = *req.Name
	}
	if req.Description != nil {
		updates["description"] = *req.Description
	}
	if len(updates) > 0 {
		if err := s.db.Model(&store.Credential{}).Where("id = ?", cred.ID).Updates(updates).Error; err != nil {
			respondError(c, http.StatusInternalServerError, CodeInternal, "更新凭据失败", nil)
			return
		}
		user := auth.CurrentUser(c)
		s.writeCredentialAudit(cred.ID, store.CredentialAuditUpdate, user.Username, "manual")
	}
	if err := s.db.First(&cred, "id = ?", cred.ID).Error; err != nil {
		respondError(c, http.StatusInternalServerError, CodeInternal, "查询凭据失败", nil)
		return
	}
	c.JSON(http.StatusOK, credentialView(cred))
}

// rotateCredential 处理 POST /api/v1/credentials/{credential_id}/rotate：
// 重新加密新 secret、刷新 last_rotated_at 并记 rotate 审计。
func (s *Server) rotateCredential(c *gin.Context) {
	cred, ok := s.resolveCredential(c, c.Param("credential_id"))
	if !ok {
		return
	}
	var req credentialRotateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		respondError(c, http.StatusBadRequest, CodeBadRequest, "请求体解析失败: "+err.Error(), nil)
		return
	}
	if len(req.Secret) == 0 {
		respondError(c, http.StatusBadRequest, CodeValidationFailed, "secret 不能为空", map[string]string{"secret": "不能为空"})
		return
	}
	ciphertext, err := s.encryptSecret(req.Secret)
	if err != nil {
		respondError(c, http.StatusInternalServerError, CodeInternal, err.Error(), nil)
		return
	}
	if err := s.db.Model(&store.Credential{}).Where("id = ?", cred.ID).
		Updates(map[string]any{"secret_ciphertext": ciphertext, "last_rotated_at": time.Now()}).Error; err != nil {
		respondError(c, http.StatusInternalServerError, CodeInternal, "轮换凭据失败", nil)
		return
	}
	user := auth.CurrentUser(c)
	s.writeCredentialAudit(cred.ID, store.CredentialAuditRotate, user.Username, "manual")
	if err := s.db.First(&cred, "id = ?", cred.ID).Error; err != nil {
		respondError(c, http.StatusInternalServerError, CodeInternal, "查询凭据失败", nil)
		return
	}
	c.JSON(http.StatusOK, credentialView(cred))
}

// listCredentialAudits 处理 GET /api/v1/credentials/{credential_id}/audits：分页审计。
func (s *Server) listCredentialAudits(c *gin.Context) {
	cred, ok := s.resolveCredential(c, c.Param("credential_id"))
	if !ok {
		return
	}
	page, pageSize, ok := parsePage(c)
	if !ok {
		return
	}
	q := s.db.Model(&store.CredentialAudit{}).Where("credential_id = ?", cred.ID)
	var total int64
	if err := q.Count(&total).Error; err != nil {
		respondError(c, http.StatusInternalServerError, CodeInternal, "查询凭据审计总数失败", nil)
		return
	}
	var rows []store.CredentialAudit
	if err := q.Order("created_at DESC").Offset((page - 1) * pageSize).Limit(pageSize).Find(&rows).Error; err != nil {
		respondError(c, http.StatusInternalServerError, CodeInternal, "查询凭据审计失败", nil)
		return
	}
	items := []store.CredentialAudit{}
	items = append(items, rows...)
	c.JSON(http.StatusOK, gin.H{"items": items, "total": total, "page": page, "page_size": pageSize})
}

// resolveCredential 加载凭据：不存在 404。
func (s *Server) resolveCredential(c *gin.Context, id string) (store.Credential, bool) {
	var cred store.Credential
	err := s.db.First(&cred, "id = ?", id).Error
	if err != nil {
		if isNotFound(err) {
			respondError(c, http.StatusNotFound, CodeNotFound, fmt.Sprintf("凭据 %q 不存在", id), nil)
		} else {
			respondError(c, http.StatusInternalServerError, CodeInternal, "查询凭据失败", nil)
		}
		return store.Credential{}, false
	}
	return cred, true
}
