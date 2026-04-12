package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"time"

	goredis "github.com/redis/go-redis/v9"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"tokenhub-server/internal/model"
	"tokenhub-server/internal/pkg/credits"
)

const (
	// agentLevelCacheKey 代理等级配置缓存键
	agentLevelCacheKey = "agent:levels:all"
	// agentLevelCacheTTL 等级配置缓存时长（2小时）
	agentLevelCacheTTL = 2 * time.Hour
	// agentProfileCachePrefix 代理档案缓存前缀
	agentProfileCachePrefix = "agent:profile:"
	// agentProfileCacheTTL 代理档案缓存时长（5分钟）
	agentProfileCacheTTL = 5 * time.Minute
)

// ---------- 响应结构体 ----------

// AgentProfileResponse 代理档案响应
type AgentProfileResponse struct {
	UserID            uint                    `json:"user_id"`
	Profile           model.UserAgentProfile  `json:"profile"`
	Level             model.AgentLevel        `json:"level"`
	NextLevel         *model.AgentLevel       `json:"next_level,omitempty"`
	AvailableBalance  float64                 `json:"available_balance"`  // 可提现金额
	PendingCommission float64                 `json:"pending_commission"` // 待结算佣金
}

// AgentUpgradeProgressResponse 代理升级进度响应
type AgentUpgradeProgressResponse struct {
	CurrentLevel  string  `json:"current_level"`
	NextLevel     string  `json:"next_level"`
	MonthlySales  float64 `json:"monthly_sales"`
	RequiredSales float64 `json:"required_sales"`
	SalesProgress float64 `json:"sales_progress"` // 百分比
	DirectSubs    int     `json:"direct_subs"`
	RequiredSubs  int     `json:"required_subs"`
	SubsProgress  float64 `json:"subs_progress"` // 百分比
}

// TeamTreeNode 团队树节点
type TeamTreeNode struct {
	UserID     uint           `json:"user_id"`
	Name       string         `json:"name"`
	Email      string         `json:"email"`
	Level      string         `json:"level"`       // 代理等级
	MonthSpend float64        `json:"month_spend"` // 本月消费
	TotalSpend float64        `json:"total_spend"` // 累计消费
	JoinedAt   time.Time      `json:"joined_at"`
	Children   []TeamTreeNode `json:"children,omitempty"`
}

// TeamTreeResponse 团队树响应
type TeamTreeResponse struct {
	Root       TeamTreeNode `json:"root"`
	TotalCount int          `json:"total_count"`
}

// TeamStatsResponse 团队统计响应
type TeamStatsResponse struct {
	DirectCount     int     `json:"direct_count"`      // 直推人数
	TotalCount      int     `json:"total_count"`       // 团队总人数
	MonthSales      float64 `json:"month_sales"`       // 本月团队销售额
	LastMonthSales  float64 `json:"last_month_sales"`  // 上月团队销售额
	TotalEarnings   float64 `json:"total_earnings"`    // 累计收益
	PendingAmount   float64 `json:"pending_amount"`    // 待结算
	SettledAmount   float64 `json:"settled_amount"`    // 已结算可提现
	WithdrawnAmount float64 `json:"withdrawn_amount"`  // 已提现
}

// ---------- 服务结构体 ----------

// AgentLevelService 代理等级服务，管理代理申请、审核、升降级、提现等
type AgentLevelService struct {
	db    *gorm.DB
	redis *goredis.Client
}

// NewAgentLevelService 创建代理等级服务实例
func NewAgentLevelService(db *gorm.DB, redis *goredis.Client) *AgentLevelService {
	return &AgentLevelService{db: db, redis: redis}
}

// roundTo6 将浮点数四舍五入到 6 位小数
func roundTo6(v float64) float64 {
	return math.Round(v*1e6) / 1e6
}

// ========== 代理申请与审核 ==========

