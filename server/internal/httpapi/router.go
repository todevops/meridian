// Package httpapi 实现 CMDB 核心 REST API（契约见 pkg/openapi/openapi.yaml）。
package httpapi

import (
	"errors"
	"net/http"
	"os"
	"strconv"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"meridian/server/internal/auth"
	"meridian/server/internal/credentials"
	"meridian/server/internal/dcim"
	"meridian/server/internal/discovery"
	"meridian/server/internal/ipam"
	"meridian/server/internal/n9e"
	"meridian/server/internal/scheduler"
)

// 错误码（机器可读）。
const (
	CodeBadRequest       = "BAD_REQUEST"
	CodeValidationFailed = "VALIDATION_FAILED"
	CodeNotFound         = "NOT_FOUND"
	CodeConflict         = "CONFLICT"
	CodeInternal         = "INTERNAL"
)

// Server 聚合 HTTP 层依赖。
type Server struct {
	db         *gorm.DB
	pipeline   *discovery.Pipeline
	auth       *auth.Service
	ipam       *ipam.Service
	dcim       *dcim.Service
	credCipher *credentials.Cipher
	scheduler  *scheduler.Scheduler
	// n9eClient 为 n9e REST 客户端（可空：未配置 N9E_API_URL/TOKEN 时，
	// 质量看板反向监控指标缺省、退役联动 n9e 动作报告跳过）。
	n9eClient *n9e.Client
	// oxidizedToken 为 Oxidized webhook 共享密钥（env OXIDIZED_WEBHOOK_TOKEN，默认 dev-oxidized-token）。
	oxidizedToken string
}

