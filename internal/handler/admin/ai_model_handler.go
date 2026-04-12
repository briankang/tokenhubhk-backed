package admin

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"tokenhub-server/internal/model"
	"tokenhub-server/internal/pkg/errcode"
	"tokenhub-server/internal/pkg/response"
	aimodelsvc "tokenhub-server/internal/service/aimodel"
)

// AIModelHandler AI模型管理接口处理器
type AIModelHandler struct {
	svc *aimodelsvc.AIModelService
}

// NewAIModelHandler 创建AI模型管理Handler实例
func NewAIModelHandler(svc *aimodelsvc.AIModelService) *AIModelHandler {
	if svc == nil {
		panic("admin ai model handler: service is nil")
	}
	return &AIModelHandler{svc: svc}
}

// PublicModelResponse 公开模型列表响应格式（供前端 /models 页面使用）
// 字段命名遵循前端 AdminModel 类型定义
type PublicModelResponse struct {
	ID                 uint     `json:"id"`                    // 模型 ID
	ModelID            string   `json:"model_id"`              // 模型标识符（如 gpt-4o）
	Name               string   `json:"name"`                  // 展示名称
	Provider           string   `json:"provider"`              // 供应商名称
	ProviderIcon       string   `json:"provider_icon"`         // 供应商图标（emoji）
	Description        string   `json:"description"`           // 模型描述
	ContextWindow      int      `json:"context_window"`        // 上下文窗口大小
	InputPrice         int64    `json:"input_price"`           // 输入价格（积分/百万token）
	OutputPrice        int64    `json:"output_price"`          // 输出价格（积分/百万token）
	InputPriceRMB      float64  `json:"input_price_rmb"`       // 输入价格（人民币/百万token）
	OutputPriceRMB     float64  `json:"output_price_rmb"`      // 输出价格（人民币/百万token）
	Capabilities       []string `json:"capabilities"`          // 能力标签
	Status             string   `json:"status"`                // 状态：online/offline
	IsNew              bool     `json:"is_new"`                // 是否新品
	IsFeatured         bool     `json:"is_featured"`           // 是否推荐
	MaxTokens          int      `json:"max_tokens"`            // 最大输出 Token 数
}

// providerIconMap 供应商图标映射（emoji）
var providerIconMap = map[string]string{
	"openai":           "🟢",
	"anthropic":        "🟣",
	"google_gemini":    "🔵",
	"azure_openai":     "🔷",
	"deepseek":         "🟠",
	"aliyun_dashscope": "🔶",
	"volcengine":       "🌋",
	"moonshot":         "🌙",
	"zhipu":            "🔮",
	"baidu_wenxin":     "🔵",
}

// capabilityKeywords 能力关键词映射（基于模型名称推断能力）
func capabilityKeywords(modelName string) []string {
	caps := []string{}
	modelNameLower := strings.ToLower(modelName)

	// 基础能力
	caps = append(caps, "文本生成")

	// 根据模型名称推断能力
	if strings.Contains(modelNameLower, "code") || strings.Contains(modelNameLower, "coder") {
		caps = append(caps, "代码生成")
	}
	if strings.Contains(modelNameLower, "vision") || strings.Contains(modelNameLower, "gemini") || strings.Contains(modelNameLower, "gpt-4o") {
		caps = append(caps, "视觉理解")
	}
	if strings.Contains(modelNameLower, "reason") || strings.Contains(modelNameLower, "think") {
		caps = append(caps, "推理")
	}
	if strings.Contains(modelNameLower, "flash") || strings.Contains(modelNameLower, "mini") || strings.Contains(modelNameLower, "haiku") {
		caps = append(caps, "快速响应")
	}
	if strings.Contains(modelNameLower, "pro") || strings.Contains(modelNameLower, "max") || strings.Contains(modelNameLower, "plus") {
		caps = append(caps, "高性能")
	}

	// 默认能力
	caps = append(caps, "对话")
	caps = append(caps, "函数调用")

	return caps
}

// toPublicResponse 将 AIModel 转换为公开 API 响应格式
// 只有 status=online 且 is_active=true 的模型才会显示为 online
func toPublicResponse(m model.AIModel) PublicModelResponse {
	// 确定展示名称
	name := m.DisplayName
	if name == "" {
		name = m.ModelName
	}

	// 获取供应商名称
	provider := ""
	if m.Supplier.Name != "" {
		provider = m.Supplier.Name
	}

	// 获取供应商图标
	icon := "🔵"
	if m.Supplier.Code != "" {
		if emoji, ok := providerIconMap[m.Supplier.Code]; ok {
			icon = emoji
		}
	}

	// 确定状态：只有同时满足 is_active=true 且 status=online 才显示为 online
	status := "offline"
	if m.IsActive && m.Status == "online" {
		status = "online"
	} else if m.Status == "error" {
		status = "error"
	}

	return PublicModelResponse{
		ID:             m.ID,
		ModelID:        m.ModelName,
		Name:           name,
		Provider:       provider,
		ProviderIcon:   icon,
		Description:    m.Description,
		ContextWindow:  m.ContextWindow,
		InputPrice:     m.InputPricePerToken,
		OutputPrice:    m.OutputPricePerToken,
		InputPriceRMB:  m.InputCostRMB,
		OutputPriceRMB: m.OutputCostRMB,
		Capabilities:   capabilityKeywords(m.ModelName),
		Status:         status,
		IsNew:          false, // TODO: 可根据创建时间计算
		IsFeatured:     false, // TODO: 可根据配置决定
		MaxTokens:      m.MaxTokens,
	}
}

