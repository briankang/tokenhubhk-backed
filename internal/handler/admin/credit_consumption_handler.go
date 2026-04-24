package admin

import (
	"encoding/csv"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"tokenhub-server/internal/pkg/errcode"
	"tokenhub-server/internal/pkg/response"
)

// ========================================================================
// CreditConsumptionHandler — 运营报表：积分消耗查询
// 数据源:
//   - user_daily_stats (聚合表，cron 01:00 聚合自 api_call_logs，持久保留)
//   - api_call_logs (逐请求，仅保留 7 天)
// 核心维度: 日期 × 用户 × 模型
// ========================================================================

// CreditConsumptionHandler 积分消耗查询处理器
type CreditConsumptionHandler struct {
	db *gorm.DB
}

// NewCreditConsumptionHandler 创建处理器实例
func NewCreditConsumptionHandler(db *gorm.DB) *CreditConsumptionHandler {
	if db == nil {
		panic("credit_consumption handler: db is nil")
	}
	return &CreditConsumptionHandler{db: db}
}

// Register 注册路由
func (h *CreditConsumptionHandler) Register(rg *gin.RouterGroup) {
	rg.GET("/consumption/daily", h.Daily)
	rg.GET("/consumption/model-breakdown", h.ModelBreakdown)
	rg.POST("/consumption/daily/export", h.ExportCSV)
}

// dailyRow 按日/用户/模型粒度的返回结构
type dailyRow struct {
	Date         string  `json:"date"`
	UserID       uint    `json:"user_id"`
	UserEmail    string  `json:"user_email"`
	UserName     string  `json:"user_name"`
	RequestModel string  `json:"request_model,omitempty"`
	RequestCount int64   `json:"request_count"`
	SuccessCount int64   `json:"success_count"`
	ErrorCount   int64   `json:"error_count"`
	InputTokens  int64   `json:"input_tokens"`
	OutputTokens int64   `json:"output_tokens"`
	TotalTokens  int64   `json:"total_tokens"`
	CostCredits  int64   `json:"cost_credits"`
	CostRMB      float64 `json:"cost_rmb"`
	AvgLatencyMs float64 `json:"avg_latency_ms"`
	ModelCount   int64   `json:"model_count,omitempty"` // 涉及模型数（date_user 模式）
	DayCount     int64   `json:"day_count,omitempty"`   // 消耗天数（user 模式）
}

