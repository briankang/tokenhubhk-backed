package admin

import (
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"tokenhub-server/internal/model"
	"tokenhub-server/internal/pkg/response"
	billingsvc "tokenhub-server/internal/service/billing"
	"tokenhub-server/internal/service/pricing"
)

type ModelOpsHandler struct {
	db *gorm.DB

	calculatorSeedOnce sync.Once
	calculatorSeedErr  error
	cacheMu            sync.RWMutex
	profileCache       map[string]modelOpsCacheEntry

	// B1 任务：将 calculate-preview 端点附加权威 BillingQuote。
	// 与 /admin/billing/quote-preview 共用同一份 QuoteService，保证试算与真实扣费同源。
	quoteSvc *billingsvc.QuoteService
}

type modelOpsCacheEntry struct {
	expiresAt time.Time
	payload   modelOpsListResponse
}

const modelOpsOfficialUSDToCNYRate = 7.10

func NewModelOpsHandler(db *gorm.DB) *ModelOpsHandler {
	return &ModelOpsHandler{
		db:           db,
		profileCache: make(map[string]modelOpsCacheEntry),
		quoteSvc:     billingsvc.NewQuoteService(db, pricing.NewPricingCalculator(db)),
	}
}

func (h *ModelOpsHandler) Register(rg *gin.RouterGroup) {
	rg.GET("/model-ops/profiles", h.ListProfiles)
	rg.GET("/model-ops/calculators", h.ListCalculators)
	rg.GET("/model-ops/scenarios", h.ListScenarios)
	rg.POST("/model-ops/calculators", h.CreateCalculator)
	rg.PUT("/model-ops/calculators/:code", h.UpdateCalculator)
	rg.POST("/model-ops/calculators/reset-defaults", h.ResetCalculators)
	rg.POST("/model-ops/calculate-preview", h.CalculatePreview)
	rg.POST("/model-ops/batch-preview", h.BatchPreview)
	rg.POST("/model-ops/batch-execute", h.BatchExecute)
}

type modelOpsLabel struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

type modelOpsAlias struct {
	ID              uint    `json:"id"`
	AliasName       string  `json:"alias_name"`
	TargetModelName string  `json:"target_model_name"`
	SupplierID      uint    `json:"supplier_id"`
	AliasType       string  `json:"alias_type"`
	Source          string  `json:"source"`
	Confidence      float64 `json:"confidence"`
	IsPublic        bool    `json:"is_public"`
	IsActive        bool    `json:"is_active"`
	Notes           string  `json:"notes,omitempty"`
}

type modelOpsRouteSummary struct {
	Total   int64 `json:"total"`
	Active  int64 `json:"active"`
	Healthy int64 `json:"healthy"`
}

type modelOpsUsageSummary struct {
	Requests             int64   `json:"requests"`
	Successes            int64   `json:"successes"`
	AvgLatency           float64 `json:"avg_latency_ms"`
	SuccessRate          float64 `json:"success_rate"`
	PromptTokens         int64   `json:"prompt_tokens"`
	CompletionTokens     int64   `json:"completion_tokens"`
	TotalTokens          int64   `json:"total_tokens"`
	CacheReadTokens      int64   `json:"cache_read_tokens"`
	CacheWriteTokens     int64   `json:"cache_write_tokens"`
	ImageCount           int64   `json:"image_count"`
	CharCount            int64   `json:"char_count"`
	DurationSec          float64 `json:"duration_sec"`
	CallCount            int64   `json:"call_count"`
	ActualRevenueRMB     float64 `json:"actual_revenue_rmb"`
	PlatformCostRMB      float64 `json:"platform_cost_rmb"`
	GrossProfitRMB       float64 `json:"gross_profit_rmb"`
	GrossProfitMargin    float64 `json:"gross_profit_margin"`
	UnderCollectedRMB    float64 `json:"under_collected_rmb"`
	CacheSavingsRMB      float64 `json:"cache_savings_rmb"`
	DeductFailedRequests int64   `json:"deduct_failed_requests"`
}

type modelOpsPriceSummary struct {
	PricingUnit                  string            `json:"pricing_unit"`
	OfficialInput                float64           `json:"official_input"`
	OfficialOutput               float64           `json:"official_output"`
	OfficialInputUSD             float64           `json:"official_input_usd,omitempty"`
	OfficialOutputUSD            float64           `json:"official_output_usd,omitempty"`
	OfficialOutputThinkingUSD    float64           `json:"official_output_thinking_usd,omitempty"`
	OfficialFXRate               float64           `json:"official_fx_rate,omitempty"`
	OfficialSourceCurrency       string            `json:"official_source_currency,omitempty"`
	OfficialInputCredits         int64             `json:"official_input_credits"`
	OfficialOutputCredits        int64             `json:"official_output_credits"`
	OfficialOutputThinking       float64           `json:"official_output_thinking,omitempty"`
	SellingInput                 float64           `json:"selling_input"`
	SellingOutput                float64           `json:"selling_output"`
	SellingInputCredits          int64             `json:"selling_input_credits"`
	SellingOutputCredits         int64             `json:"selling_output_credits"`
	SellingOutputThinking        float64           `json:"selling_output_thinking,omitempty"`
	SellingOutputThinkingCredits int64             `json:"selling_output_thinking_credits,omitempty"`
	EffectiveInput               float64           `json:"effective_input"`
	EffectiveOutput              float64           `json:"effective_output"`
	SupplierDiscount             float64           `json:"supplier_discount"`
	ModelDiscount                float64           `json:"model_discount"`
	Currency                     string            `json:"currency"`
	// 全局折扣引擎(v2)元数据,前端价格弹窗滑块需要回显该值。
	// GlobalDiscountRate=0 表示未启用全局折扣;>0 表示售价 = 官网价 × 该值。
	GlobalDiscountRate     float64    `json:"global_discount_rate,omitempty"`
	GlobalDiscountAnchored bool       `json:"global_discount_anchored,omitempty"`
	PricedAtAt             *time.Time `json:"priced_at_at,omitempty"`
	PricedAtExchangeRate   float64    `json:"priced_at_exchange_rate,omitempty"`
	HasTiers               bool       `json:"has_tiers"`
	PriceTiers                   []PublicPriceTier `json:"price_tiers,omitempty"`
	SupportsCache                bool              `json:"supports_cache"`
	CacheMechanism               string            `json:"cache_mechanism"`
	CacheMinTokens               int               `json:"cache_min_tokens,omitempty"`
	CacheInputPriceRMB           float64           `json:"cache_input_price_rmb,omitempty"`
	CacheExplicitInputPriceRMB   float64           `json:"cache_explicit_input_price_rmb,omitempty"`
	CacheWritePriceRMB           float64           `json:"cache_write_price_rmb,omitempty"`
	CacheStoragePriceRMB         float64           `json:"cache_storage_price_rmb,omitempty"`
	CacheInputPriceUSD           float64           `json:"cache_input_price_usd,omitempty"`
	CacheExplicitInputPriceUSD   float64           `json:"cache_explicit_input_price_usd,omitempty"`
	CacheWritePriceUSD           float64           `json:"cache_write_price_usd,omitempty"`
	CacheStoragePriceUSD         float64           `json:"cache_storage_price_usd,omitempty"`
}

type modelOpsProfile struct {
	ID                 uint                 `json:"id"`
	ModelName          string               `json:"model_name"`
	DisplayName        string               `json:"display_name"`
	Description        string               `json:"description"`
	SupplierID         uint                 `json:"supplier_id"`
	SupplierName       string               `json:"supplier_name"`
	SupplierCode       string               `json:"supplier_code"`
	PricingURL         string               `json:"pricing_url,omitempty"`
	CategoryName       string               `json:"category_name"`
	ModelType          string               `json:"model_type"`
	Version            string               `json:"version"`
	Domain             string               `json:"domain"`
	Source             string               `json:"source"`
	SupplierStatus     string               `json:"supplier_status"`
	Tags               string               `json:"tags"`
	Status             string               `json:"status"`
	IsActive           bool                 `json:"is_active"`
	IsFreeTier         bool                 `json:"is_free_tier"`
	CalculatorType     string               `json:"calculator_type"`
	CalculatorStatus   string               `json:"calculator_status"`
	PriceStatus        string               `json:"price_status"`
	LabelStatus        string               `json:"label_status"`
	RouteStatus        string               `json:"route_status"`
	HealthStatus       string               `json:"health_status"`
	PublicStatus       string               `json:"public_status"`
	RiskLevel          string               `json:"risk_level"`
	RiskReasons        []string             `json:"risk_reasons"`
	SuggestedActions   []string             `json:"suggested_actions"`
	Price              modelOpsPriceSummary `json:"price"`
	Routes             modelOpsRouteSummary `json:"routes"`
	Usage24h           modelOpsUsageSummary `json:"usage_24h"`
	Labels             []modelOpsLabel      `json:"labels"`
	Aliases            []modelOpsAlias      `json:"aliases"`
	Features           []string             `json:"features"`
	InputModalities    []string             `json:"input_modalities"`
	OutputModalities   []string             `json:"output_modalities"`
	TaskTypes          []string             `json:"task_types"`
	ContextWindow      int                  `json:"context_window"`
	MaxTokens          int                  `json:"max_tokens"`
	MaxInputTokens     int                  `json:"max_input_tokens"`
	MaxOutputTokens    int                  `json:"max_output_tokens"`
	CallCount          int64                `json:"call_count"`
	LastSyncedAt       *time.Time           `json:"last_synced_at,omitempty"`
	CreatedAt          time.Time            `json:"created_at"`
	UpdatedAt          time.Time            `json:"updated_at"`
	CalculatorHint     string               `json:"calculator_hint"`
	CompatibilityNotes []string             `json:"compatibility_notes"`
}

type modelOpsStats struct {
	Total             int `json:"total"`
	Healthy           int `json:"healthy"`
	Visible           int `json:"visible"`
	HighRisk          int `json:"high_risk"`
	NeedPrice         int `json:"need_price"`
	NeedCalculator    int `json:"need_calculator"`
	NeedLabels        int `json:"need_labels"`
	NoRoute           int `json:"no_route"`
	NegativeMargin    int `json:"negative_margin"`
	NeedsVerification int `json:"needs_verification"`
}

type modelOpsListResponse struct {
	List     []modelOpsProfile `json:"list"`
	Total    int64             `json:"total"`
	Page     int               `json:"page"`
	PageSize int               `json:"page_size"`
	Stats    modelOpsStats     `json:"stats"`
}

type calculatorFieldSpec struct {
	Key     string      `json:"key"`
	Label   string      `json:"label"`
	Type    string      `json:"type"`
	Unit    string      `json:"unit,omitempty"`
	Default interface{} `json:"default,omitempty"`
	Options []string    `json:"options,omitempty"`
	Help    string      `json:"help,omitempty"`
}

type calculatorSpec struct {
	Type             string                `json:"type"`
	Name             string                `json:"name"`
	ModelTypes       []string              `json:"model_types"`
	PricingUnits     []string              `json:"pricing_units"`
	Description      string                `json:"description"`
	AccuracyLevel    string                `json:"accuracy_level"`
	Fields           []calculatorFieldSpec `json:"fields"`
	Formula          []string              `json:"formula"`
	CompatibilityTip string                `json:"compatibility_tip,omitempty"`
	IsActive         bool                  `json:"is_active"`
	Version          string                `json:"version"`
	Source           string                `json:"source"`
}

type calculatePreviewRequest struct {
	ModelID uint                   `json:"model_id"`
	Params  map[string]interface{} `json:"params"`
	// B4: 折扣视角切换。0 表示平台底价（默认），>0 表示按该用户的折扣链路重算。
	// 直接透传给 QuoteService.Calculate.UserID，复用真实计费的 4 层折扣解析。
	ViewerUserID uint `json:"viewer_user_id,omitempty"`
}

type priceLayer struct {
	Name        string  `json:"name"`
	UnitPrice   float64 `json:"unit_price"`
	Quantity    float64 `json:"quantity"`
	Amount      float64 `json:"amount"`
	Description string  `json:"description"`
}

type calculationStep struct {
	Section     string  `json:"section"`
	Label       string  `json:"label"`
	UnitPrice   float64 `json:"unit_price"`
	Quantity    float64 `json:"quantity"`
	Amount      float64 `json:"amount"`
	Formula     string  `json:"formula"`
	Description string  `json:"description"`
}

type tierMatchSnapshot struct {
	Side       string  `json:"side"`
	Name       string  `json:"name"`
	Index      int     `json:"index"`
	Source     string  `json:"source"`
	InputMin   int64   `json:"input_min"`
	InputMax   *int64  `json:"input_max,omitempty"`
	OutputMin  int64   `json:"output_min"`
	OutputMax  *int64  `json:"output_max,omitempty"`
	Input      float64 `json:"input_price"`
	Output     float64 `json:"output_price"`
	CacheRead  float64 `json:"cache_read_price,omitempty"`
	CacheWrite float64 `json:"cache_write_price,omitempty"`
	Thinking   float64 `json:"thinking_price,omitempty"`
	Matched    bool    `json:"matched"`
}

type calculatePreviewResponse struct {
	ModelID            uint                   `json:"model_id"`
	ModelName          string                 `json:"model_name"`
	CalculatorType     string                 `json:"calculator_type"`
	CalculatorStatus   string                 `json:"calculator_status"`
	Currency           string                 `json:"currency"`
	OfficialAmount     float64                `json:"official_amount"`
	SellingAmount      float64                `json:"selling_amount"`
	EffectiveCost      float64                `json:"effective_cost"`
	GrossProfit        float64                `json:"gross_profit"`
	FinalDiscount      float64                `json:"final_discount"`
	Layers             []priceLayer           `json:"layers"`
	Steps              []calculationStep      `json:"steps"`
	TierMatches        []tierMatchSnapshot    `json:"tier_matches"`
	SpecialParams      map[string]interface{} `json:"special_params"`
	RegularInputTokens float64                `json:"regular_input_tokens"`
	CacheReadTokens    float64                `json:"cache_read_tokens"`
	CacheWriteTokens   float64                `json:"cache_write_tokens"`
	CacheWrite1hTokens float64                `json:"cache_write_1h_tokens"`
	ThinkingTokens     float64                `json:"thinking_tokens"`
	CacheSavings       float64                `json:"cache_savings"`
	Formula            []string               `json:"formula"`
	Warnings           []string               `json:"warnings"`
	// B1 任务：附加权威 BillingQuote（与真实扣费同源）。
	// 前端可从此字段读取 quote_hash / line_items / matched_tier_name 等三方一致字段；
	// 旧字段（layers/steps/...）仍保留作为兼容展示。
	Quote *billingsvc.BillingQuote `json:"quote,omitempty"`
	// QuoteSource 标记 quote 字段的来源：
	//   - "quote_service"：QuoteService.Calculate 成功生成（权威）
	//   - "unavailable"：生成失败（如缺 model_pricings），quote 为 nil
	QuoteSource string `json:"quote_source,omitempty"`
}

type routeAgg struct {
	StandardModelID string
	Total           int64
	Active          int64
	Healthy         int64
}

type usageAgg struct {
	ModelName             string
	Requests              int64
	Successes             int64
	AvgLatency            float64
	PromptTokens          int64
	CompletionTokens      int64
	TotalTokens           int64
	CacheReadTokens       int64
	CacheWriteTokens      int64
	ImageCount            int64
	CharCount             int64
	DurationSec           float64
	CallCount             int64
	ActualRevenueRMB      float64
	PlatformCostRMB       float64
	UnderCollectedCredits int64
	CacheSavingsRMB       float64
	DeductFailedRequests  int64
}

func (h *ModelOpsHandler) ListProfiles(c *gin.Context) {
	page := intQuery(c, "page", 1)
	pageSize := intQuery(c, "page_size", 30)
	if pageSize > 200 {
		pageSize = 200
	}
	search := strings.TrimSpace(c.Query("search"))
	cacheKey := c.Request.URL.RawQuery
	if cached, ok := h.getProfileCache(cacheKey); ok {
		response.Success(c, cached)
		return
	}

	var models []model.AIModel
	q := h.db.Preload("Supplier").Preload("Category").Preload("Pricing").Order("updated_at DESC")
	if search != "" {
		like := "%" + search + "%"
		q = q.Where("model_name LIKE ? OR display_name LIKE ? OR tags LIKE ?", like, like, like)
	}
	if suppliers := csvQuery(c, "supplier"); len(suppliers) > 0 {
		q = q.Joins("LEFT JOIN suppliers ON suppliers.id = ai_models.supplier_id").
			Where("suppliers.name IN ? OR suppliers.code IN ?", suppliers, suppliers)
	}
	if modelTypes := csvQuery(c, "model_type"); len(modelTypes) > 0 {
		q = q.Where("model_type IN ?", modelTypes)
	}
	if err := q.Find(&models).Error; err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, 500, err.Error())
		return
	}

	ids := make([]uint, 0, len(models))
	for _, item := range models {
		ids = append(ids, item.ID)
	}

	labels := h.loadLabels(ids)
	aliases := h.loadAliases()
	routes := h.loadRoutes()
	usage := h.loadUsage24h()

	profiles := make([]modelOpsProfile, 0, len(models))
	for _, item := range models {
		profile := buildModelOpsProfile(item, labels[item.ID], routes[item.ModelName], usage[item.ModelName])
		profile.Aliases = aliases[item.ModelName]
		if !matchesProfileFilters(c, profile) {
			continue
		}
		profiles = append(profiles, profile)
	}
	sortModelOpsProfiles(profiles)

	stats := buildModelOpsStats(profiles)
	total := int64(len(profiles))
	start := (page - 1) * pageSize
	if start > len(profiles) {
		start = len(profiles)
	}
	end := start + pageSize
	if end > len(profiles) {
		end = len(profiles)
	}

	payload := modelOpsListResponse{
		List:     profiles[start:end],
		Total:    total,
		Page:     page,
		PageSize: pageSize,
		Stats:    stats,
	}
	h.setProfileCache(cacheKey, payload)
	response.Success(c, payload)
}

