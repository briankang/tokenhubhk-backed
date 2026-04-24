package v1

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"tokenhub-server/internal/model"
	"tokenhub-server/internal/pkg/errcode"
	"tokenhub-server/internal/pkg/response"
	"tokenhub-server/internal/service/pricing"
)

type EstimateHandler struct {
	db         *gorm.DB
	calculator *pricing.PricingCalculator
}

type estimateCostRequest struct {
	Model        string  `json:"model" binding:"required"`
	InputTokens  int     `json:"input_tokens,omitempty"`
	OutputTokens int     `json:"output_tokens,omitempty"`
	MaxTokens    int     `json:"max_tokens,omitempty"`
	ImageCount   int     `json:"image_count,omitempty"`
	CharCount    int     `json:"char_count,omitempty"`
	DurationSec  float64 `json:"duration_sec,omitempty"`
	CallCount    int     `json:"call_count,omitempty"`
}

func NewEstimateHandler(db *gorm.DB, calculator *pricing.PricingCalculator) *EstimateHandler {
	return &EstimateHandler{db: db, calculator: calculator}
}

func (h *EstimateHandler) Register(rg *gin.RouterGroup) {
	rg.POST("/estimate/cost", h.EstimateCost)
}

func (h *EstimateHandler) EstimateCost(c *gin.Context) {
	var req estimateCostRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrValidation.Code, err.Error())
		return
	}
	if req.OutputTokens == 0 && req.MaxTokens > 0 {
		req.OutputTokens = req.MaxTokens
	}

	var aiModel model.AIModel
	err := h.db.WithContext(c.Request.Context()).
		Where("model_name = ? OR display_name = ?", req.Model, req.Model).
		First(&aiModel).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			response.ErrorMsg(c, http.StatusNotFound, errcode.ErrNotFound.Code, "model not found")
			return
		}
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, err.Error())
		return
	}

	userID, _ := c.Get("userId")
	tenantID, _ := c.Get("tenantId")
	uid, _ := userID.(uint)
	tid, _ := tenantID.(uint)

	usage := pricing.UsageInput{
		InputTokens:  req.InputTokens,
		OutputTokens: req.OutputTokens,
		ImageCount:   req.ImageCount,
		CharCount:    req.CharCount,
		DurationSec:  req.DurationSec,
		CallCount:    req.CallCount,
	}
	result, err := h.calculator.CalculateCostByUnit(c.Request.Context(), uid, aiModel.ID, tid, 0, usage)
	if err != nil {
		response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrBadRequest.Code, err.Error())
		return
	}

	response.Success(c, gin.H{
		"model":             aiModel.ModelName,
		"model_id":          aiModel.ID,
		"model_type":        aiModel.ModelType,
		"pricing_unit":      aiModel.PricingUnit,
		"input_tokens":      req.InputTokens,
		"output_tokens":     req.OutputTokens,
		"image_count":       req.ImageCount,
		"char_count":        req.CharCount,
		"duration_sec":      req.DurationSec,
		"call_count":        req.CallCount,
		"estimated_credits": result.TotalCost,
		"estimated_rmb":     result.TotalCostRMB,
		"platform_cost":     result.PlatformCost,
		"price_detail":      result.PriceDetail,
		"matched_tier":      result.MatchedTier,
		"matched_tier_idx":  result.MatchedTierIdx,
	})
}
