package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"tokenhub-server/internal/model"
	"tokenhub-server/internal/pkg/errcode"
	"tokenhub-server/internal/pkg/response"
	billingsvc "tokenhub-server/internal/service/billing"
	"tokenhub-server/internal/service/pricing"
)

// CostConsistencyHandler 三方数据源一致性核对处理器（管理员专用）。
//
// 三方含义:
//
//	A. 调用扣费 = api_call_logs.cost_rmb (真实落库金额)
//	B. 实际成本 = api_call_logs.platform_cost_rmb (平台向供应商支付)
//	C. 计算器试算 = billing_quote_service.Calculate 用同一笔 usage 重新计算
//
// 三方共享同一个 pricing.PricingCalculator 底层服务,理论上一致;
// 但在以下场景仍可能漂移,本 handler 负责诊断:
//
//   - PRICE_CONFIG_CHANGED: 该请求发生后,model_pricings.updated_at 发生变化
//   - TIER_BOUNDARY:        usage 落在某个阶梯边界 ±1% 内
//   - THINKING_ROUNDING:    thinking_mode + 偏差 ≤ 1 credit (浮点舍入)
//   - CACHE_TTL_LAG:        偏差与 2min 本地缓存窗口重合 (改价后 2min 内调用)
type CostConsistencyHandler struct {
	db          *gorm.DB
	pricingCalc *pricing.PricingCalculator
	quoteSvc    *billingsvc.QuoteService
}

// NewCostConsistencyHandler 构造 handler。db 与 pricingCalc 必须非 nil。
func NewCostConsistencyHandler(db *gorm.DB, pricingCalc *pricing.PricingCalculator) *CostConsistencyHandler {
	if db == nil {
		panic("cost consistency handler: db is nil")
	}
	if pricingCalc == nil {
		panic("cost consistency handler: pricing calculator is nil")
	}
	return &CostConsistencyHandler{
		db:          db,
		pricingCalc: pricingCalc,
		quoteSvc:    billingsvc.NewQuoteService(db, pricingCalc),
	}
}

// Register 注册路由
func (h *CostConsistencyHandler) Register(rg *gin.RouterGroup) {
	rg.GET("/api-call-logs/:requestId/three-way-check", h.GetThreeWayCheck)
	rg.GET("/cost-consistency/scan", h.ScanConsistency)
}

// 一致性级别阈值(百分比)
const (
	consistencyThresholdConsistent  = 0.001 // 0.1%
	consistencyThresholdMinorDrift  = 0.05  // 5%
	tierBoundaryWindowRatio         = 0.01  // 阶梯边界 ±1%
	cacheTTLWindowSeconds           = 120   // 本地价格缓存 2 分钟
	thinkingRoundingCreditTolerance = 1     // 思考模式 ≤1 credit 视为舍入误差
)

// ThreeWayDataPoint 一个数据源的金额信息
type ThreeWayDataPoint struct {
	CostRMB     float64 `json:"cost_rmb"`
	CostCredits int64   `json:"cost_credits"`
	Source      string  `json:"source"`
}

// DriftReason 漂移原因(供前端展示)
type DriftReason struct {
	Code   string `json:"code"`
	Detail string `json:"detail"`
}

// ThreeWayDeltas 三方两两偏差(以计算器为基准)
type ThreeWayDeltas struct {
	BillingVsCalculatorPct      float64 `json:"billing_vs_calculator_pct"`
	BillingVsCalculatorCredits  int64   `json:"billing_vs_calculator_credits"`
	PlatformVsCalculatorPct     float64 `json:"platform_vs_calculator_pct"`
	PlatformVsCalculatorCredits int64   `json:"platform_vs_calculator_credits"`
	MaxDeltaPct                 float64 `json:"max_delta_pct"`
}

