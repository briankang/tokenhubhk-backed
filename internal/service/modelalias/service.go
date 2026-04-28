package modelalias

import (
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"gorm.io/gorm"

	"tokenhub-server/internal/model"
)

var datedSnapshotSuffix = regexp.MustCompile(`^(.+)-(\d{4}-\d{2}-\d{2})$`)

const activeAliasCacheTTL = 30 * time.Second

type activeAliasCacheEntry struct {
	alias     *model.ModelAlias
	notFound  bool
	expiresAt time.Time
}

var activeAliasCache sync.Map

type Service struct {
	db *gorm.DB
}

type Resolution struct {
	RequestedModel string            `json:"requested_model"`
	ResolvedModel  string            `json:"resolved_model"`
	IsAlias        bool              `json:"is_alias"`
	Alias          *model.ModelAlias `json:"alias,omitempty"`
	Chain          []string          `json:"chain,omitempty"`
}

type ListOptions struct {
	Page        int
	PageSize    int
	Search      string
	Model       string
	SupplierID  uint
	ActiveOnly  bool
	PublicOnly  bool
	Source      string
	AliasType   string
	IncludeMeta bool
}

type ListResult struct {
	List     []model.ModelAlias `json:"list"`
	Total    int64              `json:"total"`
	Page     int                `json:"page"`
	PageSize int                `json:"page_size"`
}

type SuggestOptions struct {
	SupplierID uint
	Apply      bool
}

type AliasSuggestion struct {
	AliasName       string  `json:"alias_name"`
	TargetModelName string  `json:"target_model_name"`
	SupplierID      uint    `json:"supplier_id"`
	AliasType       string  `json:"alias_type"`
	Source          string  `json:"source"`
	Confidence      float64 `json:"confidence"`
	Reason          string  `json:"reason"`
	Applied         bool    `json:"applied"`
}

func NewService(db *gorm.DB) *Service {
	return &Service{db: db}
}

func (s *Service) List(opts ListOptions) (ListResult, error) {
	if opts.Page <= 0 {
		opts.Page = 1
	}
	if opts.PageSize <= 0 || opts.PageSize > 500 {
		opts.PageSize = 50
	}
	q := s.db.Model(&model.ModelAlias{})
	if opts.IncludeMeta {
		q = q.Preload("Supplier")
	}
	if opts.Search != "" {
		like := "%" + strings.TrimSpace(opts.Search) + "%"
		q = q.Where("alias_name LIKE ? OR target_model_name LIKE ? OR notes LIKE ?", like, like, like)
	}
	if opts.Model != "" {
		q = q.Where("alias_name = ? OR target_model_name = ?", opts.Model, opts.Model)
	}
	if opts.SupplierID > 0 {
		q = q.Where("supplier_id = ? OR supplier_id = 0", opts.SupplierID)
	}
	if opts.ActiveOnly {
		q = q.Where("is_active = ?", true)
	}
	if opts.PublicOnly {
		q = q.Where("is_public = ?", true)
	}
	if opts.Source != "" {
		q = q.Where("source = ?", opts.Source)
	}
	if opts.AliasType != "" {
		q = q.Where("alias_type = ?", opts.AliasType)
	}
	var total int64
	if err := q.Count(&total).Error; err != nil {
		return ListResult{}, err
	}
	var list []model.ModelAlias
	err := q.Order("updated_at DESC, id DESC").
		Offset((opts.Page - 1) * opts.PageSize).
		Limit(opts.PageSize).
		Find(&list).Error
	if err != nil {
		return ListResult{}, err
	}
	return ListResult{List: list, Total: total, Page: opts.Page, PageSize: opts.PageSize}, nil
}

func (s *Service) Create(alias *model.ModelAlias) (*model.ModelAlias, error) {
	if alias == nil {
		return nil, errors.New("alias is nil")
	}
	normalize(alias)
	if err := validate(alias); err != nil {
		return nil, err
	}
	if err := s.db.Create(alias).Error; err != nil {
		return nil, err
	}
	invalidateActiveAliasCache(alias.AliasName)
	return alias, nil
}