// ApplyAgent 申请成为代理（免费申请，默认A0推广员，状态PENDING）
// 如果已有档案则返回已有档案
func (s *AgentLevelService) ApplyAgent(ctx context.Context, userID uint) (*model.UserAgentProfile, error) {
	// 检查是否已有档案
	var existing model.UserAgentProfile
	if err := s.db.WithContext(ctx).Where("user_id = ?", userID).First(&existing).Error; err == nil {
		return &existing, nil // 已存在，返回现有档案
	}

	// 查找默认代理等级 A0
	var defaultLevel model.AgentLevel
	if err := s.db.WithContext(ctx).Where("level_code = ? AND is_active = ?", "A0", true).First(&defaultLevel).Error; err != nil {
		// 查找 rank 最低的等级
		if err := s.db.WithContext(ctx).Where("is_active = ?", true).Order("level_rank ASC").First(&defaultLevel).Error; err != nil {
			return nil, fmt.Errorf("未找到默认代理等级: %w", err)
		}
	}

	now := time.Now()
	profile := &model.UserAgentProfile{
		UserID:       userID,
		AgentLevelID: defaultLevel.ID,
		Status:       "PENDING",
		AppliedAt:    now,
	}
	if err := s.db.WithContext(ctx).Create(profile).Error; err != nil {
		return nil, fmt.Errorf("创建代理档案失败: %w", err)
	}

	return profile, nil
}

// ApproveAgent 管理员审核通过代理申请
func (s *AgentLevelService) ApproveAgent(ctx context.Context, profileID uint, adminID uint) error {
	now := time.Now()
	result := s.db.WithContext(ctx).Model(&model.UserAgentProfile{}).
		Where("id = ? AND status = 'PENDING'", profileID).
		Updates(map[string]interface{}{
			"status":      "ACTIVE",
			"approved_at": now,
			"approved_by": adminID,
		})
	if result.Error != nil {
		return fmt.Errorf("审核通过失败: %w", result.Error)
	}
	if result.RowsAffected == 0 {
		return fmt.Errorf("代理申请不存在或状态不是PENDING")
	}
	return nil
}

// RejectAgent 管理员拒绝代理申请
func (s *AgentLevelService) RejectAgent(ctx context.Context, profileID uint, adminID uint, reason string) error {
	result := s.db.WithContext(ctx).Model(&model.UserAgentProfile{}).
		Where("id = ? AND status = 'PENDING'", profileID).
		Updates(map[string]interface{}{
			"status":      "SUSPENDED",
			"approved_by": adminID,
		})
	if result.Error != nil {
		return fmt.Errorf("拒绝申请失败: %w", result.Error)
	}
	if result.RowsAffected == 0 {
		return fmt.Errorf("代理申请不存在或状态不是PENDING")
	}
	return nil
}

// ========== 档案查询 ==========

// GetProfile 获取代理档案（含等级信息和可提现金额）
func (s *AgentLevelService) GetProfile(ctx context.Context, userID uint) (*AgentProfileResponse, error) {
	var profile model.UserAgentProfile
	if err := s.db.WithContext(ctx).
		Preload("AgentLevel").
		Where("user_id = ?", userID).
		First(&profile).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, fmt.Errorf("代理档案不存在")
		}
		return nil, fmt.Errorf("查询代理档案失败: %w", err)
	}

	// 查询佣金状态分组
	settledAmount, pendingAmount := s.getCommissionAmounts(ctx, userID)

	// 计算可提现金额：已结算佣金 - 已申请提现（PENDING状态）的金额
	var pendingWithdrawals float64
	s.db.WithContext(ctx).Model(&model.WithdrawalRequest{}).
		Where("user_id = ? AND status = 'PENDING'", userID).
		Select("COALESCE(SUM(amount), 0)").Scan(&pendingWithdrawals)

	availableBalance := roundTo6(settledAmount - pendingWithdrawals)
	if availableBalance < 0 {
		availableBalance = 0
	}

	// 获取所有等级，找到下一级
	levels, _ := s.getAllLevelsFromDB(ctx)
	resp := &AgentProfileResponse{
		UserID:            userID,
		Profile:           profile,
		Level:             profile.AgentLevel,
		AvailableBalance:  availableBalance,
		PendingCommission: pendingAmount,
	}
	for i := range levels {
		if levels[i].Rank == profile.AgentLevel.Rank+1 {
			resp.NextLevel = &levels[i]
			break
		}
	}

	return resp, nil
}