// NewRouter 构建完整路由（健康检查 + /api/v1 业务接口）。
// 除 /healthz、/readyz 与 /api/v1/auth/login 外，所有接口需认证；
// 业务接口按权限点鉴权（权限点目录见 auth 包 catalog）。
func NewRouter(db *gorm.DB, pipeline *discovery.Pipeline, authSvc *auth.Service, credCipher *credentials.Cipher, sched *scheduler.Scheduler, n9eClient *n9e.Client) *gin.Engine {
	s := &Server{
		db:            db,
		pipeline:      pipeline,
		auth:          authSvc,
		ipam:          ipam.NewService(db),
		dcim:          dcim.NewService(db),
		credCipher:    credCipher,
		scheduler:     sched,
		n9eClient:     n9eClient,
		oxidizedToken: defaultStringEnv("OXIDIZED_WEBHOOK_TOKEN", "dev-oxidized-token"),
	}

	r := gin.Default()

	// 健康检查：进程存活即返回 ok。
	r.GET("/healthz", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	})
	// 就绪检查：ping 数据库。
	r.GET("/readyz", func(c *gin.Context) {
		sqlDB, err := db.DB()
		if err != nil || sqlDB.Ping() != nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"status": "unavailable"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	})

	v1 := r.Group("/api/v1")
	// 登录是唯一无需认证的接口。
	v1.POST("/auth/login", s.login)
	// Oxidized 事件回写（F-062）：不走会话，处理器内校验 X-Oxidized-Token 共享密钥。
	v1.POST("/integrations/oxidized/events", s.handleOxidizedEvent)

	authed := v1.Group("", authSvc.AuthRequired())
	{
		authed.POST("/auth/logout", s.logout)
		authed.GET("/auth/me", s.me)
		authed.GET("/permissions", s.listPermissions)

		authed.GET("/models", s.require("model:read"), s.listModels)
		authed.POST("/models", s.require("model:write"), s.createModel)
		authed.GET("/models/:model_id", s.require("model:read"), s.getModel)
		authed.PATCH("/models/:model_id", s.require("model:write"), s.patchModel)

		authed.GET("/cis", s.require("ci:read"), s.listCIs)
		// 全局搜索：登录即可用，分组按权限点在处理器内裁剪
		authed.GET("/search", s.globalSearch)
		authed.POST("/cis", s.require("ci:write"), s.createCI)
		authed.GET("/cis/:ci_id", s.require("ci:read"), s.getCI)
		authed.PATCH("/cis/:ci_id", s.require("ci:write"), s.patchCI)
		authed.GET("/cis/:ci_id/relations", s.require("ci:read"), s.listCIRelations)
		authed.POST("/cis/:ci_id/relations", s.require("ci:write"), s.createCIRelation)
		authed.DELETE("/cis/:ci_id/relations/:relation_code/:peer_ci_id", s.require("ci:write"), s.deleteCIRelation)

		authed.POST("/discovery-records", s.require("discovery:write"), s.createDiscoveryRecords)
		authed.POST("/reconcile/preview", s.require("discovery:read"), s.previewReconcile)

		authed.GET("/discovery-pool", s.require("discovery:read"), s.listPoolItems)
		authed.POST("/discovery-pool/:id/confirm", s.require("discovery:write"), s.confirmPoolItem)
		authed.POST("/discovery-pool/:id/ignore", s.require("discovery:write"), s.ignorePoolItem)

		// 凭据纳管（F-005）：secret 永不回明文。
		authed.GET("/credentials", s.require("credential:read"), s.listCredentials)
		authed.POST("/credentials", s.require("credential:write"), s.createCredential)
		authed.PATCH("/credentials/:credential_id", s.require("credential:write"), s.patchCredential)
		authed.POST("/credentials/:credential_id/rotate", s.require("credential:write"), s.rotateCredential)
		authed.GET("/credentials/:credential_id/audits", s.require("credential:read"), s.listCredentialAudits)

		// 采集任务（F-033）：CRUD + 手动触发 + 执行记录。
		authed.GET("/discovery/tasks", s.require("task:read"), s.listTasks)
		authed.POST("/discovery/tasks", s.require("task:write"), s.createTask)
		authed.PATCH("/discovery/tasks/:task_id", s.require("task:write"), s.patchTask)
		authed.POST("/discovery/tasks/:task_id/run", s.require("task:write"), s.runTask)
		authed.GET("/discovery/tasks/:task_id/runs", s.require("task:read"), s.listTaskRuns)

		authed.GET("/ipam/prefixes", s.require("ipam:read"), s.listPrefixes)
		authed.POST("/ipam/prefixes", s.require("ipam:write"), s.createPrefix)
		authed.GET("/ipam/prefixes/:prefix_id", s.require("ipam:read"), s.getPrefix)
		authed.POST("/ipam/prefixes/:prefix_id/allocate", s.require("ipam:write"), s.allocateIPs)
		authed.GET("/ipam/ips", s.require("ipam:read"), s.listIPs)
		authed.POST("/ipam/ips", s.require("ipam:write"), s.createIP)
		authed.PATCH("/ipam/ips/:ip_id", s.require("ipam:write"), s.patchIP)

		authed.GET("/dcim/overview", s.require("dcim:read"), s.getDCIMOverview)
		authed.GET("/dcim/racks/:ci_id/units", s.require("dcim:read"), s.getRackUnits)
		authed.POST("/dcim/racks/:ci_id/mount", s.require("dcim:write"), s.mountRackUnit)
		authed.POST("/dcim/racks/:ci_id/unmount", s.require("dcim:write"), s.unmountRackUnit)

		authed.GET("/integrations/oxidized/devices", s.require("ci:read"), s.listOxidizedDevices)

		// n9e 集成：上行回写（F-070）与嵌入代理（F-063）。
		authed.POST("/integrations/n9e/writeback", s.require("ci:write"), s.handleN9EWriteback)
		authed.GET("/integrations/n9e/dashboard-url", s.require("ci:read"), s.handleN9EDashboardURL)
		authed.GET("/integrations/n9e/alerts", s.require("ci:read"), s.handleN9EAlerts)

		// 应用归属引擎（F-028）：按规则把 host 挂接到 biz_app。
		authed.POST("/attribution/run", s.require("ci:write"), s.handleAttributionRun)

		// 告警事件（2B）：黑设备等风险线索的查询与确认。
		authed.GET("/alerts", s.require("alert:read"), s.listAlerts)
		authed.POST("/alerts/:alert_id/ack", s.require("alert:write"), s.ackAlert)

		// 数据质量看板（F-080）：五指标汇总与下钻缺失清单。
		authed.GET("/dashboard/quality", s.require("dashboard:read"), s.getQualityDashboard)
		authed.GET("/dashboard/quality/drilldown", s.require("dashboard:read"), s.getQualityDrilldown)

		// 稽核规则与整改待办（F-081）。
		authed.GET("/governance/rules", s.require("governance:read"), s.listAuditRules)
		authed.POST("/governance/rules", s.require("governance:write"), s.createAuditRule)
		authed.PATCH("/governance/rules/:id", s.require("governance:write"), s.patchAuditRule)
		authed.POST("/governance/rules/:id/run", s.require("governance:write"), s.runAuditRule)
		authed.GET("/governance/todos", s.require("governance:read"), s.listGovernanceTodos)
		authed.POST("/governance/todos/:id/close", s.require("governance:write"), s.closeGovernanceTodo)

		// 生命周期（F-026）：状态流转、退役会签与联动执行。
		authed.POST("/cis/:ci_id/lifecycle", s.require("lifecycle:write"), s.transitionCI)
		authed.GET("/lifecycle/retire-candidates", s.require("governance:read"), s.retireCandidates)
		authed.POST("/lifecycle/retire", s.require("lifecycle:write"), s.retireCI)

		// 审计日志查询（F-004）。
		authed.GET("/audit", s.require("audit:read"), s.listAuditLogs)

		authed.GET("/users", s.require("user:manage"), s.listUsers)
		authed.POST("/users", s.require("user:manage"), s.createUser)
		authed.GET("/users/:user_id", s.require("user:manage"), s.getUser)
		authed.PATCH("/users/:user_id", s.require("user:manage"), s.patchUser)

		authed.GET("/roles", s.require("role:manage"), s.listRoles)
		authed.POST("/roles", s.require("role:manage"), s.createRole)
		authed.GET("/roles/:role_id", s.require("role:manage"), s.getRole)
		authed.PATCH("/roles/:role_id", s.require("role:manage"), s.patchRole)
		authed.DELETE("/roles/:role_id", s.require("role:manage"), s.deleteRole)
	}
	return r
}

