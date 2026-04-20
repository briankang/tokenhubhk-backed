package support

import (
	"context"
	"sync"
	"time"

	"go.uber.org/zap"
	"gorm.io/gorm"

	"tokenhub-server/internal/model"
	"tokenhub-server/internal/pkg/logger"
)

// ModelSelector 客服候选模型选择器
// 按 SupportModelProfile.Priority 降序 + BudgetLevel 匹配挑选模型
// 支持 Fallback 链：主选失败后取下一个
type ModelSelector struct {
	db *gorm.DB

	mu       sync.RWMutex
	cacheAt  time.Time
	profiles []model.SupportModelProfile
}

func NewModelSelector(db *gorm.DB) *ModelSelector {
	return &ModelSelector{db: db}
}

// Candidates 返回按优先级排序的候选模型列表（过滤 is_active=true + budget_level 匹配）
// 对应业务场景：budget=NORMAL → 取 budget_level 为 normal / economy / emergency 的全部（按 priority 降序）
//               budget=ECONOMY → 取 budget_level != normal 的（优先 economy 再 emergency）
//               budget=EMERGENCY → 只取 emergency，全无则空切片
func (s *ModelSelector) Candidates(ctx context.Context, budget BudgetLevel) []model.SupportModelProfile {
	all := s.loadAll(ctx)
	out := make([]model.SupportModelProfile, 0, len(all))
	for _, p := range all {
		if !p.IsActive {
			continue
		}
		if !matchBudget(p.BudgetLevel, budget) {
			continue
		}
		out = append(out, p)
	}
	return out
}

// Primary 返回第一优先模型（若无可用，返回 nil）
func (s *ModelSelector) Primary(ctx context.Context, budget BudgetLevel) *model.SupportModelProfile {
	cands := s.Candidates(ctx, budget)
	if len(cands) == 0 {
		return nil
	}
	return &cands[0]
}

// InvalidateCache 配置变更后强制刷新
func (s *ModelSelector) InvalidateCache() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.profiles = nil
	s.cacheAt = time.Time{}
}

func (s *ModelSelector) loadAll(ctx context.Context) []model.SupportModelProfile {
	s.mu.RLock()
	if time.Since(s.cacheAt) < 2*time.Minute && s.profiles != nil {
		p := s.profiles
		s.mu.RUnlock()
		return p
	}
	s.mu.RUnlock()

	s.mu.Lock()
	defer s.mu.Unlock()
	if time.Since(s.cacheAt) < 2*time.Minute && s.profiles != nil {
		return s.profiles
	}
	var list []model.SupportModelProfile
	if err := s.db.WithContext(ctx).Where("is_active = ?", true).Order("priority DESC, id ASC").Find(&list).Error; err != nil {
		logger.L.Error("load support model profiles", zap.Error(err))
		return nil
	}
	s.profiles = list
	s.cacheAt = time.Now()
	return list
}

// matchBudget 判断候选模型的 budget_level 是否符合当前预算档位
func matchBudget(modelBudget string, current BudgetLevel) bool {
	switch current {
	case BudgetNormal:
		// 全部候选都可用
		return true
	case BudgetEconomy:
		// 不用 normal 的高成本模型
		return modelBudget == "economy" || modelBudget == "emergency"
	case BudgetEmergency:
		// 仅紧急兜底
		return modelBudget == "emergency"
	}
	return false
}
