package pricescraper

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"strings"
	"time"

	"go.uber.org/zap"
	"gorm.io/gorm"

	"tokenhub-server/internal/middleware"
	"tokenhub-server/internal/model"
	"tokenhub-server/internal/pkg/logger"
)

// =====================================================
// 价格爬虫主服务
// 统一管理多个供应商爬虫，提供预览/应用/日志查询功能
// =====================================================

// Scraper 爬虫接口
// 每个供应商实现此接口以执行价格爬取
type Scraper interface {
	ScrapePrices(ctx context.Context) (*ScrapedPriceData, error)
}

// KeySetter 可选接口：支持动态注入 API Key（优先于环境变量）
type KeySetter interface {
	SetAPIKey(key string)
}

// ScrapedPriceData 爬取结果
type ScrapedPriceData struct {
	SupplierID   uint           `json:"supplier_id"`
	SupplierName string         `json:"supplier_name"`
	FetchedAt    time.Time      `json:"fetched_at"`
	Models       []ScrapedModel `json:"models"`
	SourceURL    string         `json:"source_url"`
}

// 计费单位常量（与 model.Unit* 保持对齐）
const (
	PricingUnitPerMillionTokens     = "per_million_tokens"     // 元/百万token（默认，LLM/VLM/Embedding/Seedance 视频）
	PricingUnitPerImage             = "per_image"              // 元/张（图片生成 Seedream/wanx/cogview/dall-e）
	PricingUnitPerSecond            = "per_second"             // 元/秒（视频 wanx-video / cogvideo）
	PricingUnitPerMinute            = "per_minute"             // 元/分钟（whisper）
	PricingUnitPer10kCharacters     = "per_10k_characters"     // 元/万字符（豆包 TTS 2.0）
	PricingUnitPerMillionCharacters = "per_million_characters" // 元/百万字符（qwen-tts / openai tts-1）
	PricingUnitPerCall              = "per_call"               // 元/次（Rerank / 意图识别）
	PricingUnitPerHour              = "per_hour"               // 元/小时（ASR 豆包/paraformer）

	// 历史遗留别名，等价于 PricingUnitPer10kCharacters
	PricingUnitPerKChars = "per_k_chars"
)

// ScrapedModel 单个模型的价格数据
type ScrapedModel struct {
	ModelName   string            `json:"model_name"`    // 模型标识名
	DisplayName string           `json:"display_name"`   // 展示名称
	InputPrice  float64          `json:"input_price"`    // 基础输入价格（单位由 PricingUnit 决定）
	OutputPrice float64          `json:"output_price"`   // 基础输出价格（单位由 PricingUnit 决定）
	PriceTiers  []model.PriceTier `json:"price_tiers"`   // 阶梯价格
	Currency    string           `json:"currency"`       // 币种
	PricingUnit string           `json:"pricing_unit"`   // 计费单位: per_million_tokens(默认)/per_image/per_second/per_minute/per_10k_characters/per_million_characters/per_call/per_hour
	ModelType   string           `json:"model_type"`     // 模型类型: LLM/Vision/Embedding/ImageGeneration/VideoGeneration/TTS/ASR/Rerank
	Variant     string           `json:"variant,omitempty"` // 变体/质量档（如 "1024x1024"/"hd"/"low-latency"）
	Warnings    []string         `json:"warnings"`       // 验证警告

	// 缓存定价字段（未支持时留零值）
	SupportsCache              bool    `json:"supports_cache"`               // 是否支持缓存定价
	CacheMechanism             string  `json:"cache_mechanism"`              // auto/explicit/both/none
	CacheMinTokens             int     `json:"cache_min_tokens"`             // 触发缓存的最小Token门槛
	CacheInputPrice            float64 `json:"cache_input_price"`            // 缓存命中(隐式)输入价，元/百万Token
	CacheExplicitInputPrice    float64 `json:"cache_explicit_input_price"`   // 显式缓存命中价（both模式专用）
	CacheWritePrice            float64 `json:"cache_write_price"`            // 缓存写入价，元/百万Token
	CacheStoragePrice          float64 `json:"cache_storage_price"`          // 缓存存储价，元/百万Token/小时
	CacheSource                string  `json:"cache_source,omitempty"`       // 缓存价来源：scraped（从 HTML 解析）/derived（算法派生）

	// 视频生成模型特殊计价配置（仅 VideoGeneration 使用）
	VideoPricingConfig *model.VideoPricingConfig `json:"video_pricing_config,omitempty"`
}

// PriceDiffItem 价格差异项
type PriceDiffItem struct {
	ModelID           uint              `json:"model_id"`
	ModelName         string            `json:"model_name"`
	CurrentInputRMB   float64           `json:"current_input_rmb"`
	CurrentOutputRMB  float64           `json:"current_output_rmb"`
	NewInputRMB       float64           `json:"new_input_rmb"`
	NewOutputRMB      float64           `json:"new_output_rmb"`
	ActualInputRMB    float64           `json:"actual_input_rmb"`    // 折扣后实际价格
	ActualOutputRMB   float64           `json:"actual_output_rmb"`   // 折扣后实际价格
	InputChangeRatio  float64           `json:"input_change_ratio"`  // 百分比变动
	OutputChangeRatio float64           `json:"output_change_ratio"` // 百分比变动
	PriceTiers        []model.PriceTier `json:"price_tiers,omitempty"`
	PricingUnit       string            `json:"pricing_unit"`        // 计费单位
	ModelType         string            `json:"model_type"`          // 模型类型
	Warnings          []string          `json:"warnings"`
	HasChanges        bool              `json:"has_changes"`

	// 缓存定价（从 ScrapedModel 直传，供前端构建 PriceUpdateRequest 使用）
	SupportsCache              bool    `json:"supports_cache"`
	CacheMechanism             string  `json:"cache_mechanism,omitempty"`
	CacheMinTokens             int     `json:"cache_min_tokens,omitempty"`
	CacheInputPriceRMB         float64 `json:"cache_input_price_rmb,omitempty"`
	CacheExplicitInputPriceRMB float64 `json:"cache_explicit_input_price_rmb,omitempty"`
	CacheWritePriceRMB         float64 `json:"cache_write_price_rmb,omitempty"`
	CacheStoragePriceRMB       float64 `json:"cache_storage_price_rmb,omitempty"`

	// 视频生成模型特殊计价配置
	VideoPricingConfig *model.VideoPricingConfig `json:"video_pricing_config,omitempty"`
}