// GetAllLevels 获取所有代理等级配置（带 Redis 缓存）
func (s *AgentLevelService) GetAllLevels(ctx context.Context) ([]model.AgentLevel, error) {
	// 尝试从 Redis 缓存读取
	if s.redis != nil {
		val, err := s.redis.Get(ctx, agentLevelCacheKey).Bytes()
		if err == nil {
			var levels []model.AgentLevel
			if json.Unmarshal(val, &levels) == nil {
				return levels, nil
			}
		}
	}

	levels, err := s.getAllLevelsFromDB(ctx)
	if err != nil {
		return nil, err
	}

	// 写入缓存
	if s.redis != nil {
		if data, err := json.Marshal(levels); err == nil {
			_ = s.redis.Set(ctx, agentLevelCacheKey, data, agentLevelCacheTTL).Err()
		}
	}

	return levels, nil
}

// GetUpgradeProgress 获取代理升级进度
func (s *AgentLevelService) GetUpgradeProgress(ctx context.Context, userID uint) (*AgentUpgradeProgressResponse, error) {
	var profile model.UserAgentProfile
	if err := s.db.WithContext(ctx).
		Preload("AgentLevel").
		Where("user_id = ?", userID).
		First(&profile).Error; err != nil {
		return nil, fmt.Errorf("代理档案不存在: %w", err)
	}

	// 计算团队月销售额
	monthlySales, _ := s.GetTeamMonthlySales(ctx, userID)
	directSubs, _ := s.GetDirectSubsCount(ctx, userID)

	levels, _ := s.getAllLevelsFromDB(ctx)

	resp := &AgentUpgradeProgressResponse{
		CurrentLevel: profile.AgentLevel.LevelCode,
		MonthlySales: monthlySales,
		DirectSubs:   directSubs,
	}

	// 查找下一级
	for i := range levels {
		if levels[i].Rank == profile.AgentLevel.Rank+1 {
			resp.NextLevel = levels[i].LevelCode
			// MinMonthlySales 为积分单位，转换为人民币
			resp.RequiredSales = credits.CreditsToRMB(levels[i].MinMonthlySales)
			resp.RequiredSubs = levels[i].MinDirectSubs

			// 计算销售额进度
			if resp.RequiredSales > 0 {
				resp.SalesProgress = roundTo6(monthlySales / resp.RequiredSales * 100)
				if resp.SalesProgress > 100 {
					resp.SalesProgress = 100
				}
			}
			// 计算下线人数进度
			if resp.RequiredSubs > 0 {
				resp.SubsProgress = roundTo6(float64(directSubs) / float64(resp.RequiredSubs) * 100)
				if resp.SubsProgress > 100 {
					resp.SubsProgress = 100
				}
			}
			break
		}
	}

	// 已达最高等级
	if resp.NextLevel == "" {
		resp.NextLevel = "MAX"
		resp.SalesProgress = 100
		resp.SubsProgress = 100
	}

	return resp, nil
}

// ========== 升降级定时任务 ==========

// CheckAndUpgradeAll 定时任务：月末批量检查代理升级
// 逻辑：查询所有ACTIVE代理 → 计算团队月销售额和直推人数 → 与等级门槛对比
func (s *AgentLevelService) CheckAndUpgradeAll(ctx context.Context) error {
	var profiles []model.UserAgentProfile
	if err := s.db.WithContext(ctx).
		Preload("AgentLevel").
		Where("status = 'ACTIVE'").
		Find(&profiles).Error; err != nil {
		return fmt.Errorf("查询代理档案失败: %w", err)
	}

	levels, err := s.getAllLevelsFromDB(ctx)
	if err != nil {
		return err
	}

	for _, profile := range profiles {
		// 计算团队月销售额
		monthlySales, _ := s.GetTeamMonthlySales(ctx, profile.UserID)
		directSubs, _ := s.GetDirectSubsCount(ctx, profile.UserID)

		// 从最高等级开始匹配
		var targetLevel *model.AgentLevel
		for i := len(levels) - 1; i >= 0; i-- {
			// MinMonthlySales 为积分单位，需转换月销售额比较
			monthlySalesCredits := credits.RMBToCredits(monthlySales)
			if monthlySalesCredits >= levels[i].MinMonthlySales && directSubs >= levels[i].MinDirectSubs {
				targetLevel = &levels[i]
				break
			}
		}

		if targetLevel != nil && targetLevel.Rank > profile.AgentLevel.Rank {
			// 执行升级
			s.db.WithContext(ctx).Model(&model.UserAgentProfile{}).
				Where("id = ?", profile.ID).
				Updates(map[string]interface{}{
					"agent_level_id":      targetLevel.ID,
					"current_month_sales": monthlySales,
					"direct_subs_count":   directSubs,
					"degrade_warnings":    0,
				})
			s.invalidateProfileCache(ctx, profile.UserID)
		} else {
			// 仅更新当月数据
			s.db.WithContext(ctx).Model(&model.UserAgentProfile{}).
				Where("id = ?", profile.ID).
				Updates(map[string]interface{}{
					"current_month_sales": monthlySales,
					"direct_subs_count":   directSubs,
				})
		}
	}

	return nil
}