// ThreeWayCheckResult 三方一致性核对返回结构
type ThreeWayCheckResult struct {
	RequestID       string             `json:"request_id"`
	SnapshotTakenAt time.Time          `json:"snapshot_taken_at"`
	Billing         ThreeWayDataPoint  `json:"billing"`
	PlatformCost    ThreeWayDataPoint  `json:"platform_cost"`
	Calculator      ThreeWayDataPoint  `json:"calculator"`
	CalculatorPlatformCostRMB float64  `json:"calculator_platform_cost_rmb"`
	Deltas          ThreeWayDeltas     `json:"deltas"`
	Consensus       string             `json:"consensus"` // consistent | minor_drift | major_drift
	DriftReasons    []DriftReason      `json:"drift_reasons"`
	CalculatorError string             `json:"calculator_error,omitempty"`
}

// GetThreeWayCheck GET /api/v1/admin/api-call-logs/:requestId/three-way-check
//
// 流程:
//  1. 读取 api_call_logs(扣费 A + 平台成本 B + usage 快照)
//  2. 用同一 usage 调用 BillingQuoteService.Calculate(计算器 C)
//  3. 比对三者,识别偏差原因,输出一致性报告
func (h *CostConsistencyHandler) GetThreeWayCheck(c *gin.Context) {
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

	result := h.computeThreeWayCheck(ctx, &log)
	response.Success(c, result)
}

// computeThreeWayCheck 核心计算逻辑(独立函数便于 ScanConsistency 复用)
func (h *CostConsistencyHandler) computeThreeWayCheck(ctx context.Context, log *model.ApiCallLog) ThreeWayCheckResult {
	result := ThreeWayCheckResult{
		RequestID:       log.RequestID,
		SnapshotTakenAt: log.CreatedAt,
		Billing: ThreeWayDataPoint{
			CostRMB:     log.CostRMB,
			CostCredits: log.CostCredits,
			Source:      "api_call_logs.cost_rmb",
		},
		PlatformCost: ThreeWayDataPoint{
			CostRMB:     log.PlatformCostRMB,
			CostCredits: int64(math.Round(log.PlatformCostRMB * 10000)),
			Source:      "api_call_logs.platform_cost_rmb",
		},
		DriftReasons: []DriftReason{},
	}

	// ─── (C) 调用计算器重算 ───
	// 解析 billing_snapshot 获取原始 usage(优先)或回退到 log 字段
	usage := h.extractUsageFromLog(log)

	// 加载模型(优先 ID,回退按 model_name)
	modelID := h.resolveModelID(ctx, log)
	if modelID == 0 {
		result.CalculatorError = "model not found in ai_models"
		result.Consensus = "major_drift"
		return result
	}

	quoteReq := billingsvc.QuoteRequest{
		Scenario:     billingsvc.QuoteScenarioReplay,
		RequestID:    log.RequestID,
		ModelID:      modelID,
		UserID:       log.UserID,
		TenantID:     0, // 由 quoteSvc.loadModel 后内部回填
		AgentLevel:   0,
		Usage:        usage,
		ThinkingMode: log.ThinkingMode,
	}
	// 从 billing_snapshot 提取 dim_values (PriceMatrix 命中)
	if dimValues := h.extractDimValuesFromSnapshot(log); dimValues != nil {
		quoteReq.DimValues = dimValues
	}

	quote, err := h.quoteSvc.Calculate(ctx, quoteReq)
	if err != nil {
		result.CalculatorError = err.Error()
		result.Consensus = "major_drift"
		return result
	}

	result.Calculator = ThreeWayDataPoint{
		CostRMB:     quote.TotalRMB,
		CostCredits: quote.TotalCredits,
		Source:      "billing_quote_service.Calculate (recomputed)",
	}
	result.CalculatorPlatformCostRMB = quote.PlatformCostRMB

	// ─── 偏差计算(以计算器为基准) ───
	calcCredits := quote.TotalCredits
	billingCredits := log.CostCredits
	platformCredits := int64(math.Round(log.PlatformCostRMB * 10000))
	calcPlatformCredits := quote.PlatformCostCredits

	billingDeltaCredits := billingCredits - calcCredits
	platformDeltaCredits := platformCredits - calcPlatformCredits

	billingDeltaPct := relativeDelta(billingCredits, calcCredits)
	platformDeltaPct := relativeDelta(platformCredits, calcPlatformCredits)

	maxDelta := math.Max(math.Abs(billingDeltaPct), math.Abs(platformDeltaPct))

	result.Deltas = ThreeWayDeltas{
		BillingVsCalculatorPct:      roundPct(billingDeltaPct),
		BillingVsCalculatorCredits:  billingDeltaCredits,
		PlatformVsCalculatorPct:     roundPct(platformDeltaPct),
		PlatformVsCalculatorCredits: platformDeltaCredits,
		MaxDeltaPct:                 roundPct(maxDelta),
	}

	// ─── 漂移原因诊断 ───
	result.DriftReasons = h.diagnoseDriftReasons(ctx, log, modelID, billingDeltaCredits, usage)

	// ─── 一致性级别判定 ───
	switch {
	case maxDelta <= consistencyThresholdConsistent:
		result.Consensus = "consistent"
	case maxDelta <= consistencyThresholdMinorDrift:
		result.Consensus = "minor_drift"
	default:
		result.Consensus = "major_drift"
	}

	return result
}