// PriceDiffResult 完整差异结果
type PriceDiffResult struct {
	SupplierID   uint            `json:"supplier_id"`
	SupplierName string          `json:"supplier_name"`
	FetchedAt    time.Time       `json:"fetched_at"`
	Items        []PriceDiffItem `json:"items"`
	TotalModels  int             `json:"total_models"`
	ChangedCount int             `json:"changed_count"`
	WarningCount int             `json:"warning_count"`
	SourceURL    string          `json:"source_url"`
}

// PriceUpdateRequest 价格更新请求（单个模型）
// ModelID == 0 时代表新模型，将根据 ModelName + SupplierID 执行 INSERT
type PriceUpdateRequest struct {
	ModelID      uint              `json:"model_id"`
	ModelName    string            `json:"model_name,omitempty"`    // 模型名称（INSERT 场景必填）
	DisplayName  string            `json:"display_name,omitempty"`  // 展示名称（可选，默认等于 ModelName）
	InputCostRMB  float64          `json:"input_cost_rmb"`
	OutputCostRMB float64          `json:"output_cost_rmb"`
	PriceTiers   []model.PriceTier `json:"price_tiers,omitempty"`
	PricingUnit  string            `json:"pricing_unit,omitempty"`  // 计费单位
	ModelType    string            `json:"model_type,omitempty"`    // 模型类型
	Variant      string            `json:"variant,omitempty"`       // 变体/质量档（非 Token 单位使用）

	// 缓存定价（从 ScrapedModel 传入）
	SupportsCache              bool    `json:"supports_cache"`
	CacheMechanism             string  `json:"cache_mechanism,omitempty"`
	CacheMinTokens             int     `json:"cache_min_tokens,omitempty"`
	CacheInputPriceRMB         float64 `json:"cache_input_price_rmb,omitempty"`
	CacheExplicitInputPriceRMB float64 `json:"cache_explicit_input_price_rmb,omitempty"`
	CacheWritePriceRMB         float64 `json:"cache_write_price_rmb,omitempty"`
	CacheStoragePriceRMB       float64 `json:"cache_storage_price_rmb,omitempty"`

	// 视频生成模型特殊计价配置
	VideoPricingConfig *model.VideoPricingConfig `json:"video_pricing_config,omitempty"`
}

// ApplyResult 应用价格更新的结果
type ApplyResult struct {
	UpdatedCount  int      `json:"updated_count"`  // 已更新的存量模型数
	InsertedCount int      `json:"inserted_count"` // 新入库的模型数
	SkippedCount  int      `json:"skipped_count"`
	Errors        []string `json:"errors,omitempty"`
}

// PriceScraperService 价格爬虫服务
type PriceScraperService struct {
	db         *gorm.DB
	scrapers   map[string]Scraper // key: 供应商名称关键字
	browserMgr *BrowserManager    // headless 浏览器管理器（火山引擎仍需使用）
}

// NewPriceScraperService 创建价格爬虫服务实例
// 初始化时注册所有已实现的供应商爬虫
// API Key 通过环境变量传入: VOLCENGINE_API_KEY, ALIBABA_API_KEY
func NewPriceScraperService(db *gorm.DB) *PriceScraperService {
	browserMgr := NewBrowserManager()
	s := &PriceScraperService{
		db:         db,
		scrapers:   make(map[string]Scraper),
		browserMgr: browserMgr,
	}

	volcKey := os.Getenv("VOLCENGINE_API_KEY")
	aliKey := os.Getenv("ALIBABA_API_KEY")
	qianfanKey := os.Getenv("QIANFAN_API_KEY")
	hunyuanKey := os.Getenv("TENCENT_HUNYUAN_API_KEY")

	// 注册火山引擎爬虫（API 模型列表 + 浏览器价格）
	s.scrapers["volcengine"] = NewVolcengineScraper(volcKey, browserMgr)
	// 注册阿里云爬虫（纯 API，无需浏览器）
	s.scrapers["alibaba"] = NewAlibabaScraper(aliKey)
	// 注册百度千帆爬虫（OpenAI 兼容 API + 硬编码价格）
	s.scrapers["qianfan"] = NewQianfanScraper(qianfanKey)
	// 注册腾讯混元爬虫（OpenAI 兼容 API + 硬编码价格）
	s.scrapers["hunyuan"] = NewHunyuanScraper(hunyuanKey)
	return s
}

// Close 关闭浏览器管理器，释放 Chromium 进程
// 应在服务关闭时调用（如 main.go 的优雅退出逻辑中）
func (s *PriceScraperService) Close() {
	if s.browserMgr != nil {
		s.browserMgr.Close()
	}
}