// CheckAndDegradeAll 定时任务：批量检查代理降级
// 逻辑：连续2个月销售额低于当前等级门槛50%则降级
func (s *AgentLevelService) CheckAndDegradeAll(ctx context.Context) error {
	var profiles []model.UserAgentProfile
	if err := s.db.WithContext(ctx).
		Preload("AgentLevel").
		Where("status = 'ACTIVE'").
		Joins("JOIN agent_levels ON agent_levels.id = user_agent_profiles.agent_level_id").
		Where("agent_levels.rank > 0"). // A0 不降级
		Find(&profiles).Error; err != nil {
		return fmt.Errorf("查询代理档案失败: %w", err)
	}

	levels, err := s.getAllLevelsFromDB(ctx)
	if err != nil {
		return err
	}

	now := time.Now()

	for _, profile := range profiles {
		// 获取当月销售额
		monthlySales, _ := s.GetTeamMonthlySales(ctx, profile.UserID)
		// 降级阈值：当前等级门槛的50%（MinMonthlySales为积分单位）
		degradeThresholdCredits := int64(float64(profile.AgentLevel.MinMonthlySales) * 0.5)
		monthlySalesCredits := credits.RMBToCredits(monthlySales)
		
		if monthlySalesCredits < degradeThresholdCredits {
			profile.DegradeWarnings++
		} else {
			profile.DegradeWarnings = 0
		}

		// 连续不达标月数达到阈值，执行降级
		if profile.DegradeWarnings >= profile.AgentLevel.DegradeMonths {
			var lowerLevel *model.AgentLevel
			for i := range levels {
				if levels[i].Rank == profile.AgentLevel.Rank-1 {
					lowerLevel = &levels[i]
					break
				}
			}
			if lowerLevel != nil {
				s.db.WithContext(ctx).Model(&model.UserAgentProfile{}).
					Where("id = ?", profile.ID).
					Updates(map[string]interface{}{
						"agent_level_id":     lowerLevel.ID,
						"degrade_warnings":   0,
						"last_degrade_check": now,
					})
				s.invalidateProfileCache(ctx, profile.UserID)
				continue
			}
		}

		// 更新警告计数和检查时间
		s.db.WithContext(ctx).Model(&model.UserAgentProfile{}).
			Where("id = ?", profile.ID).
			Updates(map[string]interface{}{
				"degrade_warnings":   profile.DegradeWarnings,
				"last_degrade_check": now,
			})
	}

	return nil
}

// ========== 团队数据查询 ==========

// GetTeamMonthlySales 计算用户团队月销售额（自身+直推下线当月消费）
func (s *AgentLevelService) GetTeamMonthlySales(ctx context.Context, userID uint) (float64, error) {
	// 本月1号零点
	now := time.Now()
	monthStart := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location())

	var total float64
	// 查询用户自身 + 直推下线本月消费总额
	// balance_records 中消费为负数（CONSUME类型），取绝对值
	err := s.db.WithContext(ctx).
		Model(&model.BalanceRecord{}).
		Joins("JOIN users u ON balance_records.user_id = u.id").
		Where("(u.id = ? OR u.referred_by = ?)", userID, userID).
		Where("balance_records.type = 'CONSUME'").
		Where("balance_records.created_at >= ?", monthStart).
		Select("COALESCE(SUM(ABS(balance_records.amount)), 0)").
		Scan(&total).Error

	if err != nil {
		return 0, fmt.Errorf("查询团队月销售额失败: %w", err)
	}
	return roundTo6(total), nil
}