// extractUsageFromLog 优先从 billing_snapshot.usage 取,回退 log 字段
func (h *CostConsistencyHandler) extractUsageFromLog(log *model.ApiCallLog) billingsvc.QuoteUsage {
	usage := billingsvc.QuoteUsage{
		InputTokens:      int(log.PromptTokens),
		OutputTokens:     int(log.CompletionTokens),
		TotalTokens:      int(log.PromptTokens) + int(log.CompletionTokens),
		CacheReadTokens:  log.CacheReadTokens,
		CacheWriteTokens: log.CacheWriteTokens,
	}

	// 从 billing_snapshot 取更精确的 usage(包含 image_count / duration_sec / char_count / call_count)
	if len(log.BillingSnapshot) > 0 && string(log.BillingSnapshot) != "null" {
		var snapshot map[string]interface{}
		if err := json.Unmarshal(log.BillingSnapshot, &snapshot); err == nil {
			if quoteRaw, ok := snapshot["quote"].(map[string]interface{}); ok {
				if usageRaw, ok := quoteRaw["usage"].(map[string]interface{}); ok {
					mergeUsageFromMap(&usage, usageRaw)
				}
			}
			if usageRaw, ok := snapshot["usage"].(map[string]interface{}); ok {
				mergeUsageFromMap(&usage, usageRaw)
			}
		}
	}
	return usage
}

func mergeUsageFromMap(u *billingsvc.QuoteUsage, m map[string]interface{}) {
	if v := mapInt(m, "input_tokens"); v > 0 {
		u.InputTokens = v
	}
	if v := mapInt(m, "output_tokens"); v > 0 {
		u.OutputTokens = v
	}
	if v := mapInt(m, "cache_read_tokens"); v > 0 {
		u.CacheReadTokens = v
	}
	if v := mapInt(m, "cache_write_tokens"); v > 0 {
		u.CacheWriteTokens = v
	}
	if v := mapInt(m, "cache_write_1h_tokens"); v > 0 {
		u.CacheWrite1hTokens = v
	}
	if v := mapInt(m, "image_count"); v > 0 {
		u.ImageCount = v
	}
	if v := mapInt(m, "char_count"); v > 0 {
		u.CharCount = v
	}
	if v := mapInt(m, "call_count"); v > 0 {
		u.CallCount = v
	}
	if v := mapFloat(m, "duration_sec"); v > 0 {
		u.DurationSec = v
	}
}