func (s *Service) Update(id uint, patch map[string]interface{}) (*model.ModelAlias, error) {
	var alias model.ModelAlias
	if err := s.db.First(&alias, id).Error; err != nil {
		return nil, err
	}
	updates := sanitizePatch(patch)
	if len(updates) == 0 {
		return &alias, nil
	}
	oldAliasName := alias.AliasName
	if err := s.db.Model(&alias).Updates(updates).Error; err != nil {
		return nil, err
	}
	if err := s.db.First(&alias, id).Error; err != nil {
		return nil, err
	}
	normalize(&alias)
	if err := validate(&alias); err != nil {
		return nil, err
	}
	if err := s.db.Save(&alias).Error; err != nil {
		return nil, err
	}
	invalidateActiveAliasCache(oldAliasName)
	invalidateActiveAliasCache(alias.AliasName)
	return &alias, nil
}

func (s *Service) Delete(id uint) error {
	var alias model.ModelAlias
	_ = s.db.Select("alias_name").First(&alias, id).Error
	err := s.db.Delete(&model.ModelAlias{}, id).Error
	if err == nil && alias.AliasName != "" {
		invalidateActiveAliasCache(alias.AliasName)
	}
	return err
}

func (s *Service) Resolve(requested string) (Resolution, error) {
	requested = strings.TrimSpace(requested)
	out := Resolution{RequestedModel: requested, ResolvedModel: requested}
	if requested == "" {
		return out, nil
	}
	seen := map[string]bool{}
	current := requested
	for depth := 0; depth < 6; depth++ {
		if seen[current] {
			return out, fmt.Errorf("model alias loop detected at %s", current)
		}
		seen[current] = true
		alias, err := s.findActiveAlias(current)
		if errors.Is(err, gorm.ErrRecordNotFound) {
			if current != requested {
				out.ResolvedModel = current
			}
			return out, nil
		}
		if err != nil {
			return out, err
		}
		out.IsAlias = true
		out.Alias = alias
		out.Chain = append(out.Chain, alias.AliasName)
		current = alias.TargetModelName
		out.ResolvedModel = current
		now := time.Now()
		_ = s.db.Model(alias).Update("last_resolved_at", &now).Error
	}
	return out, fmt.Errorf("model alias chain too deep for %s", requested)
}

func (s *Service) SuggestAliases(opts SuggestOptions) ([]AliasSuggestion, error) {
	var models []model.AIModel
	q := s.db.Select("id, supplier_id, model_name, is_active, status").
		Where("is_active = ?", true)
	if opts.SupplierID > 0 {
		q = q.Where("supplier_id = ?", opts.SupplierID)
	}
	if err := q.Find(&models).Error; err != nil {
		return nil, err
	}
	bySupplier := map[uint]map[string]model.AIModel{}
	for _, item := range models {
		if bySupplier[item.SupplierID] == nil {
			bySupplier[item.SupplierID] = map[string]model.AIModel{}
		}
		bySupplier[item.SupplierID][item.ModelName] = item
	}

	var suggestions []AliasSuggestion
	for supplierID, items := range bySupplier {
		snapshots := map[string][]string{}
		for name := range items {
			if match := datedSnapshotSuffix.FindStringSubmatch(name); len(match) == 3 {
				base := match[1]
				snapshots[base] = append(snapshots[base], name)
			}
		}
		for base, names := range snapshots {
			if _, ok := items[base]; !ok {
				continue
			}
			sort.Strings(names)
			target := names[len(names)-1]
			if target == base {
				continue
			}
			exists, err := s.aliasExists(base, supplierID)
			if err != nil {
				return nil, err
			}
			item := AliasSuggestion{
				AliasName:       base,
				TargetModelName: target,
				SupplierID:      supplierID,
				AliasType:       model.ModelAliasTypeStable,
				Source:          model.ModelAliasSourceRule,
				Confidence:      0.75,
				Reason:          "stable model and dated snapshot share the same family",
			}
			if opts.Apply && !exists {
				_, err := s.Create(&model.ModelAlias{
					AliasName:          item.AliasName,
					TargetModelName:    item.TargetModelName,
					SupplierID:         item.SupplierID,
					AliasType:          item.AliasType,
					ResolutionStrategy: model.ModelAliasResolutionFixed,
					Source:             item.Source,
					Confidence:         item.Confidence,
					IsActive:           true,
					IsPublic:           true,
					Notes:              item.Reason,
				})
				if err != nil {
					return nil, err
				}
				item.Applied = true
			}
			if !exists || opts.Apply {
				suggestions = append(suggestions, item)
			}
		}
	}
	sort.SliceStable(suggestions, func(i, j int) bool {
		if suggestions[i].SupplierID != suggestions[j].SupplierID {
			return suggestions[i].SupplierID < suggestions[j].SupplierID
		}
		return suggestions[i].AliasName < suggestions[j].AliasName
	})
	return suggestions, nil
}