// GetDirectSubsCount 获取直推下线人数
func (s *AgentLevelService) GetDirectSubsCount(ctx context.Context, userID uint) (int, error) {
	var count int64
	err := s.db.WithContext(ctx).Model(&model.User{}).
		Where("referred_by = ?", userID).
		Count(&count).Error
	if err != nil {
		return 0, fmt.Errorf("查询直推下线人数失败: %w", err)
	}
	return int(count), nil
}

// GetTeamTree 获取团队树结构（递归查询，最多3层）
func (s *AgentLevelService) GetTeamTree(ctx context.Context, userID uint) (*TeamTreeResponse, error) {
	// 查询当前用户信息
	var user model.User
	if err := s.db.WithContext(ctx).First(&user, userID).Error; err != nil {
		return nil, fmt.Errorf("用户不存在: %w", err)
	}

	now := time.Now()
	monthStart := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location())

	// 构建根节点
	rootMonthSpend := s.getUserMonthSpend(ctx, userID, monthStart)
	rootTotalSpend := s.getUserTotalSpend(ctx, userID)

	rootLevel := ""
	var agentProfile model.UserAgentProfile
	if err := s.db.WithContext(ctx).Preload("AgentLevel").Where("user_id = ?", userID).First(&agentProfile).Error; err == nil {
		rootLevel = agentProfile.AgentLevel.LevelCode
	}

	root := TeamTreeNode{
		UserID:     user.ID,
		Name:       user.Name,
		Email:      user.Email,
		Level:      rootLevel,
		MonthSpend: rootMonthSpend,
		TotalSpend: rootTotalSpend,
		JoinedAt:   user.CreatedAt,
	}

	// 递归构建子节点，最多3层
	totalCount := 0
	root.Children, totalCount = s.buildTreeChildren(ctx, userID, 1, 3, monthStart)

	return &TeamTreeResponse{
		Root:       root,
		TotalCount: totalCount,
	}, nil
}

// GetTeamStats 获取团队统计数据
func (s *AgentLevelService) GetTeamStats(ctx context.Context, userID uint) (*TeamStatsResponse, error) {
	stats := &TeamStatsResponse{}

	// 直推人数
	directCount, _ := s.GetDirectSubsCount(ctx, userID)
	stats.DirectCount = directCount

	// 团队总人数（递归3层）
	stats.TotalCount = s.countTeamMembers(ctx, userID, 1, 3)

	// 本月团队销售额
	stats.MonthSales, _ = s.GetTeamMonthlySales(ctx, userID)

	// 上月团队销售额
	now := time.Now()
	lastMonth := time.Date(now.Year(), now.Month()-1, 1, 0, 0, 0, 0, now.Location())
	thisMonth := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location())
	var lastMonthTotal float64
	s.db.WithContext(ctx).
		Model(&model.BalanceRecord{}).
		Joins("JOIN users u ON balance_records.user_id = u.id").
		Where("(u.id = ? OR u.referred_by = ?)", userID, userID).
		Where("balance_records.type = 'CONSUME'").
		Where("balance_records.created_at >= ? AND balance_records.created_at < ?", lastMonth, thisMonth).
		Select("COALESCE(SUM(ABS(balance_records.amount)), 0)").
		Scan(&lastMonthTotal)
	stats.LastMonthSales = roundTo6(lastMonthTotal)

	// 查询代理档案的累计收益
	var profile model.UserAgentProfile
	if err := s.db.WithContext(ctx).Where("user_id = ?", userID).First(&profile).Error; err == nil {
		stats.TotalEarnings = profile.TotalEarnings
		stats.WithdrawnAmount = profile.WithdrawnAmount
	}

	// 佣金状态分组统计
	stats.SettledAmount, stats.PendingAmount = s.getCommissionAmounts(ctx, userID)

	return stats, nil
}

// ========== 提现管理 ==========

