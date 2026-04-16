package database

import (
	"encoding/json"
	"fmt"
	"time"

	"go.uber.org/zap"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"

	"tokenhub-server/internal/model"
	"tokenhub-server/internal/pkg/credits"
	"tokenhub-server/internal/pkg/logger"
)

// RunSeed 填充初始种子数据，仅在数据库为空时执行
// 具有幂等性：仅当Supplier表无数据时才插入
func RunSeed(db *gorm.DB) {
	var count int64
	if err := db.Model(&model.Supplier{}).Count(&count).Error; err != nil {
		logger.L.Warn("seed: failed to check supplier count", zap.Error(err))
		return
	}
	if count > 0 {
		logger.L.Info("seed: data already exists, skipping")
		return
	}

	logger.L.Info("seed: populating initial data...")

	if err := db.Transaction(func(tx *gorm.DB) error {
		if err := seedSuppliers(tx); err != nil {
			return fmt.Errorf("suppliers: %w", err)
		}
		if err := seedCategories(tx); err != nil {
			return fmt.Errorf("categories: %w", err)
		}
		if err := seedModels(tx); err != nil {
			return fmt.Errorf("models: %w", err)
		}
		if err := seedChannels(tx); err != nil {
			return fmt.Errorf("channels: %w", err)
		}
		if err := seedChannelGroups(tx); err != nil {
			return fmt.Errorf("channel_groups: %w", err)
		}
		if err := seedBackupRules(tx); err != nil {
			return fmt.Errorf("backup_rules: %w", err)
		}
		if err := seedModelPricings(tx); err != nil {
			return fmt.Errorf("model_pricings: %w", err)
		}
		if err := seedAdminUser(tx); err != nil {
			return fmt.Errorf("admin_user: %w", err)
		}
		if err := seedPaymentConfig(tx); err != nil {
			return fmt.Errorf("payment_config: %w", err)
		}
		if err := seedCodingPlanChannels(tx); err != nil {
			return fmt.Errorf("coding_plan: %w", err)
		}
		return nil
	}); err != nil {
		logger.L.Error("seed: transaction failed", zap.Error(err))
		return
	}

	logger.L.Info("seed: initial data populated successfully")
}

// ---------- helpers ----------

func mustJSON(v interface{}) []byte {
	b, _ := json.Marshal(v)
	return b
}

// pricePerToken converts ￥/million-tokens to per-token price (in credits).
// 1 RMB = 10,000 credits, so multiply RMB price by 10,000
func pricePerToken(pricePerMillion float64) int64 {
	if pricePerMillion <= 0 {
		return 0
	}
	// 每 token 价格 = ￥/百万token / 1,000,000
	// 然后转换为积分：￥ * 10,000
	perTokenRMB := pricePerMillion / 1e6
	return credits.RMBToCredits(perTokenRMB)
}

// ---------- suppliers ----------

// supplierDef 供应商种子数据定义
// 一个供应商可以同时有 api 和 coding_plan 两种接入点类型
type supplierDef struct {
	Name            string  // 供应商名称
	Code            string  // 唯一编码
	BaseURL         string  // API 基础 URL
	AuthType        string  // 认证类型（描述用）
	IsActive        bool    // 是否启用
	AccessType      string  // 接入点类型: api / coding_plan
	InputPricePerM  float64 // 输入tokens官网价格（每百万tokens，人民币）
	OutputPricePerM float64 // 输出tokens官网价格（每百万tokens，人民币）
	Discount        float64 // 折扣比例，1.0 表示无折扣
}

