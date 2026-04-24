package report

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	goredis "github.com/redis/go-redis/v9"
	"gorm.io/gorm"

	"tokenhub-server/internal/model"
	"tokenhub-server/internal/service/permission"
)

// ReportService 报表服务，提供高级报表查询
type ReportService struct {
	db         *gorm.DB
	redis      *goredis.Client
	profitCalc *ProfitCalculator
	perm       *permission.PermissionService
}

// NewReportService 创建报表服务实例
func NewReportService(db *gorm.DB, redis *goredis.Client, profitCalc *ProfitCalculator, perm *permission.PermissionService) *ReportService {
	if db == nil {
		panic("report service: db is nil")
	}
	return &ReportService{
		db:         db,
		redis:      redis,
		profitCalc: profitCalc,
		perm:       perm,
	}
}

// ReportFilter 报表查询的公共过滤参数
type ReportFilter struct {
	TenantID  *uint  `json:"tenant_id,omitempty"`
	ModelID   *uint  `json:"model_id,omitempty"`
	ChannelID *uint  `json:"channel_id,omitempty"`
	StartDate string `json:"start_date"`
	EndDate   string `json:"end_date"`
	GroupBy   string `json:"group_by"`
	Page      int    `json:"page"`
	PageSize  int    `json:"page_size"`
}

// OverviewReport 仪表盘概览数据
type OverviewReport struct {
	TotalRevenue      float64 `json:"total_revenue"`
	TotalCost         float64 `json:"total_cost"`
	GrossProfit       float64 `json:"gross_profit"`
	ProfitMargin      float64 `json:"profit_margin"`
	TotalRequests     int64   `json:"total_requests"`
	TotalInputTokens  int64   `json:"total_input_tokens"`
	TotalOutputTokens int64   `json:"total_output_tokens"`
	ErrorCount        int64   `json:"error_count"`
	ErrorRate         float64 `json:"error_rate"`
	ActiveTenants     int64   `json:"active_tenants"`
	ActiveKeys        int64   `json:"active_keys"`
	// 前端总览卡片所需的额外字段
	TotalUsers         int64   `json:"total_users"`
	TodayRevenue       float64 `json:"today_revenue"`
	TodayRevenueChange string  `json:"today_revenue_change"`
	TodayRequests      int64   `json:"today_requests"`
	TodayRequestsChange string `json:"today_requests_change"`
	TotalUsersChange   string  `json:"total_users_change"`
	ActiveChannels     int64   `json:"active_channels"`
	TotalChannels      int64   `json:"total_channels"`
}

// UsageReport 用量统计报表
type UsageReport struct {
	Items    []UsageReportItem `json:"items"`
	Total    int64             `json:"total"`
	Page     int               `json:"page"`
	PageSize int               `json:"page_size"`
}

// UsageReportItem 用量报表中的单行数据
type UsageReportItem struct {
	Period            string  `json:"period"`
	ModelID           uint    `json:"model_id,omitempty"`
	ModelName         string  `json:"model_name,omitempty"`
	ChannelID         uint    `json:"channel_id,omitempty"`
	TenantID          uint    `json:"tenant_id,omitempty"`
	TotalRequests     int64   `json:"total_requests"`
	TotalInputTokens  int64   `json:"total_input_tokens"`
	TotalOutputTokens int64   `json:"total_output_tokens"`
	AvgLatencyMs      float64 `json:"avg_latency_ms"`
	ErrorCount        int64   `json:"error_count"`
}

// ProfitReport 利润报表结果（包含分页元数据）
type ProfitReport struct {
	Summary *ProfitResult     `json:"summary"`
	Trend   []ProfitTrendItem `json:"trend,omitempty"`
}

// ConsumptionFilter 消费明细查询的过滤参数
type ConsumptionFilter struct {
	TenantID  *uint  `json:"tenant_id,omitempty"`
	UserID    *uint  `json:"user_id,omitempty"`
	KeyID     *uint  `json:"key_id,omitempty"`
	StartDate string `json:"start_date"`
	EndDate   string `json:"end_date"`
	Page      int    `json:"page"`
	PageSize  int    `json:"page_size"`
}

// ConsumptionItem 单条消费记录
type ConsumptionItem struct {
	TenantID          uint    `json:"tenant_id"`
	TenantName        string  `json:"tenant_name"`
	UserID            uint    `json:"user_id"`
	UserName          string  `json:"user_name"`
	TotalRequests     int64   `json:"total_requests"`
	TotalInputTokens  int64   `json:"total_input_tokens"`
	TotalOutputTokens int64   `json:"total_output_tokens"`
	TotalRevenue      float64 `json:"total_revenue"`
}

