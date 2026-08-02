// 集成输出（/api/v1/integrations）处理器：向外部系统供给 CMDB 权威数据。
package httpapi

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"meridian/server/internal/store"
)

// oxidizedDevice 是 Oxidized HTTP source 期望的设备条目形状。
type oxidizedDevice struct {
	Name  string `json:"name"`
	IP    string `json:"ip"`
	Model string `json:"model"`
	Group string `json:"group"`
}

// listOxidizedDevices 处理 GET /api/v1/integrations/oxidized/devices：
// 从 network_device 模型 CI 映射 Oxidized 设备清单（新设备入库即纳入配置备份，
// 退役即移除），Oxidized 侧配置 HTTP source 指向本端点即可零手工维护清单。
func (s *Server) listOxidizedDevices(c *gin.Context) {
	var model store.Model
	err := s.db.First(&model, "code = ?", "network_device").Error
	if err != nil {
		if isNotFound(err) {
			// 模型尚未种子导入：清单为空而非报错，便于集成先接通。
			c.JSON(http.StatusOK, []oxidizedDevice{})
			return
		}
		respondError(c, http.StatusInternalServerError, CodeInternal, "查询网络设备模型失败", nil)
		return
	}
	var cis []store.CI
	if err := s.db.Where("model_id = ? AND status <> ?", model.ID, "retired").
		Order("created_at ASC").Find(&cis).Error; err != nil {
		respondError(c, http.StatusInternalServerError, CodeInternal, "查询网络设备 CI 失败", nil)
		return
	}
	devices := []oxidizedDevice{}
	for _, ci := range cis {
		devices = append(devices, oxidizedDevice{
			Name:  firstStringAttr(ci, "name", "hostname", "serial_no", "mgmt_ip"),
			IP:    stringAttr(ci, "mgmt_ip"),
			Model: stringAttr(ci, "model"),
			Group: defaultString(stringAttr(ci, "group"), "default"),
		})
	}
	c.JSON(http.StatusOK, devices)
}

// stringAttr 读取 CI 字符串属性，缺失或非字符串时返回空串。
func stringAttr(ci store.CI, key string) string {
	if v, ok := ci.Attributes[key].(string); ok {
		return v
	}
	return ""
}

// firstStringAttr 按候选键顺序取第一个非空字符串属性。
func firstStringAttr(ci store.CI, keys ...string) string {
	for _, key := range keys {
		if v := stringAttr(ci, key); v != "" {
			return v
		}
	}
	return ""
}

// defaultString 在值为空时返回默认值。
func defaultString(v, def string) string {
	if v == "" {
		return def
	}
	return v
}