// supplierDefs 供应商种子数据列表
// 每个供应商可以有 api 和 coding_plan 两条记录
var supplierDefs = []supplierDef{
	// API 类型供应商（默认接入点）
	{"OpenAI", "openai", "https://api.openai.com/v1", "bearer_token", true, "api", 17.5, 70, 1.0},
	{"Anthropic", "anthropic", "https://api.anthropic.com", "api_key_header", true, "api", 21, 105, 1.0},
	{"Google Gemini", "google_gemini", "https://generativelanguage.googleapis.com/v1beta", "api_key_param", true, "api", 0.5, 2, 1.0},
	{"Azure OpenAI", "azure_openai", "", "api_key_header", false, "api", 17.5, 70, 1.0},
	{"DeepSeek", "deepseek", "https://api.deepseek.com/v1", "bearer_token", true, "api", 0.5, 2, 1.0},
	{"阿里云百炼", "aliyun_dashscope", "https://dashscope.aliyuncs.com/compatible-mode/v1", "bearer_token", true, "api", 0.3, 0.6, 1.0},
	{"火山引擎", "volcengine", "https://ark.cn-beijing.volces.com/api/v3", "bearer_token", true, "api", 0.8, 2, 1.0},
	{"Moonshot", "moonshot", "https://api.moonshot.cn/v1", "bearer_token", true, "api", 1, 1, 1.0},
	{"智谱GLM", "zhipu", "https://open.bigmodel.cn/api/paas/v4", "jwt_sign", true, "api", 5, 5, 1.0},
	{"百度文心", "baidu_wenxin", "https://aip.baidubce.com", "oauth2_token", true, "api", 0.8, 2, 1.0},
	// 百度千帆 V2（OpenAI 兼容，直接 Bearer bce-v3 密钥，独立于旧文心一言 OAuth2 接口）
	{"百度千帆", "baidu_qianfan", "https://qianfan.baidubce.com/v2", "bearer_token", true, "api", 4, 16, 1.0},
	// Coding Plan 类型供应商（代码补全接入点）
	{"阿里云百炼 (Coding Plan)", "aliyun_dashscope", "https://dashscope.aliyuncs.com/compatible-mode/v1", "bearer_token", true, "coding_plan", 0.3, 0.6, 1.0},
	{"火山引擎 (Coding Plan)", "volcengine", "https://ark.cn-beijing.volces.com/api/v3", "bearer_token", true, "coding_plan", 0.8, 2, 1.0},
}

// seedSuppliers 创建供应商种子数据
// 同一 code 可以有多个 access_type 记录（api / coding_plan）
func seedSuppliers(tx *gorm.DB) error {
	for i, s := range supplierDefs {
		sup := model.Supplier{
			Name:            s.Name,
			Code:            s.Code,
			BaseURL:         s.BaseURL,
			Description:     fmt.Sprintf("AuthType: %s", s.AuthType),
			IsActive:        s.IsActive,
			SortOrder:       (i + 1) * 10,
			AccessType:      s.AccessType,
			InputPricePerM:  s.InputPricePerM,
			OutputPricePerM: s.OutputPricePerM,
			Discount:        s.Discount,
			Status:          "active",
		}
		// 设置默认值
		if sup.AccessType == "" {
			sup.AccessType = "api"
		}
		if sup.Discount == 0 {
			sup.Discount = 1.0
		}
		if sup.Status == "" {
			sup.Status = "active"
		}
		if err := tx.Create(&sup).Error; err != nil {
			return fmt.Errorf("create supplier %s-%s: %w", s.Code, s.AccessType, err)
		}
	}
	return nil
}

// ---------- categories ----------

type categoryDef struct {
	SupplierCode string
	Name         string
	Code         string
}

var categoryDefs = []categoryDef{
	// OpenAI
	{"openai", "通用对话", "openai_chat"},
	{"openai", "编码模型", "openai_code"},
	// Anthropic
	{"anthropic", "通用对话", "anthropic_chat"},
	// Google
	{"google_gemini", "通用对话", "gemini_chat"},
	{"google_gemini", "推理模型", "gemini_reasoning"},
	// Azure
	{"azure_openai", "通用对话", "azure_chat"},
	// DeepSeek
	{"deepseek", "通用对话", "deepseek_chat"},
	{"deepseek", "推理模型", "deepseek_reasoning"},
	// 阿里云
	{"aliyun_dashscope", "通用对话", "qwen_chat"},
	{"aliyun_dashscope", "推理模型", "qwen_reasoning"},
	// 火山引擎
	{"volcengine", "通用对话", "doubao_chat"},
	// Moonshot
	{"moonshot", "通用对话", "moonshot_chat"},
	// 智谱
	{"zhipu", "通用对话", "zhipu_chat"},
	// 百度文心（旧接口）
	{"baidu_wenxin", "通用对话", "ernie_chat"},
	// 百度千帆 V2（新 OpenAI 兼容接口）
	{"baidu_qianfan", "通用对话", "qianfan_chat"},
	{"baidu_qianfan", "推理模型", "qianfan_reasoning"},
}

