// Package httpapi 实现 CMDB 核心 REST API（契约见 pkg/openapi/openapi.yaml）。
package httpapi

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"meridian/server/internal/auth"
	"meridian/server/internal/dcim"
	"meridian/server/internal/discovery"
	"meridian/server/internal/ipam"
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
	db       *gorm.DB
	pipeline *discovery.Pipeline
	auth     *auth.Service
	ipam     *ipam.Service
	dcim     *dcim.Service
}

// NewRouter 构建完整路由（健康检查 + /api/v1 业务接口）。
// 除 /healthz、/readyz 与 /api/v1/auth/login 外，所有接口需认证；
// 业务接口按权限点鉴权（权限点目录见 auth 包 catalog）。
func NewRouter(db *gorm.DB, pipeline *discovery.Pipeline, authSvc *auth.Service) *gin.Engine {
	s := &Server{
		db:       db,
		pipeline: pipeline,
		auth:     authSvc,
		ipam:     ipam.NewService(db),
		dcim:     dcim.NewService(db),
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