// List 分页获取AI模型列表 GET /api/v1/admin/ai-models
// 管理员接口：返回完整 AIModel 数据（包含 model_type, display_name, context_window,
// max_input_tokens, max_output_tokens, supplier_status, features, input_modalities,
// output_modalities, task_types, domain, version 等扩展字段），供前端 DashModelsPage 使用
func (h *AIModelHandler) List(c *gin.Context) {
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "20"))

	models, total, err := h.svc.List(c.Request.Context(), page, pageSize)
	if err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, err.Error())
		return
	}

	// 管理员接口直接返回完整 AIModel 结构，不做字段裁剪
	response.PageResult(c, models, total, page, pageSize)
}

// PublicList 公开模型列表 GET /api/v1/public/models
// 只返回 status=online 且 is_active=true 的模型
func (h *AIModelHandler) PublicList(c *gin.Context) {
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "20"))

	models, total, err := h.svc.ListOnline(c.Request.Context(), page, pageSize)
	if err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, err.Error())
		return
	}

	// 转换为公开 API 响应格式
	list := make([]PublicModelResponse, len(models))
	for i, m := range models {
		list[i] = toPublicResponse(m)
	}

	response.PageResult(c, list, total, page, pageSize)
}

// Create 新建AI模型 POST /api/v1/admin/ai-models
func (h *AIModelHandler) Create(c *gin.Context) {
	var m model.AIModel
	if err := c.ShouldBindJSON(&m); err != nil {
		response.Error(c, http.StatusBadRequest, errcode.ErrValidation)
		return
	}

	if err := h.svc.Create(c.Request.Context(), &m); err != nil {
		response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrBadRequest.Code, err.Error())
		return
	}

	response.Success(c, m)
}

// GetByID 根据ID获取AI模型详情 GET /api/v1/admin/ai-models/:id
func (h *AIModelHandler) GetByID(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || id == 0 {
		response.Error(c, http.StatusBadRequest, errcode.ErrValidation)
		return
	}

	m, err := h.svc.GetByID(c.Request.Context(), uint(id))
	if err != nil {
		response.ErrorMsg(c, http.StatusNotFound, errcode.ErrModelNotFound.Code, err.Error())
		return
	}

	response.Success(c, m)
}

// Update 更新AI模型信息 PUT /api/v1/admin/ai-models/:id
func (h *AIModelHandler) Update(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || id == 0 {
		response.Error(c, http.StatusBadRequest, errcode.ErrValidation)
		return
	}

	var updates map[string]interface{}
	if err := c.ShouldBindJSON(&updates); err != nil {
		response.Error(c, http.StatusBadRequest, errcode.ErrValidation)
		return
	}

	if err := h.svc.Update(c.Request.Context(), uint(id), updates); err != nil {
		response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrBadRequest.Code, err.Error())
		return
	}

	response.Success(c, gin.H{"message": "updated"})
}

// Delete 删除AI模型 DELETE /api/v1/admin/ai-models/:id
func (h *AIModelHandler) Delete(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || id == 0 {
		response.Error(c, http.StatusBadRequest, errcode.ErrValidation)
		return
	}

	if err := h.svc.Delete(c.Request.Context(), uint(id)); err != nil {
		response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrBadRequest.Code, err.Error())
		return
	}

	response.Success(c, gin.H{"message": "deleted"})
}

// Verify 验证模型并上线 POST /api/v1/admin/ai-models/:id/verify
// 将模型状态设置为 online，使其对用户可见
func (h *AIModelHandler) Verify(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || id == 0 {
		response.Error(c, http.StatusBadRequest, errcode.ErrValidation)
		return
	}

	// 设置模型状态为 online
	if err := h.svc.SetStatus(c.Request.Context(), uint(id), "online"); err != nil {
		response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrBadRequest.Code, err.Error())
		return
	}

	response.Success(c, gin.H{"message": "model verified and online", "status": "online"})
}

// SetOffline 将模型下线 POST /api/v1/admin/ai-models/:id/offline
// 将模型状态设置为 offline
func (h *AIModelHandler) SetOffline(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || id == 0 {
		response.Error(c, http.StatusBadRequest, errcode.ErrValidation)
		return
	}

	// 设置模型状态为 offline
	if err := h.svc.SetStatus(c.Request.Context(), uint(id), "offline"); err != nil {
		response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrBadRequest.Code, err.Error())
		return
	}

	response.Success(c, gin.H{"message": "model set offline", "status": "offline"})
}
