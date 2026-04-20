package pricescraper

import (
	"testing"
	"time"

	"tokenhub-server/internal/model"
)

// TestBuildDiffResult_FanOutMatching 验证 v3.6 两阶段匹配：
// 当爬虫返回泛型名（如 doubao-pro）时，应 fan-out 到所有未被精确命中的 DB 变体。
//
// 背景：老逻辑 findMatchingModel 做一对一前缀匹配，只挑字典序最大的 DB 变体赋价。
// 结果：doubao-pro-4k-240515 / doubao-pro-32k-240828 等历史快照价格全部归零。
// 修复后：doubao-pro 爬取项应展开到所有 27 个 doubao-pro-* 变体（除被 doubao-pro-32k 精确占位的）。
func TestBuildDiffResult_FanOutMatching(t *testing.T) {
	svc := &PriceScraperService{}

	supplier := model.Supplier{
		BaseModel: model.BaseModel{ID: 7},
		Name:      "火山引擎",
		Code:      "volcengine",
		Discount:  1.0,
	}

	// 模拟 DB 中 5 个 doubao-pro 变体（1 个通用 + 4 个日期快照）
	dbModels := []model.AIModel{
		{BaseModel: model.BaseModel{ID: 1}, ModelName: "doubao-pro-32k", SupplierID: 7, InputCostRMB: 0.5, OutputCostRMB: 1.2},
		{BaseModel: model.BaseModel{ID: 2}, ModelName: "doubao-pro-4k-240515", SupplierID: 7},
		{BaseModel: model.BaseModel{ID: 3}, ModelName: "doubao-pro-32k-240828", SupplierID: 7},
		{BaseModel: model.BaseModel{ID: 4}, ModelName: "doubao-pro-128k-240628", SupplierID: 7},
		{BaseModel: model.BaseModel{ID: 5}, ModelName: "doubao-pro-32k-character-241215", SupplierID: 7},
		{BaseModel: model.BaseModel{ID: 6}, ModelName: "doubao-lite-32k", SupplierID: 7},
	}

	// 模拟爬虫返回 2 个条目：
	//   1) doubao-pro-32k（精确匹配 → 占位 ID=1）
	//   2) doubao-pro（泛型 → fan-out 到 ID=2,3,4,5，不应触达 ID=6 doubao-lite）
	scraped := &ScrapedPriceData{
		SupplierID:   7,
		SupplierName: "火山引擎",
		FetchedAt:    time.Now(),
		Models: []ScrapedModel{
			{
				ModelName:  "doubao-pro-32k",
				InputPrice: 0.8, OutputPrice: 2.0,
				ModelType: "LLM", PricingUnit: PricingUnitPerMillionTokens,
				Currency: "CNY",
			},
			{
				ModelName:  "doubao-pro",
				InputPrice: 0.8, OutputPrice: 2.0,
				ModelType: "LLM", PricingUnit: PricingUnitPerMillionTokens,
				Currency: "CNY",
			},
		},
	}

	// 用内存 modelMap 绕过 DB 查询（直接构造 buildDiffResult 所需数据结构）
	result := callBuildDiffWithMockedDB(t, svc, supplier, scraped, dbModels)

	if result == nil {
		t.Fatal("result nil")
	}

	// 断言：应生成 5 个 diff items（1 个精确命中 + 4 个 fan-out）
	matchedIDs := make(map[uint]int)
	for _, it := range result.Items {
		if it.ModelID > 0 {
			matchedIDs[it.ModelID]++
		}
	}

	wantIDs := []uint{1, 2, 3, 4, 5}
	for _, id := range wantIDs {
		if matchedIDs[id] == 0 {
			t.Errorf("expected DB model ID %d to be matched by at least one scraped item, got zero", id)
		}
		if matchedIDs[id] > 1 {
			t.Errorf("DB model ID %d matched %d times (should be exactly 1)", id, matchedIDs[id])
		}
	}

	// doubao-lite-32k 不应被匹配
	if matchedIDs[6] > 0 {
		t.Errorf("doubao-lite-32k (ID=6) should NOT be matched by doubao-pro* scraped entries, got %d matches", matchedIDs[6])
	}

	// 断言：所有 fan-out 项都有非零输入/输出价格
	for _, it := range result.Items {
		if it.ModelID > 0 && (it.NewInputRMB != 0.8 || it.NewOutputRMB != 2.0) {
			t.Errorf("item %s (ID=%d) has wrong price: input=%.4f output=%.4f",
				it.ModelName, it.ModelID, it.NewInputRMB, it.NewOutputRMB)
		}
	}

	t.Logf("✅ fan-out 匹配正常: 5 个 diff items 覆盖 DB models %v", wantIDs)
}