// Daily 按日聚合查询
// GET /admin/consumption/daily
// 参数:
//   - group_by: date_user (默认, 按日+用户) | user (按用户) | full (按日+用户+模型)
//   - start_date, end_date: YYYY-MM-DD
//   - user_id / user_email / model
//   - min_credits / max_credits
//   - sort: cost_desc (默认) | count_desc | date_desc | date_asc
//   - page / page_size
func (h *CreditConsumptionHandler) Daily(c *gin.Context) {
	ctx := c.Request.Context()
	groupBy := c.DefaultQuery("group_by", "date_user")
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "20"))
	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > 100 {
		pageSize = 20
	}

	// ─── 构建基础查询 ───
	// 把 WHERE 条件抽成 applyFilters 以便 count 查询独立重建，避免 GORM session 副作用
	applyFilters := func(tx *gorm.DB) *gorm.DB {
		if sd := c.Query("start_date"); sd != "" {
			tx = tx.Where("s.date >= ?", sd)
		}
		if ed := c.Query("end_date"); ed != "" {
			tx = tx.Where("s.date <= ?", ed)
		}
		if uid := c.Query("user_id"); uid != "" {
			tx = tx.Where("s.user_id = ?", uid)
		}
		if ue := c.Query("user_email"); ue != "" {
			tx = tx.Where("u.email LIKE ?", "%"+ue+"%")
		}
		if m := c.Query("model"); m != "" {
			tx = tx.Where("s.request_model LIKE ?", "%"+m+"%")
		}
		return tx
	}
	q := applyFilters(h.db.WithContext(ctx).
		Table("user_daily_stats AS s").
		Joins("LEFT JOIN users AS u ON u.id = s.user_id"))

	// 选择列与分组字段
	var selectCols, groupCols, defaultOrder string
	switch groupBy {
	case "user":
		selectCols = `s.user_id, u.email AS user_email, u.name AS user_name,
			SUM(s.request_count) AS request_count,
			SUM(s.success_count) AS success_count,
			SUM(s.error_count) AS error_count,
			SUM(s.input_tokens) AS input_tokens,
			SUM(s.output_tokens) AS output_tokens,
			SUM(s.total_tokens) AS total_tokens,
			SUM(s.cost_credits) AS cost_credits,
			SUM(s.cost_credits)/10000.0 AS cost_rmb,
			AVG(s.avg_latency_ms) AS avg_latency_ms,
			COUNT(DISTINCT s.request_model) AS model_count,
			COUNT(DISTINCT s.date) AS day_count`
		groupCols = "s.user_id, u.email, u.name"
		defaultOrder = "cost_credits DESC"
	case "full":
		selectCols = `s.date, s.user_id, u.email AS user_email, u.name AS user_name,
			s.request_model,
			s.request_count, s.success_count, s.error_count,
			s.input_tokens, s.output_tokens, s.total_tokens,
			s.cost_credits, s.cost_credits/10000.0 AS cost_rmb,
			s.avg_latency_ms`
		// 无 GROUP BY，每行已是 user×model×date 粒度
		groupCols = ""
		defaultOrder = "s.date DESC, s.cost_credits DESC"
	default: // date_user
		groupBy = "date_user"
		selectCols = `s.date, s.user_id, u.email AS user_email, u.name AS user_name,
			SUM(s.request_count) AS request_count,
			SUM(s.success_count) AS success_count,
			SUM(s.error_count) AS error_count,
			SUM(s.input_tokens) AS input_tokens,
			SUM(s.output_tokens) AS output_tokens,
			SUM(s.total_tokens) AS total_tokens,
			SUM(s.cost_credits) AS cost_credits,
			SUM(s.cost_credits)/10000.0 AS cost_rmb,
			AVG(s.avg_latency_ms) AS avg_latency_ms,
			COUNT(DISTINCT s.request_model) AS model_count`
		groupCols = "s.date, s.user_id, u.email, u.name"
		defaultOrder = "s.date DESC, cost_credits DESC"
	}

	// 金额区间过滤（在 SELECT 之后的 HAVING 上应用 full 以外场景）
	minCred := c.Query("min_credits")
	maxCred := c.Query("max_credits")
	if groupCols != "" {
		q = q.Group(groupCols)
		if minCred != "" {
			q = q.Having("SUM(s.cost_credits) >= ?", minCred)
		}
		if maxCred != "" {
			q = q.Having("SUM(s.cost_credits) <= ?", maxCred)
		}
	} else {
		// full 模式直接对单行过滤
		if minCred != "" {
			q = q.Where("s.cost_credits >= ?", minCred)
		}
		if maxCred != "" {
			q = q.Where("s.cost_credits <= ?", maxCred)
		}
	}

	// ─── 排序 ───
	orderClause := defaultOrder
	switch c.Query("sort") {
	case "count_desc":
		if groupBy == "full" {
			orderClause = "s.request_count DESC"
		} else {
			orderClause = "request_count DESC"
		}
	case "date_desc":
		orderClause = "s.date DESC"
	case "date_asc":
		orderClause = "s.date ASC"
	case "cost_desc":
		if groupBy == "full" {
			orderClause = "s.cost_credits DESC"
		} else {
			orderClause = "cost_credits DESC"
		}
	}

	// ─── Count ───
	// 重新构建一个不带 GROUP BY 的 count 查询（复用 applyFilters），避免 GORM session 副作用
	var total int64
	countBase := applyFilters(h.db.WithContext(ctx).
		Table("user_daily_stats AS s").
		Joins("LEFT JOIN users AS u ON u.id = s.user_id"))

	if groupCols != "" && (minCred != "" || maxCred != "") {
		// HAVING 场景：回放 GROUP+HAVING 后 Scan 组键列表，内存计数（仅选聚合列满足 only_full_group_by）
		havingQ := countBase.Select("SUM(s.cost_credits) AS v").Group(groupCols)
		if minCred != "" {
			havingQ = havingQ.Having("SUM(s.cost_credits) >= ?", minCred)
		}
		if maxCred != "" {
			havingQ = havingQ.Having("SUM(s.cost_credits) <= ?", maxCred)
		}
		var cks []int64
		_ = havingQ.Pluck("v", &cks).Error
		total = int64(len(cks))
	} else if groupCols != "" {
		switch groupBy {
		case "user":
			countBase.Distinct("s.user_id").Count(&total)
		case "date_user":
			// MySQL 支持 COUNT(DISTINCT a, b) 语法
			_ = countBase.Select("COUNT(DISTINCT s.date, s.user_id)").Row().Scan(&total)
		}
	} else {
		countBase.Count(&total)
	}

	// ─── 分页查询 ───
	offset := (page - 1) * pageSize
	var rows []dailyRow
	if err := q.Select(selectCols).Order(orderClause).Offset(offset).Limit(pageSize).Scan(&rows).Error; err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, err.Error())
		return
	}

	response.PageResult(c, rows, total, page, pageSize)
}

