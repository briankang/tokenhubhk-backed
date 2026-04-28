package database

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"go.uber.org/zap"
	"gorm.io/gorm"

	"tokenhub-server/internal/model"
	"tokenhub-server/internal/pkg/logger"
)

// =============================================================================
// 维度配置迁移 (M2, 2026-04-28)
//
// 目标：为已有 AIModel 自动填充 DimensionConfig 字段，使前端能按 schema 渲染
// 多维矩阵编辑器，计费链路能按 DimensionConfig 校验请求 dims 完整性。
//
// 策略：
//   1. AIModel.DimensionConfig 已有（管理员手配） → 跳过
//   2. 含 DimValues 的 PriceTiers → 从 tier 反推维度（实际数据驱动）
//   3. 否则按 ModelType 填默认模板（VideoGeneration/LLM/ImageGeneration 等）
//   4. 单一价模型（无 PriceTiers / 无 dim_values）→ 留空（不强制声明维度）
//
// 触发：bootstrap.RunDataMigrations，幂等。
// =============================================================================

// RunDimensionConfigMigration 为 AIModel 填充 DimensionConfig
//
// 与 RunSeedanceDimValuesMigration 互补：S4 迁移把 PriceTiers 升级到 DimValues 形态，
// 本迁移把这些 DimValues 反向抽取为 ModelDimensionConfig，供前端 / 计费校验使用。
func RunDimensionConfigMigration(db *gorm.DB) {
	log := logger.L
	if log == nil {
		log = zap.NewNop()
	}
	if db == nil {
		log.Warn("dimension config migration: db is nil, skip")
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	type aiModelRow struct {
		ID              uint
		ModelName       string
		ModelType       string
		PriceTiers      []byte
		DimensionConfig []byte
	}

	var rows []aiModelRow
	if err := db.WithContext(ctx).
		Table("ai_models").
		Select("id, model_name, model_type, price_tiers, dimension_config").
		Where("is_active = 1").
		Find(&rows).Error; err != nil {
		log.Error("dimension config migration: query failed", zap.Error(err))
		return
	}

	if len(rows) == 0 {
		log.Info("dimension config migration: no active models, skip")
		return
	}

	migrated := 0
	skippedConfigured := 0
	skippedNoData := 0
	failed := 0

	for _, r := range rows {
		// 已有 DimensionConfig（非空 JSON）→ 跳过
		if hasNonEmptyDimensionConfig(r.DimensionConfig) {
			skippedConfigured++
			continue
		}

		// 1. 优先从 PriceTiers.DimValues 反推维度
		config := inferConfigFromTiers(r.PriceTiers)

		// 2. 无 dim 信息 → 用 ModelType 默认模板（仅视频/LLM 默认填，避免污染单一价模型）
		if config == nil {
			config = defaultConfigForModelType(r.ModelType, r.ModelName)
		}

		if config == nil {
			skippedNoData++
			continue
		}

		raw, err := json.Marshal(config)
		if err != nil {
			failed++
			log.Warn("dimension config migration: marshal failed",
				zap.Uint("id", r.ID), zap.String("model_name", r.ModelName), zap.Error(err))
			continue
		}

		if err := db.WithContext(ctx).
			Table("ai_models").
			Where("id = ?", r.ID).
			Update("dimension_config", raw).Error; err != nil {
			failed++
			log.Warn("dimension config migration: update failed",
				zap.Uint("id", r.ID), zap.String("model_name", r.ModelName), zap.Error(err))
			continue
		}
		migrated++
	}

	log.Info("dimension config migration: complete",
		zap.Int("scanned", len(rows)),
		zap.Int("migrated", migrated),
		zap.Int("skipped_already_configured", skippedConfigured),
		zap.Int("skipped_no_dim_data", skippedNoData),
		zap.Int("failed", failed))
}

// hasNonEmptyDimensionConfig 检查 DimensionConfig JSON 是否实际声明了维度
func hasNonEmptyDimensionConfig(raw []byte) bool {
	if len(raw) == 0 || string(raw) == "null" {
		return false
	}
	var c model.ModelDimensionConfig
	if err := json.Unmarshal(raw, &c); err != nil {
		return false
	}
	return len(c.Dimensions) > 0
}

// inferConfigFromTiers 从 PriceTiers.DimValues 实际数据反推 DimensionConfig
//
// 策略：扫所有 tier，收集每个维度键的所有可能值 → 生成 select 类型 dimension。
// boolean 类型（值集 = {"true","false"}）特殊识别。
func inferConfigFromTiers(raw []byte) *model.ModelDimensionConfig {
	if len(raw) == 0 {
		return nil
	}
	var data model.PriceTiersData
	if err := json.Unmarshal(raw, &data); err != nil {
		return nil
	}
	if len(data.Tiers) == 0 {
		return nil
	}

	// 收集每个键的所有 unique 值（保持出现顺序）
	type valueSet struct {
		seen   map[string]bool
		values []string
	}
	dimMap := make(map[string]*valueSet)
	keyOrder := []string{} // 保持维度键的首次出现顺序

	for _, t := range data.Tiers {
		for k, v := range t.DimValues {
			if v == "" {
				continue
			}
			vs, exists := dimMap[k]
			if !exists {
				vs = &valueSet{seen: make(map[string]bool)}
				dimMap[k] = vs
				keyOrder = append(keyOrder, k)
			}
			if !vs.seen[v] {
				vs.seen[v] = true
				vs.values = append(vs.values, v)
			}
		}
	}

	if len(keyOrder) == 0 {
		return nil
	}

	dims := make([]model.DimensionDefinition, 0, len(keyOrder))
	for _, k := range keyOrder {
		vs := dimMap[k]
		// 识别 boolean：值集恰好是 {"true","false"} 或单一布尔字符串
		isBool := isBooleanValueSet(vs.values)

		var dimType string
		if isBool {
			dimType = model.DimensionTypeBoolean
		} else {
			dimType = model.DimensionTypeSelect
		}

		def := model.DimensionDefinition{
			Key:   k,
			Label: dimLabelChinese(k),
			Type:  dimType,
			Help:  dimHelpChinese(k),
		}
		if dimType == model.DimensionTypeSelect {
			def.Values = vs.values
		}
		// 默认值：第一个值
		if len(vs.values) > 0 {
			d := vs.values[0]
			def.Default = &d
		}
		dims = append(dims, def)
	}

	return &model.ModelDimensionConfig{
		SchemaVersion: 1,
		Dimensions:    dims,
	}
}

func isBooleanValueSet(values []string) bool {
	if len(values) == 0 || len(values) > 2 {
		return false
	}
	for _, v := range values {
		if v != "true" && v != "false" {
			return false
		}
	}
	return true
}

// defaultConfigForModelType 按模型类型给默认维度模板（仅当 ModelType 已知时）
//
// 注意：仅给视频生成填默认维度。LLM/Embedding/Image 等单一价模型不强制声明维度，
// 避免误导用户「这个模型支持 thinking_mode」（实际可能不支持）。
func defaultConfigForModelType(modelType, modelName string) *model.ModelDimensionConfig {
	mt := strings.ToLower(modelType)
	if mt != "videogeneration" {
		return nil
	}
	// 视频生成默认填 5 维度（Seedance 全集，具体模型不一定都用得上，但前端可见可改）
	return &model.ModelDimensionConfig{
		SchemaVersion: 1,
		Dimensions:    model.VideoGenerationDefaultDimensions(),
	}
}

func dimLabelChinese(key string) string {
	labels := map[string]string{
		"resolution":      "分辨率",
		"input_has_video": "是否含输入视频",
		"inference_mode":  "推理模式",
		"audio_mode":      "是否生成音频",
		"draft_mode":      "Draft 样片",
		"context_tier":    "上下文档位",
		"thinking_mode":   "思考模式",
		"cache_hit_kind":  "缓存命中类型",
		"image_size":      "图片尺寸",
		"image_quality":   "图片质量",
	}
	if v, ok := labels[key]; ok {
		return v
	}
	return key
}

func dimHelpChinese(key string) string {
	helps := map[string]string{
		"resolution":      "Seedance 2.0 / wan2.7-t2v 等支持多档分辨率定价，价格随分辨率递增",
		"input_has_video": "Seedance 2.0：含视频输入时单价更低（输入视频已含部分上下文，计算资源减少）",
		"inference_mode":  "Seedance 1.0/1.5 系列支持离线推理（service_tier=flex），价格通常半价",
		"audio_mode":      "Seedance 1.5 Pro：含音频时按全价计费；无音频时半价",
		"draft_mode":      "Seedance 1.5 Pro 仅 480p 支持，token 折算 0.6（有声）/ 0.7（无声）",
		"context_tier":    "按输入 tokens 区间分阶梯定价（doubao-pro / qwen3-max 等）",
		"thinking_mode":   "qwen3.5-flash / qwq / qvq 等推理模型；部分模型思考价显著高于非思考价",
	}
	if v, ok := helps[key]; ok {
		return v
	}
	return ""
}
