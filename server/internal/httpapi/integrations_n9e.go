// n9e 集成（/api/v1/integrations/n9e/*）处理器：
// F-070 上行回写（CI 业务归属/负责人回写 target 标签与备注、未归组主机生成治理待办）；
// F-063 嵌入代理（dashboard-url 拼接、alert-cur-events 原样透传，时序与告警原文不入主库）。
// n9e 连接信息优先取 credentials 表 type=n9e 的第一条（secret 需含 api_url/token），
// 缺则回退环境变量 N9E_API_URL / N9E_API_TOKEN。
package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"

	"github.com/gin-gonic/gin"

	"meridian/server/internal/n9e"
	"meridian/server/internal/store"
)

// writeback 回写用的 target 标签键：对应 host CI 的同名属性。
var writebackTagKeys = []string{"biz_group", "owner", "env"}

// n9eWritebackRequest 与 N9EWritebackRequest 对应。
type n9eWritebackRequest struct {
	DryRun bool `json:"dry_run,omitempty"`
}

// resolveN9EClient 解析 n9e 客户端：credentials 表 type=n9e 的第一条优先，缺则读环境变量。
// 第二返回值表示是否解析成功；失败时已响应错误。
func (s *Server) resolveN9EClient(c *gin.Context) (*n9e.Client, bool) {
	var cred store.Credential
	err := s.db.Where("type = ?", store.CredentialTypeN9E).Order("created_at ASC").First(&cred).Error
	if err == nil {
		plain, derr := s.credCipher.Decrypt(cred.SecretCiphertext)
		if derr != nil {
			respondError(c, http.StatusInternalServerError, CodeInternal, "解密 n9e 凭据失败", nil)
			return nil, false
		}
		secret := map[string]any{}
		if uerr := json.Unmarshal(plain, &secret); uerr != nil {
			respondError(c, http.StatusInternalServerError, CodeInternal, "n9e 凭据 secret 不是合法 JSON 对象", nil)
			return nil, false
		}
		apiURL, _ := secret["api_url"].(string)
		token, _ := secret["token"].(string)
		if apiURL != "" && token != "" {
			return n9e.NewClient(apiURL, token), true
		}
		respondError(c, http.StatusInternalServerError, CodeInternal, "n9e 凭据 secret 缺少 api_url/token", nil)
		return nil, false
	}
	if !isNotFound(err) {
		respondError(c, http.StatusInternalServerError, CodeInternal, "查询 n9e 凭据失败", nil)
		return nil, false
	}
	// 无凭据记录：回退环境变量。
	apiURL, token := os.Getenv("N9E_API_URL"), os.Getenv("N9E_API_TOKEN")
	if apiURL == "" || token == "" {
		respondError(c, http.StatusServiceUnavailable, CodeInternal,
			"n9e 未配置（credentials 表无 type=n9e 记录，且 N9E_API_URL/N9E_API_TOKEN 未设置）", nil)
		return nil, false
	}
	return n9e.NewClient(apiURL, token), true
}

// handleN9EWriteback 处理 POST /api/v1/integrations/n9e/writeback（F-070）：
// 遍历 host CI，ident 匹配 n9e target 后回写 tags（biz_group/owner/env，按 k=v 覆盖合并）
// 与 note（负责人+环境）；无 biz_group 的主机写治理待办告警；返回 {updated,skipped,todos,errors}。
func (s *Server) handleN9EWriteback(c *gin.Context) {
	var req n9eWritebackRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		respondError(c, http.StatusBadRequest, CodeBadRequest, "请求体解析失败: "+err.Error(), nil)
		return
	}
	client, ok := s.resolveN9EClient(c)
	if !ok {
		return
	}
	targets, err := client.ListTargets(c.Request.Context())
	if err != nil {
		respondError(c, http.StatusBadGateway, CodeInternal, "拉取 n9e targets 失败: "+err.Error(), nil)
		return
	}
	byIdent := map[string]n9e.Target{}
	for _, t := range targets {
		if _, dup := byIdent[t.Ident]; !dup {
			byIdent[t.Ident] = t
		}
	}

	var model store.Model
	if err := s.db.First(&model, "code = ?", "host").Error; err != nil {
		if isNotFound(err) {
			respondError(c, http.StatusNotFound, CodeNotFound, "host 模型尚未种子导入", nil)
		} else {
			respondError(c, http.StatusInternalServerError, CodeInternal, "查询 host 模型失败", nil)
		}
		return
	}
	var hosts []store.CI
	if err := s.db.Where("model_id = ? AND status <> ?", model.ID, "retired").
		Order("created_at ASC").Find(&hosts).Error; err != nil {
		respondError(c, http.StatusInternalServerError, CodeInternal, "查询 host CI 失败", nil)
		return
	}

	updated, skipped, todos := 0, 0, 0
	errs := []string{}
	for _, host := range hosts {
		ident := stringAttr(host, "ident")
		if ident == "" {
			skipped++
			continue
		}
		// 未归组主机：治理待办告警（按 detail+未确认去重，dry_run 不落库）。
		if stringAttr(host, "biz_group") == "" {
			todos++
			if !req.DryRun {
				s.writeUngroupedTodo(host, ident)
			}
		}
		target, ok := byIdent[ident]
		if !ok {
			skipped++
			continue
		}
		// 组装回写内容：tags 按 k=v 覆盖合并，note 为负责人+环境。
		tags := mergeTargetTags(string(target.Tags), host)
		note := buildTargetNote(host)
		if len(tags) == 0 && note == "" {
			skipped++
			continue
		}
		if req.DryRun {
			updated++
			continue
		}
		if len(tags) > 0 {
			if err := client.UpdateTargetTags(c.Request.Context(), target.ID, tags); err != nil {
				errs = append(errs, fmt.Sprintf("%s: 回写 tags 失败: %v", ident, err))
				continue
			}
		}
		if note != "" {
			if err := client.UpdateTargetNote(c.Request.Context(), target.ID, note); err != nil {
				errs = append(errs, fmt.Sprintf("%s: 回写 note 失败: %v", ident, err))
				continue
			}
		}
		updated++
	}
	c.JSON(http.StatusOK, gin.H{
		"updated": updated,
		"skipped": skipped,
		"todos":   todos,
		"errors":  errs,
	})
}