func sortModelOpsProfiles(profiles []modelOpsProfile) {
	sort.SliceStable(profiles, func(i, j int) bool {
		if profiles[i].IsActive != profiles[j].IsActive {
			return profiles[i].IsActive
		}
		return false
	})
}

func (h *ModelOpsHandler) ListCalculators(c *gin.Context) {
	configs, err := h.loadCalculatorSpecs(true)
	if err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, 500, err.Error())
		return
	}
	response.Success(c, configs)
}

type updateCalculatorRequest struct {
	Type             *string               `json:"type"`
	Name             *string               `json:"name"`
	Description      *string               `json:"description"`
	ModelTypes       []string              `json:"model_types"`
	PricingUnits     []string              `json:"pricing_units"`
	Fields           []calculatorFieldSpec `json:"fields"`
	Formula          []string              `json:"formula"`
	AccuracyLevel    *string               `json:"accuracy_level"`
	CompatibilityTip *string               `json:"compatibility_tip"`
	IsActive         *bool                 `json:"is_active"`
	Version          *string               `json:"version"`
	Source           *string               `json:"source"`
}

func (h *ModelOpsHandler) CreateCalculator(c *gin.Context) {
	var req updateCalculatorRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.ErrorMsg(c, http.StatusBadRequest, 400, err.Error())
		return
	}
	code := ""
	if req.Type != nil {
		code = normalizeCalculatorCode(*req.Type)
	}
	if code == "" {
		response.ErrorMsg(c, http.StatusBadRequest, 400, "calculator type/code cannot be empty")
		return
	}
	if err := h.ensureCalculatorConfigs(); err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, 500, err.Error())
		return
	}
	var count int64
	if err := h.db.Model(&model.PriceCalculatorConfig{}).Where("code = ?", code).Count(&count).Error; err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, 500, err.Error())
		return
	}
	if count > 0 {
		response.ErrorMsg(c, http.StatusConflict, 409, "calculator already exists")
		return
	}
	spec := calculatorSpec{
		Type:          code,
		Name:          valueOrDefault(req.Name, "Custom Calculator"),
		Description:   valueOrDefault(req.Description, "Custom pricing calculator"),
		ModelTypes:    req.ModelTypes,
		PricingUnits:  req.PricingUnits,
		Fields:        req.Fields,
		Formula:       req.Formula,
		AccuracyLevel: valueOrDefault(req.AccuracyLevel, "custom"),
		IsActive:      true,
		Version:       valueOrDefault(req.Version, "v1"),
		Source:        valueOrDefault(req.Source, "custom"),
	}
	if req.CompatibilityTip != nil {
		spec.CompatibilityTip = strings.TrimSpace(*req.CompatibilityTip)
	}
	if req.IsActive != nil {
		spec.IsActive = *req.IsActive
	}
	cfg := calculatorSpecToConfig(spec)
	if err := h.db.Create(&cfg).Error; err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, 500, err.Error())
		return
	}
	h.clearProfileCache()
	response.Success(c, configToCalculatorSpec(cfg))
}

func (h *ModelOpsHandler) UpdateCalculator(c *gin.Context) {
	code := normalizeCalculatorCode(c.Param("code"))
	if code == "" {
		response.ErrorMsg(c, http.StatusBadRequest, 400, "code 不能为空")
		return
	}
	var req updateCalculatorRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.ErrorMsg(c, http.StatusBadRequest, 400, err.Error())
		return
	}
	if err := h.ensureCalculatorConfigs(); err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, 500, err.Error())
		return
	}
	var cfg model.PriceCalculatorConfig
	if err := h.db.Where("code = ?", code).First(&cfg).Error; err != nil {
		response.ErrorMsg(c, http.StatusNotFound, 404, "浠锋牸璁＄畻鍣ㄤ笉瀛樺湪")
		return
	}
	updates := map[string]interface{}{}
	if req.Name != nil {
		updates["name"] = *req.Name
	}
	if req.Description != nil {
		updates["description"] = *req.Description
	}
	if req.ModelTypes != nil {
		updates["model_types"] = mustJSON(req.ModelTypes)
	}
	if req.PricingUnits != nil {
		updates["pricing_units"] = mustJSON(req.PricingUnits)
	}
	if req.Fields != nil {
		fields := make([]calculatorFieldSpec, len(req.Fields))
		for i := range req.Fields {
			fields[i] = normalizeCalculatorField(code, req.Fields[i])
		}
		updates["fields_schema"] = mustJSON(fields)
	}
	if req.Formula != nil {
		updates["formula"] = mustJSON(req.Formula)
	}
	if req.AccuracyLevel != nil {
		updates["accuracy_level"] = *req.AccuracyLevel
	}
	if req.CompatibilityTip != nil {
		updates["compatibility_tip"] = *req.CompatibilityTip
	}
	if req.IsActive != nil {
		updates["is_active"] = *req.IsActive
	}
	if req.Version != nil {
		updates["version"] = *req.Version
	}
	if req.Source != nil {
		updates["source"] = *req.Source
	}
	if len(updates) > 0 {
		if err := h.db.Model(&model.PriceCalculatorConfig{}).Where("code = ?", code).Updates(updates).Error; err != nil {
			response.ErrorMsg(c, http.StatusInternalServerError, 500, err.Error())
			return
		}
	}
	var out model.PriceCalculatorConfig
	h.db.Where("code = ?", code).First(&out)
	h.clearProfileCache()
	response.Success(c, configToCalculatorSpec(out))
}

func (h *ModelOpsHandler) ResetCalculators(c *gin.Context) {
	if err := h.ensureCalculatorConfigs(); err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, 500, err.Error())
		return
	}
	count := int64(0)
	for _, spec := range calculatorCatalog() {
		cfg := calculatorSpecToConfig(spec)
		var existing model.PriceCalculatorConfig
		if err := h.db.Where("code = ?", cfg.Code).First(&existing).Error; err == nil {
			if err := h.db.Model(&model.PriceCalculatorConfig{}).Where("code = ?", cfg.Code).Updates(map[string]interface{}{
				"name": cfg.Name, "description": cfg.Description, "model_types": cfg.ModelTypes, "pricing_units": cfg.PricingUnits,
				"fields_schema": cfg.FieldsSchema, "formula": cfg.Formula, "accuracy_level": cfg.AccuracyLevel,
				"compatibility_tip": cfg.CompatibilityTip, "is_active": cfg.IsActive, "version": cfg.Version, "source": "builtin",
			}).Error; err != nil {
				response.ErrorMsg(c, http.StatusInternalServerError, 500, err.Error())
				return
			}
		} else if err := h.db.Create(&cfg).Error; err != nil {
			response.ErrorMsg(c, http.StatusInternalServerError, 500, err.Error())
			return
		}
		count++
	}
	h.clearProfileCache()
	response.Success(c, gin.H{"updated": count})
}

func (h *ModelOpsHandler) getProfileCache(key string) (modelOpsListResponse, bool) {
	h.cacheMu.RLock()
	defer h.cacheMu.RUnlock()
	entry, ok := h.profileCache[key]
	if !ok || time.Now().After(entry.expiresAt) {
		return modelOpsListResponse{}, false
	}
	return entry.payload, true
}

func (h *ModelOpsHandler) setProfileCache(key string, payload modelOpsListResponse) {
	h.cacheMu.Lock()
	defer h.cacheMu.Unlock()
	h.profileCache[key] = modelOpsCacheEntry{
		expiresAt: time.Now().Add(15 * time.Second),
		payload:   payload,
	}
}

func (h *ModelOpsHandler) clearProfileCache() {
	h.cacheMu.Lock()
	defer h.cacheMu.Unlock()
	h.profileCache = make(map[string]modelOpsCacheEntry)
}

func (h *ModelOpsHandler) CalculatePreview(c *gin.Context) {
	var req calculatePreviewRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.ErrorMsg(c, http.StatusBadRequest, 400, err.Error())
		return
	}
	if req.ModelID == 0 {
		response.ErrorMsg(c, http.StatusBadRequest, 400, "model_id 不能为空")
		return
	}
	var item model.AIModel
	if err := h.db.Preload("Supplier").Preload("Pricing").First(&item, req.ModelID).Error; err != nil {
		response.ErrorMsg(c, http.StatusNotFound, 404, "model not found")
		return
	}
	result := calculateModelPreview(item, req.Params)

	// B1 任务：附加权威 BillingQuote（与真实扣费同源）。
	// 通过 QuoteService.Calculate(scenario="preview") 拿到 BillingQuote，
	// 让前端能直接读取 quote_hash / line_items / matched_tier_name，
	// 与 /admin/billing/quote-preview / 真实扣费 snapshot.quote 保持完全一致的口径。
	if h.quoteSvc != nil {
		quoteReq := mapPreviewParamsToQuoteRequest(req.ModelID, item.ModelName, req.Params)
		// B4: 折扣视角切换。viewer_user_id > 0 时按该用户的折扣链路重算
		// （4 层链：UserModelDiscount → AgentPricing → AgentLevelDiscount → 平台默认），
		// 与真实扣费 path 完全相同，避免试算/扣费金额脱节。
		if req.ViewerUserID > 0 {
			quoteReq.UserID = req.ViewerUserID
		}
		if quote, qerr := h.quoteSvc.Calculate(c.Request.Context(), quoteReq); qerr == nil && quote != nil {
			result.Quote = quote
			result.QuoteSource = "quote_service"
		} else {
			result.QuoteSource = "unavailable"
		}
	}

	response.Success(c, result)
}

// mapPreviewParamsToQuoteRequest 把 calculate-preview 的旧入参映射成 QuoteRequest（B1 任务）。
//
// 调用方:CalculatePreview。
// 入参: req.Params 为 map[string]interface{}，与试算面板字段对齐
// （input_tokens / output_tokens / thinking_tokens / cache_read_tokens / image_count / duration_seconds 等）。
// 输出: billingsvc.QuoteRequest（scenario=preview，userID=0 表示平台底价视角）。
//
// 安全:未识别字段静默忽略，类型不匹配回退 0；不会因脏参数导致 panic。
func mapPreviewParamsToQuoteRequest(modelID uint, modelName string, params map[string]interface{}) billingsvc.QuoteRequest {
	usage := billingsvc.QuoteUsage{
		InputTokens:        int(numParam(params, "input_tokens", 0)),
		OutputTokens:       int(numParam(params, "output_tokens", 0)),
		ReasoningTokens:    int(numParam(params, "thinking_tokens", 0)),
		CacheReadTokens:    int(numParam(params, "cache_read_tokens", 0)),
		CacheWriteTokens:   int(numParam(params, "cache_write_tokens", 0)),
		CacheWrite1hTokens: int(numParam(params, "cache_write_1h_tokens", 0)),
		ImageCount:         int(numParam(params, "image_count", 0)),
		CharCount:          int(numParam(params, "char_count", 0)),
		DurationSec:        numParam(params, "duration_seconds", 0),
		CallCount:          int(numParam(params, "call_count", 0)),
	}
	thinkingMode := boolParamFlexible(params, "enable_thinking", false)

	// dim_values 用于 PriceMatrix 维度匹配（如视频 resolution / mode / audio）
	var dim map[string]interface{}
	for _, key := range []string{"resolution", "quality", "mode", "service_tier", "generate_audio", "input_contains_video", "draft", "fps", "width", "height"} {
		if v, ok := params[key]; ok {
			if dim == nil {
				dim = make(map[string]interface{}, 4)
			}
			dim[key] = v
		}
	}

	return billingsvc.QuoteRequest{
		Scenario:     billingsvc.QuoteScenarioPreview,
		ModelID:      modelID,
		ModelName:    modelName,
		Usage:        usage,
		ThinkingMode: thinkingMode,
		DimValues:    dim,
	}
}

type modelOpsBatchRequest struct {
	Action   string                 `json:"action"`
	ModelIDs []uint                 `json:"model_ids"`
	Payload  map[string]interface{} `json:"payload"`
}

type modelOpsBatchPreviewItem struct {
	ModelID     uint     `json:"model_id"`
	ModelName   string   `json:"model_name"`
	Before      string   `json:"before"`
	After       string   `json:"after"`
	Warnings    []string `json:"warnings"`
	CanExecute  bool     `json:"can_execute"`
	Description string   `json:"description"`
	Status      string   `json:"status"`
	SkipReason  string   `json:"skip_reason,omitempty"`
}

type modelOpsBatchResponse struct {
	Action     string                     `json:"action"`
	Total      int                        `json:"total"`
	Executable int                        `json:"executable"`
	Skipped    int                        `json:"skipped"`
	Updated    int64                      `json:"updated,omitempty"`
	Items      []modelOpsBatchPreviewItem `json:"items"`
	Warnings   []string                   `json:"warnings"`
	ExecutedAt *time.Time                 `json:"executed_at,omitempty"`
}

func (h *ModelOpsHandler) BatchPreview(c *gin.Context) {
	req, ok := bindModelOpsBatchRequest(c)
	if !ok {
		return
	}
	items, warnings := h.previewBatch(req)
	response.Success(c, makeBatchResponse(req.Action, items, warnings, 0, nil))
}

func (h *ModelOpsHandler) BatchExecute(c *gin.Context) {
	req, ok := bindModelOpsBatchRequest(c)
	if !ok {
		return
	}
	items, warnings := h.previewBatch(req)
	executableIDs := make([]uint, 0, len(items))
	for _, item := range items {
		if item.CanExecute {
			executableIDs = append(executableIDs, item.ModelID)
		}
	}
	if len(executableIDs) == 0 {
		now := time.Now()
		response.Success(c, makeBatchResponse(req.Action, items, append(warnings, "没有可执行的模型"), 0, &now))
		return
	}

	var updated int64
	err := h.db.Transaction(func(tx *gorm.DB) error {
		switch req.Action {
		case "enable", "set_public":
			res := tx.Model(&model.AIModel{}).Where("id IN ?", executableIDs).Updates(batchModelStatePatch(req.Action))
			updated = res.RowsAffected
			return res.Error
		case "disable", "hide_public":
			res := tx.Model(&model.AIModel{}).Where("id IN ?", executableIDs).Updates(batchModelStatePatch(req.Action))
			updated = res.RowsAffected
			return res.Error
		case "set_free_tier":
			res := tx.Model(&model.AIModel{}).Where("id IN ?", executableIDs).Update("is_free_tier", true)
			updated = res.RowsAffected
			return res.Error
		case "unset_free_tier":
			res := tx.Model(&model.AIModel{}).Where("id IN ?", executableIDs).Update("is_free_tier", false)
			updated = res.RowsAffected
			return res.Error
		case "add_label", "mark_price_review":
			key, value := batchLabel(req)
			if req.Action == "mark_price_review" {
				key, value = "ops", "price_review"
			}
			for _, id := range executableIDs {
				label := model.ModelLabel{ModelID: id, LabelKey: key, LabelValue: value}
				if err := tx.Where(label).FirstOrCreate(&label).Error; err != nil {
					return err
				}
				updated++
			}
			return nil
		case "bind_calculator":
			calculatorType := strings.TrimSpace(stringPayload(req.Payload, "calculator_type"))
			if calculatorType == "" {
				return nil
			}
			if err := tx.Where("model_id IN ? AND label_key = ?", executableIDs, "calculator").Delete(&model.ModelLabel{}).Error; err != nil {
				return err
			}
			for _, id := range executableIDs {
				label := model.ModelLabel{ModelID: id, LabelKey: "calculator", LabelValue: calculatorType}
				if err := tx.Create(&label).Error; err != nil {
					return err
				}
				updated++
			}
			return nil
		case "unbind_calculator":
			res := tx.Where("model_id IN ? AND label_key = ?", executableIDs, "calculator").Delete(&model.ModelLabel{})
			updated = res.RowsAffected
			return res.Error
		case "remove_label":
			key, value := batchLabel(req)
			q := tx.Where("model_id IN ? AND label_key = ?", executableIDs, key)
			if value != "" {
				q = q.Where("label_value = ?", value)
			}
			res := q.Delete(&model.ModelLabel{})
			updated = res.RowsAffected
			return res.Error
		default:
			return nil
		}
	})
	if err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, 500, err.Error())
		return
	}
	h.clearProfileCache()
	now := time.Now()
	response.Success(c, makeBatchResponse(req.Action, items, warnings, updated, &now))
}

