package database

import (
	"fmt"

	"gorm.io/gorm"
)

// DropAgentTables v3.1 迁移:物理删除代理机制遗留表
// 删除列表:agent_levels / user_agent_profiles / model_commission_configs / agent_applications
//
// 本迁移幂等:表不存在时 DROP TABLE IF EXISTS 静默跳过
// withdrawal_requests 表保留,并从 agent_profile_id 迁移到 user_id(若仍存在旧列)
func DropAgentTables(db *gorm.DB) error {
	// 1. 迁移 withdrawal_requests.agent_profile_id → user_id(若旧列存在)
	migrateWithdrawalUserID(db)

	// 2. DROP 代理相关表
	tables := []string{
		"model_commission_configs",
		"agent_applications",
		"user_agent_profiles",
		"agent_levels",
	}
	for _, t := range tables {
		if err := db.Exec(fmt.Sprintf("DROP TABLE IF EXISTS %s", t)).Error; err != nil {
			fmt.Printf("[migrate] drop table %s failed: %v\n", t, err)
			return err
		}
		fmt.Printf("[migrate] drop table %s: OK\n", t)
	}
	return nil
}

// migrateWithdrawalUserID 将 withdrawal_requests.agent_profile_id → user_id
// 通过 JOIN user_agent_profiles 回填 user_id,随后 DROP 旧列
func migrateWithdrawalUserID(db *gorm.DB) {
	// 检查旧列是否存在
	var exists int64
	db.Raw(`SELECT COUNT(*) FROM information_schema.COLUMNS
		WHERE TABLE_SCHEMA = DATABASE()
		AND TABLE_NAME = 'withdrawal_requests'
		AND COLUMN_NAME = 'agent_profile_id'`).Scan(&exists)
	if exists == 0 {
		// 旧列不存在,已迁移过或首次部署
		return
	}

	// 1. 回填 user_id(若 user_id 列不存在则先新增)
	var hasUserCol int64
	db.Raw(`SELECT COUNT(*) FROM information_schema.COLUMNS
		WHERE TABLE_SCHEMA = DATABASE()
		AND TABLE_NAME = 'withdrawal_requests'
		AND COLUMN_NAME = 'user_id'`).Scan(&hasUserCol)
	if hasUserCol == 0 {
		if err := db.Exec(`ALTER TABLE withdrawal_requests
			ADD COLUMN user_id BIGINT UNSIGNED NOT NULL DEFAULT 0 AFTER id`).Error; err != nil {
			fmt.Printf("[migrate] add user_id to withdrawal_requests failed: %v\n", err)
			return
		}
	}

	// 2. JOIN 回填(若 user_agent_profiles 表仍存在)
	var hasOldTable int64
	db.Raw(`SELECT COUNT(*) FROM information_schema.TABLES
		WHERE TABLE_SCHEMA = DATABASE()
		AND TABLE_NAME = 'user_agent_profiles'`).Scan(&hasOldTable)
	if hasOldTable > 0 {
		if err := db.Exec(`UPDATE withdrawal_requests w
			JOIN user_agent_profiles p ON w.agent_profile_id = p.id
			SET w.user_id = p.user_id
			WHERE w.user_id = 0`).Error; err != nil {
			fmt.Printf("[migrate] backfill withdrawal_requests.user_id failed: %v\n", err)
		} else {
			fmt.Println("[migrate] withdrawal_requests.user_id backfilled from agent_profile_id")
		}
	}

	// 3. DROP 旧列
	if err := db.Exec(`ALTER TABLE withdrawal_requests DROP COLUMN agent_profile_id`).Error; err != nil {
		fmt.Printf("[migrate] drop agent_profile_id column failed: %v\n", err)
		return
	}
	fmt.Println("[migrate] withdrawal_requests.agent_profile_id dropped")
}
