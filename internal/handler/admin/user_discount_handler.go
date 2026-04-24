package admin

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"tokenhub-server/internal/middleware/audit"
	"tokenhub-server/internal/model"
	"tokenhub-server/internal/pkg/errcode"
	"tokenhub-server/internal/pkg/response"
	"tokenhub-server/internal/service/pricing"
)

// UserDiscountHandler 用户特殊折扣管理
//   - 提供针对单个用户+单个模型的特殊售价覆盖（DISCOUNT/FIXED/MARKUP）
//   - 在模型列表勾选多个模型后可批量下发到同一用户
//   - 列表/详情/预览接口均返回三栏价格对比：官网原价 / 平台售价 / 特殊折扣价
type UserDiscountHandler struct {
	db      *gorm.DB
	service *pricing.UserDiscountService
}

// NewUserDiscountHandler 构造函数
func NewUserDiscountHandler(db *gorm.DB, service *pricing.UserDiscountService) *UserDiscountHandler {
	return &UserDiscountHandler{db: db, service: service}
}

// Register 挂载路由
func (h *UserDiscountHandler) Register(rg *gin.RouterGroup) {
	rg.GET("/user-discounts", h.List)
	rg.GET("/user-discounts/:id", h.Get)
	rg.POST("/user-discounts/batch", h.BatchCreate)
	rg.POST("/user-discounts/preview", h.Preview)
	rg.PUT("/user-discounts/:id", h.Update)
	rg.DELETE("/user-discounts/:id", h.Delete)
}

// userDiscountDTO 返回结构
type userDiscountDTO struct {
	ID           uint       `json:"id"`
	UserID       uint       `json:"user_id"`
	UserEmail    string     `json:"user_email"`
	UserName     string     `json:"user_name"`
	ModelID      uint       `json:"model_id"`
	ModelName    string     `json:"model_name"`
	ModelDisplay string     `json:"model_display"`
	PricingType  string     `json:"pricing_type"`
	DiscountRate *float64   `json:"discount_rate,omitempty"`
	InputPrice   *float64   `json:"input_price,omitempty"`
	OutputPrice  *float64   `json:"output_price,omitempty"`
	MarkupRate   *float64   `json:"markup_rate,omitempty"`
	EffectiveAt  *time.Time `json:"effective_at,omitempty"`
	ExpireAt     *time.Time `json:"expire_at,omitempty"`
	Note         string     `json:"note"`
	IsActive     bool       `json:"is_active"`
	OperatorID   uint       `json:"operator_id"`
	CreatedAt    time.Time  `json:"created_at"`
	UpdatedAt    time.Time  `json:"updated_at"`

	// 三栏价格对比（人民币/百万 Tokens）
	CostInputRMB    float64 `json:"cost_input_rmb"`     // 官网原价（输入）
	CostOutputRMB   float64 `json:"cost_output_rmb"`    // 官网原价（输出）
	PlatformInputRMB  float64 `json:"platform_input_rmb"`  // 平台售价（输入）
	PlatformOutputRMB float64 `json:"platform_output_rmb"` // 平台售价（输出）
	FinalInputRMB     float64 `json:"final_input_rmb"`     // 特殊折扣后（输入）
	FinalOutputRMB    float64 `json:"final_output_rmb"`    // 特殊折扣后（输出）

	// --- 扩展展示信息（Preview/列表均返回） ---

	// 计费单位（per_million_tokens / per_image / per_hour 等）
	PricingUnit string `json:"pricing_unit,omitempty"`
	ModelType   string `json:"model_type,omitempty"`

	// 缓存定价（成本价，人民币/百万 Tokens）—— 仅在 supports_cache=true 时有意义
	SupportsCache         bool    `json:"supports_cache"`
	CacheMechanism        string  `json:"cache_mechanism,omitempty"`        // auto/explicit/both/none
	CacheMinTokens        int     `json:"cache_min_tokens,omitempty"`
	CacheInputRMB         float64 `json:"cache_input_rmb,omitempty"`          // 隐式/auto 命中价
	CacheExplicitInputRMB float64 `json:"cache_explicit_input_rmb,omitempty"` // 显式命中价 (both 模式)
	CacheWriteRMB         float64 `json:"cache_write_rmb,omitempty"`          // 缓存写入溢价

	// 阶梯定价（JSON，传给前端原样渲染）
	CostPriceTiers     model.JSON `json:"cost_price_tiers,omitempty"`     // 成本侧阶梯（供应商原始）
	PlatformPriceTiers model.JSON `json:"platform_price_tiers,omitempty"` // 平台售价阶梯

	// 模型/供应商级折扣（用于展示平台售价是如何从成本价推导出的）
	ModelDiscount    float64 `json:"model_discount,omitempty"`    // 模型独立折扣 (0=未设置)
	SupplierDiscount float64 `json:"supplier_discount,omitempty"` // 供应商级折扣
	SupplierName     string  `json:"supplier_name,omitempty"`
}

