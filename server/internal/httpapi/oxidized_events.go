// Oxidized 事件回写（/api/v1/integrations/oxidized/events）处理器（F-062）：
// Oxidized webhook 把配置备份/变更事件推回本端点，按 node 名匹配 network_device CI，
// 回写备份元数据（last_backup_at/backup_count/config_source）与变更时间（last_change_at），
// 配置原文不入库；全部写操作留审计。
// 鉴权：不走会话，走共享密钥头 X-Oxidized-Token（env OXIDIZED_WEBHOOK_TOKEN，默认 dev-oxidized-token）。
package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/datatypes"

	"meridian/server/internal/reconcile"
	"meridian/server/internal/store"
)

// Oxidized 事件类型。
const (
	oxidizedEventBackup = "backup"
	oxidizedEventChange = "change"
)

// oxidizedEventRequest 是 Oxidized webhook 的事件报文。
type oxidizedEventRequest struct {
	Node  string `json:"node"`           // 设备名（匹配 network_device CI 的 name/hostname 属性）
	Event string `json:"event"`          // backup/change
	Time  string `json:"time"`           // 事件时间（RFC3339）
	User  string `json:"user,omitempty"` // 变更操作者（change 事件可带）
}

// oxidizedWebhookToken 从请求头校验共享密钥，不符时响应 401。
func (s *Server) oxidizedWebhookToken(c *gin.Context) bool {
	if c.GetHeader("X-Oxidized-Token") != s.oxidizedToken {
		respondError(c, http.StatusUnauthorized, "UNAUTHORIZED", "X-Oxidized-Token 校验失败", nil)
		return false
	}
	return true
}

// handleOxidizedEvent 处理 POST /api/v1/integrations/oxidized/events。
// backup：更新 last_backup_at、backup_count（自增）、config_source=oxidized；
// 同一时间戳重复推送按 last_backup_at 去重幂等（不累加计数）。
// change：更新 last_change_at，并写一条 info 级「配置变更」告警事件。
func (s *Server) handleOxidizedEvent(c *gin.Context) {
	if !s.oxidizedWebhookToken(c) {
		return
	}
	var req oxidizedEventRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		respondError(c, http.StatusBadRequest, CodeBadRequest, "请求体解析失败: "+err.Error(), nil)
		return
	}
	if req.Node == "" {
		respondError(c, http.StatusBadRequest, CodeValidationFailed, "node 不能为空", map[string]string{"node": "不能为空"})
		return
	}
	if req.Event != oxidizedEventBackup && req.Event != oxidizedEventChange {
		respondError(c, http.StatusBadRequest, CodeValidationFailed,
			fmt.Sprintf("event 取值 %q 非法（backup/change）", req.Event), map[string]string{"event": "非法事件类型"})
		return
	}
	eventTime, err := time.Parse(time.RFC3339, req.Time)
	if err != nil {
		respondError(c, http.StatusBadRequest, CodeValidationFailed,
			"time 必须为 RFC3339 格式", map[string]string{"time": "非法时间格式"})
		return
	}
	eventAt := eventTime.UTC().Format(time.RFC3339)

	// 按 node 名匹配 network_device CI（name 或 hostname 属性）。
	ci, ok := s.findNetworkDeviceByNode(c, req.Node)
	if !ok {
		return
	}

	switch req.Event {
	case oxidizedEventBackup:
		s.applyOxidizedBackup(c, ci, eventAt)
	case oxidizedEventChange:
		s.applyOxidizedChange(c, ci, eventAt, req.User)
	}
}

// findNetworkDeviceByNode 按 name/hostname 属性匹配 network_device CI；未命中响应 404。
func (s *Server) findNetworkDeviceByNode(c *gin.Context, node string) (store.CI, bool) {
	var model store.Model
	if err := s.db.First(&model, "code = ?", "network_device").Error; err != nil {
		if isNotFound(err) {
			respondError(c, http.StatusNotFound, CodeNotFound, "network_device 模型尚未种子导入", nil)
		} else {
			respondError(c, http.StatusInternalServerError, CodeInternal, "查询网络设备模型失败", nil)
		}
		return store.CI{}, false
	}
	var ci store.CI
	// 分组条件：model+status 为外层约束，(name=? OR hostname=?) 为内层匹配。
	nameOrHostname := s.db.Where(datatypes.JSONQuery("attributes").Equals(node, "name")).
		Or(datatypes.JSONQuery("attributes").Equals(node, "hostname"))
	err := s.db.Where("model_id = ? AND status <> ?", model.ID, "retired").
		Where(nameOrHostname).
		First(&ci).Error
	if err != nil {
		if isNotFound(err) {
			respondError(c, http.StatusNotFound, CodeNotFound, fmt.Sprintf("未找到 node=%q 对应的网络设备 CI", node), nil)
		} else {
			respondError(c, http.StatusInternalServerError, CodeInternal, "查询网络设备 CI 失败", nil)
		}
		return store.CI{}, false
	}
	return ci, true
}