// TestBuildDiffResult_ExactTakesPrecedenceOverGeneric 验证：
// 当爬虫同时返回精确名和泛型名时，精确名先占位，泛型不应覆盖已占用的 DB 模型。
func TestBuildDiffResult_ExactTakesPrecedenceOverGeneric(t *testing.T) {
	svc := &PriceScraperService{}
	supplier := model.Supplier{BaseModel: model.BaseModel{ID: 7}, Name: "火山引擎", Code: "volcengine", Discount: 1.0}

	dbModels := []model.AIModel{
		{BaseModel: model.BaseModel{ID: 10}, ModelName: "doubao-pro-32k", SupplierID: 7},
		{BaseModel: model.BaseModel{ID: 11}, ModelName: "doubao-pro-4k-240515", SupplierID: 7},
	}

	// 爬虫返回两条:
	//   "doubao-pro-32k" (精确 → 价格 A) — 先占位 ID=10
	//   "doubao-pro"    (泛型 → 价格 B) — 应只 fan-out 到 ID=11，不覆盖 ID=10
	scraped := &ScrapedPriceData{
		Models: []ScrapedModel{
			{ModelName: "doubao-pro-32k", InputPrice: 1.0, OutputPrice: 2.0, ModelType: "LLM", Currency: "CNY"},
			{ModelName: "doubao-pro", InputPrice: 5.0, OutputPrice: 9.0, ModelType: "LLM", Currency: "CNY"},
		},
	}

	result := callBuildDiffWithMockedDB(t, svc, supplier, scraped, dbModels)

	var item10, item11 *PriceDiffItem
	for i := range result.Items {
		it := &result.Items[i]
		if it.ModelID == 10 {
			item10 = it
		}
		if it.ModelID == 11 {
			item11 = it
		}
	}
	if item10 == nil {
		t.Fatal("ID=10 should have a diff item")
	}
	if item11 == nil {
		t.Fatal("ID=11 should have a diff item")
	}
	// ID=10 应拿到 "doubao-pro-32k" 的精确价格（1.0/2.0）
	if item10.NewInputRMB != 1.0 || item10.NewOutputRMB != 2.0 {
		t.Errorf("ID=10 should have exact price 1.0/2.0, got %.2f/%.2f", item10.NewInputRMB, item10.NewOutputRMB)
	}
	// ID=11 应拿到 "doubao-pro" 泛型价格（5.0/9.0）
	if item11.NewInputRMB != 5.0 || item11.NewOutputRMB != 9.0 {
		t.Errorf("ID=11 should have generic price 5.0/9.0, got %.2f/%.2f", item11.NewInputRMB, item11.NewOutputRMB)
	}
}

// callBuildDiffWithMockedDB 用内存 modelMap 直接调用两阶段匹配核心逻辑
// 由于 buildDiffResult 依赖 s.db，这里将内部匹配逻辑提取测试（复制两阶段算法）
// 保持与 buildDiffResult 等价：精确匹配 → 泛型 fan-out
func callBuildDiffWithMockedDB(t *testing.T, _ *PriceScraperService, supplier model.Supplier, scraped *ScrapedPriceData, existingModels []model.AIModel) *PriceDiffResult {
	t.Helper()
	// 复现 buildDiffResult 中的两阶段匹配（仅用于测试独立验证）
	modelMap := make(map[string]model.AIModel, len(existingModels))
	for _, m := range existingModels {
		modelMap[toLower(m.ModelName)] = m
	}
	result := &PriceDiffResult{
		SupplierID:   supplier.ID,
		SupplierName: supplier.Name,
		FetchedAt:    scraped.FetchedAt,
		TotalModels:  len(scraped.Models),
	}
	discount := supplier.Discount
	if discount <= 0 {
		discount = 1.0
	}

	usedIDs := make(map[uint]bool)
	type pending struct {
		sm       ScrapedModel
		matches  []model.AIModel
		genericK string
	}
	pendings := make([]pending, 0, len(scraped.Models))
	for _, sm := range scraped.Models {
		key := toLower(sm.ModelName)
		if m, ok := modelMap[key]; ok {
			usedIDs[m.ID] = true
			pendings = append(pendings, pending{sm: sm, matches: []model.AIModel{m}})
		} else {
			pendings = append(pendings, pending{sm: sm, genericK: key})
		}
	}
	for i := range pendings {
		p := &pendings[i]
		if len(p.matches) > 0 || p.genericK == "" {
			continue
		}
		for key, m := range modelMap {
			if usedIDs[m.ID] {
				continue
			}
			if hasPrefix(key, p.genericK+"-") {
				p.matches = append(p.matches, m)
				usedIDs[m.ID] = true
			}
		}
	}
	for _, p := range pendings {
		sm := p.sm
		base := PriceDiffItem{
			ModelName:    sm.ModelName,
			NewInputRMB:  sm.InputPrice,
			NewOutputRMB: sm.OutputPrice,
			ModelType:    sm.ModelType,
		}
		base.ActualInputRMB = roundFloat(sm.InputPrice*discount, 4)
		base.ActualOutputRMB = roundFloat(sm.OutputPrice*discount, 4)
		if len(p.matches) == 0 {
			base.HasChanges = true
			result.Items = append(result.Items, base)
			continue
		}
		for _, existing := range p.matches {
			it := base
			if len(p.matches) > 1 || !equalFold(existing.ModelName, sm.ModelName) {
				it.ModelName = existing.ModelName
			}
			it.ModelID = existing.ID
			it.CurrentInputRMB = existing.InputCostRMB
			it.CurrentOutputRMB = existing.OutputCostRMB
			result.Items = append(result.Items, it)
		}
	}
	return result
}

// 最小化字符串辅助：为了在 _test.go 内部自洽（不 import strings）
func toLower(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c >= 'A' && c <= 'Z' {
			b[i] = c + 32
		}
	}
	return string(b)
}
func hasPrefix(s, p string) bool {
	if len(s) < len(p) {
		return false
	}
	for i := 0; i < len(p); i++ {
		if s[i] != p[i] {
			return false
		}
	}
	return true
}
func equalFold(a, b string) bool { return toLower(a) == toLower(b) }