func (s *Service) findActiveAlias(name string) (*model.ModelAlias, error) {
	if cached, ok := loadActiveAliasCache(name); ok {
		if cached.notFound {
			return nil, gorm.ErrRecordNotFound
		}
		return cloneModelAlias(cached.alias), nil
	}

	var alias model.ModelAlias
	now := time.Now()
	err := s.db.Where("alias_name = ? AND is_active = ?", name, true).
		Where("(effective_from IS NULL OR effective_from <= ?) AND (effective_until IS NULL OR effective_until >= ?)", now, now).
		Order("confidence DESC, supplier_id DESC, updated_at DESC").
		First(&alias).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			storeActiveAliasCache(name, nil, true)
		}
		return nil, err
	}
	storeActiveAliasCache(name, &alias, false)
	return &alias, nil
}

func loadActiveAliasCache(name string) (*activeAliasCacheEntry, bool) {
	if strings.TrimSpace(name) == "" {
		return nil, false
	}
	if raw, ok := activeAliasCache.Load(name); ok {
		entry, ok := raw.(*activeAliasCacheEntry)
		if !ok || time.Now().After(entry.expiresAt) {
			activeAliasCache.Delete(name)
			return nil, false
		}
		return entry, true
	}
	return nil, false
}

func storeActiveAliasCache(name string, alias *model.ModelAlias, notFound bool) {
	if strings.TrimSpace(name) == "" {
		return
	}
	activeAliasCache.Store(name, &activeAliasCacheEntry{
		alias:     cloneModelAlias(alias),
		notFound:  notFound,
		expiresAt: time.Now().Add(activeAliasCacheTTL),
	})
}

func invalidateActiveAliasCache(name string) {
	if strings.TrimSpace(name) != "" {
		activeAliasCache.Delete(name)
	}
}

func cloneModelAlias(alias *model.ModelAlias) *model.ModelAlias {
	if alias == nil {
		return nil
	}
	cp := *alias
	return &cp
}

func (s *Service) aliasExists(aliasName string, supplierID uint) (bool, error) {
	var count int64
	err := s.db.Model(&model.ModelAlias{}).
		Where("alias_name = ? AND supplier_id = ?", aliasName, supplierID).
		Count(&count).Error
	return count > 0, err
}

func normalize(alias *model.ModelAlias) {
	alias.AliasName = strings.TrimSpace(alias.AliasName)
	alias.TargetModelName = strings.TrimSpace(alias.TargetModelName)
	alias.AliasType = strings.TrimSpace(alias.AliasType)
	alias.ResolutionStrategy = strings.TrimSpace(alias.ResolutionStrategy)
	alias.Source = strings.TrimSpace(alias.Source)
	if alias.AliasType == "" {
		alias.AliasType = model.ModelAliasTypeCustom
	}
	if alias.ResolutionStrategy == "" {
		alias.ResolutionStrategy = model.ModelAliasResolutionFixed
	}
	if alias.Source == "" {
		alias.Source = model.ModelAliasSourceManual
	}
	if alias.Confidence <= 0 {
		alias.Confidence = 1
	}
	if alias.Confidence > 1 {
		alias.Confidence = 1
	}
}

func validate(alias *model.ModelAlias) error {
	if alias.AliasName == "" || alias.TargetModelName == "" {
		return errors.New("alias_name and target_model_name are required")
	}
	if alias.AliasName == alias.TargetModelName {
		return errors.New("alias_name cannot equal target_model_name")
	}
	return nil
}

func sanitizePatch(patch map[string]interface{}) map[string]interface{} {
	allowed := map[string]bool{
		"alias_name":          true,
		"target_model_name":   true,
		"supplier_id":         true,
		"alias_type":          true,
		"resolution_strategy": true,
		"is_public":           true,
		"is_active":           true,
		"source":              true,
		"confidence":          true,
		"notes":               true,
		"effective_from":      true,
		"effective_until":     true,
	}
	out := map[string]interface{}{}
	for key, value := range patch {
		if allowed[key] {
			out[key] = value
		}
	}
	return out
}
