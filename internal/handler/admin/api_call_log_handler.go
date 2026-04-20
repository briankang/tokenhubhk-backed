package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"tokenhub-server/internal/config"
	"tokenhub-server/internal/model"
	"tokenhub-server/internal/pkg/errcode"
	"tokenhub-server/internal/pkg/response"
	"tokenhub-server/internal/service/apikey"
)

// ApiCallLogHandler API 调用日志查询处理器（管理员专用）
type ApiCallLogHandler struct {
	db        *gorm.DB
	apiKeySvc *apikey.ApiKeyService
}

// NewApiCallLogHandler 创建 API 调用日志处理器
// apiKeySvc 可为 nil（仅影响 Replay 端点）
func NewApiCallLogHandler(db *gorm.DB, apiKeySvc *apikey.ApiKeyService) *ApiCallLogHandler {
	if db == nil {
		panic("api_call_log handler: db is nil")
	}
	return &ApiCallLogHandler{db: db, apiKeySvc: apiKeySvc}
}

// Register 注册路由
func (h *ApiCallLogHandler) Register(rg *gin.RouterGroup) {
	rg.GET("/api-call-logs", h.List)
	rg.GET("/api-call-logs/summary", h.Summary)
	rg.GET("/api-call-logs/:requestId", h.GetByRequestID)
	rg.GET("/api-call-logs/:requestId/chain", h.GetFullChain)
	rg.GET("/api-call-logs/:requestId/cost-breakdown", h.GetCostBreakdown)
	rg.POST("/api-call-logs/:requestId/replay", h.Replay)
}

// ApiCallLogItem 列表返回项：ApiCallLog + 关联用户邮箱
// 用于成本分析表格的表头展示
// SupplierResolved 字段：旧日志可能没存 supplier_name；通过 JOIN ai_models→suppliers 实时兜底
type ApiCallLogItem struct {
	model.ApiCallLog
	UserEmail        string `json:"user_email"`
	UserName         string `json:"user_name"`
	SupplierResolved string `json:"supplier_resolved"`
}

// List 分页查询 API 调用日志 GET /api/v1/admin/api-call-logs
// 支持筛选：request_id, user_id, user_email, model, status, channel_id,
//          supplier_name, start_date, end_date, endpoint, min_latency_ms,
//          errors_only, cost_min, cost_max
func (h *ApiCallLogHandler) List(c *gin.Context) {
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "20"))
	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > 100 {
		pageSize = 20
	}

	// 基础查询：LEFT JOIN users 以便展示/筛选邮箱
	// 使用别名 l / u 以避免歧义。
	// 供应商解析采用相关子查询（避免 ai_models.model_name 一对多导致行重复）
	query := h.db.WithContext(c.Request.Context()).
		Table("api_call_logs AS l").
		Joins("LEFT JOIN users AS u ON u.id = l.user_id")

	// 筛选条件
	if rid := c.Query("request_id"); rid != "" {
		query = query.Where("l.request_id LIKE ?", "%"+rid+"%")
	}
	if uid := c.Query("user_id"); uid != "" {
		query = query.Where("l.user_id = ?", uid)
	}
	if ue := c.Query("user_email"); ue != "" {
		query = query.Where("u.email LIKE ?", "%"+ue+"%")
	}
	if m := c.Query("model"); m != "" {
		query = query.Where("l.request_model LIKE ?", "%"+m+"%")
	}
	if s := c.Query("status"); s != "" {
		query = query.Where("l.status = ?", s)
	}
	if cid := c.Query("channel_id"); cid != "" {
		query = query.Where("l.channel_id = ?", cid)
	}
	if sn := c.Query("supplier_name"); sn != "" {
		// 同时匹配日志冗余字段和通过模型反查的供应商名（兜底旧日志）
		supplierSubquery := `(SELECT s.name FROM ai_models m
				LEFT JOIN suppliers s ON s.id = m.supplier_id AND s.deleted_at IS NULL
				WHERE m.model_name = COALESCE(NULLIF(l.actual_model,''), l.request_model)
				  AND m.deleted_at IS NULL
				LIMIT 1)`
		query = query.Where("l.supplier_name LIKE ? OR "+supplierSubquery+" LIKE ?", "%"+sn+"%", "%"+sn+"%")
	}
	if sd := c.Query("start_date"); sd != "" {
		query = query.Where("l.created_at >= ?", sd)
	}
	if ed := c.Query("end_date"); ed != "" {
		query = query.Where("l.created_at <= ?", ed+" 23:59:59")
	}
	if ep := c.Query("endpoint"); ep != "" {
		query = query.Where("l.endpoint = ?", ep)
	}
	// 最小延迟筛选（慢请求排查）
	if minLatency := c.Query("min_latency_ms"); minLatency != "" {
		query = query.Where("l.total_latency_ms >= ?", minLatency)
	}
	// 成本区间（积分）
	if cmin := c.Query("cost_min"); cmin != "" {
		query = query.Where("l.cost_credits >= ?", cmin)
	}
	if cmax := c.Query("cost_max"); cmax != "" {
		query = query.Where("l.cost_credits <= ?", cmax)
	}
	// 仅错误请求
	if c.Query("errors_only") == "true" {
		query = query.Where("l.status != ?", "success")
	}

	var total int64
	if err := query.Count(&total).Error; err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, err.Error())
		return
	}

	var items []ApiCallLogItem
	offset := (page - 1) * pageSize
	// 显式选择字段，避免 ApiCallLog 和 users 字段冲突
	// supplier_resolved 优先取日志冗余字段，缺失时通过相关子查询从 ai_models→suppliers 反查
	supplierSelect := `COALESCE(
		NULLIF(l.supplier_name, ''),
		(SELECT s.name FROM ai_models m
			LEFT JOIN suppliers s ON s.id = m.supplier_id AND s.deleted_at IS NULL
			WHERE m.model_name = COALESCE(NULLIF(l.actual_model,''), l.request_model)
			  AND m.deleted_at IS NULL
			LIMIT 1),
		''
	) AS supplier_resolved`

	if err := query.
		Select("l.*, u.email AS user_email, u.name AS user_name, " + supplierSelect).
		Order("l.created_at DESC, l.id DESC").
		Offset(offset).Limit(pageSize).
		Scan(&items).Error; err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, err.Error())
		return
	}

	response.PageResult(c, items, total, page, pageSize)
}