// ScrapeAndPreview 爬取并预览价格差异（不写 DB）
// 流程:
//  1. 查找 Supplier 获取名称和折扣
//  2. 根据供应商名称选择爬虫
//  3. 执行爬取
//  4. 验证价格数据
//  5. 对比现有 AIModel 价格，计算差异
//  6. 计算折扣后实际价格
func (s *PriceScraperService) ScrapeAndPreview(ctx context.Context, supplierID uint) (*PriceDiffResult, error) {
	log := logger.L
	if log == nil {
		log = zap.NewNop()
	}

	// 1. 查找供应商
	var supplier model.Supplier
	if err := s.db.First(&supplier, supplierID).Error; err != nil {
		return nil, fmt.Errorf("供应商不存在 (ID=%d): %w", supplierID, err)
	}

	// 2. 匹配爬虫
	scraper, scraperKey := s.matchScraper(supplier.Name, supplier.Code)
	if scraper == nil {
		return nil, fmt.Errorf("未找到供应商 [%s] 对应的爬虫，当前支持: 火山引擎, 阿里云", supplier.Name)
	}

	// 从渠道配置注入 API Key（覆盖环境变量，解决未配置环境变量时 401 的问题）
	if ks, ok := scraper.(KeySetter); ok {
		if key := s.lookupChannelAPIKey(supplierID); key != "" {
			ks.SetAPIKey(key)
			log.Debug("使用渠道配置的 API Key", zap.String("scraper", scraperKey))
		}
	}

	log.Info("开始爬取价格",
		zap.Uint("supplier_id", supplierID),
		zap.String("supplier_name", supplier.Name),
		zap.String("scraper", scraperKey))

	// 3. 执行爬取
	scrapedData, err := scraper.ScrapePrices(ctx)
	if err != nil {
		return nil, fmt.Errorf("爬取失败: %w", err)
	}
	scrapedData.SupplierID = supplierID

	// 3.1 SourceURL 优先级：supplier.pricing_url > 爬虫内置 URL
	// 管理员在供应商管理页配置的定价 URL 最高优先级，保证入库数据与 UI 展示一致
	if strings.TrimSpace(supplier.PricingURL) != "" {
		scrapedData.SourceURL = supplier.PricingURL
	}

	// 4. 验证价格数据
	validationErrors := ValidateScrapedData(scrapedData)
	if len(validationErrors) > 0 {
		log.Warn("价格数据验证有警告",
			zap.Int("error_count", len(validationErrors)),
			zap.String("supplier", supplier.Name))
	}

	// 5. 对比现有模型价格，生成差异
	diffResult, err := s.buildDiffResult(supplier, scrapedData)
	if err != nil {
		return nil, fmt.Errorf("生成价格差异失败: %w", err)
	}

	log.Info("价格预览完成",
		zap.Uint("supplier_id", supplierID),
		zap.Int("total_models", diffResult.TotalModels),
		zap.Int("changed", diffResult.ChangedCount),
		zap.Int("warnings", diffResult.WarningCount))

	return diffResult, nil
}

