// Package pricing 中的 PriceMatrix 服务(v3 引入)。
//
// 提供:
//   - GetMatrix    : 读取模型当前 PriceMatrix(无则按默认模板生成)
//   - UpdateMatrix : 管理员保存矩阵,同步刷新公开模型缓存
//   - MatchCell    : 按维度组合查找命中的 cell(给 BillingService.SettleUsage 用)
//
// 数据一致性原则:
//   - PriceMatrix 是 v3 计价主链路,旧 PriceTiers 仅作 fallback 与显示兼容
//   - 编辑器保存全量 cells,后端不做 cell-level 的差量更新(避免幂等冲突)
//   - 命中时严格按 dim_values 完全匹配,不做"任意值"通配
package pricing

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"gorm.io/gorm"

	"tokenhub-server/internal/model"
)

// ErrPriceMatrixModelNotFound 模型不存在。
var ErrPriceMatrixModelNotFound = errors.New("price matrix: ai model not found")

// PriceMatrixService 矩阵服务。
type PriceMatrixService struct {
	db *gorm.DB
}

// NewPriceMatrixService 构造服务。
func NewPriceMatrixService(db *gorm.DB) *PriceMatrixService {
	if db == nil {
		panic("price matrix service: db is nil")
	}
	return &PriceMatrixService{db: db}
}

// GetMatrix 读取模型当前的 PriceMatrix。
//
// 优先级:
//  1. ModelPricing.PriceMatrix(已存) → isDefault=false
//  2. 按 BuildDefaultMatrix 生成默认模板(基于 AIModel + ModelPricing 已有数据预填) → isDefault=true
//
// 始终返回非 nil 的 *PriceMatrix,允许前端直接渲染。
// isDefault 用于前端展示「尚未保存(显示默认模板)」徽标,与 cell 价格内容解耦
// (即使默认模板已预填价格,只要未真正保存到 ModelPricing.PriceMatrix,isDefault 仍为 true)。
func (s *PriceMatrixService) GetMatrix(ctx context.Context, modelID uint) (*model.PriceMatrix, *model.AIModel, bool, error) {
	var aiModel model.AIModel
	if err := s.db.WithContext(ctx).Where("id = ?", modelID).First(&aiModel).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil, false, ErrPriceMatrixModelNotFound
		}
		return nil, nil, false, fmt.Errorf("load ai model: %w", err)
	}

	var mp model.ModelPricing
	mpFound := true
	if err := s.db.WithContext(ctx).Where("model_id = ?", modelID).
		Order("effective_from DESC, id DESC").
		First(&mp).Error; err != nil {
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, &aiModel, false, fmt.Errorf("load model pricing: %w", err)
		}
		// ErrRecordNotFound:mp 是零值,允许继续(BuildDefaultMatrix 会跳过空字段)
		mpFound = false
	}

	// 已存在 PriceMatrix
	if mpFound && len(mp.PriceMatrix) > 0 && string(mp.PriceMatrix) != "null" {
		var pm model.PriceMatrix
		if err := json.Unmarshal(mp.PriceMatrix, &pm); err == nil && pm.SchemaVersion > 0 {
			// 同步元数据
			if mp.GlobalDiscountRate > 0 {
				pm.GlobalDiscountRate = mp.GlobalDiscountRate
			}
			if mp.PricedAtAt != nil {
				pm.PricedAtAt = mp.PricedAtAt
			}
			if mp.PricedAtExchangeRate > 0 {
				pm.PricedAtExchangeRate = mp.PricedAtExchangeRate
			}
			if mp.PricedAtRateSource != "" {
				pm.PricedAtRateSource = mp.PricedAtRateSource
			}
			return &pm, &aiModel, false, nil
		}
	}

	// 默认模板兜底:把已有的 mp 传入用于预填 Selling*(若 mpFound=false 则传 nil)
	var mpForDefault *model.ModelPricing
	if mpFound {
		mpForDefault = &mp
	}
	defaultPM := BuildDefaultMatrix(&aiModel, mpForDefault)
	return defaultPM, &aiModel, true, nil
}

