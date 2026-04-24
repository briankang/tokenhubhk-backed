package database

import (
	"fmt"

	"gorm.io/gorm"
)

// RunBumpMemberLevelTPM 一次性迁移：提升会员等级默认 TPM
//
// 背景：原默认 TPM 对多模型编排/长上下文请求过低，频繁触发 429。
// 本迁移仅在当前值等于旧默认值（未被管理员自定义）时才 bump，保护管理员配置。
//
// 旧 → 新 TPM 映射：
//   V0: 50,000    → 200,000    (+4x)
//   V1: 100,000   → 500,000    (+5x)
//   V2: 200,000   → 1,500,000  (+7.5x)
//   V3: 500,000   → 5,000,000  (+10x)
//   V4: 1,000,000 → 10,000,000 (+10x)
//
// 幂等：只影响仍等于旧默认值的行；已改过的保持不动；再次运行 0 affected。
func RunBumpMemberLevelTPM(db *gorm.DB) {
	type bump struct {
		LevelCode string
		OldTPM    int
		NewTPM    int
	}
	bumps := []bump{
		{"V0", 50000, 200000},
		{"V1", 100000, 500000},
		{"V2", 200000, 1500000},
		{"V3", 500000, 5000000},
		{"V4", 1000000, 10000000},
	}

	totalAffected := int64(0)
	for _, b := range bumps {
		res := db.Exec(
			"UPDATE member_levels SET default_tpm = ? WHERE level_code = ? AND default_tpm = ?",
			b.NewTPM, b.LevelCode, b.OldTPM,
		)
		if res.Error != nil {
			fmt.Printf("[migrate] bump_member_tpm %s failed: %v\n", b.LevelCode, res.Error)
			continue
		}
		if res.RowsAffected > 0 {
			fmt.Printf("[migrate] bump_member_tpm %s: %d → %d (affected=%d)\n",
				b.LevelCode, b.OldTPM, b.NewTPM, res.RowsAffected)
			totalAffected += res.RowsAffected
		}
	}
	fmt.Printf("[migrate] bump_member_tpm complete: %d rows updated\n", totalAffected)
}