func seedCategories(tx *gorm.DB) error {
	for i, c := range categoryDefs {
		var sup model.Supplier
		// 只查询 api 类型的供应商记录（分类与 api 类型关联）
		if err := tx.Where("code = ? AND access_type = ?", c.SupplierCode, "api").First(&sup).Error; err != nil {
			return fmt.Errorf("find supplier %s: %w", c.SupplierCode, err)
		}
		cat := model.ModelCategory{
			SupplierID:  sup.ID,
			Name:        c.Name,
			Code:        c.Code,
			Description: fmt.Sprintf("%s - %s", sup.Name, c.Name),
			SortOrder:   (i + 1) * 10,
		}
		if err := tx.Create(&cat).Error; err != nil {
			return fmt.Errorf("create category %s: %w", c.Code, err)
		}
	}
	return nil
}

// ---------- AI models ----------

type aiModelDef struct {
	CategoryCode string
	SupplierCode string
	ModelName    string
	DisplayName  string
	InputPriceM  float64 // ￥ per million tokens
	OutputPriceM float64
	MaxTokens    int
	ContextWin   int
}

var aiModelDefs = []aiModelDef{
	// OpenAI
	{"openai_chat", "openai", "gpt-4o", "GPT-4o", 17.5, 70, 4096, 128000},
	{"openai_chat", "openai", "gpt-4o-mini", "GPT-4o Mini", 1.05, 4.2, 4096, 128000},
	{"openai_chat", "openai", "gpt-3.5-turbo", "GPT-3.5 Turbo", 3.5, 10.5, 4096, 16385},
	// Anthropic
	{"anthropic_chat", "anthropic", "claude-3-5-sonnet-20241022", "Claude 3.5 Sonnet", 21, 105, 8192, 200000},
	{"anthropic_chat", "anthropic", "claude-3-haiku-20240307", "Claude 3 Haiku", 1.75, 8.75, 4096, 200000},
	// Google Gemini
	{"gemini_chat", "google_gemini", "gemini-2.0-flash", "Gemini 2.0 Flash", 0.5, 2, 8192, 1048576},
	{"gemini_reasoning", "google_gemini", "gemini-1.5-pro", "Gemini 1.5 Pro", 8.75, 35, 8192, 2097152},
	// DeepSeek
	{"deepseek_chat", "deepseek", "deepseek-chat", "DeepSeek Chat", 0.5, 2, 4096, 64000},
	{"deepseek_reasoning", "deepseek", "deepseek-reasoner", "DeepSeek Reasoner", 2.5, 10, 4096, 64000},
	// 阿里云百炼
	{"qwen_chat", "aliyun_dashscope", "qwen-turbo", "Qwen Turbo", 0.3, 0.6, 2000, 131072},
	{"qwen_chat", "aliyun_dashscope", "qwen-plus", "Qwen Plus", 0.8, 2, 2000, 131072},
	{"qwen_chat", "aliyun_dashscope", "qwen-max", "Qwen Max", 2.5, 10, 2000, 32768},
	// 火山引擎
	{"doubao_chat", "volcengine", "doubao-pro-4k", "豆包 Pro 4K", 0.8, 2, 4096, 4096},
	{"doubao_chat", "volcengine", "doubao-pro-32k", "豆包 Pro 32K", 0.8, 2, 4096, 32768},
	{"doubao_chat", "volcengine", "doubao-pro-128k", "豆包 Pro 128K", 5, 9, 4096, 131072},
	// Moonshot
	{"moonshot_chat", "moonshot", "moonshot-v1-8k", "Moonshot v1 8K", 1, 1, 4096, 8192},
	{"moonshot_chat", "moonshot", "moonshot-v1-32k", "Moonshot v1 32K", 2, 2, 4096, 32768},
	{"moonshot_chat", "moonshot", "moonshot-v1-128k", "Moonshot v1 128K", 6, 6, 4096, 131072},
	// 智谱
	{"zhipu_chat", "zhipu", "glm-4-plus", "GLM-4 Plus", 5, 5, 4096, 128000},
	{"zhipu_chat", "zhipu", "glm-4-flash", "GLM-4 Flash", 0, 0, 4096, 128000},
	// 百度文心（旧接口）
	{"ernie_chat", "baidu_wenxin", "ernie-4.0-8k", "ERNIE 4.0 8K", 30, 90, 4096, 8192},
	{"ernie_chat", "baidu_wenxin", "ernie-3.5-8k", "ERNIE 3.5 8K", 0.8, 2, 4096, 8192},
	// 百度千帆 V2（新接口，OpenAI 兼容）
	// ERNIE 4.5 系列（旗舰）
	{"qianfan_chat", "baidu_qianfan", "ernie-4.5-8k", "ERNIE 4.5 8K", 4, 16, 4096, 8192},
	{"qianfan_chat", "baidu_qianfan", "ernie-4.5-8k-preview", "ERNIE 4.5 8K Preview", 4, 16, 4096, 8192},
	{"qianfan_chat", "baidu_qianfan", "ernie-4.5-turbo-8k", "ERNIE 4.5 Turbo 8K", 2, 8, 4096, 8192},
	{"qianfan_chat", "baidu_qianfan", "ernie-4.5-turbo-128k", "ERNIE 4.5 Turbo 128K", 2, 8, 4096, 131072},
	// ERNIE X1 系列（推理模型）
	{"qianfan_reasoning", "baidu_qianfan", "ernie-x1", "ERNIE X1", 4, 16, 8192, 128000},
	{"qianfan_reasoning", "baidu_qianfan", "ernie-x1-turbo", "ERNIE X1 Turbo", 2, 8, 8192, 128000},
	// ERNIE 4.0 系列
	{"qianfan_chat", "baidu_qianfan", "ernie-4.0-8k-latest", "ERNIE 4.0 8K Latest", 30, 60, 4096, 8192},
	{"qianfan_chat", "baidu_qianfan", "ernie-4.0-8k", "ERNIE 4.0 8K", 30, 60, 4096, 8192},
	{"qianfan_chat", "baidu_qianfan", "ernie-4.0-turbo-8k", "ERNIE 4.0 Turbo 8K", 20, 60, 4096, 8192},
	// ERNIE 3.5 系列
	{"qianfan_chat", "baidu_qianfan", "ernie-3.5-8k", "ERNIE 3.5 8K", 0.8, 2, 4096, 8192},
	{"qianfan_chat", "baidu_qianfan", "ernie-3.5-128k", "ERNIE 3.5 128K", 0.8, 2, 4096, 131072},
	// ERNIE Speed Pro（付费）
	{"qianfan_chat", "baidu_qianfan", "ernie-speed-pro-8k", "ERNIE Speed Pro 8K", 3, 9, 4096, 8192},
	{"qianfan_chat", "baidu_qianfan", "ernie-speed-pro-128k", "ERNIE Speed Pro 128K", 3, 9, 4096, 131072},
	// ERNIE Speed（免费）
	{"qianfan_chat", "baidu_qianfan", "ernie-speed-8k", "ERNIE Speed 8K", 0, 0, 4096, 8192},
	{"qianfan_chat", "baidu_qianfan", "ernie-speed-128k", "ERNIE Speed 128K", 0, 0, 4096, 131072},
	// ERNIE Lite / Tiny（免费）
	{"qianfan_chat", "baidu_qianfan", "ernie-lite-8k", "ERNIE Lite 8K", 0, 0, 4096, 8192},
	{"qianfan_chat", "baidu_qianfan", "ernie-tiny-8k", "ERNIE Tiny 8K", 0, 0, 4096, 8192},
}