func (h *UserDiscountHandler) buildDTO(ctx *gin.Context, d *model.UserModelDiscount) userDiscountDTO {
	dto := userDiscountDTO{
		ID:           d.ID,
		UserID:       d.UserID,
		ModelID:      d.ModelID,
		PricingType:  d.PricingType,
		DiscountRate: d.DiscountRate,
		InputPrice:   d.InputPrice,
		OutputPrice:  d.OutputPrice,
		MarkupRate:   d.MarkupRate,
		EffectiveAt:  d.EffectiveAt,
		ExpireAt:     d.ExpireAt,
		Note:         d.Note,
		IsActive:     d.IsActive,
		OperatorID:   d.OperatorID,
		CreatedAt:    d.CreatedAt,
		UpdatedAt:    d.UpdatedAt,
	}
	if d.User != nil {
		dto.UserEmail = d.User.Email
		dto.UserName = d.User.Name
	}
	if d.Model != nil {
		dto.ModelName = d.Model.ModelName
		dto.ModelDisplay = d.Model.DisplayName
		dto.CostInputRMB = d.Model.InputCostRMB
		dto.CostOutputRMB = d.Model.OutputCostRMB
		dto.PricingUnit = d.Model.PricingUnit
		dto.ModelType = d.Model.ModelType
		dto.SupportsCache = d.Model.SupportsCache
		dto.CacheMechanism = d.Model.CacheMechanism
		dto.CacheMinTokens = d.Model.CacheMinTokens
		dto.CacheInputRMB = d.Model.CacheInputPriceRMB
		dto.CacheExplicitInputRMB = d.Model.CacheExplicitInputPriceRMB
		dto.CacheWriteRMB = d.Model.CacheWritePriceRMB
		dto.CostPriceTiers = d.Model.PriceTiers
		dto.ModelDiscount = d.Model.Discount
		// 供应商关联（若已 Preload）
		if d.Model.Supplier.ID != 0 {
			dto.SupplierName = d.Model.Supplier.Name
			dto.SupplierDiscount = d.Model.Supplier.Discount
		}
	}
	// 若 buildDTO 被调用但 Model 关联未 Preload，补充读取模型与供应商折扣
	if d.Model == nil || (dto.SupplierName == "" && d.ModelID > 0) {
		var m model.AIModel
		if err := h.db.WithContext(ctx.Request.Context()).Preload("Supplier").First(&m, d.ModelID).Error; err == nil {
			if dto.ModelName == "" {
				dto.ModelName = m.ModelName
				dto.ModelDisplay = m.DisplayName
			}
			if dto.CostInputRMB == 0 {
				dto.CostInputRMB = m.InputCostRMB
				dto.CostOutputRMB = m.OutputCostRMB
			}
			if dto.PricingUnit == "" {
				dto.PricingUnit = m.PricingUnit
			}
			if dto.ModelType == "" {
				dto.ModelType = m.ModelType
			}
			dto.SupportsCache = m.SupportsCache
			dto.CacheMechanism = m.CacheMechanism
			dto.CacheMinTokens = m.CacheMinTokens
			dto.CacheInputRMB = m.CacheInputPriceRMB
			dto.CacheExplicitInputRMB = m.CacheExplicitInputPriceRMB
			dto.CacheWriteRMB = m.CacheWritePriceRMB
			if dto.CostPriceTiers == nil {
				dto.CostPriceTiers = m.PriceTiers
			}
			dto.ModelDiscount = m.Discount
			if m.Supplier.ID != 0 {
				dto.SupplierName = m.Supplier.Name
				dto.SupplierDiscount = m.Supplier.Discount
			}
		}
	}
	// 平台售价
	var mp model.ModelPricing
	if err := h.db.WithContext(ctx.Request.Context()).
		Where("model_id = ?", d.ModelID).
		Order("effective_from DESC").
		First(&mp).Error; err == nil {
		dto.PlatformInputRMB = mp.InputPriceRMB
		dto.PlatformOutputRMB = mp.OutputPriceRMB
		dto.PlatformPriceTiers = mp.PriceTiers
	}
	// 计算折扣后价
	dto.FinalInputRMB, dto.FinalOutputRMB = applyDiscountPreview(dto.PlatformInputRMB, dto.PlatformOutputRMB, d)
	return dto
}

