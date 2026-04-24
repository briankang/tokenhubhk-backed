package admin

import (
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"

	"tokenhub-server/internal/database"
	"tokenhub-server/internal/model"
	"tokenhub-server/internal/pkg/response"
	"tokenhub-server/internal/service/pricing"
)

// PricingHandler 定价管理接口处理器
type PricingHandler struct {
	pricingSvc *pricing.PricingService
	calculator *pricing.PricingCalculator
}

// NewPricingHandler creates a new PricingHandler.
func NewPricingHandler(pricingSvc *pricing.PricingService, calculator *pricing.PricingCalculator) *PricingHandler {
	return &PricingHandler{
		pricingSvc: pricingSvc,
		calculator: calculator,
	}
}

// listModelPricingsReq is the query params for listing model pricings.
type listModelPricingsReq struct {
	Page     int `form:"page" binding:"omitempty,min=1"`
	PageSize int `form:"page_size" binding:"omitempty,min=1,max=100"`
}

// ListModelPricings handles GET /api/v1/admin/model-pricings
func (h *PricingHandler) ListModelPricings(c *gin.Context) {
	var req listModelPricingsReq
	if err := c.ShouldBindQuery(&req); err != nil {
		response.ErrorMsg(c, http.StatusBadRequest, 20001, "invalid query params: "+err.Error())
		return
	}
	if req.Page == 0 {
		req.Page = 1
	}
	if req.PageSize == 0 {
		req.PageSize = 20
	}

	list, total, err := h.pricingSvc.ListModelPricings(c.Request.Context(), req.Page, req.PageSize)
	if err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, 50001, err.Error())
		return
	}
	response.PageResult(c, list, total, req.Page, req.PageSize)
}

// createModelPricingReq is the request body for creating/setting model pricing.
// 价格单位：每百万 token 的积分价格（int64）
type createModelPricingReq struct {
	ModelID             uint   `json:"model_id" binding:"required"`
	InputPricePerToken  int64  `json:"input_price_per_token"`  // 输入售价（积分/百万token）
	OutputPricePerToken int64  `json:"output_price_per_token"` // 输出售价（积分/百万token）
	Currency            string `json:"currency"`
}

// CreateModelPricing handles POST /api/v1/admin/model-pricings
func (h *PricingHandler) CreateModelPricing(c *gin.Context) {
	var req createModelPricingReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.ErrorMsg(c, http.StatusBadRequest, 20001, "invalid request body: "+err.Error())
		return
	}

	mp := &model.ModelPricing{
		ModelID:             req.ModelID,
		InputPricePerToken:  req.InputPricePerToken,
		OutputPricePerToken: req.OutputPricePerToken,
		Currency:            req.Currency,
	}
	if mp.Currency == "" {
		mp.Currency = "USD"
	}

	if err := h.pricingSvc.SetModelPricing(c.Request.Context(), mp); err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, 50001, err.Error())
		return
	}
	invalidatePublicModelsCache()
	response.Success(c, mp)
}

// updateModelPricingReq is the request body for updating model pricing.
// 价格单位：每百万 token 的积分价格（int64）
type updateModelPricingReq struct {
	InputPricePerToken  int64  `json:"input_price_per_token"`  // 输入售价（积分/百万token）
	OutputPricePerToken int64  `json:"output_price_per_token"` // 输出售价（积分/百万token）
	Currency            string `json:"currency"`
}

// UpdateModelPricing handles PUT /api/v1/admin/model-pricings/:id
func (h *PricingHandler) UpdateModelPricing(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || id == 0 {
		response.ErrorMsg(c, http.StatusBadRequest, 20001, "invalid id")
		return
	}

	var req updateModelPricingReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.ErrorMsg(c, http.StatusBadRequest, 20001, "invalid request body: "+err.Error())
		return
	}

	mp := &model.ModelPricing{
		InputPricePerToken:  req.InputPricePerToken,
		OutputPricePerToken: req.OutputPricePerToken,
		Currency:            req.Currency,
	}
	if mp.Currency == "" {
		mp.Currency = "USD"
	}

	if err := h.pricingSvc.UpdateModelPricing(c.Request.Context(), uint(id), mp); err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, 50001, err.Error())
		return
	}
	invalidatePublicModelsCache()
	response.Success(c, nil)
}

