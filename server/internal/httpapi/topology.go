// 网络拓扑（/api/v1/topology，F-061）处理器：链路图与主机接入定位。
package httpapi

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"meridian/server/internal/topology"
)

// getTopology 处理 GET /api/v1/topology：
// 返回 {nodes:[{id,name,model_code,room}], edges:[{a,b,a_port,b_port,source}]}，
// 边由 network_link 发现记录双向互证合并去重，无记录佐证的 connected_to 关系补边。
func (s *Server) getTopology(c *gin.Context) {
	graph, err := topology.New(s.db).Graph(c.Request.Context())
	if err != nil {
		respondError(c, http.StatusInternalServerError, CodeInternal, "组装拓扑图失败: "+err.Error(), nil)
		return
	}
	c.JSON(http.StatusOK, graph)
}

// getHostLocation 处理 GET /api/v1/topology/host-location?ip=：
// ARP/MAC 交叉定位主机接入端口，返回 {ip,mac,switch,port,protocol}；任一环未命中返回 404。
func (s *Server) getHostLocation(c *gin.Context) {
	ip := c.Query("ip")
	if ip == "" {
		respondError(c, http.StatusBadRequest, CodeBadRequest, "缺少必填查询参数 ip", nil)
		return
	}
	loc, err := topology.New(s.db).HostLocation(c.Request.Context(), ip)
	if err != nil {
		respondError(c, http.StatusInternalServerError, CodeInternal, "主机接入定位失败: "+err.Error(), nil)
		return
	}
	if loc == nil {
		respondError(c, http.StatusNotFound, CodeNotFound, "该 IP 无法定位接入端口（无 MAC 或链路记录命中）", nil)
		return
	}
	c.JSON(http.StatusOK, loc)
}
