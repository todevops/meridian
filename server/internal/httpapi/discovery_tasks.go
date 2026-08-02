// 采集任务（/api/v1/discovery/tasks）处理器（F-033）：
// 任务 CRUD、手动触发执行与执行记录查询；周期调度由 scheduler 包承载。
package httpapi

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"gorm.io/datatypes"

	"meridian/server/internal/scheduler"
	"meridian/server/internal/store"
)

// taskSaveRequest 与 DiscoveryTaskCreateRequest / DiscoveryTaskPatchRequest 对应。
// PATCH 场景 nil 字段表示不修改。
type taskSaveRequest struct {
	Name            *string        `json:"name"`
	CollectorType   *string        `json:"collector_type"`
	CredentialID    *string        `json:"credential_id"`
	IntervalSeconds *int           `json:"interval_seconds"`
	Enabled         *bool          `json:"enabled"`
	Config          map[string]any `json:"config"`
}

// validateCollectorType 校验采集器类型：builtin:n9e-consumer 或 exec:<binary>。
func validateCollectorType(t string) string {
	if t == store.CollectorBuiltinN9E {
		return ""
	}
	if rest, ok := strings.CutPrefix(t, store.CollectorTypeExecPrefix); ok && strings.TrimSpace(rest) != "" {
		return ""
	}
	return fmt.Sprintf("collector_type 取值 %q 非法（支持 %s 或 exec:<binary>）", t, store.CollectorBuiltinN9E)
}

// listTasks 处理 GET /api/v1/discovery/tasks：enabled/collector_type 过滤 + 分页。
func (s *Server) listTasks(c *gin.Context) {
	page, pageSize, ok := parsePage(c)
	if !ok {
		return
	}
	q := s.db.Model(&store.DiscoveryTask{})
	if v := c.Query("enabled"); v != "" {
		if v != "true" && v != "false" {
			respondError(c, http.StatusBadRequest, CodeBadRequest, "enabled 取值须为 true/false", nil)
			return
		}
		q = q.Where("enabled = ?", v == "true")
	}
	if v := c.Query("collector_type"); v != "" {
		q = q.Where("collector_type = ?", v)
	}
	var total int64
	if err := q.Count(&total).Error; err != nil {
		respondError(c, http.StatusInternalServerError, CodeInternal, "查询任务总数失败", nil)
		return
	}
	var rows []store.DiscoveryTask
	if err := q.Order("created_at DESC").Offset((page - 1) * pageSize).Limit(pageSize).Find(&rows).Error; err != nil {
		respondError(c, http.StatusInternalServerError, CodeInternal, "查询任务列表失败", nil)
		return
	}
	items := []store.DiscoveryTask{}
	items = append(items, rows...)
	c.JSON(http.StatusOK, gin.H{"items": items, "total": total, "page": page, "page_size": pageSize})
}

// createTask 处理 POST /api/v1/discovery/tasks。
func (s *Server) createTask(c *gin.Context) {
	var req taskSaveRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		respondError(c, http.StatusBadRequest, CodeBadRequest, "请求体解析失败: "+err.Error(), nil)
		return
	}
	if req.Name == nil || strings.TrimSpace(*req.Name) == "" {
		respondError(c, http.StatusBadRequest, CodeValidationFailed, "name 不能为空", map[string]string{"name": "不能为空"})
		return
	}
	if req.CollectorType == nil {
		respondError(c, http.StatusBadRequest, CodeValidationFailed, "collector_type 必填", map[string]string{"collector_type": "必填"})
		return
	}
	if msg := validateCollectorType(*req.CollectorType); msg != "" {
		respondError(c, http.StatusBadRequest, CodeValidationFailed, msg, map[string]string{"collector_type": "非法类型"})
		return
	}
	interval := 300
	if req.IntervalSeconds != nil {
		if *req.IntervalSeconds < 10 {
			respondError(c, http.StatusBadRequest, CodeValidationFailed,
				"interval_seconds 须 >= 10（调度粒度 10 秒）", map[string]string{"interval_seconds": "须 >= 10"})
			return
		}
		interval = *req.IntervalSeconds
	}
	if req.CredentialID != nil && *req.CredentialID != "" {
		if _, ok := s.resolveCredential(c, *req.CredentialID); !ok {
			return
		}
	}
	cfg := datatypes.JSONMap{}
	if req.Config != nil {
		cfg = datatypes.JSONMap(req.Config)
	}
	enabled := false
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	task := store.DiscoveryTask{
		Name:            strings.TrimSpace(*req.Name),
		CollectorType:   *req.CollectorType,
		IntervalSeconds: interval,
		Enabled:         enabled,
		Config:          cfg,
		Status:          store.TaskStatusIdle,
	}
	if req.CredentialID != nil && *req.CredentialID != "" {
		task.CredentialID = req.CredentialID
	}
	if err := s.db.Create(&task).Error; err != nil {
		respondError(c, http.StatusInternalServerError, CodeInternal, "创建任务失败", nil)
		return
	}
	c.JSON(http.StatusCreated, task)
}

