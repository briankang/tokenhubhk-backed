package pricescraper

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"go.uber.org/zap"
	"gorm.io/gorm"

	"tokenhub-server/internal/middleware"
	"tokenhub-server/internal/model"
	"tokenhub-server/internal/pkg/logger"
)

// =====================================================
// 批量按模型ID精准抓取价格
// 流程：按 supplier_id 分组 → 各 supplier 调对应 scraper（共用 HTML 缓存）
//       → 按 model_ids 过滤抓取结果 → 生成逐字段 diff → 选择性写库
// =====================================================

// ItemStatus 单个模型的抓取状态
type ItemStatus string

const (
	StatusOK                  ItemStatus = "ok"                   // 抓取成功且有差异
	StatusUnchanged           ItemStatus = "unchanged"            // 抓取成功但与现有价格一致
	StatusNotFound            ItemStatus = "not_found"            // 供应商定价页未找到该模型
	StatusUnsupportedSupplier ItemStatus = "unsupported_supplier" // 供应商暂无对应爬虫
	StatusError               ItemStatus = "error"                // 爬取/匹配过程出错
)

// PriceSnapshot 模型现有价格快照
type PriceSnapshot struct {
	InputCostRMB               float64             `json:"input_cost_rmb"`
	OutputCostRMB              float64             `json:"output_cost_rmb"`
	PriceTiers                 []model.PriceTier   `json:"price_tiers,omitempty"`
	PricingUnit                string              `json:"pricing_unit"`
	ModelType                  string              `json:"model_type"`
	SupportsCache              bool                `json:"supports_cache"`
	CacheMechanism             string              `json:"cache_mechanism,omitempty"`
	CacheMinTokens             int                 `json:"cache_min_tokens,omitempty"`
	CacheInputPriceRMB         float64             `json:"cache_input_price_rmb,omitempty"`
	CacheExplicitInputPriceRMB float64             `json:"cache_explicit_input_price_rmb,omitempty"`
	CacheWritePriceRMB         float64             `json:"cache_write_price_rmb,omitempty"`
	CacheStoragePriceRMB       float64             `json:"cache_storage_price_rmb,omitempty"`
}

// FieldDiff 单字段差异（供前端选择性应用）
type FieldDiff struct {
	Field    string      `json:"field"`     // input_price / output_price / cache_input / cache_write / cache_storage / tiers / pricing_unit / model_type
	Old      interface{} `json:"old"`       // 原值
	New      interface{} `json:"new"`       // 新值
	Changed  bool        `json:"changed"`   // 是否有实质变动
	ChangePct float64    `json:"change_pct,omitempty"` // 变化百分比（数值字段）
}

// ModelScrapeItem 单个模型的抓取结果
type ModelScrapeItem struct {
	ModelID      uint           `json:"model_id"`
	ModelName    string         `json:"model_name"`
	SupplierID   uint           `json:"supplier_id"`
	SupplierCode string         `json:"supplier_code"`
	SupplierName string         `json:"supplier_name"`
	Status       ItemStatus     `json:"status"`
	OldPrices    *PriceSnapshot `json:"old_prices,omitempty"`
	NewPrices    *ScrapedModel  `json:"new_prices,omitempty"`
	FieldDiffs   []FieldDiff    `json:"field_diffs,omitempty"`
	CacheSource  string         `json:"cache_source,omitempty"` // scraped / derived
	Warnings     []string       `json:"warnings,omitempty"`
	Reason       string         `json:"reason,omitempty"`
}

// SupplierSummary 按供应商汇总信息
type SupplierSummary struct {
	SupplierID   uint   `json:"supplier_id"`
	SupplierCode string `json:"supplier_code"`
	SupplierName string `json:"supplier_name"`
	Total        int    `json:"total"`
	Matched      int    `json:"matched"`
	NotFound     int    `json:"not_found"`
	Error        string `json:"error,omitempty"`
}

// BatchScrapeResult 批量抓取的完整结果
type BatchScrapeResult struct {
	TaskID             string            `json:"task_id"`
	Items              []ModelScrapeItem `json:"items"`
	SupplierSummaries  []SupplierSummary `json:"supplier_summaries"`
	FetchedAt          time.Time         `json:"fetched_at"`
	TotalModels        int               `json:"total_models"`
	SupportedModels    int               `json:"supported_models"`
}