func (h *ModelOpsHandler) previewBatch(req modelOpsBatchRequest) ([]modelOpsBatchPreviewItem, []string) {
	if len(req.ModelIDs) == 0 {
		return nil, []string{"请选择至少一个模型"}
	}
	var models []model.AIModel
	h.db.Preload("Supplier").Preload("Category").Preload("Pricing").Where("id IN ?", req.ModelIDs).Find(&models)
	items := make([]modelOpsBatchPreviewItem, 0, len(models))
	for _, item := range models {
		before, after, desc, warnings, ok := describeBatchChange(req, item)
		status := "可执行"
		skipReason := ""
		if !ok {
			status = "跳过"
			if len(warnings) > 0 {
				skipReason = strings.Join(warnings, "；")
			}
		}
		items = append(items, modelOpsBatchPreviewItem{
			ModelID:     item.ID,
			ModelName:   item.ModelName,
			Before:      before,
			After:       after,
			Warnings:    warnings,
			CanExecute:  ok,
			Description: desc,
			Status:      status,
			SkipReason:  skipReason,
		})
	}
	var warnings []string
	if len(models) != len(req.ModelIDs) {
		warnings = append(warnings, "部分模型不存在或已被删除")
	}
	if req.Action == "set_public" {
		warnings = append(warnings, "公开可用仍取决于售价、渠道和健康状态")
	}
	return items, warnings
}

func buildModelOpsProfile(item model.AIModel, labels []model.ModelLabel, route modelOpsRouteSummary, usage modelOpsUsageSummary) modelOpsProfile {
	price := buildPriceSummary(item)
	calculatorType, calculatorStatus, calculatorHint, notes := classifyCalculator(item)
	if boundCalculator := labelValue(labels, "calculator"); boundCalculator != "" {
		calculatorType = boundCalculator
		calculatorStatus = "bound"
		calculatorHint = calculatorHintFor(boundCalculator)
		notes = append(notes, "Calculator explicitly bound by ops")
	}
	priceStatus := classifyPriceStatus(item, price, calculatorStatus)
	labelStatus := "confirmed"
	if len(labels) == 0 {
		labelStatus = "missing"
		if len(featureNames(item)) > 0 || item.Tags != "" {
			labelStatus = "suggested"
		}
	}
	routeStatus := "available"
	if route.Active == 0 {
		routeStatus = "no_route"
	} else if route.Healthy == 0 {
		routeStatus = "route_unhealthy"
	}
	healthStatus := "healthy"
	if !item.IsActive {
		healthStatus = "disabled"
	} else if item.Status == "error" {
		healthStatus = "error"
	} else if item.Status == "offline" {
		healthStatus = "offline"
	} else if routeStatus != "available" {
		healthStatus = routeStatus
	}
	publicStatus := "visible"
	if !item.IsActive || item.Status != "online" {
		publicStatus = "hidden"
	} else if priceStatus == "missing" || routeStatus == "no_route" {
		publicStatus = "blocked"
	} else if calculatorStatus == "needs_review" || priceStatus == "needs_verification" {
		publicStatus = "draft"
	}

	reasons := riskReasons(priceStatus, calculatorStatus, labelStatus, routeStatus, healthStatus)
	actions := suggestedActions(priceStatus, calculatorStatus, labelStatus, routeStatus, healthStatus)
	risk := "green"
	if containsAny([]string{priceStatus, calculatorStatus, routeStatus, healthStatus}, "negative_margin", "missing", "unbound", "no_route", "error", "offline") {
		risk = "red"
	} else if containsAny([]string{priceStatus, calculatorStatus, labelStatus, routeStatus}, "no_selling", "needs_verification", "needs_review", "suggested", "missing", "route_unhealthy") {
		risk = "amber"
	}

	return modelOpsProfile{
		ID:                 item.ID,
		ModelName:          item.ModelName,
		DisplayName:        item.DisplayName,
		Description:        item.Description,
		SupplierID:         item.SupplierID,
		SupplierName:       item.Supplier.Name,
		SupplierCode:       item.Supplier.Code,
		PricingURL:         item.Supplier.PricingURL,
		CategoryName:       item.Category.Name,
		ModelType:          item.ModelType,
		Version:            item.Version,
		Domain:             item.Domain,
		Source:             item.Source,
		SupplierStatus:     item.SupplierStatus,
		Tags:               item.Tags,
		Status:             item.Status,
		IsActive:           item.IsActive,
		IsFreeTier:         item.IsFreeTier,
		CalculatorType:     calculatorType,
		CalculatorStatus:   calculatorStatus,
		PriceStatus:        priceStatus,
		LabelStatus:        labelStatus,
		RouteStatus:        routeStatus,
		HealthStatus:       healthStatus,
		PublicStatus:       publicStatus,
		RiskLevel:          risk,
		RiskReasons:        reasons,
		SuggestedActions:   actions,
		Price:              price,
		Routes:             route,
		Usage24h:           usage,
		Labels:             convertLabels(labels),
		Features:           featureNames(item),
		InputModalities:    stringArrayFromJSON(item.InputModalities),
		OutputModalities:   stringArrayFromJSON(item.OutputModalities),
		TaskTypes:          stringArrayFromJSON(item.TaskTypes),
		ContextWindow:      item.ContextWindow,
		MaxTokens:          item.MaxTokens,
		MaxInputTokens:     item.MaxInputTokens,
		MaxOutputTokens:    item.MaxOutputTokens,
		CallCount:          item.CallCount,
		LastSyncedAt:       item.LastSyncedAt,
		CreatedAt:          item.CreatedAt,
		UpdatedAt:          item.UpdatedAt,
		CalculatorHint:     calculatorHint,
		CompatibilityNotes: notes,
	}
}

func buildPriceSummary(item model.AIModel) modelOpsPriceSummary {
	sellingInput, sellingOutput := 0.0, 0.0
	sellingInputCredits, sellingOutputCredits, sellingOutputThinkingCredits := int64(0), int64(0), int64(0)
	sellingOutputThinking := 0.0
	currency := item.Currency
	if item.Pricing != nil {
		sellingInput = item.Pricing.InputPriceRMB
		sellingOutput = item.Pricing.OutputPriceRMB
		sellingInputCredits = item.Pricing.InputPricePerToken
		sellingOutputCredits = item.Pricing.OutputPricePerToken
		sellingOutputThinking = item.Pricing.OutputPriceThinkingRMB
		sellingOutputThinkingCredits = item.Pricing.OutputPriceThinkingPerToken
		if item.Pricing.Currency != "" {
			currency = item.Pricing.Currency
		}
	}
	if currency == "" {
		currency = "CREDIT"
	}
	discount := item.Supplier.Discount
	if discount <= 0 {
		discount = 1
	}
	if item.Discount > 0 {
		discount = item.Discount
	}
	tiers := buildPublicTiers(item.PriceTiers)
	summary := modelOpsPriceSummary{
		PricingUnit:                  item.PricingUnit,
		OfficialInput:                item.InputCostRMB,
		OfficialOutput:               item.OutputCostRMB,
		OfficialInputCredits:         item.InputPricePerToken,
		OfficialOutputCredits:        item.OutputPricePerToken,
		OfficialOutputThinking:       item.OutputCostThinkingRMB,
		SellingInput:                 sellingInput,
		SellingOutput:                sellingOutput,
		SellingInputCredits:          sellingInputCredits,
		SellingOutputCredits:         sellingOutputCredits,
		SellingOutputThinking:        sellingOutputThinking,
		SellingOutputThinkingCredits: sellingOutputThinkingCredits,
		EffectiveInput:               item.InputCostRMB * discount,
		EffectiveOutput:              item.OutputCostRMB * discount,
		SupplierDiscount:             item.Supplier.Discount,
		ModelDiscount:                item.Discount,
		Currency:                     currency,
		HasTiers:                     len(tiers) > 0,
		PriceTiers:                   tiers,
		SupportsCache:                item.SupportsCache,
		CacheMechanism:               item.CacheMechanism,
		CacheMinTokens:               item.CacheMinTokens,
		CacheInputPriceRMB:           item.CacheInputPriceRMB,
		CacheExplicitInputPriceRMB:   item.CacheExplicitInputPriceRMB,
		CacheWritePriceRMB:           item.CacheWritePriceRMB,
		CacheStoragePriceRMB:         item.CacheStoragePriceRMB,
	}
	if item.Pricing != nil {
		summary.GlobalDiscountRate = item.Pricing.GlobalDiscountRate
		summary.GlobalDiscountAnchored = item.Pricing.GlobalDiscountAnchored
		summary.PricedAtAt = item.Pricing.PricedAtAt
		summary.PricedAtExchangeRate = item.Pricing.PricedAtExchangeRate
	}
	enrichOfficialUSDPrice(item, &summary)
	enrichOpenAIAutoCachePrice(item, &summary)
	return summary
}

func enrichOpenAIAutoCachePrice(item model.AIModel, price *modelOpsPriceSummary) {
	ratio, ok := openAICacheInputRatio(item.ModelName)
	if !ok || price == nil || price.OfficialInput <= 0 {
		return
	}
	price.SupportsCache = true
	price.CacheMechanism = "auto"
	if price.CacheMinTokens <= 0 {
		price.CacheMinTokens = 1024
	}
	if price.CacheInputPriceRMB <= 0 {
		price.CacheInputPriceRMB = roundModelOpsPrice(price.OfficialInput * ratio)
	}
	if price.CacheInputPriceUSD <= 0 && price.OfficialInputUSD > 0 {
		price.CacheInputPriceUSD = roundModelOpsPrice(price.OfficialInputUSD * ratio)
	}
}

func openAICacheInputRatio(modelName string) (float64, bool) {
	name := strings.ToLower(modelName)
	switch {
	case strings.HasPrefix(name, "gpt-4o-mini"):
		return 0.5, true
	case name == "gpt-4o" || strings.HasPrefix(name, "gpt-4o-2024-08") || strings.HasPrefix(name, "gpt-4o-2024-11"):
		return 0.5, true
	case name == "gpt-4.1" || strings.HasPrefix(name, "gpt-4.1-mini") || strings.HasPrefix(name, "gpt-4.1-nano"):
		return 0.25, true
	case strings.HasPrefix(name, "gpt-5") && !strings.Contains(name, "-pro"):
		return 0.1, true
	default:
		return 0, false
	}
}

func enrichOfficialUSDPrice(item model.AIModel, price *modelOpsPriceSummary) {
	if price == nil {
		return
	}
	if strings.EqualFold(item.PriceSourceCurrency, "USD") || item.InputCostUSD > 0 || item.OutputCostUSD > 0 {
		price.OfficialSourceCurrency = "USD"
		price.OfficialFXRate = item.PriceSourceExchangeRate
		if price.OfficialFXRate <= 0 {
			price.OfficialFXRate = modelOpsOfficialUSDToCNYRate
		}
		price.OfficialInputUSD = firstPositiveFloat(item.InputCostUSD, dividePrice(price.OfficialInput, price.OfficialFXRate))
		price.OfficialOutputUSD = firstPositiveFloat(item.OutputCostUSD, dividePrice(price.OfficialOutput, price.OfficialFXRate))
		price.OfficialOutputThinkingUSD = firstPositiveFloat(item.OutputCostThinkingUSD, dividePrice(price.OfficialOutputThinking, price.OfficialFXRate))
		price.CacheInputPriceUSD = item.CacheInputPriceUSD
		price.CacheExplicitInputPriceUSD = item.CacheExplicitInputPriceUSD
		price.CacheWritePriceUSD = item.CacheWritePriceUSD
		price.CacheStoragePriceUSD = item.CacheStoragePriceUSD
		return
	}
	if !isOverseasOfficialPrice(item) || modelOpsOfficialUSDToCNYRate <= 0 {
		return
	}
	price.OfficialSourceCurrency = "USD"
	price.OfficialFXRate = modelOpsOfficialUSDToCNYRate
	price.OfficialInputUSD = dividePrice(price.OfficialInput, modelOpsOfficialUSDToCNYRate)
	price.OfficialOutputUSD = dividePrice(price.OfficialOutput, modelOpsOfficialUSDToCNYRate)
	price.OfficialOutputThinkingUSD = dividePrice(price.OfficialOutputThinking, modelOpsOfficialUSDToCNYRate)
	price.CacheInputPriceUSD = dividePrice(price.CacheInputPriceRMB, modelOpsOfficialUSDToCNYRate)
	price.CacheExplicitInputPriceUSD = dividePrice(price.CacheExplicitInputPriceRMB, modelOpsOfficialUSDToCNYRate)
	price.CacheWritePriceUSD = dividePrice(price.CacheWritePriceRMB, modelOpsOfficialUSDToCNYRate)
	price.CacheStoragePriceUSD = dividePrice(price.CacheStoragePriceRMB, modelOpsOfficialUSDToCNYRate)
}

func firstPositiveFloat(values ...float64) float64 {
	for _, value := range values {
		if value > 0 {
			return roundModelOpsPrice(value)
		}
	}
	return 0
}

func dividePrice(value, divisor float64) float64 {
	if value <= 0 || divisor <= 0 {
		return 0
	}
	return roundModelOpsPrice(value / divisor)
}

func isOverseasOfficialPrice(item model.AIModel) bool {
	code := strings.ToLower(item.Supplier.Code)
	pricingURL := strings.ToLower(item.Supplier.PricingURL)
	modelName := strings.ToLower(item.ModelName)
	if code == "openai" || code == "anthropic" || code == "google_gemini" || code == "google" || code == "gemini" {
		return true
	}
	if strings.Contains(pricingURL, "openai.com") || strings.Contains(pricingURL, "anthropic.com") || strings.Contains(pricingURL, "ai.google.dev") || strings.Contains(pricingURL, "cloud.google.com/vertex-ai") {
		return true
	}
	return code == "wangsu_aigw" && (strings.Contains(modelName, "gpt") || strings.Contains(modelName, "claude") || strings.Contains(modelName, "gemini"))
}

func shouldIgnorePreviewCacheWrite(item model.AIModel) bool {
	name := strings.ToLower(item.ModelName)
	code := strings.ToLower(item.Supplier.Code)
	return item.CacheWritePriceRMB <= 0 && (strings.Contains(name, "gemini") || code == "google_gemini" || code == "google" || code == "gemini")
}

func roundModelOpsPrice(value float64) float64 {
	if value <= 0 || math.IsNaN(value) || math.IsInf(value, 0) {
		return 0
	}
	return math.Round(value*1000000) / 1000000
}

func classifyCalculator(item model.AIModel) (string, string, string, []string) {
	name := strings.ToLower(item.ModelName)
	unit := item.PricingUnit
	status := "bound"
	calculator := "token_io"
	hint := "Calculate input and output usage separately."
	var notes []string

	switch item.ModelType {
	case model.ModelTypeVideoGeneration:
		if strings.Contains(name, "seedance-2") || strings.Contains(name, "seedance_2") {
			return "volc_seedance_2_video_formula", status, "Estimate video tokens by duration, resolution, and fps.", []string{"Seedance 2.0 uses a dedicated video formula."}
		}
		if strings.Contains(name, "seedance-1.5") || strings.Contains(name, "seedance-1-5") {
			return "volc_seedance_1_5_video_matrix", status, "Use Seedance 1.5 video pricing matrix.", nil
		}
		if unit == model.UnitPerSecond || unit == model.UnitPerMinute || unit == model.UnitPerHour {
			return "video_duration", status, "Calculate by output video duration.", nil
		}
		if unit == "" || unit == model.UnitPerMillionTokens {
			return "video_token_formula", status, "Convert video parameters to estimated tokens.", nil
		}
		return "video_custom", "needs_review", "Video model needs a dedicated calculator.", []string{"Current pricing unit cannot express video generation parameters."}
	case model.ModelTypeImageGeneration:
		return "image_unit_matrix", status, "Calculate by image count and variant.", nil
	case model.ModelTypeVision:
		if unit == model.UnitPerImage {
			return "vision_image_unit", status, "Calculate by image count.", nil
		}
	case "3DGeneration":
		return "three_d_generation_token", status, "Calculate 3D generation token usage.", nil
	case model.ModelTypeTTS, "SpeechSynthesis":
		if unit == model.UnitPer10kCharacters || unit == model.UnitPerMillionCharacters || unit == model.UnitPerKChars {
			return "tts_character", status, "Calculate by character count.", nil
		}
		return "tts_custom", "needs_review", "TTS should usually be billed by characters.", nil
	case model.ModelTypeASR, "SpeechRecognition":
		return "asr_duration", status, "Calculate by audio duration.", nil
	case model.ModelTypeEmbedding:
		return "embedding_token", status, "Calculate embedding input tokens.", nil
	case model.ModelTypeRerank:
		if unit == model.UnitPerCall {
			return "rerank_call", status, "Calculate by call count.", nil
		}
		return "rerank_token", status, "Calculate query/document tokens.", nil
	}

	if modelOpsSupportsCache(item) || hasTieredPricing(item.PriceTiers) {
		calculator = "token_io_tiered_cache"
		hint = "Calculate input/output, tiered price, and cache usage."
	}
	if unit != "" && unit != model.UnitPerMillionTokens {
		calculator = "generic_" + unit
		hint = "Normalize a non-token pricing unit."
	}
	return calculator, status, hint, notes
}

func modelOpsSupportsCache(item model.AIModel) bool {
	if item.SupportsCache {
		return true
	}
	if item.CacheInputPriceRMB > 0 || item.CacheExplicitInputPriceRMB > 0 || item.CacheWritePriceRMB > 0 {
		return true
	}
	_, ok := openAICacheInputRatio(item.ModelName)
	return ok && item.InputCostRMB > 0
}