// GetByRequestID 根据 request_id 获取单条详情 GET /api/v1/admin/api-call-logs/:requestId
func (h *ApiCallLogHandler) GetByRequestID(c *gin.Context) {
	requestID := c.Param("requestId")
	if requestID == "" {
		response.Error(c, http.StatusBadRequest, errcode.ErrValidation)
		return
	}

	var log model.ApiCallLog
	if err := h.db.WithContext(c.Request.Context()).Where("request_id = ?", requestID).First(&log).Error; err != nil {
		response.ErrorMsg(c, http.StatusNotFound, errcode.ErrNotFound.Code, "日志不存在")
		return
	}

	response.Success(c, log)
}

// GetFullChain 获取 request_id 的全链路关联数据 GET /api/v1/admin/api-call-logs/:requestId/chain
// 返回：api_call_log + 关联的所有 channel_logs
func (h *ApiCallLogHandler) GetFullChain(c *gin.Context) {
	requestID := c.Param("requestId")
	if requestID == "" {
		response.Error(c, http.StatusBadRequest, errcode.ErrValidation)
		return
	}

	// 查询 API 调用日志
	var callLog model.ApiCallLog
	h.db.WithContext(c.Request.Context()).Where("request_id = ?", requestID).First(&callLog)

	// 查询关联的渠道日志（同一 request_id 可能因重试产生多条）
	var channelLogs []model.ChannelLog
	h.db.WithContext(c.Request.Context()).
		Where("request_id = ?", requestID).
		Order("created_at ASC").
		Find(&channelLogs)

	response.Success(c, gin.H{
		"api_call_log": callLog,
		"channel_logs": channelLogs,
		"retry_count":  len(channelLogs),
		"request_id":   requestID,
	})
}

