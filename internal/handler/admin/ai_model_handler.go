package admin

import (
	"encoding/json"
	"log"
	"math"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"

	"tokenhub-server/internal/database"
	"tokenhub-server/internal/middleware"
	"tokenhub-server/internal/model"
	"tokenhub-server/internal/pkg/errcode"
	"tokenhub-server/internal/pkg/response"
	aimodelsvc "tokenhub-server/internal/service/aimodel"
	"tokenhub-server/internal/service/modeldiscovery"
	"tokenhub-server/internal/taskqueue"
)

// invalidatePublicModelsCache 清除公开模型列表缓存，使管理员操作立即对用户可见
func invalidatePublicModelsCache() {
	middleware.CacheInvalidate("cache:/api/v1/public/models*")
}

// AIModelHandler AI模型管理接口处理器
type AIModelHandler struct {
	svc    *aimodelsvc.AIModelService
	bridge *taskqueue.SSEBridge // nil=单体模式，非nil=委派模式
}

// NewAIModelHandler 创建AI模型管理Handler实例
func NewAIModelHandler(svc *aimodelsvc.AIModelService, bridge ...*taskqueue.SSEBridge) *AIModelHandler {
	if svc == nil {
		panic("admin ai model handler: service is nil")
	}
	h := &AIModelHandler{svc: svc}
	if len(bridge) > 0 {
		h.bridge = bridge[0]
	}
	return h
}

// LabelDTO 模型标签 k:v 数据传输对象（公开 API 使用）
type LabelDTO struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

// PublicModelResponse 公开模型列表响应格式（供前端 /models 页面使用）
// 字段命名遵循前端 AdminModel 类型定义
type PublicModelResponse struct {
	ID                 uint       `json:"id"`                    // 模型 ID
	ModelID            string     `json:"model_id"`              // 模型标识符（如 gpt-4o）
	Name               string     `json:"name"`                  // 展示名称
	Provider           string     `json:"provider"`              // 供应商名称
	ProviderIcon       string     `json:"provider_icon"`         // 供应商图标（emoji）
	Description        string     `json:"description"`           // 模型描述
	ContextWindow      int        `json:"context_window"`        // 上下文窗口大小
	InputPrice         int64      `json:"input_price"`           // 输入价格（积分/百万token）
	OutputPrice        int64      `json:"output_price"`          // 输出价格（积分/百万token）
	InputPriceRMB      float64    `json:"input_price_rmb"`       // 输入价格（人民币/百万token）
	OutputPriceRMB     float64    `json:"output_price_rmb"`      // 输出价格（人民币/百万token）
	Capabilities       []string   `json:"capabilities"`          // 能力标签
	Status             string     `json:"status"`                // 状态：online/offline
	IsNew              bool       `json:"is_new"`                // 是否新品
	IsFeatured         bool       `json:"is_featured"`           // 是否推荐
	MaxTokens          int        `json:"max_tokens"`            // 最大输出 Token 数
	ModelType          string     `json:"model_type"`            // 模型类型: LLM/VLM/ImageGeneration/VideoGeneration/Audio 等
	Tags               string     `json:"tags"`                  // 搜索标签（逗号分隔）
	Labels             []LabelDTO `json:"labels,omitempty"`      // k:v 标签列表（热卖/开源/优惠等）
	Discount           int        `json:"discount,omitempty"`    // 折扣百分比（如85表示85折），0表示无折扣信息
	AvgLatencyMs       int64      `json:"avg_latency_ms,omitempty"`  // 平均延迟（毫秒），最近24小时
	SuccessRate        float64    `json:"success_rate,omitempty"`    // 成功率（0-100），最近24小时
	RequestCount       int64      `json:"request_count,omitempty"`   // 请求量，最近24小时
	// 多计费单位支持（v3.2）
	PricingUnit        string     `json:"pricing_unit,omitempty"`    // 计费单位: per_million_tokens / per_image / per_second / per_minute / per_10k_characters / per_million_characters / per_call / per_hour
	Variant            string     `json:"variant,omitempty"`         // 变体/质量档（如 1024x1024/hd/720p）
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

	// 动态生成完整标签：合并数据库存储的 tags + 供应商品牌
	// 确保即使数据库中 tags 未回填，API 也能返回正确的品牌标签
	tags := modeldiscovery.InferModelTags(m.ModelName, m.Supplier.Code)

	// 显示售价：优先使用 ModelPricing 中的售价，未配置时默认官方价9折
	var inputPriceRMB, outputPriceRMB float64
	if m.Pricing != nil && (m.Pricing.InputPriceRMB > 0 || m.Pricing.OutputPriceRMB > 0) {
		inputPriceRMB = m.Pricing.InputPriceRMB
		outputPriceRMB = m.Pricing.OutputPriceRMB
	} else {
		// 售价未配置，默认官方成本价9折
		inputPriceRMB = math.Round(m.InputCostRMB*0.9*10000) / 10000
		outputPriceRMB = math.Round(m.OutputCostRMB*0.9*10000) / 10000
	}
	inputPrice := int64(inputPriceRMB * 10000)
	outputPrice := int64(outputPriceRMB * 10000)

	// 计算折扣百分比（基于输入价格）
	// 只有折扣力度超过10%（即售价低于原价9折以下）才在前端展示折扣标签
	discount := 0
	if m.InputCostRMB > 0 && inputPriceRMB < m.InputCostRMB {
		d := int(math.Round(inputPriceRMB / m.InputCostRMB * 100))
		if d < 90 { // < 9折才显示（折扣 > 10%）
			discount = d
		}
	}

	return PublicModelResponse{
		ID:             m.ID,
		ModelID:        m.ModelName,
		Name:           name,
		Provider:       provider,
		ProviderIcon:   icon,
		Description:    m.Description,
		ContextWindow:  m.ContextWindow,
		InputPrice:     inputPrice,
		OutputPrice:    outputPrice,
		InputPriceRMB:  inputPriceRMB,
		OutputPriceRMB: outputPriceRMB,
		Capabilities:   capabilityKeywords(m.ModelName),
		Status:         status,
		IsNew:          false, // TODO: 可根据创建时间计算
		IsFeatured:     false, // TODO: 可根据配置决定
		MaxTokens:      m.MaxTokens,
		ModelType:       m.ModelType,
		Tags:            tags,
		Discount:        discount,
		PricingUnit:     m.PricingUnit,
		Variant:         m.Variant,
	}
}

