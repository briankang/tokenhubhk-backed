package admin

import (
	"context"
	"encoding/csv"
	"fmt"
	"io"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"tokenhub-server/internal/pkg/response"
)

// BillingReconcileHandler 月度供应商账单对账
//
// 流程：管理员上传供应商月度账单 CSV →
//
//	服务端按 (model_name, period) 聚合 api_call_logs.platform_cost_rmb →
//	与 CSV 中 cost_rmb 比对 → 输出 diff 报告
//
// 容差阈值：3%（diff_pct > 3% 标红）
// 后端只做计算，结果不入库（避免数据膨胀）
type BillingReconcileHandler struct {
	db *gorm.DB
}

// NewBillingReconcileHandler 构造函数
func NewBillingReconcileHandler(db *gorm.DB) *BillingReconcileHandler {
	return &BillingReconcileHandler{db: db}
}

// Register 注册路由（adminGroup）
func (h *BillingReconcileHandler) Register(rg *gin.RouterGroup) {
	rg.POST("/billing/reconcile", h.Reconcile)
}

// ReconcileItem 单个模型对账行
type ReconcileItem struct {
	ModelName       string  `json:"model_name"`
	PlatformCostRMB float64 `json:"platform_cost_rmb"`  // 我们日志聚合的成本
	SupplierCostRMB float64 `json:"supplier_cost_rmb"`  // 供应商账单
	DiffRMB         float64 `json:"diff_rmb"`           // 差额（platform - supplier）
	DiffPct         float64 `json:"diff_pct"`           // 偏差百分比
	PlatformInputTokens  int64 `json:"platform_input_tokens"`
	PlatformOutputTokens int64 `json:"platform_output_tokens"`
	SupplierInputTokens  int64 `json:"supplier_input_tokens,omitempty"`
	SupplierOutputTokens int64 `json:"supplier_output_tokens,omitempty"`
	TokenDiff       int64   `json:"token_diff"`         // (platform_total - supplier_total)
	OverThreshold   bool    `json:"over_threshold"`     // |DiffPct| > 3%
}

// ReconcileResult 对账结果
type ReconcileResult struct {
	SupplierID        uint            `json:"supplier_id"`
	Period            string          `json:"period"`
	WindowStart       time.Time       `json:"window_start"`
	WindowEnd         time.Time       `json:"window_end"`
	Items             []ReconcileItem `json:"items"`
	TotalModels       int             `json:"total_models"`
	OverThresholdHits int             `json:"over_threshold_hits"`
	PlatformTotalRMB  float64         `json:"platform_total_rmb"`
	SupplierTotalRMB  float64         `json:"supplier_total_rmb"`
	OverallDiffRMB    float64         `json:"overall_diff_rmb"`
	OverallDiffPct    float64         `json:"overall_diff_pct"`
}

// Reconcile POST /api/v1/admin/billing/reconcile
//
// 请求：multipart/form-data
//   - supplier_id: uint
//   - period: "2026-04"
//   - bill_csv: 文件，列名 model_name,input_tokens,output_tokens,cost_rmb
//
// 响应：ReconcileResult
func (h *BillingReconcileHandler) Reconcile(c *gin.Context) {
	supplierIDStr := c.PostForm("supplier_id")
	supplierID, err := strconv.ParseUint(supplierIDStr, 10, 64)
	if err != nil || supplierID == 0 {
		response.ErrorMsg(c, http.StatusBadRequest, 40001, "supplier_id 无效")
		return
	}
	period := strings.TrimSpace(c.PostForm("period"))
	if period == "" {
		response.ErrorMsg(c, http.StatusBadRequest, 40002, "period 必填（格式 YYYY-MM）")
		return
	}
	periodStart, periodEnd, perr := parsePeriod(period)
	if perr != nil {
		response.ErrorMsg(c, http.StatusBadRequest, 40003, perr.Error())
		return
	}

	fileHeader, err := c.FormFile("bill_csv")
	if err != nil {
		response.ErrorMsg(c, http.StatusBadRequest, 40004, "bill_csv 必填: "+err.Error())
		return
	}
	if fileHeader.Size > 10*1024*1024 {
		response.ErrorMsg(c, http.StatusBadRequest, 40005, "CSV 文件大小超过 10MB 上限")
		return
	}
	f, err := fileHeader.Open()
	if err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, 50001, "读取 CSV 失败: "+err.Error())
		return
	}
	defer f.Close()

	supplierItems, parseErr := parseSupplierCSV(f)
	if parseErr != nil {
		response.ErrorMsg(c, http.StatusBadRequest, 40006, parseErr.Error())
		return
	}

	// 加载本平台聚合数据
	platformItems, perr2 := h.aggregatePlatformByModel(c.Request.Context(), uint(supplierID), periodStart, periodEnd)
	if perr2 != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, 50002, perr2.Error())
		return
	}

	// 合并比对
	result := mergeReconcile(platformItems, supplierItems)
	result.SupplierID = uint(supplierID)
	result.Period = period
	result.WindowStart = periodStart
	result.WindowEnd = periodEnd

	response.Success(c, result)
}

