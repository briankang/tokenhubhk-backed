package parammapping

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"sync"

	"tokenhub-server/internal/model"
	"tokenhub-server/internal/pkg/logger"

	"go.uber.org/zap"
	"gorm.io/gorm"
)

// ParamMappingService 平台参数映射服务
// 负责管理平台标准参数定义和供应商映射，以及运行时参数转换
type ParamMappingService struct {
	db    *gorm.DB
	mu    sync.RWMutex
	cache map[string][]mappingEntry // supplierCode → 映射列表缓存
}

// mappingEntry 运行时缓存的映射条目
type mappingEntry struct {
	PlatformParam   string // 平台参数名
	VendorParam     string // 供应商参数名
	TransformType   string
	TransformRule   string
	Supported       bool
}

type ParamCoverageItem struct {
	ParamID     uint   `json:"param_id"`
	ParamName   string `json:"param_name"`
	DisplayName string `json:"display_name"`
	Category    string `json:"category"`
	ParamType   string `json:"param_type"`
}

type SupplierParamCoverage struct {
	SupplierID          uint                `json:"supplier_id"`
	SupplierCode        string              `json:"supplier_code"`
	SupplierName        string              `json:"supplier_name"`
	ActiveModelCount    int64               `json:"active_model_count"`
	TotalParams         int                 `json:"total_params"`
	SupportedMappings   int                 `json:"supported_mappings"`
	UnsupportedMappings int                 `json:"unsupported_mappings"`
	MissingMappings     int                 `json:"missing_mappings"`
	CoveragePercent     float64             `json:"coverage_percent"`
	MissingParams       []ParamCoverageItem `json:"missing_params"`
	UnsupportedParams   []ParamCoverageItem `json:"unsupported_params"`
}

type ParamMappingCoverageReport struct {
	TotalParams int                     `json:"total_params"`
	Suppliers   []SupplierParamCoverage `json:"suppliers"`
}

// NewParamMappingService 创建参数映射服务
func NewParamMappingService(db *gorm.DB) *ParamMappingService {
	return &ParamMappingService{db: db, cache: make(map[string][]mappingEntry)}
}

// ─── CRUD 操作 ───

// ListParams 获取所有平台参数（含映射）
func (s *ParamMappingService) ListParams(ctx context.Context) ([]model.PlatformParam, error) {
	var params []model.PlatformParam
	err := s.db.WithContext(ctx).Preload("Mappings").Order("sort_order ASC, id ASC").Find(&params).Error
	return params, err
}

// GetParam 获取单个平台参数（含映射）
func (s *ParamMappingService) GetParam(ctx context.Context, id uint) (*model.PlatformParam, error) {
	var param model.PlatformParam
	err := s.db.WithContext(ctx).Preload("Mappings").First(&param, id).Error
	if err != nil {
		return nil, err
	}
	return &param, nil
}

// CreateParam 创建平台参数
func (s *ParamMappingService) CreateParam(ctx context.Context, param *model.PlatformParam) error {
	if err := s.db.WithContext(ctx).Create(param).Error; err != nil {
		return err
	}
	s.invalidateCache()
	return nil
}

// UpdateParam 更新平台参数
func (s *ParamMappingService) UpdateParam(ctx context.Context, id uint, updates map[string]interface{}) error {
	if err := s.db.WithContext(ctx).Model(&model.PlatformParam{}).Where("id = ?", id).Updates(updates).Error; err != nil {
		return err
	}
	s.invalidateCache()
	return nil
}

// DeleteParam 删除平台参数及其所有映射
func (s *ParamMappingService) DeleteParam(ctx context.Context, id uint) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Unscoped().Where("platform_param_id = ?", id).Delete(&model.SupplierParamMapping{}).Error; err != nil {
			return err
		}
		if err := tx.Unscoped().Delete(&model.PlatformParam{}, id).Error; err != nil {
			return err
		}
		s.invalidateCache()
		return nil
	})
}