// Stats 返回模型统计数量（总数/已启用/在线），直接聚合全量数据，不受分页限制
// GET /api/v1/admin/ai-models/stats
func (h *AIModelHandler) Stats(c *gin.Context) {
	stats, err := h.svc.GetStats(c.Request.Context())
	if err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, err.Error())
		return
	}
	response.Success(c, stats)
}

// List 分页获取AI模型列表 GET /api/v1/admin/ai-models
// 管理员接口：返回完整 AIModel 数据（包含 model_type, display_name, context_window,
// max_input_tokens, max_output_tokens, supplier_status, features, input_modalities,
// output_modalities, task_types, domain, version 等扩展字段），供前端 DashModelsPage 使用
func (h *AIModelHandler) List(c *gin.Context) {
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "20"))
	supplierID, _ := strconv.Atoi(c.DefaultQuery("supplier_id", "0"))
	search := c.DefaultQuery("search", "")

	models, total, err := h.svc.ListWithFilter(c.Request.Context(), page, pageSize, uint(supplierID), search)
	if err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, err.Error())
		return
	}

	// 管理员接口直接返回完整 AIModel 结构，不做字段裁剪
	response.PageResult(c, models, total, page, pageSize)
}

// PublicList 公开模型列表 GET /api/v1/public/models
// 只返回 status=online 且 is_active=true 的模型
// 支持 ?type=ImageGeneration / VideoGeneration 等过滤（不传则默认返回聊天类 LLM/VLM）
func (h *AIModelHandler) PublicList(c *gin.Context) {
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "20"))
	modelType := c.DefaultQuery("type", "")
	sort := c.DefaultQuery("sort", "")

	var models []model.AIModel
	var total int64
	var err error
	if sort == "popular" {
		models, total, err = h.svc.ListOnlineByPopularity(c.Request.Context(), page, pageSize)
	} else if modelType != "" {
		models, total, err = h.svc.ListOnline(c.Request.Context(), page, pageSize, modelType)
	} else {
		models, total, err = h.svc.ListOnline(c.Request.Context(), page, pageSize)
	}
	if err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, err.Error())
		return
	}

	// 转换为公开 API 响应格式
	list := make([]PublicModelResponse, len(models))
	for i, m := range models {
		list[i] = toPublicResponse(m)
	}

	// 批量查询最近24小时的模型统计数据（延迟、成功率、请求量）
	type modelStat struct {
		ModelName    string  `gorm:"column:model_name"`
		AvgLatency   float64 `gorm:"column:avg_latency"`
		SuccessRate  float64 `gorm:"column:success_rate"`
		RequestCount int64   `gorm:"column:request_count"`
	}
	var stats []modelStat
	since := time.Now().Add(-24 * time.Hour)
	modelNames := make([]string, len(models))
	for i, m := range models {
		modelNames[i] = m.ModelName
	}
	database.DB.Raw(`
		SELECT model_name,
			ROUND(AVG(latency_ms)) AS avg_latency,
			ROUND(SUM(CASE WHEN status_code >= 200 AND status_code < 400 THEN 1 ELSE 0 END) * 100.0 / COUNT(*), 1) AS success_rate,
			COUNT(*) AS request_count
		FROM channel_logs
		WHERE model_name IN ? AND created_at >= ? AND deleted_at IS NULL
		GROUP BY model_name
	`, modelNames, since).Scan(&stats)

	statMap := make(map[string]*modelStat, len(stats))
	for i := range stats {
		statMap[stats[i].ModelName] = &stats[i]
	}
	for i, m := range models {
		if s, ok := statMap[m.ModelName]; ok {
			list[i].AvgLatencyMs = int64(s.AvgLatency)
			list[i].SuccessRate = s.SuccessRate
			list[i].RequestCount = s.RequestCount
		}
	}

	// 批量加载模型 k:v 标签（一次查询，避免 N+1）
	modelIDs := make([]uint, len(models))
	for i, m := range models {
		modelIDs[i] = m.ID
	}
	var allLabels []model.ModelLabel
	database.DB.Where("model_id IN ?", modelIDs).Find(&allLabels)
	labelMap := make(map[uint][]LabelDTO, len(models))
	for _, lbl := range allLabels {
		labelMap[lbl.ModelID] = append(labelMap[lbl.ModelID], LabelDTO{
			Key:   lbl.LabelKey,
			Value: lbl.LabelValue,
		})
	}
	for i, m := range models {
		if lbls, ok := labelMap[m.ID]; ok {
			list[i].Labels = lbls
		}
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

	// 前端发 input_price_rmb，后端字段为 input_cost_rmb，需要映射
	if v, ok := updates["input_price_rmb"]; ok {
		updates["input_cost_rmb"] = v
		delete(updates, "input_price_rmb")
	}
	if v, ok := updates["output_price_rmb"]; ok {
		updates["output_cost_rmb"] = v
		delete(updates, "output_price_rmb")
	}

	// JSON 字段需要序列化为 model.JSON 字节（extra_params / task_types / input_modalities / output_modalities / features）
	jsonFields := []string{"extra_params", "task_types", "input_modalities", "output_modalities", "features"}
	for _, field := range jsonFields {
		if val, ok := updates[field]; ok {
			if val == nil {
				updates[field] = model.JSON(nil)
			} else {
				bytes, _ := json.Marshal(val)
				updates[field] = model.JSON(bytes)
			}
		}
	}

	// 提取售价字段，保存到 ModelPricing 表
	sellingInputRmb, hasSellingIn := updates["selling_input_rmb"]
	sellingOutputRmb, hasSellingOut := updates["selling_output_rmb"]
	delete(updates, "selling_input_rmb")
	delete(updates, "selling_output_rmb")

	if err := h.svc.Update(c.Request.Context(), uint(id), updates); err != nil {
		response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrBadRequest.Code, err.Error())
		return
	}

	// 更新或创建平台售价
	if hasSellingIn || hasSellingOut {
		modelID := uint(id)
		var pricing model.ModelPricing
		db := database.DB
		err := db.Where("model_id = ?", modelID).First(&pricing).Error
		if err != nil {
			// 不存在则创建
			pricing = model.ModelPricing{ModelID: modelID}
		}
		if hasSellingIn {
			if v, ok := sellingInputRmb.(float64); ok {
				pricing.InputPriceRMB = v
				pricing.InputPricePerToken = int64(v * 10000)
			}
		}
		if hasSellingOut {
			if v, ok := sellingOutputRmb.(float64); ok {
				pricing.OutputPriceRMB = v
				pricing.OutputPricePerToken = int64(v * 10000)
			}
		}
		if pricing.ID == 0 {
			db.Create(&pricing)
		} else {
			db.Save(&pricing)
		}
	}

	invalidatePublicModelsCache()
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

	invalidatePublicModelsCache()
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

	invalidatePublicModelsCache()
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

	invalidatePublicModelsCache()
	response.Success(c, gin.H{"message": "model set offline", "status": "offline"})
}

