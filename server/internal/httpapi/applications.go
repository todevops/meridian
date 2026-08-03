// 应用系统聚合（/api/v1/applications/*、/api/v1/cis/{id}/impact）处理器（F-027）。
// 查询逻辑全部在 internal/aggregation（批量预取，禁 N+1），此处只做参数与错误映射。
package httpapi

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"

	"meridian/server/internal/aggregation"
	"meridian/server/internal/jssync"
)

// getApplicationTree 处理 GET /api/v1/applications/tree：两级业务树
// （biz_line 节点含应用数/主机数汇总，无归属应用进 unassigned 组）。
func (s *Server) getApplicationTree(c *gin.Context) {
	view, err := aggregation.NewService(s.db).Tree(c.Request.Context())
	if err != nil {
		respondError(c, http.StatusInternalServerError, CodeInternal, "查询业务树失败: "+err.Error(), nil)
		return
	}
	c.JSON(http.StatusOK, view)
}

// getApplicationAggregate 处理 GET /api/v1/applications/{id}/aggregate：应用详情一屏聚合。
func (s *Server) getApplicationAggregate(c *gin.Context) {
	view, err := aggregation.NewService(s.db).Aggregate(c.Request.Context(), c.Param("app_id"))
	if err != nil {
		s.respondAggregationError(c, err, "查询应用聚合失败")
		return
	}
	c.JSON(http.StatusOK, view)
}

// getApplicationDependencies 处理 GET /api/v1/applications/{id}/dependencies：
// 应用↔应用、应用↔DB 依赖拓扑（本应用为心两跳以内）。
func (s *Server) getApplicationDependencies(c *gin.Context) {
	view, err := aggregation.NewService(s.db).Dependencies(c.Request.Context(), c.Param("app_id"))
	if err != nil {
		s.respondAggregationError(c, err, "查询应用依赖拓扑失败")
		return
	}
	c.JSON(http.StatusOK, view)
}

// getCIImpact 处理 GET /api/v1/cis/{ci_id}/impact：资源影响面反查
// （沿 belongs_to/deployed_on/depends_on/runs_on 入向最多两跳列出受影响应用及路径）。
func (s *Server) getCIImpact(c *gin.Context) {
	view, err := aggregation.NewService(s.db).Impact(c.Request.Context(), c.Param("ci_id"))
	if err != nil {
		s.respondAggregationError(c, err, "影响面反查失败")
		return
	}
	c.JSON(http.StatusOK, view)
}

// respondAggregationError 把聚合层错误映射为契约错误响应。
func (s *Server) respondAggregationError(c *gin.Context, err error, prefix string) {
	switch {
	case isNotFound(err):
		respondError(c, http.StatusNotFound, CodeNotFound, "CI 不存在", nil)
	case errors.Is(err, aggregation.ErrNotApp):
		respondError(c, http.StatusBadRequest, CodeValidationFailed, "目标 CI 不是应用（biz_app）", nil)
	default:
		respondError(c, http.StatusInternalServerError, CodeInternal, prefix+": "+err.Error(), nil)
	}
}

// jumpServerSyncRequest 是 JumpServer 同步请求体。
type jumpServerSyncRequest struct {
	DryRun bool `json:"dry_run"`
}

// handleJumpServerSync 处理 POST /api/v1/integrations/jumpserver/sync（F-071）：
// "在用+已归属"主机资产创建/更新，退役/失归属资产禁用；dry_run=true 只预演不写入。
func (s *Server) handleJumpServerSync(c *gin.Context) {
	var req jumpServerSyncRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		respondError(c, http.StatusBadRequest, CodeBadRequest, "请求体解析失败: "+err.Error(), nil)
		return
	}
	client := jssync.ResolveClient(s.db, s.credCipher)
	result, err := jssync.New(s.db, client).Sync(c.Request.Context(), req.DryRun)
	if err != nil {
		respondError(c, http.StatusServiceUnavailable, CodeUpstream, "JumpServer 同步失败: "+err.Error(), nil)
		return
	}
	c.JSON(http.StatusOK, result)
}

// handleUModelStats 处理 GET /api/v1/integrations/umodel/stats（F-073）：
// UModel 生成器统计计数（实体/关联 upsert 累计、tombstone 累计、最近对账时间）。
func (s *Server) handleUModelStats(c *gin.Context) {
	if s.umodelGen == nil {
		respondError(c, http.StatusServiceUnavailable, CodeUpstream, "UModel 生成器未启用", nil)
		return
	}
	c.JSON(http.StatusOK, s.umodelGen.StatsSnapshot())
}