// extractDimValuesFromSnapshot 从 billing_snapshot.matched_dim_values 提取 PriceMatrix 维度
func (h *CostConsistencyHandler) extractDimValuesFromSnapshot(log *model.ApiCallLog) map[string]interface{} {
	if len(log.BillingSnapshot) == 0 || string(log.BillingSnapshot) == "null" {
		return nil
	}
	var snapshot map[string]interface{}
	if err := json.Unmarshal(log.BillingSnapshot, &snapshot); err != nil {
		return nil
	}
	if quoteRaw, ok := snapshot["quote"].(map[string]interface{}); ok {
		if dim, ok := quoteRaw["matched_dim_values"].(map[string]interface{}); ok && len(dim) > 0 {
			return dim
		}
	}
	if dim, ok := snapshot["matched_dim_values"].(map[string]interface{}); ok && len(dim) > 0 {
		return dim
	}
	return nil
}

// resolveModelID 优先按 actual_model > request_model 反查
func (h *CostConsistencyHandler) resolveModelID(ctx context.Context, log *model.ApiCallLog) uint {
	modelName := log.ActualModel
	if modelName == "" {
		modelName = log.RequestModel
	}
	if modelName == "" {
		return 0
	}
	var m model.AIModel
	if err := h.db.WithContext(ctx).
		Select("id").
		Where("model_name = ?", modelName).
		Order("id ASC").
		First(&m).Error; err != nil {
		return 0
	}
	return m.ID
}

// diagnoseDriftReasons 诊断漂移原因
func (h *CostConsistencyHandler) diagnoseDriftReasons(
	ctx context.Context,
	log *model.ApiCallLog,
	modelID uint,
	billingDeltaCredits int64,
	usage billingsvc.QuoteUsage,
) []DriftReason {
	reasons := []DriftReason{}

	absDelta := billingDeltaCredits
	if absDelta < 0 {
		absDelta = -absDelta
	}
	if absDelta == 0 {
		return reasons
	}

	// 1. PRICE_CONFIG_CHANGED:对比该日志后 model_pricings 是否更新过
	var mp model.ModelPricing
	if err := h.db.WithContext(ctx).
		Where("model_id = ?", modelID).
		Order("effective_from DESC, id DESC").
		First(&mp).Error; err == nil {
		if mp.UpdatedAt.After(log.CreatedAt) {
			elapsed := mp.UpdatedAt.Sub(log.CreatedAt).Round(time.Second)
			reasons = append(reasons, DriftReason{
				Code: "PRICE_CONFIG_CHANGED",
				Detail: fmt.Sprintf(
					"该请求发生于 %s,但 model_pricings 在 %s 后被更新(间隔 %s),计算器使用最新价",
					log.CreatedAt.Format(time.RFC3339),
					elapsed,
					elapsed,
				),
			})
		}
	}

	// 2. CACHE_TTL_LAG:本地价格缓存 2 分钟,改价后 2 分钟窗口内的请求可能用旧价
	if mp.UpdatedAt.Before(log.CreatedAt) && !mp.UpdatedAt.IsZero() {
		gap := log.CreatedAt.Sub(mp.UpdatedAt).Seconds()
		if gap > 0 && gap < cacheTTLWindowSeconds {
			reasons = append(reasons, DriftReason{
				Code: "CACHE_TTL_LAG",
				Detail: fmt.Sprintf(
					"该请求发生时距上次价格更新仅 %.0f 秒(< %d 秒缓存 TTL),可能命中本地缓存的旧价",
					gap, cacheTTLWindowSeconds,
				),
			})
		}
	}

	// 3. TIER_BOUNDARY:usage 落在阶梯边界 ±1% 内
	if mp.PriceTiers != nil {
		var tiers struct {
			Tiers []struct {
				InputMin int   `json:"input_min"`
				InputMax int64 `json:"input_max"`
				Name     string `json:"name"`
			} `json:"tiers"`
		}
		if err := json.Unmarshal(mp.PriceTiers, &tiers); err == nil {
			for _, t := range tiers.Tiers {
				if t.InputMax <= 0 {
					continue
				}
				delta := math.Abs(float64(usage.InputTokens) - float64(t.InputMax))
				window := float64(t.InputMax) * tierBoundaryWindowRatio
				if window > 0 && delta <= window {
					reasons = append(reasons, DriftReason{
						Code: "TIER_BOUNDARY",
						Detail: fmt.Sprintf(
							"输入 tokens=%d 距阶梯[%s]边界 %d 仅 %.0f tokens(±1%% 窗口),边界判定可能因缓存状态命中不同 tier",
							usage.InputTokens, t.Name, t.InputMax, delta,
						),
					})
					break
				}
			}
		}
	}

	// 4. THINKING_ROUNDING:思考模式 + 偏差 ≤ 1 credit
	if log.ThinkingMode && absDelta <= thinkingRoundingCreditTolerance {
		reasons = append(reasons, DriftReason{
			Code: "THINKING_ROUNDING",
			Detail: fmt.Sprintf(
				"思考模式 RMB→Credits 转换浮点舍入,差异 %d credit (≤ %d 容差)",
				absDelta, thinkingRoundingCreditTolerance,
			),
		})
	}

	// 5. NO_KNOWN_REASON:其他原因(可能是 user_discount/agent_pricing 配置变更)
	if len(reasons) == 0 {
		reasons = append(reasons, DriftReason{
			Code: "UNKNOWN_DRIFT",
			Detail: fmt.Sprintf(
				"偏差 %d credits,未识别明确原因,可能涉及 user_discount/agent_pricing 配置变更或快照缺失",
				billingDeltaCredits,
			),
		})
	}

	return reasons
}

