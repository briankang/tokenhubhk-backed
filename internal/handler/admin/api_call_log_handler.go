package admin

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"tokenhub-server/internal/config"
	"tokenhub-server/internal/model"
	"tokenhub-server/internal/pkg/errcode"
	"tokenhub-server/internal/pkg/response"
	"tokenhub-server/internal/service/apikey"
	referralsvc "tokenhub-server/internal/service/referral"
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
	rg.GET("/api-call-logs/reconciliation", h.Reconciliation)
	rg.GET("/api-call-logs/reconciliation/export", h.ExportReconciliationCSV)
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
//
//	supplier_name, start_date, end_date, endpoint, min_latency_ms,
//	errors_only, cost_min, cost_max
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
	if bs := c.Query("billing_status"); bs != "" {
		query = query.Where("l.billing_status = ?", bs)
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
	// 仅显示应用了特殊折扣的调用
	if c.Query("user_discount_only") == "true" {
		query = query.Where("l.user_discount_id IS NOT NULL AND l.user_discount_id > 0")
	}
	if c.Query("under_collected_only") == "true" {
		query = query.Where("l.under_collected_credits > 0")
	}
	if c.Query("missing_snapshot_only") == "true" {
		query = query.Where("(l.billing_snapshot IS NULL OR CAST(l.billing_snapshot AS CHAR) = '' OR CAST(l.billing_snapshot AS CHAR) = 'null')")
	}
	if c.Query("negative_profit_only") == "true" {
		query = query.Where("(CASE WHEN COALESCE(l.actual_cost_credits,0) > 0 THEN l.actual_cost_credits WHEN COALESCE(l.billing_status,'settled') = 'settled' THEN l.cost_credits ELSE 0 END)/10000.0 - COALESCE(l.platform_cost_rmb,0) < 0")
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
		"status_code":         resp.StatusCode,
		"latency_ms":          latencyMs,
		"response_body":       string(respBytes),
		"new_request_id":      newRequestID,
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
	billingSnapshot := map[string]interface{}{}
	snapshotFound := false
	if len(log.BillingSnapshot) > 0 && string(log.BillingSnapshot) != "null" {
		if err := json.Unmarshal(log.BillingSnapshot, &billingSnapshot); err == nil {
			snapshotFound = true
		}
	}
	snapshotInt64 := func(key string) int64 {
		v, ok := billingSnapshot[key]
		if !ok || v == nil {
			return 0
		}
		switch n := v.(type) {
		case float64:
			return int64(n)
		case int64:
			return n
		case int:
			return int64(n)
		case json.Number:
			i, _ := n.Int64()
			return i
		default:
			return 0
		}
	}
	snapshotFloat64 := func(key string) float64 {
		v, ok := billingSnapshot[key]
		if !ok || v == nil {
			return 0
		}
		switch n := v.(type) {
		case float64:
			return n
		case int64:
			return float64(n)
		case int:
			return float64(n)
		case json.Number:
			f, _ := n.Float64()
			return f
		default:
			return 0
		}
	}

	var user model.User
	_ = h.db.WithContext(ctx).Select("id, email, name, tenant_id").
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
		// 思考模式输出价（0 = 不区分，与非思考同价）
		CostOutputThinkingRMB float64 `json:"cost_output_thinking_rmb,omitempty"`
		SellInputRMB          float64 `json:"sell_input_rmb"`
		SellOutputRMB         float64 `json:"sell_output_rmb"`
		SellOutputThinkingRMB float64 `json:"sell_output_thinking_rmb,omitempty"`
		IsMatched             bool    `json:"is_matched"`
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
				costOutThinking := aiModel.OutputCostThinkingRMB // 顶层 fallback
				if i < len(costTierData.Tiers) {
					if costTierData.Tiers[i].InputPrice > 0 {
						costIn = costTierData.Tiers[i].InputPrice
					}
					if costTierData.Tiers[i].OutputPrice > 0 {
						costOut = costTierData.Tiers[i].OutputPrice
					}
					if costTierData.Tiers[i].OutputPriceThinking > 0 {
						costOutThinking = costTierData.Tiers[i].OutputPriceThinking
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
				// 思考模式售价（选择链路与 resolveThinkingOutputSellRMB 保持一致）
				var sellOutThinking float64
				if tier.SellingOutputThinkingPrice != nil && *tier.SellingOutputThinkingPrice > 0 {
					sellOutThinking = *tier.SellingOutputThinkingPrice
				} else if costOutThinking > 0 && costOut > 0 && sellOut > 0 {
					// 按 (tier 售价/tier 成本) 比例缩放成本→售价
					sellOutThinking = costOutThinking * (sellOut / costOut)
				} else if mp.OutputPriceThinkingRMB > 0 {
					sellOutThinking = mp.OutputPriceThinkingRMB
				}
				// 确定范围上限（-1 = ∞）
				inputMax := int64(-1)
				if tier.InputMax != nil {
					inputMax = *tier.InputMax
				}
				priceTiersDetail = append(priceTiersDetail, TierDetailItem{
					TierName:              tier.Name,
					TierIdx:               i,
					InputMin:              tier.InputMin,
					InputMax:              inputMax,
					CostInputRMB:          costIn,
					CostOutputRMB:         costOut,
					CostOutputThinkingRMB: costOutThinking,
					SellInputRMB:          sellIn,
					SellOutputRMB:         sellOut,
					SellOutputThinkingRMB: sellOutThinking,
					IsMatched:             i == log.MatchedPriceTierIdx,
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
		// 思考模式：若本次请求 thinking_mode=true 且模型有思考售价，按思考售价重算输出
		// 优先级与 resolveThinkingOutputSellRMB 一致：阶梯 selling → 阶梯 cost×ratio → mp thinking → top-level × ratio
		outputPricePerTokenForRecompute := effectiveOutputPricePerToken
		if log.ThinkingMode {
			var thinkingSellRMB float64
			if log.MatchedPriceTierIdx >= 0 && len(priceTiersDetail) > log.MatchedPriceTierIdx {
				thinkingSellRMB = priceTiersDetail[log.MatchedPriceTierIdx].SellOutputThinkingRMB
			}
			if thinkingSellRMB == 0 && mp.OutputPriceThinkingRMB > 0 {
				thinkingSellRMB = mp.OutputPriceThinkingRMB
			}
			if thinkingSellRMB == 0 && aiModel.OutputCostThinkingRMB > 0 && aiModel.OutputCostRMB > 0 && mp.OutputPriceRMB > 0 {
				ratio := mp.OutputPriceRMB / aiModel.OutputCostRMB
				thinkingSellRMB = aiModel.OutputCostThinkingRMB * ratio
			}
			if thinkingSellRMB > 0 {
				outputPricePerTokenForRecompute = int64(thinkingSellRMB * 10000) // RMB→credits/M
			}
		}
		recomputedOutputCost = outputPricePerTokenForRecompute * completion / 1_000_000
		recomputedTotal = recomputedInputCost + recomputedOutputCost
		if recomputedTotal == 0 && (prompt > 0 || completion > 0) {
			recomputedTotal = 1 // 保底 1 积分，与计价引擎逻辑一致
		}
		platformCostCredits = (inputCostCredits*prompt + outputCostCredits*completion) / 1_000_000
		formulaApplicable = true
	}

	deviation := log.CostCredits - recomputedTotal
	profitCredits := log.CostCredits - platformCostCredits
	if snapshotFound {
		if v := snapshotInt64("input_cost_credits"); v > 0 {
			recomputedInputCost = v
		}
		if v := snapshotInt64("output_cost_credits"); v > 0 {
			recomputedOutputCost = v
		}
		if v := snapshotInt64("total_cost_credits"); v > 0 {
			recomputedTotal = v
		}
		if v := snapshotInt64("platform_cost_credits"); v > 0 {
			platformCostCredits = v
		}
		deviation = log.CostCredits - recomputedTotal
		profitCredits = log.ActualCostCredits - platformCostCredits
		if log.ActualCostCredits == 0 && log.CostCredits > 0 && log.BillingStatus == "settled" {
			profitCredits = log.CostCredits - platformCostCredits
		}
	}
	actualRevenueCredits := log.ActualCostCredits
	if actualRevenueCredits == 0 && log.CostCredits > 0 && (log.BillingStatus == "" || log.BillingStatus == "settled") {
		actualRevenueCredits = log.CostCredits
	}
	commissionInfo := h.resolveCommissionInfo(ctx, log.UserID, aiModel.ID, float64(actualRevenueCredits)/10000.0)
	commissionRMB, _ := commissionInfo["commission_rmb"].(float64)
	netProfitRMB := float64(actualRevenueCredits-platformCostCredits)/10000.0 - commissionRMB

	// ─── v4.0 用户特殊折扣命中信息 ───
	userDiscountApplied := false
	var userDiscountDetail *model.UserModelDiscount
	if log.UserDiscountID != nil && *log.UserDiscountID > 0 {
		userDiscountApplied = true
		var ud model.UserModelDiscount
		if err := h.db.WithContext(ctx).First(&ud, *log.UserDiscountID).Error; err == nil {
			userDiscountDetail = &ud
		}
	}

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
	formulaPromptTokens := prompt
	formulaCompletionTokens := completion
	if snapshotFound {
		if v := snapshotInt64("input_tokens"); v > 0 {
			formulaPromptTokens = v
		}
		if v := snapshotInt64("output_tokens"); v > 0 {
			formulaCompletionTokens = v
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
				effectiveInputPricePerToken, formulaPromptTokens, recomputedInputCost,
				effectiveOutputPricePerToken, formulaCompletionTokens, recomputedOutputCost,
				recomputedInputCost, recomputedOutputCost, recomputedTotal,
				float64(recomputedTotal)/10000.0,
			)
		}
	} else if modelFound {
		formula = fmt.Sprintf("模型计费单位为 %s，非按百万 tokens 计费，公式无法线性重算。", aiModel.PricingUnit)
	} else {
		formula = "未在 ai_models 表中找到该模型，无法执行价格重算。"
	}

	// v4.0: 用户特殊折扣公式行
	if userDiscountApplied && userDiscountDetail != nil {
		line := ""
		switch userDiscountDetail.PricingType {
		case "DISCOUNT":
			if userDiscountDetail.DiscountRate != nil {
				line = fmt.Sprintf("\n【特殊折扣】× %.2f（用户自定义折扣", *userDiscountDetail.DiscountRate)
			}
		case "FIXED":
			line = "\n【特殊折扣】固定价格（用户自定义"
		case "MARKUP":
			if userDiscountDetail.MarkupRate != nil {
				line = fmt.Sprintf("\n【特殊折扣】+ %.2f%%（用户自定义加价", *userDiscountDetail.MarkupRate*100)
			}
		}
		if line != "" {
			if userDiscountDetail.Note != "" {
				line += "：" + userDiscountDetail.Note + "）"
			} else {
				line += "）"
			}
			formula = line + "\n" + formula
		}
	}

	payload := gin.H{
		"log":                log,
		"user_email":         user.Email,
		"user_name":          user.Name,
		"snapshot_found":     snapshotFound,
		"billing_snapshot":   billingSnapshot,
		"snapshot_total_rmb": snapshotFloat64("total_cost_rmb"),
		"user_role":          "", // v4.0: role 字段已移除，保留 key 防止前端字段缺失告警

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
		// 思考模式输出成本/售价（0 = 不区分，与普通输出同价；>0 时可用于计算加价）
		"current_output_cost_thinking_rmb":  aiModel.OutputCostThinkingRMB,
		"current_output_price_thinking_rmb": mp.OutputPriceThinkingRMB,
		// 本次请求是否按思考模式计费（由 api_call_logs.thinking_mode 读取）
		"thinking_mode": log.ThinkingMode,

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
		"platform_cost_credits":  platformCostCredits,
		"platform_cost_rmb":      float64(platformCostCredits) / 10000.0,
		"profit_credits":         profitCredits,
		"profit_rmb":             float64(profitCredits) / 10000.0,
		"actual_revenue_credits": actualRevenueCredits,
		"actual_revenue_rmb":     float64(actualRevenueCredits) / 10000.0,
		"net_profit_rmb":         netProfitRMB,

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

		// v4.0 用户特殊折扣命中
		"user_discount_applied": userDiscountApplied,
		"user_discount_id":      log.UserDiscountID,
		"user_discount_rate":    log.UserDiscountRate,
		"user_discount_detail":  userDiscountDetail,

		// v4.3 返佣信息（本次请求产生的返佣对平台是负收益）
		"commission_info": commissionInfo,

		"formula": formula,
	}

	response.Success(c, payload)
}

// Reconciliation 返回成本分析对账报表。
// 维度包括按日、按模型、按供应商聚合的应收、实收、欠收、平台成本和毛利。
func (h *ApiCallLogHandler) Reconciliation(c *gin.Context) {
	ctx := c.Request.Context()
	base := h.db.WithContext(ctx).
		Table("api_call_logs AS l").
		Joins("LEFT JOIN users AS u ON u.id = l.user_id")

	applyFilters := func(query *gorm.DB) *gorm.DB {
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
		if bs := c.Query("billing_status"); bs != "" {
			query = query.Where("l.billing_status = ?", bs)
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
		if cmin := c.Query("cost_min"); cmin != "" {
			query = query.Where("l.cost_credits >= ?", cmin)
		}
		if cmax := c.Query("cost_max"); cmax != "" {
			query = query.Where("l.cost_credits <= ?", cmax)
		}
		if c.Query("errors_only") == "true" {
			query = query.Where("l.status != ?", "success")
		}
		if c.Query("user_discount_only") == "true" {
			query = query.Where("l.user_discount_id IS NOT NULL AND l.user_discount_id > 0")
		}
		if c.Query("under_collected_only") == "true" {
			query = query.Where("l.under_collected_credits > 0")
		}
		if c.Query("missing_snapshot_only") == "true" {
			query = query.Where("(l.billing_snapshot IS NULL OR CAST(l.billing_snapshot AS CHAR) = '' OR CAST(l.billing_snapshot AS CHAR) = 'null')")
		}
		if c.Query("negative_profit_only") == "true" {
			query = query.Where("(CASE WHEN COALESCE(l.actual_cost_credits,0) > 0 THEN l.actual_cost_credits WHEN COALESCE(l.billing_status,'settled') = 'settled' THEN l.cost_credits ELSE 0 END)/10000.0 - COALESCE(l.platform_cost_rmb,0) < 0")
		}
		return query
	}

	type totalsRow struct {
		Requests              int64   `json:"requests"`
		ChargeCredits         int64   `json:"charge_credits"`
		ChargeRMB             float64 `json:"charge_rmb"`
		ActualRevenueCredits  int64   `json:"actual_revenue_credits"`
		ActualRevenueRMB      float64 `json:"actual_revenue_rmb"`
		UnderCollectedCredits int64   `json:"under_collected_credits"`
		UnderCollectedRMB     float64 `json:"under_collected_rmb"`
		PlatformCostRMB       float64 `json:"platform_cost_rmb"`
		GrossProfitRMB        float64 `json:"gross_profit_rmb"`
		DeductFailedRequests  int64   `json:"deduct_failed_requests"`
	}
	actualCreditsExpr := "CASE WHEN COALESCE(l.actual_cost_credits,0) > 0 THEN l.actual_cost_credits WHEN COALESCE(l.billing_status,'settled') = 'settled' THEN l.cost_credits ELSE 0 END"
	selectTotals := "COUNT(*) AS requests," +
		"COALESCE(SUM(l.cost_credits),0) AS charge_credits," +
		"COALESCE(SUM(l.cost_credits),0)/10000.0 AS charge_rmb," +
		"COALESCE(SUM(" + actualCreditsExpr + "),0) AS actual_revenue_credits," +
		"COALESCE(SUM(" + actualCreditsExpr + "),0)/10000.0 AS actual_revenue_rmb," +
		"COALESCE(SUM(l.under_collected_credits),0) AS under_collected_credits," +
		"COALESCE(SUM(l.under_collected_credits),0)/10000.0 AS under_collected_rmb," +
		"COALESCE(SUM(l.platform_cost_rmb),0) AS platform_cost_rmb," +
		"(COALESCE(SUM(" + actualCreditsExpr + "),0)/10000.0 - COALESCE(SUM(l.platform_cost_rmb),0)) AS gross_profit_rmb," +
		"SUM(CASE WHEN l.billing_status = 'deduct_failed' THEN 1 ELSE 0 END) AS deduct_failed_requests"

	var totals totalsRow
	if err := applyFilters(base.Session(&gorm.Session{})).Select(selectTotals).Scan(&totals).Error; err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, err.Error())
		return
	}

	type groupRow struct {
		Key                   string  `gorm:"column:dimension_key" json:"key"`
		Requests              int64   `json:"requests"`
		ChargeCredits         int64   `json:"charge_credits"`
		ChargeRMB             float64 `json:"charge_rmb"`
		ActualRevenueCredits  int64   `json:"actual_revenue_credits"`
		ActualRevenueRMB      float64 `json:"actual_revenue_rmb"`
		UnderCollectedCredits int64   `json:"under_collected_credits"`
		UnderCollectedRMB     float64 `json:"under_collected_rmb"`
		PlatformCostRMB       float64 `json:"platform_cost_rmb"`
		GrossProfitRMB        float64 `json:"gross_profit_rmb"`
		DeductFailedRequests  int64   `json:"deduct_failed_requests"`
	}
	queryGroup := func(groupExpr, keyExpr, orderExpr string) ([]groupRow, error) {
		var rows []groupRow
		err := applyFilters(base.Session(&gorm.Session{})).
			Select(keyExpr + " AS dimension_key," + selectTotals).
			Group(groupExpr).
			Order(orderExpr).
			Limit(30).
			Scan(&rows).Error
		return rows, err
	}

	byDay, err := queryGroup("DATE(l.created_at)", "DATE(l.created_at)", "dimension_key DESC")
	if err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, err.Error())
		return
	}
	byModel, err := queryGroup("l.request_model", "COALESCE(NULLIF(l.request_model,''),'-')", "charge_credits DESC")
	if err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, err.Error())
		return
	}
	bySupplier, err := queryGroup("COALESCE(NULLIF(l.supplier_name,''),'-')", "COALESCE(NULLIF(l.supplier_name,''),'-')", "charge_credits DESC")
	if err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, err.Error())
		return
	}

	type alertItem struct {
		Type     string  `json:"type"`
		Label    string  `json:"label"`
		Count    int64   `json:"count"`
		Amount   float64 `json:"amount_rmb"`
		Severity string  `json:"severity"`
		Hint     string  `json:"hint"`
	}
	countAlert := func(where, amountExpr string) (int64, float64) {
		var row struct {
			Count  int64   `json:"count"`
			Amount float64 `json:"amount_rmb"`
		}
		selectExpr := "COUNT(*) AS count"
		if amountExpr != "" {
			selectExpr += ", COALESCE(SUM(" + amountExpr + "),0) AS amount"
		} else {
			selectExpr += ", 0 AS amount"
		}
		_ = applyFilters(base.Session(&gorm.Session{})).
			Where(where).
			Select(selectExpr).
			Scan(&row).Error
		return row.Count, row.Amount
	}
	negativeProfitExpr := "(" + actualCreditsExpr + ")/10000.0 - COALESCE(l.platform_cost_rmb,0)"
	missingSnapshotWhere := "(l.billing_snapshot IS NULL OR CAST(l.billing_snapshot AS CHAR) = '' OR CAST(l.billing_snapshot AS CHAR) = 'null')"
	deductFailedCount, deductFailedAmount := countAlert("l.billing_status = 'deduct_failed'", "l.cost_credits/10000.0")
	underCollectedCount, underCollectedAmount := countAlert("l.under_collected_credits > 0", "l.under_collected_credits/10000.0")
	missingSnapshotCount, _ := countAlert(missingSnapshotWhere, "")
	negativeProfitCount, negativeProfitAmount := countAlert(negativeProfitExpr+" < 0", "-("+negativeProfitExpr+")")
	alerts := []alertItem{
		{Type: "deduct_failed", Label: "扣费失败", Count: deductFailedCount, Amount: deductFailedAmount, Severity: "critical", Hint: "已生成调用日志但扣款未成功，需要补扣或人工核销。"},
		{Type: "under_collected", Label: "欠收", Count: underCollectedCount, Amount: underCollectedAmount, Severity: "warning", Hint: "冻结/预估金额低于最终应收，建议对余额和冻结记录做二次对账。"},
		{Type: "missing_snapshot", Label: "快照缺失", Count: missingSnapshotCount, Amount: 0, Severity: "warning", Hint: "缺少 billing_snapshot 的旧日志只能按当前价重算，历史账单可信度较低。"},
		{Type: "negative_profit", Label: "毛利为负", Count: negativeProfitCount, Amount: negativeProfitAmount, Severity: "critical", Hint: "实收低于平台成本，需检查供应商成本价、用户折扣或返佣配置。"},
	}

	response.Success(c, gin.H{
		"totals":      totals,
		"by_day":      byDay,
		"by_model":    byModel,
		"by_supplier": bySupplier,
		"alerts":      alerts,
	})
}