// ProgressEvent 抓取过程中的进度事件
type ProgressEvent struct {
	Kind       string `json:"kind"` // supplier_start | supplier_done | model_matched | model_missing | error | completed
	SupplierID uint   `json:"supplier_id,omitempty"`
	ModelID    uint   `json:"model_id,omitempty"`
	Message    string `json:"message,omitempty"`
	Percent    int    `json:"percent,omitempty"`
}

// AppliedItem 应用阶段的单行请求
type AppliedItem struct {
	ModelID     uint     `json:"model_id"`
	ApplyFields []string `json:"apply_fields"` // 支持：input_price / output_price / cache_input / cache_explicit / cache_write / cache_storage / tiers / pricing_unit / model_type
}

// ApplyFailure 单行失败记录
type ApplyFailure struct {
	ModelID uint   `json:"model_id"`
	Reason  string `json:"reason"`
}

// ApplyReport 应用结果
type ApplyReport struct {
	Applied int            `json:"applied"`
	Failed  []ApplyFailure `json:"failed,omitempty"`
}

// ScrapeSpecificModels 按 model_ids 精准抓取价格
// progress 回调可为 nil
func (s *PriceScraperService) ScrapeSpecificModels(ctx context.Context, modelIDs []uint, progress func(ProgressEvent)) (*BatchScrapeResult, error) {
	log := logger.L
	if log == nil {
		log = zap.NewNop()
	}
	if len(modelIDs) == 0 {
		return nil, fmt.Errorf("model_ids 不能为空")
	}
	if len(modelIDs) > 50 {
		return nil, fmt.Errorf("单次最多 50 个模型，当前 %d", len(modelIDs))
	}

	emit := func(e ProgressEvent) {
		if progress != nil {
			progress(e)
		}
	}

	// 1. 加载模型 + 所属供应商
	var models []model.AIModel
	if err := s.db.Where("id IN ?", modelIDs).Find(&models).Error; err != nil {
		return nil, fmt.Errorf("加载模型失败: %w", err)
	}
	if len(models) == 0 {
		return nil, fmt.Errorf("未找到任何指定的模型 ID")
	}

	// 2. 按 supplier_id 分组
	supplierIDs := uniqueSupplierIDs(models)
	var suppliers []model.Supplier
	if err := s.db.Where("id IN ?", supplierIDs).Find(&suppliers).Error; err != nil {
		return nil, fmt.Errorf("加载供应商失败: %w", err)
	}
	supplierMap := make(map[uint]model.Supplier, len(suppliers))
	for _, sp := range suppliers {
		supplierMap[sp.ID] = sp
	}

	// 建立 supplier_id → []AIModel
	groups := make(map[uint][]model.AIModel)
	for _, m := range models {
		groups[m.SupplierID] = append(groups[m.SupplierID], m)
	}

	// 稳定排序 supplier id
	orderedSupplierIDs := make([]uint, 0, len(groups))
	for sid := range groups {
		orderedSupplierIDs = append(orderedSupplierIDs, sid)
	}
	sort.Slice(orderedSupplierIDs, func(i, j int) bool { return orderedSupplierIDs[i] < orderedSupplierIDs[j] })

	result := &BatchScrapeResult{
		FetchedAt:         time.Now(),
		TotalModels:       len(models),
		SupplierSummaries: make([]SupplierSummary, 0, len(orderedSupplierIDs)),
		Items:             make([]ModelScrapeItem, 0, len(models)),
	}

	totalGroups := len(orderedSupplierIDs)
	doneGroups := 0

	for _, supplierID := range orderedSupplierIDs {
		supplier, ok := supplierMap[supplierID]
		summary := SupplierSummary{
			SupplierID: supplierID,
			Total:      len(groups[supplierID]),
		}
		if ok {
			summary.SupplierName = supplier.Name
			summary.SupplierCode = supplier.Code
		}

		emit(ProgressEvent{Kind: "supplier_start", SupplierID: supplierID, Message: fmt.Sprintf("开始抓取 %s", supplier.Name)})

		// 匹配爬虫
		scraper, scraperKey := s.matchScraper(supplier.Name, supplier.Code)
		if scraper == nil {
			// 该供应商不支持 → 所有成员标记为 unsupported
			for _, m := range groups[supplierID] {
				result.Items = append(result.Items, ModelScrapeItem{
					ModelID:      m.ID,
					ModelName:    m.ModelName,
					SupplierID:   supplierID,
					SupplierCode: supplier.Code,
					SupplierName: supplier.Name,
					Status:       StatusUnsupportedSupplier,
					Reason:       "暂无该供应商的价格爬虫，请手动维护",
				})
			}
			summary.Error = "unsupported_supplier"
			result.SupplierSummaries = append(result.SupplierSummaries, summary)
			doneGroups++
			emit(ProgressEvent{Kind: "supplier_done", SupplierID: supplierID, Percent: percent(doneGroups, totalGroups)})
			continue
		}

		// 注入 API Key
		if ks, ok := scraper.(KeySetter); ok {
			if key := s.lookupChannelAPIKey(supplierID); key != "" {
				ks.SetAPIKey(key)
			}
		}

		// 爬取该供应商所有模型（共用浏览器缓存，不会重复加载）
		scraped, err := scraper.ScrapePrices(ctx)
		if err != nil {
			log.Warn("供应商爬取失败", zap.String("supplier", supplier.Name), zap.Error(err))
			for _, m := range groups[supplierID] {
				result.Items = append(result.Items, ModelScrapeItem{
					ModelID:      m.ID,
					ModelName:    m.ModelName,
					SupplierID:   supplierID,
					SupplierCode: supplier.Code,
					SupplierName: supplier.Name,
					Status:       StatusError,
					Reason:       fmt.Sprintf("爬取失败: %v", err),
				})
			}
			summary.Error = err.Error()
			result.SupplierSummaries = append(result.SupplierSummaries, summary)
			doneGroups++
			emit(ProgressEvent{Kind: "error", SupplierID: supplierID, Message: err.Error(), Percent: percent(doneGroups, totalGroups)})
			continue
		}

		// 对每个选中的模型，在 scraped.Models 中做 fuzzy 匹配
		for _, m := range groups[supplierID] {
			_ = scraperKey
			match := FuzzyMatchModel(m.ModelName, scraped.Models)
			if match == nil {
				result.Items = append(result.Items, ModelScrapeItem{
					ModelID:      m.ID,
					ModelName:    m.ModelName,
					SupplierID:   supplierID,
					SupplierCode: supplier.Code,
					SupplierName: supplier.Name,
					Status:       StatusNotFound,
					OldPrices:    snapshotPrices(m),
					Reason:       "供应商定价页未找到该模型，请手动核对名称",
				})
				summary.NotFound++
				emit(ProgressEvent{Kind: "model_missing", SupplierID: supplierID, ModelID: m.ID, Message: m.ModelName})
				continue
			}
			item := buildItemFromMatch(m, supplier, match)
			result.Items = append(result.Items, item)
			summary.Matched++
			emit(ProgressEvent{Kind: "model_matched", SupplierID: supplierID, ModelID: m.ID, Message: m.ModelName})
		}

		result.SupplierSummaries = append(result.SupplierSummaries, summary)
		doneGroups++
		emit(ProgressEvent{Kind: "supplier_done", SupplierID: supplierID, Percent: percent(doneGroups, totalGroups)})
	}

	// 计算 supported 统计
	for _, it := range result.Items {
		if it.Status == StatusOK || it.Status == StatusUnchanged {
			result.SupportedModels++
		}
	}

	emit(ProgressEvent{Kind: "completed", Percent: 100, Message: fmt.Sprintf("共 %d 个模型，匹配 %d", result.TotalModels, result.SupportedModels)})
	return result, nil
}