// ApplyPrices 应用价格更新（事务写入）
// 在事务中批量更新 AIModel 的价格字段，并记录 PriceSyncLog
func (s *PriceScraperService) ApplyPrices(ctx context.Context, supplierID uint, updates []PriceUpdateRequest) (*ApplyResult, error) {
	log := logger.L
	if log == nil {
		log = zap.NewNop()
	}

	// 查找供应商
	var supplier model.Supplier
	if err := s.db.First(&supplier, supplierID).Error; err != nil {
		return nil, fmt.Errorf("供应商不存在 (ID=%d): %w", supplierID, err)
	}

	result := &ApplyResult{}
	var changes []map[string]interface{} // 记录变更详情

	// 预加载默认分类 ID（INSERT 新模型时使用）
	var defaultCategoryID uint = 1
	var firstCat model.ModelCategory
	if err := s.db.First(&firstCat).Error; err == nil {
		defaultCategoryID = firstCat.ID
	}

	// 事务写入
	err := s.db.Transaction(func(tx *gorm.DB) error {
		for _, u := range updates {
			// ModelID == 0 → 新模型路径：先尝试按 (supplier_id, model_name) 查找，找不到则 INSERT
			if u.ModelID == 0 {
				if strings.TrimSpace(u.ModelName) == "" {
					result.Errors = append(result.Errors, "新模型缺少 model_name，已跳过")
					result.SkippedCount++
					continue
				}

				// 先查是否已存在（避免并发/重试场景下的重复插入）
				var existing model.AIModel
				if err := tx.Where("supplier_id = ? AND model_name = ?", supplierID, u.ModelName).
					First(&existing).Error; err == nil {
					// 已存在 → 退化为 update 路径
					u.ModelID = existing.ID
				} else {
					// 执行 INSERT
					newModel, insertErr := s.insertNewModel(tx, supplierID, supplier.Code, defaultCategoryID, u)
					if insertErr != nil {
						result.Errors = append(result.Errors, fmt.Sprintf("新增模型 [%s] 失败: %v", u.ModelName, insertErr))
						result.SkippedCount++
						continue
					}
					changes = append(changes, map[string]interface{}{
						"action":         "insert",
						"model_id":       newModel.ID,
						"model_name":     newModel.ModelName,
						"new_input_rmb":  newModel.InputCostRMB,
						"new_output_rmb": newModel.OutputCostRMB,
						"pricing_unit":   newModel.PricingUnit,
						"model_type":     newModel.ModelType,
					})
					result.InsertedCount++
					continue
				}
			}

			var aiModel model.AIModel
			if err := tx.First(&aiModel, u.ModelID).Error; err != nil {
				result.Errors = append(result.Errors, fmt.Sprintf("模型 %d 不存在: %v", u.ModelID, err))
				result.SkippedCount++
				continue
			}

			// 记录变更前后的价格
			change := map[string]interface{}{
				"model_id":        aiModel.ID,
				"model_name":      aiModel.ModelName,
				"old_input_rmb":   aiModel.InputCostRMB,
				"old_output_rmb":  aiModel.OutputCostRMB,
				"new_input_rmb":   u.InputCostRMB,
				"new_output_rmb":  u.OutputCostRMB,
			}

			// 更新价格字段
			updateFields := map[string]interface{}{
				"input_cost_rmb":  u.InputCostRMB,
				"output_cost_rmb": u.OutputCostRMB,
				"last_synced_at":  time.Now(),
			}

			// 更新计费单位、模型类型、变体（如果有）
				if u.PricingUnit != "" {
					updateFields["pricing_unit"] = u.PricingUnit
				}
				if u.ModelType != "" {
					updateFields["model_type"] = u.ModelType
				}
				if u.Variant != "" {
					updateFields["variant"] = u.Variant
				}

				// 更新缓存定价字段
				// v3.5：当 scraper 明确指明不支持缓存时强制清零（修正旧数据）
				// 规则：仅 LLM/VLM/Vision 且按 per_million_tokens 计费的模型才允许启用缓存
				cacheEligible := (u.ModelType == "LLM" || u.ModelType == "VLM" || u.ModelType == "Vision") &&
					(u.PricingUnit == "" || u.PricingUnit == PricingUnitPerMillionTokens)

				if u.SupportsCache && cacheEligible {
					updateFields["supports_cache"] = true
					if u.CacheMechanism != "" {
						updateFields["cache_mechanism"] = u.CacheMechanism
					}
					if u.CacheMinTokens > 0 {
						updateFields["cache_min_tokens"] = u.CacheMinTokens
					}
					if u.CacheInputPriceRMB > 0 {
						updateFields["cache_input_price_rmb"] = u.CacheInputPriceRMB
					}
					if u.CacheExplicitInputPriceRMB > 0 {
						updateFields["cache_explicit_input_price_rmb"] = u.CacheExplicitInputPriceRMB
					}
					if u.CacheWritePriceRMB > 0 {
						updateFields["cache_write_price_rmb"] = u.CacheWritePriceRMB
					}
					if u.CacheStoragePriceRMB > 0 {
						updateFields["cache_storage_price_rmb"] = u.CacheStoragePriceRMB
					}
				} else if !cacheEligible {
					// 非 LLM/VLM 类型或非 Token 单位 → 强制清零（修正旧 seed 数据）
					updateFields["supports_cache"] = false
					updateFields["cache_mechanism"] = "none"
					updateFields["cache_min_tokens"] = 0
					updateFields["cache_input_price_rmb"] = 0
					updateFields["cache_explicit_input_price_rmb"] = 0
					updateFields["cache_write_price_rmb"] = 0
					updateFields["cache_storage_price_rmb"] = 0
				}

				// 更新阶梯价格（有显式阶梯用原值；无则注入默认阶梯兜底）
			tiersToWrite := u.PriceTiers
			if len(tiersToWrite) == 0 && (u.InputCostRMB > 0 || u.OutputCostRMB > 0) {
				tiersToWrite = []model.PriceTier{model.DefaultTier(u.InputCostRMB, u.OutputCostRMB)}
			}
			if len(tiersToWrite) > 0 {
				// 归一化 + 排序
				for i := range tiersToWrite {
					tiersToWrite[i].Normalize()
				}
				model.SortTiers(tiersToWrite)
				tiersData := model.PriceTiersData{
					Tiers:     tiersToWrite,
					Currency:  "CNY",
					UpdatedAt: time.Now(),
				}
				tiersJSON, err := json.Marshal(tiersData)
				if err == nil {
					updateFields["price_tiers"] = tiersJSON
				}
			}

			// 更新视频定价配置（如果有）
			if u.VideoPricingConfig != nil {
				vcJSON, err := json.Marshal(u.VideoPricingConfig)
				if err == nil {
					updateFields["video_pricing_config"] = vcJSON
				}
			}

			// 显式 Where，避免 GORM v2 在 map Updates 时误判无 WHERE 条件
			if err := tx.Model(&model.AIModel{}).Where("id = ?", aiModel.ID).Updates(updateFields).Error; err != nil {
				result.Errors = append(result.Errors, fmt.Sprintf("更新模型 %s 价格失败: %v", aiModel.ModelName, err))
				result.SkippedCount++
				continue
			}

			changes = append(changes, change)
			result.UpdatedCount++
		}

		// 记录 PriceSyncLog
		changesJSON, _ := json.Marshal(changes)
		errorsJSON, _ := json.Marshal(result.Errors)

		affected := result.UpdatedCount + result.InsertedCount
		status := "success"
		if len(result.Errors) > 0 && affected > 0 {
			status = "partial_success"
		} else if affected == 0 {
			status = "failed"
		}

		syncLog := model.PriceSyncLog{
			SupplierID:    supplierID,
			SupplierName:  supplier.Name,
			SyncTime:      time.Now(),
			Status:        status,
			FetchStatus:   "applied",
			ModelsChecked: len(updates),
			ModelsUpdated: affected, // 同步日志统计包含更新 + 新增
			ModelsSkipped: result.SkippedCount,
			Changes:       changesJSON,
			Errors:        errorsJSON,
		}
		if err := tx.Create(&syncLog).Error; err != nil {
			log.Error("记录价格同步日志失败", zap.Error(err))
			// 不阻断主流程
		}

		return nil
	})

	if err != nil {
		return nil, fmt.Errorf("事务写入失败: %w", err)
	}

	// 清除公开模型列表缓存，使价格更新/新增立即生效
	if result.UpdatedCount > 0 || result.InsertedCount > 0 {
		middleware.CacheInvalidate("cache:/api/v1/public/models*")
	}

	log.Info("价格应用完成",
		zap.Uint("supplier_id", supplierID),
		zap.Int("updated", result.UpdatedCount),
		zap.Int("inserted", result.InsertedCount),
		zap.Int("skipped", result.SkippedCount))

	return result, nil
}

