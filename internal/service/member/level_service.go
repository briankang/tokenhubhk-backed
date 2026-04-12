package member

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"time"

	goredis "github.com/redis/go-redis/v9"
	"gorm.io/gorm"

	"tokenhub-server/internal/model"
	"tokenhub-server/internal/pkg/credits"
)

const (
	// memberLevelCacheKey 会员等级配置缓存键
	memberLevelCacheKey = "member:levels:all"
	// memberLevelCacheTTL 等级配置缓存时长（2小时）
	memberLevelCacheTTL = 2 * time.Hour
	// memberProfileCachePrefix 会员档案缓存前缀
	memberProfileCachePrefix = "member:profile:"
	// memberProfileCacheTTL 用户档案缓存时长（5分钟）
	memberProfileCacheTTL = 5 * time.Minute
)

// MemberProfileResponse 会员档案响应结构
type MemberProfileResponse struct {
	UserID       uint               `json:"user_id"`
	Level        model.MemberLevel  `json:"level"`
	TotalConsume float64            `json:"total_consume"`
	NextLevel    *model.MemberLevel `json:"next_level,omitempty"` // 下一级信息（V4则为nil）
	GapToNext    float64            `json:"gap_to_next"`          // 距下一级差额
	Benefits     map[string]interface{} `json:"benefits"`         // 当前权益
}

// UpgradeProgressResponse 升级进度响应结构
type UpgradeProgressResponse struct {
	CurrentLevel  string  `json:"current_level"`
	NextLevel     string  `json:"next_level"`
	CurrentValue  float64 `json:"current_value"`  // 当前累计消费
	RequiredValue float64 `json:"required_value"` // 下一级门槛
	Progress      float64 `json:"progress"`       // 进度百分比 0-100
}

// MemberLevelService 会员等级服务，管理用户会员等级的初始化、升降级、赠送额度等
type MemberLevelService struct {
	db    *gorm.DB
	redis *goredis.Client
}

// NewMemberLevelService 创建会员等级服务实例
func NewMemberLevelService(db *gorm.DB, redis *goredis.Client) *MemberLevelService {
	return &MemberLevelService{db: db, redis: redis}
}

// roundTo6 将浮点数四舍五入到 6 位小数
func roundTo6(v float64) float64 {
	return math.Round(v*1e6) / 1e6
}

// InitMemberProfile 用户注册时初始化会员档案（默认V0等级）
// 如果已存在则跳过，保证幂等
func (s *MemberLevelService) InitMemberProfile(ctx context.Context, userID uint) error {
	// 检查是否已有档案
	var count int64
	s.db.WithContext(ctx).Model(&model.UserMemberProfile{}).Where("user_id = ?", userID).Count(&count)
	if count > 0 {
		return nil // 已存在，幂等跳过
	}

	// 查找默认等级 V0
	var defaultLevel model.MemberLevel
	if err := s.db.WithContext(ctx).Where("level_code = ? AND is_active = ?", "V0", true).First(&defaultLevel).Error; err != nil {
		// V0 不存在时查找 rank 最低的等级
		if err := s.db.WithContext(ctx).Where("is_active = ?", true).Order("level_rank ASC").First(&defaultLevel).Error; err != nil {
			return fmt.Errorf("未找到默认会员等级: %w", err)
		}
	}

	profile := &model.UserMemberProfile{
		UserID:        userID,
		MemberLevelID: defaultLevel.ID,
		TotalConsume:  0,
	}
	if err := s.db.WithContext(ctx).Create(profile).Error; err != nil {
		return fmt.Errorf("创建会员档案失败: %w", err)
	}
	return nil
}

