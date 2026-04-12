package pricescraper

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"time"

	"go.uber.org/zap"
	"gorm.io/gorm"

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

// ScrapedPriceData 爬取结果
type ScrapedPriceData struct {
	SupplierID   uint           `json:"supplier_id"`
	SupplierName string         `json:"supplier_name"`
	FetchedAt    time.Time      `json:"fetched_at"`
	Models       []ScrapedModel `json:"models"`
	SourceURL    string         `json:"source_url"`
}

// ScrapedModel 单个模型的价格数据
type ScrapedModel struct {
	ModelName   string            `json:"model_name"`   // 模型标识名
	DisplayName string           `json:"display_name"`  // 展示名称
	InputPrice  float64          `json:"input_price"`   // 基础输入价格（RMB/百万token）
	OutputPrice float64          `json:"output_price"`  // 基础输出价格（RMB/百万token）
	PriceTiers  []model.PriceTier `json:"price_tiers"`  // 阶梯价格
	Currency    string           `json:"currency"`      // 币种
	Warnings    []string         `json:"warnings"`      // 验证警告
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
	Warnings          []string          `json:"warnings"`
	HasChanges        bool              `json:"has_changes"`
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
type PriceUpdateRequest struct {
	ModelID      uint              `json:"model_id"`
	InputCostRMB  float64          `json:"input_cost_rmb"`
	OutputCostRMB float64          `json:"output_cost_rmb"`
	PriceTiers   []model.PriceTier `json:"price_tiers,omitempty"`
}

// ApplyResult 应用价格更新的结果
type ApplyResult struct {
	UpdatedCount int      `json:"updated_count"`
	SkippedCount int      `json:"skipped_count"`
	Errors       []string `json:"errors,omitempty"`
}

// PriceScraperService 价格爬虫服务
type PriceScraperService struct {
	db       *gorm.DB
	scrapers map[string]Scraper // key: 供应商名称关键字
}

// NewPriceScraperService 创建价格爬虫服务实例
// 初始化时注册所有已实现的供应商爬虫
func NewPriceScraperService(db *gorm.DB) *PriceScraperService {
	s := &PriceScraperService{
		db:       db,
		scrapers: make(map[string]Scraper),
	}
	// 注册火山引擎爬虫
	s.scrapers["volcengine"] = NewVolcengineScraper()
	// 注册阿里云爬虫
	s.scrapers["alibaba"] = NewAlibabaScraper()
	return s
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

	// 事务写入
	err := s.db.Transaction(func(tx *gorm.DB) error {
		for _, u := range updates {
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

			// 更新阶梯价格（如果有）
			if len(u.PriceTiers) > 0 {
				tiersData := model.PriceTiersData{
					Tiers:     u.PriceTiers,
					Currency:  "CNY",
					UpdatedAt: time.Now(),
				}
				tiersJSON, err := json.Marshal(tiersData)
				if err == nil {
					updateFields["price_tiers"] = tiersJSON
				}
			}

			if err := tx.Model(&aiModel).Updates(updateFields).Error; err != nil {
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

		status := "success"
		if len(result.Errors) > 0 && result.UpdatedCount > 0 {
			status = "partial_success"
		} else if result.UpdatedCount == 0 {
			status = "failed"
		}

		syncLog := model.PriceSyncLog{
			SupplierID:    supplierID,
			SupplierName:  supplier.Name,
			SyncTime:      time.Now(),
			Status:        status,
			FetchStatus:   "applied",
			ModelsChecked: len(updates),
			ModelsUpdated: result.UpdatedCount,
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

	log.Info("价格应用完成",
		zap.Uint("supplier_id", supplierID),
		zap.Int("updated", result.UpdatedCount),
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

	for _, sm := range scraped.Models {
		item := PriceDiffItem{
			ModelName:   sm.ModelName,
			NewInputRMB: sm.InputPrice,
			NewOutputRMB: sm.OutputPrice,
			PriceTiers:  sm.PriceTiers,
			Warnings:    sm.Warnings,
		}

		// 计算折扣后实际价格
		item.ActualInputRMB = roundFloat(sm.InputPrice*discount, 4)
		item.ActualOutputRMB = roundFloat(sm.OutputPrice*discount, 4)

		// 查找匹配的现有模型
		if existing, ok := modelMap[strings.ToLower(sm.ModelName)]; ok {
			item.ModelID = existing.ID
			item.CurrentInputRMB = existing.InputCostRMB
			item.CurrentOutputRMB = existing.OutputCostRMB

			// 计算输入价格变动比率
			if existing.InputCostRMB > 0 {
				item.InputChangeRatio = roundFloat((item.ActualInputRMB-existing.InputCostRMB)/existing.InputCostRMB, 4)
			}

			// 计算输出价格变动比率
			if existing.OutputCostRMB > 0 {
				item.OutputChangeRatio = roundFloat((item.ActualOutputRMB-existing.OutputCostRMB)/existing.OutputCostRMB, 4)
			}

			// 检测是否有实质变更（容差 0.0001）
			inputDiff := math.Abs(item.ActualInputRMB - existing.InputCostRMB)
			outputDiff := math.Abs(item.ActualOutputRMB - existing.OutputCostRMB)
			item.HasChanges = inputDiff > 0.0001 || outputDiff > 0.0001

			// 异常检测
			if isAnomaly, _, warning := DetectAnomalies(existing.InputCostRMB, item.ActualInputRMB, anomalyThreshold); isAnomaly {
				item.Warnings = append(item.Warnings, "输入价格: "+warning)
			}
			if isAnomaly, _, warning := DetectAnomalies(existing.OutputCostRMB, item.ActualOutputRMB, anomalyThreshold); isAnomaly {
				item.Warnings = append(item.Warnings, "输出价格: "+warning)
			}
		} else {
			// 新模型（数据库中不存在），标记为有变更
			item.HasChanges = true
			item.Warnings = append(item.Warnings, "新模型，数据库中暂无匹配记录")
		}

		if item.HasChanges {
			result.ChangedCount++
		}
		if len(item.Warnings) > 0 {
			result.WarningCount++
		}

		result.Items = append(result.Items, item)
	}

	return result, nil
}

// roundFloat 四舍五入到指定小数位
func roundFloat(val float64, precision int) float64 {
	ratio := math.Pow(10, float64(precision))
	return math.Round(val*ratio) / ratio
}