// RequestWithdrawal 申请提现
// 逻辑：检查可提现金额(SETTLED佣金) → 创建提现申请 → 冻结对应佣金
// 参数 amount: 提现金额（人民币元）
func (s *AgentLevelService) RequestWithdrawal(ctx context.Context, userID uint, amount float64, bankInfo string) (*model.WithdrawalRequest, error) {
	if amount <= 0 {
		return nil, fmt.Errorf("提现金额必须为正数")
	}
	amount = roundTo6(amount)

	// 检查代理状态
	var profile model.UserAgentProfile
	if err := s.db.WithContext(ctx).Where("user_id = ? AND status = 'ACTIVE'", userID).First(&profile).Error; err != nil {
		return nil, fmt.Errorf("代理档案不存在或未激活")
	}

	// 获取最低提现金额（积分单位）
	var cfg model.ReferralConfig
	minCredits := int64(100000) // 默认最低10元=100000积分
	if err := s.db.WithContext(ctx).Where("is_active = ?", true).First(&cfg).Error; err == nil {
		minCredits = cfg.MinWithdrawAmount
	}
	creditsAmount := credits.RMBToCredits(amount)
	if creditsAmount < minCredits {
		return nil, fmt.Errorf("最低提现金额为 %.2f 元", credits.CreditsToRMB(minCredits))
	}

	// 计算可提现金额（人民币）
	settledAmount, _ := s.getCommissionAmounts(ctx, userID)

	// 减去已申请提现（PENDING状态）中的金额
	var pendingWithdrawals float64
	s.db.WithContext(ctx).Model(&model.WithdrawalRequest{}).
		Where("user_id = ? AND status = 'PENDING'", userID).
		Select("COALESCE(SUM(amount), 0)").Scan(&pendingWithdrawals)

	available := roundTo6(settledAmount - pendingWithdrawals)
	if available < amount {
		return nil, fmt.Errorf("可提现余额不足: 可用 %.2f 元, 申请 %.2f 元", available, amount)
	}

	// 创建提现申请
	withdrawal := &model.WithdrawalRequest{
		UserID:   userID,
		Amount:   amount,
		Status:   "PENDING",
		BankInfo: bankInfo,
	}
	if err := s.db.WithContext(ctx).Create(withdrawal).Error; err != nil {
		return nil, fmt.Errorf("创建提现申请失败: %w", err)
	}

	return withdrawal, nil
}

// GetWithdrawals 获取提现记录（分页）
func (s *AgentLevelService) GetWithdrawals(ctx context.Context, userID uint, page, pageSize int) ([]model.WithdrawalRequest, int64, error) {
	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > 100 {
		pageSize = 20
	}

	var total int64
	query := s.db.WithContext(ctx).Model(&model.WithdrawalRequest{}).Where("user_id = ?", userID)
	query.Count(&total)

	var records []model.WithdrawalRequest
	err := query.Order("created_at DESC").
		Offset((page - 1) * pageSize).
		Limit(pageSize).
		Find(&records).Error

	return records, total, err
}

// ApproveWithdrawal 管理员审核通过提现
func (s *AgentLevelService) ApproveWithdrawal(ctx context.Context, withdrawalID uint, adminID uint) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// 查询并锁定提现记录
		var wr model.WithdrawalRequest
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ? AND status = 'PENDING'", withdrawalID).First(&wr).Error; err != nil {
			return fmt.Errorf("提现申请不存在或状态不是PENDING: %w", err)
		}

		now := time.Now()
		// 更新提现记录为已批准
		if err := tx.Model(&wr).Updates(map[string]interface{}{
			"status":       "APPROVED",
			"admin_id":     adminID,
			"processed_at": now,
		}).Error; err != nil {
			return fmt.Errorf("更新提现状态失败: %w", err)
		}

		// 更新代理档案的已提现金额
		if err := tx.Model(&model.UserAgentProfile{}).
			Where("user_id = ?", wr.UserID).
			UpdateColumn("withdrawn_amount", gorm.Expr("withdrawn_amount + ?", wr.Amount)).Error; err != nil {
			return fmt.Errorf("更新已提现金额失败: %w", err)
		}

		// 将对应佣金记录标记为已提现
		tx.Model(&model.CommissionRecord{}).
			Where("user_id = ? AND status = 'SETTLED'", wr.UserID).
			Limit(1). // 逐笔标记（简化实现）
			Update("status", "WITHDRAWN")

		return nil
	})
}

// RejectWithdrawal 管理员拒绝提现
func (s *AgentLevelService) RejectWithdrawal(ctx context.Context, withdrawalID uint, adminID uint, reason string) error {
	now := time.Now()
	result := s.db.WithContext(ctx).Model(&model.WithdrawalRequest{}).
		Where("id = ? AND status = 'PENDING'", withdrawalID).
		Updates(map[string]interface{}{
			"status":       "REJECTED",
			"admin_id":     adminID,
			"admin_remark": reason,
			"processed_at": now,
		})
	if result.Error != nil {
		return fmt.Errorf("拒绝提现失败: %w", result.Error)
	}
	if result.RowsAffected == 0 {
		return fmt.Errorf("提现申请不存在或状态不是PENDING")
	}
	return nil
}