func calculatorCatalog() []calculatorSpec {
	return []calculatorSpec{
		{
			Type: "token_io", Name: "Token input/output", ModelTypes: []string{"LLM", "VLM", "Embedding"}, PricingUnits: []string{model.UnitPerMillionTokens},
			Description: "Calculate input and output token usage separately.", AccuracyLevel: "stable", IsActive: true, Version: "v1", Source: "builtin",
			Fields:  []calculatorFieldSpec{{Key: "input_tokens", Label: "Input tokens", Type: "number", Unit: "tokens", Default: 1000}, {Key: "output_tokens", Label: "Visible output tokens", Type: "number", Unit: "tokens", Default: 500}, {Key: "thinking_tokens", Label: "Thinking tokens", Type: "number", Unit: "tokens", Default: 0}},
			Formula: []string{"input cost = input price / 1,000,000 * input tokens", "output cost = output price / 1,000,000 * output tokens"},
		},
		{
			Type: "token_io_tiered_cache", Name: "Token tier/cache", ModelTypes: []string{"LLM", "VLM"}, PricingUnits: []string{model.UnitPerMillionTokens},
			Description: "Calculate token usage with tiers, cache read/write, cache storage, and reasoning output fields.", AccuracyLevel: "compatible", IsActive: true, Version: "v2", Source: "builtin",
			Fields: []calculatorFieldSpec{
				{Key: "input_tokens", Label: "Input tokens", Type: "number", Unit: "tokens", Default: 1000},
				{Key: "output_tokens", Label: "Visible output tokens", Type: "number", Unit: "tokens", Default: 500},
				{Key: "thinking_tokens", Label: "Thinking tokens", Type: "number", Unit: "tokens", Default: 0},
				{Key: "cache_read_tokens", Label: "Cache read tokens", Type: "number", Unit: "tokens", Default: 0},
				{Key: "cache_write_tokens", Label: "Cache write tokens", Type: "number", Unit: "tokens", Default: 0},
				{Key: "cache_write_1h_tokens", Label: "1h cache write tokens", Type: "number", Unit: "tokens", Default: 0},
				{Key: "cache_storage_tokens", Label: "Cache storage tokens", Type: "number", Unit: "tokens", Default: 0},
				{Key: "cache_storage_hours", Label: "Cache storage hours", Type: "number", Unit: "hours", Default: 0},
				{Key: "enable_thinking", Label: "Enable thinking", Type: "boolean", Default: false},
				{Key: "reasoning_effort", Label: "Reasoning effort", Type: "select", Default: "default", Options: []string{"default", "low", "medium", "high"}},
			},
			Formula: []string{"match tier price first", "regular_input = input - cache_read - cache_write", "cache storage = storage_price x storage_tokens x storage_hours"},
		},
		{Type: "image_unit_matrix", Name: "Image unit", ModelTypes: []string{"ImageGeneration"}, PricingUnits: []string{model.UnitPerImage}, Description: "Calculate by image count, resolution, quality, and edit mode.", AccuracyLevel: "compatible", IsActive: true, Version: "v2", Source: "builtin", Fields: []calculatorFieldSpec{{Key: "image_count", Label: "Images", Type: "number", Unit: "images", Default: 1}, {Key: "input_image_count", Label: "Input images", Type: "number", Unit: "images", Default: 0}, {Key: "resolution", Label: "Resolution", Type: "select", Default: "1024x1024", Options: []string{"512x512", "1024x1024", "2048x2048", "4096x4096"}}, {Key: "quality", Label: "Quality", Type: "select", Default: "standard", Options: []string{"standard", "hd", "pro"}}, {Key: "mode", Label: "Mode", Type: "select", Default: "generation", Options: []string{"generation", "edit", "variation"}}}, Formula: []string{"cost = image unit price * image count", "resolution/quality/mode select the provider matrix row when configured"}},
		{Type: "vision_image_unit", Name: "Vision image unit", ModelTypes: []string{"Vision"}, PricingUnits: []string{model.UnitPerImage}, Description: "Calculate visual understanding or OCR by processed image count.", AccuracyLevel: "stable", IsActive: true, Version: "v1", Source: "builtin", Fields: []calculatorFieldSpec{{Key: "image_count", Label: "Images", Type: "number", Unit: "images", Default: 1}, {Key: "resolution", Label: "Resolution", Type: "select", Default: "default", Options: []string{"default", "low", "high"}}}, Formula: []string{"cost = image unit price * image count"}},
		{Type: "volc_seedance_2_video_formula", Name: "Seedance 2.0 video", ModelTypes: []string{"VideoGeneration"}, PricingUnits: []string{model.UnitPerMillionTokens}, Description: "Estimate video tokens by input/output duration, resolution, fps, and input-video minimum usage.", AccuracyLevel: "needs_review", IsActive: true, Version: "v2", Source: "builtin", Fields: []calculatorFieldSpec{{Key: "input_video_seconds", Label: "Input seconds", Type: "number", Unit: "s", Default: 0}, {Key: "output_seconds", Label: "Output seconds", Type: "number", Unit: "s", Default: 5}, {Key: "resolution", Label: "Resolution", Type: "select", Default: "720p", Options: []string{"480p", "720p", "1080p"}}, {Key: "width", Label: "Width", Type: "number", Unit: "px", Default: 1280}, {Key: "height", Label: "Height", Type: "number", Unit: "px", Default: 720}, {Key: "fps", Label: "FPS", Type: "number", Unit: "fps", Default: 24}, {Key: "input_contains_video", Label: "Input contains video", Type: "boolean", Default: false}}, Formula: []string{"estimated tokens = (input_seconds + output_seconds) * width * height * fps / 1024", "input-video scenarios apply provider minimum tokens"}},
		{Type: "volc_seedance_1_5_video_matrix", Name: "Seedance 1.5 video", ModelTypes: []string{"VideoGeneration"}, PricingUnits: []string{model.UnitPerMillionTokens}, Description: "Video pricing matrix by duration, resolution, audio, draft, fps, and service tier.", AccuracyLevel: "compatible", IsActive: true, Version: "v3", Source: "builtin", Fields: []calculatorFieldSpec{{Key: "input_video_seconds", Label: "Input seconds", Type: "number", Unit: "s", Default: 0}, {Key: "output_seconds", Label: "Output seconds", Type: "number", Unit: "s", Default: 5}, {Key: "resolution", Label: "Resolution", Type: "select", Default: "720p", Options: []string{"480p", "720p", "1080p"}}, {Key: "fps", Label: "FPS", Type: "number", Unit: "fps", Default: 24}, {Key: "generate_audio", Label: "Audio", Type: "select", Default: "true", Options: []string{"true", "false"}}, {Key: "draft", Label: "Draft mode", Type: "boolean", Default: false}, {Key: "service_tier", Label: "Service tier", Type: "select", Default: "default", Options: []string{"default", "flex"}}}, Formula: []string{"unit_price = audio/default 16, silent/default 8, audio/flex 8, silent/flex 4", "estimated_tokens = (input_seconds + output_seconds) x width x height x fps / 1024", "draft 480p may apply provider coefficient"}},
		{Type: "video_token_formula", Name: "Video token", ModelTypes: []string{"VideoGeneration"}, PricingUnits: []string{model.UnitPerMillionTokens}, Description: "Generic video token formula.", AccuracyLevel: "compatible", IsActive: true, Version: "v2", Source: "builtin", Fields: []calculatorFieldSpec{{Key: "input_video_seconds", Label: "Input seconds", Type: "number", Unit: "s", Default: 0}, {Key: "output_seconds", Label: "Output seconds", Type: "number", Unit: "s", Default: 5}, {Key: "resolution", Label: "Resolution", Type: "select", Default: "720p", Options: []string{"480p", "720p", "1080p"}}, {Key: "fps", Label: "FPS", Type: "number", Unit: "fps", Default: 24}}, Formula: []string{"cost = token price * estimated tokens"}},
		{Type: "video_duration", Name: "Duration", ModelTypes: []string{"VideoGeneration", "ASR"}, PricingUnits: []string{model.UnitPerSecond, model.UnitPerMinute, model.UnitPerHour}, Description: "Calculate by media duration.", AccuracyLevel: "stable", IsActive: true, Version: "v1", Source: "builtin", Fields: []calculatorFieldSpec{{Key: "duration_seconds", Label: "Duration", Type: "number", Unit: "s", Default: 60}}, Formula: []string{"cost = duration * unit price"}},
		{Type: "tts_character", Name: "Characters", ModelTypes: []string{"TTS", "SpeechSynthesis"}, PricingUnits: []string{model.UnitPer10kCharacters, model.UnitPerMillionCharacters, model.UnitPerKChars}, Description: "Calculate by character count.", AccuracyLevel: "stable", IsActive: true, Version: "v1", Source: "builtin", Fields: []calculatorFieldSpec{{Key: "char_count", Label: "Characters", Type: "number", Unit: "chars", Default: 1000}}, Formula: []string{"cost = characters * unit price"}},
		{Type: "embedding_token", Name: "Embedding token", ModelTypes: []string{"Embedding"}, PricingUnits: []string{model.UnitPerMillionTokens}, Description: "Calculate embedding input tokens.", AccuracyLevel: "stable", IsActive: true, Version: "v1", Source: "builtin", Fields: []calculatorFieldSpec{{Key: "input_tokens", Label: "Input tokens", Type: "number", Unit: "tokens", Default: 1000}}, Formula: []string{"cost = input price / 1,000,000 * input tokens"}},
		{Type: "asr_duration", Name: "ASR duration", ModelTypes: []string{"ASR", "SpeechRecognition"}, PricingUnits: []string{model.UnitPerSecond, model.UnitPerMinute, model.UnitPerHour}, Description: "Calculate speech recognition by duration.", AccuracyLevel: "stable", IsActive: true, Version: "v1", Source: "builtin", Fields: []calculatorFieldSpec{{Key: "duration_seconds", Label: "Duration", Type: "number", Unit: "s", Default: 60}}, Formula: []string{"cost = duration * unit price"}},
		{Type: "rerank_token", Name: "Rerank token", ModelTypes: []string{"Rerank"}, PricingUnits: []string{model.UnitPerMillionTokens}, Description: "Calculate rerank query/document tokens.", AccuracyLevel: "stable", IsActive: true, Version: "v1", Source: "builtin", Fields: []calculatorFieldSpec{{Key: "input_tokens", Label: "Tokens", Type: "number", Unit: "tokens", Default: 1000}}, Formula: []string{"cost = input price / 1,000,000 * tokens"}},
		{Type: "rerank_call", Name: "Rerank call", ModelTypes: []string{"Rerank"}, PricingUnits: []string{model.UnitPerCall}, Description: "Calculate rerank by call count.", AccuracyLevel: "stable", IsActive: true, Version: "v1", Source: "builtin", Fields: []calculatorFieldSpec{{Key: "call_count", Label: "Calls", Type: "number", Unit: "calls", Default: 1}}, Formula: []string{"cost = call price * call count"}},
		{Type: "three_d_generation_token", Name: "3D token", ModelTypes: []string{"3DGeneration"}, PricingUnits: []string{model.UnitPerMillionTokens}, Description: "Calculate 3D generation token usage.", AccuracyLevel: "compatible", IsActive: true, Version: "v1", Source: "builtin", Fields: []calculatorFieldSpec{{Key: "input_tokens", Label: "Input tokens", Type: "number", Unit: "tokens", Default: 1000}, {Key: "output_tokens", Label: "Output tokens", Type: "number", Unit: "tokens", Default: 1000}}, Formula: []string{"cost = token price * usage"}},
		{Type: "generic_per_image", Name: "Generic image unit", ModelTypes: []string{"Custom"}, PricingUnits: []string{model.UnitPerImage}, Description: "Fallback calculator for image-count billing.", AccuracyLevel: "compatible", IsActive: true, Version: "v1", Source: "builtin", Fields: []calculatorFieldSpec{{Key: "image_count", Label: "Images", Type: "number", Unit: "images", Default: 1}}, Formula: []string{"amount = unit_price * image_count"}},
		{Type: "generic_per_second", Name: "Generic second unit", ModelTypes: []string{"Custom"}, PricingUnits: []string{model.UnitPerSecond}, Description: "Fallback calculator for per-second billing.", AccuracyLevel: "compatible", IsActive: true, Version: "v1", Source: "builtin", Fields: []calculatorFieldSpec{{Key: "duration_seconds", Label: "Duration", Type: "number", Unit: "s", Default: 60}}, Formula: []string{"amount = unit_price * duration_seconds"}},
		{Type: "generic_per_minute", Name: "Generic minute unit", ModelTypes: []string{"Custom"}, PricingUnits: []string{model.UnitPerMinute}, Description: "Fallback calculator for per-minute billing.", AccuracyLevel: "compatible", IsActive: true, Version: "v1", Source: "builtin", Fields: []calculatorFieldSpec{{Key: "duration_seconds", Label: "Duration", Type: "number", Unit: "s", Default: 60}}, Formula: []string{"amount = unit_price * duration_seconds / 60"}},
		{Type: "generic_per_hour", Name: "Generic hour unit", ModelTypes: []string{"Custom"}, PricingUnits: []string{model.UnitPerHour}, Description: "Fallback calculator for per-hour billing.", AccuracyLevel: "compatible", IsActive: true, Version: "v1", Source: "builtin", Fields: []calculatorFieldSpec{{Key: "duration_seconds", Label: "Duration", Type: "number", Unit: "s", Default: 3600}}, Formula: []string{"amount = unit_price * duration_seconds / 3600"}},
		{Type: "generic_per_call", Name: "Generic call unit", ModelTypes: []string{"Custom"}, PricingUnits: []string{model.UnitPerCall}, Description: "Fallback calculator for per-call billing.", AccuracyLevel: "compatible", IsActive: true, Version: "v1", Source: "builtin", Fields: []calculatorFieldSpec{{Key: "call_count", Label: "Calls", Type: "number", Unit: "calls", Default: 1}}, Formula: []string{"amount = unit_price * call_count"}},
		{Type: "generic_per_10k_characters", Name: "Generic 10k characters", ModelTypes: []string{"Custom"}, PricingUnits: []string{model.UnitPer10kCharacters, model.UnitPerKChars}, Description: "Fallback calculator for 10k-character billing.", AccuracyLevel: "compatible", IsActive: true, Version: "v1", Source: "builtin", Fields: []calculatorFieldSpec{{Key: "char_count", Label: "Characters", Type: "number", Unit: "chars", Default: 1000}}, Formula: []string{"amount = unit_price * char_count / 10000"}},
		{Type: "generic_per_million_characters", Name: "Generic million characters", ModelTypes: []string{"Custom"}, PricingUnits: []string{model.UnitPerMillionCharacters}, Description: "Fallback calculator for million-character billing.", AccuracyLevel: "compatible", IsActive: true, Version: "v1", Source: "builtin", Fields: []calculatorFieldSpec{{Key: "char_count", Label: "Characters", Type: "number", Unit: "chars", Default: 1000}}, Formula: []string{"amount = unit_price * char_count / 1000000"}},
	}
}
func (h *ModelOpsHandler) loadCalculatorSpecs(includeInactive bool) ([]calculatorSpec, error) {
	if err := h.ensureCalculatorConfigs(); err != nil {
		return nil, err
	}
	var configs []model.PriceCalculatorConfig
	q := h.db.Order("id ASC")
	if !includeInactive {
		q = q.Where("is_active = ?", true)
	}
	if err := q.Find(&configs).Error; err != nil {
		return nil, err
	}
	if len(configs) == 0 {
		return calculatorCatalog(), nil
	}
	out := make([]calculatorSpec, 0, len(configs))
	for _, cfg := range configs {
		out = append(out, configToCalculatorSpec(cfg))
	}
	return out, nil
}

func (h *ModelOpsHandler) ensureCalculatorConfigs() error {
	h.calculatorSeedOnce.Do(func() {
		var count int64
		if err := h.db.Model(&model.PriceCalculatorConfig{}).Count(&count).Error; err != nil {
			h.calculatorSeedErr = err
			return
		}
		if count > 0 {
			return
		}
		for _, spec := range calculatorCatalog() {
			cfg := calculatorSpecToConfig(spec)
			if err := h.db.Create(&cfg).Error; err != nil {
				h.calculatorSeedErr = err
				return
			}
		}
	})
	return h.calculatorSeedErr
}

func calculatorSpecToConfig(spec calculatorSpec) model.PriceCalculatorConfig {
	spec = normalizeCalculatorSpec(spec)
	return model.PriceCalculatorConfig{
		Code: spec.Type, Name: spec.Name, Description: spec.Description,
		ModelTypes: mustJSON(spec.ModelTypes), PricingUnits: mustJSON(spec.PricingUnits),
		FieldsSchema: mustJSON(spec.Fields), Formula: mustJSON(spec.Formula),
		AccuracyLevel: spec.AccuracyLevel, CompatibilityTip: spec.CompatibilityTip,
		IsActive: spec.IsActive, Version: defaultString(spec.Version, "v1"), Source: defaultString(spec.Source, "builtin"),
	}
}

func configToCalculatorSpec(cfg model.PriceCalculatorConfig) calculatorSpec {
	return normalizeCalculatorSpec(calculatorSpec{
		Type: cfg.Code, Name: cfg.Name, Description: cfg.Description,
		ModelTypes: jsonStringArray(cfg.ModelTypes), PricingUnits: jsonStringArray(cfg.PricingUnits),
		Fields: jsonCalculatorFields(cfg.FieldsSchema), Formula: jsonStringArray(cfg.Formula),
		AccuracyLevel: cfg.AccuracyLevel, CompatibilityTip: cfg.CompatibilityTip,
		IsActive: cfg.IsActive, Version: cfg.Version, Source: cfg.Source,
	})
}