// Reactivate 手动重新上线模型 POST /api/v1/admin/ai-models/:id/reactivate
//
// 用途：管理员确认某个被批量检测自动下线的模型实际可用，需要立即恢复并阻止下次检测再次自动下线。
//
// 流程：
//  1. 模型 status=online
//  2. 写入一条 model_check_log{available=true, error="manual_reactivate", upstream_status="manual_override"}
//     - 该成功记录会让 IsModelMarkedUnavailableSoft / discovery.isModelCheckFailed 立即返回 false
//     - 下一次批量检测的连续失败计数从 0 重新开始
//  3. 清除公开模型缓存
func (h *AIModelHandler) Reactivate(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || id == 0 {
		response.Error(c, http.StatusBadRequest, errcode.ErrValidation)
		return
	}

	// 加载模型确认存在并取 ModelName（写日志冗余字段需要）
	var aiModel model.AIModel
	if err := database.DB.First(&aiModel, uint(id)).Error; err != nil {
		response.ErrorMsg(c, http.StatusNotFound, 40401, "模型不存在")
		return
	}

	// 1. 设置模型状态为 online
	if err := h.svc.SetStatus(c.Request.Context(), uint(id), "online"); err != nil {
		response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrBadRequest.Code, err.Error())
		return
	}

	// 2. 写入一条 manual_override 检测日志
	log := &model.ModelCheckLog{
		ModelID:        aiModel.ID,
		ModelName:      aiModel.ModelName,
		Available:      true,
		Error:          "manual_reactivate",
		CheckedAt:      time.Now(),
		AutoDisabled:   false,
		UpstreamStatus: "manual_override",
	}
	if err := database.DB.Create(log).Error; err != nil {
		// 日志写入失败不阻塞主流程，仅记录
		response.Success(c, gin.H{"message": "model reactivated (log write failed)", "status": "online"})
		return
	}

	// 3. 清除公开模型缓存
	invalidatePublicModelsCache()
	response.Success(c, gin.H{"message": "model reactivated", "status": "online", "log_id": log.ID})
}