func seedModels(tx *gorm.DB) error {
	for _, m := range aiModelDefs {
		var cat model.ModelCategory
		if err := tx.Where("code = ?", m.CategoryCode).First(&cat).Error; err != nil {
			return fmt.Errorf("find category %s: %w", m.CategoryCode, err)
		}
		var sup model.Supplier
		// 只查询 api 类型的供应商记录（模型与 api 类型关联）
		if err := tx.Where("code = ? AND access_type = ?", m.SupplierCode, "api").First(&sup).Error; err != nil {
			return fmt.Errorf("find supplier %s: %w", m.SupplierCode, err)
		}
		// InputPricePerToken 和 OutputPricePerToken 为积分单位
		aim := model.AIModel{
			CategoryID:          cat.ID,
			SupplierID:          sup.ID,
			ModelName:           m.ModelName,
			DisplayName:         m.DisplayName,
			IsActive:            true,
			MaxTokens:           m.MaxTokens,
			ContextWindow:       m.ContextWin,
			InputPricePerToken:  pricePerToken(m.InputPriceM),
			OutputPricePerToken: pricePerToken(m.OutputPriceM),
			InputCostRMB:        m.InputPriceM / 1e6,
			OutputCostRMB:       m.OutputPriceM / 1e6,
			Currency:            "CREDIT",
		}
		if err := tx.Create(&aim).Error; err != nil {
			return fmt.Errorf("create model %s: %w", m.ModelName, err)
		}
	}
	return nil
}