func mustJSON(v interface{}) model.JSON {
	b, err := json.Marshal(v)
	if err != nil {
		return model.JSON("[]")
	}
	return model.JSON(b)
}

func jsonStringArray(raw model.JSON) []string {
	if len(raw) == 0 {
		return nil
	}
	var out []string
	_ = json.Unmarshal(raw, &out)
	return out
}

func jsonCalculatorFields(raw model.JSON) []calculatorFieldSpec {
	if len(raw) == 0 {
		return nil
	}
	var out []calculatorFieldSpec
	_ = json.Unmarshal(raw, &out)
	return out
}

func defaultString(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

func valueOrDefault(value *string, fallback string) string {
	if value == nil {
		return fallback
	}
	return defaultString(*value, fallback)
}

func normalizeCalculatorCode(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return ""
	}
	var b strings.Builder
	lastUnderscore := false
	for _, r := range value {
		ok := (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')
		if ok {
			b.WriteRune(r)
			lastUnderscore = false
			continue
		}
		if (r == '_' || r == '-' || r == ' ') && !lastUnderscore {
			b.WriteByte('_')
			lastUnderscore = true
		}
	}
	return strings.Trim(b.String(), "_")
}

func normalizeCalculatorSpec(spec calculatorSpec) calculatorSpec {
	spec.Type = normalizeCalculatorCode(spec.Type)
	spec.Name = strings.TrimSpace(spec.Name)
	spec.Description = strings.TrimSpace(spec.Description)
	spec.AccuracyLevel = defaultString(spec.AccuracyLevel, "stable")
	spec.CompatibilityTip = defaultString(spec.CompatibilityTip, defaultCalculatorCompatibilityTip(spec))
	spec.Version = defaultString(spec.Version, "v1")
	spec.Source = defaultString(spec.Source, "builtin")
	if len(spec.ModelTypes) == 0 {
		spec.ModelTypes = []string{"LLM"}
	}
	if len(spec.PricingUnits) == 0 {
		spec.PricingUnits = []string{model.UnitPerMillionTokens}
	}
	spec.Fields = mergeCalculatorFields(ensureCalculatorEvolutionFields(spec.Type, spec.Fields), providerPricingFieldSpecs())
	for i := range spec.Fields {
		spec.Fields[i] = normalizeCalculatorField(spec.Type, spec.Fields[i])
	}
	if len(spec.Formula) == 0 {
		spec.Formula = []string{"total = sum(line_items)"}
	}
	return spec
}

func providerPricingFieldSpecs() []calculatorFieldSpec {
	return []calculatorFieldSpec{
		{Key: "processing_mode", Label: "Processing mode", Type: "select", Default: "standard", Options: []string{"standard", "batch", "flex", "priority", "offline"}},
		{Key: "service_tier", Label: "Service tier", Type: "select", Default: "default", Options: []string{"default", "standard", "batch", "flex", "priority", "offline"}},
		{Key: "region_mode", Label: "Region mode", Type: "select", Default: "default", Options: []string{"default", "global", "regional", "china", "us", "eu"}},
		{Key: "batch", Label: "Batch pricing", Type: "boolean", Default: false},
		{Key: "cache_ttl", Label: "Cache TTL", Type: "select", Default: "5m", Options: []string{"5m", "1h", "24h"}},
		{Key: "cache_control", Label: "Cache control", Type: "select", Default: "auto", Options: []string{"auto", "implicit", "explicit", "none"}},
		{Key: "cache_mode", Label: "Cache mode", Type: "select", Default: "auto", Options: []string{"auto", "implicit", "explicit", "context_api", "none"}},
		{Key: "cached_tokens", Label: "Cached tokens", Type: "number", Unit: "tokens", Default: 0},
		{Key: "cache_hit_tokens", Label: "Cache hit tokens", Type: "number", Unit: "tokens", Default: 0},
		{Key: "cache_miss_tokens", Label: "Cache miss tokens", Type: "number", Unit: "tokens", Default: 0},
		{Key: "thinking_budget", Label: "Thinking budget", Type: "number", Unit: "tokens", Default: 0},
		{Key: "input_text_tokens", Label: "Input text tokens", Type: "number", Unit: "tokens", Default: 0},
		{Key: "input_image_tokens", Label: "Input image tokens", Type: "number", Unit: "tokens", Default: 0},
		{Key: "input_video_tokens", Label: "Input video tokens", Type: "number", Unit: "tokens", Default: 0},
		{Key: "input_audio_tokens", Label: "Input audio tokens", Type: "number", Unit: "tokens", Default: 0},
		{Key: "input_image_count", Label: "Input images", Type: "number", Unit: "images", Default: 0},
		{Key: "width", Label: "Width", Type: "number", Unit: "px", Default: 1280},
		{Key: "height", Label: "Height", Type: "number", Unit: "px", Default: 720},
		{Key: "size", Label: "Size", Type: "select", Default: "1024x1024", Options: []string{"512x512", "1024x1024", "1024x1792", "1792x1024", "2048x2048"}},
		{Key: "aspect_ratio", Label: "Aspect ratio", Type: "select", Default: "16:9", Options: []string{"1:1", "4:3", "3:4", "16:9", "9:16", "21:9"}},
		{Key: "output_format", Label: "Output format", Type: "select", Default: "png", Options: []string{"png", "jpeg", "webp", "mp4", "wav", "mp3"}},
		{Key: "background", Label: "Background", Type: "select", Default: "auto", Options: []string{"auto", "transparent", "opaque"}},
		{Key: "style", Label: "Style", Type: "select", Default: "default", Options: []string{"default", "natural", "vivid", "realistic", "anime"}},
		{Key: "output_count", Label: "Output count", Type: "number", Unit: "items", Default: 1},
		{Key: "video_count", Label: "Video count", Type: "number", Unit: "videos", Default: 1},
		{Key: "audio_mode", Label: "Audio mode", Type: "select", Default: "auto", Options: []string{"auto", "silent", "with_audio", "voiceover"}},
		{Key: "voice_tier", Label: "Voice tier", Type: "select", Default: "standard", Options: []string{"standard", "hd", "turbo", "premium"}},
		{Key: "voice_id", Label: "Voice ID", Type: "string", Default: ""},
		{Key: "voice_design_count", Label: "Voice design count", Type: "number", Unit: "voices", Default: 0},
		{Key: "voice_clone_count", Label: "Voice clone count", Type: "number", Unit: "voices", Default: 0},
		{Key: "text_char_count", Label: "Text characters", Type: "number", Unit: "chars", Default: 0},
		{Key: "sample_rate", Label: "Sample rate", Type: "number", Unit: "Hz", Default: 24000},
		{Key: "format", Label: "Format", Type: "select", Default: "mp3", Options: []string{"mp3", "wav", "opus", "aac", "pcm"}},
		{Key: "speed", Label: "Speed", Type: "number", Unit: "x", Default: 1},
		{Key: "pitch", Label: "Pitch", Type: "number", Unit: "semitones", Default: 0},
		{Key: "volume", Label: "Volume", Type: "number", Unit: "%", Default: 100},
		{Key: "streaming", Label: "Streaming", Type: "boolean", Default: false},
		{Key: "realtime_session_minutes", Label: "Realtime session minutes", Type: "number", Unit: "minutes", Default: 0},
		{Key: "web_search_count", Label: "Web search count", Type: "number", Unit: "calls", Default: 0},
		{Key: "search_tokens", Label: "Search tokens", Type: "number", Unit: "tokens", Default: 0},
		{Key: "google_search_query_count", Label: "Google search queries", Type: "number", Unit: "queries", Default: 0},
		{Key: "maps_query_count", Label: "Maps queries", Type: "number", Unit: "queries", Default: 0},
		{Key: "tool_call_count", Label: "Tool call count", Type: "number", Unit: "calls", Default: 0},
		{Key: "file_search_count", Label: "File search count", Type: "number", Unit: "calls", Default: 0},
		{Key: "container_seconds", Label: "Container seconds", Type: "number", Unit: "seconds", Default: 0},
		{Key: "container_minutes", Label: "Container minutes", Type: "number", Unit: "minutes", Default: 0},
		{Key: "retrieval_count", Label: "Retrieval count", Type: "number", Unit: "calls", Default: 0},
		{Key: "knowledge_search_count", Label: "Knowledge search count", Type: "number", Unit: "calls", Default: 0},
		{Key: "page_count", Label: "Page count", Type: "number", Unit: "pages", Default: 1},
		{Key: "document_count", Label: "Document count", Type: "number", Unit: "documents", Default: 0},
		{Key: "storage_gb", Label: "Storage GB", Type: "number", Unit: "GB", Default: 0},
		{Key: "storage_hours", Label: "Storage hours", Type: "number", Unit: "hours", Default: 0},
		{Key: "response_format", Label: "Response format", Type: "select", Default: "text", Options: []string{"text", "json_object", "json_schema"}},
		{Key: "tools", Label: "Tools", Type: "json", Default: "[]"},
		{Key: "tool_choice", Label: "Tool choice", Type: "select", Default: "auto", Options: []string{"auto", "none", "required"}},
	}
}

func mergeCalculatorFields(fields []calculatorFieldSpec, extras []calculatorFieldSpec) []calculatorFieldSpec {
	seen := map[string]bool{}
	out := make([]calculatorFieldSpec, 0, len(fields)+len(extras))
	for _, field := range fields {
		key := strings.TrimSpace(field.Key)
		if key == "" || seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, field)
	}
	for _, field := range extras {
		key := strings.TrimSpace(field.Key)
		if key == "" || seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, field)
	}
	return out
}

func ensureCalculatorEvolutionFields(calculatorType string, fields []calculatorFieldSpec) []calculatorFieldSpec {
	if calculatorType != "token_io_tiered_cache" {
		return fields
	}
	for _, field := range fields {
		if strings.TrimSpace(field.Key) == "cache_write_1h_tokens" {
			return fields
		}
	}
	return append(fields, calculatorFieldSpec{
		Key:     "cache_write_1h_tokens",
		Label:   "1h 缓存写 tokens",
		Type:    "number",
		Unit:    "tokens",
		Default: 0,
		Help:    "Subset of cache_write_tokens charged as 1-hour cache writes at 2x input price.",
	})
}

func normalizeCalculatorField(calculatorType string, field calculatorFieldSpec) calculatorFieldSpec {
	field.Key = strings.TrimSpace(field.Key)
	field.Label = defaultString(field.Label, field.Key)
	field.Type = defaultString(field.Type, "number")
	if field.Type == "number" {
		field.Unit = defaultString(field.Unit, defaultCalculatorFieldUnit(field.Key))
	}
	if field.Type == "select" && len(field.Options) == 0 {
		field.Options = defaultCalculatorFieldOptions(field.Key)
	}
	field.Help = defaultString(field.Help, defaultCalculatorFieldHelp(calculatorType, field))
	return field
}

func defaultCalculatorCompatibilityTip(spec calculatorSpec) string {
	switch spec.AccuracyLevel {
	case "needs_review":
		return "This calculator depends on provider usage or dedicated parameters; verify with real request logs before saving."
	case "compatible":
		return "This calculator covers common billing paths; verify provider matrix, tier, or minimum usage prices when needed."
	default:
		return "This calculator uses a clear linear usage-times-unit-price formula."
	}
}

func defaultCalculatorFieldUnit(key string) string {
	switch key {
	case "input_tokens", "prompt_tokens", "output_tokens", "completion_tokens", "total_tokens", "thinking_tokens", "reasoning_tokens", "thinking_budget", "cache_read_tokens", "cached_tokens", "cache_hit_tokens", "cache_miss_tokens", "cache_write_tokens", "cache_write_1h_tokens", "cache_storage_tokens", "input_text_tokens", "input_image_tokens", "input_video_tokens", "input_audio_tokens", "search_tokens":
		return "tokens"
	case "image_count", "input_image_count":
		return "images"
	case "video_count":
		return "videos"
	case "duration_seconds", "input_video_seconds", "output_seconds", "container_seconds":
		return "seconds"
	case "cache_storage_hours", "storage_hours":
		return "hours"
	case "width", "height":
		return "px"
	case "fps":
		return "fps"
	case "char_count", "character_count", "text_char_count":
		return "chars"
	case "call_count", "web_search_count", "google_search_query_count", "maps_query_count", "tool_call_count", "file_search_count", "retrieval_count", "knowledge_search_count":
		return "calls"
	case "container_minutes", "realtime_session_minutes":
		return "minutes"
	case "voice_design_count", "voice_clone_count":
		return "voices"
	case "page_count":
		return "pages"
	case "document_count":
		return "documents"
	case "storage_gb":
		return "GB"
	case "sample_rate":
		return "Hz"
	case "speed":
		return "x"
	case "pitch":
		return "semitones"
	case "volume":
		return "%"
	default:
		return "unit"
	}
}

func defaultCalculatorFieldOptions(key string) []string {
	switch key {
	case "resolution":
		return []string{"480p", "720p", "1080p"}
	case "quality":
		return []string{"standard", "hd", "pro", "low", "medium", "high", "auto"}
	case "mode":
		return []string{"generation", "edit", "variation", "text-to-video", "image-to-video", "video-to-video"}
	case "service_tier":
		return []string{"default", "standard", "batch", "flex", "priority", "offline"}
	case "processing_mode":
		return []string{"standard", "batch", "flex", "priority", "offline"}
	case "region_mode":
		return []string{"default", "global", "regional", "china", "us", "eu"}
	case "cache_control":
		return []string{"auto", "implicit", "explicit", "none"}
	case "cache_mode":
		return []string{"auto", "implicit", "explicit", "context_api", "none"}
	case "reasoning_effort":
		return []string{"default", "low", "medium", "high"}
	case "cache_ttl":
		return []string{"5m", "1h", "24h"}
	case "size":
		return []string{"512x512", "1024x1024", "1024x1792", "1792x1024", "2048x2048"}
	case "aspect_ratio":
		return []string{"1:1", "4:3", "3:4", "16:9", "9:16", "21:9"}
	case "output_format":
		return []string{"png", "jpeg", "webp", "mp4", "wav", "mp3"}
	case "background":
		return []string{"auto", "transparent", "opaque"}
	case "style":
		return []string{"default", "natural", "vivid", "realistic", "anime"}
	case "audio_mode":
		return []string{"auto", "silent", "with_audio", "voiceover"}
	case "voice_tier":
		return []string{"standard", "hd", "turbo", "premium"}
	case "format":
		return []string{"mp3", "wav", "opus", "aac", "pcm"}
	case "response_format":
		return []string{"text", "json_object", "json_schema"}
	case "tool_choice":
		return []string{"auto", "none", "required"}
	default:
		return []string{"default"}
	}
}

