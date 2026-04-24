package admin

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"tokenhub-server/internal/model"
	"tokenhub-server/internal/pkg/response"
)

// ================================================================================
// SupportModelProfile 管理接口
// 挂在 SupportAdminHandler 上，复用 db + services.Selector（CRUD 后调用 InvalidateCache）
// 路径：/admin/support/model-profiles
// ================================================================================

// modelProfileRequest AI 客服模型候选配置入参
type modelProfileRequest struct {
	ModelKey       string   `json:"model_key"`
	DisplayName    string   `json:"display_name"`
	Priority       *int     `json:"priority"`
	IsActive       *bool    `json:"is_active"`
	MaxTokens      *int     `json:"max_tokens"`
	Temperature    *float32 `json:"temperature"`
	EnableSearch   *bool    `json:"enable_search"`
	EnableThinking *bool    `json:"enable_thinking"`
	BudgetLevel    string   `json:"budget_level"`
	Notes          string   `json:"notes"`
}

// supportModelProfileResponse 返回体 - 含 ai_models 的能力位 (supports_web_search / supports_thinking)
// 便于前端展示「模型本身是否支持联网搜索」的能力徽标，指导配置决策。
type supportModelProfileResponse struct {
	model.SupportModelProfile
	ModelSupportsWebSearch bool `json:"model_supports_web_search"`
	ModelSupportsThinking  bool `json:"model_supports_thinking"`
	ModelExists            bool `json:"model_exists"`
}

// ListModelProfiles GET /admin/support/model-profiles
// 返回全部 SupportModelProfile（无论 is_active），按 priority DESC 排序
func (h *SupportAdminHandler) ListModelProfiles(c *gin.Context) {
	ctx := c.Request.Context()
	var profiles []model.SupportModelProfile
	if err := h.db.WithContext(ctx).
		Order("priority DESC, id ASC").
		Find(&profiles).Error; err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, 50001, err.Error())
		return
	}

	// 批量查对应 ai_models 的 features，标注「模型是否支持联网搜索」
	keys := make([]string, 0, len(profiles))
	for i := range profiles {
		keys = append(keys, profiles[i].ModelKey)
	}

	type capRow struct {
		ModelName          string `json:"model_name"`
		SupportsWebSearch  bool   `json:"supports_web_search"`
		SupportsThinking   bool   `json:"supports_thinking"`
		Exists             bool   `json:"-"`
	}
	capByKey := make(map[string]capRow, len(keys))
	if len(keys) > 0 {
		var rows []struct {
			ModelName string `gorm:"column:model_name"`
			Features  string `gorm:"column:features"`
		}
		h.db.WithContext(ctx).
			Table("ai_models").
			Select("model_name, features").
			Where("model_name IN ?", keys).
			Scan(&rows)
		for _, r := range rows {
			cap := capRow{ModelName: r.ModelName, Exists: true}
			// features 是 JSON 字符串 / longtext，按 MySQL JSON_EXTRACT 风格处理
			if strings.Contains(r.Features, `"supports_web_search":true`) {
				cap.SupportsWebSearch = true
			}
			if strings.Contains(r.Features, `"supports_thinking":true`) {
				cap.SupportsThinking = true
			}
			capByKey[r.ModelName] = cap
		}
	}

	out := make([]supportModelProfileResponse, 0, len(profiles))
	for i := range profiles {
		cap := capByKey[profiles[i].ModelKey]
		out = append(out, supportModelProfileResponse{
			SupportModelProfile:    profiles[i],
			ModelSupportsWebSearch: cap.SupportsWebSearch,
			ModelSupportsThinking:  cap.SupportsThinking,
			ModelExists:            cap.Exists,
		})
	}
	response.Success(c, out)
}