// Replay 一键重放：管理员使用自己的 API Key 重新发起原请求，走完整计费与渠道路由
// POST /api/v1/admin/api-call-logs/:requestId/replay
// 返回：{ status_code, latency_ms, response_body, new_request_id }
func (h *ApiCallLogHandler) Replay(c *gin.Context) {
	requestID := c.Param("requestId")
	if requestID == "" {
		response.Error(c, http.StatusBadRequest, errcode.ErrValidation)
		return
	}
	if h.apiKeySvc == nil {
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, "ApiKeyService 未注入，无法重放")
		return
	}

	// 取管理员身份
	adminUserIDRaw, exists := c.Get("userId")
	if !exists {
		response.Error(c, http.StatusUnauthorized, errcode.ErrUnauthorized)
		return
	}
	adminUserID, ok := adminUserIDRaw.(uint)
	if !ok || adminUserID == 0 {
		response.Error(c, http.StatusUnauthorized, errcode.ErrUnauthorized)
		return
	}

	ctx := c.Request.Context()

	// 查询原日志
	var origLog model.ApiCallLog
	if err := h.db.WithContext(ctx).Where("request_id = ?", requestID).First(&origLog).Error; err != nil {
		response.ErrorMsg(c, http.StatusNotFound, errcode.ErrNotFound.Code, "原日志不存在")
		return
	}
	if origLog.RequestBody == "" {
		response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrValidation.Code, "原日志未存储请求体，无法重放")
		return
	}
	if origLog.Endpoint != "/v1/chat/completions" {
		response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrValidation.Code, "仅支持重放 /v1/chat/completions 端点")
		return
	}

	// 解析请求体为通用 map，以便修改 stream 字段
	var bodyMap map[string]interface{}
	if err := json.Unmarshal([]byte(origLog.RequestBody), &bodyMap); err != nil {
		// 兼容旧日志：上线完整 JSON 存储之前的记录可能只保存了消息摘要（非 JSON），无法重放
		trimmed := origLog.RequestBody
		if len(trimmed) > 80 {
			trimmed = trimmed[:80] + "..."
		}
		response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrValidation.Code,
			"该日志的请求体不是完整 JSON，可能是旧版本（仅存储了最后一条消息摘要）记录，无法重放。请使用本次更新后新产生的日志进行重放。存储内容预览: "+trimmed)
		return
	}
	// 强制非流式，便于查看返回
	bodyMap["stream"] = false
	delete(bodyMap, "stream_options")

	// 取管理员第一个可用 API Key（有 KeyEncrypted 才能解密）
	var adminKey model.ApiKey
	if err := h.db.WithContext(ctx).
		Where("user_id = ? AND is_active = ? AND key_encrypted <> ''", adminUserID, true).
		Order("id ASC").
		First(&adminKey).Error; err != nil {
		response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrValidation.Code,
			"未找到可重放的 API Key，请先在"+
				"「API Key 管理」中创建一个启用中的 Key（需要加密存储支持）")
		return
	}

	plainKey, err := h.apiKeySvc.RevealKey(ctx, adminKey.ID, adminUserID)
	if err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, "获取 API Key 明文失败: "+err.Error())
		return
	}

	// 构造 loopback 请求
	newBody, err := json.Marshal(bodyMap)
	if err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, "序列化请求体失败: "+err.Error())
		return
	}

	port := config.Global.Server.Port
	if port == 0 {
		port = 8080
	}
	loopbackURL := fmt.Sprintf("http://127.0.0.1:%d/v1/chat/completions", port)

	reqCtx, cancel := context.WithTimeout(ctx, 120*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, loopbackURL, bytes.NewReader(newBody))
	if err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, "构造重放请求失败: "+err.Error())
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+plainKey)
	req.Header.Set("X-Replay-Source", "admin-replay")

	start := time.Now()
	client := &http.Client{Timeout: 120 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		response.ErrorMsg(c, http.StatusBadGateway, errcode.ErrInternal.Code, "重放请求失败: "+err.Error())
		return
	}
	defer resp.Body.Close()

	respBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, "读取重放响应失败: "+err.Error())
		return
	}
	latencyMs := time.Since(start).Milliseconds()

	// 尝试从响应中提取新的 request_id（OpenAI 兼容格式 id 字段）
	newRequestID := resp.Header.Get("X-Request-Id")
	if newRequestID == "" {
		var parsed map[string]interface{}
		if json.Unmarshal(respBytes, &parsed) == nil {
			if v, ok := parsed["id"].(string); ok {
				newRequestID = v
			}
		}
	}

	response.Success(c, gin.H{
		"status_code":     resp.StatusCode,
		"latency_ms":      latencyMs,
		"response_body":   string(respBytes),
		"new_request_id":  newRequestID,
		"original_request_id": requestID,
	})
}