// ========== 模型下线扫描与批量下线 ==========

// DeprecationScanResult 下线扫描结果（含检测报告）
type DeprecationScanResult struct {
	// 我们数据库中有、但供应商 API 已不返回的模型（可能被下线）
	PossiblyDeprecated []DeprecationCandidate `json:"possibly_deprecated"`
	// 供应商返回的、我们数据库中没有的新模型（仅供参考）
	NewModelsFromProvider int `json:"new_models_from_provider"`
	// 供应商返回但仅在我们 offline 库中存在的模型（供应商仍在但我们标记了下线）
	NewModelsFromProviderList []string `json:"new_models_from_provider_list,omitempty"`
	// 扫描的供应商名称
	SupplierName string `json:"supplier_name"`
	// 供应商 API 返回的模型总数
	ProviderTotal int `json:"provider_total"`
	// 我们数据库中该供应商的 online 模型总数
	OurOnlineTotal int `json:"our_online_total"`
	// 我们数据库中该供应商的 offline 模型总数
	OurOfflineTotal int `json:"our_offline_total"`
	// 已下线模型列表（供应商 API 已不返回、且在我们数据库中已标记 offline 的模型）
	AlreadyOfflineModels []DeprecationCandidate `json:"already_offline_models"`
	// 扫描耗时（毫秒）
	ScanDurationMs int64 `json:"scan_duration_ms"`
}