// UpsertMapping 创建或更新供应商映射
func (s *ParamMappingService) UpsertMapping(ctx context.Context, mapping *model.SupplierParamMapping) error {
	var existing model.SupplierParamMapping
	err := s.db.WithContext(ctx).
		Where("platform_param_id = ? AND supplier_code = ?", mapping.PlatformParamID, mapping.SupplierCode).
		First(&existing).Error

	if err == gorm.ErrRecordNotFound {
		if err := s.db.WithContext(ctx).Create(mapping).Error; err != nil {
			return err
		}
	} else if err == nil {
		if err := s.db.WithContext(ctx).Model(&model.SupplierParamMapping{}).Where("id = ?", existing.ID).Updates(map[string]interface{}{
			"vendor_param_name": mapping.VendorParamName,
			"transform_type":   mapping.TransformType,
			"transform_rule":   mapping.TransformRule,
			"supported":        mapping.Supported,
			"notes":            mapping.Notes,
		}).Error; err != nil {
			return err
		}
	} else {
		return err
	}

	s.invalidateCache()
	return nil
}

// DeleteMapping 删除供应商映射（硬删除，避免唯一索引冲突）
func (s *ParamMappingService) DeleteMapping(ctx context.Context, id uint) error {
	if err := s.db.WithContext(ctx).Unscoped().Delete(&model.SupplierParamMapping{}, id).Error; err != nil {
		return err
	}
	s.invalidateCache()
	return nil
}

// BatchUpdateMappings 批量更新某供应商的映射
func (s *ParamMappingService) BatchUpdateMappings(ctx context.Context, supplierCode string, mappings []model.SupplierParamMapping) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// 硬删除该供应商的旧映射（避免软删除后唯一索引冲突）
		if err := tx.Unscoped().Where("supplier_code = ?", supplierCode).Delete(&model.SupplierParamMapping{}).Error; err != nil {
			return err
		}
		// 批量创建
		if len(mappings) > 0 {
			for i := range mappings {
				mappings[i].SupplierCode = supplierCode
				mappings[i].ID = 0
			}
			if err := tx.CreateInBatches(mappings, 50).Error; err != nil {
				return err
			}
		}
		s.invalidateCache()
		return nil
	})
}

// GetSupplierParamSupport 返回指定供应商的参数支持情况 map[平台参数名]是否支持
func (s *ParamMappingService) GetSupplierParamSupport(ctx context.Context, supplierCode string) (map[string]bool, error) {
	entries := s.loadMappings(supplierCode)
	result := make(map[string]bool, len(entries))
	for _, e := range entries {
		result[e.PlatformParam] = e.Supported
	}
	return result, nil
}

// GetMappingsBySupplier 获取某供应商的所有映射
func (s *ParamMappingService) GetMappingsBySupplier(ctx context.Context, supplierCode string) ([]model.SupplierParamMapping, error) {
	var mappings []model.SupplierParamMapping
	err := s.db.WithContext(ctx).Where("supplier_code = ?", supplierCode).Find(&mappings).Error
	return mappings, err
}

func (s *ParamMappingService) GetCoverageReport(ctx context.Context) (*ParamMappingCoverageReport, error) {
	var params []model.PlatformParam
	if err := s.db.WithContext(ctx).
		Where("is_active = ?", true).
		Order("category ASC, sort_order ASC, id ASC").
		Find(&params).Error; err != nil {
		return nil, err
	}

	var suppliers []model.Supplier
	if err := s.db.WithContext(ctx).
		Where("is_active = ?", true).
		Order("sort_order ASC, id ASC").
		Find(&suppliers).Error; err != nil {
		return nil, err
	}

	report := &ParamMappingCoverageReport{
		TotalParams: len(params),
		Suppliers:   make([]SupplierParamCoverage, 0, len(suppliers)),
	}
	if len(params) == 0 || len(suppliers) == 0 {
		return report, nil
	}

	paramByID := make(map[uint]model.PlatformParam, len(params))
	for _, p := range params {
		paramByID[p.ID] = p
	}

	for _, supplier := range suppliers {
		var mappings []model.SupplierParamMapping
		if err := s.db.WithContext(ctx).
			Where("supplier_code = ?", supplier.Code).
			Find(&mappings).Error; err != nil {
			return nil, err
		}

		mapped := make(map[uint]model.SupplierParamMapping, len(mappings))
		for _, m := range mappings {
			if _, ok := paramByID[m.PlatformParamID]; ok {
				mapped[m.PlatformParamID] = m
			}
		}

		item := SupplierParamCoverage{
			SupplierID:       supplier.ID,
			SupplierCode:     supplier.Code,
			SupplierName:     supplier.Name,
			TotalParams:      len(params),
			MissingParams:    make([]ParamCoverageItem, 0),
			UnsupportedParams: make([]ParamCoverageItem, 0),
		}
		if err := s.db.WithContext(ctx).Model(&model.AIModel{}).
			Where("supplier_id = ? AND status = ? AND is_active = ?", supplier.ID, "online", true).
			Count(&item.ActiveModelCount).Error; err != nil {
			return nil, err
		}

		for _, p := range params {
			m, ok := mapped[p.ID]
			if !ok {
				item.MissingMappings++
				item.MissingParams = append(item.MissingParams, toCoverageItem(p))
				continue
			}
			if m.Supported {
				item.SupportedMappings++
			} else {
				item.UnsupportedMappings++
				item.UnsupportedParams = append(item.UnsupportedParams, toCoverageItem(p))
			}
		}
		item.CoveragePercent = coveragePercent(item.SupportedMappings, item.TotalParams)
		report.Suppliers = append(report.Suppliers, item)
	}

	sort.SliceStable(report.Suppliers, func(i, j int) bool {
		if report.Suppliers[i].MissingMappings != report.Suppliers[j].MissingMappings {
			return report.Suppliers[i].MissingMappings > report.Suppliers[j].MissingMappings
		}
		return report.Suppliers[i].SupplierCode < report.Suppliers[j].SupplierCode
	})
	return report, nil
}