// ─── Helpers ───

// relativeDelta 相对偏差 (a - b) / b,b=0 时返回 0
func relativeDelta(a, b int64) float64 {
	if b == 0 {
		if a == 0 {
			return 0
		}
		return 1.0
	}
	return float64(a-b) / float64(b)
}

// roundPct 保留 4 位小数(0.1234 = 12.34%)
func roundPct(p float64) float64 {
	return math.Round(p*10000) / 10000
}

func mapInt(m map[string]interface{}, key string) int {
	v, ok := m[key]
	if !ok || v == nil {
		return 0
	}
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	case int64:
		return int(n)
	case json.Number:
		i, _ := n.Int64()
		return int(i)
	}
	return 0
}

func mapFloat(m map[string]interface{}, key string) float64 {
	v, ok := m[key]
	if !ok || v == nil {
		return 0
	}
	switch n := v.(type) {
	case float64:
		return n
	case int:
		return float64(n)
	case int64:
		return float64(n)
	case json.Number:
		f, _ := n.Float64()
		return f
	}
	return 0
}

// ─── 批量扫描端点 ───

// ConsistencyScanItem 单条扫描结果
type ConsistencyScanItem struct {
	RequestID           string    `json:"request_id"`
	CreatedAt           time.Time `json:"created_at"`
	UserID              uint      `json:"user_id"`
	BillingCredits      int64     `json:"billing_credits"`
	CalculatorCredits   int64     `json:"calculator_credits"`
	DeltaCredits        int64     `json:"delta_credits"`
	MaxDeltaPct         float64   `json:"max_delta_pct"`
	Consensus           string    `json:"consensus"`
	TopDriftReason      string    `json:"top_drift_reason"`
}

// ConsistencyScanResult 扫描汇总
type ConsistencyScanResult struct {
	ModelID         uint                 `json:"model_id"`
	ModelName       string               `json:"model_name"`
	Hours           int                  `json:"hours"`
	TotalScanned    int                  `json:"total_scanned"`
	ConsistentCount int                  `json:"consistent_count"`
	MinorDriftCount int                  `json:"minor_drift_count"`
	MajorDriftCount int                  `json:"major_drift_count"`
	DriftRateByCode map[string]int       `json:"drift_rate_by_code"`
	TopDriftItems   []ConsistencyScanItem `json:"top_drift_items"`
	ScanDurationMs  int64                `json:"scan_duration_ms"`
}