// DeleteModelPricing handles DELETE /api/v1/admin/model-pricings/:id
func (h *PricingHandler) DeleteModelPricing(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || id == 0 {
		response.ErrorMsg(c, http.StatusBadRequest, 20001, "invalid id")
		return
	}

	if err := h.pricingSvc.DeleteModelPricing(c.Request.Context(), uint(id)); err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, 50001, err.Error())
		return
	}
	response.Success(c, nil)
}

// priceMatrixReq is the query params for the price matrix.
type priceMatrixReq struct {
	TenantID   uint `form:"tenant_id"`
	AgentLevel int  `form:"agent_level"`
}

// GetPriceMatrix handles GET /api/v1/admin/price-matrix
func (h *PricingHandler) GetPriceMatrix(c *gin.Context) {
	var req priceMatrixReq
	if err := c.ShouldBindQuery(&req); err != nil {
		response.ErrorMsg(c, http.StatusBadRequest, 20001, "invalid query params: "+err.Error())
		return
	}

	matrix, err := h.calculator.GetPriceMatrix(c.Request.Context(), 0, req.TenantID, req.AgentLevel)
	if err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, 50001, err.Error())
		return
	}
	response.Success(c, matrix)
}

// priceCalculateReq is the request body for simulating price calculation.
type priceCalculateReq struct {
	ModelID      uint `json:"model_id" binding:"required"`
	TenantID     uint `json:"tenant_id"`
	AgentLevel   int  `json:"agent_level"`
	InputTokens  int  `json:"input_tokens"`
	OutputTokens int  `json:"output_tokens"`
}

// CalculatePrice handles POST /api/v1/admin/price-calculate
func (h *PricingHandler) CalculatePrice(c *gin.Context) {
	var req priceCalculateReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.ErrorMsg(c, http.StatusBadRequest, 20001, "invalid request body: "+err.Error())
		return
	}

	// If tokens are provided, calculate cost; otherwise just return price
	if req.InputTokens > 0 || req.OutputTokens > 0 {
		cost, err := h.calculator.CalculateCost(
			c.Request.Context(), 0, req.ModelID, req.TenantID, req.AgentLevel,
			req.InputTokens, req.OutputTokens,
		)
		if err != nil {
			response.ErrorMsg(c, http.StatusInternalServerError, 50001, err.Error())
			return
		}
		response.Success(c, cost)
		return
	}

	price, err := h.calculator.CalculatePrice(c.Request.Context(), 0, req.ModelID, req.TenantID, req.AgentLevel)
	if err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, 50001, err.Error())
		return
	}
	response.Success(c, price)
}

// repairPricingReq 一次性修复请求体
type repairPricingReq struct {
	DryRun bool `json:"dry_run"` // true=预览模式，不写库
}

// RepairPricingItem 单条修复结果
type RepairPricingItem struct {
	ModelID     uint   `json:"model_id"`
	ModelName   string `json:"model_name"`
	DisplayName string `json:"display_name,omitempty"`
	Action      string `json:"action"` // backfilled / disabled / skipped
	Reason      string `json:"reason,omitempty"`
	InputPrice  int64  `json:"input_price,omitempty"`  // 积分/百万 token
	OutputPrice int64  `json:"output_price,omitempty"` // 积分/百万 token
}