// UpdateMatrix 保存矩阵(管理员编辑后调用)。
//
// 行为:
//   - 整体覆盖 ModelPricing.PriceMatrix JSON
//   - 同步 selling_input/output 等顶层字段(取第一个 supported 单 cell 作主售价,
//     向后兼容旧的 PricingForm 展示与扣费 fallback 路径)
//   - 不创建多个 ModelPricing 历史记录,只更新最新一条(若无则创建)
func (s *PriceMatrixService) UpdateMatrix(ctx context.Context, modelID uint, pm *model.PriceMatrix) error {
	if pm == nil {
		return errors.New("price matrix is nil")
	}
	if pm.SchemaVersion == 0 {
		pm.SchemaVersion = 1
	}

	var mp model.ModelPricing
	err := s.db.WithContext(ctx).Where("model_id = ?", modelID).
		Order("effective_from DESC, id DESC").
		First(&mp).Error
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return fmt.Errorf("load model pricing: %w", err)
	}
	if errors.Is(err, gorm.ErrRecordNotFound) {
		mp = model.ModelPricing{ModelID: modelID, Currency: "CREDIT"}
	}

	// 同步主售价到顶层字段(向后兼容旧路径)
	syncPrimaryPriceFromMatrix(pm, &mp)

	raw, err := json.Marshal(pm)
	if err != nil {
		return fmt.Errorf("marshal matrix: %w", err)
	}
	mp.PriceMatrix = raw

	if mp.ID == 0 {
		if err := s.db.WithContext(ctx).Create(&mp).Error; err != nil {
			return fmt.Errorf("create model pricing: %w", err)
		}
	} else {
		if err := s.db.WithContext(ctx).Save(&mp).Error; err != nil {
			return fmt.Errorf("save model pricing: %w", err)
		}
	}
	return nil
}

// syncPrimaryPriceFromMatrix 从矩阵中找第一个 supported 单 cell,
// 同步到 ModelPricing 顶层 input_price_rmb/output_price_rmb 字段,
// 向后兼容尚未接入 PriceMatrix 的代码路径(旧的 PricingForm 展示、统计等)。
func syncPrimaryPriceFromMatrix(pm *model.PriceMatrix, mp *model.ModelPricing) {
	if pm == nil || mp == nil {
		return
	}
	for _, c := range pm.Cells {
		if !c.Supported {
			continue
		}
		if c.SellingInput != nil {
			mp.InputPriceRMB = *c.SellingInput
			mp.InputPricePerToken = int64(*c.SellingInput * 10000)
		} else if c.SellingPerUnit != nil {
			mp.InputPriceRMB = *c.SellingPerUnit
			mp.InputPricePerToken = int64(*c.SellingPerUnit * 10000)
		}
		if c.SellingOutput != nil {
			mp.OutputPriceRMB = *c.SellingOutput
			mp.OutputPricePerToken = int64(*c.SellingOutput * 10000)
		} else if c.SellingPerUnit != nil && mp.OutputPriceRMB == 0 {
			// 单价模型:input/output 共用 per_unit
			mp.OutputPriceRMB = *c.SellingPerUnit
			mp.OutputPricePerToken = int64(*c.SellingPerUnit * 10000)
		}
		break
	}
}

// MatchCellByModelID 是 BillingQuote 三方一致性的统一入口:
// 给定 (modelID, dimValues),从 ModelPricing 表加载最新一条矩阵记录,
// 在矩阵中查找命中 cell。
//
// 该函数是 PreviewQuote / SettleUsage / SettleUnitUsage / GetCostBreakdown(回退分支)
// 共用的 PriceMatrix 命中入口,确保三方在同一份矩阵数据上做匹配,避免漂移。
//
// 返回 (cell, true) 表示命中;否则 (nil, false)。任何 DB 错误 / 矩阵不存在 / 未命中
// 都按未命中处理,调用方应 fallback 到顶层售价或 PriceTiers。
func MatchCellByModelID(ctx context.Context, db *gorm.DB, modelID uint, dimValues map[string]interface{}) (*model.PriceMatrixCell, bool) {
	if len(dimValues) == 0 || db == nil || modelID == 0 {
		return nil, false
	}
	var mp model.ModelPricing
	if err := db.WithContext(ctx).Where("model_id = ?", modelID).
		Order("effective_from DESC, id DESC").
		First(&mp).Error; err != nil {
		return nil, false
	}
	if len(mp.PriceMatrix) == 0 || string(mp.PriceMatrix) == "null" {
		return nil, false
	}
	var pm model.PriceMatrix
	if err := json.Unmarshal(mp.PriceMatrix, &pm); err != nil || pm.SchemaVersion == 0 {
		return nil, false
	}
	cell := MatchCell(&pm, dimValues)
	if cell == nil {
		return nil, false
	}
	return cell, true
}