func defaultCalculatorFieldHelp(calculatorType string, field calculatorFieldSpec) string {
	switch field.Key {
	case "input_tokens":
		return "Request input token count."
	case "output_tokens":
		return "Visible output token count returned to the user."
	case "thinking_tokens", "reasoning_tokens":
		return "Internal reasoning tokens when the provider reports and bills them separately."
	case "cache_read_tokens":
		return "Input tokens charged at cache-read price."
	case "cache_write_tokens":
		return "Input tokens charged at cache-write price."
	case "cache_write_1h_tokens":
		return "Subset of cache_write_tokens charged as 1-hour cache writes at 2x input price."
	case "cache_storage_tokens":
		return "Cached tokens kept in provider context-cache storage."
	case "cache_storage_hours":
		return "Context-cache storage duration in hours."
	case "cache_ttl":
		return "Context-cache TTL bucket, such as 5 minutes or 1 hour."
	case "cache_control":
		return "Cache strategy requested for providers that distinguish implicit, explicit, or disabled caching."
	case "cache_mode":
		return "Provider cache mode, such as automatic cache, explicit Context API, or no cache."
	case "cached_tokens", "cache_hit_tokens":
		return "Tokens reported as cache hits by the provider."
	case "cache_miss_tokens":
		return "Tokens reported as cache misses by the provider."
	case "thinking_budget":
		return "Maximum thinking token budget requested for models that expose an explicit reasoning budget."
	case "input_text_tokens":
		return "Text modality input tokens for providers that price modalities separately."
	case "input_image_tokens":
		return "Image modality input tokens for providers that price multimodal input separately."
	case "input_video_tokens":
		return "Video modality input tokens for providers that price multimodal input separately."
	case "input_audio_tokens":
		return "Audio modality input tokens for providers that price multimodal input separately."
	case "image_count":
		return "Number of generated, edited, or recognized images."
	case "input_image_count":
		return "Number of input images used by edit, variation, or multimodal requests."
	case "video_count":
		return "Number of generated videos."
	case "resolution":
		return "Provider pricing resolution bucket."
	case "quality":
		return "Image quality or model tier bucket."
	case "input_contains_video":
		return "Whether the request includes video input for formulas with video-input minimum usage."
	case "input_video_seconds":
		return "Input video duration in seconds."
	case "output_seconds", "duration_seconds":
		return "Media duration in seconds."
	case "width":
		return "Output video width."
	case "height":
		return "Output video height."
	case "size":
		return "Image or media size bucket used by provider price matrices."
	case "aspect_ratio":
		return "Generated media aspect ratio bucket."
	case "output_format":
		return "Requested output format when providers price or restrict formats differently."
	case "background":
		return "Image background mode, such as transparent or opaque."
	case "style":
		return "Image style bucket when providers expose style-specific pricing or routing."
	case "fps":
		return "Output video frame rate."
	case "has_audio":
		return "Whether the video pricing matrix includes audio."
	case "audio_mode":
		return "Video or voice audio mode used by provider pricing matrices."
	case "draft":
		return "Whether draft-mode pricing applies."
	case "mode":
		return "Generation, edit, or variation pricing mode."
	case "service_tier":
		return "Provider service tier, such as online/default or flex/batch."
	case "processing_mode":
		return "Price simulation mode for standard, batch, flex, priority, or offline processing."
	case "region_mode":
		return "Region or data-processing mode when providers expose regional price differences."
	case "batch":
		return "Whether batch pricing should be simulated."
	case "reasoning_effort":
		return "Reasoning effort bucket used by providers that expose low/medium/high thinking modes."
	case "char_count", "character_count", "text_char_count":
		return "Character count for TTS or text processing."
	case "call_count":
		return "API call count."
	case "voice_tier":
		return "Voice quality tier when voice synthesis prices differ by tier."
	case "voice_id":
		return "Provider voice identifier for routing, cloning, or premium voice pricing."
	case "voice_design_count":
		return "Paid voice design count for providers that charge custom voice design."
	case "voice_clone_count":
		return "Paid voice clone count for providers that charge custom voice cloning."
	case "sample_rate":
		return "Audio sample rate used by providers that price or restrict output quality."
	case "format":
		return "Audio or media output format."
	case "speed":
		return "Speech speed multiplier."
	case "pitch":
		return "Speech pitch adjustment."
	case "volume":
		return "Speech volume adjustment."
	case "streaming":
		return "Whether streaming or realtime mode is enabled."
	case "realtime_session_minutes":
		return "Realtime session duration in minutes."
	case "web_search_count":
		return "Paid web-search calls triggered by the model."
	case "search_tokens":
		return "Search result tokens later billed as model input."
	case "google_search_query_count":
		return "Google Search grounding query count."
	case "maps_query_count":
		return "Maps grounding query count."
	case "tool_call_count":
		return "Generic paid tool-call count."
	case "file_search_count":
		return "Paid file-search call count."
	case "container_seconds":
		return "Code interpreter or hosted container runtime in seconds."
	case "container_minutes":
		return "Code interpreter or hosted container runtime in minutes."
	case "retrieval_count":
		return "Knowledge-base retrieval call count."
	case "knowledge_search_count":
		return "Knowledge-base search call count."
	case "page_count":
		return "OCR or document parsing page count."
	case "document_count":
		return "Document count for parsing or knowledge-base ingestion."
	case "storage_gb":
		return "Storage size in GB for file search, vector stores, cache, or knowledge bases."
	case "storage_hours":
		return "Storage duration in hours."
	case "response_format":
		return "Response format requested for debugging and provider compatibility."
	case "tools":
		return "Tool definition payload used to simulate provider-side tool pricing."
	case "tool_choice":
		return "Tool choice mode used by OpenAI-compatible tool calls."
	default:
		return fmt.Sprintf("%s parameter for %s calculator", defaultString(field.Label, field.Key), defaultString(calculatorType, "current"))
	}
}

func calculateModelPreview(item model.AIModel, params map[string]interface{}) calculatePreviewResponse {
	calculatorType, calculatorStatus, _, notes := classifyCalculator(item)
	price := buildPriceSummary(item)
	if params == nil {
		params = map[string]interface{}{}
	}

	layers := make([]priceLayer, 0, 8)
	steps := make([]calculationStep, 0, 18)
	tierMatches := make([]tierMatchSnapshot, 0, 2)
	formula := make([]string, 0, 8)
	warnings := append([]string{}, notes...)
	specialParams := collectSpecialPreviewParams(params)

	addLayer := func(name string, unitPrice, quantity float64, desc string) {
		if quantity <= 0 || unitPrice <= 0 {
			return
		}
		layers = append(layers, priceLayer{Name: name, UnitPrice: unitPrice, Quantity: quantity, Amount: unitPrice * quantity, Description: desc})
	}
	addStep := func(section, label string, unitPrice, quantity float64, desc string) {
		if quantity <= 0 || unitPrice <= 0 {
			return
		}
		amount := unitPrice * quantity
		steps = append(steps, calculationStep{
			Section:     section,
			Label:       label,
			UnitPrice:   unitPrice,
			Quantity:    quantity,
			Amount:      amount,
			Formula:     fmt.Sprintf("%s = %.6f / 1,000,000 x %.0f tokens = %.6f", label, unitPrice, quantity*1_000_000, amount),
			Description: desc,
		})
	}
	addUnitSteps := func(label string, officialUnit, sellingUnit, quantity float64, desc string) {
		addLayer("official_"+label, officialUnit, quantity, desc)
		addStep("official", "official_"+label, officialUnit, quantity, desc)
		discount := supplierDiscountForPreview(price)
		addStep("effective", "effective_"+label, officialUnit*discount, quantity, fmt.Sprintf("supplier discount %.4f", discount))
		if sellingUnit <= 0 {
			sellingUnit = officialUnit
		}
		addStep("selling", "selling_"+label, sellingUnit, quantity, "platform selling price")
	}

	switch calculatorType {
	case "volc_seedance_2_video_formula", "video_token_formula":
		inputSeconds := numParam(params, "input_video_seconds", 0)
		seconds := numParam(params, "output_seconds", 5)
		defaultWidth, defaultHeight := seedanceResolutionSize(strParam(params, "resolution", "720p"))
		width := numParam(params, "width", defaultWidth)
		height := numParam(params, "height", defaultHeight)
		fps := numParam(params, "fps", 24)
		estimatedTokens := (inputSeconds + seconds) * width * height * fps / 1024
		if calculatorType == "volc_seedance_2_video_formula" {
			minTokens := seedanceMinTokens(strParam(params, "resolution", "720p"), boolParam(params, "input_contains_video", false))
			if estimatedTokens < minTokens {
				warnings = append(warnings, "estimated tokens below provider minimum; minimum usage applied")
				estimatedTokens = minTokens
			}
		}
		qty := estimatedTokens / 1000000
		unit := firstPositive(item.OutputCostRMB, item.InputCostRMB)
		sellUnit := firstPositive(price.SellingOutput, price.SellingInput, unit)
		addUnitSteps("video_output", unit, sellUnit, qty, "video token formula")
		formula = append(formula, "estimated_tokens = (input_seconds + output_seconds) x width x height x fps / 1024", "amount = unit_price / 1,000,000 x estimated_tokens")
	case "volc_seedance_1_5_video_matrix":
		inputSeconds := numParam(params, "input_video_seconds", 0)
		seconds := numParam(params, "output_seconds", 5)
		width, height := seedanceResolutionSize(strParam(params, "resolution", "720p"))
		fps := numParam(params, "fps", 24)
		estimatedTokens := (inputSeconds + seconds) * width * height * fps / 1024
		generateAudio := boolParamFlexible(params, "generate_audio", true)
		serviceTier := strParam(params, "service_tier", "default")
		if boolParamFlexible(params, "draft", false) && strings.Contains(strings.ToLower(strParam(params, "resolution", "720p")), "480") {
			if generateAudio {
				estimatedTokens *= 0.6
			} else {
				estimatedTokens *= 0.7
			}
			warnings = append(warnings, "draft coefficient applied for 480p Seedance preview")
		}
		unit := seedance15UnitPrice(generateAudio, serviceTier)
		qty := estimatedTokens / 1000000
		sellUnit := unit
		if price.SellingOutput > 0 && price.OfficialOutput > 0 {
			sellUnit = price.SellingOutput * unit / price.OfficialOutput
		}
		addUnitSteps("seedance_1_5", unit, sellUnit, qty, seedance15PriceLabel(generateAudio, serviceTier))
		formula = append(formula, "unit_price = Seedance 1.5 audio/silent and default/flex matrix", "estimated_tokens = (input_seconds + output_seconds) x width x height x fps / 1024", "amount = unit_price / 1,000,000 x estimated_tokens")
	case "image_unit_matrix", "vision_image_unit":
		count := numParamAny(params, []string{"image_count", "output_count"}, 1)
		unit := firstPositive(item.OutputCostRMB, item.InputCostRMB)
		sellUnit := firstPositive(price.SellingOutput, price.SellingInput, unit)
		addUnitSteps("image", unit, sellUnit, count, "image count")
		formula = append(formula, "image_amount = image_unit_price x image_count")
	case "video_duration", "asr_duration":
		seconds := numParam(params, "duration_seconds", 60)
		qty := durationQuantity(item.PricingUnit, seconds)
		unit := firstPositive(item.OutputCostRMB, item.InputCostRMB)
		sellUnit := firstPositive(price.SellingOutput, price.SellingInput, unit)
		addUnitSteps("duration", unit, sellUnit, qty, item.PricingUnit)
		formula = append(formula, "duration_amount = duration_unit_price x normalized_duration")
	case "tts_character":
		chars := numParamAny(params, []string{"char_count", "text_char_count", "character_count"}, 1000)
		qty := chars / characterDivisor(item.PricingUnit)
		unit := firstPositive(item.InputCostRMB, item.OutputCostRMB)
		sellUnit := firstPositive(price.SellingInput, price.SellingOutput, unit)
		addUnitSteps("characters", unit, sellUnit, qty, item.PricingUnit)
		formula = append(formula, "character_amount = character_unit_price x characters / unit_characters")
	case "rerank_call":
		count := numParam(params, "call_count", 1)
		unit := firstPositive(item.InputCostRMB, item.OutputCostRMB)
		sellUnit := firstPositive(price.SellingInput, price.SellingOutput, unit)
		addUnitSteps("call", unit, sellUnit, count, "call count")
		formula = append(formula, "amount = call_unit_price x call_count")
	default:
		inputTokens := normalizedInputTokens(params, 1000)
		outputTokens := numParamAny(params, []string{"output_tokens", "completion_tokens"}, 500)
		thinkingTokens := numParamAny(params, []string{"thinking_tokens", "reasoning_tokens"}, 0)
		cacheReadTokens := numParamAny(params, []string{"cache_read_tokens", "cached_tokens", "cache_hit_tokens"}, 0)
		cacheWriteTokens := numParam(params, "cache_write_tokens", 0)
		cacheWrite1hTokens := numParam(params, "cache_write_1h_tokens", 0)
		cacheWriteIgnored := false
		if shouldIgnorePreviewCacheWrite(item) && (cacheWriteTokens > 0 || cacheWrite1hTokens > 0) {
			warnings = append(warnings, "cache_write_tokens ignored: Gemini context caching bills cached reads and storage, not a separate cache write line item")
			cacheWriteTokens = 0
			cacheWrite1hTokens = 0
			cacheWriteIgnored = true
		}
		if cacheWrite1hTokens > cacheWriteTokens {
			warnings = append(warnings, "cache_write_1h_tokens exceeds cache_write_tokens; 1h tokens were clamped")
			cacheWrite1hTokens = cacheWriteTokens
		}
		cacheWrite5mTokens := math.Max(cacheWriteTokens-cacheWrite1hTokens, 0)
		cacheStorageTokens := numParam(params, "cache_storage_tokens", 0)
		cacheStorageHours := numParam(params, "cache_storage_hours", 0)
		if cacheReadTokens+cacheWriteTokens > inputTokens {
			warnings = append(warnings, "cache_read_tokens + cache_write_tokens exceeds input_tokens; regular input was clamped to 0")
		}
		regularInputTokens := math.Max(inputTokens-cacheReadTokens-cacheWriteTokens, 0)

		costTier, costTierIdx, costTierSource := selectPreviewTier(item.PriceTiers, inputTokens, outputTokens, "supplier_cost_tiers")
		var sellTier *model.PriceTier
		sellTierIdx := -1
		sellTierSource := "platform_price_tiers"
		if item.Pricing != nil {
			sellTier, sellTierIdx, sellTierSource = selectPreviewTier(item.Pricing.PriceTiers, inputTokens, outputTokens, "platform_price_tiers")
		}

		costInput := firstPositive(tierValue(costTier, "input"), item.InputCostRMB)
		costOutput := firstPositive(tierValue(costTier, "output"), item.OutputCostRMB)
		costThinking := firstPositive(tierValue(costTier, "thinking"), item.OutputCostThinkingRMB, costOutput)
		costCacheRead := cacheReadPreviewPrice(item, costInput, costTier)
		costCacheWrite := cacheWritePreviewPrice(item, costInput, costTier)
		costCacheWrite1h := costInput * 2
		costCacheStorage := item.CacheStoragePriceRMB

		sellInput := price.SellingInput
		sellOutput := price.SellingOutput
		sellThinking := 0.0
		if item.Pricing != nil {
			sellThinking = item.Pricing.OutputPriceThinkingRMB
		}
		if sellTier != nil {
			sellInput = firstPositive(optionalTierValue(sellTier.SellingInputPrice), sellTier.InputPrice, sellInput)
			sellOutput = firstPositive(optionalTierValue(sellTier.SellingOutputPrice), sellTier.OutputPrice, sellOutput)
			sellThinking = firstPositive(optionalTierValue(sellTier.SellingOutputThinkingPrice), sellTier.OutputPriceThinking, sellThinking)
		}
		sellThinking = firstPositive(sellThinking, sellOutput)
		sellCacheRead := sellingCachePreviewPrice(sellInput, costInput, costCacheRead, sellTier, "read")
		sellCacheWrite := sellingCachePreviewPrice(sellInput, costInput, costCacheWrite, sellTier, "write")
		sellCacheWrite1h := sellInput * 2
		sellCacheStorage := sellingCachePreviewPrice(sellInput, costInput, costCacheStorage, sellTier, "storage")

		tierMatches = append(tierMatches,
			buildTierMatchSnapshot("supplier_cost", costTier, costTierIdx, costTierSource),
			buildTierMatchSnapshot("platform_selling", sellTier, sellTierIdx, sellTierSource),
		)
		if costTier != nil {
			warnings = append(warnings, fmt.Sprintf("supplier cost tier matched: %s", costTier.Name))
		}
		if sellTier != nil {
			warnings = append(warnings, fmt.Sprintf("platform selling tier matched: %s", sellTier.Name))
		}
		if item.SupportsCache && item.CacheMinTokens > 0 && inputTokens < float64(item.CacheMinTokens) && (cacheReadTokens > 0 || cacheWriteTokens > 0) {
			warnings = append(warnings, fmt.Sprintf("input_tokens is below cache_min_tokens %d; verify provider usage", item.CacheMinTokens))
		}

		addLayer("official_regular_input", costInput, regularInputTokens/1000000, "supplier regular input")
		addLayer("official_cache_read", costCacheRead, cacheReadTokens/1000000, cachePreviewDesc(item, "read"))
		addLayer("official_cache_write", costCacheWrite, cacheWrite5mTokens/1000000, cachePreviewDesc(item, "write"))
		addLayer("official_cache_write_1h", costCacheWrite1h, cacheWrite1hTokens/1000000, "1h cache write = 2x input")
		addLayer("official_cache_storage", costCacheStorage, cacheStorageTokens/1000000*cacheStorageHours, "cache storage")
		addLayer("official_output", costOutput, outputTokens/1000000, "supplier output")
		addLayer("official_reasoning_output", costThinking, thinkingTokens/1000000, "supplier reasoning output")

		regularInputDesc := "regular_input = input - cache_read - cache_write"
		if shouldIgnorePreviewCacheWrite(item) {
			regularInputDesc = "regular_input = input - cache_read; Gemini cache write is not a separate billable line item"
		}
		addStep("official", "official_regular_input", costInput, regularInputTokens/1000000, regularInputDesc)
		addStep("official", "official_cache_read", costCacheRead, cacheReadTokens/1000000, cachePreviewDesc(item, "read"))
		addStep("official", "official_cache_write", costCacheWrite, cacheWrite5mTokens/1000000, cachePreviewDesc(item, "write"))
		addStep("official", "official_cache_write_1h", costCacheWrite1h, cacheWrite1hTokens/1000000, "1h cache write = 2x input")
		addStep("official", "official_cache_storage", costCacheStorage, cacheStorageTokens/1000000*cacheStorageHours, "cache storage")
		addStep("official", "official_output", costOutput, outputTokens/1000000, "completion tokens")
		addStep("official", "official_reasoning_output", costThinking, thinkingTokens/1000000, "thinking tokens")

		discount := supplierDiscountForPreview(price)
		addStep("effective", "effective_regular_input", costInput*discount, regularInputTokens/1000000, fmt.Sprintf("supplier discount %.4f", discount))
		addStep("effective", "effective_cache_read", costCacheRead*discount, cacheReadTokens/1000000, fmt.Sprintf("supplier discount %.4f", discount))
		addStep("effective", "effective_cache_write", costCacheWrite*discount, cacheWrite5mTokens/1000000, fmt.Sprintf("supplier discount %.4f", discount))
		addStep("effective", "effective_cache_write_1h", costCacheWrite1h*discount, cacheWrite1hTokens/1000000, fmt.Sprintf("supplier discount %.4f", discount))
		addStep("effective", "effective_cache_storage", costCacheStorage*discount, cacheStorageTokens/1000000*cacheStorageHours, fmt.Sprintf("supplier discount %.4f", discount))
		addStep("effective", "effective_output", costOutput*discount, outputTokens/1000000, fmt.Sprintf("supplier discount %.4f", discount))
		addStep("effective", "effective_reasoning_output", costThinking*discount, thinkingTokens/1000000, fmt.Sprintf("supplier discount %.4f", discount))

		addStep("selling", "selling_regular_input", sellInput, regularInputTokens/1000000, "platform selling price")
		addStep("selling", "selling_cache_read", sellCacheRead, cacheReadTokens/1000000, "tier cache price or ratio-derived price")
		addStep("selling", "selling_cache_write", sellCacheWrite, cacheWrite5mTokens/1000000, "tier cache price or ratio-derived price")
		addStep("selling", "selling_cache_write_1h", sellCacheWrite1h, cacheWrite1hTokens/1000000, "1h cache write = 2x input")
		addStep("selling", "selling_cache_storage", sellCacheStorage, cacheStorageTokens/1000000*cacheStorageHours, "tier cache storage price or ratio-derived price")
		addStep("selling", "selling_output", sellOutput, outputTokens/1000000, "platform selling price")
		addStep("selling", "selling_reasoning_output", sellThinking, thinkingTokens/1000000, "platform reasoning output price")

		if cacheWriteIgnored || shouldIgnorePreviewCacheWrite(item) {
			formula = append(formula,
				"regular_input_tokens = max(input_tokens - cache_read_tokens, 0)",
				"cache_write_tokens ignored for Gemini context caching; use cache_read_tokens and cache_storage_tokens/cache_storage_hours",
			)
		} else {
			formula = append(formula,
				"regular_input_tokens = max(input_tokens - cache_read_tokens - cache_write_tokens, 0)",
				"cache_write_tokens = cache_write_5m_tokens + cache_write_1h_tokens; 1h writes use 2x input price",
			)
		}
		formula = append(formula,
			"official_amount = sum(supplier_matched_unit_price / 1,000,000 x tokens)",
			"effective_cost = sum(official_line_item x supplier_or_model_discount)",
			"selling_amount = sum(platform_matched_unit_price / 1,000,000 x tokens)",
			"gross_profit = selling_amount - effective_cost",
		)
	}

	official := sumPreviewLayers(layers)
	supplierDiscount := supplierDiscountForPreview(price)
	effective := sumPreviewSteps(steps, "effective")
	if effective == 0 {
		effective = official * supplierDiscount
	}
	sellingRate := 1.0
	if price.OfficialInput > 0 && price.SellingInput > 0 {
		sellingRate = price.SellingInput / price.OfficialInput
	} else if price.OfficialOutput > 0 && price.SellingOutput > 0 {
		sellingRate = price.SellingOutput / price.OfficialOutput
	}
	selling := sumPreviewSteps(steps, "selling")
	if selling == 0 {
		selling = official * sellingRate
	}
	if official == 0 {
		warnings = append(warnings, "official list price is 0 or missing; preview cannot be used for finance reconciliation")
	}
	finalDiscount := 0.0
	if official > 0 {
		finalDiscount = selling / official
	}
	responseInputTokens := normalizedInputTokens(params, 1000)
	responseCacheReadTokens := numParamAny(params, []string{"cache_read_tokens", "cached_tokens", "cache_hit_tokens"}, 0)
	responseCacheWriteTokens := numParam(params, "cache_write_tokens", 0)
	responseCacheWrite1hTokens := numParam(params, "cache_write_1h_tokens", 0)
	if shouldIgnorePreviewCacheWrite(item) {
		responseCacheWriteTokens = 0
		responseCacheWrite1hTokens = 0
	}
	regularInputTokens := math.Max(responseInputTokens-responseCacheReadTokens-responseCacheWriteTokens, 0)
	return calculatePreviewResponse{
		ModelID: item.ID, ModelName: item.ModelName, CalculatorType: calculatorType, CalculatorStatus: calculatorStatus,
		Currency: "CNY", OfficialAmount: official, SellingAmount: selling, EffectiveCost: effective, GrossProfit: selling - effective,
		FinalDiscount: finalDiscount, Layers: layers, Steps: steps, TierMatches: tierMatches, SpecialParams: specialParams,
		RegularInputTokens: regularInputTokens, CacheReadTokens: responseCacheReadTokens, CacheWriteTokens: responseCacheWriteTokens, CacheWrite1hTokens: responseCacheWrite1hTokens,
		ThinkingTokens: numParamAny(params, []string{"thinking_tokens", "reasoning_tokens"}, 0), CacheSavings: estimatePreviewCacheSavings(steps),
		Formula: formula, Warnings: warnings,
	}
}
func parsePreviewTiers(raw model.JSON) *model.PriceTiersData {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var data model.PriceTiersData
	if err := json.Unmarshal(raw, &data); err != nil {
		return nil
	}
	for i := range data.Tiers {
		data.Tiers[i].Normalize()
	}
	return &data
}