// GetCostBreakdown 成本核查：返回一次请求的完整计费明细 + 基于当前价格的重算值
// GET /api/v1/admin/api-call-logs/:requestId/cost-breakdown
//
// 展示字段包括：
//   - 请求日志本身（用户/渠道/供应商/模型/tokens/已扣积分）
//   - 日志生成时实际扣费（cost_credits / cost_rmb）
//   - 当前模型成本价（ai_models.input_cost_rmb / input_price_per_token）
//   - 当前模型售价（model_pricings.*）
//   - 按「当前售价 × 日志 tokens」重算的应扣积分（用于核对定价变更）
//   - 平台成本（按成本价估算）、利润、偏差
//   - 人类可读的计算公式
func (h *ApiCallLogHandler) GetCostBreakdown(c *gin.Context) {
	requestID := c.Param("requestId")
	if requestID == "" {
		response.Error(c, http.StatusBadRequest, errcode.ErrValidation)
		return
	}
	ctx := c.Request.Context()

	var log model.ApiCallLog
	if err := h.db.WithContext(ctx).Where("request_id = ?", requestID).First(&log).Error; err != nil {
		response.ErrorMsg(c, http.StatusNotFound, errcode.ErrNotFound.Code, "日志不存在")
		return
	}

	// 关联用户
	var user model.User
	_ = h.db.WithContext(ctx).Select("id, email, name, role, tenant_id").
		Where("id = ?", log.UserID).First(&user).Error

	// 查找模型（按 request_model 优先匹配，fallback 到 actual_model）
	modelName := log.ActualModel
	if modelName == "" {
		modelName = log.RequestModel
	}
	var aiModel model.AIModel
	modelFound := false
	if modelName != "" {
		if err := h.db.WithContext(ctx).
			Where("model_name = ?", modelName).
			Order("id ASC").
			First(&aiModel).Error; err == nil {
			modelFound = true
		}
	}

	// 关联供应商名称
	supplierName := log.SupplierName
	if modelFound {
		var sup model.Supplier
		if err := h.db.WithContext(ctx).Select("id, name").Where("id = ?", aiModel.SupplierID).First(&sup).Error; err == nil {
			supplierName = sup.Name
		}
	}

	// 当前售价（ModelPricing）
	var mp model.ModelPricing
	pricingFound := false
	if modelFound {
		if err := h.db.WithContext(ctx).
			Where("model_id = ?", aiModel.ID).
			Order("effective_from DESC, id DESC").
			First(&mp).Error; err == nil {
			pricingFound = true
		}
	}

	// ─── 重算当前成本（仅对 per_million_tokens 精确；其它单位提示无法重算） ───
	// 积分计算：cost = price_per_million * tokens / 1_000_000
	prompt := int64(log.PromptTokens)
	completion := int64(log.CompletionTokens)

	// 成本价兜底：DB 仅写入 *_cost_rmb 时（如价格爬虫），由 RMB 反推积分单价
	// 1 RMB = 10000 积分（1 积分 = 0.0001 元）
	inputCostCredits := aiModel.InputPricePerToken
	if inputCostCredits == 0 && aiModel.InputCostRMB > 0 {
		inputCostCredits = int64(aiModel.InputCostRMB * 10000)
	}
	outputCostCredits := aiModel.OutputPricePerToken
	if outputCostCredits == 0 && aiModel.OutputCostRMB > 0 {
		outputCostCredits = int64(aiModel.OutputCostRMB * 10000)
	}

	// ─── 阶梯价格覆盖 ───
	// 若日志记录了命中的阶梯下标，从 ModelPricing.PriceTiers 取阶梯售价覆盖基础价格
	// 这样"重算"结果才能正确反映用户实际被计费的阶梯价格
	effectiveInputPricePerToken := mp.InputPricePerToken
	effectiveOutputPricePerToken := mp.OutputPricePerToken
	effectiveInputPriceRMB := mp.InputPriceRMB
	effectiveOutputPriceRMB := mp.OutputPriceRMB
	if pricingFound && log.MatchedPriceTierIdx >= 0 {
		var tierData model.PriceTiersData
		if len(mp.PriceTiers) > 0 {
			if jerr := json.Unmarshal(mp.PriceTiers, &tierData); jerr == nil &&
				log.MatchedPriceTierIdx < len(tierData.Tiers) {
				tier := tierData.Tiers[log.MatchedPriceTierIdx]
				if tier.SellingInputPrice != nil {
					effectiveInputPriceRMB = *tier.SellingInputPrice
					effectiveInputPricePerToken = int64(*tier.SellingInputPrice * 10000)
				}
				if tier.SellingOutputPrice != nil {
					effectiveOutputPriceRMB = *tier.SellingOutputPrice
					effectiveOutputPricePerToken = int64(*tier.SellingOutputPrice * 10000)
				}
			}
		}
	}

	// ─── 阶梯价格明细（用于前端展示所有阶梯的成本价 vs 售价） ───
	// 格式：[{tier_name, tier_idx, input_min, input_max(-1=∞), cost_input_rmb, cost_output_rmb,
	//          sell_input_rmb, sell_output_rmb, is_matched}]
	type TierDetailItem struct {
		TierName      string  `json:"tier_name"`
		TierIdx       int     `json:"tier_idx"`
		InputMin      int64   `json:"input_min"`
		InputMax      int64   `json:"input_max"` // -1 表示无上限
		CostInputRMB  float64 `json:"cost_input_rmb"`
		CostOutputRMB float64 `json:"cost_output_rmb"`
		SellInputRMB  float64 `json:"sell_input_rmb"`
		SellOutputRMB float64 `json:"sell_output_rmb"`
		IsMatched     bool    `json:"is_matched"`
	}
	var priceTiersDetail []TierDetailItem
	if pricingFound {
		var mpTierData model.PriceTiersData
		if len(mp.PriceTiers) > 0 {
			_ = json.Unmarshal(mp.PriceTiers, &mpTierData)
		}
		// 同时解析 aiModel.PriceTiers 作为供应商成本阶梯（可能为空）
		var costTierData model.PriceTiersData
		if len(aiModel.PriceTiers) > 0 {
			_ = json.Unmarshal(aiModel.PriceTiers, &costTierData)
		}
		if len(mpTierData.Tiers) > 0 {
			priceTiersDetail = make([]TierDetailItem, 0, len(mpTierData.Tiers))
			for i, tier := range mpTierData.Tiers {
				// 确定成本价：优先用 aiModel 的成本阶梯（对应下标），否则用 flat 成本价
				costIn := aiModel.InputCostRMB
				costOut := aiModel.OutputCostRMB
				if i < len(costTierData.Tiers) {
					if costTierData.Tiers[i].InputPrice > 0 {
						costIn = costTierData.Tiers[i].InputPrice
					}
					if costTierData.Tiers[i].OutputPrice > 0 {
						costOut = costTierData.Tiers[i].OutputPrice
					}
				}
				// 确定售价：优先 SellingInputPrice，否则 InputPrice，再 fallback 基础价
				sellIn := mp.InputPriceRMB
				if tier.SellingInputPrice != nil {
					sellIn = *tier.SellingInputPrice
				} else if tier.InputPrice > 0 {
					sellIn = tier.InputPrice
				}
				sellOut := mp.OutputPriceRMB
				if tier.SellingOutputPrice != nil {
					sellOut = *tier.SellingOutputPrice
				} else if tier.OutputPrice > 0 {
					sellOut = tier.OutputPrice
				}
				// 确定范围上限（-1 = ∞）
				inputMax := int64(-1)
				if tier.InputMax != nil {
					inputMax = *tier.InputMax
				}
				priceTiersDetail = append(priceTiersDetail, TierDetailItem{
					TierName:      tier.Name,
					TierIdx:       i,
					InputMin:      tier.InputMin,
					InputMax:      inputMax,
					CostInputRMB:  costIn,
					CostOutputRMB: costOut,
					SellInputRMB:  sellIn,
					SellOutputRMB: sellOut,
					IsMatched:     i == log.MatchedPriceTierIdx,
				})
			}
		}
	}

	var recomputedInputCost, recomputedOutputCost, recomputedTotal int64
	var platformCostCredits int64
	formulaApplicable := false

	// 缓存拆分重算（当日志有缓存用量且模型支持缓存时按比率重算用户侧扣费）
	cacheReadTokens := int64(log.CacheReadTokens)
	cacheWriteTokens := int64(log.CacheWriteTokens)
	var (
		cacheReadPerMillion  int64
		cacheWritePerMillion int64
		cacheReadCost        int64
		cacheWriteCost       int64
		regularInputTokens   int64
		regularInputCost     int64
		cacheReadRatio       float64
		cacheWriteRatio      float64
	)

	if pricingFound && (aiModel.PricingUnit == "" || aiModel.PricingUnit == "per_million_tokens") {
		// 含缓存用量时，按比率拆分用户侧售价扣费
		if aiModel.SupportsCache && aiModel.CacheMechanism != "none" && aiModel.CacheMechanism != "" &&
			(cacheReadTokens > 0 || cacheWriteTokens > 0) && aiModel.InputCostRMB > 0 {
			ratio := func(cachePriceRMB, fallback float64) float64 {
				if cachePriceRMB > 0 {
					return cachePriceRMB / aiModel.InputCostRMB
				}
				return fallback
			}
			switch aiModel.CacheMechanism {
			case "both":
				if cacheWriteTokens > 0 {
					cacheReadRatio = ratio(aiModel.CacheExplicitInputPriceRMB, 0.10)
				} else {
					cacheReadRatio = ratio(aiModel.CacheInputPriceRMB, 0.20)
				}
				cacheWriteRatio = ratio(aiModel.CacheWritePriceRMB, 1.25)
			case "explicit":
				cacheReadRatio = ratio(aiModel.CacheInputPriceRMB, 0.10)
				cacheWriteRatio = ratio(aiModel.CacheWritePriceRMB, 1.25)
			default: // auto
				cacheReadRatio = ratio(aiModel.CacheInputPriceRMB, 0.50)
				cacheWriteRatio = 1.0
			}
			cacheReadPerMillion = int64(float64(effectiveInputPricePerToken) * cacheReadRatio)
			cacheWritePerMillion = int64(float64(effectiveInputPricePerToken) * cacheWriteRatio)
			regularInputTokens = prompt - cacheReadTokens - cacheWriteTokens
			if regularInputTokens < 0 {
				regularInputTokens = 0
			}
			regularInputCost = effectiveInputPricePerToken * regularInputTokens / 1_000_000
			cacheReadCost = cacheReadPerMillion * cacheReadTokens / 1_000_000
			cacheWriteCost = cacheWritePerMillion * cacheWriteTokens / 1_000_000
			recomputedInputCost = regularInputCost + cacheReadCost + cacheWriteCost
		} else {
			regularInputTokens = prompt
			regularInputCost = effectiveInputPricePerToken * prompt / 1_000_000
			recomputedInputCost = regularInputCost
		}
		recomputedOutputCost = effectiveOutputPricePerToken * completion / 1_000_000
		recomputedTotal = recomputedInputCost + recomputedOutputCost
		if recomputedTotal == 0 && (prompt > 0 || completion > 0) {
			recomputedTotal = 1 // 保底 1 积分，与计价引擎逻辑一致
		}
		platformCostCredits = (inputCostCredits*prompt + outputCostCredits*completion) / 1_000_000
		formulaApplicable = true
	}

	deviation := log.CostCredits - recomputedTotal
	profitCredits := log.CostCredits - platformCostCredits

	// 动态重算缓存节省金额（比较"全部按常规价"与"实际缓存拆分"的差值）
	// 日志中存储的 CacheSavingsRMB 可能为 0（旧日志 / 手动插入），此处基于当前价格重算
	computedCacheSavingsRMB := log.CacheSavingsRMB
	if formulaApplicable && (cacheReadTokens > 0 || cacheWriteTokens > 0) {
		noCacheInputCost := effectiveInputPricePerToken * prompt / 1_000_000
		savedCredits := noCacheInputCost - recomputedInputCost
		if savedCredits > 0 {
			computedCacheSavingsRMB = float64(savedCredits) / 10000.0
		}
	}

	// 人类可读公式（中文）
	formula := ""
	if formulaApplicable {
		// 阶梯价格说明前缀
		tierPrefix := ""
		if log.MatchedPriceTierIdx >= 0 && log.MatchedPriceTier != "" {
			tierPrefix = fmt.Sprintf("【命中阶梯】%s（下标 #%d），输入售价 %.4f 元/百万\n",
				log.MatchedPriceTier, log.MatchedPriceTierIdx, effectiveInputPriceRMB)
		}

		if cacheReadTokens > 0 || cacheWriteTokens > 0 {
			noCacheInputCost := effectiveInputPricePerToken * prompt / 1_000_000
			savedCredits := noCacheInputCost - recomputedInputCost
			savingsLine := ""
			if savedCredits > 0 {
				savingsLine = fmt.Sprintf("\n缓存节省 = %d（无缓存） - %d（实际）= %d 积分（%.4f 元）",
					noCacheInputCost, recomputedInputCost, savedCredits, float64(savedCredits)/10000.0)
			}
			formula = tierPrefix + fmt.Sprintf(
				"输入成本（按缓存拆分）：\n"+
					"  · 常规输入 = %d × %d ÷ 1,000,000 = %d 积分\n"+
					"  · 缓存命中 = %d（= %d × %.2f 比率） × %d ÷ 1,000,000 = %d 积分\n"+
					"  · 缓存写入 = %d（= %d × %.2f 比率） × %d ÷ 1,000,000 = %d 积分\n"+
					"  · 输入合计 = %d + %d + %d = %d 积分\n"+
					"输出成本 = %d × %d ÷ 1,000,000 = %d 积分\n"+
					"合计 = %d + %d = %d 积分（%.4f 元）%s",
				effectiveInputPricePerToken, regularInputTokens, regularInputCost,
				cacheReadPerMillion, effectiveInputPricePerToken, cacheReadRatio, cacheReadTokens, cacheReadCost,
				cacheWritePerMillion, effectiveInputPricePerToken, cacheWriteRatio, cacheWriteTokens, cacheWriteCost,
				regularInputCost, cacheReadCost, cacheWriteCost, recomputedInputCost,
				effectiveOutputPricePerToken, completion, recomputedOutputCost,
				recomputedInputCost, recomputedOutputCost, recomputedTotal,
				float64(recomputedTotal)/10000.0,
				savingsLine,
			)
		} else {
			formula = tierPrefix + fmt.Sprintf(
				"输入成本 = %d（每百万积分） × %d（输入tokens） ÷ 1,000,000 = %d 积分\n"+
					"输出成本 = %d（每百万积分） × %d（输出tokens） ÷ 1,000,000 = %d 积分\n"+
					"合计 = %d + %d = %d 积分（%.4f 元）",
				effectiveInputPricePerToken, prompt, recomputedInputCost,
				effectiveOutputPricePerToken, completion, recomputedOutputCost,
				recomputedInputCost, recomputedOutputCost, recomputedTotal,
				float64(recomputedTotal)/10000.0,
			)
		}
	} else if modelFound {
		formula = fmt.Sprintf("模型计费单位为 %s，非按百万 tokens 计费，公式无法线性重算。", aiModel.PricingUnit)
	} else {
		formula = "未在 ai_models 表中找到该模型，无法执行价格重算。"
	}

	payload := gin.H{
		"log":        log,
		"user_email": user.Email,
		"user_name":  user.Name,
		"user_role":  user.Role,

		"model_found":   modelFound,
		"pricing_found": pricingFound,
		"model_id":      aiModel.ID,
		"supplier_id":   aiModel.SupplierID,
		"supplier_name": supplierName,
		"model_name":    aiModel.ModelName,
		"display_name":  aiModel.DisplayName,
		"pricing_unit":  aiModel.PricingUnit,

		// 成本价（ai_models）- 平台向供应商的成本
		// 兜底：若 *_price_per_token 缺失但 *_cost_rmb 有值，反算积分单价
		"current_input_cost_per_million":  inputCostCredits,
		"current_output_cost_per_million": outputCostCredits,
		"current_input_cost_rmb":          aiModel.InputCostRMB,
		"current_output_cost_rmb":         aiModel.OutputCostRMB,

		// 售价（model_pricings）- 平台对用户的定价（含阶梯覆盖）
		"current_input_price_per_million":  effectiveInputPricePerToken,
		"current_output_price_per_million": effectiveOutputPricePerToken,
		"current_input_price_rmb":          effectiveInputPriceRMB,
		"current_output_price_rmb":         effectiveOutputPriceRMB,
		"pricing_effective_from":           mp.EffectiveFrom,
		// 所有阶梯的成本价 vs 售价明细（nil = 无阶梯定价，仅单价）
		"price_tiers_detail": priceTiersDetail,

		// 重算值
		"recomputed_input_cost":     recomputedInputCost,
		"recomputed_output_cost":    recomputedOutputCost,
		"recomputed_total_cost":     recomputedTotal,
		"recomputed_total_cost_rmb": float64(recomputedTotal) / 10000.0,

		// 平台成本与利润估算
		"platform_cost_credits": platformCostCredits,
		"platform_cost_rmb":     float64(platformCostCredits) / 10000.0,
		"profit_credits":        profitCredits,
		"profit_rmb":            float64(profitCredits) / 10000.0,

		// 与日志记录值的偏差
		"recorded_cost_credits": log.CostCredits,
		"recorded_cost_rmb":     log.CostRMB,
		"deviation_credits":     deviation,
		"deviation_rmb":         float64(deviation) / 10000.0,
		"cost_match":            deviation == 0 || !formulaApplicable,

		// 缓存定价明细
		"supports_cache":                 aiModel.SupportsCache,
		"cache_mechanism":                aiModel.CacheMechanism,
		"cache_read_tokens":              log.CacheReadTokens,
		"cache_write_tokens":             log.CacheWriteTokens,
		"cache_savings_rmb":              computedCacheSavingsRMB,
		"cache_input_price_rmb":          aiModel.CacheInputPriceRMB,
		"cache_explicit_input_price_rmb": aiModel.CacheExplicitInputPriceRMB,
		"cache_write_price_rmb":          aiModel.CacheWritePriceRMB,
		"cache_read_ratio":               cacheReadRatio,
		"cache_write_ratio":              cacheWriteRatio,
		"cache_read_per_million":         cacheReadPerMillion,
		"cache_write_per_million":        cacheWritePerMillion,
		"cache_read_cost":                cacheReadCost,
		"cache_write_cost":               cacheWriteCost,
		"regular_input_tokens":           regularInputTokens,
		"regular_input_cost":             regularInputCost,

		// 多级定价命中信息
		"matched_price_tier":     log.MatchedPriceTier,
		"matched_price_tier_idx": log.MatchedPriceTierIdx,
		"discount_info":          h.resolvePricingLayers(ctx, log.UserID, aiModel.ID, user.TenantID),

		"formula": formula,
	}

	response.Success(c, payload)
}