// buildItemFromMatch 从爬取结果构造 ModelScrapeItem 并生成字段级差异
func buildItemFromMatch(m model.AIModel, supplier model.Supplier, match *ScrapedModel) ModelScrapeItem {
	old := snapshotPrices(m)
	nm := *match // 拷贝
	item := ModelScrapeItem{
		ModelID:      m.ID,
		ModelName:    m.ModelName,
		SupplierID:   supplier.ID,
		SupplierCode: supplier.Code,
		SupplierName: supplier.Name,
		Status:       StatusOK,
		OldPrices:    old,
		NewPrices:    &nm,
		CacheSource:  match.CacheSource,
		Warnings:     match.Warnings,
	}

	diffs := make([]FieldDiff, 0, 8)
	// input / output
	diffs = append(diffs, numericDiff("input_price", old.InputCostRMB, match.InputPrice))
	diffs = append(diffs, numericDiff("output_price", old.OutputCostRMB, match.OutputPrice))
	// cache
	if match.SupportsCache || old.SupportsCache {
		diffs = append(diffs, numericDiff("cache_input", old.CacheInputPriceRMB, match.CacheInputPrice))
		if match.CacheExplicitInputPrice > 0 || old.CacheExplicitInputPriceRMB > 0 {
			diffs = append(diffs, numericDiff("cache_explicit", old.CacheExplicitInputPriceRMB, match.CacheExplicitInputPrice))
		}
		if match.CacheWritePrice > 0 || old.CacheWritePriceRMB > 0 {
			diffs = append(diffs, numericDiff("cache_write", old.CacheWritePriceRMB, match.CacheWritePrice))
		}
		if match.CacheStoragePrice > 0 || old.CacheStoragePriceRMB > 0 {
			diffs = append(diffs, numericDiff("cache_storage", old.CacheStoragePriceRMB, match.CacheStoragePrice))
		}
	}
	// pricing unit / model type
	if match.PricingUnit != "" && match.PricingUnit != old.PricingUnit {
		diffs = append(diffs, FieldDiff{Field: "pricing_unit", Old: old.PricingUnit, New: match.PricingUnit, Changed: true})
	}
	if match.ModelType != "" && match.ModelType != old.ModelType {
		diffs = append(diffs, FieldDiff{Field: "model_type", Old: old.ModelType, New: match.ModelType, Changed: true})
	}
	// tiers
	if tiersChanged(old.PriceTiers, match.PriceTiers) {
		diffs = append(diffs, FieldDiff{Field: "tiers", Old: old.PriceTiers, New: match.PriceTiers, Changed: true})
	}

	item.FieldDiffs = diffs

	// 若所有 diff.Changed=false，则状态置为 unchanged
	anyChanged := false
	for _, d := range diffs {
		if d.Changed {
			anyChanged = true
			break
		}
	}
	if !anyChanged {
		item.Status = StatusUnchanged
	}
	return item
}