// CheckAndUpgrade 消费后检查会员是否可升级（实时触发）
// 逻辑：查询用户累计消费 → 与各等级门槛对比 → 如果可升级则更新等级
func (s *MemberLevelService) CheckAndUpgrade(ctx context.Context, userID uint) error {
	// 查询用户会员档案
	var profile model.UserMemberProfile
	if err := s.db.WithContext(ctx).Preload("MemberLevel").Where("user_id = ?", userID).First(&profile).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil // 无档案，跳过
		}
		return fmt.Errorf("查询会员档案失败: %w", err)
	}

	// 查询用户累计消费（从 UserBalance 表获取，单位为积分）
	var ub model.UserBalance
	if err := s.db.WithContext(ctx).Where("user_id = ?", userID).First(&ub).Error; err != nil {
		return nil // 无余额记录，跳过
	}
	totalConsumeCredits := ub.TotalConsumed
	
	// 更新档案中的累计消费字段（转换为人民币用于展示）
	profile.TotalConsume = credits.CreditsToRMB(totalConsumeCredits)
	
	// 查询所有可用等级，按 rank 降序排列（从最高等级开始匹配）
	levels, err := s.getAllLevelsFromDB(ctx)
	if err != nil {
		return err
	}
	
	// 从最高等级开始匹配，找到第一个满足门槛的等级
	// MinTotalConsume 为积分单位
	var targetLevel *model.MemberLevel
	for i := len(levels) - 1; i >= 0; i-- {
		if totalConsumeCredits >= levels[i].MinTotalConsume {
			targetLevel = &levels[i]
			break
		}
	}

	if targetLevel == nil {
		return nil
	}

	// 如果目标等级高于当前等级，执行升级
	if targetLevel.Rank > profile.MemberLevel.Rank {
		profile.MemberLevelID = targetLevel.ID
		profile.DegradeWarnings = 0 // 升级时重置降级计数器
		if err := s.db.WithContext(ctx).Save(&profile).Error; err != nil {
			return fmt.Errorf("升级会员等级失败: %w", err)
		}
		// 清除缓存
		s.invalidateProfileCache(ctx, userID)
	} else {
		// 仅更新累计消费字段（转换为人民币）
		s.db.WithContext(ctx).Model(&profile).UpdateColumn("total_consume", credits.CreditsToRMB(totalConsumeCredits))
	}

	return nil
}

// CheckAndDegradeAll 定时任务：批量检查所有会员降级
// 逻辑：查询连续N个月消费不达标的用户 → 降一级
// 降级条件：连续 DegradeMonths 个月，每月消费低于当前等级 MinTotalConsume 的 1/12
func (s *MemberLevelService) CheckAndDegradeAll(ctx context.Context) error {
	// 查询所有 V1 及以上等级的用户档案
	var profiles []model.UserMemberProfile
	if err := s.db.WithContext(ctx).
		Preload("MemberLevel").
		Joins("JOIN member_levels ON member_levels.id = user_member_profiles.member_level_id").
		Where("member_levels.rank > 0").
		Find(&profiles).Error; err != nil {
		return fmt.Errorf("查询会员档案失败: %w", err)
	}

	// 获取所有等级配置
	levels, err := s.getAllLevelsFromDB(ctx)
	if err != nil {
		return err
	}

	for _, profile := range profiles {
		// 计算当前月消费门槛（年消费门槛 / 12，转换为人民币）
		monthlyThresholdRMB := credits.CreditsToRMB(profile.MemberLevel.MinTotalConsume) / 12.0

		// 检查最近一个月消费是否达标
		lastMonthConsume := profile.MonthConsume1
		if lastMonthConsume < monthlyThresholdRMB {
			// 未达标，增加警告计数
			profile.DegradeWarnings++
		} else {
			// 达标，重置计数
			profile.DegradeWarnings = 0
		}

		// 连续不达标月数达到阈值，执行降级
		if profile.DegradeWarnings >= profile.MemberLevel.DegradeMonths {
			// 找到低一级的等级
			var lowerLevel *model.MemberLevel
			for i := range levels {
				if levels[i].Rank == profile.MemberLevel.Rank-1 {
					lowerLevel = &levels[i]
					break
				}
			}
			if lowerLevel != nil {
				profile.MemberLevelID = lowerLevel.ID
				profile.DegradeWarnings = 0 // 重置计数器
			}
		}

		now := time.Now()
		profile.LastDegradeCheck = &now
		s.db.WithContext(ctx).Save(&profile)
		s.invalidateProfileCache(ctx, profile.UserID)
	}

	return nil
}