// DeprecationCandidate 可能下线的模型候选
type DeprecationCandidate struct {
	ID          uint   `json:"id"`
	ModelName   string `json:"model_name"`
	DisplayName string `json:"display_name"`
	Status      string `json:"status"`
	SupplierID  uint   `json:"supplier_id"`
	ModelType   string `json:"model_type,omitempty"`
	PricingUnit string `json:"pricing_unit,omitempty"`
}

// scanSupplierDeprecation 扫描单个供应商的模型下线情况（核心逻辑）
func (h *AIModelHandler) scanSupplierDeprecation(supplier model.Supplier) (*DeprecationScanResult, error) {
	// 获取数据库中该供应商的所有模型（online + offline 均需要）
	var onlineModels []model.AIModel
	database.DB.Where("supplier_id = ? AND status = ?", supplier.ID, "online").Find(&onlineModels)

	var offlineModels []model.AIModel
	database.DB.Where("supplier_id = ? AND status = ?", supplier.ID, "offline").Find(&offlineModels)

	// 通过 DiscoveryService 拉取供应商当前可用模型列表
	discoverySvc := modeldiscovery.NewDiscoveryService(database.DB)
	providerModelNames, err := discoverySvc.FetchProviderModelNames(supplier.ID)
	if err != nil {
		return nil, err
	}

	// 构建供应商模型名称集合
	providerSet := make(map[string]bool, len(providerModelNames))
	for _, name := range providerModelNames {
		providerSet[strings.ToLower(name)] = true
	}

	// 构建我们 online 模型名称集合
	onlineSet := make(map[string]bool, len(onlineModels))
	for _, m := range onlineModels {
		onlineSet[strings.ToLower(m.ModelName)] = true
	}
	// 构建我们全部模型名称集合（online+offline）
	allDBSet := make(map[string]bool, len(onlineModels)+len(offlineModels))
	for _, m := range onlineModels {
		allDBSet[strings.ToLower(m.ModelName)] = true
	}
	for _, m := range offlineModels {
		allDBSet[strings.ToLower(m.ModelName)] = true
	}

	// ① 找出我们 online 但供应商已不返回的模型 → 需要下线的候选
	var candidates []DeprecationCandidate
	for _, m := range onlineModels {
		if !providerSet[strings.ToLower(m.ModelName)] {
			candidates = append(candidates, DeprecationCandidate{
				ID:          m.ID,
				ModelName:   m.ModelName,
				DisplayName: m.DisplayName,
				Status:      m.Status,
				SupplierID:  m.SupplierID,
				ModelType:   m.ModelType,
				PricingUnit: m.PricingUnit,
			})
		}
	}

	// ② 找出我们 offline 且供应商也不返回的模型 → 确认已下线
	var alreadyOffline []DeprecationCandidate
	for _, m := range offlineModels {
		if !providerSet[strings.ToLower(m.ModelName)] {
			alreadyOffline = append(alreadyOffline, DeprecationCandidate{
				ID:          m.ID,
				ModelName:   m.ModelName,
				DisplayName: m.DisplayName,
				Status:      m.Status,
				SupplierID:  m.SupplierID,
				ModelType:   m.ModelType,
				PricingUnit: m.PricingUnit,
			})
		}
	}

	// ③ 统计供应商有但我们完全没有的新模型（排除 online+offline 都有的）
	var newModelNames []string
	for _, name := range providerModelNames {
		if !allDBSet[strings.ToLower(name)] {
			newModelNames = append(newModelNames, name)
		}
	}

	return &DeprecationScanResult{
		PossiblyDeprecated:        candidates,
		NewModelsFromProvider:     len(newModelNames),
		NewModelsFromProviderList: newModelNames,
		SupplierName:              supplier.Name,
		ProviderTotal:             len(providerModelNames),
		OurOnlineTotal:            len(onlineModels),
		OurOfflineTotal:           len(offlineModels),
		AlreadyOfflineModels:      alreadyOffline,
	}, nil
}