// ─── 运行时参数转换 ───

// TransformParams 将平台标准参数转换为供应商特定参数
// platformParams: 用户设置的平台标准参数（如 {"enable_thinking": true, "thinking_budget": 5000}）
// supplierCode: 目标供应商代码
// 返回: 转换后的供应商参数 map
func (s *ParamMappingService) TransformParams(supplierCode string, platformParams map[string]interface{}) map[string]interface{} {
	return s.TransformParamsWithContext(context.Background(), supplierCode, platformParams)
}

// TransformParamsWithContext 带 context 的参数转换（支持 request_id 日志追踪）
func (s *ParamMappingService) TransformParamsWithContext(ctx context.Context, supplierCode string, platformParams map[string]interface{}) map[string]interface{} {
	if len(platformParams) == 0 {
		return nil
	}

	entries := s.loadMappings(supplierCode)
	if len(entries) == 0 {
		// 无映射配置，直接透传
		return platformParams
	}

	result := make(map[string]interface{})
	mapped := make(map[string]bool) // 记录已映射的平台参数

	for _, entry := range entries {
		value, exists := platformParams[entry.PlatformParam]
		if !exists {
			continue
		}
		mapped[entry.PlatformParam] = true

		if !entry.Supported {
			// 该供应商不支持此参数，跳过
			reqID, _ := ctx.Value("request_id").(string)
			logger.L.Debug("param filtered: not supported by supplier",
				zap.String("platform_param", entry.PlatformParam),
				zap.String("supplier", supplierCode),
				zap.Any("value", value),
				zap.String("request_id", reqID),
			)
			continue
		}

		switch entry.TransformType {
		case "direct":
			// 参数名相同，直接透传
			result[entry.VendorParam] = value

		case "rename":
			// 参数名不同，值不变
			result[entry.VendorParam] = value

		case "nested":
			// 需要构造嵌套结构
			s.applyNestedTransform(result, entry, value, platformParams)

		case "mapping":
			// 值映射
			s.applyValueMapping(result, entry, value)

		default:
			result[entry.VendorParam] = value
		}
	}

	// 未映射的参数直接透传（兼容自定义参数）
	for k, v := range platformParams {
		if !mapped[k] {
			result[k] = v
		}
	}

	if len(result) == 0 {
		return nil
	}
	return result
}