// applyDiscountPreview 基于平台售价预览折扣后价格
func applyDiscountPreview(platInput, platOutput float64, d *model.UserModelDiscount) (float64, float64) {
	switch d.PricingType {
	case "DISCOUNT":
		rate := 1.0
		if d.DiscountRate != nil {
			rate = *d.DiscountRate
		}
		return platInput * rate, platOutput * rate
	case "FIXED":
		in := platInput
		out := platOutput
		if d.InputPrice != nil {
			in = *d.InputPrice
		}
		if d.OutputPrice != nil {
			out = *d.OutputPrice
		}
		return in, out
	case "MARKUP":
		rate := 1.0
		if d.MarkupRate != nil {
			rate = *d.MarkupRate
		}
		return platInput * rate, platOutput * rate
	}
	return platInput, platOutput
}

// List GET /admin/user-discounts
func (h *UserDiscountHandler) List(c *gin.Context) {
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "20"))
	userID, _ := strconv.ParseUint(c.Query("user_id"), 10, 64)
	modelID, _ := strconv.ParseUint(c.Query("model_id"), 10, 64)
	keyword := c.Query("keyword")
	filter := pricing.ListFilter{
		UserID:   uint(userID),
		ModelID:  uint(modelID),
		Keyword:  keyword,
		Page:     page,
		PageSize: pageSize,
	}
	if v := c.Query("is_active"); v != "" {
		b := v == "1" || v == "true"
		filter.IsActive = &b
	}
	result, err := h.service.List(c.Request.Context(), filter)
	if err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, err.Error())
		return
	}
	dtos := make([]userDiscountDTO, 0, len(result.Items))
	for i := range result.Items {
		dtos = append(dtos, h.buildDTO(c, &result.Items[i]))
	}
	response.PageResult(c, dtos, result.Total, result.Page, result.PageSize)
}

// Get GET /admin/user-discounts/:id
func (h *UserDiscountHandler) Get(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrBadRequest.Code, "invalid id")
		return
	}
	row, err := h.service.Get(c.Request.Context(), uint(id))
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			response.ErrorMsg(c, http.StatusNotFound, errcode.ErrNotFound.Code, "discount not found")
			return
		}
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, err.Error())
		return
	}
	response.Success(c, h.buildDTO(c, row))
}

