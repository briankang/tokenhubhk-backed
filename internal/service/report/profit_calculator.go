package report

import (
	"context"
	"fmt"
	"time"

	"gorm.io/gorm"

	"tokenhub-server/internal/service/permission"
)

// ProfitCalculator 利润计算器，基于 DailyStats 预聚合数据计算利润指标
type ProfitCalculator struct {
	db *gorm.DB
}

// NewProfitCalculator 创建利润计算器实例
func NewProfitCalculator(db *gorm.DB) *ProfitCalculator {
	if db == nil {
		panic("profit calculator: db is nil")
	}
	return &ProfitCalculator{db: db}
}

// ProfitResult 聚合利润数据
type ProfitResult struct {
	TotalRevenue      float64 `json:"total_revenue"`
	TotalCost         float64 `json:"total_cost"`
	GrossProfit       float64 `json:"gross_profit"`
	ProfitMargin      float64 `json:"profit_margin"`
	RequestCount      int64   `json:"request_count"`
	TotalInputTokens  int64   `json:"total_input_tokens"`
	TotalOutputTokens int64   `json:"total_output_tokens"`
	ErrorCount        int64   `json:"error_count"`
}

// ProfitFilter 利润计算的查询参数
type ProfitFilter struct {
	TenantID   *uint  `json:"tenant_id,omitempty"`
	ModelID    *uint  `json:"model_id,omitempty"`
	ChannelID  *uint  `json:"channel_id,omitempty"`
	StartDate  string `json:"start_date"`
	EndDate    string `json:"end_date"`
	GroupBy    string `json:"group_by"` // day / week / month / year
	AgentLevel *int   `json:"agent_level,omitempty"`
}

// ProfitTrendItem 利润趋势中的单个数据点
type ProfitTrendItem struct {
	Period            string  `json:"period"`
	TotalRevenue      float64 `json:"total_revenue"`
	TotalCost         float64 `json:"total_cost"`
	GrossProfit       float64 `json:"gross_profit"`
	ProfitMargin      float64 `json:"profit_margin"`
	RequestCount      int64   `json:"request_count"`
	TotalInputTokens  int64   `json:"total_input_tokens"`
	TotalOutputTokens int64   `json:"total_output_tokens"`
}

// AgentProfitRank 单个代理商的利润贡献排名
type AgentProfitRank struct {
	TenantID     uint    `json:"tenant_id"`
	TenantName   string  `json:"tenant_name"`
	AgentLevel   int     `json:"agent_level"`
	TotalRevenue float64 `json:"total_revenue"`
	TotalCost    float64 `json:"total_cost"`
	GrossProfit  float64 `json:"gross_profit"`
	RequestCount int64   `json:"request_count"`
}

// AgentDrillDownItem 直属子代理的利润明细
type AgentDrillDownItem struct {
	TenantID      uint    `json:"tenant_id"`
	TenantName    string  `json:"tenant_name"`
	Level         int     `json:"level"`
	TotalRevenue  float64 `json:"total_revenue"`
	TotalCost     float64 `json:"total_cost"`
	GrossProfit   float64 `json:"gross_profit"`
	ProfitMargin  float64 `json:"profit_margin"`
	RequestCount  int64   `json:"request_count"`
	ChildrenCount int     `json:"children_count"`
}

// CalculateProfit 根据过滤条件计算利润，按上下文权限限定范围
func (c *ProfitCalculator) CalculateProfit(ctx context.Context, filter ProfitFilter) (*ProfitResult, error) {
	if err := validateDateRange(filter.StartDate, filter.EndDate); err != nil {
		return nil, err
	}

	query := c.db.Table("daily_stats").
		Select(`
			COALESCE(SUM(total_revenue), 0) as total_revenue,
			COALESCE(SUM(total_cost), 0) as total_cost,
			COALESCE(SUM(total_requests), 0) as request_count,
			COALESCE(SUM(total_input_tokens), 0) as total_input_tokens,
			COALESCE(SUM(total_output_tokens), 0) as total_output_tokens,
			COALESCE(SUM(error_count), 0) as error_count
		`).
		Where("date >= ? AND date <= ?", filter.StartDate, filter.EndDate).
		Scopes(permission.TenantScope(ctx))

	query = applyProfitFilters(query, filter)

	var result ProfitResult
	if err := query.Scan(&result).Error; err != nil {
		return nil, fmt.Errorf("failed to calculate profit: %w", err)
	}

	result.GrossProfit = result.TotalRevenue - result.TotalCost
	if result.TotalRevenue > 0 {
		result.ProfitMargin = result.GrossProfit / result.TotalRevenue * 100
	}

	return &result, nil
}