// 内部聚合结构
type platformAgg struct {
	ModelName       string
	InputTokens     int64
	OutputTokens    int64
	PlatformCostRMB float64
}

type supplierAgg struct {
	ModelName        string
	InputTokens      int64
	OutputTokens     int64
	SupplierCostRMB  float64
}

// parsePeriod 把 "2026-04" 转换为 [2026-04-01 00:00, 2026-05-01 00:00) 上海时区
func parsePeriod(period string) (time.Time, time.Time, error) {
	t, err := time.ParseInLocation("2006-01", period, time.Local)
	if err != nil {
		return time.Time{}, time.Time{}, fmt.Errorf("period 格式错误（应为 YYYY-MM）: %w", err)
	}
	end := t.AddDate(0, 1, 0)
	return t, end, nil
}

// parseSupplierCSV 读取标准列 model_name,input_tokens,output_tokens,cost_rmb
//
// 容错：
//   - 跳过空行
//   - BOM 处理
//   - 列顺序按 header 自适应
func parseSupplierCSV(r io.Reader) ([]supplierAgg, error) {
	reader := csv.NewReader(r)
	reader.FieldsPerRecord = -1 // 允许列数不一致
	records, err := reader.ReadAll()
	if err != nil {
		return nil, fmt.Errorf("解析 CSV 失败: %w", err)
	}
	if len(records) < 2 {
		return nil, fmt.Errorf("CSV 至少包含 header + 1 行数据")
	}
	// 处理 BOM
	if len(records[0]) > 0 {
		records[0][0] = strings.TrimPrefix(records[0][0], "\uFEFF")
	}
	header := make(map[string]int, len(records[0]))
	for i, col := range records[0] {
		header[strings.ToLower(strings.TrimSpace(col))] = i
	}
	idxName, okName := header["model_name"]
	idxIn := header["input_tokens"]
	idxOut := header["output_tokens"]
	idxCost, okCost := header["cost_rmb"]
	if !okName || !okCost {
		return nil, fmt.Errorf("CSV 必须包含列：model_name,cost_rmb（可选 input_tokens,output_tokens）")
	}
	var out []supplierAgg
	for i := 1; i < len(records); i++ {
		row := records[i]
		if len(row) <= idxName || len(row) <= idxCost {
			continue
		}
		name := strings.TrimSpace(row[idxName])
		if name == "" {
			continue
		}
		costStr := strings.TrimSpace(row[idxCost])
		cost, _ := strconv.ParseFloat(costStr, 64)
		var inTok, outTok int64
		if idxIn < len(row) {
			inTok, _ = strconv.ParseInt(strings.TrimSpace(row[idxIn]), 10, 64)
		}
		if idxOut < len(row) {
			outTok, _ = strconv.ParseInt(strings.TrimSpace(row[idxOut]), 10, 64)
		}
		out = append(out, supplierAgg{
			ModelName:       name,
			InputTokens:     inTok,
			OutputTokens:    outTok,
			SupplierCostRMB: cost,
		})
	}
	return out, nil
}