// batchRequest 批量创建请求体
type batchRequest struct {
	UserID       uint       `json:"user_id" binding:"required"`
	ModelIDs     []uint     `json:"model_ids" binding:"required"`
	PricingType  string     `json:"pricing_type" binding:"required"`
	DiscountRate *float64   `json:"discount_rate"`
	InputPrice   *float64   `json:"input_price"`
	OutputPrice  *float64   `json:"output_price"`
	MarkupRate   *float64   `json:"markup_rate"`
	EffectiveAt  *time.Time `json:"effective_at"`
	ExpireAt     *time.Time `json:"expire_at"`
	Note         string     `json:"note"`
	IsActive     *bool      `json:"is_active"`
}

// BatchCreate POST /admin/user-discounts/batch
func (h *UserDiscountHandler) BatchCreate(c *gin.Context) {
	var req batchRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrBadRequest.Code, err.Error())
		return
	}
	var operatorID uint
	if v, ok := c.Get("userId"); ok {
		if u, ok := v.(uint); ok {
			operatorID = u
		}
	}
	tmpl := pricing.UserDiscountInput{
		PricingType:  req.PricingType,
		DiscountRate: req.DiscountRate,
		InputPrice:   req.InputPrice,
		OutputPrice:  req.OutputPrice,
		MarkupRate:   req.MarkupRate,
		EffectiveAt:  req.EffectiveAt,
		ExpireAt:     req.ExpireAt,
		Note:         req.Note,
		IsActive:     req.IsActive,
		OperatorID:   operatorID,
	}
	rows, err := h.service.BatchUpsert(c.Request.Context(), req.UserID, req.ModelIDs, tmpl)
	if err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, err.Error())
		return
	}
	dtos := make([]userDiscountDTO, 0, len(rows))
	for i := range rows {
		// 重新加载关联（BatchUpsert 内部未 Preload）
		full, _ := h.service.Get(c.Request.Context(), rows[i].ID)
		if full != nil {
			dtos = append(dtos, h.buildDTO(c, full))
		} else {
			dtos = append(dtos, h.buildDTO(c, &rows[i]))
		}
	}
	response.Success(c, gin.H{"items": dtos, "count": len(dtos)})
}

// Preview POST /admin/user-discounts/preview
// 给定 user_id + model_ids + 折扣配置，返回三栏对比，不落库
func (h *UserDiscountHandler) Preview(c *gin.Context) {
	var req batchRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrBadRequest.Code, err.Error())
		return
	}
	// 构造临时对象逐个模型预览
	results := make([]userDiscountDTO, 0, len(req.ModelIDs))
	for _, mid := range req.ModelIDs {
		var m model.AIModel
		if err := h.db.WithContext(c.Request.Context()).Preload("Supplier").First(&m, mid).Error; err != nil {
			continue
		}
		var mp model.ModelPricing
		platInput := 0.0
		platOutput := 0.0
		var platTiers model.JSON
		if err := h.db.WithContext(c.Request.Context()).Where("model_id = ?", mid).Order("effective_from DESC").First(&mp).Error; err == nil {
			platInput = mp.InputPriceRMB
			platOutput = mp.OutputPriceRMB
			platTiers = mp.PriceTiers
		}
		tmp := &model.UserModelDiscount{
			UserID:       req.UserID,
			ModelID:      mid,
			PricingType:  req.PricingType,
			DiscountRate: req.DiscountRate,
			InputPrice:   req.InputPrice,
			OutputPrice:  req.OutputPrice,
			MarkupRate:   req.MarkupRate,
		}
		finalInput, finalOutput := applyDiscountPreview(platInput, platOutput, tmp)
		item := userDiscountDTO{
			UserID:            req.UserID,
			ModelID:           mid,
			ModelName:         m.ModelName,
			ModelDisplay:      m.DisplayName,
			PricingType:       req.PricingType,
			DiscountRate:      req.DiscountRate,
			InputPrice:        req.InputPrice,
			OutputPrice:       req.OutputPrice,
			MarkupRate:        req.MarkupRate,
			Note:              req.Note,
			CostInputRMB:      m.InputCostRMB,
			CostOutputRMB:     m.OutputCostRMB,
			PlatformInputRMB:  platInput,
			PlatformOutputRMB: platOutput,
			FinalInputRMB:     finalInput,
			FinalOutputRMB:    finalOutput,
			// 扩展展示
			PricingUnit:           m.PricingUnit,
			ModelType:             m.ModelType,
			SupportsCache:         m.SupportsCache,
			CacheMechanism:        m.CacheMechanism,
			CacheMinTokens:        m.CacheMinTokens,
			CacheInputRMB:         m.CacheInputPriceRMB,
			CacheExplicitInputRMB: m.CacheExplicitInputPriceRMB,
			CacheWriteRMB:         m.CacheWritePriceRMB,
			CostPriceTiers:        m.PriceTiers,
			PlatformPriceTiers:    platTiers,
			ModelDiscount:         m.Discount,
		}
		if m.Supplier.ID != 0 {
			item.SupplierName = m.Supplier.Name
			item.SupplierDiscount = m.Supplier.Discount
		}
		results = append(results, item)
	}
	response.Success(c, gin.H{"items": results, "count": len(results)})
}