// ScanConsistency GET /api/v1/admin/cost-consistency/scan?model_id=&hours=24
//
// 扫描指定模型最近 N 小时的所有日志,统计偏差分布 + Top 10 异常请求。
// 用于发现"昨天下午 3 点起 200 单连续偏差 5%"这种系统性问题。
func (h *CostConsistencyHandler) ScanConsistency(c *gin.Context) {
	ctx := c.Request.Context()
	startTime := time.Now()

	modelIDStr := c.Query("model_id")
	if modelIDStr == "" {
		response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrValidation.Code, "model_id 必填")
		return
	}
	modelID64, err := strconv.ParseUint(modelIDStr, 10, 64)
	if err != nil || modelID64 == 0 {
		response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrValidation.Code, "model_id 格式错误")
		return
	}
	modelID := uint(modelID64)

	hours, _ := strconv.Atoi(c.DefaultQuery("hours", "24"))
	if hours <= 0 || hours > 168 {
		hours = 24 // 最多扫描 7 天
	}

	// 加载模型名
	var aiModel model.AIModel
	if err := h.db.WithContext(ctx).Select("id, model_name").First(&aiModel, modelID).Error; err != nil {
		response.ErrorMsg(c, http.StatusNotFound, errcode.ErrNotFound.Code, "模型不存在")
		return
	}

	// 查近 N 小时该模型的日志(限 500 条以控制扫描成本)
	since := time.Now().Add(-time.Duration(hours) * time.Hour)
	var logs []model.ApiCallLog
	if err := h.db.WithContext(ctx).
		Where("(actual_model = ? OR request_model = ?) AND created_at >= ?",
			aiModel.ModelName, aiModel.ModelName, since).
		Order("created_at DESC").
		Limit(500).
		Find(&logs).Error; err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, err.Error())
		return
	}

	result := ConsistencyScanResult{
		ModelID:         modelID,
		ModelName:       aiModel.ModelName,
		Hours:           hours,
		TotalScanned:    len(logs),
		DriftRateByCode: map[string]int{},
		TopDriftItems:   []ConsistencyScanItem{},
	}

	// 逐条核对(同步,500 条以内可接受;超大量场景应改异步任务)
	driftCandidates := make([]ConsistencyScanItem, 0, len(logs))
	for i := range logs {
		check := h.computeThreeWayCheck(ctx, &logs[i])
		switch check.Consensus {
		case "consistent":
			result.ConsistentCount++
		case "minor_drift":
			result.MinorDriftCount++
		case "major_drift":
			result.MajorDriftCount++
		}
		for _, r := range check.DriftReasons {
			result.DriftRateByCode[r.Code]++
		}
		if check.Consensus != "consistent" {
			topReason := ""
			if len(check.DriftReasons) > 0 {
				topReason = check.DriftReasons[0].Code
			}
			driftCandidates = append(driftCandidates, ConsistencyScanItem{
				RequestID:         logs[i].RequestID,
				CreatedAt:         logs[i].CreatedAt,
				UserID:            logs[i].UserID,
				BillingCredits:    check.Billing.CostCredits,
				CalculatorCredits: check.Calculator.CostCredits,
				DeltaCredits:      check.Deltas.BillingVsCalculatorCredits,
				MaxDeltaPct:       check.Deltas.MaxDeltaPct,
				Consensus:         check.Consensus,
				TopDriftReason:    topReason,
			})
		}
	}

	// Top 10 by abs(delta_pct) desc
	for i := 0; i < len(driftCandidates); i++ {
		for j := i + 1; j < len(driftCandidates); j++ {
			if math.Abs(driftCandidates[j].MaxDeltaPct) > math.Abs(driftCandidates[i].MaxDeltaPct) {
				driftCandidates[i], driftCandidates[j] = driftCandidates[j], driftCandidates[i]
			}
		}
	}
	if len(driftCandidates) > 10 {
		driftCandidates = driftCandidates[:10]
	}
	result.TopDriftItems = driftCandidates
	result.ScanDurationMs = time.Since(startTime).Milliseconds()

	response.Success(c, result)
}