// GrantMonthlyGifts 定时任务：每月1号为所有V1+会员发放赠送额度
// 逻辑：查询所有V1-V4会员 → 按等级发放 MonthlyGift → 写余额记录
func (s *MemberLevelService) GrantMonthlyGifts(ctx context.Context) error {
	// 查询所有 V1 及以上且 MonthlyGift > 0 的会员档案
	var profiles []model.UserMemberProfile
	if err := s.db.WithContext(ctx).
		Preload("MemberLevel").
		Joins("JOIN member_levels ON member_levels.id = user_member_profiles.member_level_id").
		Where("member_levels.rank > 0 AND member_levels.monthly_gift > 0").
		Find(&profiles).Error; err != nil {
		return fmt.Errorf("查询会员档案失败: %w", err)
	}

	now := time.Now()
	currentMonth := now.Format("2006-01")

	for _, profile := range profiles {
		// 检查本月是否已发放（防止重复发放）
		if profile.LastGiftAt != nil && profile.LastGiftAt.Format("2006-01") == currentMonth {
			continue
		}

		// MonthlyGift 为积分单位，转换为人民币用于记录
		giftAmountRMB := credits.CreditsToRMB(profile.MemberLevel.MonthlyGift)
		if giftAmountRMB <= 0 {
			continue
		}
		
		// 在事务中发放赠送额度
		err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			// 查询用户余额
			var ub model.UserBalance
			if err := tx.Where("user_id = ?", profile.UserID).First(&ub).Error; err != nil {
				if err == gorm.ErrRecordNotFound {
					return nil // 无余额记录，跳过
				}
				return err
			}
		
			// 增加免费额度（积分为单位）
			before := ub.FreeQuota
			ub.FreeQuota += profile.MemberLevel.MonthlyGift
			ub.FreeQuotaRMB = credits.CreditsToRMB(ub.FreeQuota)
			if err := tx.Save(&ub).Error; err != nil {
				return fmt.Errorf("更新赠送额度失败: %w", err)
			}
		
			// 写入余额变动记录
			record := &model.BalanceRecord{
				UserID:        profile.UserID,
				TenantID:      ub.TenantID,
				Type:          "GIFT",
				Amount:        profile.MemberLevel.MonthlyGift,
				AmountRMB:     giftAmountRMB,
				BeforeBalance: before,
				AfterBalance:  ub.FreeQuota,
				Remark:        fmt.Sprintf("会员%s月度赠送额度", profile.MemberLevel.LevelName),
			}
			if err := tx.Create(record).Error; err != nil {
				return fmt.Errorf("创建赠送记录失败: %w", err)
			}

			// 更新最后发放时间
			return tx.Model(&model.UserMemberProfile{}).
				Where("id = ?", profile.ID).
				Update("last_gift_at", now).Error
		})

		if err != nil {
			continue // 单个用户失败不影响其他用户
		}
	}

	return nil
}

// GetProfile 获取用户会员档案（含等级信息和升级进度）
func (s *MemberLevelService) GetProfile(ctx context.Context, userID uint) (*MemberProfileResponse, error) {
	var profile model.UserMemberProfile
	if err := s.db.WithContext(ctx).
		Preload("MemberLevel").
		Where("user_id = ?", userID).
		First(&profile).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, fmt.Errorf("会员档案不存在")
		}
		return nil, fmt.Errorf("查询会员档案失败: %w", err)
	}

	// 获取所有等级
	levels, err := s.getAllLevelsFromDB(ctx)
	if err != nil {
		return nil, err
	}

	resp := &MemberProfileResponse{
		UserID:       userID,
		Level:        profile.MemberLevel,
		TotalConsume: profile.TotalConsume,
		Benefits:     s.buildBenefits(&profile.MemberLevel),
	}

	// 查找下一级
	for i := range levels {
		if levels[i].Rank == profile.MemberLevel.Rank+1 {
			resp.NextLevel = &levels[i]
			// GapToNext 为人民币值
			resp.GapToNext = roundTo6(credits.CreditsToRMB(levels[i].MinTotalConsume) - profile.TotalConsume)
			if resp.GapToNext < 0 {
				resp.GapToNext = 0
			}
			break
		}
	}

	return resp, nil
}