// ---------- channels ----------

func seedChannels(tx *gorm.DB) error {
	// 为每个 api 类型供应商创建模板渠道（未激活，APIKey为空）
	for _, sd := range supplierDefs {
		// 只为 api 类型创建模板渠道
		if sd.AccessType != "api" {
			continue
		}
		var sup model.Supplier
		if err := tx.Where("code = ? AND access_type = ?", sd.Code, "api").First(&sup).Error; err != nil {
			return fmt.Errorf("find supplier %s: %w", sd.Code, err)
		}

		channelType := sd.Code
		if sd.Code == "aliyun_dashscope" || sd.Code == "volcengine" || sd.Code == "moonshot" ||
			sd.Code == "zhipu" || sd.Code == "baidu_wenxin" || sd.Code == "baidu_qianfan" {
			channelType = "openai" // OpenAI-compatible
		}
		if sd.Code == "azure_openai" {
			channelType = "azure"
		}

		ch := model.Channel{
			Name:           fmt.Sprintf("%s 模板渠道", sd.Name),
			SupplierID:     sup.ID,
			Type:           channelType,
			Endpoint:       sd.BaseURL,
			APIKey:         "",
			Models:         mustJSON([]string{}),
			Weight:         1,
			Priority:       0,
			Status:         "inactive",
			MaxConcurrency: 100,
			QPM:            60,
		}
		if err := tx.Create(&ch).Error; err != nil {
			return fmt.Errorf("create template channel %s: %w", sd.Code, err)
		}
	}

	// 真实渠道：阿里云百炼
	var aliyunSup model.Supplier
	// 只查询 api 类型的供应商
	if err := tx.Where("code = ? AND access_type = ?", "aliyun_dashscope", "api").First(&aliyunSup).Error; err != nil {
		return fmt.Errorf("find aliyun supplier: %w", err)
	}
	aliyunCh := model.Channel{
		Name:           "阿里云百炼-真实渠道",
		SupplierID:     aliyunSup.ID,
		Type:           "openai",
		Endpoint:       "https://dashscope.aliyuncs.com/compatible-mode/v1",
		APIKey:         "sk-2843b10e786f4d20b1d46444a8b020b1",
		Models:         mustJSON([]string{"qwen-turbo", "qwen-plus", "qwen-max"}),
		Weight:         10,
		Priority:       10,
		Status:         "active",
		MaxConcurrency: 100,
		QPM:            60,
	}
	if err := tx.Create(&aliyunCh).Error; err != nil {
		return fmt.Errorf("create aliyun real channel: %w", err)
	}

	// 真实渠道：火山引擎
	var volcSup model.Supplier
	// 只查询 api 类型的供应商
	if err := tx.Where("code = ? AND access_type = ?", "volcengine", "api").First(&volcSup).Error; err != nil {
		return fmt.Errorf("find volcengine supplier: %w", err)
	}
	volcCh := model.Channel{
		Name:           "火山引擎-真实渠道",
		SupplierID:     volcSup.ID,
		Type:           "openai",
		Endpoint:       "https://ark.cn-beijing.volces.com/api/v3",
		APIKey:         "fc1ca371-a7fc-467a-b594-27e247b09dd4",
		Models:         mustJSON([]string{"doubao-pro-4k", "doubao-pro-32k", "doubao-pro-128k"}),
		Weight:         10,
		Priority:       10,
		Status:         "active",
		MaxConcurrency: 100,
		QPM:            60,
	}
	if err := tx.Create(&volcCh).Error; err != nil {
		return fmt.Errorf("create volcengine real channel: %w", err)
	}

	// 真实渠道：百度千帆 V2
	var qianfanSup model.Supplier
	if err := tx.Where("code = ? AND access_type = ?", "baidu_qianfan", "api").First(&qianfanSup).Error; err != nil {
		return fmt.Errorf("find qianfan supplier: %w", err)
	}
	qianfanCh := model.Channel{
		Name:           "百度千帆-真实渠道",
		SupplierID:     qianfanSup.ID,
		Type:           "openai",
		Endpoint:       "https://qianfan.baidubce.com/v2",
		APIKey:         "bce-v3/ALTAK-sLTAcXvm2vjPcjqJmjBRK/bff65bf034b359f46cfdff7bd3b45dbf3548f7a8",
		Models:         mustJSON([]string{"ernie-4.5-8k", "ernie-x1", "ernie-4.0-8k", "ernie-3.5-8k", "ernie-speed-8k", "ernie-lite-8k"}),
		Weight:         10,
		Priority:       10,
		Status:         "active",
		MaxConcurrency: 100,
		QPM:            60,
	}
	if err := tx.Create(&qianfanCh).Error; err != nil {
		return fmt.Errorf("create qianfan real channel: %w", err)
	}

	return nil
}