// numericDiff 构造数值字段的差异
func numericDiff(field string, oldV, newV float64) FieldDiff {
	fd := FieldDiff{Field: field, Old: oldV, New: newV}
	if math.Abs(oldV-newV) > 0.000001 {
		fd.Changed = true
		if oldV > 0 {
			fd.ChangePct = (newV - oldV) / oldV
		}
	}
	return fd
}

// tiersChanged 判断阶梯是否变化（长度 / 任一阶梯字段差异）
func tiersChanged(oldT, newT []model.PriceTier) bool {
	if len(oldT) != len(newT) {
		return true
	}
	for i := range oldT {
		if math.Abs(oldT[i].InputPrice-newT[i].InputPrice) > 0.000001 ||
			math.Abs(oldT[i].OutputPrice-newT[i].OutputPrice) > 0.000001 ||
			oldT[i].Name != newT[i].Name {
			return true
		}
	}
	return false
}

// snapshotPrices 从 AIModel 提取 PriceSnapshot
func snapshotPrices(m model.AIModel) *PriceSnapshot {
	snap := &PriceSnapshot{
		InputCostRMB:               m.InputCostRMB,
		OutputCostRMB:              m.OutputCostRMB,
		PricingUnit:                m.PricingUnit,
		ModelType:                  m.ModelType,
		SupportsCache:              m.SupportsCache,
		CacheMechanism:             m.CacheMechanism,
		CacheMinTokens:             m.CacheMinTokens,
		CacheInputPriceRMB:         m.CacheInputPriceRMB,
		CacheExplicitInputPriceRMB: m.CacheExplicitInputPriceRMB,
		CacheWritePriceRMB:         m.CacheWritePriceRMB,
		CacheStoragePriceRMB:       m.CacheStoragePriceRMB,
	}
	if len(m.PriceTiers) > 0 {
		var td model.PriceTiersData
		if err := json.Unmarshal(m.PriceTiers, &td); err == nil {
			snap.PriceTiers = td.Tiers
		}
	}
	return snap
}

func uniqueSupplierIDs(models []model.AIModel) []uint {
	seen := make(map[uint]struct{}, len(models))
	out := make([]uint, 0, len(models))
	for _, m := range models {
		if _, ok := seen[m.SupplierID]; !ok {
			seen[m.SupplierID] = struct{}{}
			out = append(out, m.SupplierID)
		}
	}
	return out
}

func percent(done, total int) int {
	if total <= 0 {
		return 0
	}
	p := done * 100 / total
	if p > 100 {
		p = 100
	}
	return p
}