// ExportReconciliationCSV exports the reconciliation rows for the current filters.
func (h *ApiCallLogHandler) ExportReconciliationCSV(c *gin.Context) {
	ctx := c.Request.Context()
	base := h.db.WithContext(ctx).
		Table("api_call_logs AS l").
		Joins("LEFT JOIN users AS u ON u.id = l.user_id")
	actualCreditsExpr := "CASE WHEN COALESCE(l.actual_cost_credits,0) > 0 THEN l.actual_cost_credits WHEN COALESCE(l.billing_status,'settled') = 'settled' THEN l.cost_credits ELSE 0 END"
	applyFilters := func(query *gorm.DB) *gorm.DB {
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
		if bs := c.Query("billing_status"); bs != "" {
			query = query.Where("l.billing_status = ?", bs)
		}
		if cid := c.Query("channel_id"); cid != "" {
			query = query.Where("l.channel_id = ?", cid)
		}
		if sn := c.Query("supplier_name"); sn != "" {
			query = query.Where("l.supplier_name LIKE ?", "%"+sn+"%")
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
		if cmin := c.Query("cost_min"); cmin != "" {
			query = query.Where("l.cost_credits >= ?", cmin)
		}
		if cmax := c.Query("cost_max"); cmax != "" {
			query = query.Where("l.cost_credits <= ?", cmax)
		}
		if c.Query("errors_only") == "true" {
			query = query.Where("l.status != ?", "success")
		}
		if c.Query("user_discount_only") == "true" {
			query = query.Where("l.user_discount_id IS NOT NULL AND l.user_discount_id > 0")
		}
		if c.Query("under_collected_only") == "true" {
			query = query.Where("l.under_collected_credits > 0")
		}
		if c.Query("missing_snapshot_only") == "true" {
			query = query.Where("(l.billing_snapshot IS NULL OR CAST(l.billing_snapshot AS CHAR) = '' OR CAST(l.billing_snapshot AS CHAR) = 'null')")
		}
		if c.Query("negative_profit_only") == "true" {
			query = query.Where("(" + actualCreditsExpr + ")/10000.0 - COALESCE(l.platform_cost_rmb,0) < 0")
		}
		return query
	}
	selectTotals := "COUNT(*) AS requests," +
		"COALESCE(SUM(l.cost_credits),0)/10000.0 AS charge_rmb," +
		"COALESCE(SUM(" + actualCreditsExpr + "),0)/10000.0 AS actual_revenue_rmb," +
		"COALESCE(SUM(l.under_collected_credits),0)/10000.0 AS under_collected_rmb," +
		"COALESCE(SUM(l.platform_cost_rmb),0) AS platform_cost_rmb," +
		"(COALESCE(SUM(" + actualCreditsExpr + "),0)/10000.0 - COALESCE(SUM(l.platform_cost_rmb),0)) AS gross_profit_rmb," +
		"SUM(CASE WHEN l.billing_status = 'deduct_failed' THEN 1 ELSE 0 END) AS deduct_failed_requests"
	type exportRow struct {
		Key                  string  `gorm:"column:dimension_key"`
		Requests             int64   `gorm:"column:requests"`
		ChargeRMB            float64 `gorm:"column:charge_rmb"`
		ActualRevenueRMB     float64 `gorm:"column:actual_revenue_rmb"`
		UnderCollectedRMB    float64 `gorm:"column:under_collected_rmb"`
		PlatformCostRMB      float64 `gorm:"column:platform_cost_rmb"`
		GrossProfitRMB       float64 `gorm:"column:gross_profit_rmb"`
		DeductFailedRequests int64   `gorm:"column:deduct_failed_requests"`
	}
	queryGroup := func(groupExpr, keyExpr, orderExpr string) ([]exportRow, error) {
		var rows []exportRow
		err := applyFilters(base.Session(&gorm.Session{})).
			Select(keyExpr + " AS dimension_key," + selectTotals).
			Group(groupExpr).
			Order(orderExpr).
			Limit(500).
			Scan(&rows).Error
		return rows, err
	}
	sections := []struct {
		Name      string
		GroupExpr string
		KeyExpr   string
		OrderExpr string
	}{
		{"by_day", "DATE(l.created_at)", "DATE(l.created_at)", "dimension_key DESC"},
		{"by_model", "l.request_model", "COALESCE(NULLIF(l.request_model,''),'-')", "charge_rmb DESC"},
		{"by_supplier", "COALESCE(NULLIF(l.supplier_name,''),'-')", "COALESCE(NULLIF(l.supplier_name,''),'-')", "charge_rmb DESC"},
	}

	c.Header("Content-Type", "text/csv; charset=utf-8")
	c.Header("Content-Disposition", `attachment; filename="cost_reconciliation.csv"`)
	w := csv.NewWriter(c.Writer)
	_ = w.Write([]string{"section", "dimension", "requests", "charge_rmb", "actual_revenue_rmb", "under_collected_rmb", "platform_cost_rmb", "gross_profit_rmb", "deduct_failed_requests"})
	for _, section := range sections {
		rows, err := queryGroup(section.GroupExpr, section.KeyExpr, section.OrderExpr)
		if err != nil {
			response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, err.Error())
			return
		}
		for _, row := range rows {
			_ = w.Write([]string{
				section.Name,
				strings.TrimSpace(row.Key),
				strconv.FormatInt(row.Requests, 10),
				fmt.Sprintf("%.6f", row.ChargeRMB),
				fmt.Sprintf("%.6f", row.ActualRevenueRMB),
				fmt.Sprintf("%.6f", row.UnderCollectedRMB),
				fmt.Sprintf("%.6f", row.PlatformCostRMB),
				fmt.Sprintf("%.6f", row.GrossProfitRMB),
				strconv.FormatInt(row.DeductFailedRequests, 10),
			})
		}
	}
	w.Flush()
}