// RepairPricingResult 修复汇总
type RepairPricingResult struct {
	Total      int                 `json:"total"`      // 扫描到的无售价模型总数
	Backfilled int                 `json:"backfilled"` // 用官网价补齐的数量
	Disabled   int                 `json:"disabled"`   // 因官网价缺失被禁用的数量
	Skipped    int                 `json:"skipped"`    // 因其他原因跳过
	DryRun     bool                `json:"dry_run"`
	Items      []RepairPricingItem `json:"items"`
}

// RepairPricing handles POST /api/v1/admin/model-pricings/repair
// 一次性数据修复：
//  1. 扫描所有 ai_models 中没有 model_pricings 记录的模型
//  2. 若 ai_models 的官网价字段 (input_price_per_token / output_price_per_token) 大于 0，
//     则按官网价创建 ModelPricing 记录（售价 = 官网价，后续可手动加价）
//  3. 若官网价也缺失，则将模型禁用（is_active=false, status=offline）
//
// 支持 ?dry_run=true 预览模式，仅返回处置建议不写库。
func (h *PricingHandler) RepairPricing(c *gin.Context) {
	var req repairPricingReq
	_ = c.ShouldBindJSON(&req) // 允许空 body

	db := database.DB
	ctx := c.Request.Context()

	// 1) 扫描没有 ModelPricing 的活跃模型
	var models []model.AIModel
	err := db.WithContext(ctx).
		Where("NOT EXISTS (SELECT 1 FROM model_pricings WHERE model_pricings.model_id = ai_models.id AND model_pricings.deleted_at IS NULL)").
		Find(&models).Error
	if err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, 50001, "scan models: "+err.Error())
		return
	}

	result := RepairPricingResult{
		Total:  len(models),
		DryRun: req.DryRun,
		Items:  make([]RepairPricingItem, 0, len(models)),
	}

	now := time.Now()
	for _, m := range models {
		item := RepairPricingItem{
			ModelID:     m.ID,
			ModelName:   m.ModelName,
			DisplayName: m.DisplayName,
		}

		hasOfficial := m.InputPricePerToken > 0 || m.OutputPricePerToken > 0 ||
			m.InputCostRMB > 0 || m.OutputCostRMB > 0

		if hasOfficial {
			// 按官网价创建 ModelPricing
			mp := &model.ModelPricing{
				ModelID:             m.ID,
				InputPricePerToken:  m.InputPricePerToken,
				InputPriceRMB:       m.InputCostRMB,
				OutputPricePerToken: m.OutputPricePerToken,
				OutputPriceRMB:      m.OutputCostRMB,
				Currency:            m.Currency,
				EffectiveFrom:       &now,
			}
			if mp.Currency == "" {
				mp.Currency = "CREDIT"
			}
			item.Action = "backfilled"
			item.Reason = "supplier official price available"
			item.InputPrice = m.InputPricePerToken
			item.OutputPrice = m.OutputPricePerToken

			if !req.DryRun {
				if err := db.WithContext(ctx).Create(mp).Error; err != nil {
					item.Action = "skipped"
					item.Reason = "create model_pricing failed: " + err.Error()
					result.Skipped++
				} else {
					result.Backfilled++
				}
			} else {
				result.Backfilled++
			}
		} else {
			// 官网价缺失：禁用模型
			item.Action = "disabled"
			item.Reason = "no official price available"

			if !req.DryRun {
				err := db.WithContext(ctx).Model(&model.AIModel{}).
					Where("id = ?", m.ID).
					Updates(map[string]interface{}{
						"is_active": false,
						"status":    "offline",
					}).Error
				if err != nil {
					item.Action = "skipped"
					item.Reason = "disable model failed: " + err.Error()
					result.Skipped++
				} else {
					result.Disabled++
				}
			} else {
				result.Disabled++
			}
		}

		result.Items = append(result.Items, item)
	}

	// 清理定价缓存（批量清模型定价相关 key）
	if !req.DryRun && len(models) > 0 {
		for _, m := range models {
			h.calculator.InvalidateCache(ctx, m.ID, nil)
		}
	}

	response.Success(c, result)
}