// applyOxidizedBackup 应用 backup 事件：幂等去重后回写备份元数据并留审计。
func (s *Server) applyOxidizedBackup(c *gin.Context, ci store.CI, eventAt string) {
	// 幂等：同一时间戳的重复推送不累加 backup_count。
	if stringAttr(ci, "last_backup_at") == eventAt {
		c.JSON(http.StatusOK, gin.H{"status": "ok", "ci_id": ci.ID, "idempotent": true})
		return
	}
	changes := map[string]reconcile.Change{
		"last_backup_at": {Old: ci.Attributes["last_backup_at"], New: eventAt},
		"backup_count":   {Old: ci.Attributes["backup_count"], New: intAttr(ci, "backup_count") + 1},
		"config_source":  {Old: ci.Attributes["config_source"], New: "oxidized"},
	}
	s.applyOxidizedAttrs(c, ci, changes, "Oxidized 配置备份完成")
}

// intAttr 读取 CI 整数属性：容忍 JSON 反序列化的 float64/json.Number 形态。
func intAttr(ci store.CI, key string) int {
	switch v := ci.Attributes[key].(type) {
	case float64:
		return int(v)
	case json.Number:
		n, _ := v.Int64()
		return int(n)
	case int:
		return v
	}
	return 0
}

// applyOxidizedChange 应用 change 事件：回写变更时间、写配置变更告警并留审计。
func (s *Server) applyOxidizedChange(c *gin.Context, ci store.CI, eventAt, user string) {
	changes := map[string]reconcile.Change{
		"last_change_at": {Old: ci.Attributes["last_change_at"], New: eventAt},
		"config_source":  {Old: ci.Attributes["config_source"], New: "oxidized"},
	}
	s.applyOxidizedAttrs(c, ci, changes, "Oxidized 检测到配置变更")

	detail := fmt.Sprintf("设备 %s 配置发生变更（时间=%s）", firstStringAttr(ci, "name", "hostname"), eventAt)
	if user != "" {
		detail += fmt.Sprintf("，操作者=%s", user)
	}
	if err := s.db.Create(&store.AlertEvent{
		Level:  store.AlertLevelInfo,
		Title:  "配置变更",
		Source: "oxidized",
		CIID:   ci.ID,
		Detail: detail,
	}).Error; err != nil {
		respondError(c, http.StatusInternalServerError, CodeInternal, "写入配置变更告警失败", nil)
		return
	}
}

// applyOxidizedAttrs 把变更写入 CI 属性（field_sources 记 oxidized）并留审计，响应 200。
func (s *Server) applyOxidizedAttrs(c *gin.Context, ci store.CI, changes map[string]reconcile.Change, message string) {
	attrs := datatypes.JSONMap{}
	for k, v := range ci.Attributes {
		attrs[k] = v
	}
	for k, ch := range changes {
		attrs[k] = ch.New
	}
	sources := datatypes.JSONMap{}
	for k, v := range ci.FieldSources {
		sources[k] = v
	}
	for k := range changes {
		sources[k] = "oxidized"
	}
	if err := s.db.Model(&store.CI{}).Where("id = ?", ci.ID).
		Updates(map[string]any{"attributes": attrs, "field_sources": sources}).Error; err != nil {
		respondError(c, http.StatusInternalServerError, CodeInternal, "更新 CI 备份元数据失败", nil)
		return
	}
	writeAuditLog(s.db, ci.ID, "update", "oxidized", "system", changes, message)
	c.JSON(http.StatusOK, gin.H{"status": "ok", "ci_id": ci.ID, "idempotent": false})
}
