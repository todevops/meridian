// CI 关系创建/删除（/api/v1/cis/{ci_id}/relations）处理器。
package httpapi

import (
	"fmt"
	"net/http"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"meridian/server/internal/store"
)

type ciRelationCreateRequest struct {
	RelationCode string `json:"relation_code" binding:"required"`
	PeerCIID     string `json:"peer_ci_id" binding:"required"`
}

// relationView 把关系投影为契约形状（含对端 CI 摘要，方向相对当前 CI）。
func relationView(rel store.CIRelation, currentCIID string, peer store.CI) gin.H {
	direction := "outgoing"
	if rel.DstCIID == currentCIID && rel.SrcCIID != currentCIID {
		direction = "incoming"
	}
	return gin.H{"relation_code": rel.RelationCode, "direction": direction, "peer_ci": peer}
}

// createCIRelation 处理 POST /api/v1/cis/{ci_id}/relations。
// 校验：关系编码在模型关系定义内、对端 CI 模型匹配 target_model；
// one_to_one 关系自动替换同编码同方向的既有关系（用于改挂，如机柜换机房）。
func (s *Server) createCIRelation(c *gin.Context) {
	ci, ok := s.resolveCI(c, c.Param("ci_id"))
	if !ok {
		return
	}
	var req ciRelationCreateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		respondError(c, http.StatusBadRequest, CodeBadRequest, "relation_code 与 peer_ci_id 均为必填", nil)
		return
	}

	// 关系定义须在 CI 所属模型的 relations 内。
	var model store.Model
	if err := s.db.First(&model, "id = ?", ci.ModelID).Error; err != nil {
		respondError(c, http.StatusInternalServerError, CodeInternal, "加载 CI 模型失败", nil)
		return
	}
	var def *store.RelationDefinition
	for i, d := range model.Relations.Data() {
		if d.Code == req.RelationCode {
			def = &model.Relations.Data()[i]
			break
		}
	}
	if def == nil {
		respondError(c, http.StatusBadRequest, CodeValidationFailed,
			fmt.Sprintf("模型 %s 未定义关系 %q", model.Code, req.RelationCode), nil)
		return
	}

	// 对端 CI 存在且模型匹配 target_model。
	peer, ok := s.resolveCI(c, req.PeerCIID)
	if !ok {
		return
	}
	var peerModel store.Model
	if err := s.db.First(&peerModel, "id = ?", peer.ModelID).Error; err != nil {
		respondError(c, http.StatusInternalServerError, CodeInternal, "加载对端 CI 模型失败", nil)
		return
	}
	if peerModel.Code != def.TargetModel {
		respondError(c, http.StatusBadRequest, CodeValidationFailed,
			fmt.Sprintf("关系 %q 的目标模型应为 %s，对端 CI 模型为 %s", def.Code, def.TargetModel, peerModel.Code), nil)
		return
	}

	// 方向：定义相对本模型为 outgoing 时本 CI 为源，incoming 时本 CI 为目的。
	srcID, dstID := ci.ID, peer.ID
	if def.Direction == "incoming" {
		srcID, dstID = peer.ID, ci.ID
	}

	err := s.db.Transaction(func(tx *gorm.DB) error {
		// one_to_one：替换同编码、同侧 CI 的既有关系。
		if def.Cardinality == "one_to_one" {
			var cond *gorm.DB
			if def.Direction == "incoming" {
				cond = tx.Where("relation_code = ? AND dst_ci_id = ?", def.Code, dstID)
			} else {
				cond = tx.Where("relation_code = ? AND src_ci_id = ?", def.Code, srcID)
			}
			if err := cond.Delete(&store.CIRelation{}).Error; err != nil {
				return err
			}
		}
		// 去重：同一 (code, src, dst) 关系不重复建。
		var count int64
		if err := tx.Model(&store.CIRelation{}).
			Where("relation_code = ? AND src_ci_id = ? AND dst_ci_id = ?", def.Code, srcID, dstID).
			Count(&count).Error; err != nil {
			return err
		}
		if count > 0 {
			return nil
		}
		return tx.Create(&store.CIRelation{RelationCode: def.Code, SrcCIID: srcID, DstCIID: dstID}).Error
	})
	if err != nil {
		respondError(c, http.StatusInternalServerError, CodeInternal, "创建关系失败: "+err.Error(), nil)
		return
	}
	c.JSON(http.StatusOK, relationView(store.CIRelation{RelationCode: def.Code, SrcCIID: srcID, DstCIID: dstID}, ci.ID, peer))
}

// deleteCIRelation 处理 DELETE /api/v1/cis/{ci_id}/relations/{relation_code}/{peer_ci_id}。
func (s *Server) deleteCIRelation(c *gin.Context) {
	ci, ok := s.resolveCI(c, c.Param("ci_id"))
	if !ok {
		return
	}
	code, peerID := c.Param("relation_code"), c.Param("peer_ci_id")
	res := s.db.Where(
		"relation_code = ? AND ((src_ci_id = ? AND dst_ci_id = ?) OR (src_ci_id = ? AND dst_ci_id = ?))",
		code, ci.ID, peerID, peerID, ci.ID,
	).Delete(&store.CIRelation{})
	if res.Error != nil {
		respondError(c, http.StatusInternalServerError, CodeInternal, "删除关系失败", nil)
		return
	}
	if res.RowsAffected == 0 {
		respondError(c, http.StatusNotFound, CodeNotFound, "关系不存在", nil)
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}