// DeprecationScan POST /admin/models/deprecation-scan
// 通过对比数据库模型与供应商 API 返回的模型列表，找出可能已下线的模型
// 同时返回已下线模型列表（供应商已不返回 + 我们库中 offline 的模型）
func (h *AIModelHandler) DeprecationScan(c *gin.Context) {
	startTime := time.Now()
	supplierCode := c.DefaultQuery("supplier", "alibaba")

	// 查询指定供应商
	var supplier model.Supplier
	if err := database.DB.Where("code = ?", supplierCode).First(&supplier).Error; err != nil {
		// 尝试按名称查找
		if err2 := database.DB.Where("name LIKE ?", "%"+supplierCode+"%").First(&supplier).Error; err2 != nil {
			response.ErrorMsg(c, http.StatusBadRequest, 40001, "未找到指定供应商："+supplierCode)
			return
		}
	}

	result, err := h.scanSupplierDeprecation(supplier)
	if err != nil {
		response.ErrorMsg(c, http.StatusServiceUnavailable, 50301, "无法连接供应商API："+err.Error())
		return
	}

	scanDuration := time.Since(startTime).Milliseconds()
	result.ScanDurationMs = scanDuration

	response.Success(c, result)
}

// ScannedOfflineAllResult 所有供应商扫描下线模型汇总结果
type ScannedOfflineAllResult struct {
	Groups           []ScannedOfflineGroup `json:"groups"`
	TotalModels      int                   `json:"total_models"`
	SuppliersScanned int                   `json:"suppliers_scanned"`
	SuppliersFailed  int                   `json:"suppliers_failed"`
	ScanDurationMs   int64                 `json:"scan_duration_ms"`
}

// ScannedOfflineGroup 单个供应商的扫描下线模型分组
type ScannedOfflineGroup struct {
	SupplierID   uint                   `json:"supplier_id"`
	SupplierCode string                 `json:"supplier_code"`
	SupplierName string                 `json:"supplier_name"`
	Models       []DeprecationCandidate `json:"models"`
}

// ScanOfflineAll GET /admin/models/scanned-offline
// 聚合所有API型供应商的扫描下线模型列表
func (h *AIModelHandler) ScanOfflineAll(c *gin.Context) {
	// 三服务模式：委派给 Worker
	if h.bridge != nil {
		result, err := h.bridge.PublishAndWait(c.Request.Context(), taskqueue.TaskScanOffline, nil)
		if err != nil {
			response.ErrorMsg(c, http.StatusInternalServerError, 50001, err.Error())
			return
		}
		// 直接返回 Worker 的 JSON 结果
		c.Data(http.StatusOK, "application/json", []byte(`{"code":0,"message":"ok","data":`+result.Data+`}`))
		return
	}

	// 单体模式：本地执行
	startTime := time.Now()

	// 查询所有活跃的 API 型供应商
	var suppliers []model.Supplier
	if err := database.DB.Where("status = ? AND is_active = ? AND (access_type = ? OR access_type = ?)",
		"active", true, "api", "").Find(&suppliers).Error; err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, 50001, "查询供应商失败："+err.Error())
		return
	}

	if len(suppliers) == 0 {
		response.Success(c, ScannedOfflineAllResult{
			Groups:           []ScannedOfflineGroup{},
			TotalModels:      0,
			SuppliersScanned: 0,
			SuppliersFailed:  0,
			ScanDurationMs:   time.Since(startTime).Milliseconds(),
		})
		return
	}

	// 并发扫描所有供应商，使用信号量限制并发数为 5
	var (
		mu              sync.Mutex
		wg              sync.WaitGroup
		groups          []ScannedOfflineGroup
		suppliersScanned int
		suppliersFailed  int
		totalModels     int
		semaphore       = make(chan struct{}, 5)
	)

	for _, supplier := range suppliers {
		wg.Add(1)
		go func(sup model.Supplier) {
			defer wg.Done()

			// 获取信号量
			semaphore <- struct{}{}
			defer func() { <-semaphore }()

			result, err := h.scanSupplierDeprecation(sup)
			if err != nil {
				log.Printf("[WARN] 扫描供应商 %s (ID=%d) 失败: %v", sup.Name, sup.ID, err)
				mu.Lock()
				suppliersFailed++
				mu.Unlock()
				return
			}

			// 只收集 already_offline_models（确认已下线的模型）
			if len(result.AlreadyOfflineModels) > 0 {
				mu.Lock()
				groups = append(groups, ScannedOfflineGroup{
					SupplierID:   sup.ID,
					SupplierCode: sup.Code,
					SupplierName: sup.Name,
					Models:       result.AlreadyOfflineModels,
				})
				totalModels += len(result.AlreadyOfflineModels)
				suppliersScanned++
				mu.Unlock()
			} else {
				mu.Lock()
				suppliersScanned++
				mu.Unlock()
			}
		}(supplier)
	}

	wg.Wait()

	scanDuration := time.Since(startTime).Milliseconds()

	response.Success(c, ScannedOfflineAllResult{
		Groups:           groups,
		TotalModels:      totalModels,
		SuppliersScanned: suppliersScanned,
		SuppliersFailed:  suppliersFailed,
		ScanDurationMs:   scanDuration,
	})
}


