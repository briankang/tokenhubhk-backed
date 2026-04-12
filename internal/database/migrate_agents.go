package database

import (
	"fmt"
	"time"

	"gorm.io/gorm"

	"tokenhub-server/internal/model"
	"tokenhub-server/internal/pkg/credits"
)

// MigrateAgentData 将现有 AGENT_L1/L2/L3 用户迁移到新的 UserAgentProfile 体系
// 迁移规则：
//   AGENT_L1 → A1 青铜代理（status=ACTIVE）
//   AGENT_L2 → A2 白银代理（status=ACTIVE）
//   AGENT_L3 → A3 黄金代理（status=ACTIVE）
// 幂等设计：如果 UserAgentProfile 已存在则跳过
func MigrateAgentData(db *gorm.DB) error {
	// 角色到代理等级编码的映射
	roleToLevel := map[string]string{
		"AGENT_L1": "A1",
		"AGENT_L2": "A2",
		"AGENT_L3": "A3",
	}

	// 预加载所有代理等级配置，建立 code→ID 映射
	var levels []model.AgentLevel
	if err := db.Where("is_active = ?", true).Find(&levels).Error; err != nil {
		return fmt.Errorf("查询代理等级失败: %w", err)
	}
	levelMap := make(map[string]uint)
	for _, lvl := range levels {
		levelMap[lvl.LevelCode] = lvl.ID
	}

	migrated := 0
	skipped := 0

	// 遍历所有 AGENT_L1/L2/L3 角色用户
	for role, levelCode := range roleToLevel {
		// 查询该角色的所有用户
		var users []model.User
		if err := db.Where("role = ?", role).Find(&users).Error; err != nil {
			fmt.Printf("[migrate] 查询 %s 用户失败: %v\n", role, err)
			continue
		}

		levelID, ok := levelMap[levelCode]
		if !ok {
			fmt.Printf("[migrate] 代理等级 %s 不存在，跳过 %s 角色迁移\n", levelCode, role)
			continue
		}

		for _, user := range users {
			// 幂等检查：如果已存在 UserAgentProfile 则跳过
			var count int64
			db.Model(&model.UserAgentProfile{}).Where("user_id = ?", user.ID).Count(&count)
			if count > 0 {
				skipped++
				continue
			}

			now := time.Now()
			profile := &model.UserAgentProfile{
				UserID:       user.ID,
				AgentLevelID: levelID,
				Status:       "ACTIVE",
				AppliedAt:    user.CreatedAt,
				ApprovedAt:   &now,
			}
			if err := db.Create(profile).Error; err != nil {
				fmt.Printf("[migrate] 创建代理档案失败 user_id=%d: %v\n", user.ID, err)
				continue
			}
			migrated++
		}
	}

	fmt.Printf("[migrate] 代理数据迁移完成: migrated=%d, skipped=%d\n", migrated, skipped)
	return nil
}

// MigrateMemberData 为所有现有用户创建 UserMemberProfile
// 根据 UserBalance.total_consumed 计算初始等级
// 幂等设计：如果 UserMemberProfile 已存在则跳过
func MigrateMemberData(db *gorm.DB) error {
	// 预加载所有会员等级配置，按 rank 升序
	var levels []model.MemberLevel
	if err := db.Where("is_active = ?", true).Order("level_rank ASC").Find(&levels).Error; err != nil {
		return fmt.Errorf("查询会员等级失败: %w", err)
	}
	if len(levels) == 0 {
		fmt.Println("[migrate] 会员等级未配置，跳过会员数据迁移")
		return nil
	}

	// 默认等级为最低等级（V0）
	defaultLevelID := levels[0].ID

	// 查询所有用户
	var users []model.User
	if err := db.Find(&users).Error; err != nil {
		return fmt.Errorf("查询用户列表失败: %w", err)
	}

	migrated := 0
	skipped := 0

	for _, user := range users {
		// 幂等检查：如果已存在 UserMemberProfile 则跳过
		var count int64
		db.Model(&model.UserMemberProfile{}).Where("user_id = ?", user.ID).Count(&count)
		if count > 0 {
			skipped++
			continue
		}

		// 查询用户累计消费金额（积分）
		var ub model.UserBalance
		totalConsumed := int64(0)
		if err := db.Where("user_id = ?", user.ID).First(&ub).Error; err == nil {
			totalConsumed = ub.TotalConsumed
		}
		
		// 根据累计消费匹配最高可达等级
		// 从最高等级开始匹配，找到第一个满足门槛的等级
		matchedLevelID := defaultLevelID
		for i := len(levels) - 1; i >= 0; i-- {
			// MinTotalConsume 为积分单位，直接比较
			if totalConsumed >= levels[i].MinTotalConsume {
				matchedLevelID = levels[i].ID
				break
			}
		}
		
		// TotalConsume 字段为冗余字段，存储人民币值用于展示
		profile := &model.UserMemberProfile{
			UserID:        user.ID,
			MemberLevelID: matchedLevelID,
			TotalConsume:  credits.CreditsToRMB(totalConsumed),
		}
		if err := db.Create(profile).Error; err != nil {
			fmt.Printf("[migrate] 创建会员档案失败 user_id=%d: %v\n", user.ID, err)
			continue
		}
		migrated++
	}

	fmt.Printf("[migrate] 会员数据迁移完成: migrated=%d, skipped=%d\n", migrated, skipped)
	return nil
}