// ApplySpecificItems 按用户选择的字段集写库（仅更新勾选字段）
// 支持的 field：input_price / output_price / cache_input / cache_explicit / cache_write / cache_storage / tiers / pricing_unit / model_type
// items 的 NewPrices 从已缓存的 BatchScrapeResult 中恢复（handler 负责组装）
func (s *PriceScraperService) ApplySpecificItems(ctx context.Context, result *BatchScrapeResult, selections []AppliedItem) (*ApplyReport, error) {
	log := logger.L
	if log == nil {
		log = zap.NewNop()
	}
	if result == nil {
		return nil, fmt.Errorf("result 不能为空")
	}
	if len(selections) == 0 {
		return &ApplyReport{}, nil
	}

	// 建立 model_id → item 索引
	itemMap := make(map[uint]*ModelScrapeItem, len(result.Items))
	for i := range result.Items {
		itemMap[result.Items[i].ModelID] = &result.Items[i]
	}

	report := &ApplyReport{}
	var applied int
	err := s.db.Transaction(func(tx *gorm.DB) error {
		for _, sel := range selections {
			it, ok := itemMap[sel.ModelID]
			if !ok || it.NewPrices == nil {
				report.Failed = append(report.Failed, ApplyFailure{ModelID: sel.ModelID, Reason: "未在抓取结果中找到"})
				continue
			}
			if it.Status == StatusUnsupportedSupplier || it.Status == StatusNotFound || it.Status == StatusError {
				report.Failed = append(report.Failed, ApplyFailure{ModelID: sel.ModelID, Reason: fmt.Sprintf("状态 %s 不可应用", it.Status)})
				continue
			}

			updates := map[string]interface{}{
				"last_synced_at": time.Now(),
			}
			fields := normalizeFieldSet(sel.ApplyFields)
			nm := it.NewPrices

			if fields["input_price"] && nm.InputPrice > 0 {
				updates["input_cost_rmb"] = nm.InputPrice
			}
			if fields["output_price"] && nm.OutputPrice > 0 {
				updates["output_cost_rmb"] = nm.OutputPrice
			}
			if fields["pricing_unit"] && nm.PricingUnit != "" {
				updates["pricing_unit"] = nm.PricingUnit
			}
			if fields["model_type"] && nm.ModelType != "" {
				updates["model_type"] = nm.ModelType
			}
			if fields["cache_input"] && nm.SupportsCache {
				updates["supports_cache"] = true
				if nm.CacheMechanism != "" {
					updates["cache_mechanism"] = nm.CacheMechanism
				}
				if nm.CacheMinTokens > 0 {
					updates["cache_min_tokens"] = nm.CacheMinTokens
				}
				if nm.CacheInputPrice > 0 {
					updates["cache_input_price_rmb"] = nm.CacheInputPrice
				}
			}
			if fields["cache_explicit"] && nm.CacheExplicitInputPrice > 0 {
				updates["cache_explicit_input_price_rmb"] = nm.CacheExplicitInputPrice
			}
			if fields["cache_write"] && nm.CacheWritePrice > 0 {
				updates["cache_write_price_rmb"] = nm.CacheWritePrice
			}
			if fields["cache_storage"] && nm.CacheStoragePrice > 0 {
				updates["cache_storage_price_rmb"] = nm.CacheStoragePrice
			}
			if fields["tiers"] && len(nm.PriceTiers) > 0 {
				tiers := make([]model.PriceTier, len(nm.PriceTiers))
				copy(tiers, nm.PriceTiers)
				for i := range tiers {
					tiers[i].Normalize()
				}
				model.SortTiers(tiers)
				td := model.PriceTiersData{Tiers: tiers, Currency: "CNY", UpdatedAt: time.Now()}
				if b, err := json.Marshal(td); err == nil {
					updates["price_tiers"] = b
				}
			}

			// 没有可写字段
			if len(updates) <= 1 {
				report.Failed = append(report.Failed, ApplyFailure{ModelID: sel.ModelID, Reason: "未勾选有效字段或抓取值为空"})
				continue
			}

			if err := tx.Model(&model.AIModel{}).Where("id = ?", sel.ModelID).Updates(updates).Error; err != nil {
				report.Failed = append(report.Failed, ApplyFailure{ModelID: sel.ModelID, Reason: err.Error()})
				continue
			}
			applied++
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	report.Applied = applied

	if applied > 0 {
		middleware.CacheInvalidate("cache:/api/v1/public/models*")
		log.Info("批量抓取价格已应用", zap.Int("applied", applied), zap.Int("failed", len(report.Failed)))
	}
	return report, nil
}

// normalizeFieldSet 把 slice 转 set，同时做小写 + 去空
func normalizeFieldSet(fields []string) map[string]bool {
	set := make(map[string]bool, len(fields))
	for _, f := range fields {
		f = strings.ToLower(strings.TrimSpace(f))
		if f == "" {
			continue
		}
		set[f] = true
	}
	return set
}