// GetSyncLogs 查询同步历史日志
// 支持分页查询，按同步时间倒序排列
func (s *PriceScraperService) GetSyncLogs(ctx context.Context, supplierID uint, page, pageSize int) ([]model.PriceSyncLog, int64, error) {
	var logs []model.PriceSyncLog
	var total int64

	query := s.db.Model(&model.PriceSyncLog{})
	if supplierID > 0 {
		query = query.Where("supplier_id = ?", supplierID)
	}

	// 查询总数
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, fmt.Errorf("查询同步日志总数失败: %w", err)
	}

	// 分页查询
	offset := (page - 1) * pageSize
	if err := query.Order("sync_time DESC").Offset(offset).Limit(pageSize).Find(&logs).Error; err != nil {
		return nil, 0, fmt.Errorf("查询同步日志失败: %w", err)
	}

	return logs, total, nil
}

// lookupChannelAPIKey 查询供应商下状态为 active 且 api_key 非空的渠道，返回第一个 API Key
// 用于在爬取前动态注入渠道配置的密钥，替代环境变量方式
func (s *PriceScraperService) lookupChannelAPIKey(supplierID uint) string {
	var ch model.Channel
	err := s.db.Select("api_key").
		Where("supplier_id = ? AND status = 'active' AND api_key != ''", supplierID).
		Order("id ASC").First(&ch).Error
	if err != nil {
		return ""
	}
	return ch.APIKey
}

// =====================================================
// 内部辅助方法
// =====================================================

// matchScraper 根据供应商名称/代码匹配爬虫
// 匹配规则:
//   - 包含"火山"或"volcengine" → 火山引擎爬虫
//   - 包含"阿里"或"alibaba"或"dashscope" → 阿里云爬虫
func (s *PriceScraperService) matchScraper(name, code string) (Scraper, string) {
	nameLower := strings.ToLower(name)
	codeLower := strings.ToLower(code)

	// 火山引擎匹配
	if strings.Contains(nameLower, "火山") || strings.Contains(codeLower, "volcengine") ||
		strings.Contains(nameLower, "volcengine") || strings.Contains(codeLower, "volc") {
		return s.scrapers["volcengine"], "volcengine"
	}

	// 阿里云匹配
	if strings.Contains(nameLower, "阿里") || strings.Contains(codeLower, "alibaba") ||
		strings.Contains(nameLower, "alibaba") || strings.Contains(codeLower, "dashscope") ||
		strings.Contains(nameLower, "dashscope") || strings.Contains(nameLower, "百炼") ||
		strings.Contains(codeLower, "qwen") || strings.Contains(codeLower, "aliyun") {
		return s.scrapers["alibaba"], "alibaba"
	}

	// 百度千帆匹配
	if strings.Contains(nameLower, "千帆") || strings.Contains(nameLower, "qianfan") ||
		strings.Contains(codeLower, "qianfan") || strings.Contains(nameLower, "ernie") ||
		strings.Contains(codeLower, "baidu_qianfan") {
		return s.scrapers["qianfan"], "qianfan"
	}

	// 腾讯混元匹配
	if strings.Contains(nameLower, "腾讯") || strings.Contains(nameLower, "混元") ||
		strings.Contains(codeLower, "hunyuan") || strings.Contains(codeLower, "tencent") ||
		strings.Contains(nameLower, "hunyuan") {
		return s.scrapers["hunyuan"], "hunyuan"
	}

	return nil, ""
}