// ---------- channel groups ----------

func seedChannelGroups(tx *gorm.DB) error {
	// Find the real channel IDs
	var aliyunCh, volcCh, qianfanCh model.Channel
	if err := tx.Where("name = ?", "阿里云百炼-真实渠道").First(&aliyunCh).Error; err != nil {
		return fmt.Errorf("find aliyun channel: %w", err)
	}
	if err := tx.Where("name = ?", "火山引擎-真实渠道").First(&volcCh).Error; err != nil {
		return fmt.Errorf("find volcengine channel: %w", err)
	}
	if err := tx.Where("name = ?", "百度千帆-真实渠道").First(&qianfanCh).Error; err != nil {
		return fmt.Errorf("find qianfan channel: %w", err)
	}

	channelIDs := mustJSON([]uint{aliyunCh.ID, volcCh.ID, qianfanCh.ID})

	// 通用对话组 (Priority)
	g1 := model.ChannelGroup{
		Name:       "通用对话组",
		Code:       "general_chat",
		Strategy:   "Priority",
		ChannelIDs: channelIDs,
		MixMode:    "FALLBACK_CHAIN",
		IsActive:   true,
	}
	if err := tx.Create(&g1).Error; err != nil {
		return fmt.Errorf("create general_chat group: %w", err)
	}

	// 经济路线组 (CostFirst)
	g2 := model.ChannelGroup{
		Name:       "经济路线组",
		Code:       "economy_route",
		Strategy:   "CostFirst",
		ChannelIDs: channelIDs,
		MixMode:    "FALLBACK_CHAIN",
		IsActive:   true,
	}
	if err := tx.Create(&g2).Error; err != nil {
		return fmt.Errorf("create economy_route group: %w", err)
	}

	return nil
}

// ---------- backup rules ----------

func seedBackupRules(tx *gorm.DB) error {
	var primary, backup model.ChannelGroup
	if err := tx.Where("code = ?", "general_chat").First(&primary).Error; err != nil {
		return fmt.Errorf("find general_chat group: %w", err)
	}
	if err := tx.Where("code = ?", "economy_route").First(&backup).Error; err != nil {
		return fmt.Errorf("find economy_route group: %w", err)
	}

	rule := model.BackupRule{
		Name:           "通用对话备用路由",
		ModelPattern:   "*",
		PrimaryGroupID: primary.ID,
		BackupGroupIDs: mustJSON([]uint{backup.ID}),
		SwitchRules: mustJSON(map[string]interface{}{
			"type":                "consecutive_errors",
			"consecutive_errors":  3,
			"window_seconds":     300,
			"cooldown_seconds":   60,
		}),
		IsActive: true,
	}
	if err := tx.Create(&rule).Error; err != nil {
		return fmt.Errorf("create backup rule: %w", err)
	}
	return nil
}

// ---------- model pricings ----------