// GetProfitTrend 获取按指定时间维度聚合的利润趋势数据
func (c *ProfitCalculator) GetProfitTrend(ctx context.Context, filter ProfitFilter) ([]ProfitTrendItem, error) {
	if err := validateDateRange(filter.StartDate, filter.EndDate); err != nil {
		return nil, err
	}

	groupExpr := dateGroupExpression(filter.GroupBy)

	query := c.db.Table("daily_stats").
		Select(fmt.Sprintf(`
			%s as period,
			COALESCE(SUM(total_revenue), 0) as total_revenue,
			COALESCE(SUM(total_cost), 0) as total_cost,
			COALESCE(SUM(total_requests), 0) as request_count,
			COALESCE(SUM(total_input_tokens), 0) as total_input_tokens,
			COALESCE(SUM(total_output_tokens), 0) as total_output_tokens
		`, groupExpr)).
		Where("date >= ? AND date <= ?", filter.StartDate, filter.EndDate).
		Scopes(permission.TenantScope(ctx)).
		Group("period").
		Order("period ASC")

	query = applyProfitFilters(query, filter)

	var items []ProfitTrendItem
	if err := query.Scan(&items).Error; err != nil {
		return nil, fmt.Errorf("failed to get profit trend: %w", err)
	}

	// Compute derived fields
	for i := range items {
		items[i].GrossProfit = items[i].TotalRevenue - items[i].TotalCost
		if items[i].TotalRevenue > 0 {
			items[i].ProfitMargin = items[i].GrossProfit / items[i].TotalRevenue * 100
		}
	}

	return items, nil
}

// GetTopAgents 获取按毛利贡献排名的 Top N 代理商
func (c *ProfitCalculator) GetTopAgents(ctx context.Context, startDate, endDate string, limit int) ([]AgentProfitRank, error) {
	if err := validateDateRange(startDate, endDate); err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = 10
	}

	query := c.db.Table("daily_stats ds").
		Select(`
			ds.tenant_id,
			t.name as tenant_name,
			t.level as agent_level,
			COALESCE(SUM(ds.total_revenue), 0) as total_revenue,
			COALESCE(SUM(ds.total_cost), 0) as total_cost,
			COALESCE(SUM(ds.total_revenue) - SUM(ds.total_cost), 0) as gross_profit,
			COALESCE(SUM(ds.total_requests), 0) as request_count
		`).
		Joins("LEFT JOIN tenants t ON t.id = ds.tenant_id").
		Where("ds.date >= ? AND ds.date <= ?", startDate, endDate).
		Scopes(permission.TenantScope(ctx)).
		Group("ds.tenant_id, t.name, t.level").
		Order("gross_profit DESC").
		Limit(limit)

	var items []AgentProfitRank
	if err := query.Scan(&items).Error; err != nil {
		return nil, fmt.Errorf("failed to get top agents: %w", err)
	}

	return items, nil
}

