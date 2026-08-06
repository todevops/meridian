// DBMS 治理（/api/v1/dbms/*）：EOL 清单导出（US-3.3，F-005 范围内用户仅见其归属实例）。
package httpapi

import (
	"encoding/csv"
	"fmt"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"meridian/server/internal/store"
)

// eolItem 与契约 EOLReportItem 对应。
type eolItem struct {
	InstanceAddr  string `json:"instance_addr"`
	ComponentType string `json:"component_type"`
	Version       string `json:"version"`
	Role          string `json:"role"`
	ClusterName   string `json:"cluster_name"`
	AppName       string `json:"app_name"`
	AppOwner      string `json:"app_owner"`
}

// eolCSVHeader 为 CSV 表头（与 JSON 字段同名；文件带 BOM 便于 Excel 打开）。
var eolCSVHeader = []string{"instance_addr", "component_type", "version", "role", "cluster_name", "app_name", "app_owner"}

// strAttr 取 CI 字符串属性（缺失/非字符串给空串）。
func strAttr(ci store.CI, key string) string {
	s, _ := ci.Attributes[key].(string)
	return s
}

// getDBMSEOLReport 处理 GET /api/v1/dbms/eol-report：
// 扫描 db_instance CI（排除已退役），按 component 精确匹配与 version_prefix 前缀过滤；
// 所属应用/负责人沿 depends_on 入向反查 biz_app（取第一归属）。
func (s *Server) getDBMSEOLReport(c *gin.Context) {
	format := c.DefaultQuery("format", "json")
	if format != "json" && format != "csv" {
		respondError(c, http.StatusBadRequest, CodeBadRequest,
			fmt.Sprintf("format 取值 %q 非法（json/csv）", format), nil)
		return
	}

	var model store.Model
	if err := s.db.First(&model, "code = ?", "db_instance").Error; err != nil {
		if isNotFound(err) {
			// 模型未种子：返回空清单而非报错（导出按钮在空库也可用）。
			s.respondEOL(c, format, []eolItem{})
			return
		}
		respondError(c, http.StatusInternalServerError, CodeInternal, "查询模型失败", nil)
		return
	}

	var cis []store.CI
	if err := s.db.Where("model_id = ? AND status <> ?", model.ID, "retired").
		Order("created_at ASC").Find(&cis).Error; err != nil {
		respondError(c, http.StatusInternalServerError, CodeInternal, "查询数据库实例失败", nil)
		return
	}

	// 应用反查：depends_on 入向（biz_app → db_instance）。
	var appModel store.Model
	appName, appOwner := map[string]string{}, map[string]string{}
	if err := s.db.First(&appModel, "code = ?", "biz_app").Error; err == nil {
		var rels []store.CIRelation
		if err := s.db.Where("relation_code = ?", "depends_on").Find(&rels).Error; err == nil {
			srcIDs := []string{}
			for _, rel := range rels {
				srcIDs = append(srcIDs, rel.SrcCIID)
			}
			apps := map[string]store.CI{}
			if len(srcIDs) > 0 {
				var rows []store.CI
				if err := s.db.Where("model_id = ? AND id IN ?", appModel.ID, srcIDs).Find(&rows).Error; err == nil {
					for _, a := range rows {
						apps[a.ID] = a
					}
				}
			}
			for _, rel := range rels {
				if app, ok := apps[rel.SrcCIID]; ok {
					if _, taken := appName[rel.DstCIID]; !taken {
						appName[rel.DstCIID] = strAttr(app, "name")
						appOwner[rel.DstCIID] = strAttr(app, "owner")
					}
				}
			}
		}
	}

	// 数据范围（F-005）：受约束用户仅导出归属闭包内的实例。
	set, restricted, err := s.ciVisibleSet(c)
	if err != nil {
		respondError(c, http.StatusInternalServerError, CodeInternal, "计算数据范围失败", nil)
		return
	}

	component := strings.TrimSpace(c.Query("component"))
	versionPrefix := strings.TrimSpace(c.Query("version_prefix"))
	items := []eolItem{}
	for _, ci := range cis {
		if restricted && !set[ci.ID] {
			continue
		}
		ct := strAttr(ci, "component_type")
		if component != "" && ct != component {
			continue
		}
		ver := strAttr(ci, "version")
		if versionPrefix != "" && !strings.HasPrefix(ver, versionPrefix) {
			continue
		}
		items = append(items, eolItem{
			InstanceAddr:  strAttr(ci, "instance_addr"),
			ComponentType: ct,
			Version:       ver,
			Role:          strAttr(ci, "role"),
			ClusterName:   strAttr(ci, "cluster_name"),
			AppName:       appName[ci.ID],
			AppOwner:      appOwner[ci.ID],
		})
	}
	s.respondEOL(c, format, items)
}

// respondEOL 按 format 输出 JSON 或带 BOM 的 CSV。
func (s *Server) respondEOL(c *gin.Context, format string, items []eolItem) {
	if format == "csv" {
		c.Header("Content-Type", "text/csv; charset=utf-8")
		c.Header("Content-Disposition", `attachment; filename="dbms-eol-report.csv"`)
		w := c.Writer
		_, _ = w.Write([]byte{0xEF, 0xBB, 0xBF}) // BOM：Excel 正确识别 UTF-8
		cw := csv.NewWriter(w)
		_ = cw.Write(eolCSVHeader)
		for _, it := range items {
			_ = cw.Write([]string{it.InstanceAddr, it.ComponentType, it.Version, it.Role, it.ClusterName, it.AppName, it.AppOwner})
		}
		cw.Flush()
		return
	}
	c.JSON(http.StatusOK, gin.H{"items": items})
}