func seedModelPricings(tx *gorm.DB) error {
	var models []model.AIModel
	if err := tx.Find(&models).Error; err != nil {
		return fmt.Errorf("find models: %w", err)
	}

	now := time.Now()
	for _, m := range models {
		// 定价加价30%（加价后的人民币值）
		inputPriceRMB := m.InputCostRMB * 1.3
		outputPriceRMB := m.OutputCostRMB * 1.3
		mp := model.ModelPricing{
			ModelID:             m.ID,
			InputPricePerToken:  credits.RMBToCredits(inputPriceRMB),
			InputPriceRMB:       inputPriceRMB,
			OutputPricePerToken: credits.RMBToCredits(outputPriceRMB),
			OutputPriceRMB:      outputPriceRMB,
			Currency:            "CREDIT",
			EffectiveFrom:       &now,
		}
		if err := tx.Create(&mp).Error; err != nil {
			return fmt.Errorf("create pricing for model %d: %w", m.ID, err)
		}
	}
	return nil
}

// ---------- admin user ----------

func seedAdminUser(tx *gorm.DB) error {
	const (
		adminUser  = "admin"
		adminEmail = "admin@tokenhubhk.com"
		adminPass  = "admin123456"
	)

	// Check if already exists
	var count int64
	if err := tx.Model(&model.User{}).Where("email = ?", adminEmail).Count(&count).Error; err != nil {
		return fmt.Errorf("check admin: %w", err)
	}
	if count > 0 {
		return nil
	}

	// Ensure tenant exists
	var tenant model.Tenant
	if err := tx.Where("parent_id IS NULL AND level = 1").First(&tenant).Error; err != nil {
		// Create default tenant
		tenant = model.Tenant{
			Name:         "Platform",
			Domain:       "platform",
			Level:        1,
			IsActive:     true,
			ContactEmail: adminEmail,
		}
		if err := tx.Create(&tenant).Error; err != nil {
			return fmt.Errorf("create tenant: %w", err)
		}
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(adminPass), 12)
	if err != nil {
		return fmt.Errorf("bcrypt hash: %w", err)
	}

	user := model.User{
		TenantID:     tenant.ID,
		Email:        adminEmail,
		PasswordHash: string(hash),
		Name:         adminUser,
		Role:         "ADMIN",
		IsActive:     true,
		Language:     "zh",
	}
	if err := tx.Create(&user).Error; err != nil {
		return fmt.Errorf("create admin user: %w", err)
	}

	return nil
}

// ---------- payment config ----------

func seedPaymentConfig(tx *gorm.DB) error {
	// 检查是否已存在
	var count int64
	if err := tx.Model(&model.PaymentProviderConfig{}).Count(&count).Error; err != nil {
		return fmt.Errorf("check payment config: %w", err)
	}
	if count > 0 {
		return nil
	}

	// 支付渠道配置（默认停用）
	providers := []model.PaymentProviderConfig{
		{ProviderType: "WECHAT", DisplayName: "微信支付", IsActive: false, IsSandbox: true, ConfigJSON: "", SortOrder: 10},
		{ProviderType: "ALIPAY", DisplayName: "支付宝", IsActive: false, IsSandbox: true, ConfigJSON: "", SortOrder: 20},
		{ProviderType: "STRIPE", DisplayName: "Stripe", IsActive: false, IsSandbox: true, ConfigJSON: "", SortOrder: 30},
		{ProviderType: "PAYPAL", DisplayName: "PayPal", IsActive: false, IsSandbox: true, ConfigJSON: "", SortOrder: 40},
	}
	for _, p := range providers {
		if err := tx.Create(&p).Error; err != nil {
			return fmt.Errorf("create provider %s: %w", p.ProviderType, err)
		}
	}

	// 付款方式（默认启用对公转账）
	methods := []model.PaymentMethod{
		{Type: "WECHAT", DisplayName: "微信支付", Icon: "wechat", Description: "使用微信扫码支付", IsActive: false, SortOrder: 10},
		{Type: "ALIPAY", DisplayName: "支付宝", Icon: "alipay", Description: "使用支付宝付款", IsActive: false, SortOrder: 20},
		{Type: "STRIPE", DisplayName: "Stripe", Icon: "credit-card", Description: "国际信用卡/借记卡支付", IsActive: false, SortOrder: 30},
		{Type: "PAYPAL", DisplayName: "PayPal", Icon: "paypal", Description: "使用 PayPal 账户付款", IsActive: false, SortOrder: 40},
		{Type: "BANK_TRANSFER", DisplayName: "对公转账", Icon: "building", Description: "银行对公转账，请备注用户ID", IsActive: true, SortOrder: 50},
	}
	for _, m := range methods {
		if err := tx.Create(&m).Error; err != nil {
			return fmt.Errorf("create payment method %s: %w", m.Type, err)
		}
	}

	return nil
}

