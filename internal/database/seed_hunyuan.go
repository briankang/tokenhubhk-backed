package database

import (
	"encoding/json"

	"go.uber.org/zap"
	"gorm.io/gorm"

	"tokenhub-server/internal/model"
	"tokenhub-server/internal/pkg/credits"
	"tokenhub-server/internal/pkg/logger"
)

// RunSeedHunyuan 幂等种子：增量添加腾讯混元供应商、分类、模型、渠道
//
// 设计原则：
//   - 按 code+access_type 检查供应商是否已存在，已存在则跳过
//   - 按 code 检查分类，按 model_name+supplier_id 检查模型，均已存在则跳过
//   - 渠道按 name 检查，已存在则跳过
//   - 适合在数据库已初始化（RunSeed 已跑过）的情况下增量追加
func RunSeedHunyuan(db *gorm.DB) {
	log := logger.L
	if log == nil {
		log = zap.NewNop()
	}
	if db == nil {
		log.Warn("seed_hunyuan: db is nil, skip")
		return
	}

	log.Info("seed_hunyuan: 开始增量添加腾讯混元数据...")

	// ---- 1. 供应商 ----
	var hunyuanSup model.Supplier
	err := db.Where("code = ? AND access_type = ?", "tencent_hunyuan", "api").First(&hunyuanSup).Error
	if err != nil {
		hunyuanSup = model.Supplier{
			Name:            "腾讯混元",
			Code:            "tencent_hunyuan",
			BaseURL:         "https://api.hunyuan.cloud.tencent.com/v1",
			Description:     "AuthType: bearer_token | 腾讯云混元大模型，OpenAI 兼容接口",
			IsActive:        true,
			SortOrder:       120,
			AccessType:      "api",
			InputPricePerM:  4.5,
			OutputPricePerM: 5.0,
			Discount:        1.0,
			Status:          "active",
		}
		if createErr := db.Create(&hunyuanSup).Error; createErr != nil {
			log.Error("seed_hunyuan: 创建供应商失败", zap.Error(createErr))
			return
		}
		log.Info("seed_hunyuan: 创建供应商成功", zap.Uint("id", hunyuanSup.ID))
	} else {
		log.Info("seed_hunyuan: 供应商已存在，跳过", zap.Uint("id", hunyuanSup.ID))
	}

	// ---- 2. 分类 ----
	type catDef struct {
		Code        string
		Name        string
		Description string
		SortOrder   int
	}
	catDefs := []catDef{
		{"hunyuan_chat", "通用对话", "腾讯混元 - 通用对话模型", 10},
		{"hunyuan_vision", "多模态视觉", "腾讯混元 - 多模态视觉模型", 20},
		{"hunyuan_code", "代码生成", "腾讯混元 - 代码生成模型", 30},
		{"hunyuan_tool", "工具与角色", "腾讯混元 - 工具调用与角色扮演模型", 40},
	}
	catIDMap := make(map[string]uint, len(catDefs))
	for _, cd := range catDefs {
		var cat model.ModelCategory
		if catErr := db.Where("code = ?", cd.Code).First(&cat).Error; catErr != nil {
			cat = model.ModelCategory{
				SupplierID:  hunyuanSup.ID,
				Name:        cd.Name,
				Code:        cd.Code,
				Description: cd.Description,
				SortOrder:   cd.SortOrder,
			}
			if createErr := db.Create(&cat).Error; createErr != nil {
				log.Error("seed_hunyuan: 创建分类失败",
					zap.String("code", cd.Code), zap.Error(createErr))
				continue
			}
			log.Info("seed_hunyuan: 创建分类成功", zap.String("code", cd.Code))
		}
		catIDMap[cd.Code] = cat.ID
	}

	// ---- 3. 模型 ----
	type modelDef struct {
		CategoryCode string
		ModelName    string
		DisplayName  string
		InputPriceM  float64 // ¥/百万tokens
		OutputPriceM float64
		MaxTokens    int
		ContextWin   int
		ModelType    string // LLM / VLM
		Tags         string
	}
	modelDefs := []modelDef{
		// 通用对话系列
		{"hunyuan_chat", "hunyuan-lite", "Hunyuan Lite", 0, 0, 4096, 262144, "LLM", "Tencent,Free"},
		{"hunyuan_chat", "hunyuan-standard", "Hunyuan Standard", 4.5, 5, 4096, 32768, "LLM", "Tencent"},
		{"hunyuan_chat", "hunyuan-standard-256k", "Hunyuan Standard 256K", 15, 60, 4096, 262144, "LLM", "Tencent,LongContext"},
		{"hunyuan_chat", "hunyuan-pro", "Hunyuan Pro", 30, 100, 4096, 32768, "LLM", "Tencent"},
		{"hunyuan_chat", "hunyuan-turbo", "Hunyuan Turbo", 15, 50, 4096, 32768, "LLM", "Tencent"},
		{"hunyuan_chat", "hunyuan-turbo-latest", "Hunyuan Turbo Latest", 15, 50, 4096, 32768, "LLM", "Tencent"},
		{"hunyuan_chat", "hunyuan-large", "Hunyuan Large", 4, 8, 4096, 262144, "LLM", "Tencent,LongContext"},

		// 代码生成
		{"hunyuan_code", "hunyuan-code", "Hunyuan Code", 4.5, 5, 4096, 32768, "LLM", "Tencent,Coding"},

		// 工具与角色
		{"hunyuan_tool", "hunyuan-role", "Hunyuan Role", 4.5, 5, 4096, 32768, "LLM", "Tencent,RolePlay"},
		{"hunyuan_tool", "hunyuan-functioncall", "Hunyuan FunctionCall", 4.5, 5, 4096, 32768, "LLM", "Tencent,FunctionCall"},

		// 多模态视觉
		{"hunyuan_vision", "hunyuan-vision", "Hunyuan Vision", 18, 22, 4096, 8192, "VLM", "Tencent,Vision"},
		{"hunyuan_vision", "hunyuan-turbo-vision", "Hunyuan Turbo Vision", 40, 80, 4096, 8192, "VLM", "Tencent,Vision"},
	}

	createdModels := 0
	for _, md := range modelDefs {
		catID, ok := catIDMap[md.CategoryCode]
		if !ok {
			var cat model.ModelCategory
			if findErr := db.Where("code = ?", md.CategoryCode).First(&cat).Error; findErr != nil {
				log.Warn("seed_hunyuan: 分类未找到，跳过模型",
					zap.String("category", md.CategoryCode),
					zap.String("model", md.ModelName))
				continue
			}
			catID = cat.ID
		}

		var existing model.AIModel
		if findErr := db.Where("supplier_id = ? AND model_name = ?",
			hunyuanSup.ID, md.ModelName).First(&existing).Error; findErr == nil {
			// 已存在：更新价格 + 上线
			db.Model(&model.AIModel{}).Where("id = ?", existing.ID).Updates(map[string]interface{}{
				"input_cost_rmb":         md.InputPriceM,
				"output_cost_rmb":        md.OutputPriceM,
				"input_price_per_token":  credits.RMBToCredits(md.InputPriceM / 1e6),
				"output_price_per_token": credits.RMBToCredits(md.OutputPriceM / 1e6),
				"context_window":         md.ContextWin,
				"max_tokens":             md.MaxTokens,
				"status":                 "online",
				"is_active":              true,
			})
			continue
		}

		aim := model.AIModel{
			CategoryID:          catID,
			SupplierID:          hunyuanSup.ID,
			ModelName:           md.ModelName,
			DisplayName:         md.DisplayName,
			IsActive:            true,
			Status:              "online",
			MaxTokens:           md.MaxTokens,
			ContextWindow:       md.ContextWin,
			InputPricePerToken:  credits.RMBToCredits(md.InputPriceM / 1e6),
			OutputPricePerToken: credits.RMBToCredits(md.OutputPriceM / 1e6),
			InputCostRMB:        md.InputPriceM,
			OutputCostRMB:       md.OutputPriceM,
			Currency:            "CREDIT",
			ModelType:           md.ModelType,
			PricingUnit:         "per_million_tokens",
			Source:              "seed",
			Tags:                md.Tags,
		}
		if createErr := db.Create(&aim).Error; createErr != nil {
			log.Error("seed_hunyuan: 创建模型失败",
				zap.String("model", md.ModelName), zap.Error(createErr))
			continue
		}

		// 创建对应的 ModelPricing 记录（加价 30%）
		inputPriceMRmb := md.InputPriceM * 1.3
		outputPriceMRmb := md.OutputPriceM * 1.3
		mp := model.ModelPricing{
			ModelID:             aim.ID,
			InputPricePerToken:  credits.RMBToCredits(inputPriceMRmb / 1e6),
			InputPriceRMB:       inputPriceMRmb,
			OutputPricePerToken: credits.RMBToCredits(outputPriceMRmb / 1e6),
			OutputPriceRMB:      outputPriceMRmb,
			Currency:            "CREDIT",
		}
		db.Create(&mp)

		createdModels++
	}
	log.Info("seed_hunyuan: 模型种子完成", zap.Int("created", createdModels))

	// ---- 4. 模板渠道（未激活）----
	var templateCh model.Channel
	if db.Where("name = ?", "腾讯混元 模板渠道").First(&templateCh).Error != nil {
		modelsJSON, _ := json.Marshal([]string{})
		templateCh = model.Channel{
			Name:           "腾讯混元 模板渠道",
			SupplierID:     hunyuanSup.ID,
			Type:           "openai",
			Endpoint:       "https://api.hunyuan.cloud.tencent.com/v1",
			APIKey:         "",
			Models:         modelsJSON,
			Weight:         1,
			Priority:       0,
			Status:         "inactive",
			MaxConcurrency: 100,
			QPM:            60,
		}
		if createErr := db.Create(&templateCh).Error; createErr != nil {
			log.Error("seed_hunyuan: 创建模板渠道失败", zap.Error(createErr))
		} else {
			log.Info("seed_hunyuan: 创建模板渠道成功")
		}
	}

	// ---- 5. 真实渠道（已激活）----
	allModelNames := make([]string, 0, len(modelDefs))
	for _, md := range modelDefs {
		allModelNames = append(allModelNames, md.ModelName)
	}

	var realCh model.Channel
	if db.Where("name = ?", "腾讯混元-真实渠道").First(&realCh).Error != nil {
		modelsJSON, _ := json.Marshal(allModelNames)
		realCh = model.Channel{
			Name:           "腾讯混元-真实渠道",
			SupplierID:     hunyuanSup.ID,
			Type:           "openai",
			Endpoint:       "https://api.hunyuan.cloud.tencent.com/v1",
			APIKey:         "", // 通过管理后台「渠道管理」配置，禁止在代码中硬编码
			Models:         modelsJSON,
			Weight:         10,
			Priority:       10,
			Status:         "inactive", // 无 APIKey 时保持未激活
			MaxConcurrency: 100,
			QPM:            60,
		}
		if createErr := db.Create(&realCh).Error; createErr != nil {
			log.Error("seed_hunyuan: 创建真实渠道失败", zap.Error(createErr))
		} else {
			log.Info("seed_hunyuan: 创建真实渠道成功", zap.Uint("id", realCh.ID))
		}
	} else {
		log.Info("seed_hunyuan: 真实渠道已存在，跳过", zap.Uint("id", realCh.ID))
	}

	// ---- 6. 将混元真实渠道加入现有渠道组 ----
	if realCh.ID > 0 {
		addHunyuanChannelToGroups(db, realCh.ID)
	}

	log.Info("seed_hunyuan: 腾讯混元增量数据添加完成")
}

// addHunyuanChannelToGroups 将混元真实渠道追加到已有渠道组中
func addHunyuanChannelToGroups(db *gorm.DB, channelID uint) {
	log := logger.L
	if log == nil {
		log = zap.NewNop()
	}

	groupCodes := []string{"general_chat", "economy_route"}
	for _, code := range groupCodes {
		var group model.ChannelGroup
		if err := db.Where("code = ?", code).First(&group).Error; err != nil {
			continue
		}

		var ids []uint
		if len(group.ChannelIDs) > 0 {
			if err := json.Unmarshal(group.ChannelIDs, &ids); err != nil {
				continue
			}
		}

		for _, id := range ids {
			if id == channelID {
				return
			}
		}

		ids = append(ids, channelID)
		newIDs, err := json.Marshal(ids)
		if err != nil {
			log.Error("seed_hunyuan: marshal channel_ids failed", zap.Error(err))
			continue
		}
		db.Model(&model.ChannelGroup{}).Where("id = ?", group.ID).
			Update("channel_ids", newIDs)
		log.Info("seed_hunyuan: 已将混元渠道加入渠道组",
			zap.String("group", code),
			zap.Uint("channel_id", channelID))
	}
}