// ModelBreakdown 弹窗：某用户某日各模型消耗明细
// GET /admin/consumption/model-breakdown?user_id=X&date=YYYY-MM-DD
func (h *CreditConsumptionHandler) ModelBreakdown(c *gin.Context) {
	userID := c.Query("user_id")
	date := c.Query("date")
	startDate := c.Query("start_date")
	endDate := c.Query("end_date")
	if userID == "" {
		response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrValidation.Code, "user_id is required")
		return
	}
	if date == "" && startDate == "" && endDate == "" {
		response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrValidation.Code, "date or date range is required")
		return
	}

	type modelRow struct {
		Date         string  `json:"date,omitempty"`
		RequestModel string  `json:"request_model"`
		RequestCount int64   `json:"request_count"`
		SuccessCount int64   `json:"success_count"`
		ErrorCount   int64   `json:"error_count"`
		InputTokens  int64   `json:"input_tokens"`
		OutputTokens int64   `json:"output_tokens"`
		TotalTokens  int64   `json:"total_tokens"`
		CostCredits  int64   `json:"cost_credits"`
		CostRMB      float64 `json:"cost_rmb"`
		AvgLatencyMs float64 `json:"avg_latency_ms"`
	}

	var rows []modelRow
	q := h.db.WithContext(c.Request.Context()).
		Table("user_daily_stats").
		Where("user_id = ?", userID)
	if date != "" {
		q = q.Where("date = ?", date).
			Select(`date, request_model, request_count, success_count, error_count,
				input_tokens, output_tokens, total_tokens,
				cost_credits, cost_credits/10000.0 AS cost_rmb, avg_latency_ms`).
			Order("cost_credits DESC")
	} else {
		if startDate != "" {
			q = q.Where("date >= ?", startDate)
		}
		if endDate != "" {
			q = q.Where("date <= ?", endDate)
		}
		q = q.Select(`date, request_model,
				SUM(request_count) AS request_count,
				SUM(success_count) AS success_count,
				SUM(error_count) AS error_count,
				SUM(input_tokens) AS input_tokens,
				SUM(output_tokens) AS output_tokens,
				SUM(total_tokens) AS total_tokens,
				SUM(cost_credits) AS cost_credits,
				SUM(cost_credits)/10000.0 AS cost_rmb,
				AVG(avg_latency_ms) AS avg_latency_ms`).
			Group("date, request_model").
			Order("date DESC, cost_credits DESC")
	}
	err := q.Limit(500).Scan(&rows).Error
	if err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, err.Error())
		return
	}
	response.Success(c, gin.H{"list": rows})
}