// applyNestedTransform 应用嵌套结构转换
func (s *ParamMappingService) applyNestedTransform(result map[string]interface{}, entry mappingEntry, value interface{}, allParams map[string]interface{}) {
	if entry.TransformRule == "" {
		result[entry.VendorParam] = value
		return
	}

	var rule map[string]interface{}
	if err := json.Unmarshal([]byte(entry.TransformRule), &rule); err != nil {
		logger.L.Debug("param mapping: invalid transform rule", zap.String("param", entry.PlatformParam), zap.Error(err))
		result[entry.VendorParam] = value
		return
	}

	// 处理 when_true / when_false 模式（用于 bool 参数）
	if whenTrue, ok := rule["when_true"]; ok {
		boolVal := toBool(value)
		if boolVal {
			result[entry.VendorParam] = whenTrue
		} else if whenFalse, ok := rule["when_false"]; ok && whenFalse != nil {
			result[entry.VendorParam] = whenFalse
		}
		return
	}

	// 处理 path + field 模式（嵌套字段设置）
	if path, ok := rule["path"].(string); ok {
		field, _ := rule["field"].(string)
		if field == "" {
			result[path] = value
			return
		}
		// 获取或创建嵌套对象
		nested, _ := result[path].(map[string]interface{})
		if nested == nil {
			nested = make(map[string]interface{})
		}
		// 值映射
		if valueMap, ok := rule["value_map"].(map[string]interface{}); ok {
			strVal := fmt.Sprintf("%v", value)
			if mapped, ok := valueMap[strVal]; ok {
				nested[field] = mapped
			} else {
				nested[field] = value
			}
		} else {
			nested[field] = value
		}
		result[path] = nested
	}
}

// applyValueMapping 应用值映射转换
func (s *ParamMappingService) applyValueMapping(result map[string]interface{}, entry mappingEntry, value interface{}) {
	if entry.TransformRule == "" {
		result[entry.VendorParam] = value
		return
	}

	var valueMap map[string]interface{}
	if err := json.Unmarshal([]byte(entry.TransformRule), &valueMap); err != nil {
		result[entry.VendorParam] = value
		return
	}

	strVal := fmt.Sprintf("%v", value)
	if mapped, ok := valueMap[strVal]; ok {
		result[entry.VendorParam] = mapped
	} else {
		result[entry.VendorParam] = value
	}
}

// ─── 缓存管理 ───

func (s *ParamMappingService) loadMappings(supplierCode string) []mappingEntry {
	s.mu.RLock()
	if entries, ok := s.cache[supplierCode]; ok {
		s.mu.RUnlock()
		return entries
	}
	s.mu.RUnlock()

	// 缓存未命中，从 DB 加载
	var mappings []model.SupplierParamMapping
	s.db.Preload("").Where("supplier_code = ?", supplierCode).Find(&mappings)

	// 加载关联的平台参数名
	paramIDs := make([]uint, 0, len(mappings))
	for _, m := range mappings {
		paramIDs = append(paramIDs, m.PlatformParamID)
	}
	var params []model.PlatformParam
	if len(paramIDs) > 0 {
		s.db.Where("id IN ? AND is_active = ?", paramIDs, true).Find(&params)
	}
	paramNameMap := make(map[uint]string)
	for _, p := range params {
		paramNameMap[p.ID] = p.ParamName
	}

	entries := make([]mappingEntry, 0, len(mappings))
	for _, m := range mappings {
		name, ok := paramNameMap[m.PlatformParamID]
		if !ok {
			continue // 参数已禁用或删除
		}
		entries = append(entries, mappingEntry{
			PlatformParam: name,
			VendorParam:   m.VendorParamName,
			TransformType: m.TransformType,
			TransformRule: m.TransformRule,
			Supported:     m.Supported,
		})
	}

	s.mu.Lock()
	s.cache[supplierCode] = entries
	s.mu.Unlock()

	return entries
}

func (s *ParamMappingService) invalidateCache() {
	s.mu.Lock()
	s.cache = make(map[string][]mappingEntry)
	s.mu.Unlock()
}

func toCoverageItem(p model.PlatformParam) ParamCoverageItem {
	return ParamCoverageItem{
		ParamID:     p.ID,
		ParamName:   p.ParamName,
		DisplayName: p.DisplayName,
		Category:    p.Category,
		ParamType:   p.ParamType,
	}
}

func coveragePercent(supported, total int) float64 {
	if total <= 0 {
		return 0
	}
	return math.Round(float64(supported)/float64(total)*10000) / 100
}

// toBool 将 interface{} 转为 bool
func toBool(v interface{}) bool {
	switch val := v.(type) {
	case bool:
		return val
	case string:
		return val == "true" || val == "1" || val == "yes"
	case float64:
		return val != 0
	case int:
		return val != 0
	default:
		return false
	}
}
