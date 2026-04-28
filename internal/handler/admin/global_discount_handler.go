package admin

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"tokenhub-server/internal/middleware"
	"tokenhub-server/internal/pkg/errcode"
	"tokenhub-server/internal/pkg/response"
	exchangesvc "tokenhub-server/internal/service/exchange"
	"tokenhub-server/internal/service/pricing"
)

// GlobalDiscountHandler 全局折扣引擎 HTTP 入口。
//
// 提供:
//   - POST /admin/ai-models/:id/apply-global-discount
//   - POST /admin/ai-models/:id/preview-global-discount
//   - PUT  /admin/ai-models/:id/lock-overrides
//   - DELETE /admin/ai-models/:id/lock-overrides/:archKey
type GlobalDiscountHandler struct {
	svc         *pricing.GlobalDiscountService
	exchangeSvc *exchangesvc.ExchangeRateService
}

// NewGlobalDiscountHandler 构造 handler。exchangeSvc 可空,空时不锁定汇率。
func NewGlobalDiscountHandler(db *gorm.DB, exchangeSvc *exchangesvc.ExchangeRateService) *GlobalDiscountHandler {
	return &GlobalDiscountHandler{
		svc:         pricing.NewGlobalDiscountService(db),
		exchangeSvc: exchangeSvc,
	}
}

// applyDiscountRequest 请求体。
type applyDiscountRequest struct {
	Rate              float64  `json:"rate" binding:"required"`      // 折扣率,如 0.85
	Scopes            []string `json:"scopes,omitempty"`             // ["base","thinking","cache","tiers"],留空时全部应用
	PreserveOverrides *bool    `json:"preserve_overrides,omitempty"` // 默认 true
}

// ApplyGlobalDiscount 把全局折扣应用到模型所有价格档(写库)。
//
//	POST /admin/ai-models/:id/apply-global-discount
//	body: { rate: 0.85, scopes: ["base","tiers"] }
func (h *GlobalDiscountHandler) ApplyGlobalDiscount(c *gin.Context) {
	id, ok := h.parseModelID(c)
	if !ok {
		return
	}
	req, ok := h.bindRequest(c)
	if !ok {
		return
	}
	preserve := true
	if req.PreserveOverrides != nil {
		preserve = *req.PreserveOverrides
	}

	exchangeRate, exchangeSrc := h.lookupExchangeRate(c)
	res, err := h.svc.Apply(c.Request.Context(), pricing.ApplyRequest{
		ModelID:            uint(id),
		Rate:               req.Rate,
		Scopes:             toScopes(req.Scopes),
		PreserveOverrides:  preserve,
		ExchangeRate:       exchangeRate,
		ExchangeRateSource: exchangeSrc,
	})
	if err != nil {
		h.sendApplyError(c, err)
		return
	}
	// 触发公开模型缓存失效,使官网同步价格立即对用户可见
	middleware.CacheInvalidate("cache:/api/v1/public/models*")
	response.Success(c, res)
}

// PreviewGlobalDiscount 预览,不写库。
//
//	POST /admin/ai-models/:id/preview-global-discount
//	body: { rate: 0.85, scopes: ["base","tiers"] }
func (h *GlobalDiscountHandler) PreviewGlobalDiscount(c *gin.Context) {
	id, ok := h.parseModelID(c)
	if !ok {
		return
	}
	req, ok := h.bindRequest(c)
	if !ok {
		return
	}
	preserve := true
	if req.PreserveOverrides != nil {
		preserve = *req.PreserveOverrides
	}
	res, err := h.svc.Preview(c.Request.Context(), pricing.ApplyRequest{
		ModelID:           uint(id),
		Rate:              req.Rate,
		Scopes:            toScopes(req.Scopes),
		PreserveOverrides: preserve,
	})
	if err != nil {
		h.sendApplyError(c, err)
		return
	}
	response.Success(c, res)
}

// SetLockOverride 解锁某档(该档保持原值,不参与全局折扣应用)。
//
//	PUT /admin/ai-models/:id/lock-overrides
//	body: { arch_key: "cache_input", value: 0.10 }
func (h *GlobalDiscountHandler) SetLockOverride(c *gin.Context) {
	id, ok := h.parseModelID(c)
	if !ok {
		return
	}
	var req struct {
		ArchKey string  `json:"arch_key" binding:"required"`
		Value   float64 `json:"value"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrValidation.Code, err.Error())
		return
	}
	if err := h.svc.SetLockOverride(c.Request.Context(), uint(id), req.ArchKey, req.Value); err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, err.Error())
		return
	}
	response.Success(c, gin.H{"message": "lock override set", "arch_key": req.ArchKey, "value": req.Value})
}

// ClearLockOverride 清除某档解锁,该档恢复参与全局折扣。
//
//	DELETE /admin/ai-models/:id/lock-overrides/:archKey
func (h *GlobalDiscountHandler) ClearLockOverride(c *gin.Context) {
	id, ok := h.parseModelID(c)
	if !ok {
		return
	}
	archKey := c.Param("archKey")
	if archKey == "" {
		response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrValidation.Code, "arch_key 为空")
		return
	}
	if err := h.svc.ClearLockOverride(c.Request.Context(), uint(id), archKey); err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, err.Error())
		return
	}
	response.Success(c, gin.H{"message": "lock override cleared", "arch_key": archKey})
}

// ===== helpers =====

func (h *GlobalDiscountHandler) parseModelID(c *gin.Context) (uint64, bool) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || id == 0 {
		response.Error(c, http.StatusBadRequest, errcode.ErrValidation)
		return 0, false
	}
	return id, true
}

func (h *GlobalDiscountHandler) bindRequest(c *gin.Context) (*applyDiscountRequest, bool) {
	var req applyDiscountRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrValidation.Code, err.Error())
		return nil, false
	}
	return &req, true
}

func (h *GlobalDiscountHandler) sendApplyError(c *gin.Context, err error) {
	if errors.Is(err, pricing.ErrInvalidDiscountRate) {
		response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrValidation.Code, err.Error())
		return
	}
	response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, err.Error())
}

// lookupExchangeRate 从 ExchangeRateService 获取当前 USD/CNY 汇率,
// 用于在 Apply 时锁定到 ModelPricing.PricedAtExchangeRate 字段。
//
// 失败时返回 (0, ""),Apply 内部不会更新汇率字段。
func (h *GlobalDiscountHandler) lookupExchangeRate(c *gin.Context) (float64, string) {
	if h.exchangeSvc == nil {
		return 0, ""
	}
	rate, err := h.exchangeSvc.GetUSDToCNY(c.Request.Context())
	if err != nil || rate <= 0 {
		return 0, ""
	}
	return rate, "exchange_rate_service"
}

func toScopes(raw []string) []pricing.GlobalDiscountScope {
	if len(raw) == 0 {
		return nil
	}
	out := make([]pricing.GlobalDiscountScope, 0, len(raw))
	for _, s := range raw {
		out = append(out, pricing.GlobalDiscountScope(s))
	}
	return out
}