// buildDiffResult 构建价格差异对比结果
// 将爬取到的模型数据与数据库中现有模型进行对比
func (s *PriceScraperService) buildDiffResult(supplier model.Supplier, scraped *ScrapedPriceData) (*PriceDiffResult, error) {
	// 查询该供应商下所有模型
	var existingModels []model.AIModel
	if err := s.db.Where("supplier_id = ?", supplier.ID).Find(&existingModels).Error; err != nil {
		return nil, fmt.Errorf("查询现有模型失败: %w", err)
	}

	// 构建模型名称到模型的映射（用于快速查找）
	modelMap := make(map[string]model.AIModel, len(existingModels))
	for _, m := range existingModels {
		modelMap[strings.ToLower(m.ModelName)] = m
	}

	// 折扣比例
	discount := supplier.Discount
	if discount <= 0 {
		discount = 1.0 // 无折扣
	}

	result := &PriceDiffResult{
		SupplierID:   supplier.ID,
		SupplierName: supplier.Name,
		FetchedAt:    scraped.FetchedAt,
		TotalModels:  len(scraped.Models),
		SourceURL:    scraped.SourceURL,
	}

	// 异常检测阈值（50%）
	anomalyThreshold := 0.5

	// 两阶段匹配：
	// 阶段 1 — 精确匹配占位：被精确命中的 DB 模型放入 usedIDs，避免后续泛型 fan-out 重复覆盖
	// 阶段 2 — 泛型前缀 fan-out：一条泛型爬取项（如 "doubao-pro"）展开到所有未被占用的
	//        DB 变体（doubao-pro-32k-240515 / doubao-pro-4k-240515 / ...），每个生成独立 diff item
	// 这样避免以前"一对一最佳前缀匹配"只给单个变体赋价的 bug
	usedIDs := make(map[uint]bool, len(existingModels))

	// 阶段 1：精确匹配优先，占位 DB 模型
	type pendingItem struct {
		sm       ScrapedModel
		matches  []model.AIModel // 已匹配 DB 记录；空表示等阶段 2 做 fan-out
		genericK string           // 阶段 2 要 fan-out 的泛型 key
	}
	pending := make([]pendingItem, 0, len(scraped.Models))
	for _, sm := range scraped.Models {
		scrapedLower := strings.ToLower(sm.ModelName)
		if m, ok := modelMap[scrapedLower]; ok {
			usedIDs[m.ID] = true
			pending = append(pending, pendingItem{sm: sm, matches: []model.AIModel{m}})
		} else {
			// 标记为待 fan-out
			pending = append(pending, pendingItem{sm: sm, genericK: scrapedLower})
		}
	}

	// 阶段 2：对未精确命中的项做前缀 fan-out（返回 matches 为空则视为新增）
	for i := range pending {
		p := &pending[i]
		if len(p.matches) > 0 || p.genericK == "" {
			continue
		}
		for key, m := range modelMap {
			if usedIDs[m.ID] {
				continue
			}
			// 前缀匹配：scraped 名称 + "-" 是 DB 模型名前缀
			// 例如 scraped "doubao-pro" 可 fan-out 到 "doubao-pro-32k", "doubao-pro-32k-240515" 等
			if strings.HasPrefix(key, p.genericK+"-") {
				p.matches = append(p.matches, m)
				usedIDs[m.ID] = true
			}
		}
	}

	// 阶段 3：为每个 (scraped, matched_db_model) 组合生成 diff item
	for _, p := range pending {
		sm := p.sm
		baseItem := PriceDiffItem{
			ModelName:    sm.ModelName,
			NewInputRMB:  sm.InputPrice,
			NewOutputRMB: sm.OutputPrice,
			PriceTiers:   sm.PriceTiers,
			PricingUnit:  sm.PricingUnit,
			ModelType:    sm.ModelType,
			Warnings:     sm.Warnings,
			SupportsCache:              sm.SupportsCache,
			CacheMechanism:             sm.CacheMechanism,
			CacheMinTokens:             sm.CacheMinTokens,
			CacheInputPriceRMB:         sm.CacheInputPrice,
			CacheExplicitInputPriceRMB: sm.CacheExplicitInputPrice,
			CacheWritePriceRMB:         sm.CacheWritePrice,
			CacheStoragePriceRMB:       sm.CacheStoragePrice,
			VideoPricingConfig:         sm.VideoPricingConfig,
		}
		if baseItem.PricingUnit == "" {
			baseItem.PricingUnit = PricingUnitPerMillionTokens
		}
		baseItem.ActualInputRMB = roundFloat(sm.InputPrice*discount, 4)
		baseItem.ActualOutputRMB = roundFloat(sm.OutputPrice*discount, 4)

		if len(p.matches) == 0 {
			// 新模型（数据库中不存在），标记为有变更
			item := baseItem
			item.HasChanges = true
			item.Warnings = append(item.Warnings, "新模型，数据库中暂无匹配记录")
			if item.HasChanges {
				result.ChangedCount++
			}
			if len(item.Warnings) > 0 {
				result.WarningCount++
			}
			result.Items = append(result.Items, item)
			continue
		}

		// fan-out：每个匹配的 DB 模型生成一个 diff item
		for _, existing := range p.matches {
			item := baseItem
			// 泛型 fan-out 时保留 DB 的真实 model_name，前端展示更清晰
			if len(p.matches) > 1 || !strings.EqualFold(existing.ModelName, sm.ModelName) {
				item.ModelName = existing.ModelName
			}
			item.ModelID = existing.ID
			item.CurrentInputRMB = existing.InputCostRMB
			item.CurrentOutputRMB = existing.OutputCostRMB

			if existing.InputCostRMB > 0 {
				item.InputChangeRatio = roundFloat((item.ActualInputRMB-existing.InputCostRMB)/existing.InputCostRMB, 4)
			}
			if existing.OutputCostRMB > 0 {
				item.OutputChangeRatio = roundFloat((item.ActualOutputRMB-existing.OutputCostRMB)/existing.OutputCostRMB, 4)
			}

			inputDiff := math.Abs(item.ActualInputRMB - existing.InputCostRMB)
			outputDiff := math.Abs(item.ActualOutputRMB - existing.OutputCostRMB)
			item.HasChanges = inputDiff > 0.0001 || outputDiff > 0.0001
			if !item.HasChanges && hasTierStructureChange(existing.PriceTiers, item.PriceTiers) {
				item.HasChanges = true
			}

			if isAnomaly, _, warning := DetectAnomalies(existing.InputCostRMB, item.ActualInputRMB, anomalyThreshold); isAnomaly {
				item.Warnings = append(item.Warnings, "输入价格: "+warning)
			}
			if isAnomaly, _, warning := DetectAnomalies(existing.OutputCostRMB, item.ActualOutputRMB, anomalyThreshold); isAnomaly {
				item.Warnings = append(item.Warnings, "输出价格: "+warning)
			}

			if item.HasChanges {
				result.ChangedCount++
			}
			if len(item.Warnings) > 0 {
				result.WarningCount++
			}
			result.Items = append(result.Items, item)
		}
	}

	return result, nil
}

// roundFloat 四舍五入到指定小数位
func roundFloat(val float64, precision int) float64 {
	ratio := math.Pow(10, float64(precision))
	return math.Round(val*ratio) / ratio
}