// ChannelLogItem 单条 API 调用记录
type ChannelLogItem struct {
	ID             uint      `json:"id"`
	ModelName      string    `json:"model_name"`
	RequestTokens  int       `json:"request_tokens"`
	ResponseTokens int       `json:"response_tokens"`
	LatencyMs      int       `json:"latency_ms"`
	StatusCode     int       `json:"status_code"`
	ErrorMessage   string    `json:"error_message,omitempty"`
	RequestID      string    `json:"request_id"`
	CreatedAt      time.Time `json:"created_at"`
}

// --- Report Cache Helpers ---

const (
	reportCachePrefix = "report:"
	cacheTTLDay       = 30 * time.Minute
	cacheTTLWeek      = 1 * time.Hour
	cacheTTLMonth     = 1 * time.Hour
	cacheTTLYear      = 1 * time.Hour
)

func (s *ReportService) cacheTTL(groupBy string) time.Duration {
	switch groupBy {
	case "week":
		return cacheTTLWeek
	case "month":
		return cacheTTLMonth
	case "year":
		return cacheTTLYear
	default:
		return cacheTTLDay
	}
}

func (s *ReportService) getFromCache(ctx context.Context, key string, dest interface{}) bool {
	if s.redis == nil {
		return false
	}
	val, err := s.redis.Get(ctx, key).Result()
	if err != nil {
		return false
	}
	if err := json.Unmarshal([]byte(val), dest); err != nil {
		return false
	}
	return true
}

func (s *ReportService) setCache(ctx context.Context, key string, value interface{}, ttl time.Duration) {
	if s.redis == nil {
		return
	}
	data, err := json.Marshal(value)
	if err != nil {
		return
	}
	_ = s.redis.Set(ctx, key, string(data), ttl).Err()
}

// GetOverviewReport 获取指定租户和时间段的仪表盘概览报表
func (s *ReportService) GetOverviewReport(ctx context.Context, tenantID uint, period string) (*OverviewReport, error) {
	startDate, endDate := resolvePeriod(period)

	cacheKey := fmt.Sprintf("%soverview:%d:%s", reportCachePrefix, tenantID, period)
	var cached OverviewReport
	if s.getFromCache(ctx, cacheKey, &cached) {
		return &cached, nil
	}

	filter := ProfitFilter{StartDate: startDate, EndDate: endDate}
	if tenantID > 0 {
		filter.TenantID = &tenantID
	}

	profit, err := s.profitCalc.CalculateProfit(ctx, filter)
	if err != nil {
		return nil, fmt.Errorf("failed to get overview: %w", err)
	}

	report := &OverviewReport{
		TotalRevenue:      profit.TotalRevenue,
		TotalCost:         profit.TotalCost,
		GrossProfit:       profit.GrossProfit,
		ProfitMargin:      profit.ProfitMargin,
		TotalRequests:     profit.RequestCount,
		TotalInputTokens:  profit.TotalInputTokens,
		TotalOutputTokens: profit.TotalOutputTokens,
		ErrorCount:        profit.ErrorCount,
	}

	if report.TotalRequests > 0 {
		report.ErrorRate = float64(report.ErrorCount) / float64(report.TotalRequests) * 100
	}

	// Count active tenants and keys
	tenantQuery := s.db.Model(&model.Tenant{}).Where("is_active = ?", true)
	keyQuery := s.db.Model(&model.ApiKey{}).Where("is_active = ?", true)

	if tenantID > 0 {
		tenantQuery = tenantQuery.Scopes(permission.TenantScope(ctx))
		keyQuery = keyQuery.Scopes(permission.TenantScope(ctx))
	}

	tenantQuery.Count(&report.ActiveTenants)
	keyQuery.Count(&report.ActiveKeys)

	// ── 前端总览卡片所需数据 ──

	// 总用户数
	s.db.Model(&model.User{}).Count(&report.TotalUsers)

	// 活跃渠道 / 总渠道数
	s.db.Model(&model.Channel{}).Where("status = ?", "active").Count(&report.ActiveChannels)
	s.db.Model(&model.Channel{}).Count(&report.TotalChannels)

	// 今日请求数（从 channel_logs 统计当天）
	today := time.Now().Format("2006-01-02")
	s.db.Table("channel_logs").
		Where("created_at >= ?", today).
		Count(&report.TodayRequests)

	// 今日收入（从 daily_stats 统计当天）
	var todayRev struct{ Revenue float64 }
	s.db.Table("daily_stats").
		Select("COALESCE(SUM(total_revenue), 0) as revenue").
		Where("date = ?", today).
		Scan(&todayRev)
	report.TodayRevenue = todayRev.Revenue

	// 变化率：对比昨日
	yesterday := time.Now().AddDate(0, 0, -1).Format("2006-01-02")
	var yesterdayReqs int64
	s.db.Table("channel_logs").
		Where("created_at >= ? AND created_at < ?", yesterday, today).
		Count(&yesterdayReqs)

	if yesterdayReqs > 0 {
		pct := float64(report.TodayRequests-yesterdayReqs) / float64(yesterdayReqs) * 100
		if pct >= 0 {
			report.TodayRequestsChange = fmt.Sprintf("+%.0f%%", pct)
		} else {
			report.TodayRequestsChange = fmt.Sprintf("%.0f%%", pct)
		}
	}

	s.setCache(ctx, cacheKey, report, cacheTTLDay)
	return report, nil
}