// BulkDeprecateRequest 批量下线请求
type BulkDeprecateRequest struct {
	ModelIDs             []uint `json:"model_ids" binding:"required,min=1"`
	OfflineDays          int    `json:"offline_days"`           // 多少天后正式下线，默认 7
	AnnouncementTitle    string `json:"announcement_title"`
	AnnouncementContent  string `json:"announcement_content"`
}

// BulkDeprecate POST /admin/models/bulk-deprecate
// 批量标记模型为 pending_offline，创建 model_deprecation 类型公告
func (h *AIModelHandler) BulkDeprecate(c *gin.Context) {
	var req BulkDeprecateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.ErrorMsg(c, http.StatusBadRequest, 40001, err.Error())
		return
	}
	if req.OfflineDays <= 0 {
		req.OfflineDays = 7
	}

	// 将选中模型标记为 offline（立刻对用户不可见），并记录下线时间
	now := time.Now()
	offlineAt := now.AddDate(0, 0, req.OfflineDays)
	_ = offlineAt // 可用于未来的定时任务记录

	var affectedModels []model.AIModel
	database.DB.Where("id IN ?", req.ModelIDs).Find(&affectedModels)
	if len(affectedModels) == 0 {
		response.ErrorMsg(c, http.StatusBadRequest, 40002, "未找到指定模型")
		return
	}

	// 将这些模型设为 offline
	database.DB.Model(&model.AIModel{}).Where("id IN ?", req.ModelIDs).Update("status", "offline")
	invalidatePublicModelsCache()

	// 构建公告标题和内容
	title := req.AnnouncementTitle
	if title == "" {
		modelNames := make([]string, 0, len(affectedModels))
		for _, m := range affectedModels {
			name := m.DisplayName
			if name == "" {
				name = m.ModelName
			}
			modelNames = append(modelNames, name)
		}
		title = "模型下线通知：" + strings.Join(modelNames, "、")
	}
	content := req.AnnouncementContent
	if content == "" {
		modelList := ""
		for _, m := range affectedModels {
			name := m.DisplayName
			if name == "" {
				name = m.ModelName
			}
			modelList += "- `" + name + "`\n"
		}
		content = "以下模型将于 **" + offlineAt.Format("2006-01-02") + "** 正式下线，请提前完成迁移：\n\n" + modelList + "\n如有疑问请联系客服。"
	}

	// 创建模型下线公告
	expiresAt := offlineAt.AddDate(0, 0, 7) // 公告在下线后7天过期
	// 持久化关联的模型 ID 列表（一键检测时据此跳过已确认下线的模型）
	modelIDsJSON, _ := json.Marshal(req.ModelIDs)
	ann := &model.Announcement{
		Title:      title,
		Content:    content,
		Type:       "model_deprecation",
		Priority:   "high",
		Status:     "active",
		ShowBanner: true,
		ExpiresAt:  &expiresAt,
		ModelIDs:   modelIDsJSON,
	}
	if uid, ok := c.Get("userId"); ok {
		ann.CreatedBy, _ = uid.(uint)
	}
	database.DB.Create(ann)

	response.Success(c, gin.H{
		"affected_count":  len(affectedModels),
		"announcement_id": ann.ID,
		"offline_at":      offlineAt.Format("2006-01-02"),
	})
}