// GetAllLevels 获取所有会员等级配置（带 Redis 缓存）
func (s *MemberLevelService) GetAllLevels(ctx context.Context) ([]model.MemberLevel, error) {
	// 尝试从 Redis 缓存读取
	if s.redis != nil {
		val, err := s.redis.Get(ctx, memberLevelCacheKey).Bytes()
		if err == nil {
			var levels []model.MemberLevel
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
			_ = s.redis.Set(ctx, memberLevelCacheKey, data, memberLevelCacheTTL).Err()
		}
	}

	return levels, nil
}

// GetUpgradeProgress 获取升级进度（距下一级差额）
func (s *MemberLevelService) GetUpgradeProgress(ctx context.Context, userID uint) (*UpgradeProgressResponse, error) {
	var profile model.UserMemberProfile
	if err := s.db.WithContext(ctx).
		Preload("MemberLevel").
		Where("user_id = ?", userID).
		First(&profile).Error; err != nil {
		return nil, fmt.Errorf("会员档案不存在: %w", err)
	}

	levels, err := s.getAllLevelsFromDB(ctx)
	if err != nil {
		return nil, err
	}

	resp := &UpgradeProgressResponse{
		CurrentLevel: profile.MemberLevel.LevelCode,
		CurrentValue: profile.TotalConsume,
	}

	// 查找下一级
	for i := range levels {
		if levels[i].Rank == profile.MemberLevel.Rank+1 {
			resp.NextLevel = levels[i].LevelCode
			resp.RequiredValue = credits.CreditsToRMB(levels[i].MinTotalConsume)
			// 计算进度百分比
			if resp.RequiredValue > 0 {
				resp.Progress = roundTo6(resp.CurrentValue / resp.RequiredValue * 100)
				if resp.Progress > 100 {
					resp.Progress = 100
				}
			}
			break
		}
	}

	// 已达最高等级
	if resp.NextLevel == "" {
		resp.NextLevel = "MAX"
		resp.Progress = 100
	}

	return resp, nil
}

// GetEffectiveDiscount 获取用户最优折扣（会员折扣 vs 代理折扣取最低）
// 返回值：折扣率（如 0.80 = 8折, 1.00 = 无折扣）
func (s *MemberLevelService) GetEffectiveDiscount(ctx context.Context, userID uint) (float64, error) {
	discount := 1.0 // 默认无折扣

	// 查询会员等级折扣
	var memberProfile model.UserMemberProfile
	if err := s.db.WithContext(ctx).
		Preload("MemberLevel").
		Where("user_id = ?", userID).
		First(&memberProfile).Error; err == nil {
		if memberProfile.MemberLevel.ModelDiscount > 0 && memberProfile.MemberLevel.ModelDiscount < discount {
			discount = memberProfile.MemberLevel.ModelDiscount
		}
	}

	// 查询代理等级折扣（如果代理也有折扣的话）
	// 代理等级目前没有 ModelDiscount 字段，但预留取最低逻辑
	// 如果将来代理也有折扣，在此处对比取最低值

	return discount, nil
}

// RotateMonthConsume 月末轮转月消费数据（定时任务调用）
// 将 MonthConsume1→MonthConsume2, MonthConsume2→MonthConsume3
// 然后将 MonthConsume1 设为当月实际消费
func (s *MemberLevelService) RotateMonthConsume(ctx context.Context) error {
	return s.db.WithContext(ctx).
		Model(&model.UserMemberProfile{}).
		Where("1 = 1").
		Updates(map[string]interface{}{
			"month_consume_3": gorm.Expr("month_consume_2"),
			"month_consume_2": gorm.Expr("month_consume_1"),
			"month_consume_1": 0,
		}).Error
}

// getAllLevelsFromDB 从数据库查询所有活跃的会员等级，按 rank 升序
func (s *MemberLevelService) getAllLevelsFromDB(ctx context.Context) ([]model.MemberLevel, error) {
	var levels []model.MemberLevel
	if err := s.db.WithContext(ctx).
		Where("is_active = ?", true).
		Order("level_rank ASC").
		Find(&levels).Error; err != nil {
		return nil, fmt.Errorf("查询会员等级失败: %w", err)
	}
	return levels, nil
}