// updateRequest 更新请求体（单条）
// PricingType 为空时保留原记录的类型
type updateRequest struct {
	PricingType  string     `json:"pricing_type"`
	DiscountRate *float64   `json:"discount_rate"`
	InputPrice   *float64   `json:"input_price"`
	OutputPrice  *float64   `json:"output_price"`
	MarkupRate   *float64   `json:"markup_rate"`
	EffectiveAt  *time.Time `json:"effective_at"`
	ExpireAt     *time.Time `json:"expire_at"`
	Note         string     `json:"note"`
	IsActive     *bool      `json:"is_active"`
}

// Update PUT /admin/user-discounts/:id
func (h *UserDiscountHandler) Update(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrBadRequest.Code, "invalid id")
		return
	}
	// 读取旧记录供 audit diff
	old, _ := h.service.Get(c.Request.Context(), uint(id))
	if old != nil {
		audit.SetOldValue(c, old)
	}

	var req updateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrBadRequest.Code, err.Error())
		return
	}
	// 若请求未指定 PricingType，沿用原记录类型
	if req.PricingType == "" && old != nil {
		req.PricingType = old.PricingType
	}
	if req.PricingType == "" {
		req.PricingType = "DISCOUNT" // 兜底默认值
	}

	var operatorID uint
	if v, ok := c.Get("userId"); ok {
		if u, ok := v.(uint); ok {
			operatorID = u
		}
	}
	in := pricing.UserDiscountInput{
		PricingType:  req.PricingType,
		DiscountRate: req.DiscountRate,
		InputPrice:   req.InputPrice,
		OutputPrice:  req.OutputPrice,
		MarkupRate:   req.MarkupRate,
		EffectiveAt:  req.EffectiveAt,
		ExpireAt:     req.ExpireAt,
		Note:         req.Note,
		IsActive:     req.IsActive,
		OperatorID:   operatorID,
	}
	row, err := h.service.Update(c.Request.Context(), uint(id), &in)
	if err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, err.Error())
		return
	}
	full, _ := h.service.Get(c.Request.Context(), row.ID)
	if full != nil {
		response.Success(c, h.buildDTO(c, full))
		return
	}
	response.Success(c, h.buildDTO(c, row))
}

// Delete DELETE /admin/user-discounts/:id
func (h *UserDiscountHandler) Delete(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrBadRequest.Code, "invalid id")
		return
	}
	old, _ := h.service.Get(c.Request.Context(), uint(id))
	if old != nil {
		audit.SetOldValue(c, old)
	}
	if err := h.service.Delete(c.Request.Context(), uint(id)); err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, err.Error())
		return
	}
	response.Success(c, gin.H{"id": id})
}
