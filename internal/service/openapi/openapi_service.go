package openapi

import (
	"context"
	"fmt"
	"time"

	"gorm.io/gorm"

	"tokenhub-server/internal/model"
	"tokenhub-server/internal/pkg/credits"
)

// OpenAPIService 提供 Open API 对外接口的业务逻辑层。
type OpenAPIService struct {
	db *gorm.DB
}

// NewOpenAPIService 创建 Open API 服务实例。
func NewOpenAPIService(db *gorm.DB) *OpenAPIService {
	if db == nil {
		panic("openapi service: db is nil")
	}
	return &OpenAPIService{db: db}
}

// --- 消费类 ---

// ConsumptionSummaryItem 消费汇总项
type ConsumptionSummaryItem struct {
	Period        string  `json:"period"`
	TotalRequests int64   `json:"total_requests"`
	TotalTokens   int64   `json:"total_tokens"`
	TotalCost     float64 `json:"total_cost"`
}

// GetConsumptionSummary 获取消费汇总（按日/周/月汇总）。
func (s *OpenAPIService) GetConsumptionSummary(ctx context.Context, userID uint, dateFrom, dateTo string, groupBy string) ([]ConsumptionSummaryItem, error) {
	if groupBy == "" {
		groupBy = "day"
	}

	var dateFmt string
	switch groupBy {
	case "week":
		dateFmt = "%%Y-%%u" // year-week
	case "month":
		dateFmt = "%%Y-%%m"
	default:
		dateFmt = "%%Y-%%m-%%d"
	}

	query := s.db.WithContext(ctx).Table("channel_logs").
		Select(fmt.Sprintf("DATE_FORMAT(created_at, '%s') as period, COUNT(*) as total_requests, SUM(request_tokens + response_tokens) as total_tokens, 0 as total_cost", dateFmt)).
		Where("user_id = ? AND status_code = 200", userID).
		Group("period").
		Order("period ASC")

	if dateFrom != "" {
		query = query.Where("created_at >= ?", dateFrom)
	}
	if dateTo != "" {
		query = query.Where("created_at <= ?", dateTo+" 23:59:59")
	}

	var results []ConsumptionSummaryItem
	if err := query.Find(&results).Error; err != nil {
		return nil, fmt.Errorf("query consumption summary: %w", err)
	}
	return results, nil
}

// ConsumptionDetailItem 消费明细项
type ConsumptionDetailItem struct {
	ID             uint      `json:"id"`
	ModelName      string    `json:"model_name"`
	RequestTokens  int       `json:"request_tokens"`
	ResponseTokens int       `json:"response_tokens"`
	LatencyMs      int       `json:"latency_ms"`
	StatusCode     int       `json:"status_code"`
	RequestID      string    `json:"request_id"`
	CreatedAt      time.Time `json:"created_at"`
}

// GetConsumptionDetails 获取消费明细列表（分页，支持模型/日期过滤）。
func (s *OpenAPIService) GetConsumptionDetails(ctx context.Context, userID uint, modelName, dateFrom, dateTo string, page, pageSize int) ([]ConsumptionDetailItem, int64, error) {
	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > 100 {
		pageSize = 20
	}

	query := s.db.WithContext(ctx).Table("channel_logs").Where("user_id = ?", userID)
	if modelName != "" {
		query = query.Where("model_name = ?", modelName)
	}
	if dateFrom != "" {
		query = query.Where("created_at >= ?", dateFrom)
	}
	if dateTo != "" {
		query = query.Where("created_at <= ?", dateTo+" 23:59:59")
	}

	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, fmt.Errorf("count consumption details: %w", err)
	}

	var logs []model.ChannelLog
	offset := (page - 1) * pageSize
	if err := query.Order("created_at DESC").Offset(offset).Limit(pageSize).Find(&logs).Error; err != nil {
		return nil, 0, fmt.Errorf("query consumption details: %w", err)
	}

	items := make([]ConsumptionDetailItem, len(logs))
	for i, log := range logs {
		items[i] = ConsumptionDetailItem{
			ID:             log.ID,
			ModelName:      log.ModelName,
			RequestTokens:  log.RequestTokens,
			ResponseTokens: log.ResponseTokens,
			LatencyMs:      log.LatencyMs,
			StatusCode:     log.StatusCode,
			RequestID:      log.RequestID,
			CreatedAt:      log.CreatedAt,
		}
	}
	return items, total, nil
}