// buildBenefits 构建会员权益描述 map
// 金额相关字段转换为人民币用于展示
func (s *MemberLevelService) buildBenefits(level *model.MemberLevel) map[string]interface{} {
	benefits := map[string]interface{}{
		"model_discount":     level.ModelDiscount,
		"monthly_gift":       credits.CreditsToRMB(level.MonthlyGift),
		"max_tokens_per_req": level.MaxTokensPerReq,
		"daily_limit":        credits.CreditsToRMB(level.DailyLimit),
	}
	return benefits
}

// UpdateLevel 管理员更新会员等级配置（部分更新）
// 自动处理 RMB → 积分换算：前端传 RMB 字段时同步更新对应积分字段
func (s *MemberLevelService) UpdateLevel(ctx context.Context, levelID uint, updates map[string]interface{}) (*model.MemberLevel, error) {
	var level model.MemberLevel
	if err := s.db.WithContext(ctx).First(&level, levelID).Error; err != nil {
		return nil, fmt.Errorf("会员等级不存在: %w", err)
	}

	// RMB → 积分自动换算（1 RMB = 10,000 credits）
	if rmbVal, ok := updates["min_total_consume_rmb"]; ok {
		if rmb, ok := rmbVal.(float64); ok {
			updates["min_total_consume"] = int64(rmb * 10000)
		}
	}
	if rmbVal, ok := updates["monthly_gift_rmb"]; ok {
		if rmb, ok := rmbVal.(float64); ok {
			updates["monthly_gift"] = int64(rmb * 10000)
		}
	}
	if rmbVal, ok := updates["daily_limit_rmb"]; ok {
		if rmb, ok := rmbVal.(float64); ok {
			updates["daily_limit"] = int64(rmb * 10000)
		}
	}

	if err := s.db.WithContext(ctx).Model(&level).Updates(updates).Error; err != nil {
		return nil, fmt.Errorf("更新会员等级失败: %w", err)
	}

	// 清除等级缓存
	s.clearCache()

	return &level, nil
}

// CreateLevel 创建会员等级
// 接收 RMB 值自动换算积分（1 RMB = 10,000 credits）
func (s *MemberLevelService) CreateLevel(level *model.MemberLevel) error {
	// 自动换算: RMB -> 积分（前端传入 RMB 字段，后端同步写入积分字段）
	if level.MinTotalConsumeRMB > 0 && level.MinTotalConsume == 0 {
		level.MinTotalConsume = int64(level.MinTotalConsumeRMB * 10000)
	}
	if level.MonthlyGiftRMB > 0 && level.MonthlyGift == 0 {
		level.MonthlyGift = int64(level.MonthlyGiftRMB * 10000)
	}
	if level.DailyLimitRMB > 0 && level.DailyLimit == 0 {
		level.DailyLimit = int64(level.DailyLimitRMB * 10000)
	}
	result := s.db.Create(level)
	if result.Error != nil {
		return result.Error
	}
	// 清除等级缓存，确保列表查询能获取最新数据
	s.clearCache()
	return nil
}

// DeleteLevel 删除会员等级
func (s *MemberLevelService) DeleteLevel(id uint) error {
	result := s.db.Delete(&model.MemberLevel{}, id)
	if result.Error != nil {
		return result.Error
	}
	// 清除等级缓存
	s.clearCache()
	return nil
}

// clearCache 清除会员等级配置的 Redis 缓存
func (s *MemberLevelService) clearCache() {
	if s.redis != nil {
		ctx := context.Background()
		_ = s.redis.Del(ctx, memberLevelCacheKey).Err()
	}
}

// invalidateProfileCache 清除用户会员档案缓存
func (s *MemberLevelService) invalidateProfileCache(ctx context.Context, userID uint) {
	if s.redis != nil {
		key := fmt.Sprintf("%s%d", memberProfileCachePrefix, userID)
		_ = s.redis.Del(ctx, key).Err()
	}
}