// CreateModelProfile POST /admin/support/model-profiles
func (h *SupportAdminHandler) CreateModelProfile(c *gin.Context) {
	var body modelProfileRequest
	if err := c.ShouldBindJSON(&body); err != nil {
		response.ErrorMsg(c, http.StatusBadRequest, 40000, err.Error())
		return
	}
	body.ModelKey = strings.TrimSpace(body.ModelKey)
	if body.ModelKey == "" {
		response.ErrorMsg(c, http.StatusBadRequest, 40001, "model_key 必填")
		return
	}
	if !isValidBudgetLevel(body.BudgetLevel) {
		response.ErrorMsg(c, http.StatusBadRequest, 40002, "budget_level 必须是 normal / economy / emergency")
		return
	}

	// 唯一性检查：model_key 全库唯一
	var existing model.SupportModelProfile
	if err := h.db.WithContext(c.Request.Context()).
		Where("model_key = ?", body.ModelKey).
		First(&existing).Error; err == nil {
		response.ErrorMsg(c, http.StatusConflict, 40003, "model_key 已存在")
		return
	}

	p := &model.SupportModelProfile{
		ModelKey:       body.ModelKey,
		DisplayName:    strings.TrimSpace(body.DisplayName),
		Priority:       defIntP(body.Priority, 100),
		IsActive:       defBoolP(body.IsActive, true),
		MaxTokens:      defIntP(body.MaxTokens, 1024),
		Temperature:    defFloat32P(body.Temperature, 0.3),
		EnableSearch:   defBoolP(body.EnableSearch, false),
		EnableThinking: defBoolP(body.EnableThinking, false),
		BudgetLevel:    body.BudgetLevel,
		Notes:          strings.TrimSpace(body.Notes),
	}

	if err := h.db.WithContext(c.Request.Context()).Create(p).Error; err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, 50001, err.Error())
		return
	}

	h.invalidateSelectorCache()
	response.Success(c, p)
}

// UpdateModelProfile PUT /admin/support/model-profiles/:id
func (h *SupportAdminHandler) UpdateModelProfile(c *gin.Context) {
	id, _ := strconv.ParseUint(c.Param("id"), 10, 64)
	if id == 0 {
		response.ErrorMsg(c, http.StatusBadRequest, 40000, "invalid id")
		return
	}
	var body modelProfileRequest
	if err := c.ShouldBindJSON(&body); err != nil {
		response.ErrorMsg(c, http.StatusBadRequest, 40000, err.Error())
		return
	}

	var existing model.SupportModelProfile
	if err := h.db.WithContext(c.Request.Context()).First(&existing, id).Error; err != nil {
		response.ErrorMsg(c, http.StatusNotFound, 40404, "profile not found")
		return
	}

	// 部分更新：仅更新提供的字段（零值也可显式修改 bool）
	updates := map[string]any{}
	if key := strings.TrimSpace(body.ModelKey); key != "" && key != existing.ModelKey {
		// 如要改 model_key，需唯一性校验
		var dup int64
		h.db.Model(&model.SupportModelProfile{}).
			Where("model_key = ? AND id != ?", key, id).
			Count(&dup)
		if dup > 0 {
			response.ErrorMsg(c, http.StatusConflict, 40003, "model_key 已被其他配置占用")
			return
		}
		updates["model_key"] = key
	}
	if body.DisplayName != "" || strings.TrimSpace(body.DisplayName) == "" {
		// 允许清空 display_name（客户端显式传入空串时不更新，保持幂等）
		if body.DisplayName != "" {
			updates["display_name"] = strings.TrimSpace(body.DisplayName)
		}
	}
	if body.Priority != nil {
		updates["priority"] = *body.Priority
	}
	if body.IsActive != nil {
		updates["is_active"] = *body.IsActive
	}
	if body.MaxTokens != nil {
		updates["max_tokens"] = *body.MaxTokens
	}
	if body.Temperature != nil {
		updates["temperature"] = *body.Temperature
	}
	if body.EnableSearch != nil {
		updates["enable_search"] = *body.EnableSearch
	}
	if body.EnableThinking != nil {
		updates["enable_thinking"] = *body.EnableThinking
	}
	if body.BudgetLevel != "" {
		if !isValidBudgetLevel(body.BudgetLevel) {
			response.ErrorMsg(c, http.StatusBadRequest, 40002, "budget_level 必须是 normal / economy / emergency")
			return
		}
		updates["budget_level"] = body.BudgetLevel
	}
	if body.Notes != "" {
		updates["notes"] = strings.TrimSpace(body.Notes)
	}

	if len(updates) == 0 {
		response.Success(c, gin.H{"ok": true, "noop": true})
		return
	}

	if err := h.db.WithContext(c.Request.Context()).
		Model(&model.SupportModelProfile{}).
		Where("id = ?", id).
		Updates(updates).Error; err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, 50001, err.Error())
		return
	}

	h.invalidateSelectorCache()
	response.Success(c, gin.H{"ok": true})
}