func selectPreviewTier(raw model.JSON, inputTokens, outputTokens float64, source string) (*model.PriceTier, int, string) {
	data := parsePreviewTiers(raw)
	if data == nil || len(data.Tiers) == 0 {
		return nil, -1, source
	}
	model.SortTiers(data.Tiers)
	idx, tier, _ := model.SelectTierOrLargest(data.Tiers, int64(inputTokens), int64(outputTokens))
	if tier == nil {
		return nil, -1, source
	}
	return tier, idx, source
}

func buildTierMatchSnapshot(side string, tier *model.PriceTier, idx int, source string) tierMatchSnapshot {
	if tier == nil {
		return tierMatchSnapshot{Side: side, Index: -1, Source: source, Matched: false}
	}
	return tierMatchSnapshot{
		Side: side, Name: tier.Name, Index: idx, Source: source,
		InputMin: tier.InputMin, InputMax: tier.InputMax, OutputMin: tier.OutputMin, OutputMax: tier.OutputMax,
		Input: tier.InputPrice, Output: tier.OutputPrice, CacheRead: tier.CacheInputPrice,
		CacheWrite: tier.CacheWritePrice, Thinking: tier.OutputPriceThinking, Matched: true,
	}
}

func tierValue(tier *model.PriceTier, key string) float64 {
	if tier == nil {
		return 0
	}
	switch key {
	case "input":
		return tier.InputPrice
	case "output":
		return tier.OutputPrice
	case "thinking":
		return tier.OutputPriceThinking
	case "cache_read":
		return tier.CacheInputPrice
	case "cache_write":
		return tier.CacheWritePrice
	default:
		return 0
	}
}

func optionalTierValue(value *float64) float64 {
	if value == nil {
		return 0
	}
	return *value
}

func supplierDiscountForPreview(price modelOpsPriceSummary) float64 {
	discount := price.SupplierDiscount
	if discount <= 0 {
		discount = 1
	}
	if price.ModelDiscount > 0 {
		discount = price.ModelDiscount
	}
	return discount
}

func cacheReadPreviewPrice(item model.AIModel, baseInput float64, tier *model.PriceTier) float64 {
	if v := tierValue(tier, "cache_read"); v > 0 {
		return v
	}
	if item.CacheMechanism == "both" && item.CacheExplicitInputPriceRMB > 0 {
		return item.CacheExplicitInputPriceRMB
	}
	if item.CacheInputPriceRMB > 0 {
		return item.CacheInputPriceRMB
	}
	switch item.CacheMechanism {
	case "explicit":
		return baseInput * 0.10
	case "both":
		return baseInput * 0.20
	default:
		return baseInput * 0.50
	}
}

func cacheWritePreviewPrice(item model.AIModel, baseInput float64, tier *model.PriceTier) float64 {
	if v := tierValue(tier, "cache_write"); v > 0 {
		return v
	}
	if item.CacheWritePriceRMB > 0 {
		return item.CacheWritePriceRMB
	}
	if item.CacheMechanism == "auto" || item.CacheMechanism == "none" || item.CacheMechanism == "" {
		return baseInput
	}
	return baseInput * 1.25
}

func sellingCachePreviewPrice(sellInput, costInput, costCache float64, tier *model.PriceTier, kind string) float64 {
	if tier != nil {
		if kind == "read" && tier.CacheInputPrice > 0 {
			return tier.CacheInputPrice
		}
		if kind == "write" && tier.CacheWritePrice > 0 {
			return tier.CacheWritePrice
		}
	}
	if costInput <= 0 {
		return sellInput
	}
	return sellInput * costCache / costInput
}

func cachePreviewDesc(item model.AIModel, kind string) string {
	if kind == "write" {
		return "cache write, mechanism: " + defaultString(item.CacheMechanism, "none")
	}
	return "cache read, mechanism: " + defaultString(item.CacheMechanism, "none")
}

func sumPreviewLayers(layers []priceLayer) float64 {
	total := 0.0
	for _, layer := range layers {
		total += layer.Amount
	}
	return total
}

func sumPreviewSteps(steps []calculationStep, section string) float64 {
	total := 0.0
	for _, step := range steps {
		if step.Section == section {
			total += step.Amount
		}
	}
	return total
}

func estimatePreviewCacheSavings(steps []calculationStep) float64 {
	regularUnit := 0.0
	cacheReadUnit := 0.0
	cacheReadQty := 0.0
	for _, step := range steps {
		if step.Section != "official" {
			continue
		}
		switch step.Label {
		case "official_regular_input":
			regularUnit = step.UnitPrice
		case "official_cache_read":
			cacheReadUnit = step.UnitPrice
			cacheReadQty = step.Quantity
		}
	}
	if regularUnit <= cacheReadUnit || cacheReadQty <= 0 {
		return 0
	}
	return (regularUnit - cacheReadUnit) * cacheReadQty
}

func collectSpecialPreviewParams(params map[string]interface{}) map[string]interface{} {
	out := map[string]interface{}{}
	for _, key := range []string{
		"enable_thinking", "thinking_budget", "reasoning_effort",
		"processing_mode", "service_tier", "region_mode", "batch",
		"cache_mode", "cache_control", "cache_ttl", "cached_tokens", "cache_hit_tokens", "cache_miss_tokens",
		"input_text_tokens", "input_image_tokens", "input_video_tokens", "input_audio_tokens", "input_image_count",
		"resolution", "quality", "mode", "size", "width", "height", "aspect_ratio", "output_format", "background", "style",
		"input_video_seconds", "output_seconds", "input_contains_video", "generate_audio", "draft", "video_count", "output_count", "audio_mode",
		"voice_tier", "voice_id", "voice_design_count", "voice_clone_count", "sample_rate", "format", "speed", "pitch", "volume", "streaming", "realtime_session_minutes",
		"web_search_count", "search_tokens", "google_search_query_count", "maps_query_count", "tool_call_count", "file_search_count", "container_seconds", "container_minutes", "retrieval_count", "knowledge_search_count",
		"page_count", "document_count", "storage_gb", "storage_hours",
		"response_format", "tools", "tool_choice",
	} {
		if value, ok := params[key]; ok {
			out[key] = value
		}
	}
	return out
}

func seedanceResolutionSize(resolution string) (float64, float64) {
	switch strings.ToLower(resolution) {
	case "480p", "480":
		return 854, 480
	case "1080p", "1080":
		return 1920, 1080
	default:
		return 1280, 720
	}
}

func seedance15UnitPrice(generateAudio bool, serviceTier string) float64 {
	unit := 16.0
	if !generateAudio {
		unit = 8.0
	}
	if strings.EqualFold(strings.TrimSpace(serviceTier), "flex") {
		unit *= 0.5
	}
	return unit
}

func seedance15PriceLabel(generateAudio bool, serviceTier string) string {
	mode := "在线推理"
	if strings.EqualFold(strings.TrimSpace(serviceTier), "flex") {
		mode = "离线推理"
	}
	audio := "有声视频"
	if !generateAudio {
		audio = "无声视频"
	}
	return mode + " · " + audio
}

func seedanceMinTokens(resolution string, containsVideo bool) float64 {
	if !containsVideo {
		return 0
	}
	// Volcengine's public examples are prices per video, not token counts.
	// These values are token estimates derived from the official price examples
	// for input-video scenarios, so the preview does not overstate cost by 100x.
	base := map[string]float64{"480p": 55000, "720p": 118260, "1080p": 265882}
	if v, ok := base[resolution]; ok {
		return v
	}
	return base["720p"]
}

func durationQuantity(unit string, seconds float64) float64 {
	switch unit {
	case model.UnitPerMinute:
		return seconds / 60
	case model.UnitPerHour:
		return seconds / 3600
	default:
		return seconds
	}
}

func characterDivisor(unit string) float64 {
	switch unit {
	case model.UnitPerMillionCharacters:
		return 1000000
	default:
		return 10000
	}
}

func numParam(params map[string]interface{}, key string, fallback float64) float64 {
	v, ok := params[key]
	if !ok || v == nil {
		return fallback
	}
	switch n := v.(type) {
	case float64:
		return n
	case int:
		return float64(n)
	case string:
		parsed, err := strconv.ParseFloat(n, 64)
		if err == nil {
			return parsed
		}
	}
	return fallback
}

func numParamAny(params map[string]interface{}, keys []string, fallback float64) float64 {
	for _, key := range keys {
		if _, ok := params[key]; ok {
			return numParam(params, key, fallback)
		}
	}
	return fallback
}

func normalizedInputTokens(params map[string]interface{}, fallback float64) float64 {
	if _, ok := params["input_tokens"]; ok {
		return numParam(params, "input_tokens", fallback)
	}
	if _, ok := params["prompt_tokens"]; ok {
		return numParam(params, "prompt_tokens", fallback)
	}
	sum := 0.0
	for _, key := range []string{"input_text_tokens", "input_image_tokens", "input_video_tokens", "input_audio_tokens"} {
		sum += numParam(params, key, 0)
	}
	if sum > 0 {
		return sum
	}
	missTokens := numParam(params, "cache_miss_tokens", 0)
	hitTokens := numParamAny(params, []string{"cache_hit_tokens", "cached_tokens", "cache_read_tokens"}, 0)
	if missTokens+hitTokens > 0 {
		return missTokens + hitTokens
	}
	return fallback
}

func strParam(params map[string]interface{}, key, fallback string) string {
	if v, ok := params[key].(string); ok && v != "" {
		return v
	}
	return fallback
}

func boolParam(params map[string]interface{}, key string, fallback bool) bool {
	if v, ok := params[key].(bool); ok {
		return v
	}
	return fallback
}

func boolParamFlexible(params map[string]interface{}, key string, fallback bool) bool {
	v, ok := params[key]
	if !ok || v == nil {
		return fallback
	}
	switch typed := v.(type) {
	case bool:
		return typed
	case string:
		switch strings.ToLower(strings.TrimSpace(typed)) {
		case "true", "1", "yes", "on":
			return true
		case "false", "0", "no", "off":
			return false
		}
	}
	return fallback
}

func firstPositive(values ...float64) float64 {
	for _, value := range values {
		if value > 0 {
			return value
		}
	}
	return 0
}

func formatFloat(v float64) string {
	return strconv.FormatFloat(v, 'f', 2, 64)
}

func classifyPriceStatus(item model.AIModel, price modelOpsPriceSummary, calculatorStatus string) string {
	if item.IsFreeTier && price.OfficialInput == 0 && price.OfficialOutput == 0 {
		return "free"
	}
	hasOfficial := price.OfficialInput > 0 || price.OfficialOutput > 0 || price.HasTiers
	hasSelling := price.SellingInput > 0 || price.SellingOutput > 0
	if !hasOfficial {
		return "missing"
	}
	if !hasSelling {
		return "no_selling"
	}
	if price.SellingInput > 0 && price.EffectiveInput > 0 && price.SellingInput < price.EffectiveInput {
		return "negative_margin"
	}
	if price.SellingOutput > 0 && price.EffectiveOutput > 0 && price.SellingOutput < price.EffectiveOutput {
		return "negative_margin"
	}
	if calculatorStatus == "needs_review" {
		return "needs_verification"
	}
	return "ok"
}

func (h *ModelOpsHandler) loadLabels(ids []uint) map[uint][]model.ModelLabel {
	out := make(map[uint][]model.ModelLabel)
	if len(ids) == 0 {
		return out
	}
	var labels []model.ModelLabel
	h.db.Where("model_id IN ?", ids).Find(&labels)
	for _, label := range labels {
		out[label.ModelID] = append(out[label.ModelID], label)
	}
	return out
}

func (h *ModelOpsHandler) loadAliases() map[string][]modelOpsAlias {
	var rows []model.ModelAlias
	h.db.Order("is_active DESC, is_public DESC, confidence DESC, updated_at DESC").Find(&rows)
	out := make(map[string][]modelOpsAlias)
	for _, row := range rows {
		item := modelOpsAlias{
			ID:              row.ID,
			AliasName:       row.AliasName,
			TargetModelName: row.TargetModelName,
			SupplierID:      row.SupplierID,
			AliasType:       row.AliasType,
			Source:          row.Source,
			Confidence:      row.Confidence,
			IsPublic:        row.IsPublic,
			IsActive:        row.IsActive,
			Notes:           row.Notes,
		}
		out[row.TargetModelName] = append(out[row.TargetModelName], item)
		if row.AliasName != row.TargetModelName {
			out[row.AliasName] = append(out[row.AliasName], item)
		}
	}
	return out
}