// ExportConsumptionCSV 导出消费数据为 CSV 行。
func (s *OpenAPIService) ExportConsumptionCSV(ctx context.Context, userID uint, dateFrom, dateTo string) ([][]string, error) {
	query := s.db.WithContext(ctx).Table("channel_logs").Where("user_id = ?", userID)
	if dateFrom != "" {
		query = query.Where("created_at >= ?", dateFrom)
	}
	if dateTo != "" {
		query = query.Where("created_at <= ?", dateTo+" 23:59:59")
	}

	var logs []model.ChannelLog
	if err := query.Order("created_at DESC").Limit(10000).Find(&logs).Error; err != nil {
		return nil, fmt.Errorf("export consumption: %w", err)
	}

	rows := [][]string{
		{"ID", "Model", "Request Tokens", "Response Tokens", "Latency(ms)", "Status", "Request ID", "Time"},
	}
	for _, log := range logs {
		rows = append(rows, []string{
			fmt.Sprintf("%d", log.ID),
			log.ModelName,
			fmt.Sprintf("%d", log.RequestTokens),
			fmt.Sprintf("%d", log.ResponseTokens),
			fmt.Sprintf("%d", log.LatencyMs),
			fmt.Sprintf("%d", log.StatusCode),
			log.RequestID,
			log.CreatedAt.Format(time.RFC3339),
		})
	}
	return rows, nil
}

// --- 余额类 ---

// BalanceInfo 余额信息（返回人民币等值金额用于展示）
type BalanceInfo struct {
	Balance       float64 `json:"balance"`        // 余额（人民币）
	FreeQuota     float64 `json:"free_quota"`    // 赠送额度（人民币）
	TotalConsumed float64 `json:"total_consumed"` // 累计消费（人民币）
	FrozenAmount  float64 `json:"frozen_amount"` // 冻结金额（人民币）
	Currency      string  `json:"currency"`      // 币种 CNY
}

// BalanceCreditsInfo 余额信息（积分单位）
type BalanceCreditsInfo struct {
	Balance       int64  `json:"balance"`        // 余额（积分）
	FreeQuota     int64  `json:"free_quota"`    // 赠送额度（积分）
	TotalConsumed int64  `json:"total_consumed"` // 累计消费（积分）
	FrozenAmount  int64  `json:"frozen_amount"` // 冻结金额（积分）
	Currency      string `json:"currency"`      // 币种 CREDIT
}

// GetBalance 获取用户当前余额信息（返回人民币等值金额）。
func (s *OpenAPIService) GetBalance(ctx context.Context, userID uint) (*BalanceInfo, error) {
	var ub model.UserBalance
	err := s.db.WithContext(ctx).Where("user_id = ?", userID).First(&ub).Error
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return &BalanceInfo{Currency: "CNY"}, nil
		}
		return nil, fmt.Errorf("get balance: %w", err)
	}
	// 将积分转换为人民币用于展示
	return &BalanceInfo{
		Balance:       credits.CreditsToRMB(ub.Balance),
		FreeQuota:     credits.CreditsToRMB(ub.FreeQuota),
		TotalConsumed: credits.CreditsToRMB(ub.TotalConsumed),
		FrozenAmount:  credits.CreditsToRMB(ub.FrozenAmount),
		Currency:      "CNY",
	}, nil
}

// RechargeRecordItem 充值记录项（返回人民币等值金额）
type RechargeRecordItem struct {
	ID            uint      `json:"id"`
	Type          string    `json:"type"`
	Amount        float64   `json:"amount"`         // 变动金额（人民币）
	BeforeBalance float64   `json:"before_balance"` // 变动前余额（人民币）
	AfterBalance  float64   `json:"after_balance"`  // 变动后余额（人民币）
	Remark        string    `json:"remark"`
	CreatedAt     time.Time `json:"created_at"`
}

// GetRechargeRecords 获取充值记录列表。
func (s *OpenAPIService) GetRechargeRecords(ctx context.Context, userID uint, page, pageSize int) ([]RechargeRecordItem, int64, error) {
	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > 100 {
		pageSize = 20
	}

	query := s.db.WithContext(ctx).Model(&model.BalanceRecord{}).
		Where("user_id = ? AND type IN ?", userID, []string{"RECHARGE", "GIFT", "ADMIN_ADJUST"})

	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, fmt.Errorf("count recharge records: %w", err)
	}

	var records []model.BalanceRecord
	offset := (page - 1) * pageSize
	if err := query.Order("created_at DESC").Offset(offset).Limit(pageSize).Find(&records).Error; err != nil {
		return nil, 0, fmt.Errorf("query recharge records: %w", err)
	}

	items := make([]RechargeRecordItem, len(records))
	for i, r := range records {
		// 将积分转换为人民币用于展示
		items[i] = RechargeRecordItem{
			ID:            r.ID,
			Type:          r.Type,
			Amount:        credits.CreditsToRMB(r.Amount),
			BeforeBalance: credits.CreditsToRMB(r.BeforeBalance),
			AfterBalance:  credits.CreditsToRMB(r.AfterBalance),
			Remark:        r.Remark,
			CreatedAt:     r.CreatedAt,
		}
	}
	return items, total, nil
}