// MatchCell 按 dim_values 在矩阵中查找完全匹配的 cell。
//
// 命中规则:
//   - dim_values 中所有 key 在 cell.DimValues 中都存在且值相等
//   - cell.Supported = true(unsupported cell 不参与计费)
//   - 多 cell 匹配时取第一条(矩阵语义上不应出现多匹配)
//
// 未命中时返回 nil,调用方应 fallback 到旧 PriceTiers 或顶层售价字段。
func MatchCell(pm *model.PriceMatrix, dimValues map[string]interface{}) *model.PriceMatrixCell {
	if pm == nil || len(pm.Cells) == 0 {
		return nil
	}
	for i := range pm.Cells {
		cell := &pm.Cells[i]
		if !cell.Supported {
			continue
		}
		if matchDimValues(cell.DimValues, dimValues) {
			return cell
		}
	}
	return nil
}

// matchDimValues 判断 cell 是否匹配请求 dim_values。
//
// 规则:cell.DimValues 是请求的"约束子集",
//   - cell 中每个 key 必须在请求中存在且值相等
//   - 请求中多余的 key 不影响匹配(允许 cell 不约束所有维度,例如 Seedance 用 cell
//     `{inference_mode: "offline", supported: false}` 表达"任意分辨率 + 任意 input_has_video
//     在 offline 下都不支持")
//   - 类型容错:fmt.Sprint 比较,适配 JSON 反序列化后的 float64 / 字符串 / 布尔
func matchDimValues(cellDim, reqDim map[string]interface{}) bool {
	for k, want := range cellDim {
		got, ok := reqDim[k]
		if !ok {
			return false
		}
		if !valuesEqual(got, want) {
			return false
		}
	}
	return true
}

func valuesEqual(a, b interface{}) bool {
	if a == b {
		return true
	}
	// 容错:JSON 反序列化后的数字都是 float64,字符串保持原样,布尔保持原样
	return fmt.Sprintf("%v", a) == fmt.Sprintf("%v", b)
}

// RefreshPriceMatrixForModel 重建指定模型的 PriceMatrix（M4, 2026-04-28）
//
// 用途：scraper.ApplyPrices 在更新 ai_models.price_tiers 后调用，
// 让 ModelPricing.PriceMatrix 与最新的 PriceTiers / DimValues 保持同步。
//
// 行为：
//   - 直接复用 BuildDefaultMatrix（与 RunPriceMatrixMigration 同源逻辑）
//   - 强制覆盖已有 PriceMatrix（不跳过已存在，因为价格刚更新需要立即同步）
//   - 失败不返回 error，仅静默返回（不阻塞 scraper 主流程）
//
// 调用链：scraper.ApplyPrices → tx 内本函数 → BuildDefaultMatrix → mp.PriceMatrix
// 副作用：让 selectPriceForTokens 步骤 0（PriceMatrix 优先命中）能立即用上最新数据。
func RefreshPriceMatrixForModel(db *gorm.DB, aiModel *model.AIModel) {
	if db == nil || aiModel == nil || aiModel.ID == 0 {
		return
	}
	var mp model.ModelPricing
	if err := db.Where("model_id = ?", aiModel.ID).
		Order("effective_from DESC, id DESC").
		First(&mp).Error; err != nil {
		// 该模型未配置 ModelPricing → 无需刷新（计费会走 fallback）
		return
	}

	matrix := BuildDefaultMatrix(aiModel, &mp)
	if matrix == nil {
		return
	}
	raw, err := json.Marshal(matrix)
	if err != nil {
		return
	}
	_ = db.Model(&model.ModelPricing{}).
		Where("id = ?", mp.ID).
		Update("price_matrix", raw).Error
}