// resolvePricingLayers 解析该用户/模型组合命中的多级定价链路
// 返回 [{layer, detail}] 供前端展示：
//   - agent_custom: 该租户针对该模型的自定义定价
//   - level_discount: 该用户会员等级的折扣率
//   - platform: 兜底平台售价
func (h *ApiCallLogHandler) resolvePricingLayers(ctx context.Context, userID, modelID, tenantID uint) []map[string]interface{} {
	layers := make([]map[string]interface{}, 0, 3)

	// 代理商自定义定价
	if tenantID > 0 && modelID > 0 {
		var ap model.AgentPricing
		if err := h.db.WithContext(ctx).
			Where("tenant_id = ? AND model_id = ?", tenantID, modelID).
			First(&ap).Error; err == nil && ap.PricingType != "INHERIT" {
			layer := map[string]interface{}{
				"layer":        "agent_custom",
				"label":        "代理商自定义定价",
				"pricing_type": ap.PricingType,
			}
			if ap.InputPrice != nil {
				layer["input_price"] = *ap.InputPrice
			}
			if ap.OutputPrice != nil {
				layer["output_price"] = *ap.OutputPrice
			}
			if ap.MarkupRate != nil {
				layer["markup_rate"] = *ap.MarkupRate
			}
			if ap.DiscountRate != nil {
				layer["discount_rate"] = *ap.DiscountRate
			}
			layers = append(layers, layer)
		}
	}

	// 会员等级折扣（基于 user.member_level）
	if userID > 0 {
		var mlevel struct {
			LevelCode string
			Discount  float64
		}
		// user_balances.member_level_id → member_levels.model_discount
		_ = h.db.WithContext(ctx).Raw(`
			SELECT ml.level_code AS level_code, COALESCE(ml.model_discount, 1.0) AS discount
			FROM user_balances ub
			LEFT JOIN member_levels ml ON ml.id = ub.member_level_id AND ml.deleted_at IS NULL
			WHERE ub.user_id = ? LIMIT 1
		`, userID).Scan(&mlevel).Error
		if mlevel.LevelCode != "" && mlevel.Discount > 0 && mlevel.Discount < 1.0 {
			layers = append(layers, map[string]interface{}{
				"layer":         "member_level",
				"label":         "会员等级折扣",
				"level_code":    mlevel.LevelCode,
				"discount_rate": mlevel.Discount,
			})
		}
	}

	// 平台基础售价（兜底）
	layers = append(layers, map[string]interface{}{
		"layer": "platform",
		"label": "平台基础售价",
	})

	return layers
}