// ========== 内部辅助方法 ==========

// getAllLevelsFromDB 从数据库查询所有活跃的代理等级，按 rank 升序
func (s *AgentLevelService) getAllLevelsFromDB(ctx context.Context) ([]model.AgentLevel, error) {
	var levels []model.AgentLevel
	if err := s.db.WithContext(ctx).
		Where("is_active = ?", true).
		Order("level_rank ASC").
		Find(&levels).Error; err != nil {
		return nil, fmt.Errorf("查询代理等级失败: %w", err)
	}
	return levels, nil
}

// getCommissionAmounts 获取用户各状态佣金汇总（已结算, 待结算）
func (s *AgentLevelService) getCommissionAmounts(ctx context.Context, userID uint) (settled, pending float64) {
	type amountResult struct {
		Status string
		Total  float64
	}
	var results []amountResult
	s.db.WithContext(ctx).Model(&model.CommissionRecord{}).
		Select("status, COALESCE(SUM(commission_amount), 0) as total").
		Where("user_id = ?", userID).
		Group("status").
		Scan(&results)

	for _, r := range results {
		switch r.Status {
		case "SETTLED":
			settled = r.Total
		case "PENDING":
			pending = r.Total
		}
	}
	return
}

// getUserMonthSpend 获取用户本月消费金额
func (s *AgentLevelService) getUserMonthSpend(ctx context.Context, userID uint, monthStart time.Time) float64 {
	var total float64
	s.db.WithContext(ctx).Model(&model.BalanceRecord{}).
		Where("user_id = ? AND type = 'CONSUME' AND created_at >= ?", userID, monthStart).
		Select("COALESCE(SUM(ABS(amount)), 0)").
		Scan(&total)
	return roundTo6(total)
}

// getUserTotalSpend 获取用户累计消费金额（返回人民币值）
func (s *AgentLevelService) getUserTotalSpend(ctx context.Context, userID uint) float64 {
	var ub model.UserBalance
	if err := s.db.WithContext(ctx).Where("user_id = ?", userID).First(&ub).Error; err == nil {
		return credits.CreditsToRMB(ub.TotalConsumed)
	}
	return 0
}

// buildTreeChildren 递归构建团队树子节点
// currentDepth: 当前递归深度, maxDepth: 最大递归深度
func (s *AgentLevelService) buildTreeChildren(ctx context.Context, parentID uint, currentDepth, maxDepth int, monthStart time.Time) ([]TeamTreeNode, int) {
	if currentDepth > maxDepth {
		return nil, 0
	}

	// 查询直推下线
	var users []model.User
	s.db.WithContext(ctx).Where("referred_by = ?", parentID).Find(&users)

	totalCount := len(users)
	var children []TeamTreeNode

	for _, u := range users {
		// 获取用户的代理等级
		level := ""
		var ap model.UserAgentProfile
		if err := s.db.WithContext(ctx).Preload("AgentLevel").Where("user_id = ?", u.ID).First(&ap).Error; err == nil {
			level = ap.AgentLevel.LevelCode
		}

		node := TeamTreeNode{
			UserID:     u.ID,
			Name:       u.Name,
			Email:      u.Email,
			Level:      level,
			MonthSpend: s.getUserMonthSpend(ctx, u.ID, monthStart),
			TotalSpend: s.getUserTotalSpend(ctx, u.ID),
			JoinedAt:   u.CreatedAt,
		}

		// 递归查询子节点
		if currentDepth < maxDepth {
			subChildren, subCount := s.buildTreeChildren(ctx, u.ID, currentDepth+1, maxDepth, monthStart)
			node.Children = subChildren
			totalCount += subCount
		}

		children = append(children, node)
	}

	return children, totalCount
}

// countTeamMembers 递归计算团队总人数
func (s *AgentLevelService) countTeamMembers(ctx context.Context, parentID uint, currentDepth, maxDepth int) int {
	if currentDepth > maxDepth {
		return 0
	}

	var userIDs []uint
	s.db.WithContext(ctx).Model(&model.User{}).
		Where("referred_by = ?", parentID).
		Pluck("id", &userIDs)

	count := len(userIDs)
	for _, uid := range userIDs {
		count += s.countTeamMembers(ctx, uid, currentDepth+1, maxDepth)
	}
	return count
}