// --- 用量类 ---

// UsageStatItem 用量统计项（按模型分组）
type UsageStatItem struct {
	ModelName      string `json:"model_name"`
	TotalRequests  int64  `json:"total_requests"`
	TotalInputTokens  int64  `json:"total_input_tokens"`
	TotalOutputTokens int64  `json:"total_output_tokens"`
	TotalTokens    int64  `json:"total_tokens"`
}

// GetUsageStatistics 获取用量统计（按模型分组）。
func (s *OpenAPIService) GetUsageStatistics(ctx context.Context, userID uint, dateFrom, dateTo string) ([]UsageStatItem, error) {
	query := s.db.WithContext(ctx).Table("channel_logs").
		Select("model_name, COUNT(*) as total_requests, SUM(request_tokens) as total_input_tokens, SUM(response_tokens) as total_output_tokens, SUM(request_tokens + response_tokens) as total_tokens").
		Where("user_id = ?", userID).
		Group("model_name").
		Order("total_tokens DESC")

	if dateFrom != "" {
		query = query.Where("created_at >= ?", dateFrom)
	}
	if dateTo != "" {
		query = query.Where("created_at <= ?", dateTo+" 23:59:59")
	}

	var results []UsageStatItem
	if err := query.Find(&results).Error; err != nil {
		return nil, fmt.Errorf("query usage statistics: %w", err)
	}
	return results, nil
}

// UsageTrendItem 用量趋势项（每日）
type UsageTrendItem struct {
	Date          string `json:"date"`
	TotalRequests int64  `json:"total_requests"`
	TotalTokens   int64  `json:"total_tokens"`
	InputTokens   int64  `json:"input_tokens"`
	OutputTokens  int64  `json:"output_tokens"`
}

// GetUsageTrend 获取用量趋势（每日 Token 消费趋势图数据）。
func (s *OpenAPIService) GetUsageTrend(ctx context.Context, userID uint, dateFrom, dateTo string) ([]UsageTrendItem, error) {
	// 默认查询近30天
	if dateFrom == "" {
		dateFrom = time.Now().AddDate(0, 0, -30).Format("2006-01-02")
	}
	if dateTo == "" {
		dateTo = time.Now().Format("2006-01-02")
	}

	query := s.db.WithContext(ctx).Table("channel_logs").
		Select("DATE(created_at) as date, COUNT(*) as total_requests, SUM(request_tokens + response_tokens) as total_tokens, SUM(request_tokens) as input_tokens, SUM(response_tokens) as output_tokens").
		Where("user_id = ? AND created_at >= ? AND created_at <= ?", userID, dateFrom, dateTo+" 23:59:59").
		Group("DATE(created_at)").
		Order("date ASC")

	var results []UsageTrendItem
	if err := query.Find(&results).Error; err != nil {
		return nil, fmt.Errorf("query usage trend: %w", err)
	}
	return results, nil
}

// --- 模型定价类 ---

// ModelPricingItem 模型定价项
type ModelPricingItem struct {
	ModelID             uint    `json:"model_id"`
	ModelName           string  `json:"model_name"`
	DisplayName         string  `json:"display_name"`
	SupplierName        string  `json:"supplier_name"`
	CategoryName        string  `json:"category_name"`
	InputPricePerToken  float64 `json:"input_price_per_token"`
	OutputPricePerToken float64 `json:"output_price_per_token"`
	Currency            string  `json:"currency"`
	MaxTokens           int     `json:"max_tokens"`
	ContextWindow       int     `json:"context_window"`
}

// GetModelPricingList 获取模型定价列表。
func (s *OpenAPIService) GetModelPricingList(ctx context.Context) ([]ModelPricingItem, error) {
	var models []model.AIModel
	err := s.db.WithContext(ctx).
		Preload("Supplier").
		Preload("Category").
		Where("is_active = ?", true).
		Order("supplier_id, model_name").
		Find(&models).Error
	if err != nil {
		return nil, fmt.Errorf("query model pricing: %w", err)
	}

	// 获取模型定价
	var pricings []model.ModelPricing
	_ = s.db.WithContext(ctx).Find(&pricings).Error
	pricingMap := make(map[uint]*model.ModelPricing)
	for i := range pricings {
		pricingMap[pricings[i].ModelID] = &pricings[i]
	}

	items := make([]ModelPricingItem, len(models))
	for i, m := range models {
		item := ModelPricingItem{
			ModelID:       m.ID,
			ModelName:     m.ModelName,
			DisplayName:   m.DisplayName,
			MaxTokens:     m.MaxTokens,
			ContextWindow: m.ContextWindow,
		}
		if m.Supplier.ID > 0 {
			item.SupplierName = m.Supplier.Name
		}
		if m.Category.ID > 0 {
			item.CategoryName = m.Category.Name
		}
		// 优先使用 ModelPricing 表的定价，否则用 AIModel 自身的定价
		// 定价字段为积分单位（每百万token），转换为人民币用于展示
		if p, ok := pricingMap[m.ID]; ok {
			item.InputPricePerToken = credits.CreditsToRMB(p.InputPricePerToken)
			item.OutputPricePerToken = credits.CreditsToRMB(p.OutputPricePerToken)
			item.Currency = "CNY"
		} else {
			item.InputPricePerToken = credits.CreditsToRMB(m.InputPricePerToken)
			item.OutputPricePerToken = credits.CreditsToRMB(m.OutputPricePerToken)
			item.Currency = "CNY"
		}
		items[i] = item
	}
	return items, nil
}