// hasTierStructureChange 判断爬虫返回的阶梯结构是否与数据库现有值不同
// 检测：阶梯数量差异，或阶梯的价格 / 区间边界差异
func hasTierStructureChange(dbTiersRaw model.JSON, scrapedTiers []model.PriceTier) bool {
	// DB 无阶梯，爬虫返回 >1 个阶梯 → 有变更
	if len(dbTiersRaw) == 0 || string(dbTiersRaw) == "null" {
		return len(scrapedTiers) > 1
	}
	var dbData model.PriceTiersData
	if err := json.Unmarshal(dbTiersRaw, &dbData); err != nil {
		return true // 解析失败视为变更（保守策略）
	}
	if len(dbData.Tiers) != len(scrapedTiers) {
		return true
	}
	// 同长度 → 逐阶梯对比价格和边界
	for i := range scrapedTiers {
		if math.Abs(dbData.Tiers[i].InputPrice-scrapedTiers[i].InputPrice) > 0.0001 ||
			math.Abs(dbData.Tiers[i].OutputPrice-scrapedTiers[i].OutputPrice) > 0.0001 {
			return true
		}
		// 边界对比
		dbMax := int64(-1)
		if dbData.Tiers[i].InputMax != nil {
			dbMax = *dbData.Tiers[i].InputMax
		}
		scrapedMax := int64(-1)
		if scrapedTiers[i].InputMax != nil {
			scrapedMax = *scrapedTiers[i].InputMax
		}
		if dbData.Tiers[i].InputMin != scrapedTiers[i].InputMin || dbMax != scrapedMax {
			return true
		}
	}
	return false
}

// findMatchingModel 已弃用：v3.6 起 buildDiffResult 使用两阶段匹配（精确占位 + 泛型 fan-out）
// 保留此函数仅为兼容旧测试；新代码不要使用
// Deprecated: use two-phase matching in buildDiffResult
func findMatchingModel(scrapedName string, modelMap map[string]model.AIModel) (model.AIModel, bool) {
	scrapedLower := strings.ToLower(scrapedName)
	if m, ok := modelMap[scrapedLower]; ok {
		return m, true
	}
	var bestMatch model.AIModel
	bestKey := ""
	for key, m := range modelMap {
		if strings.HasPrefix(key, scrapedLower+"-") {
			if bestKey == "" || key > bestKey {
				bestMatch = m
				bestKey = key
			}
		}
	}
	if bestKey != "" {
		return bestMatch, true
	}
	return model.AIModel{}, false
}

// insertNewModel 为价格爬取到但数据库尚无记录的新模型执行 INSERT
// - CategoryID 使用默认分类（或第一个可用分类）
// - SupplierID 从 Apply 入参传入
// - 计费单位 / 模型类型：请求体提供则使用；否则按模型名推断，最终 fallback 为 LLM+per_million_tokens
// - Status 保持默认 "offline"，等待后续一键检测激活
// - IsActive 对带日期后缀的旧版本模型置为 false，其余默认 true
func (s *PriceScraperService) insertNewModel(tx *gorm.DB, supplierID uint, supplierCode string, defaultCategoryID uint, u PriceUpdateRequest) (*model.AIModel, error) {
	modelName := strings.TrimSpace(u.ModelName)
	if !isValidModelName(modelName) {
		return nil, fmt.Errorf("模型名称非法或包含乱码: %q", modelName)
	}
	displayName := u.DisplayName
	if displayName == "" {
		displayName = modelName
	}

	// 模型类型与计费单位：优先采用前端/爬虫推断值，否则按名称回退
	modelType := u.ModelType
	if modelType == "" {
		modelType = inferModelTypeFromName(modelName)
	}
	pricingUnit := u.PricingUnit
	if pricingUnit == "" {
		pricingUnit = inferPricingUnitFromName(modelName, modelType)
	}
	// 矫正：非 LLM 类模型不应使用 per_million_tokens（爬虫可能对所有模型一律打 token 标签）
	// VideoGeneration 除外（Seedance 等视频模型确实按 token 计费）
	if pricingUnit == PricingUnitPerMillionTokens &&
		(modelType == "ImageGeneration" || modelType == "TTS" || modelType == "ASR" || modelType == "Rerank") {
		pricingUnit = inferPricingUnitFromName(modelName, modelType)
	}

	// 带日期后缀（如 qwen-max-1201）默认停用
	isActive := !hasDatedSuffix(modelName)

	// 定价策略：免费模型打 Free 标签，缺价模型默认停用并打 NeedsPricing 标签
	isFree := IsFreeModel(modelName, u.InputCostRMB, u.OutputCostRMB)
	priceMissing := IsPriceMissing(modelName, pricingUnit, modelType, u.InputCostRMB, u.OutputCostRMB)
	if priceMissing {
		isActive = false
	}
	tags := AugmentTagsForPricing(inferTagsForScraper(modelName, supplierCode), isFree, priceMissing)

	now := time.Now()
	newModel := &model.AIModel{
		CategoryID:    defaultCategoryID,
		SupplierID:    supplierID,
		ModelName:     modelName,
		DisplayName:   displayName,
		Status:        "offline",
		Source:        "auto",
		IsActive:      isActive,
		ModelType:     modelType,
		PricingUnit:   pricingUnit,
		Variant:       u.Variant,
		InputCostRMB:  u.InputCostRMB,
		OutputCostRMB: u.OutputCostRMB,
		Currency:      "CREDIT", // 与现有模型默认值保持一致
		LastSyncedAt:  &now,
		Tags:          tags,
	}

	// 缓存定价字段
	if u.SupportsCache {
		newModel.SupportsCache = true
	}
	if u.CacheMechanism != "" {
		newModel.CacheMechanism = u.CacheMechanism
	}
	if u.CacheMinTokens > 0 {
		newModel.CacheMinTokens = u.CacheMinTokens
	}
	if u.CacheInputPriceRMB > 0 {
		newModel.CacheInputPriceRMB = u.CacheInputPriceRMB
	}
	if u.CacheExplicitInputPriceRMB > 0 {
		newModel.CacheExplicitInputPriceRMB = u.CacheExplicitInputPriceRMB
	}
	if u.CacheWritePriceRMB > 0 {
		newModel.CacheWritePriceRMB = u.CacheWritePriceRMB
	}
	if u.CacheStoragePriceRMB > 0 {
		newModel.CacheStoragePriceRMB = u.CacheStoragePriceRMB
	}

	// 阶梯价格
	if len(u.PriceTiers) > 0 {
		tiersData := model.PriceTiersData{
			Tiers:     u.PriceTiers,
			Currency:  "CNY",
			UpdatedAt: now,
		}
		if tiersJSON, err := json.Marshal(tiersData); err == nil {
			newModel.PriceTiers = tiersJSON
		}
	}

	if err := tx.Create(newModel).Error; err != nil {
		return nil, err
	}
	return newModel, nil
}