// DeleteModelProfile DELETE /admin/support/model-profiles/:id
func (h *SupportAdminHandler) DeleteModelProfile(c *gin.Context) {
	id, _ := strconv.ParseUint(c.Param("id"), 10, 64)
	if id == 0 {
		response.ErrorMsg(c, http.StatusBadRequest, 40000, "invalid id")
		return
	}

	// 检查：不能删除当前唯一可用的配置，否则 orchestrator 会 fallback 到 emergency reply
	var activeCount int64
	h.db.Model(&model.SupportModelProfile{}).
		Where("is_active = ? AND id != ?", true, id).
		Count(&activeCount)
	if activeCount == 0 {
		response.ErrorMsg(c, http.StatusBadRequest, 40005, "至少保留一个启用的模型配置")
		return
	}

	if err := h.db.WithContext(c.Request.Context()).
		Unscoped().
		Delete(&model.SupportModelProfile{}, id).Error; err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, 50001, err.Error())
		return
	}

	h.invalidateSelectorCache()
	response.Success(c, gin.H{"ok": true})
}

// ToggleModelProfile PATCH /admin/support/model-profiles/:id/toggle
// 快捷切换 is_active
func (h *SupportAdminHandler) ToggleModelProfile(c *gin.Context) {
	id, _ := strconv.ParseUint(c.Param("id"), 10, 64)
	if id == 0 {
		response.ErrorMsg(c, http.StatusBadRequest, 40000, "invalid id")
		return
	}
	var existing model.SupportModelProfile
	if err := h.db.WithContext(c.Request.Context()).First(&existing, id).Error; err != nil {
		response.ErrorMsg(c, http.StatusNotFound, 40404, "profile not found")
		return
	}

	newState := !existing.IsActive
	// 若要禁用，检查剩余启用数
	if !newState {
		var activeCount int64
		h.db.Model(&model.SupportModelProfile{}).
			Where("is_active = ? AND id != ?", true, id).
			Count(&activeCount)
		if activeCount == 0 {
			response.ErrorMsg(c, http.StatusBadRequest, 40005, "至少保留一个启用的模型配置")
			return
		}
	}

	if err := h.db.WithContext(c.Request.Context()).
		Model(&model.SupportModelProfile{}).
		Where("id = ?", id).
		Update("is_active", newState).Error; err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, 50001, err.Error())
		return
	}

	h.invalidateSelectorCache()
	response.Success(c, gin.H{"ok": true, "is_active": newState})
}

// ---------- helpers ----------

func (h *SupportAdminHandler) invalidateSelectorCache() {
	if h.services != nil && h.services.Selector != nil {
		h.services.Selector.InvalidateCache()
	}
}

func isValidBudgetLevel(s string) bool {
	switch s {
	case "", "normal", "economy", "emergency":
		return true
	}
	return false
}

func defIntP(p *int, fallback int) int {
	if p == nil {
		return fallback
	}
	return *p
}

func defBoolP(p *bool, fallback bool) bool {
	if p == nil {
		return fallback
	}
	return *p
}

func defFloat32P(p *float32, fallback float32) float32 {
	if p == nil {
		return fallback
	}
	return *p
}