// ---------- Coding Plan 渠道 ----------

// seedCodingPlanChannels 初始化 Coding Plan 类型的渠道种子数据
// 包括阿里云百炼和火山引擎的 Coding Plan 渠道，以及混合渠道组
func seedCodingPlanChannels(tx *gorm.DB) error {
	// 检查是否已存在 Coding Plan 渠道
	var count int64
	if err := tx.Model(&model.Channel{}).Where("channel_type = ?", "CODING").Count(&count).Error; err != nil {
		return fmt.Errorf("check coding channels: %w", err)
	}
	if count > 0 {
		return nil
	}

	// 查找阿里云和火山引擎供应商（coding_plan 类型）
	var aliyunSup, volcSup model.Supplier
	if err := tx.Where("code = ? AND access_type = ?", "aliyun_dashscope", "coding_plan").First(&aliyunSup).Error; err != nil {
		return fmt.Errorf("find aliyun coding_plan supplier: %w", err)
	}
	if err := tx.Where("code = ? AND access_type = ?", "volcengine", "coding_plan").First(&volcSup).Error; err != nil {
		return fmt.Errorf("find volcengine coding_plan supplier: %w", err)
	}

	// 阿里云百炼 Coding Plan 渠道（使用真实 Key）
	aliyunCodingCh := model.Channel{
		Name:           "阿里云百炼-Coding Plan",
		SupplierID:     aliyunSup.ID,
		Type:           "openai",
		ChannelType:    "CODING",
		Endpoint:       "https://dashscope.aliyuncs.com/compatible-mode/v1",
		APIKey:         "sk-sp-117b06de47cb4212976d5d1e98a6f7f0",
		Models:         mustJSON([]string{"qwen-coder-plus", "qwen-coder-turbo", "qwen-plus", "qwen-turbo"}),
		Weight:         10,
		Priority:       10,
		Status:         "active",
		MaxConcurrency: 100,
		QPM:            60,
	}
	if err := tx.Create(&aliyunCodingCh).Error; err != nil {
		return fmt.Errorf("create aliyun coding channel: %w", err)
	}

	// 火山引擎 Coding Plan 渠道（使用真实 Key）
	volcCodingCh := model.Channel{
		Name:           "火山引擎-Coding Plan",
		SupplierID:     volcSup.ID,
		Type:           "openai",
		ChannelType:    "CODING",
		Endpoint:       "https://ark.cn-beijing.volces.com/api/v3",
		APIKey:         "583a2d35-6453-407d-a845-66965821d959",
		Models:         mustJSON([]string{"doubao-coder", "doubao-coder-pro", "doubao-pro-32k"}),
		Weight:         10,
		Priority:       8,
		Status:         "active",
		MaxConcurrency: 100,
		QPM:            60,
	}
	if err := tx.Create(&volcCodingCh).Error; err != nil {
		return fmt.Errorf("create volcengine coding channel: %w", err)
	}

	// Coding Plan 渠道组（混合阿里云 + 火山引擎，RoundRobin 轮询策略）
	codingGroupIDs := mustJSON([]uint{aliyunCodingCh.ID, volcCodingCh.ID})
	codingGroup := model.ChannelGroup{
		Name:       "Coding Plan 渠道组",
		Code:       "coding_plan",
		Strategy:   "RoundRobin",
		ChannelIDs: codingGroupIDs,
		MixMode:    "FALLBACK_CHAIN",
		IsActive:   true,
	}
	if err := tx.Create(&codingGroup).Error; err != nil {
		return fmt.Errorf("create coding plan group: %w", err)
	}

	logger.L.Info("seed: Coding Plan 渠道初始化完成",
		zap.Uint("aliyun_channel_id", aliyunCodingCh.ID),
		zap.Uint("volc_channel_id", volcCodingCh.ID),
		zap.Uint("group_id", codingGroup.ID),
	)

	return nil
}
