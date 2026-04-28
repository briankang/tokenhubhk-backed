package database

import (
	"regexp"
	"strings"

	"go.uber.org/zap"
	"gorm.io/gorm"

	"tokenhub-server/internal/model"
	"tokenhub-server/internal/pkg/logger"
)

var publicModelDescriptionCleanupPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)(?:^|[。；;，,]\s*)?(?:官网价|官方价)\s*[$￥¥]?[0-9.,]+(?:\s*/\s*[$￥¥]?[0-9.,]+)?\s*[×xX*]\s*汇率\s*[0-9.]+[^。；;，,\n]*[。；;，,]?`),
	regexp.MustCompile(`(?i)\s*AI\s*网关（[^）]*）[。；;，,]?`),
	regexp.MustCompile(`(?i)(?:官网价|官方价)[^。\n]*[。]?`),
	regexp.MustCompile(`(?i)成本按[^。\n]*[。]?`),
	regexp.MustCompile(`(?i)售价[^。\n]*官网价[^。\n]*[。]?`),
	regexp.MustCompile(`(?i)(?:来源|Source)\s*[:：][^；。\n]*[；。]?`),
	regexp.MustCompile(`(?i)\s*(?:OpenAI|Together AI|Google|Anthropic|MiniMax)\s+official[^；;。\n]*(?:price|pricing)[^；;。\n]*(?:[.。；;]|$)`),
	regexp.MustCompile(`(?i)\s*\d+x\d+\s+is\s+about[^。；;\n]*(?:[.。；;]|$)`),
	regexp.MustCompile(`(?i)(?:^|[。；;，,]\s*)?合同折扣\s*[0-9.]+[^。；;，,\n]*[。；;，,]?`),
	regexp.MustCompile(`(?i)\s+via\s+(?:Wangsu\s+AI\s+Gateway|Wangsu|网宿(?:网关)?)`),
	regexp.MustCompile(`(?i)\s*Default price uses[^\n]*(?:[.。]|$)`),
	regexp.MustCompile(`(?i)\s*Cost is[^\n]*(?:[.。]|$)`),
	regexp.MustCompile(`(?i)\s*selling price equals official API price[^.。\n]*(?:[.。]|$)`),
	regexp.MustCompile(`(?i),?\s*keep only for Wangsu compatibility`),
	regexp.MustCompile(`(?i),?\s*Wangsu\s*预览命名`),
	regexp.MustCompile(`(?i),?\s*Wangsu\s*自有命名`),
	regexp.MustCompile(`(?i)网宿(?:网关)?`),
	regexp.MustCompile(`(?i)\s*[-–—]\s*经[^。.!；;，,\n]{0,60}(?:代理|网关|中转|转发)[。.!]?`),
	regexp.MustCompile(`(?i)经[^。.!；;，,\n]{0,60}(?:代理|网关|中转|转发)[。.!]?`),
	regexp.MustCompile(`(?i)通过\s*OpenAI\s*兼容协议\s*/\s*v1/chat/completions\s*统一接入[^。.!]*[。.!]?`),
}

// RunPublicModelDescriptionCleanupMigration 清理公开模型描述里的供应商代理/平台接入文案。
// 公开 /models 只展示模型自身描述；数据库也保持同一口径，做到所见即所得。
func RunPublicModelDescriptionCleanupMigration(db *gorm.DB) error {
	var rows []model.AIModel
	if err := db.Select("id", "model_name", "display_name", "description").
		Where("description <> ''").
		Find(&rows).Error; err != nil {
		return err
	}

	updated := 0
	for _, row := range rows {
		cleaned := cleanPublicModelDescription(row.Description, row.DisplayName, row.ModelName)
		if cleaned == row.Description {
			continue
		}
		if err := db.Model(&model.AIModel{}).
			Where("id = ?", row.ID).
			Update("description", cleaned).Error; err != nil {
			return err
		}
		updated++
	}
	if updated > 0 {
		logger.L.Info("public model descriptions cleaned", zap.Int("updated", updated))
	}
	return nil
}

func cleanPublicModelDescription(description, displayName, modelID string) string {
	out := strings.TrimSpace(description)
	for _, pattern := range publicModelDescriptionCleanupPatterns {
		out = pattern.ReplaceAllString(out, "")
	}
	out = strings.TrimSpace(strings.Trim(out, "-–—，,。.!；; "))
	if onlyModelIdentifier(out, displayName, modelID) {
		return ""
	}
	return out
}

func onlyModelIdentifier(text, displayName, modelID string) bool {
	normalize := func(value string) string {
		return strings.ToLower(strings.TrimSpace(strings.Trim(value, "-–—，,。.!；; ")))
	}
	target := normalize(text)
	return target != "" && (target == normalize(displayName) || target == normalize(modelID))
}