// patchTask 处理 PATCH /api/v1/discovery/tasks/{task_id}。
func (s *Server) patchTask(c *gin.Context) {
	task, ok := s.resolveTask(c, c.Param("task_id"))
	if !ok {
		return
	}
	var req taskSaveRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		respondError(c, http.StatusBadRequest, CodeBadRequest, "请求体解析失败: "+err.Error(), nil)
		return
	}
	updates := map[string]any{}
	if req.Name != nil {
		if strings.TrimSpace(*req.Name) == "" {
			respondError(c, http.StatusBadRequest, CodeValidationFailed, "name 不能为空", map[string]string{"name": "不能为空"})
			return
		}
		updates["name"] = strings.TrimSpace(*req.Name)
	}
	if req.CollectorType != nil {
		if task.Status == store.TaskStatusRunning {
			respondError(c, http.StatusConflict, CodeConflict, "任务运行中，不允许修改 collector_type", nil)
			return
		}
		if msg := validateCollectorType(*req.CollectorType); msg != "" {
			respondError(c, http.StatusBadRequest, CodeValidationFailed, msg, map[string]string{"collector_type": "非法类型"})
			return
		}
		updates["collector_type"] = *req.CollectorType
	}
	if req.IntervalSeconds != nil {
		if *req.IntervalSeconds < 10 {
			respondError(c, http.StatusBadRequest, CodeValidationFailed,
				"interval_seconds 须 >= 10（调度粒度 10 秒）", map[string]string{"interval_seconds": "须 >= 10"})
			return
		}
		updates["interval_seconds"] = *req.IntervalSeconds
	}
	if req.Enabled != nil {
		updates["enabled"] = *req.Enabled
	}
	if req.Config != nil {
		updates["config"] = datatypes.JSONMap(req.Config)
	}
	if req.CredentialID != nil {
		if *req.CredentialID == "" {
			updates["credential_id"] = nil // 显式置空表示解除凭据关联
		} else {
			if _, ok := s.resolveCredential(c, *req.CredentialID); !ok {
				return
			}
			updates["credential_id"] = *req.CredentialID
		}
	}
	if len(updates) > 0 {
		if err := s.db.Model(&store.DiscoveryTask{}).Where("id = ?", task.ID).Updates(updates).Error; err != nil {
			respondError(c, http.StatusInternalServerError, CodeInternal, "更新任务失败", nil)
			return
		}
	}
	if err := s.db.First(&task, "id = ?", task.ID).Error; err != nil {
		respondError(c, http.StatusInternalServerError, CodeInternal, "查询任务失败", nil)
		return
	}
	c.JSON(http.StatusOK, task)
}

// runTask 处理 POST /api/v1/discovery/tasks/{task_id}/run：手动触发（同步执行）。
func (s *Server) runTask(c *gin.Context) {
	if _, ok := s.resolveTask(c, c.Param("task_id")); !ok {
		return
	}
	run, err := s.scheduler.RunTask(c.Request.Context(), c.Param("task_id"))
	if err == scheduler.ErrTaskRunning {
		respondError(c, http.StatusConflict, CodeConflict, "任务正在运行中，请稍后重试", nil)
		return
	}
	if err != nil && run.ID == "" {
		// 执行器未启动（如任务不存在/执行记录创建失败）。
		respondError(c, http.StatusInternalServerError, CodeInternal, "任务执行失败: "+err.Error(), nil)
		return
	}
	// 执行器层面的失败（采集报错/超时）已落执行记录，HTTP 仍返回 200 + 执行记录。
	c.JSON(http.StatusOK, run)
}

// listTaskRuns 处理 GET /api/v1/discovery/tasks/{task_id}/runs：分页执行记录。
func (s *Server) listTaskRuns(c *gin.Context) {
	task, ok := s.resolveTask(c, c.Param("task_id"))
	if !ok {
		return
	}
	page, pageSize, ok := parsePage(c)
	if !ok {
		return
	}
	q := s.db.Model(&store.DiscoveryTaskRun{}).Where("task_id = ?", task.ID)
	var total int64
	if err := q.Count(&total).Error; err != nil {
		respondError(c, http.StatusInternalServerError, CodeInternal, "查询执行记录总数失败", nil)
		return
	}
	var rows []store.DiscoveryTaskRun
	if err := q.Order("started_at DESC").Offset((page - 1) * pageSize).Limit(pageSize).Find(&rows).Error; err != nil {
		respondError(c, http.StatusInternalServerError, CodeInternal, "查询执行记录失败", nil)
		return
	}
	items := []store.DiscoveryTaskRun{}
	items = append(items, rows...)
	c.JSON(http.StatusOK, gin.H{"items": items, "total": total, "page": page, "page_size": pageSize})
}

// resolveTask 加载采集任务：不存在 404。
func (s *Server) resolveTask(c *gin.Context, id string) (store.DiscoveryTask, bool) {
	var task store.DiscoveryTask
	err := s.db.First(&task, "id = ?", id).Error
	if err != nil {
		if isNotFound(err) {
			respondError(c, http.StatusNotFound, CodeNotFound, fmt.Sprintf("采集任务 %q 不存在", id), nil)
		} else {
			respondError(c, http.StatusInternalServerError, CodeInternal, "查询采集任务失败", nil)
		}
		return store.DiscoveryTask{}, false
	}
	return task, true
}