// mergeTargetTags 把 host 的 biz_group/owner/env 属性合并进 target 现有标签：
// 同键覆盖、其余原样保留（不做全局去重）；host 三个属性均空时返回 nil（无需回写）。
func mergeTargetTags(existing string, host store.CI) []string {
	newKV := map[string]string{}
	for _, key := range writebackTagKeys {
		if v := stringAttr(host, key); v != "" {
			newKV[key] = v
		}
	}
	if len(newKV) == 0 {
		return nil
	}
	merged := []string{}
	for _, tok := range strings.Fields(existing) {
		k, _, _ := strings.Cut(tok, "=")
		if _, covered := newKV[k]; covered {
			continue // 同键旧值被覆盖
		}
		merged = append(merged, tok)
	}
	for _, key := range writebackTagKeys {
		if v, ok := newKV[key]; ok {
			merged = append(merged, key+"="+v)
		}
	}
	return merged
}

// buildTargetNote 组装 target 备注：负责人+环境（均空时返回空串）。
func buildTargetNote(host store.CI) string {
	parts := []string{}
	if v := stringAttr(host, "owner"); v != "" {
		parts = append(parts, "负责人:"+v)
	}
	if v := stringAttr(host, "env"); v != "" {
		parts = append(parts, "环境:"+v)
	}
	return strings.Join(parts, " ")
}

// writeUngroupedTodo 为未归组主机写一条 info 级治理待办告警（按 detail+未确认去重）。
func (s *Server) writeUngroupedTodo(host store.CI, ident string) {
	detail := fmt.Sprintf("主机 ident=%s 无 biz_group 属性，未归入任何 n9e 业务组，请补齐业务归属", ident)
	var existing int64
	if err := s.db.Model(&store.AlertEvent{}).
		Where("detail = ? AND acknowledged = ?", detail, false).
		Count(&existing).Error; err != nil || existing > 0 {
		return
	}
	_ = s.db.Create(&store.AlertEvent{
		Level:  store.AlertLevelInfo,
		Title:  "治理待办：未归组主机",
		Source: "n9e-writeback",
		CIID:   host.ID,
		Detail: detail,
	}).Error
}

// handleN9EDashboardURL 处理 GET /api/v1/integrations/n9e/dashboard-url?ident=（F-063）：
// 拼 n9e 主机仪表盘链接返回给前端 iframe 嵌入。
func (s *Server) handleN9EDashboardURL(c *gin.Context) {
	ident := c.Query("ident")
	if ident == "" {
		respondError(c, http.StatusBadRequest, CodeBadRequest, "ident 查询参数必填", nil)
		return
	}
	client, ok := s.resolveN9EClient(c)
	if !ok {
		return
	}
	c.JSON(http.StatusOK, gin.H{"url": client.BaseURL() + "/dashboards/host?ident=" + url.QueryEscape(ident)})
}

// handleN9EAlerts 处理 GET /api/v1/integrations/n9e/alerts?ident=（F-063）：
// 代理 n9e /api/n9e/alert-cur-events，解开官方响应壳 {"dat":[...],"err":""}
// 返回裸数组（前端按数组消费；告警原文不入主库）。
func (s *Server) handleN9EAlerts(c *gin.Context) {
	ident := c.Query("ident")
	if ident == "" {
		respondError(c, http.StatusBadRequest, CodeBadRequest, "ident 查询参数必填", nil)
		return
	}
	client, ok := s.resolveN9EClient(c)
	if !ok {
		return
	}
	raw, err := client.AlertCurEvents(c.Request.Context(), ident)
	if err != nil {
		respondError(c, http.StatusBadGateway, CodeInternal, "拉取 n9e 当前告警失败: "+err.Error(), nil)
		return
	}
	// 解开官方信封：{"dat":[...],"err":""} → 裸数组；非信封形态（已是数组）原样透传。
	var envelope struct {
		Dat json.RawMessage `json:"dat"`
		Err string          `json:"err"`
	}
	if err := json.Unmarshal(raw, &envelope); err == nil && len(envelope.Dat) > 0 {
		raw = envelope.Dat
	}
	c.Data(http.StatusOK, "application/json; charset=utf-8", raw)
}