func (h *ModelOpsHandler) loadRoutes() map[string]modelOpsRouteSummary {
	var rows []routeAgg
	h.db.Model(&model.ChannelModel{}).
		Select(`channel_models.standard_model_id,
			COUNT(*) AS total,
			SUM(CASE WHEN channel_models.is_active = ? THEN 1 ELSE 0 END) AS active,
			SUM(CASE WHEN channel_models.is_active = ? AND channels.status = ? THEN 1 ELSE 0 END) AS healthy`, true, true, "active").
		Joins("LEFT JOIN channels ON channels.id = channel_models.channel_id").
		Group("channel_models.standard_model_id").
		Scan(&rows)
	out := make(map[string]modelOpsRouteSummary, len(rows))
	for _, row := range rows {
		out[row.StandardModelID] = modelOpsRouteSummary{Total: row.Total, Active: row.Active, Healthy: row.Healthy}
	}
	return out
}

func (h *ModelOpsHandler) loadUsage24h() map[string]modelOpsUsageSummary {
	var rows []usageAgg
	h.db.Model(&model.ApiCallLog{}).
		Select(`COALESCE(NULLIF(actual_model, ''), request_model) AS model_name,
			COUNT(*) AS requests,
			SUM(CASE WHEN status = ? OR status_code < 400 THEN 1 ELSE 0 END) AS successes,
			AVG(total_latency_ms) AS avg_latency,
			SUM(prompt_tokens) AS prompt_tokens,
			SUM(completion_tokens) AS completion_tokens,
			SUM(total_tokens) AS total_tokens,
			SUM(cache_read_tokens) AS cache_read_tokens,
			SUM(cache_write_tokens) AS cache_write_tokens,
			SUM(image_count) AS image_count,
			SUM(char_count) AS char_count,
			SUM(duration_sec) AS duration_sec,
			SUM(call_count) AS call_count,
			SUM(CASE WHEN actual_cost_units > 0 THEN actual_cost_units / 100000000.0 WHEN actual_cost_credits > 0 THEN actual_cost_credits / 10000.0 ELSE cost_rmb END) AS actual_revenue_rmb,
			SUM(under_collected_credits) AS under_collected_credits,
			SUM(cache_savings_rmb) AS cache_savings_rmb,
			SUM(CASE WHEN billing_status = ? THEN 1 ELSE 0 END) AS deduct_failed_requests`, "success", "deduct_failed").
		Where("created_at >= ?", time.Now().Add(-24*time.Hour)).
		Group("COALESCE(NULLIF(actual_model, ''), request_model)").
		Scan(&rows)

	models := h.loadModelCostInputs()
	out := make(map[string]modelOpsUsageSummary, len(rows))
	for _, row := range rows {
		rate := 0.0
		if row.Requests > 0 {
			rate = float64(row.Successes) * 100 / float64(row.Requests)
		}
		discountedCost := discountedCostForModelOpsUsage(models[row.ModelName], row)
		grossProfit := row.ActualRevenueRMB - discountedCost
		margin := 0.0
		if row.ActualRevenueRMB > 0 {
			margin = grossProfit * 100 / row.ActualRevenueRMB
		}
		out[row.ModelName] = modelOpsUsageSummary{
			Requests:         row.Requests,
			Successes:        row.Successes,
			AvgLatency:       row.AvgLatency,
			SuccessRate:      rate,
			PromptTokens:     row.PromptTokens,
			CompletionTokens: row.CompletionTokens,
			TotalTokens:      row.TotalTokens,
			CacheReadTokens:  row.CacheReadTokens,
			CacheWriteTokens: row.CacheWriteTokens,
			ImageCount:       row.ImageCount,
			CharCount:        row.CharCount,
			DurationSec:      row.DurationSec,
			CallCount:        row.CallCount,
			ActualRevenueRMB: row.ActualRevenueRMB,
			// JSON key remains platform_cost_rmb for API compatibility; value is discounted supplier/model cost.
			PlatformCostRMB:      discountedCost,
			GrossProfitRMB:       grossProfit,
			GrossProfitMargin:    margin,
			UnderCollectedRMB:    float64(row.UnderCollectedCredits) / 10000,
			CacheSavingsRMB:      row.CacheSavingsRMB,
			DeductFailedRequests: row.DeductFailedRequests,
		}
	}
	return out
}

func (h *ModelOpsHandler) loadModelCostInputs() map[string]model.AIModel {
	var models []model.AIModel
	h.db.Preload("Supplier").Find(&models)
	out := make(map[string]model.AIModel, len(models))
	for _, item := range models {
		out[item.ModelName] = item
	}
	return out
}

func discountedCostForModelOpsUsage(item model.AIModel, usage usageAgg) float64 {
	discount := item.Discount
	if discount <= 0 {
		discount = item.Supplier.Discount
	}
	if discount <= 0 {
		discount = 1
	}

	unit := item.PricingUnit
	if unit == "" || unit == model.UnitPerMillionTokens {
		inputUnit := item.InputCostRMB
		outputUnit := item.OutputCostRMB
		regularInput := usage.PromptTokens - usage.CacheReadTokens - usage.CacheWriteTokens
		if regularInput < 0 {
			regularInput = 0
		}
		cacheReadUnit := cacheReadCostUnit(item, usage.CacheWriteTokens > 0)
		cacheWriteUnit := cacheWriteCostUnit(item)
		amount := (float64(regularInput)*inputUnit +
			float64(usage.CacheReadTokens)*cacheReadUnit +
			float64(usage.CacheWriteTokens)*cacheWriteUnit +
			float64(usage.CompletionTokens)*outputUnit) / 1_000_000.0
		return amount * discount
	}

	quantity := modelOpsUsageQuantity(unit, usage)
	if quantity <= 0 {
		return 0
	}
	return item.InputCostRMB * quantity * discount
}

func cacheReadCostUnit(item model.AIModel, hasCacheWrite bool) float64 {
	if !item.SupportsCache || item.InputCostRMB <= 0 {
		return item.InputCostRMB
	}
	switch item.CacheMechanism {
	case "both":
		if hasCacheWrite {
			if item.CacheExplicitInputPriceRMB > 0 {
				return item.CacheExplicitInputPriceRMB
			}
			return item.InputCostRMB * 0.10
		}
		if item.CacheInputPriceRMB > 0 {
			return item.CacheInputPriceRMB
		}
		return item.InputCostRMB * 0.20
	case "explicit":
		if item.CacheInputPriceRMB > 0 {
			return item.CacheInputPriceRMB
		}
		return item.InputCostRMB * 0.10
	case "auto":
		fallthrough
	default:
		if item.CacheInputPriceRMB > 0 {
			return item.CacheInputPriceRMB
		}
		return item.InputCostRMB * 0.50
	}
}

func cacheWriteCostUnit(item model.AIModel) float64 {
	if !item.SupportsCache || item.InputCostRMB <= 0 {
		return item.InputCostRMB
	}
	switch item.CacheMechanism {
	case "both", "explicit":
		if item.CacheWritePriceRMB > 0 {
			return item.CacheWritePriceRMB
		}
		return item.InputCostRMB * 1.25
	default:
		return item.InputCostRMB
	}
}

func modelOpsUsageQuantity(unit string, usage usageAgg) float64 {
	switch unit {
	case model.UnitPerImage:
		if usage.ImageCount > 0 {
			return float64(usage.ImageCount)
		}
		return float64(usage.Requests)
	case model.UnitPerSecond:
		return usage.DurationSec
	case model.UnitPerMinute:
		return usage.DurationSec / 60.0
	case model.UnitPerHour:
		return usage.DurationSec / 3600.0
	case model.UnitPer10kCharacters, model.UnitPerKChars:
		return float64(usage.CharCount) / 10000.0
	case model.UnitPerMillionCharacters:
		return float64(usage.CharCount) / 1_000_000.0
	case model.UnitPerCall:
		if usage.CallCount > 0 {
			return float64(usage.CallCount)
		}
		return float64(usage.Requests)
	default:
		return float64(usage.Requests)
	}
}

func matchesProfileFilters(c *gin.Context, p modelOpsProfile) bool {
	filters := map[string]string{
		"calculator_type":   p.CalculatorType,
		"price_status":      p.PriceStatus,
		"calculator_status": p.CalculatorStatus,
		"label_status":      p.LabelStatus,
		"route_status":      p.RouteStatus,
		"health_status":     p.HealthStatus,
		"public_status":     p.PublicStatus,
		"risk_level":        p.RiskLevel,
	}
	for key, value := range filters {
		if wants := csvQuery(c, key); len(wants) > 0 && !containsString(wants, value) {
			return false
		}
	}
	return true
}

func csvQuery(c *gin.Context, key string) []string {
	raw := strings.TrimSpace(c.Query(key))
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	values := make([]string, 0, len(parts))
	for _, part := range parts {
		if value := strings.TrimSpace(part); value != "" {
			values = append(values, value)
		}
	}
	return values
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func buildModelOpsStats(list []modelOpsProfile) modelOpsStats {
	stats := modelOpsStats{Total: len(list)}
	for _, p := range list {
		if p.HealthStatus == "healthy" {
			stats.Healthy++
		}
		if p.PublicStatus == "visible" {
			stats.Visible++
		}
		if p.RiskLevel == "red" {
			stats.HighRisk++
		}
		if p.PriceStatus == "missing" || p.PriceStatus == "no_selling" {
			stats.NeedPrice++
		}
		if p.CalculatorStatus == "needs_review" || p.CalculatorStatus == "unbound" {
			stats.NeedCalculator++
		}
		if p.LabelStatus == "missing" || p.LabelStatus == "suggested" {
			stats.NeedLabels++
		}
		if p.RouteStatus == "no_route" {
			stats.NoRoute++
		}
		if p.PriceStatus == "negative_margin" {
			stats.NegativeMargin++
		}
		if p.PriceStatus == "needs_verification" {
			stats.NeedsVerification++
		}
	}
	return stats
}

func bindModelOpsBatchRequest(c *gin.Context) (modelOpsBatchRequest, bool) {
	var req modelOpsBatchRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.ErrorMsg(c, http.StatusBadRequest, 400, err.Error())
		return req, false
	}
	req.Action = strings.TrimSpace(req.Action)
	if req.Action == "" {
		response.ErrorMsg(c, http.StatusBadRequest, 400, "action 不能为空")
		return req, false
	}
	if len(req.ModelIDs) > 500 {
		response.ErrorMsg(c, http.StatusBadRequest, 400, "最多一次操作 500 个模型")
		return req, false
	}
	return req, true
}

func makeBatchResponse(action string, items []modelOpsBatchPreviewItem, warnings []string, updated int64, at *time.Time) modelOpsBatchResponse {
	executable := 0
	for _, item := range items {
		if item.CanExecute {
			executable++
		}
	}
	return modelOpsBatchResponse{
		Action: action, Total: len(items), Executable: executable, Skipped: len(items) - executable,
		Updated: updated, Items: items, Warnings: warnings, ExecutedAt: at,
	}
}

func batchModelStatePatch(action string) map[string]interface{} {
	switch action {
	case "enable", "set_public":
		return map[string]interface{}{"is_active": true, "status": "online"}
	case "disable", "hide_public":
		return map[string]interface{}{"is_active": false, "status": "offline"}
	default:
		return map[string]interface{}{}
	}
}

func describeBatchChange(req modelOpsBatchRequest, item model.AIModel) (string, string, string, []string, bool) {
	switch req.Action {
	case "enable", "set_public":
		return modelAvailabilityText(item), "已启用并上线", "启用模型；公开可用仍取决于售价、渠道和健康状态。", nil, true
	case "disable", "hide_public":
		return modelAvailabilityText(item), "已停用并离线", "停用模型，用户侧不可调用。", nil, true
	case "set_free_tier":
		return boolText(item.IsFreeTier), "免费层", "加入免费模型范围。", nil, true
	case "unset_free_tier":
		return boolText(item.IsFreeTier), "付费层", "移出免费模型范围。", nil, true
	case "add_label", "remove_label":
		key, value := batchLabel(req)
		if key == "" {
			return "标签", "无效", "必须填写标签 key。", []string{"缺少 label_key"}, false
		}
		return "标签", key + ":" + value, "批量调整模型标签。", nil, true
	case "mark_price_review":
		return "价格状态", "待复核", "添加 ops:price_review 标签。", nil, true
	case "bind_calculator":
		calculatorType := strings.TrimSpace(stringPayload(req.Payload, "calculator_type"))
		if calculatorType == "" {
			return "计算器", "无效", "必须选择计算器类型。", []string{"缺少 calculator_type"}, false
		}
		return "计算器", calculatorType, "绑定指定价格计算器。", nil, true
	case "unbind_calculator":
		return "计算器", "自动推断", "移除显式计算器绑定。", nil, true
	default:
		return "未知", req.Action, "不支持的批量动作。", []string{"未知动作"}, false
	}
}

func batchLabel(req modelOpsBatchRequest) (string, string) {
	key, _ := req.Payload["label_key"].(string)
	value, _ := req.Payload["label_value"].(string)
	if strings.TrimSpace(key) == "" {
		key = "tag"
	}
	return strings.TrimSpace(key), strings.TrimSpace(value)
}

func stringPayload(payload map[string]interface{}, key string) string {
	if payload == nil {
		return ""
	}
	value, _ := payload[key].(string)
	return value
}

func labelValue(labels []model.ModelLabel, key string) string {
	for _, label := range labels {
		if label.LabelKey == key {
			return label.LabelValue
		}
	}
	return ""
}

func calculatorHintFor(calculatorType string) string {
	for _, spec := range calculatorCatalog() {
		if spec.Type == calculatorType {
			return spec.Description
		}
	}
	return "杩愯惀鏄惧紡缁戝畾鐨勪环鏍艰绠楀櫒"
}

func convertLabels(labels []model.ModelLabel) []modelOpsLabel {
	out := make([]modelOpsLabel, 0, len(labels))
	for _, label := range labels {
		out = append(out, modelOpsLabel{Key: label.LabelKey, Value: label.LabelValue})
	}
	return out
}

func featureNames(item model.AIModel) []string {
	raw := string(item.Features)
	if raw == "" || raw == "null" {
		return nil
	}
	names := make([]string, 0)
	for _, key := range []string{"supports_thinking", "supports_vision", "supports_web_search", "supports_json_mode", "supports_cache", "function_calling"} {
		if strings.Contains(raw, `"`+key+`":true`) || strings.Contains(raw, `"`+key+`": true`) {
			names = append(names, key)
		}
	}
	return names
}

func stringArrayFromJSON(raw model.JSON) []string {
	text := strings.TrimSpace(string(raw))
	if text == "" || text == "null" || text == "[]" {
		return nil
	}
	text = strings.Trim(text, "[]")
	if text == "" {
		return nil
	}
	parts := strings.Split(text, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.Trim(strings.TrimSpace(part), `"`)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

func riskReasons(priceStatus, calculatorStatus, labelStatus, routeStatus, healthStatus string) []string {
	reasons := make([]string, 0, 4)
	if priceStatus != "ok" && priceStatus != "free" {
		reasons = append(reasons, "浠锋牸鐘舵€侊細"+priceStatus)
	}
	if calculatorStatus == "needs_review" || calculatorStatus == "unbound" {
		reasons = append(reasons, "浠锋牸璁＄畻鍣ㄩ渶瑕佸鐞嗭細"+calculatorStatus)
	}
	if labelStatus == "missing" || labelStatus == "suggested" {
		reasons = append(reasons, "能力标签未完全确认")
	}
	if routeStatus != "available" {
		reasons = append(reasons, "route status: "+routeStatus)
	}
	if healthStatus != "healthy" && healthStatus != routeStatus {
		reasons = append(reasons, "鍋ュ悍鐘舵€侊細"+healthStatus)
	}
	return reasons
}

func suggestedActions(priceStatus, calculatorStatus, labelStatus, routeStatus, healthStatus string) []string {
	actions := make([]string, 0, 4)
	if priceStatus == "missing" || priceStatus == "no_selling" || priceStatus == "negative_margin" {
		actions = append(actions, "琛ュ叏瀹樼綉鍘熶环銆佸钩鍙板敭浠蜂笌鎶樻墸閾捐矾")
	}
	if calculatorStatus == "needs_review" || calculatorStatus == "unbound" {
		actions = append(actions, "缁戝畾鎴栨牳楠屼笓鐢ㄤ环鏍艰绠楀櫒")
	}
	if labelStatus == "missing" || labelStatus == "suggested" {
		actions = append(actions, "纭鑳藉姏鏍囩鍜屽叕寮€鎼滅储鏍囩")
	}
	if routeStatus != "available" || healthStatus == "error" || healthStatus == "offline" {
		actions = append(actions, "检查渠道映射和真实请求健康状态")
	}
	return actions
}

func containsAny(values []string, targets ...string) bool {
	set := make(map[string]struct{}, len(targets))
	for _, target := range targets {
		set[target] = struct{}{}
	}
	for _, value := range values {
		if _, ok := set[value]; ok {
			return true
		}
	}
	return false
}

func boolText(v bool) string {
	if v {
		return "是"
	}
	return "否"
}

func modelAvailabilityText(item model.AIModel) string {
	if item.IsActive && item.Status == "online" {
		return "启用/在线"
	}
	if item.IsActive {
		return "启用/" + item.Status
	}
	if item.Status == "" {
		return "停用"
	}
	return "停用/" + item.Status
}

func intQuery(c *gin.Context, key string, fallback int) int {
	raw := c.Query(key)
	if raw == "" {
		return fallback
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		return fallback
	}
	return n
}
