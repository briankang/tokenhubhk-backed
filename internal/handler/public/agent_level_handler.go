package public

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"tokenhub-server/internal/model"
	"tokenhub-server/internal/pkg/errcode"
	"tokenhub-server/internal/pkg/response"
)

// AgentLevelPublicHandler 代理等级公开接口处理器
// 提供无需认证的等级查询，供前端展示页使用
type AgentLevelPublicHandler struct {
	db *gorm.DB
}

// NewAgentLevelPublicHandler 创建代理等级公开Handler实例
func NewAgentLevelPublicHandler(db *gorm.DB) *AgentLevelPublicHandler {
	return &AgentLevelPublicHandler{db: db}
}

// AgentLevelPublicResponse 公开返回的等级信息（不含敏感字段）
type AgentLevelPublicResponse struct {
	ID                 uint    `json:"id"`
	LevelCode          string  `json:"level_code"`
	LevelName          string  `json:"level_name"`
	Rank               int     `json:"rank"`
	MinMonthlySalesRMB float64 `json:"min_monthly_sales_rmb"`
	MinDirectSubs      int     `json:"min_direct_subs"`
	DirectCommission   float64 `json:"direct_commission"`
	Benefits           string  `json:"benefits"`
}

// GetPublicAgentLevels 获取所有启用的代理等级（公开接口，无需认证）
// GET /api/v1/public/agent-levels
// 返回按等级排序的代理等级列表，仅包含展示用字段
func (h *AgentLevelPublicHandler) GetPublicAgentLevels(c *gin.Context) {
	var levels []model.AgentLevel
	if err := h.db.Where("is_active = ?", true).Order("level_rank ASC").Find(&levels).Error; err != nil {
		response.Error(c, http.StatusInternalServerError, errcode.ErrInternal)
		return
	}

	// 转换为公开响应结构，过滤敏感字段
	result := make([]AgentLevelPublicResponse, 0, len(levels))
	for _, lv := range levels {
		result = append(result, AgentLevelPublicResponse{
			ID:                 lv.ID,
			LevelCode:          lv.LevelCode,
			LevelName:          lv.LevelName,
			Rank:               lv.Rank,
			MinMonthlySalesRMB: lv.MinMonthlySalesRMB,
			MinDirectSubs:      lv.MinDirectSubs,
			DirectCommission:   lv.DirectCommission,
			Benefits:           lv.Benefits,
		})
	}

	response.Success(c, result)
}
