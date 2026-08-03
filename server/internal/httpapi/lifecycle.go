// CI 生命周期（/api/v1/cis/{id}/lifecycle、/api/v1/lifecycle/*）处理器（F-026）。
package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"

	"github.com/gin-gonic/gin"
	"gorm.io/datatypes"

	"meridian/server/internal/jumpserver"
	"meridian/server/internal/lifecycle"
	"meridian/server/internal/store"
)

// lifecycleRequest 与状态流转请求对应。
type lifecycleRequest struct {
	To string `json:"to"`
}

// transitionCI 处理 POST /api/v1/cis/{ci_id}/lifecycle：校验合法流转并落审计。
func (s *Server) transitionCI(c *gin.Context) {
	ci, ok := s.resolveCI(c, c.Param("ci_id"))
	if !ok {
		return
	}
	var req lifecycleRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		respondError(c, http.StatusBadRequest, CodeBadRequest, "请求体解析失败: "+err.Error(), nil)
		return
	}
	if !lifecycle.CanTransit(ci.Status, req.To) {
		respondError(c, http.StatusBadRequest, CodeValidationFailed,
			fmt.Sprintf("不允许从 %q 流转到 %q", ci.Status, req.To), nil)
		return
	}
	operator := currentOperator(c)
	if err := s.db.Model(&store.CI{}).Where("id = ?", ci.ID).Update("status", req.To).Error; err != nil {
		respondError(c, http.StatusInternalServerError, CodeInternal, "状态流转失败: "+err.Error(), nil)
		return
	}
	_ = s.db.Create(&store.AuditLog{
		CIID:     ci.ID,
		Action:   "lifecycle",
		Source:   "lifecycle",
		Operator: operator,
		Changes: datatypes.JSONMap{
			"status": map[string]any{"old": ci.Status, "new": req.To},
		},
		Message: "生命周期状态流转",
	}).Error
	if err := s.db.First(&ci, "id = ?", ci.ID).Error; err != nil {
		respondError(c, http.StatusInternalServerError, CodeInternal, "重新加载 CI 失败", nil)
		return
	}
	c.JSON(http.StatusOK, ci)
}

// retireCandidates 处理 GET /api/v1/lifecycle/retire-candidates：三方会签判定清单。
func (s *Server) retireCandidates(c *gin.Context) {
	checks, err := lifecycle.NewService(s.db, s.n9eClient).RetireCandidates(c.Request.Context())
	if err != nil {
		respondError(c, http.StatusInternalServerError, CodeInternal, "退役会签判定失败: "+err.Error(), nil)
		return
	}
	c.JSON(http.StatusOK, gin.H{"items": checks})
}

// retireRequest 与退役执行请求对应。
type retireRequest struct {
	CIID    string `json:"ci_id"`
	Confirm bool   `json:"confirm"`
}

// retireCI 处理 POST /api/v1/lifecycle/retire：confirm=true 时执行退役联动。
func (s *Server) retireCI(c *gin.Context) {
	var req retireRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		respondError(c, http.StatusBadRequest, CodeBadRequest, "请求体解析失败: "+err.Error(), nil)
		return
	}
	if req.CIID == "" || !req.Confirm {
		respondError(c, http.StatusBadRequest, CodeValidationFailed, "ci_id 为必填项且必须 confirm=true 确认执行", nil)
		return
	}
	ci, ok := s.resolveCI(c, req.CIID)
	if !ok {
		return
	}
	actions, err := lifecycle.NewService(s.db, s.n9eClient).
		Retire(c.Request.Context(), ci, currentOperator(c), s.resolveJumpServerClient())
	if err != nil {
		respondError(c, http.StatusBadRequest, CodeBadRequest, "退役执行失败: "+err.Error(), nil)
		return
	}
	c.JSON(http.StatusOK, gin.H{"ci_id": ci.ID, "actions": actions})
}

// resolveJumpServerClient 解析 JumpServer 客户端：
// 优先凭据库 type=jumpserver 的凭据（secret {"url"/"base_url","token"}），
// 其次环境变量 JUMPSERVER_URL / JUMPSERVER_TOKEN；都未配置返回 nil（联动跳过）。
func (s *Server) resolveJumpServerClient() *jumpserver.Client {
	if s.credCipher != nil {
		var cred store.Credential
		if err := s.db.Where("type = ?", store.CredentialTypeJumpServer).First(&cred).Error; err == nil {
			if plain, err := s.credCipher.Decrypt(cred.SecretCiphertext); err == nil {
				var secret map[string]any
				if json.Unmarshal(plain, &secret) == nil {
					baseURL, _ := secret["url"].(string)
					if baseURL == "" {
						baseURL, _ = secret["base_url"].(string)
					}
					token, _ := secret["token"].(string)
					if baseURL != "" {
						return jumpserver.NewClient(baseURL, token)
					}
				}
			}
		}
	}
	if baseURL := os.Getenv("JUMPSERVER_URL"); baseURL != "" {
		return jumpserver.NewClient(baseURL, os.Getenv("JUMPSERVER_TOKEN"))
	}
	return nil
}