// inferModelTypeFromName 按模型名关键词推断模型类型
// 与 modeldiscovery.inferModelTypeFromID 保持同义，独立实现避免 cross-package 依赖
func inferModelTypeFromName(id string) string {
	s := strings.ToLower(id)
	switch {
	case containsAnyStr(s, "image", "wan", "seedream", "cogview", "dall-e", "gpt-image", "imagen", "hunyuan-image"):
		return "ImageGeneration"
	case containsAnyStr(s, "video", "seaweed", "seedance", "wanx-video", "cogvideo", "veo", "hunyuan-video"):
		return "VideoGeneration"
	case containsAnyStr(s, "embedding", "text-embedding"):
		return "Embedding"
	case containsAnyStr(s, "tts", "cosyvoice", "speech-synthesis", "speech-02"):
		return "TTS"
	case containsAnyStr(s, "asr", "paraformer", "sensevoice", "whisper", "recording"):
		return "ASR"
	case containsAnyStr(s, "rerank"):
		return "Rerank"
	case containsAnyStr(s, "vl", "omni", "-mm"):
		return "VLM"
	default:
		return "LLM"
	}
}

// inferPricingUnitFromName 按模型名 + 类型推断计费单位
func inferPricingUnitFromName(id, modelType string) string {
	s := strings.ToLower(id)
	switch modelType {
	case "ImageGeneration":
		return PricingUnitPerImage
	case "VideoGeneration":
		if containsAnyStr(s, "seedance") {
			return PricingUnitPerMillionTokens
		}
		return PricingUnitPerSecond
	case "TTS", "SpeechSynthesis", "TextToSpeech":
		if containsAnyStr(s, "qwen", "openai", "tts-1", "speech-02", "minimax-speech") {
			return PricingUnitPerMillionCharacters
		}
		return PricingUnitPer10kCharacters
	case "ASR", "SpeechRecognition", "SpeechToText":
		if containsAnyStr(s, "whisper") {
			return PricingUnitPerMinute
		}
		return PricingUnitPerHour
	case "Rerank":
		return PricingUnitPerCall
	default:
		return PricingUnitPerMillionTokens
	}
}

// hasDatedSuffix 检测形如 qwen-max-1201 的 MMDD 日期后缀
func hasDatedSuffix(modelName string) bool {
	name := strings.ToLower(modelName)
	if len(name) < 5 {
		return false
	}
	if name[len(name)-5] != '-' {
		return false
	}
	for _, c := range name[len(name)-4:] {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// inferTagsForScraper 基于供应商 code + 模型名的一层简化标签推断
// 仅注入供应商品牌 + 按关键词命中的主流品牌，确保价格流程独立可运行
func inferTagsForScraper(modelName, supplierCode string) string {
	name := strings.ToLower(modelName)
	seen := map[string]bool{}
	var tags []string
	add := func(t string) {
		if t != "" && !seen[t] {
			seen[t] = true
			tags = append(tags, t)
		}
	}
	// 按名称关键词
	switch {
	case strings.Contains(name, "qwen"), strings.Contains(name, "wanx"):
		add("Qwen")
	case strings.Contains(name, "doubao"), strings.Contains(name, "seedream"), strings.Contains(name, "seedance"):
		add("Doubao")
	case strings.Contains(name, "deepseek"):
		add("DeepSeek")
	case strings.Contains(name, "glm"), strings.Contains(name, "cogview"), strings.Contains(name, "cogvideo"):
		add("ChatGLM")
	case strings.Contains(name, "moonshot"), strings.Contains(name, "kimi"):
		add("Moonshot")
	case strings.Contains(name, "gpt"), strings.Contains(name, "dall-e"), strings.Contains(name, "whisper"):
		add("OpenAI")
	case strings.Contains(name, "claude"):
		add("Claude")
	case strings.Contains(name, "gemini"):
		add("Gemini")
	}
	// 按供应商 code
	switch strings.ToLower(supplierCode) {
	case "alibaba", "aliyun", "dashscope":
		add("阿里云")
		add("百炼")
	case "volcengine", "volc":
		add("火山引擎")
		add("豆包")
	}
	return strings.Join(tags, ",")
}

func containsAnyStr(s string, subs ...string) bool {
	for _, sub := range subs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

// isValidModelName 校验模型名称是否为合法的模型 ID
// - 仅允许 a-z A-Z 0-9 - _ . : / 字符
// - 长度 2-128
// - 拦截爬虫编码错误导致的 mojibake（如 "ģ��"、含替换字符 U+FFFD 的串）
func isValidModelName(name string) bool {
	if len(name) < 2 || len(name) > 128 {
		return false
	}
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9':
		case r == '-' || r == '_' || r == '.' || r == ':' || r == '/':
		default:
			return false
		}
	}
	return true
}