// GetUsageReport 获取按指定维度分组的用量统计
func (s *ReportService) GetUsageReport(ctx context.Context, filter ReportFilter) (*UsageReport, error) {
	if err := validateDateRange(filter.StartDate, filter.EndDate); err != nil {
		return nil, err
	}
	normalizePageParams(&filter.Page, &filter.PageSize)

	groupExpr := dateGroupExpression(filter.GroupBy)

	query := s.db.Table("daily_stats").
		Select(fmt.Sprintf(`
			%s as period,
			model_id,
			tenant_id,
			COALESCE(SUM(total_requests), 0) as total_requests,
			COALESCE(SUM(total_input_tokens), 0) as total_input_tokens,
			COALESCE(SUM(total_output_tokens), 0) as total_output_tokens,
			COALESCE(AVG(avg_latency_ms), 0) as avg_latency_ms,
			COALESCE(SUM(error_count), 0) as error_count
		`, groupExpr)).
		Where("date >= ? AND date <= ?", filter.StartDate, filter.EndDate).
		Scopes(permission.TenantScope(ctx))

	if filter.TenantID != nil {
		query = query.Where("tenant_id = ?", *filter.TenantID)
	}
	if filter.ModelID != nil {
		query = query.Where("model_id = ?", *filter.ModelID)
	}
	if filter.ChannelID != nil {
		query = query.Where("channel_id = ?", *filter.ChannelID)
	}

	groupCols := "period, model_id, tenant_id"
	query = query.Group(groupCols).Order("period DESC")

	// Count total distinct groups — 避免 GORM 子查询包裹导致 MySQL 1064 错误
	// 重建独立 count 查询，复用相同 WHERE 条件
	var total int64
	{
		countQ := s.db.Table("daily_stats").
			Where("date >= ? AND date <= ?", filter.StartDate, filter.EndDate).
			Scopes(permission.TenantScope(ctx))
		if filter.TenantID != nil {
			countQ = countQ.Where("tenant_id = ?", *filter.TenantID)
		}
		if filter.ModelID != nil {
			countQ = countQ.Where("model_id = ?", *filter.ModelID)
		}
		if filter.ChannelID != nil {
			countQ = countQ.Where("channel_id = ?", *filter.ChannelID)
		}
		row := countQ.Select(fmt.Sprintf("COUNT(DISTINCT %s, model_id, tenant_id)", groupExpr)).Row()
		if err := row.Scan(&total); err != nil {
			return nil, fmt.Errorf("failed to count usage report: %w", err)
		}
	}

	// Paginate
	var items []UsageReportItem
	offset := (filter.Page - 1) * filter.PageSize
	if err := query.Offset(offset).Limit(filter.PageSize).Scan(&items).Error; err != nil {
		return nil, fmt.Errorf("failed to query usage report: %w", err)
	}

	return &UsageReport{
		Items:    items,
		Total:    total,
		Page:     filter.Page,
		PageSize: filter.PageSize,
	}, nil
}

// GetProfitReport 获取利润汇总和可选的趋势数据
func (s *ReportService) GetProfitReport(ctx context.Context, filter ReportFilter) (*ProfitReport, error) {
	pf := ProfitFilter{
		TenantID:  filter.TenantID,
		ModelID:   filter.ModelID,
		ChannelID: filter.ChannelID,
		StartDate: filter.StartDate,
		EndDate:   filter.EndDate,
		GroupBy:   filter.GroupBy,
	}

	summary, err := s.profitCalc.CalculateProfit(ctx, pf)
	if err != nil {
		return nil, err
	}

	var trend []ProfitTrendItem
	if filter.GroupBy != "" {
		trend, err = s.profitCalc.GetProfitTrend(ctx, pf)
		if err != nil {
			return nil, err
		}
	}

	return &ProfitReport{
		Summary: summary,
		Trend:   trend,
	}, nil
}

