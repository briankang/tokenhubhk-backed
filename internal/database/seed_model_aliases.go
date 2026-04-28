package database

import (
	"errors"

	"go.uber.org/zap"
	"gorm.io/gorm"

	"tokenhub-server/internal/model"
	"tokenhub-server/internal/pkg/logger"
)

type defaultModelAliasDef struct {
	AliasName       string
	TargetModelName string
	AliasType       string
	Notes           string
}

var defaultModelAliasDefs = []defaultModelAliasDef{
	{AliasName: "gpt-4", TargetModelName: "gpt-4o", AliasType: model.ModelAliasTypeCompat, Notes: "OpenAI legacy GPT-4 compatibility alias"},
	{AliasName: "gpt-4-turbo", TargetModelName: "gpt-4o", AliasType: model.ModelAliasTypeCompat, Notes: "OpenAI legacy GPT-4 Turbo compatibility alias"},
	{AliasName: "gpt-3.5", TargetModelName: "gpt-3.5-turbo", AliasType: model.ModelAliasTypeCompat, Notes: "OpenAI GPT-3.5 shorthand compatibility alias"},
	{AliasName: "claude-3-5-sonnet", TargetModelName: "claude-3-5-sonnet-20241022", AliasType: model.ModelAliasTypeStable, Notes: "Claude Sonnet stable short alias"},
	{AliasName: "claude-sonnet", TargetModelName: "claude-3-5-sonnet-20241022", AliasType: model.ModelAliasTypeCompat, Notes: "Claude Sonnet family compatibility alias"},
	{AliasName: "claude-haiku", TargetModelName: "claude-3-haiku-20240307", AliasType: model.ModelAliasTypeCompat, Notes: "Claude Haiku family compatibility alias"},
	{AliasName: "gemini-pro", TargetModelName: "gemini-1.5-pro", AliasType: model.ModelAliasTypeCompat, Notes: "Gemini Pro legacy compatibility alias"},
	{AliasName: "gemini-flash", TargetModelName: "gemini-2.0-flash", AliasType: model.ModelAliasTypeCompat, Notes: "Gemini Flash shorthand compatibility alias"},
	{AliasName: "qwen", TargetModelName: "qwen-plus", AliasType: model.ModelAliasTypeCompat, Notes: "Qwen default balanced model alias"},
	{AliasName: "qwen-latest", TargetModelName: "qwen-plus-latest", AliasType: model.ModelAliasTypeLatest, Notes: "Qwen latest balanced model alias"},
	{AliasName: "qwen-coder", TargetModelName: "qwen3-coder-plus", AliasType: model.ModelAliasTypeStable, Notes: "Qwen Coder stable alias"},
	{AliasName: "qwen-vl", TargetModelName: "qwen3-vl-plus", AliasType: model.ModelAliasTypeStable, Notes: "Qwen vision-language stable alias"},
	{AliasName: "kimi", TargetModelName: "kimi-latest", AliasType: model.ModelAliasTypeCompat, Notes: "Kimi default compatibility alias"},
	{AliasName: "moonshot", TargetModelName: "moonshot-v1-auto", AliasType: model.ModelAliasTypeCompat, Notes: "Moonshot default compatibility alias"},
	{AliasName: "ernie", TargetModelName: "ernie-4.5-8k", AliasType: model.ModelAliasTypeCompat, Notes: "ERNIE default compatibility alias"},
	{AliasName: "ernie-bot", TargetModelName: "ernie-4.0-8k", AliasType: model.ModelAliasTypeCompat, Notes: "Baidu ERNIE Bot legacy compatibility alias"},
	{AliasName: "doubao-pro", TargetModelName: "doubao-pro-32k", AliasType: model.ModelAliasTypeCompat, Notes: "Doubao Pro shorthand compatibility alias"},
	{AliasName: "doubao-lite", TargetModelName: "doubao-lite-32k", AliasType: model.ModelAliasTypeCompat, Notes: "Doubao Lite shorthand compatibility alias"},
	{AliasName: "text-embedding-3-small", TargetModelName: "text-embedding-v3", AliasType: model.ModelAliasTypeCompat, Notes: "OpenAI embedding small compatibility alias"},
	{AliasName: "text-embedding-3-large", TargetModelName: "text-embedding-v3", AliasType: model.ModelAliasTypeCompat, Notes: "OpenAI embedding large compatibility alias"},
	{AliasName: "sora", TargetModelName: "sora-2", AliasType: model.ModelAliasTypeCompat, Notes: "Sora video generation compatibility alias"},
	{AliasName: "sora-latest", TargetModelName: "sora-2", AliasType: model.ModelAliasTypeLatest, Notes: "Sora latest video generation alias"},
}

// RunSeedModelAliases creates common public model aliases for OpenAI-compatible clients.
//
// The seeder only inserts an alias when its target model is already present, so a
// stale or partially seeded database never advertises aliases that cannot route.
func RunSeedModelAliases(db *gorm.DB) {
	log := logger.L
	if log == nil {
		log = zap.NewNop()
	}
	if db == nil {
		log.Warn("seed model aliases: db is nil, skip")
		return
	}

	created := 0
	existing := 0
	skippedMissingTarget := 0
	skippedErrors := 0

	for _, def := range defaultModelAliasDefs {
		var target model.AIModel
		err := db.Where("model_name = ? AND is_active = ? AND status = ?", def.TargetModelName, true, "online").First(&target).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			skippedMissingTarget++
			continue
		}
		if err != nil {
			log.Warn("seed model aliases: target lookup failed",
				zap.String("alias", def.AliasName),
				zap.String("target", def.TargetModelName),
				zap.Error(err),
			)
			skippedErrors++
			continue
		}

		var alias model.ModelAlias
		err = db.Where("alias_name = ?", def.AliasName).First(&alias).Error
		if err == nil {
			existing++
			continue
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			log.Warn("seed model aliases: alias lookup failed",
				zap.String("alias", def.AliasName),
				zap.Error(err),
			)
			skippedErrors++
			continue
		}

		alias = model.ModelAlias{
			AliasName:          def.AliasName,
			TargetModelName:    def.TargetModelName,
			SupplierID:         target.SupplierID,
			AliasType:          def.AliasType,
			ResolutionStrategy: model.ModelAliasResolutionFixed,
			IsPublic:           true,
			IsActive:           true,
			Source:             model.ModelAliasSourceRule,
			Confidence:         0.95,
			Notes:              def.Notes,
		}
		if err := db.Create(&alias).Error; err != nil {
			log.Warn("seed model aliases: create failed",
				zap.String("alias", def.AliasName),
				zap.String("target", def.TargetModelName),
				zap.Error(err),
			)
			skippedErrors++
			continue
		}
		created++
	}

	log.Info("seed model aliases: complete",
		zap.Int("created", created),
		zap.Int("existing", existing),
		zap.Int("skipped_missing_target", skippedMissingTarget),
		zap.Int("skipped_errors", skippedErrors),
	)
}
