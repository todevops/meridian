// 应用归属引擎（/api/v1/attribution/run）处理器（F-028）：
// 按规则顺序把 host CI 挂接到 biz_app CI（deployed_on 关系，幂等）：
//
//	a) 标签继承：host attributes.tags 含 app=<code> → 挂到 code 匹配的 biz_app；
//	b) 业务组映射：biz_group 经 group_map 映射 biz_app code；
//	c) 命名规范：ident 匹配 ^([a-z0-9]+)-([a-z]+)，取第二段经 group_map 尝试。
package httpapi

import (
	"net/http"
	"regexp"
	"strings"

	"github.com/gin-gonic/gin"

	"meridian/server/internal/store"
)

// 归属规则标识（rules_hit 的键）。
const (
	attributionRuleTagInherit = "tag_inherit"
	attributionRuleGroupMap   = "group_map"
	attributionRuleNaming     = "naming"
)

// defaultGroupMap 是默认业务组 → biz_app code 映射（可被请求 rules.group_map 整体覆盖）。
var defaultGroupMap = map[string]string{
	"电商前台": "mall-front",
	"订单中台": "mall-order",
}

// identNamingPattern 命名规范：<前缀>-<业务段>（业务段供 group_map 尝试）。
var identNamingPattern = regexp.MustCompile(`^([a-z0-9]+)-([a-z]+)`)

// attributionRunRequest 与 AttributionRunRequest 对应。
type attributionRunRequest struct {
	DryRun bool `json:"dry_run,omitempty"`
	Rules  struct {
		GroupMap map[string]string `json:"group_map,omitempty"`
	} `json:"rules,omitempty"`
}

// handleAttributionRun 处理 POST /api/v1/attribution/run。
// 返回 {matched, rules_hit, unmatched[]}；dry_run 只演练不落库。
func (s *Server) handleAttributionRun(c *gin.Context) {
	var req attributionRunRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		respondError(c, http.StatusBadRequest, CodeBadRequest, "请求体解析失败: "+err.Error(), nil)
		return
	}
	groupMap := defaultGroupMap
	if req.Rules.GroupMap != nil {
		groupMap = req.Rules.GroupMap
	}

	// biz_app CI 按 attributes.code 建索引。
	appModel, ok := s.loadModelByCode(c, "biz_app")
	if !ok {
		return
	}
	var apps []store.CI
	if err := s.db.Where("model_id = ? AND status <> ?", appModel.ID, "retired").Find(&apps).Error; err != nil {
		respondError(c, http.StatusInternalServerError, CodeInternal, "查询 biz_app CI 失败", nil)
		return
	}
	appByCode := map[string]store.CI{}
	for _, app := range apps {
		if code := stringAttr(app, "code"); code != "" {
			appByCode[code] = app
		}
	}

	hostModel, ok := s.loadModelByCode(c, "host")
	if !ok {
		return
	}
	var hosts []store.CI
	if err := s.db.Where("model_id = ? AND status <> ?", hostModel.ID, "retired").
		Order("created_at ASC").Find(&hosts).Error; err != nil {
		respondError(c, http.StatusInternalServerError, CodeInternal, "查询 host CI 失败", nil)
		return
	}

	matched := 0
	rulesHit := map[string]int{
		attributionRuleTagInherit: 0,
		attributionRuleGroupMap:   0,
		attributionRuleNaming:     0,
	}
	unmatched := []string{}
	for _, host := range hosts {
		app, rule := matchBizApp(host, appByCode, groupMap)
		if rule == "" {
			unmatched = append(unmatched, defaultString(stringAttr(host, "ident"), host.ID))
			continue
		}
		if !req.DryRun {
			if err := s.ensureDeployedOn(app.ID, host.ID); err != nil {
				respondError(c, http.StatusInternalServerError, CodeInternal, "创建 deployed_on 关系失败: "+err.Error(), nil)
				return
			}
		}
		matched++
		rulesHit[rule]++
	}
	c.JSON(http.StatusOK, gin.H{
		"matched":   matched,
		"rules_hit": rulesHit,
		"unmatched": unmatched,
	})
}

// matchBizApp 按规则顺序为 host 匹配 biz_app；未命中返回空 rule。
func matchBizApp(host store.CI, appByCode map[string]store.CI, groupMap map[string]string) (store.CI, string) {
	// a) 标签继承：tags（逗号/空格分隔）中的 app=<code>。
	for _, tok := range strings.FieldsFunc(stringAttr(host, "tags"), func(r rune) bool {
		return r == ',' || r == ' '
	}) {
		if code, ok := strings.CutPrefix(tok, "app="); ok {
			if app, exists := appByCode[code]; exists {
				return app, attributionRuleTagInherit
			}
		}
	}
	// b) 业务组映射：biz_group 经 group_map。
	if bg := stringAttr(host, "biz_group"); bg != "" {
		if code, ok := groupMap[bg]; ok {
			if app, exists := appByCode[code]; exists {
				return app, attributionRuleGroupMap
			}
		}
	}
	// c) 命名规范：ident 第二段经 group_map 尝试。
	if m := identNamingPattern.FindStringSubmatch(stringAttr(host, "ident")); m != nil {
		if code, ok := groupMap[m[2]]; ok {
			if app, exists := appByCode[code]; exists {
				return app, attributionRuleNaming
			}
		}
	}
	return store.CI{}, ""
}

// ensureDeployedOn 为 biz_app CI 建 deployed_on→host 关系（按三元组去重，幂等）。
func (s *Server) ensureDeployedOn(appCIID, hostCIID string) error {
	var count int64
	if err := s.db.Model(&store.CIRelation{}).
		Where("relation_code = ? AND src_ci_id = ? AND dst_ci_id = ?", "deployed_on", appCIID, hostCIID).
		Count(&count).Error; err != nil {
		return err
	}
	if count > 0 {
		return nil
	}
	return s.db.Create(&store.CIRelation{
		RelationCode: "deployed_on",
		SrcCIID:      appCIID,
		DstCIID:      hostCIID,
	}).Error
}

// loadModelByCode 按编码加载模型；未找到响应 404（模型未种子导入）。
func (s *Server) loadModelByCode(c *gin.Context, code string) (store.Model, bool) {
	var model store.Model
	err := s.db.First(&model, "code = ?", code).Error
	if err != nil {
		if isNotFound(err) {
			respondError(c, http.StatusNotFound, CodeNotFound, code+" 模型尚未种子导入", nil)
		} else {
			respondError(c, http.StatusInternalServerError, CodeInternal, "查询 "+code+" 模型失败", nil)
		}
		return store.Model{}, false
	}
	return model, true
}
