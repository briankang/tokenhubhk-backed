package database

import (
	"strings"
	"time"

	"go.uber.org/zap"
	"gorm.io/gorm"

	"tokenhub-server/internal/model"
	"tokenhub-server/internal/pkg/logger"
)

// RunMigrateTagsToLabels 将 ai_models.tags（逗号分隔字符串）迁移到 model_labels 表
//
// 策略：
//   - 解析每行 tags，按 aliasMap 将中/英文别名映射到字典 key（如"热卖"→hot）
//   - 白名单：仅明确列出的 key 才会 migrate；供应商名称等非人读标签保留在 tags 不迁移
//   - 幂等：已存在的 (model_id, label_key) 跳过，不覆盖管理员手动打的标签
//
// 执行时机：bootstrap/main 启动时，紧跟 RunSeedLabelDictionary 之后调用
//
// 说明：ai_models.tags 字段保留（过渡期兼容 scraper 的 inferTagsForScraper），
// 下个版本确认 model_labels 稳定后 DROP COLUMN。
func RunMigrateTagsToLabels(db *gorm.DB) {
	log := logger.L
	if log == nil {
		log = zap.NewNop()
	}
	start := time.Now()

	// 1. 构建别名映射（中/英文 tag 字符串 → 字典 key）
	//    未列出的 tag（如 Alibaba/Volc/scraper 写入的供应商 code）不迁移，保留在 ai_models.tags
	aliasMap := map[string]string{
		// 用户标签（中英别名）
		"热卖": "hot", "HOT": "hot", "hot": "hot", "Hot": "hot",
		"优惠": "promo", "Promo": "promo", "promo": "promo", "discount": "promo",
		"新品": "new", "New": "new", "new": "new", "NEW": "new",
		"免费": "free", "Free": "free", "free": "free", "FREE": "free",
		"推荐": "featured", "Featured": "featured", "featured": "featured",
		"测试版": "beta", "Beta": "beta", "beta": "beta", "测试": "beta",
		"即将下线": "deprecated", "Deprecated": "deprecated", "deprecated": "deprecated",

		// 系统标识
		"待定价": "needs_pricing", "NeedsPricing": "needs_pricing", "needs_pricing": "needs_pricing",
		"待设售价": "needs_sell_price", "NeedsSellPrice": "needs_sell_price", "needs_sell_price": "needs_sell_price",

		// 品牌（中英别名）
		"通义千问": "qwen", "Qwen": "qwen", "QWEN": "qwen",
		"豆包": "doubao", "Doubao": "doubao",
		"文心": "ernie", "ERNIE": "ernie", "Ernie": "ernie",
		"混元": "hunyuan", "Hunyuan": "hunyuan",
		"深度求索": "deepseek", "DeepSeek": "deepseek",
		"Claude": "claude",
		"OpenAI": "openai",
		"Gemini": "gemini",
		"月之暗面": "moonshot", "Moonshot": "moonshot", "Kimi": "moonshot",
		"智谱": "glm", "ChatGLM": "glm", "GLM": "glm",
		"MiniMax": "minimax",
	}

	// 2. 查询所有有 tags 的模型
	var models []model.AIModel
	if err := db.Select("id, tags").
		Where("tags IS NOT NULL AND tags != ''").
		Find(&models).Error; err != nil {
		log.Warn("migrate_tags_to_labels: 查询模型失败", zap.Error(err))
		return
	}

	if len(models) == 0 {
		log.Info("migrate_tags_to_labels: 无 tags 数据，跳过",
			zap.Duration("duration", time.Since(start)))
		return
	}

	// 3. 构建已存在的 (model_id, label_key) 索引（一次性加载，避免 N+1 查询）
	type existing struct {
		ModelID  uint
		LabelKey string
	}
	var existingLabels []existing
	db.Model(&model.ModelLabel{}).
		Select("model_id, label_key").
		Where("deleted_at IS NULL").
		Scan(&existingLabels)
	existsSet := make(map[string]bool, len(existingLabels))
	for _, e := range existingLabels {
		existsSet[string(rune(e.ModelID))+"|"+e.LabelKey] = true
	}

	// 4. 逐行迁移
	inserted := 0
	skipped := 0
	unmatched := 0

	for _, m := range models {
		rawTags := strings.Split(m.Tags, ",")
		for _, raw := range rawTags {
			raw = strings.TrimSpace(raw)
			if raw == "" {
				continue
			}

			key, ok := aliasMap[raw]
			if !ok {
				unmatched++
				continue
			}

			// 幂等：已存在的 (model_id, label_key) 跳过
			existsKey := string(rune(m.ID)) + "|" + key
			if existsSet[existsKey] {
				skipped++
				continue
			}

			lbl := model.ModelLabel{
				ModelID:    m.ID,
				LabelKey:   key,
				LabelValue: "", // 纯开关标签，value 留空
			}
			if err := db.Create(&lbl).Error; err != nil {
				// 如果是唯一索引冲突（并发场景），算作跳过
				if strings.Contains(err.Error(), "Duplicate") || strings.Contains(err.Error(), "UNIQUE") {
					skipped++
					continue
				}
				log.Warn("migrate_tags_to_labels: 写入失败",
					zap.Uint("model_id", m.ID), zap.String("key", key), zap.Error(err))
				continue
			}
			existsSet[existsKey] = true
			inserted++
		}
	}

	log.Info("migrate_tags_to_labels: 完成",
		zap.Int("models_scanned", len(models)),
		zap.Int("inserted", inserted),
		zap.Int("skipped", skipped),
		zap.Int("unmatched", unmatched),
		zap.Duration("duration", time.Since(start)))
}