// require 按权限点编码（"obj:act"）生成鉴权中间件。
func (s *Server) require(permCode string) gin.HandlerFunc {
	obj, act := auth.SplitPermission(permCode)
	return s.auth.RequirePermission(obj, act)
}

// respondError 按契约 Error schema 返回错误。
func respondError(c *gin.Context, status int, code, message string, details any) {
	body := gin.H{"code": code, "message": message}
	if details != nil {
		body["details"] = details
	}
	c.JSON(status, body)
}

// parsePage 解析分页参数：page 从 1 开始默认 1，page_size 默认 20、上限 200。
func parsePage(c *gin.Context) (page, pageSize int, ok bool) {
	page, pageSize = 1, 20
	if v := c.Query("page"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			respondError(c, http.StatusBadRequest, CodeBadRequest, "page 必须为 >= 1 的整数", nil)
			return 0, 0, false
		}
		page = n
	}
	if v := c.Query("page_size"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 200 {
			respondError(c, http.StatusBadRequest, CodeBadRequest, "page_size 必须为 1-200 的整数", nil)
			return 0, 0, false
		}
		pageSize = n
	}
	return page, pageSize, true
}

// isNotFound 判定 GORM 错误是否为记录不存在。
func isNotFound(err error) bool {
	return errors.Is(err, gorm.ErrRecordNotFound)
}

// stringSlice 保证空切片序列化为 [] 而非 null（契约要求数组）。
func stringSlice(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// defaultStringEnv 读取环境变量，未设置时返回默认值。
func defaultStringEnv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
