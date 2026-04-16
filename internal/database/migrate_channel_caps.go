package database

import (
	"fmt"

	"gorm.io/gorm"
)

// MigrateChannelCapabilities 为历史渠道回填 supported_capabilities 字段
//
// 规则（幂等，仅处理空字段）：
//   - 所有渠道默认含 "chat"
//   - 阿里云 DashScope 渠道 额外添加 "image,video,tts,asr,embedding"
//   - 火山引擎渠道 额外添加 "image,video,tts,asr"
//   - OpenAI 系列 额外添加 "embedding,image,tts,asr"
//
// 已有 supported_capabilities 非空的渠道跳过，允许管理员手动调整后不被覆盖
func MigrateChannelCapabilities(db *gorm.DB) error {
	type row struct {
		ID                    uint
		Endpoint              string
		SupplierName          string
		SupportedCapabilities string
	}

	var rows []row
	if err := db.Raw(`
		SELECT c.id, c.endpoint, COALESCE(s.name, '') AS supplier_name, c.supported_capabilities
		FROM channels c
		LEFT JOIN suppliers s ON c.supplier_id = s.id
		WHERE c.deleted_at IS NULL
	`).Scan(&rows).Error; err != nil {
		return fmt.Errorf("查询渠道失败: %w", err)
	}

	updated := 0
	skipped := 0

	for _, r := range rows {
		// 已显式配置（非空且非默认值）→ 跳过，不覆盖管理员手动配置
		if r.SupportedCapabilities != "" && r.SupportedCapabilities != "chat" {
			skipped++
			continue
		}

		caps := "chat"
		endpoint := r.Endpoint
		supplier := r.SupplierName

		// 阿里云 DashScope：文本/图像/视频/TTS/ASR/Embedding 全能
		if contains(endpoint, "dashscope") || contains(supplier, "阿里") || contains(supplier, "aliyun") {
			caps = "chat,image,video,tts,asr,embedding"
		}
		// 火山引擎：文本/图像/视频/TTS/ASR
		if contains(endpoint, "volces.com") || contains(supplier, "火山") || contains(supplier, "volc") {
			caps = "chat,image,video,tts,asr"
		}
		// OpenAI：文本/图像/TTS/ASR/Embedding
		if contains(endpoint, "api.openai.com") || contains(supplier, "OpenAI") {
			caps = "chat,image,tts,asr,embedding"
		}

		if err := db.Exec(`UPDATE channels SET supported_capabilities = ? WHERE id = ?`, caps, r.ID).Error; err != nil {
			fmt.Printf("[migrate] 回填渠道 %d 能力失败: %v\n", r.ID, err)
			continue
		}
		updated++
	}

	fmt.Printf("[migrate] 渠道能力回填完成: updated=%d, skipped=%d\n", updated, skipped)
	return nil
}

// contains 判断 s 是否包含 sub（不区分大小写，简易实现）
func contains(s, sub string) bool {
	if len(sub) == 0 {
		return true
	}
	if len(s) < len(sub) {
		return false
	}
	// 简易小写比较
	lower := toLower(s)
	subLower := toLower(sub)
	for i := 0; i+len(subLower) <= len(lower); i++ {
		if lower[i:i+len(subLower)] == subLower {
			return true
		}
	}
	return false
}

func toLower(s string) string {
	b := make([]byte, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'A' && c <= 'Z' {
			c += 'a' - 'A'
		}
		b[i] = c
	}
	return string(b)
}