// ExportCSV 导出积分消耗按日报表 CSV
// POST /admin/consumption/daily/export
// body: 与 Daily 同参数
func (h *CreditConsumptionHandler) ExportCSV(c *gin.Context) {
	ctx := c.Request.Context()
	var params struct {
		GroupBy    string `json:"group_by"`
		StartDate  string `json:"start_date"`
		EndDate    string `json:"end_date"`
		UserID     string `json:"user_id"`
		UserEmail  string `json:"user_email"`
		Model      string `json:"model"`
		MinCredits string `json:"min_credits"`
		MaxCredits string `json:"max_credits"`
		Sort       string `json:"sort"`
	}
	_ = c.ShouldBindJSON(&params)
	if params.GroupBy == "" {
		params.GroupBy = "date_user"
	}

	q := h.db.WithContext(ctx).
		Table("user_daily_stats AS s").
		Joins("LEFT JOIN users AS u ON u.id = s.user_id")
	if params.StartDate != "" {
		q = q.Where("s.date >= ?", params.StartDate)
	}
	if params.EndDate != "" {
		q = q.Where("s.date <= ?", params.EndDate)
	}
	if params.UserID != "" {
		q = q.Where("s.user_id = ?", params.UserID)
	}
	if params.UserEmail != "" {
		q = q.Where("u.email LIKE ?", "%"+params.UserEmail+"%")
	}
	if params.Model != "" {
		q = q.Where("s.request_model LIKE ?", "%"+params.Model+"%")
	}

	const exportCap = 100000

	var rows []dailyRow
	switch params.GroupBy {
	case "user":
		q = q.Select(`s.user_id, u.email AS user_email, u.name AS user_name,
			SUM(s.request_count) AS request_count,
			SUM(s.success_count) AS success_count,
			SUM(s.error_count) AS error_count,
			SUM(s.input_tokens) AS input_tokens,
			SUM(s.output_tokens) AS output_tokens,
			SUM(s.total_tokens) AS total_tokens,
			SUM(s.cost_credits) AS cost_credits,
			SUM(s.cost_credits)/10000.0 AS cost_rmb,
			AVG(s.avg_latency_ms) AS avg_latency_ms,
			COUNT(DISTINCT s.request_model) AS model_count,
			COUNT(DISTINCT s.date) AS day_count`).
			Group("s.user_id, u.email, u.name").
			Order("cost_credits DESC").Limit(exportCap)
	case "full":
		q = q.Select(`s.date, s.user_id, u.email AS user_email, u.name AS user_name,
			s.request_model, s.request_count, s.success_count, s.error_count,
			s.input_tokens, s.output_tokens, s.total_tokens,
			s.cost_credits, s.cost_credits/10000.0 AS cost_rmb, s.avg_latency_ms`).
			Order("s.date DESC, s.cost_credits DESC").Limit(exportCap)
	default:
		params.GroupBy = "date_user"
		q = q.Select(`s.date, s.user_id, u.email AS user_email, u.name AS user_name,
			SUM(s.request_count) AS request_count,
			SUM(s.success_count) AS success_count,
			SUM(s.error_count) AS error_count,
			SUM(s.input_tokens) AS input_tokens,
			SUM(s.output_tokens) AS output_tokens,
			SUM(s.total_tokens) AS total_tokens,
			SUM(s.cost_credits) AS cost_credits,
			SUM(s.cost_credits)/10000.0 AS cost_rmb,
			AVG(s.avg_latency_ms) AS avg_latency_ms,
			COUNT(DISTINCT s.request_model) AS model_count`).
			Group("s.date, s.user_id, u.email, u.name").
			Order("s.date DESC, cost_credits DESC").Limit(exportCap)
	}

	if err := q.Scan(&rows).Error; err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, err.Error())
		return
	}

	filename := fmt.Sprintf("consumption_%s_%s.csv", params.GroupBy, time.Now().Format("20060102_150405"))
	c.Header("Content-Type", "text/csv; charset=utf-8-sig")
	c.Header("Content-Disposition", "attachment; filename=\""+filename+"\"")
	// UTF-8 BOM 让 Excel 正确识别中文
	c.Writer.Write([]byte{0xEF, 0xBB, 0xBF})

	w := csv.NewWriter(c.Writer)
	defer w.Flush()

	// 表头
	var headers []string
	switch params.GroupBy {
	case "user":
		headers = []string{"用户ID", "邮箱", "昵称", "调用次数", "成功", "失败", "输入Tokens", "输出Tokens", "总Tokens", "消耗积分", "等值RMB", "平均延迟ms", "涉及模型数", "消耗天数"}
	case "full":
		headers = []string{"日期", "用户ID", "邮箱", "昵称", "模型", "调用次数", "成功", "失败", "输入Tokens", "输出Tokens", "总Tokens", "消耗积分", "等值RMB", "平均延迟ms"}
	default:
		headers = []string{"日期", "用户ID", "邮箱", "昵称", "调用次数", "成功", "失败", "输入Tokens", "输出Tokens", "总Tokens", "消耗积分", "等值RMB", "平均延迟ms", "涉及模型数"}
	}
	_ = w.Write(headers)

	for _, r := range rows {
		var record []string
		switch params.GroupBy {
		case "user":
			record = []string{
				strconv.FormatUint(uint64(r.UserID), 10), r.UserEmail, r.UserName,
				strconv.FormatInt(r.RequestCount, 10),
				strconv.FormatInt(r.SuccessCount, 10),
				strconv.FormatInt(r.ErrorCount, 10),
				strconv.FormatInt(r.InputTokens, 10),
				strconv.FormatInt(r.OutputTokens, 10),
				strconv.FormatInt(r.TotalTokens, 10),
				strconv.FormatInt(r.CostCredits, 10),
				fmt.Sprintf("%.4f", r.CostRMB),
				fmt.Sprintf("%.2f", r.AvgLatencyMs),
				strconv.FormatInt(r.ModelCount, 10),
				strconv.FormatInt(r.DayCount, 10),
			}
		case "full":
			record = []string{
				r.Date, strconv.FormatUint(uint64(r.UserID), 10), r.UserEmail, r.UserName,
				r.RequestModel,
				strconv.FormatInt(r.RequestCount, 10),
				strconv.FormatInt(r.SuccessCount, 10),
				strconv.FormatInt(r.ErrorCount, 10),
				strconv.FormatInt(r.InputTokens, 10),
				strconv.FormatInt(r.OutputTokens, 10),
				strconv.FormatInt(r.TotalTokens, 10),
				strconv.FormatInt(r.CostCredits, 10),
				fmt.Sprintf("%.4f", r.CostRMB),
				fmt.Sprintf("%.2f", r.AvgLatencyMs),
			}
		default:
			record = []string{
				r.Date, strconv.FormatUint(uint64(r.UserID), 10), r.UserEmail, r.UserName,
				strconv.FormatInt(r.RequestCount, 10),
				strconv.FormatInt(r.SuccessCount, 10),
				strconv.FormatInt(r.ErrorCount, 10),
				strconv.FormatInt(r.InputTokens, 10),
				strconv.FormatInt(r.OutputTokens, 10),
				strconv.FormatInt(r.TotalTokens, 10),
				strconv.FormatInt(r.CostCredits, 10),
				fmt.Sprintf("%.4f", r.CostRMB),
				fmt.Sprintf("%.2f", r.AvgLatencyMs),
				strconv.FormatInt(r.ModelCount, 10),
			}
		}
		_ = w.Write(record)
	}
}