// GetAgentDrillDown 获取指定租户直属子代理的利润明细
func (c *ProfitCalculator) GetAgentDrillDown(ctx context.Context, tenantID uint, startDate, endDate string) ([]AgentDrillDownItem, error) {
	if tenantID == 0 {
		return nil, fmt.Errorf("tenantID must not be zero")
	}
	if err := validateDateRange(startDate, endDate); err != nil {
		return nil, err
	}

	// Get direct children
	var children []struct {
		ID   uint
		Name string
		Level int
	}
	if err := c.db.Table("tenants").
		Select("id, name, level").
		Where("parent_id = ? AND deleted_at IS NULL", tenantID).
		Scan(&children).Error; err != nil {
		return nil, fmt.Errorf("failed to get child tenants: %w", err)
	}

	if len(children) == 0 {
		return []AgentDrillDownItem{}, nil
	}

	childIDs := make([]uint, len(children))
	childMap := make(map[uint]struct{ Name string; Level int })
	for i, ch := range children {
		childIDs[i] = ch.ID
		childMap[ch.ID] = struct{ Name string; Level int }{ch.Name, ch.Level}
	}

	// Aggregate stats for each direct child (including their subtrees)
	var stats []struct {
		TenantID     uint
		TotalRevenue float64
		TotalCost    float64
		RequestCount int64
	}
	if err := c.db.Table("daily_stats").
		Select(`
			tenant_id,
			COALESCE(SUM(total_revenue), 0) as total_revenue,
			COALESCE(SUM(total_cost), 0) as total_cost,
			COALESCE(SUM(total_requests), 0) as request_count
		`).
		Where("tenant_id IN ? AND date >= ? AND date <= ?", childIDs, startDate, endDate).
		Group("tenant_id").
		Scan(&stats).Error; err != nil {
		return nil, fmt.Errorf("failed to get drilldown stats: %w", err)
	}

	statsMap := make(map[uint]struct{ Revenue, Cost float64; Count int64 })
	for _, s := range stats {
		statsMap[s.TenantID] = struct{ Revenue, Cost float64; Count int64 }{s.TotalRevenue, s.TotalCost, s.RequestCount}
	}

	// Count grandchildren for each child
	var childCounts []struct {
		ParentID uint
		Count    int
	}
	if err := c.db.Table("tenants").
		Select("parent_id, COUNT(*) as count").
		Where("parent_id IN ? AND deleted_at IS NULL", childIDs).
		Group("parent_id").
		Scan(&childCounts).Error; err != nil {
		return nil, fmt.Errorf("failed to count grandchildren: %w", err)
	}

	countMap := make(map[uint]int)
	for _, cc := range childCounts {
		countMap[cc.ParentID] = cc.Count
	}

	// Assemble results
	result := make([]AgentDrillDownItem, 0, len(children))
	for _, ch := range children {
		item := AgentDrillDownItem{
			TenantID:      ch.ID,
			TenantName:    childMap[ch.ID].Name,
			Level:         childMap[ch.ID].Level,
			ChildrenCount: countMap[ch.ID],
		}
		if s, ok := statsMap[ch.ID]; ok {
			item.TotalRevenue = s.Revenue
			item.TotalCost = s.Cost
			item.RequestCount = s.Count
			item.GrossProfit = s.Revenue - s.Cost
			if s.Revenue > 0 {
				item.ProfitMargin = item.GrossProfit / s.Revenue * 100
			}
		}
		result = append(result, item)
	}

	return result, nil
}

// applyProfitFilters 向查询添加可选的过滤条件
func applyProfitFilters(query *gorm.DB, filter ProfitFilter) *gorm.DB {
	if filter.TenantID != nil {
		query = query.Where("tenant_id = ?", *filter.TenantID)
	}
	if filter.ModelID != nil {
		query = query.Where("model_id = ?", *filter.ModelID)
	}
	if filter.ChannelID != nil {
		query = query.Where("channel_id = ?", *filter.ChannelID)
	}
	if filter.AgentLevel != nil {
		query = query.Where("agent_level = ?", *filter.AgentLevel)
	}
	return query
}

// dateGroupExpression 返回指定粒度的 MySQL 日期分组表达式
func dateGroupExpression(groupBy string) string {
	switch groupBy {
	case "week":
		return "DATE_FORMAT(date, '%x-W%v')"
	case "month":
		return "DATE_FORMAT(date, '%Y-%m')"
	case "year":
		return "DATE_FORMAT(date, '%Y')"
	default: // "day" or empty
		return "date"
	}
}

// validateDateRange 校验起止日期是否有效
func validateDateRange(startDate, endDate string) error {
	if startDate == "" || endDate == "" {
		return fmt.Errorf("start_date and end_date are required")
	}
	s, err := time.Parse("2006-01-02", startDate)
	if err != nil {
		return fmt.Errorf("invalid start_date format, expected YYYY-MM-DD: %w", err)
	}
	e, err := time.Parse("2006-01-02", endDate)
	if err != nil {
		return fmt.Errorf("invalid end_date format, expected YYYY-MM-DD: %w", err)
	}
	if s.After(e) {
		return fmt.Errorf("start_date must not be after end_date")
	}
	return nil
}
