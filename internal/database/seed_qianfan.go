package database

import (
	"encoding/json"
	"fmt"

	"go.uber.org/zap"
	"gorm.io/gorm"

	"tokenhub-server/internal/model"
	"tokenhub-server/internal/pkg/logger"
)

// RunSeedQianfan 幂等种子：增量添加百度千帆 V2 供应商、分类、模型、渠道
//
// 设计原则：
//   - 按 code+access_type 检查供应商是否已存在，已存在则跳过
//   - 按 code 检查分类，按 model_name+supplier_id 检查模型，均已存在则跳过
//   - 渠道按 name 检查，已存在则跳过
//   - 适合在数据库已初始化（RunSeed 已跑过）的情况下增量追加
func RunSeedQianfan(db *gorm.DB) {
	log := logger.L
	if log == nil {
		log = zap.NewNop()
	}
	if db == nil {
		log.Warn("seed_qianfan: db is nil, skip")
		return
	}

	log.Info("seed_qianfan: 开始增量添加百度千帆 V2 数据...")

	// ---- 1. 供应商 ----
	var qianfanSup model.Supplier
	err := db.Where("code = ? AND access_type = ?", "baidu_qianfan", "api").First(&qianfanSup).Error
	if err != nil {
		// 不存在，创建
		qianfanSup = model.Supplier{
			Name:            "百度千帆",
			Code:            "baidu_qianfan",
			BaseURL:         "https://qianfan.baidubce.com/v2",
			Description:     "AuthType: bearer_token",
			IsActive:        true,
			SortOrder:       110,
			AccessType:      "api",
			InputPricePerM:  4.0,
			OutputPricePerM: 16.0,
			Discount:        1.0,
			Status:          "active",
		}
		if createErr := db.Create(&qianfanSup).Error; createErr != nil {
			log.Error("seed_qianfan: 创建供应商失败", zap.Error(createErr))
			return
		}
		log.Info("seed_qianfan: 创建供应商成功", zap.Uint("id", qianfanSup.ID))
	} else {
		log.Info("seed_qianfan: 供应商已存在，跳过", zap.Uint("id", qianfanSup.ID))
	}

	// ---- 2. 分类 ----
	type catDef struct {
		Code        string
		Name        string
		Description string
		SortOrder   int
	}
	catDefs := []catDef{
		{"qianfan_chat", "通用对话", "百度千帆 V2 - 通用对话", 10},
		{"qianfan_reasoning", "推理模型", "百度千帆 V2 - 推理模型", 20},
		{"qianfan_embedding", "向量模型", "百度千帆 V2 - 向量模型", 30},
	}
	catIDMap := make(map[string]uint, 2)
	for _, cd := range catDefs {
		var cat model.ModelCategory
		if catErr := db.Where("code = ?", cd.Code).First(&cat).Error; catErr != nil {
			cat = model.ModelCategory{
				SupplierID:  qianfanSup.ID,
				Name:        cd.Name,
				Code:        cd.Code,
				Description: cd.Description,
				SortOrder:   cd.SortOrder,
			}
			if createErr := db.Create(&cat).Error; createErr != nil {
				log.Error("seed_qianfan: 创建分类失败",
					zap.String("code", cd.Code), zap.Error(createErr))
				continue
			}
			log.Info("seed_qianfan: 创建分类成功", zap.String("code", cd.Code))
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
	}
	modelDefs := []modelDef{
		// ERNIE 4.5 系列（旗舰）
		// 注意：千帆 V2 API 实际 model_id 含 context 规格后缀（如 -32k/-128k），不再使用裸 -8k 命名
		{"qianfan_chat", "ernie-4.5-8k-preview", "ERNIE 4.5 8K Preview", 4, 16, 4096, 8192},
		{"qianfan_chat", "ernie-4.5-turbo-32k", "ERNIE 4.5 Turbo 32K", 2, 8, 8192, 32768},
		{"qianfan_chat", "ernie-4.5-turbo-128k", "ERNIE 4.5 Turbo 128K", 2, 8, 8192, 131072},
		// ERNIE X1 系列（推理模型）
		{"qianfan_reasoning", "ernie-x1.1", "ERNIE X1.1", 4, 16, 8192, 128000},
		{"qianfan_reasoning", "ernie-x1-32k", "ERNIE X1 32K", 4, 16, 8192, 32768},
		{"qianfan_reasoning", "ernie-x1-turbo-32k", "ERNIE X1 Turbo 32K", 2, 8, 8192, 32768},
		// ERNIE 4.0 系列
		{"qianfan_chat", "ernie-4.0-8k-latest", "ERNIE 4.0 8K Latest", 30, 60, 4096, 8192},
		{"qianfan_chat", "ernie-4.0-8k", "ERNIE 4.0 8K", 30, 60, 4096, 8192},
		{"qianfan_chat", "ernie-4.0-turbo-8k", "ERNIE 4.0 Turbo 8K", 20, 60, 4096, 8192},
		// ERNIE 3.5 系列
		{"qianfan_chat", "ernie-3.5-8k", "ERNIE 3.5 8K", 0.8, 2, 4096, 8192},
		{"qianfan_chat", "ernie-3.5-128k", "ERNIE 3.5 128K", 0.8, 2, 4096, 131072},
		// ERNIE Speed Pro（付费，仅 128k 版本可用）
		{"qianfan_chat", "ernie-speed-pro-128k", "ERNIE Speed Pro 128K", 3, 9, 4096, 131072},
		// Embedding 模型
		{"qianfan_embedding", "bge-large-zh", "BGE Large ZH", 0.2, 0, 0, 512},
		{"qianfan_embedding", "bge-large-en-v1.5", "BGE Large EN v1.5", 0.2, 0, 0, 512},
		{"qianfan_embedding", "tao-8k", "Tao 8K Embedding", 0.2, 0, 0, 8192},
	}

	// 模型创建已禁用：交由 auto-discovery（DiscoveryService.SyncFromChannel）从千帆 API 拉取
	// 避免硬编码模型与官方实际模型列表/价格漂移
	_ = modelDefs
	_ = catIDMap
	log.Info("seed_qianfan: 模型创建已禁用，等待 auto-discovery 同步")

	// ---- 4. 真实渠道（未激活，APIKey 需在管理后台配置）----
	// 注意：APIKey 不在代码中存储，请通过管理后台「渠道管理」填入
	var realCh model.Channel
	if db.Where("name = ?", "百度千帆-真实渠道").First(&realCh).Error != nil {
		realCh = model.Channel{
			Name:           "百度千帆-真实渠道",
			SupplierID:     qianfanSup.ID,
			Type:           "openai",
			Endpoint:       "https://qianfan.baidubce.com/v2",
			APIKey:         "", // 通过管理后台「渠道管理」配置，禁止在代码中硬编码
			Models: mustJSONArr([]string{
				"ernie-3.5-128k", "ernie-3.5-8k",
				"ernie-4.0-8k", "ernie-4.0-8k-latest", "ernie-4.0-turbo-8k",
				"ernie-4.5-8k-preview", "ernie-4.5-turbo-32k", "ernie-4.5-turbo-128k",
				"ernie-speed-pro-128k",
				"ernie-x1-32k", "ernie-x1-turbo-32k", "ernie-x1.1",
				"bge-large-zh", "bge-large-en-v1.5", "tao-8k",
			}),
			Weight:         10,
			Priority:       10,
			Status:         "inactive", // 无 APIKey 时保持未激活
			MaxConcurrency: 100,
			QPM:            60,
		}
		if createErr := db.Create(&realCh).Error; createErr != nil {
			log.Error("seed_qianfan: 创建真实渠道失败", zap.Error(createErr))
		} else {
			log.Info("seed_qianfan: 创建真实渠道成功", zap.Uint("id", realCh.ID))
		}
	} else {
		log.Info("seed_qianfan: 真实渠道已存在，跳过", zap.Uint("id", realCh.ID))
	}

	// ---- 6. 将千帆真实渠道加入现有渠道组 ----
	if realCh.ID > 0 {
		addQianfanChannelToGroups(db, realCh.ID)
	}

	log.Info("seed_qianfan: 百度千帆 V2 增量数据添加完成")
}

// addQianfanChannelToGroups 将千帆真实渠道追加到已有渠道组中
// 渠道组 channel_ids 是 JSON 数组，需要解析后追加并回写
func addQianfanChannelToGroups(db *gorm.DB, channelID uint) {
	log := logger.L
	if log == nil {
		log = zap.NewNop()
	}

	// 查找通用对话组和经济路线组
	groupCodes := []string{"general_chat", "economy_route"}
	for _, code := range groupCodes {
		var group model.ChannelGroup
		if err := db.Where("code = ?", code).First(&group).Error; err != nil {
			continue
		}

		// 解析现有 channel_ids
		var ids []uint
		if len(group.ChannelIDs) > 0 {
			if err := json.Unmarshal(group.ChannelIDs, &ids); err != nil {
				continue
			}
		}

		// 检查是否已存在
		for _, id := range ids {
			if id == channelID {
				return
			}
		}

		// 追加
		ids = append(ids, channelID)
		newIDs, err := json.Marshal(ids)
		if err != nil {
			log.Error("seed_qianfan: marshal channel_ids failed", zap.Error(err))
			continue
		}
		db.Model(&model.ChannelGroup{}).Where("id = ?", group.ID).
			Update("channel_ids", newIDs)
		log.Info("seed_qianfan: 已将千帆渠道加入渠道组",
			zap.String("group", code),
			zap.Uint("channel_id", channelID))
	}
}

// mustJSONArr 将 interface{} 编码为 JSON bytes（不返回错误）
func mustJSONArr(v interface{}) []byte {
	b, _ := json.Marshal(v)
	return b
}

// ensureCategoryForSupplierQianfan 确保千帆供应商下存在指定分类
// 用于 RunSeedNonTokenModels 扩展时引用千帆分类
func ensureCategoryForSupplierQianfan(db *gorm.DB, supplierID uint, code, name string) (uint, error) {
	var cat model.ModelCategory
	if err := db.Where("code = ?", code).First(&cat).Error; err == nil {
		return cat.ID, nil
	}
	cat = model.ModelCategory{
		SupplierID:  supplierID,
		Name:        name,
		Code:        code,
		Description: fmt.Sprintf("百度千帆 V2 - %s", name),
		SortOrder:   30,
	}
	if err := db.Create(&cat).Error; err != nil {
		return 0, err
	}
	return cat.ID, nil
}