// resolveCommissionInfo 返回本次请求产生的返佣信息
// 优先走 Redis 缓存（ruleResolver.Resolve 已带缓存）；不阻塞异步佣金写入
func (h *ApiCallLogHandler) resolveCommissionInfo(ctx context.Context, consumerUserID, modelID uint, costRMB float64) map[string]interface{} {
	info := map[string]interface{}{
		"has_commission": false,
		"user_rate":      0.0,
		"platform_rate":  0.0,
		"source":         "",
		"commission_rmb": 0.0,
	}

	if consumerUserID == 0 {
		return info
	}

	// 查归因
	var attr model.ReferralAttribution
	err := h.db.WithContext(ctx).
		Where("user_id = ? AND is_valid = ?", consumerUserID, true).
		First(&attr).Error
	if err != nil {
		return info
	}

	// 归因窗口 / 解锁状态校验
	now := time.Now()
	if (!attr.ExpiresAt.IsZero() && now.After(attr.ExpiresAt)) || attr.UnlockedAt == nil {
		info["inviter_id"] = attr.InviterID
		info["unlocked"] = false
		return info
	}

	// 查邀请人
	var inviter model.User
	_ = h.db.WithContext(ctx).Select("id, email, name").First(&inviter, attr.InviterID).Error

	// 解析比例（走 RuleResolver 缓存）
	resolver := referralsvc.Default
	if resolver == nil {
		// fallback：无 resolver 时降级仅查 ReferralConfig
		var cfg model.ReferralConfig
		if qErr := h.db.WithContext(ctx).Where("is_active = ?", true).First(&cfg).Error; qErr == nil {
			info["user_rate"] = cfg.CommissionRate
			info["platform_rate"] = cfg.CommissionRate
			info["source"] = "config"
		}
		info["has_commission"] = true
		info["inviter_id"] = inviter.ID
		info["inviter_email"] = inviter.Email
		return info
	}

	resolved, _ := resolver.Resolve(ctx, attr.InviterID, modelID)
	if resolved == nil {
		return info
	}

	info["has_commission"] = resolved.Rate > 0
	info["user_rate"] = resolved.Rate
	info["platform_rate"] = resolved.PlatformRate
	info["source"] = resolved.Source
	info["commission_rmb"] = costRMB * resolved.Rate
	info["inviter_id"] = inviter.ID
	info["inviter_email"] = inviter.Email
	info["inviter_name"] = inviter.Name
	if resolved.RuleID > 0 {
		info["rule_id"] = resolved.RuleID
		// 附带规则名（供悬浮提示）
		var rule model.CommissionRule
		if rErr := h.db.WithContext(ctx).Select("id, name, note").First(&rule, resolved.RuleID).Error; rErr == nil {
			info["rule_name"] = rule.Name
			info["rule_note"] = rule.Note
		}
	}
	if resolved.OverrideID > 0 {
		info["override_id"] = resolved.OverrideID
	}
	return info
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
	if bs := c.Query("billing_status"); bs != "" {
		query = query.Where("l.billing_status = ?", bs)
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
	if c.Query("user_discount_only") == "true" {
		query = query.Where("l.user_discount_id IS NOT NULL AND l.user_discount_id > 0")
	}
	if c.Query("under_collected_only") == "true" {
		query = query.Where("l.under_collected_credits > 0")
	}
	if c.Query("missing_snapshot_only") == "true" {
		query = query.Where("(l.billing_snapshot IS NULL OR CAST(l.billing_snapshot AS CHAR) = '' OR CAST(l.billing_snapshot AS CHAR) = 'null')")
	}
	if c.Query("negative_profit_only") == "true" {
		query = query.Where("(CASE WHEN COALESCE(l.actual_cost_credits,0) > 0 THEN l.actual_cost_credits WHEN COALESCE(l.billing_status,'settled') = 'settled' THEN l.cost_credits ELSE 0 END)/10000.0 - COALESCE(l.platform_cost_rmb,0) < 0")
	}

	var result struct {
		TotalRequests         int64   `json:"total_requests"`
		TotalPrompt           int64   `json:"total_prompt_tokens"`
		TotalCompletion       int64   `json:"total_completion_tokens"`
		TotalTokens           int64   `json:"total_tokens"`
		TotalCostCredits      int64   `json:"total_cost_credits"`
		TotalCostRMB          float64 `json:"total_cost_rmb"`
		ActualRevenueCredits  int64   `json:"actual_revenue_credits"`
		ActualRevenueRMB      float64 `json:"actual_revenue_rmb"`
		UnderCollectedCredits int64   `json:"under_collected_credits"`
		UnderCollectedRMB     float64 `json:"under_collected_rmb"`
		DeductFailedRequests  int64   `json:"deduct_failed_requests"`
		PlatformCostRMB       float64 `json:"platform_cost_rmb"`
	}
	if err := query.Select(
		"COUNT(*) AS total_requests," +
			"COALESCE(SUM(l.prompt_tokens),0) AS total_prompt," +
			"COALESCE(SUM(l.completion_tokens),0) AS total_completion," +
			"COALESCE(SUM(l.total_tokens),0) AS total_tokens," +
			"COALESCE(SUM(l.cost_credits),0) AS total_cost_credits," +
			"COALESCE(SUM(l.cost_rmb),0) AS total_cost_rmb," +
			"COALESCE(SUM(CASE WHEN COALESCE(l.actual_cost_credits,0) > 0 THEN l.actual_cost_credits WHEN COALESCE(l.billing_status,'settled') = 'settled' THEN l.cost_credits ELSE 0 END),0) AS actual_revenue_credits," +
			"COALESCE(SUM(CASE WHEN COALESCE(l.actual_cost_credits,0) > 0 THEN l.actual_cost_credits WHEN COALESCE(l.billing_status,'settled') = 'settled' THEN l.cost_credits ELSE 0 END),0)/10000.0 AS actual_revenue_rmb," +
			"COALESCE(SUM(l.under_collected_credits),0) AS under_collected_credits," +
			"COALESCE(SUM(l.under_collected_credits),0)/10000.0 AS under_collected_rmb," +
			"SUM(CASE WHEN l.billing_status = 'deduct_failed' THEN 1 ELSE 0 END) AS deduct_failed_requests," +
			"COALESCE(SUM(l.platform_cost_rmb),0) AS platform_cost_rmb",
	).Scan(&result).Error; err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, err.Error())
		return
	}
	response.Success(c, result)
}