// Summary 聚合统计：与 List 接受相同筛选参数，返回当前筛选条件下的汇总数据
// GET /api/v1/admin/api-call-logs/summary
func (h *ApiCallLogHandler) Summary(c *gin.Context) {
	query := h.db.WithContext(c.Request.Context()).
		Table("api_call_logs AS l").
		Joins("LEFT JOIN users AS u ON u.id = l.user_id")

	if rid := c.Query("request_id"); rid != "" {
		query = query.Where("l.request_id LIKE ?", "%"+rid+"%")
	}
	if uid := c.Query("user_id"); uid != "" {
		query = query.Where("l.user_id = ?", uid)
	}
	if ue := c.Query("user_email"); ue != "" {
		query = query.Where("u.email LIKE ?", "%"+ue+"%")
	}
	if m := c.Query("model"); m != "" {
		query = query.Where("l.request_model LIKE ?", "%"+m+"%")
	}
	if s := c.Query("status"); s != "" {
		query = query.Where("l.status = ?", s)
	}
	if cid := c.Query("channel_id"); cid != "" {
		query = query.Where("l.channel_id = ?", cid)
	}
	if sn := c.Query("supplier_name"); sn != "" {
		supplierSubquery := `(SELECT s.name FROM ai_models m
				LEFT JOIN suppliers s ON s.id = m.supplier_id AND s.deleted_at IS NULL
				WHERE m.model_name = COALESCE(NULLIF(l.actual_model,''), l.request_model)
				  AND m.deleted_at IS NULL
				LIMIT 1)`
		query = query.Where("l.supplier_name LIKE ? OR "+supplierSubquery+" LIKE ?", "%"+sn+"%", "%"+sn+"%")
	}
	if sd := c.Query("start_date"); sd != "" {
		query = query.Where("l.created_at >= ?", sd)
	}
	if ed := c.Query("end_date"); ed != "" {
		query = query.Where("l.created_at <= ?", ed+" 23:59:59")
	}
	if ep := c.Query("endpoint"); ep != "" {
		query = query.Where("l.endpoint = ?", ep)
	}
	if minLatency := c.Query("min_latency_ms"); minLatency != "" {
		query = query.Where("l.total_latency_ms >= ?", minLatency)
	}
	if cmin := c.Query("cost_min"); cmin != "" {
		query = query.Where("l.cost_credits >= ?", cmin)
	}
	if cmax := c.Query("cost_max"); cmax != "" {
		query = query.Where("l.cost_credits <= ?", cmax)
	}
	if c.Query("errors_only") == "true" {
		query = query.Where("l.status != ?", "success")
	}

	var result struct {
		TotalRequests    int64   `json:"total_requests"`
		TotalPrompt      int64   `json:"total_prompt_tokens"`
		TotalCompletion  int64   `json:"total_completion_tokens"`
		TotalTokens      int64   `json:"total_tokens"`
		TotalCostCredits int64   `json:"total_cost_credits"`
		TotalCostRMB     float64 `json:"total_cost_rmb"`
	}
	if err := query.Select(
		"COUNT(*) AS total_requests," +
			"COALESCE(SUM(l.prompt_tokens),0) AS total_prompt," +
			"COALESCE(SUM(l.completion_tokens),0) AS total_completion," +
			"COALESCE(SUM(l.total_tokens),0) AS total_tokens," +
			"COALESCE(SUM(l.cost_credits),0) AS total_cost_credits," +
			"COALESCE(SUM(l.cost_rmb),0) AS total_cost_rmb",
	).Scan(&result).Error; err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, err.Error())
		return
	}
	response.Success(c, result)
}