// GetConsumptionDetail 获取按租户/用户分组的消费数据
func (s *ReportService) GetConsumptionDetail(ctx context.Context, filter ConsumptionFilter) ([]ConsumptionItem, int64, error) {
	if err := validateDateRange(filter.StartDate, filter.EndDate); err != nil {
		return nil, 0, err
	}
	normalizePageParams(&filter.Page, &filter.PageSize)

	query := s.db.Table("daily_stats ds").
		Select(`
			ds.tenant_id,
			t.name as tenant_name,
			0 as user_id,
			'' as user_name,
			COALESCE(SUM(ds.total_requests), 0) as total_requests,
			COALESCE(SUM(ds.total_input_tokens), 0) as total_input_tokens,
			COALESCE(SUM(ds.total_output_tokens), 0) as total_output_tokens,
			COALESCE(SUM(ds.total_revenue), 0) as total_revenue
		`).
		Joins("LEFT JOIN tenants t ON t.id = ds.tenant_id").
		Where("ds.date >= ? AND ds.date <= ?", filter.StartDate, filter.EndDate).
		Scopes(permission.TenantScope(ctx))

	if filter.TenantID != nil {
		query = query.Where("ds.tenant_id = ?", *filter.TenantID)
	}

	query = query.Group("ds.tenant_id, t.name")

	var total int64
	s.db.Table("(?) as sub", query).Count(&total)

	var items []ConsumptionItem
	offset := (filter.Page - 1) * filter.PageSize
	if err := query.Order("total_revenue DESC").Offset(offset).Limit(filter.PageSize).Scan(&items).Error; err != nil {
		return nil, 0, fmt.Errorf("failed to get consumption detail: %w", err)
	}

	return items, total, nil
}

// GetKeyUsageDetail 获取指定 API Key 的调用日志
func (s *ReportService) GetKeyUsageDetail(ctx context.Context, keyID uint, page, pageSize int) ([]ChannelLogItem, int64, error) {
	if keyID == 0 {
		return nil, 0, fmt.Errorf("key ID is required")
	}
	normalizePageParams(&page, &pageSize)

	// Verify access permission
	if s.perm != nil {
		ok, err := s.perm.CanAccessApiKey(ctx, keyID)
		if err != nil {
			return nil, 0, fmt.Errorf("permission check failed: %w", err)
		}
		if !ok {
			return nil, 0, fmt.Errorf("access denied to API key %d", keyID)
		}
	}

	// Get key details to find user_id
	var key model.ApiKey
	if err := s.db.Select("user_id, tenant_id").Where("id = ?", keyID).First(&key).Error; err != nil {
		return nil, 0, fmt.Errorf("API key not found: %w", err)
	}

	query := s.db.Table("channel_logs").
		Where("user_id = ? AND tenant_id = ?", key.UserID, key.TenantID)

	var total int64
	query.Count(&total)

	var items []ChannelLogItem
	offset := (page - 1) * pageSize
	if err := query.
		Select("id, model_name, request_tokens, response_tokens, latency_ms, status_code, error_message, request_id, created_at").
		Order("created_at DESC").
		Offset(offset).
		Limit(pageSize).
		Scan(&items).Error; err != nil {
		return nil, 0, fmt.Errorf("failed to get key usage detail: %w", err)
	}

	return items, total, nil
}

// resolvePeriod converts a period name to a date range.
func resolvePeriod(period string) (string, string) {
	now := time.Now()
	endDate := now.Format("2006-01-02")

	switch period {
	case "week":
		return now.AddDate(0, 0, -7).Format("2006-01-02"), endDate
	case "month":
		return now.AddDate(0, -1, 0).Format("2006-01-02"), endDate
	case "quarter":
		return now.AddDate(0, -3, 0).Format("2006-01-02"), endDate
	case "year":
		return now.AddDate(-1, 0, 0).Format("2006-01-02"), endDate
	default: // "today"
		return endDate, endDate
	}
}

// normalizePageParams sets default values for pagination.
func normalizePageParams(page, pageSize *int) {
	if *page < 1 {
		*page = 1
	}
	if *pageSize < 1 {
		*pageSize = 20
	}
	if *pageSize > 100 {
		*pageSize = 100
	}
}