// aggregatePlatformByModel 从 api_call_logs 按 supplier 下所有 model 聚合
//
// 关联条件：api_call_logs.actual_model 或 model_name 关联 ai_models.model_name，
// ai_models.supplier_id = ? 且 created_at 在 period 区间
func (h *BillingReconcileHandler) aggregatePlatformByModel(ctx context.Context,
	supplierID uint, periodStart, periodEnd time.Time) ([]platformAgg, error) {
	type row struct {
		ModelName       string
		InputTokens     int64
		OutputTokens    int64
		PlatformCostRMB float64
	}
	var rows []row
	// 通过 join ai_models 取 supplier
	if err := h.db.WithContext(ctx).Raw(`
SELECT
  COALESCE(am.model_name, l.actual_model, l.model_name) AS model_name,
  COALESCE(SUM(l.prompt_tokens), 0)     AS input_tokens,
  COALESCE(SUM(l.completion_tokens), 0) AS output_tokens,
  COALESCE(SUM(l.platform_cost_rmb), 0) AS platform_cost_rmb
FROM api_call_logs l
LEFT JOIN ai_models am ON am.model_name = COALESCE(NULLIF(l.actual_model, ''), l.model_name)
WHERE am.supplier_id = ?
  AND l.created_at >= ? AND l.created_at < ?
  AND l.status_code = 200
GROUP BY COALESCE(am.model_name, l.actual_model, l.model_name)
HAVING input_tokens + output_tokens > 0
`, supplierID, periodStart, periodEnd).Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("聚合平台数据失败: %w", err)
	}
	out := make([]platformAgg, 0, len(rows))
	for _, r := range rows {
		out = append(out, platformAgg{
			ModelName:       r.ModelName,
			InputTokens:     r.InputTokens,
			OutputTokens:    r.OutputTokens,
			PlatformCostRMB: r.PlatformCostRMB,
		})
	}
	return out, nil
}

// mergeReconcile 把平台聚合 + 供应商账单合并为最终结果
//
// 容差阈值：3%（|DiffPct| > 0.03 → OverThreshold=true）
// 仅平台或仅供应商单边出现的模型也作为对账行返回（其中一边 0）
func mergeReconcile(platform []platformAgg, supplier []supplierAgg) ReconcileResult {
	platformMap := make(map[string]platformAgg, len(platform))
	for _, p := range platform {
		platformMap[normalizeModelKey(p.ModelName)] = p
	}
	supplierMap := make(map[string]supplierAgg, len(supplier))
	for _, s := range supplier {
		supplierMap[normalizeModelKey(s.ModelName)] = s
	}
	keys := make(map[string]struct{}, len(platformMap)+len(supplierMap))
	for k := range platformMap {
		keys[k] = struct{}{}
	}
	for k := range supplierMap {
		keys[k] = struct{}{}
	}
	items := make([]ReconcileItem, 0, len(keys))
	var totalPlatform, totalSupplier float64
	overHits := 0
	for k := range keys {
		p := platformMap[k]
		s := supplierMap[k]
		modelName := p.ModelName
		if modelName == "" {
			modelName = s.ModelName
		}
		diffRMB := p.PlatformCostRMB - s.SupplierCostRMB
		var diffPct float64
		if s.SupplierCostRMB > 0 {
			diffPct = diffRMB / s.SupplierCostRMB
		} else if p.PlatformCostRMB > 0 {
			diffPct = 1.0 // 供应商无记录但我们有 → 100% 偏差
		}
		over := math.Abs(diffPct) > 0.03
		if over {
			overHits++
		}
		items = append(items, ReconcileItem{
			ModelName:            modelName,
			PlatformCostRMB:      round6(p.PlatformCostRMB),
			SupplierCostRMB:      round6(s.SupplierCostRMB),
			DiffRMB:              round6(diffRMB),
			DiffPct:              round6(diffPct),
			PlatformInputTokens:  p.InputTokens,
			PlatformOutputTokens: p.OutputTokens,
			SupplierInputTokens:  s.InputTokens,
			SupplierOutputTokens: s.OutputTokens,
			TokenDiff:            (p.InputTokens + p.OutputTokens) - (s.InputTokens + s.OutputTokens),
			OverThreshold:        over,
		})
		totalPlatform += p.PlatformCostRMB
		totalSupplier += s.SupplierCostRMB
	}
	sort.Slice(items, func(i, j int) bool {
		return math.Abs(items[i].DiffPct) > math.Abs(items[j].DiffPct)
	})
	overall := totalPlatform - totalSupplier
	var overallPct float64
	if totalSupplier > 0 {
		overallPct = overall / totalSupplier
	}
	return ReconcileResult{
		Items:             items,
		TotalModels:       len(items),
		OverThresholdHits: overHits,
		PlatformTotalRMB:  round6(totalPlatform),
		SupplierTotalRMB:  round6(totalSupplier),
		OverallDiffRMB:    round6(overall),
		OverallDiffPct:    round6(overallPct),
	}
}

// normalizeModelKey 模型名归一化（lower + trim），便于跨大小写匹配
func normalizeModelKey(name string) string {
	return strings.ToLower(strings.TrimSpace(name))
}

func round6(v float64) float64 {
	return math.Round(v*1e6) / 1e6
}