// --- 账户类 ---

// AccountInfo 账户基本信息
type AccountInfo struct {
	UserID   uint   `json:"user_id"`
	Name     string `json:"name"`
	Email    string `json:"email"`
	Role     string `json:"role"`
	Language string `json:"language"`
	IsActive bool   `json:"is_active"`
}

// GetAccountInfo 获取账户基本信息。
func (s *OpenAPIService) GetAccountInfo(ctx context.Context, userID uint) (*AccountInfo, error) {
	var user model.User
	err := s.db.WithContext(ctx).Where("id = ?", userID).First(&user).Error
	if err != nil {
		return nil, fmt.Errorf("get account info: %w", err)
	}
	return &AccountInfo{
		UserID:   user.ID,
		Name:     user.Name,
		Email:    user.Email,
		Role:     user.Role,
		Language: user.Language,
		IsActive: user.IsActive,
	}, nil
}

// KeyItem API Key 列表项
type KeyItem struct {
	ID         uint       `json:"id"`
	Name       string     `json:"name"`
	KeyPrefix  string     `json:"key_prefix"`
	IsActive   bool       `json:"is_active"`
	ExpiresAt  *time.Time `json:"expires_at,omitempty"`
	LastUsedAt *time.Time `json:"last_used_at,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
}

// GetAPIKeys 获取用户的 API Key 列表。
func (s *OpenAPIService) GetAPIKeys(ctx context.Context, userID uint) ([]KeyItem, error) {
	var keys []model.ApiKey
	err := s.db.WithContext(ctx).Where("user_id = ?", userID).Order("id DESC").Find(&keys).Error
	if err != nil {
		return nil, fmt.Errorf("get api keys: %w", err)
	}

	items := make([]KeyItem, len(keys))
	for i, k := range keys {
		items[i] = KeyItem{
			ID:         k.ID,
			Name:       k.Name,
			KeyPrefix:  k.KeyPrefix,
			IsActive:   k.IsActive,
			ExpiresAt:  k.ExpiresAt,
			LastUsedAt: k.LastUsedAt,
			CreatedAt:  k.CreatedAt,
		}
	}
	return items, nil
}

// KeyUsageInfo 单个 Key 用量信息
type KeyUsageInfo struct {
	KeyID          uint   `json:"key_id"`
	KeyName        string `json:"key_name"`
	TotalRequests  int64  `json:"total_requests"`
	TotalTokens    int64  `json:"total_tokens"`
	InputTokens    int64  `json:"input_tokens"`
	OutputTokens   int64  `json:"output_tokens"`
}

// GetKeyUsage 获取单个 API Key 的用量信息。
// 注意: channel_logs 表没有 api_key_id 字段，所以这里通过 user_id 查询所有用量。
// 后续可以扩展 channel_logs 表添加 api_key_id 字段。
func (s *OpenAPIService) GetKeyUsage(ctx context.Context, userID, keyID uint) (*KeyUsageInfo, error) {
	// 验证 key 归属
	var apiKey model.ApiKey
	err := s.db.WithContext(ctx).Where("id = ? AND user_id = ?", keyID, userID).First(&apiKey).Error
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, fmt.Errorf("api key not found")
		}
		return nil, fmt.Errorf("get key usage: %w", err)
	}

	// 查询该用户的总用量作为近似值
	var result struct {
		TotalRequests int64 `json:"total_requests"`
		InputTokens   int64 `json:"input_tokens"`
		OutputTokens  int64 `json:"output_tokens"`
	}
	s.db.WithContext(ctx).Table("channel_logs").
		Select("COUNT(*) as total_requests, COALESCE(SUM(request_tokens),0) as input_tokens, COALESCE(SUM(response_tokens),0) as output_tokens").
		Where("user_id = ?", userID).
		Scan(&result)

	return &KeyUsageInfo{
		KeyID:         apiKey.ID,
		KeyName:       apiKey.Name,
		TotalRequests: result.TotalRequests,
		TotalTokens:   result.InputTokens + result.OutputTokens,
		InputTokens:   result.InputTokens,
		OutputTokens:  result.OutputTokens,
	}, nil
}