// UpdateLevel 管理员更新代理等级配置（部分更新）
// 自动处理 RMB → 积分换算：前端传 RMB 字段时同步更新对应积分字段
func (s *AgentLevelService) UpdateLevel(ctx context.Context, levelID uint, updates map[string]interface{}) (*model.AgentLevel, error) {
	var level model.AgentLevel
	if err := s.db.WithContext(ctx).First(&level, levelID).Error; err != nil {
		return nil, fmt.Errorf("代理等级不存在: %w", err)
	}

	// RMB → 积分自动换算（1 RMB = 10,000 credits）
	if rmbVal, ok := updates["min_monthly_sales_rmb"]; ok {
		if rmb, ok := rmbVal.(float64); ok {
			updates["min_monthly_sales"] = int64(rmb * 10000)
		}
	}

	if err := s.db.WithContext(ctx).Model(&level).Updates(updates).Error; err != nil {
		return nil, fmt.Errorf("更新代理等级失败: %w", err)
	}

	// 清除等级缓存
	s.clearCache()

	return &level, nil
}

// CreateLevel 创建代理等级
// 接收 RMB 值自动换算积分（1 RMB = 10,000 credits）
func (s *AgentLevelService) CreateLevel(level *model.AgentLevel) error {
	// 自动换算: RMB -> 积分（前端传入 RMB 字段，后端同步写入积分字段）
	if level.MinMonthlySalesRMB > 0 && level.MinMonthlySales == 0 {
		level.MinMonthlySales = int64(level.MinMonthlySalesRMB * 10000)
	}
	result := s.db.Create(level)
	if result.Error != nil {
		return result.Error
	}
	// 清除等级缓存，确保列表查询能获取最新数据
	s.clearCache()
	return nil
}

// DeleteLevel 删除代理等级
func (s *AgentLevelService) DeleteLevel(id uint) error {
	result := s.db.Delete(&model.AgentLevel{}, id)
	if result.Error != nil {
		return result.Error
	}
	// 清除等级缓存
	s.clearCache()
	return nil
}

// clearCache 清除代理等级配置的 Redis 缓存
func (s *AgentLevelService) clearCache() {
	if s.redis != nil {
		ctx := context.Background()
		_ = s.redis.Del(ctx, agentLevelCacheKey).Err()
	}
}

// GetApplications 管理员查询代理申请列表（支持状态筛选和分页）
func (s *AgentLevelService) GetApplications(ctx context.Context, status string, page, pageSize int) ([]model.UserAgentProfile, int64, error) {
	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > 100 {
		pageSize = 20
	}

	query := s.db.WithContext(ctx).Model(&model.UserAgentProfile{}).Preload("AgentLevel")
	if status != "" {
		query = query.Where("status = ?", status)
	}

	var total int64
	query.Count(&total)

	var profiles []model.UserAgentProfile
	err := query.Order("created_at DESC").
		Offset((page - 1) * pageSize).
		Limit(pageSize).
		Find(&profiles).Error

	return profiles, total, err
}

// GetAllWithdrawals 管理员查询所有提现申请（支持状态筛选和分页）
func (s *AgentLevelService) GetAllWithdrawals(ctx context.Context, status string, page, pageSize int) ([]model.WithdrawalRequest, int64, error) {
	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > 100 {
		pageSize = 20
	}

	query := s.db.WithContext(ctx).Model(&model.WithdrawalRequest{})
	if status != "" {
		query = query.Where("status = ?", status)
	}

	var total int64
	query.Count(&total)

	var records []model.WithdrawalRequest
	err := query.Order("created_at DESC").
		Offset((page - 1) * pageSize).
		Limit(pageSize).
		Find(&records).Error

	return records, total, err
}

// invalidateProfileCache 清除代理档案缓存
func (s *AgentLevelService) invalidateProfileCache(ctx context.Context, userID uint) {
	if s.redis != nil {
		key := fmt.Sprintf("%s%d", agentProfileCachePrefix, userID)
		_ = s.redis.Del(ctx, key).Err()
	}
}
