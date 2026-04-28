package modeldiscovery

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"gorm.io/gorm"

	"tokenhub-server/internal/model"
	"tokenhub-server/internal/service/pricescraper"
)

// DiscoveryService 模型自动发现服务
// 通过调用供应商的 /v1/models API，自动拉取可用模型列表，
// 并增量写入 AIModel 和 ChannelModel 表
type DiscoveryService struct {
	db     *gorm.DB
	client *http.Client // HTTP 客户端，超时30秒
}

// NewDiscoveryService 创建模型发现服务实例
func NewDiscoveryService(db *gorm.DB) *DiscoveryService {
	return &DiscoveryService{
		db: db,
		client: &http.Client{
			Timeout: 30 * time.Second, // 请求超时30秒
		},
	}
}

// isModelCheckFailed 检查模型是否在最近检测中被确认不可用
// 用于模型同步时跳过已知"硬下线"模型（避免反复上线又下线）
//
// 2026-04-15 起放宽规则：
//   - 最近 1 条日志 available=true → 不跳过（最新成功覆盖历史失败）
//   - 最近 3 条全部失败 且 (有 upstream_status=deprecated_upstream 或 达 3 次连续失败) → 跳过
//   - 其他 → 不跳过（避免观察窗口内的临时失败永久打压）
func (s *DiscoveryService) isModelCheckFailed(modelName string) bool {
	const threshold = 3
	var logs []model.ModelCheckLog
	if err := s.db.Where("model_name = ?", modelName).
		Order("checked_at DESC").
		Limit(threshold).
		Find(&logs).Error; err != nil || len(logs) == 0 {
		return false
	}
	// 最近一条成功 → 不跳过
	if logs[0].Available {
		return false
	}
	// 检查是否最近 N 条全部失败 + 有官网下架标记
	hasDeprecated := false
	allFailed := true
	for _, l := range logs {
		if l.Available {
			allFailed = false
			break
		}
		if l.UpstreamStatus == "deprecated_upstream" {
			hasDeprecated = true
		}
	}
	if !allFailed {
		return false
	}
	return hasDeprecated || len(logs) >= threshold
}

// SyncResult 单个渠道的同步结果
type SyncResult struct {
	ChannelID     uint     `json:"channel_id"`
	ChannelName   string   `json:"channel_name"`
	ModelsFound   int      `json:"models_found"`   // 从供应商API发现的模型总数
	ModelsAdded   int      `json:"models_added"`   // 新增写入的模型数
	ModelsUpdated int      `json:"models_updated"` // 更新已有模型数
	ModelsSkipped int      `json:"models_skipped"` // 已存在跳过的模型数
	Errors        []string `json:"errors,omitempty"`
	NewModelIDs   []uint   `json:"new_model_ids,omitempty"` // 本次新增的模型 ID（用于增量检测）
}

// SyncAllResult 全量同步所有渠道的汇总结果
type SyncAllResult struct {
	Results []SyncResult `json:"results"`
	Total   int          `json:"total"` // 参与同步的渠道总数
}

// openAIModelResponse OpenAI /v1/models API 的返回格式
type openAIModelResponse struct {
	Object string          `json:"object"` // 固定为 "list"
	Data   json.RawMessage `json:"data"`   // 原始 JSON，延迟解析以支持不同供应商格式
}

// openAIModelID 标准 OpenAI 模型条目（阿里云等仅返回这4个字段）
type openAIModelID struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Created int64  `json:"created"`
	OwnedBy string `json:"owned_by"`
}

// VolcengineModel 火山引擎扩展模型信息
// 火山引擎 /api/v3/models 返回的模型对象包含丰富的元数据
type VolcengineModel struct {
	ID         string `json:"id"`
	Name       string `json:"name"`    // 模型显示名称
	Version    string `json:"version"` // 版本号
	Object     string `json:"object"`
	Created    int64  `json:"created"`
	Domain     string `json:"domain"` // 领域: LLM/VLM/Embedding 等
	Status     string `json:"status"` // 状态: Active/Deprecated 等
	Modalities struct {
		InputModalities  []string `json:"input_modalities"`  // 输入模态: ["text","image"]
		OutputModalities []string `json:"output_modalities"` // 输出模态: ["text"]
	} `json:"modalities"`
	TaskType    []string `json:"task_type"` // 任务类型列表
	TokenLimits struct {
		ContextWindow           int `json:"context_window"`
		MaxInputTokenLength     int `json:"max_input_token_length"`
		MaxOutputTokenLength    int `json:"max_output_token_length"`
		MaxReasoningTokenLength int `json:"max_reasoning_token_length,omitempty"`
	} `json:"token_limits"`
	Features json.RawMessage `json:"features"` // 模型特性完整 JSON
}

// SyncFromChannel 从单个供应商接入点同步模型
// 流程:
//  1. 读取 Channel 的 ApiProtocol 和 AuthMethod
//  2. 根据协议选择发现策略 (openai/anthropic/custom)
//  3. 构建鉴权 Header 并发起 HTTP 请求
//  4. 解析返回的模型列表
//  5. 增量写入 ChannelModel + AIModel
//  6. 更新向后兼容的 Channel.Models JSON 字段
func (s *DiscoveryService) SyncFromChannel(channelID uint) (*SyncResult, error) {
	// --- 1. 查询渠道信息，预加载供应商 ---
	var channel model.Channel
	if err := s.db.Preload("Supplier").First(&channel, channelID).Error; err != nil {
		return nil, fmt.Errorf("渠道不存在: %w", err)
	}

	result := &SyncResult{
		ChannelID:   channel.ID,
		ChannelName: channel.Name,
	}

	// --- 2. 根据 ApiProtocol 选择发现策略 ---
	protocol := channel.ApiProtocol
	if protocol == "" {
		protocol = "openai_chat" // 默认协议
	}

	// custom 协议无法自动发现，需要手动配置
	if protocol == "custom" {
		result.Errors = append(result.Errors, "custom 协议不支持自动发现，请手动配置模型")
		return result, nil
	}

	// --- 3. 构建模型列表请求 URL ---
	modelsURL := s.buildModelsURL(channel.Endpoint, protocol)

	// --- 4. 构建 HTTP 请求并设置鉴权 Header ---
	req, err := http.NewRequest("GET", modelsURL, nil)
	if err != nil {
		return nil, fmt.Errorf("构建请求失败: %w", err)
	}
	s.setAuthHeaders(req, channel)

	// --- 5. 发起请求并解析响应 ---
	resp, err := s.client.Do(req)
	if err != nil {
		result.Errors = append(result.Errors, fmt.Sprintf("请求供应商API失败: %v", err))
		return result, nil
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		result.Errors = append(result.Errors, fmt.Sprintf("供应商API返回 %d: %s", resp.StatusCode, string(body)))
		return result, nil
	}

	// 读取原始响应体
	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		result.Errors = append(result.Errors, fmt.Sprintf("读取响应体失败: %v", err))
		return result, nil
	}

	// 解析外层 {"object":"list","data":[...]}
	var modelsResp openAIModelResponse
	if err := json.Unmarshal(bodyBytes, &modelsResp); err != nil {
		result.Errors = append(result.Errors, fmt.Sprintf("解析模型列表失败: %v", err))
		return result, nil
	}

	// --- 6. 根据供应商类型选择解析策略 ---
	isVolcengine := s.isVolcengineChannel(channel)

	if isVolcengine {
		// 火山引擎：使用丰富字段解析
		var volcModels []VolcengineModel
		if err := json.Unmarshal(modelsResp.Data, &volcModels); err != nil {
			result.Errors = append(result.Errors, fmt.Sprintf("解析火山引擎模型数据失败: %v", err))
			return result, nil
		}
		result.ModelsFound = len(volcModels)
		if err := s.syncVolcengineModels(channel, volcModels, result); err != nil {
			result.Errors = append(result.Errors, fmt.Sprintf("写入数据库失败: %v", err))
		}
	} else {
		// 标准 OpenAI 兼容格式（阿里云等）
		var standardModels []openAIModelID
		if err := json.Unmarshal(modelsResp.Data, &standardModels); err != nil {
			result.Errors = append(result.Errors, fmt.Sprintf("解析标准模型数据失败: %v", err))
			return result, nil
		}
		result.ModelsFound = len(standardModels)
		if err := s.syncStandardModels(channel, standardModels, result); err != nil {
			result.Errors = append(result.Errors, fmt.Sprintf("写入数据库失败: %v", err))
		}
	}

	return result, nil
}

// SyncAllActive 同步所有活跃且非 custom 协议的渠道
func (s *DiscoveryService) SyncAllActive() (*SyncAllResult, error) {
	// 查询所有状态为 active 且协议非 custom 的渠道
	var channels []model.Channel
	if err := s.db.Where("status IN ? AND api_protocol != ?",
		[]string{"active", "unverified"}, "custom").
		Find(&channels).Error; err != nil {
		return nil, fmt.Errorf("查询活跃渠道失败: %w", err)
	}

	allResult := &SyncAllResult{
		Total: len(channels),
	}

	// 逐个渠道同步
	for _, ch := range channels {
		syncResult, err := s.SyncFromChannel(ch.ID)
		if err != nil {
			// 单个渠道失败不中断整体同步
			allResult.Results = append(allResult.Results, SyncResult{
				ChannelID:   ch.ID,
				ChannelName: ch.Name,
				Errors:      []string{err.Error()},
			})
			continue
		}
		allResult.Results = append(allResult.Results, *syncResult)
	}

	// 同步完成后，停用所有未配置售价的在线模型
	s.disableModelsWithoutSellPrice()

	return allResult, nil
}

// isVolcengineChannel 判断渠道是否为火山引擎
// 通过供应商 Code 或渠道 Endpoint 判断
func (s *DiscoveryService) isVolcengineChannel(channel model.Channel) bool {
	// 优先通过供应商 Code 判断
	if channel.Supplier.Code == "volcengine" {
		return true
	}
	// 通过 Endpoint URL 判断
	endpoint := strings.ToLower(channel.Endpoint)
	return strings.Contains(endpoint, "volces.com") || strings.Contains(endpoint, "volcengineapi.com")
}

// buildModelsURL 根据协议和端点构建模型列表请求 URL
// OpenAI 系列: {Endpoint}/v1/models
// Anthropic: {Endpoint}/v1/models
func (s *DiscoveryService) buildModelsURL(endpoint, protocol string) string {
	// 去除尾部斜杠
	endpoint = strings.TrimRight(endpoint, "/")

	switch protocol {
	case "anthropic":
		// Anthropic 也支持 /v1/models 端点
		return endpoint + "/v1/models"
	default:
		// openai_chat, openai_responses 等 OpenAI 系列协议
		// endpoint 已包含版本号后缀（/v1、/v2、/v3）时，直接追加 /models
		// 例如：百度千帆 https://qianfan.baidubce.com/v2 → /v2/models
		//       火山引擎 .../api/v3 → /api/v3/models
		if strings.HasSuffix(endpoint, "/v1") ||
			strings.HasSuffix(endpoint, "/v2") ||
			strings.HasSuffix(endpoint, "/v3") {
			return endpoint + "/models"
		}
		return endpoint + "/v1/models"
	}
}

// setAuthHeaders 根据渠道的 AuthMethod 设置请求鉴权 Header
// bearer:    Authorization: Bearer <APIKey>
// x-api-key: x-api-key: <APIKey>
// custom:    <AuthHeader>: <APIKey>
func (s *DiscoveryService) setAuthHeaders(req *http.Request, channel model.Channel) {
	apiKey := channel.APIKey
	authMethod := channel.AuthMethod
	if authMethod == "" {
		authMethod = "bearer" // 默认使用 Bearer Token
	}

	switch authMethod {
	case "x-api-key":
		// Anthropic 风格鉴权
		req.Header.Set("x-api-key", apiKey)
		// Anthropic 需要额外的 version header
		req.Header.Set("anthropic-version", "2023-06-01")
	case "custom":
		// 自定义 Header 名称
		headerName := channel.AuthHeader
		if headerName == "" {
			headerName = "Authorization"
		}
		req.Header.Set(headerName, apiKey)
	default:
		// bearer 模式（默认）
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}

	req.Header.Set("Content-Type", "application/json")
}

// syncStandardModels 将标准 OpenAI 格式的模型增量写入数据库（阿里云等）
// 阿里云仅返回 id/object/created/owned_by 四字段，通过 inferModelTypeFromID 推断类型
func (s *DiscoveryService) syncStandardModels(channel model.Channel, models []openAIModelID, result *SyncResult) error {
	now := time.Now()

	// 查询该渠道已有的 ChannelModel 映射，用于去重
	var existingMappings []model.ChannelModel
	s.db.Where("channel_id = ?", channel.ID).Find(&existingMappings)

	// 构建已有映射的快速查找集合 (vendor_model_id -> true)
	existingSet := make(map[string]bool, len(existingMappings))
	for _, m := range existingMappings {
		existingSet[m.VendorModelID] = true
	}

	// 查询所有已有的 AIModel，用于判断是否需要新建
	var existingAIModels []model.AIModel
	s.db.Find(&existingAIModels)
	aiModelSet := make(map[string]bool, len(existingAIModels))
	for _, m := range existingAIModels {
		aiModelSet[m.ModelName] = true
	}

	// 用于更新 Channel.Models JSON 的模型名列表
	var channelModelNames []string

	for _, m := range models {
		vendorModelID := m.ID
		if vendorModelID == "" {
			continue // 跳过空 ID
		}

		standardModelID := vendorModelID
		channelModelNames = append(channelModelNames, vendorModelID)

		// --- 写入 ChannelModel 映射 ---
		if existingSet[vendorModelID] {
			// 已存在的映射: 更新已有 AIModel 的扩展字段
			modelName := standardModelID
			if aiModelSet[modelName] {
				updates := map[string]interface{}{
					"last_synced_at": now,
				}
				// 阿里云: 通过模型 ID 推断类型并更新（仅在当前值为默认值 LLM 时才覆盖）
				inferredType := inferModelTypeFromID(modelName)
				if inferredType != "LLM" {
					// 非 LLM 类型，更新 model_type + pricing_unit
					updates["model_type"] = inferredType
					updates["pricing_unit"] = inferPricingUnitFromID(modelName, inferredType)
				}
				if m.Created > 0 {
					updates["api_created_at"] = m.Created
				}
				s.db.Model(&model.AIModel{}).Where("model_name = ? AND (model_type = '' OR model_type = 'LLM')", modelName).Updates(updates)
			}
			result.ModelsSkipped++
			continue
		}

		// 新增 ChannelModel 记录
		cm := model.ChannelModel{
			ChannelID:       channel.ID,
			VendorModelID:   vendorModelID,
			StandardModelID: standardModelID,
			IsActive:        true,
			Source:          "auto",
		}
		if err := s.db.Create(&cm).Error; err != nil {
			// 唯一约束冲突时视为跳过
			if strings.Contains(err.Error(), "Duplicate") || strings.Contains(err.Error(), "UNIQUE") {
				result.ModelsSkipped++
				continue
			}
			result.Errors = append(result.Errors, fmt.Sprintf("写入 ChannelModel [%s] 失败: %v", vendorModelID, err))
			continue
		}

		// 自动为默认 CustomChannel 创建路由，确保新模型可被路由和检测
		s.autoCreateRouteForDefault(channel.ID, standardModelID, vendorModelID)

		// --- 写入 AIModel（新增或更新扩展字段） ---
		modelName := standardModelID

		// 跳过被批量检测标记为不可用的模型（不自动重新上线）
		if s.isModelCheckFailed(modelName) {
			result.ModelsSkipped++
			continue
		}

		if !aiModelSet[modelName] {
			// 查找默认分类ID
			var defaultCategoryID uint = 1
			var cat model.ModelCategory
			if err := s.db.First(&cat).Error; err == nil {
				defaultCategoryID = cat.ID
			}

			// 阿里云: display_name 就是完整的模型 ID
			modelType := inferModelTypeFromID(modelName)
			pricingUnit := inferPricingUnitFromID(modelName, modelType)
			// 自动入库的新模型一律默认禁用（IsActive=false），等待管理员人工审核启用，避免直接对外公开
			// 后续配置售价不会自动启用 —— 见 pricing_service.autoEnableIfNeedsSellPrice 的 source=='auto' 守卫
			isFree := pricescraper.IsFreeModel(modelName, 0, 0)
			priceMissing := !isFree && pricescraper.IsPriceMissing(modelName, pricingUnit, modelType, 0, 0)

			aiModel := model.AIModel{
				ModelName:    modelName,
				DisplayName:  modelName,
				CategoryID:   defaultCategoryID,
				SupplierID:   channel.SupplierID,
				Status:       "offline",
				Source:       "auto",
				IsActive:     false,
				LastSyncedAt: &now,
				ModelType:    modelType,
				PricingUnit:  pricingUnit,
				ApiCreatedAt: m.Created,
			}
			// 初始化 features：先继承供应商默认能力，再叠加强制流式标记
			aiModel.Features = mergeSupplierDefaultFeatures(channel.Supplier.DefaultFeatures, channel.Supplier.Code, modelName, modelType, nil, nil)
			// 基础品牌标签 + 能力衍生标签 + 定价标签 + NeedsReview 待审核标签
			baseTags := InferModelTagsWithFeatures(modelName, channel.Supplier.Code, modelType, aiModel.Features)
			tagsWithPricing := pricescraper.AugmentTagsForPricing(baseTags, isFree, priceMissing)
			aiModel.Tags = addTagToStr(tagsWithPricing, "NeedsReview")
			if err := s.db.Create(&aiModel).Error; err != nil {
				if strings.Contains(err.Error(), "Duplicate") || strings.Contains(err.Error(), "UNIQUE") {
					aiModelSet[modelName] = true
				} else {
					result.Errors = append(result.Errors, fmt.Sprintf("写入 AIModel [%s] 失败: %v", modelName, err))
				}
			} else {
				// AIModel.IsActive 字段有 GORM `default:true` tag，Create 时 false 零值会被跳过
				// 必须显式 UPDATE 才能落地 is_active=false
				s.db.Table("ai_models").Where("id = ?", aiModel.ID).Update("is_active", false)
				aiModelSet[modelName] = true
				result.NewModelIDs = append(result.NewModelIDs, aiModel.ID)
			}
		} else {
			// 已存在的 AIModel: 更新同步时间 + 推断类型（仅更新默认值）
			updates := map[string]interface{}{
				"last_synced_at": now,
			}
			inferredType := inferModelTypeFromID(modelName)
			if inferredType != "LLM" {
				updates["model_type"] = inferredType
				updates["pricing_unit"] = inferPricingUnitFromID(modelName, inferredType)
			}
			if m.Created > 0 {
				updates["api_created_at"] = m.Created
			}
			s.db.Model(&model.AIModel{}).Where("model_name = ? AND (model_type = '' OR model_type = 'LLM')", modelName).Updates(updates)
			result.ModelsUpdated++
		}

		result.ModelsAdded++
	}

	// --- 更新 Channel.Models JSON 字段（向后兼容） ---
	if len(channelModelNames) > 0 {
		modelsJSON, _ := json.Marshal(channelModelNames)
		s.db.Model(&model.Channel{}).Where("id = ?", channel.ID).Update("models", modelsJSON)
	}

	return nil
}

// syncVolcengineModels 将火山引擎丰富格式的模型增量写入数据库
// 火山引擎返回 11 个字段，直接映射到 AIModel 扩展字段
func (s *DiscoveryService) syncVolcengineModels(channel model.Channel, models []VolcengineModel, result *SyncResult) error {
	now := time.Now()

	// 查询该渠道已有的 ChannelModel 映射
	var existingMappings []model.ChannelModel
	s.db.Where("channel_id = ?", channel.ID).Find(&existingMappings)
	existingSet := make(map[string]bool, len(existingMappings))
	for _, m := range existingMappings {
		existingSet[m.VendorModelID] = true
	}

	// 查询所有已有的 AIModel
	var existingAIModels []model.AIModel
	s.db.Find(&existingAIModels)
	aiModelSet := make(map[string]bool, len(existingAIModels))
	for _, m := range existingAIModels {
		aiModelSet[m.ModelName] = true
	}

	var channelModelNames []string

	for _, m := range models {
		vendorModelID := m.ID
		if vendorModelID == "" {
			continue
		}

		// 火山引擎接入点ID(ep-xxx)无法直接映射到标准模型名
		standardModelID := vendorModelID
		if strings.HasPrefix(vendorModelID, "ep-") {
			standardModelID = ""
		}

		channelModelNames = append(channelModelNames, vendorModelID)

		// 构建火山引擎扩展字段
		volcFields := s.buildVolcengineFields(m)

		// --- 写入 ChannelModel 映射 ---
		if existingSet[vendorModelID] {
			// 已存在的映射: 更新已有 AIModel 的扩展字段（不覆盖手动编辑的数据）
			modelName := standardModelID
			if modelName == "" {
				modelName = vendorModelID
			}
			if aiModelSet[modelName] {
				s.updateExistingAIModel(modelName, volcFields, now)
				result.ModelsUpdated++
			}
			result.ModelsSkipped++
			continue
		}

		// 新增 ChannelModel 记录
		cm := model.ChannelModel{
			ChannelID:       channel.ID,
			VendorModelID:   vendorModelID,
			StandardModelID: standardModelID,
			IsActive:        true,
			Source:          "auto",
		}
		if err := s.db.Create(&cm).Error; err != nil {
			if strings.Contains(err.Error(), "Duplicate") || strings.Contains(err.Error(), "UNIQUE") {
				result.ModelsSkipped++
				continue
			}
			result.Errors = append(result.Errors, fmt.Sprintf("写入 ChannelModel [%s] 失败: %v", vendorModelID, err))
			continue
		}

		// 自动为默认 CustomChannel 创建路由
		routeAlias := standardModelID
		if routeAlias == "" {
			routeAlias = vendorModelID
		}
		s.autoCreateRouteForDefault(channel.ID, routeAlias, vendorModelID)

		// --- 写入 AIModel ---
		modelName := standardModelID
		if modelName == "" {
			modelName = vendorModelID
		}

		// 跳过被批量检测标记为不可用的模型
		if s.isModelCheckFailed(modelName) {
			result.ModelsSkipped++
			continue
		}

		if !aiModelSet[modelName] {
			var defaultCategoryID uint = 1
			var cat model.ModelCategory
			if err := s.db.First(&cat).Error; err == nil {
				defaultCategoryID = cat.ID
			}

			// 火山引擎: 直接映射丰富字段
			// 若 Volcengine 返回的 Domain 能映射到具体类型则用之，否则按名称推断
			inferredType := volcFields.modelType
			if inferredType == "" || inferredType == "LLM" {
				if guess := inferModelTypeFromID(modelName); guess != "LLM" {
					inferredType = guess
				}
			}
			pricingUnit := inferPricingUnitFromID(modelName, inferredType)
			// 自动入库的新模型一律默认禁用，等待管理员人工审核启用
			aiModel := model.AIModel{
				ModelName:        modelName,
				DisplayName:      volcFields.displayName,
				CategoryID:       defaultCategoryID,
				SupplierID:       channel.SupplierID,
				Status:           "offline",
				Source:           "auto",
				IsActive:         false,
				LastSyncedAt:     &now,
				ModelType:        inferredType,
				PricingUnit:      pricingUnit,
				Version:          volcFields.version,
				Domain:           volcFields.domain,
				TaskTypes:        volcFields.taskTypes,
				InputModalities:  volcFields.inputModalities,
				OutputModalities: volcFields.outputModalities,
				ContextWindow:    volcFields.contextWindow,
				MaxInputTokens:   volcFields.maxInputTokens,
				MaxOutputTokens:  volcFields.maxOutputTokens,
				Features:         mergeVolcengineFeatures(volcFields.features, channel.Supplier.DefaultFeatures, channel.Supplier.Code, modelName, inferredType, volcFields.inputModalities, volcFields.taskTypes),
				SupplierStatus:   volcFields.supplierStatus,
				ApiCreatedAt:     volcFields.apiCreatedAt,
			}
			aiModel.Tags = addTagToStr(
				InferModelTagsWithFeatures(modelName, channel.Supplier.Code, inferredType, aiModel.Features),
				"NeedsReview",
			)

			if err := s.db.Create(&aiModel).Error; err != nil {
				if strings.Contains(err.Error(), "Duplicate") || strings.Contains(err.Error(), "UNIQUE") {
					aiModelSet[modelName] = true
				} else {
					result.Errors = append(result.Errors, fmt.Sprintf("写入 AIModel [%s] 失败: %v", modelName, err))
				}
			} else {
				// AIModel.IsActive 字段有 GORM `default:true` tag，Create 时 false 零值会被跳过
				// 必须显式 UPDATE 才能落地 is_active=false
				s.db.Table("ai_models").Where("id = ?", aiModel.ID).Update("is_active", false)
				aiModelSet[modelName] = true
				result.NewModelIDs = append(result.NewModelIDs, aiModel.ID)
			}
		} else {
			// 已存在: 更新扩展字段
			s.updateExistingAIModel(modelName, volcFields, now)
			result.ModelsUpdated++
		}

		result.ModelsAdded++
	}

	// 更新 Channel.Models JSON
	if len(channelModelNames) > 0 {
		modelsJSON, _ := json.Marshal(channelModelNames)
		s.db.Model(&model.Channel{}).Where("id = ?", channel.ID).Update("models", modelsJSON)
	}

	return nil
}

// volcengineFields 火山引擎模型的解析后字段集合
type volcengineFields struct {
	displayName      string
	modelType        string
	version          string
	domain           string
	taskTypes        []byte // JSON
	inputModalities  []byte // JSON
	outputModalities []byte // JSON
	contextWindow    int
	maxInputTokens   int
	maxOutputTokens  int
	features         []byte // JSON
	supplierStatus   string
	apiCreatedAt     int64
}

// buildVolcengineFields 从火山引擎模型数据构建 AIModel 扩展字段
func (s *DiscoveryService) buildVolcengineFields(m VolcengineModel) volcengineFields {
	f := volcengineFields{
		displayName:     m.Name,
		version:         m.Version,
		domain:          m.Domain,
		modelType:       m.Domain, // 火山引擎 Domain 就是模型类型（LLM/VLM/Embedding 等）
		contextWindow:   m.TokenLimits.ContextWindow,
		maxInputTokens:  m.TokenLimits.MaxInputTokenLength,
		maxOutputTokens: m.TokenLimits.MaxOutputTokenLength,
		supplierStatus:  m.Status,
		apiCreatedAt:    m.Created,
	}

	// 如果 display_name 为空，使用 ID 作为兜底
	if f.displayName == "" {
		f.displayName = m.ID
	}

	// 如果 Domain 为空，默认 LLM
	if f.modelType == "" {
		f.modelType = "LLM"
	}

	// 序列化 JSON 数组字段
	if len(m.TaskType) > 0 {
		f.taskTypes, _ = json.Marshal(m.TaskType)
	}
	if len(m.Modalities.InputModalities) > 0 {
		f.inputModalities, _ = json.Marshal(m.Modalities.InputModalities)
	}
	if len(m.Modalities.OutputModalities) > 0 {
		f.outputModalities, _ = json.Marshal(m.Modalities.OutputModalities)
	}
	if len(m.Features) > 0 {
		f.features = m.Features
	}

	return f
}

// updateExistingAIModel 更新已有 AIModel 的扩展字段
// 只更新变化的字段，不覆盖手动编辑的数据（如 DisplayName 非空时不覆盖）
func (s *DiscoveryService) updateExistingAIModel(modelName string, f volcengineFields, now time.Time) {
	updates := map[string]interface{}{
		"last_synced_at": now,
	}

	// 仅在供应商返回了有效值时才更新
	if f.modelType != "" && f.modelType != "LLM" {
		updates["model_type"] = f.modelType
		updates["pricing_unit"] = inferPricingUnitFromID(modelName, f.modelType)
	}
	if f.version != "" {
		updates["version"] = f.version
	}
	if f.domain != "" {
		updates["domain"] = f.domain
	}
	if f.taskTypes != nil {
		updates["task_types"] = f.taskTypes
	}
	if f.inputModalities != nil {
		updates["input_modalities"] = f.inputModalities
	}
	if f.outputModalities != nil {
		updates["output_modalities"] = f.outputModalities
	}
	if f.contextWindow > 0 {
		updates["context_window"] = f.contextWindow
	}
	if f.maxInputTokens > 0 {
		updates["max_input_tokens"] = f.maxInputTokens
	}
	if f.maxOutputTokens > 0 {
		updates["max_output_tokens"] = f.maxOutputTokens
	}
	if f.features != nil {
		updates["features"] = f.features
	}
	if f.supplierStatus != "" {
		updates["supplier_status"] = f.supplierStatus
	}
	if f.apiCreatedAt > 0 {
		updates["api_created_at"] = f.apiCreatedAt
	}

	s.db.Model(&model.AIModel{}).Where("model_name = ?", modelName).Updates(updates)
}

// supplierBrandMap 供应商 code → 品牌标签（用于自动注入供应商品牌）
var supplierBrandMap = map[string]string{
	"openai":           "OpenAI",
	"anthropic":        "Anthropic",
	"google_gemini":    "Google",
	"azure_openai":     "OpenAI",
	"deepseek":         "DeepSeek",
	"aliyun_dashscope": "Alibaba",
	"volcengine":       "Volcengine",
	"moonshot":         "Moonshot",
	"zhipu":            "智谱GLM",
	"baidu_wenxin":     "Baidu",
	"baidu_qianfan":    "Baidu",
	"tencent_hunyuan":  "Tencent",
	"xai":              "xAI",
	"meta":             "Meta",
	"mistral":          "Mistral",
	"01ai":             "01.AI",
}

// brandKeywordRules 模型名称中的品牌关键词 → 标签列表
// 按优先级排列，长关键词在前避免短关键词误匹配
type brandRule struct {
	keyword string
	brands  []string
}

var brandKeywordRules = []brandRule{
	// 精确前缀匹配（用 prefix: 标记）
	{"prefix:o1-", []string{"OpenAI"}},
	{"prefix:o3-", []string{"OpenAI"}},
	{"prefix:o4-", []string{"OpenAI"}},
	{"prefix:yi-", []string{"01.AI"}},
	{"prefix:phi-", []string{"Microsoft"}},
	// 包含匹配
	{"deepseek", []string{"DeepSeek"}},
	{"qwen", []string{"Qwen"}},
	{"chatglm", []string{"智谱GLM"}},
	{"glm-", []string{"智谱GLM"}},
	{"glm4", []string{"智谱GLM"}},
	{"codegeex", []string{"智谱GLM"}},
	{"cogview", []string{"智谱GLM"}},
	{"cogvideo", []string{"智谱GLM"}},
	{"llama", []string{"Meta"}},
	{"baichuan", []string{"Baichuan"}},
	{"mistral", []string{"Mistral"}},
	{"mixtral", []string{"Mistral"}},
	{"codestral", []string{"Mistral"}},
	{"gemma", []string{"Google"}},
	{"gemini", []string{"Google"}},
	{"internlm", []string{"InternLM"}},
	{"moonshot", []string{"Moonshot"}},
	{"kimi", []string{"Moonshot"}},
	{"minimax", []string{"MiniMax"}},
	{"abab", []string{"MiniMax"}},
	{"ernie", []string{"Baidu"}},
	{"claude", []string{"Anthropic"}},
	{"gpt-", []string{"OpenAI"}},
	{"gpt4", []string{"OpenAI"}},
	{"dall-e", []string{"OpenAI"}},
	{"whisper", []string{"OpenAI"}},
	{"tts-", []string{"OpenAI"}},
	{"text-embedding", []string{"OpenAI"}},
	{"wan2", []string{"Alibaba"}},
	{"wan-", []string{"Alibaba"}},
	{"flux", []string{"Flux"}},
	{"stable", []string{"StabilityAI"}},
	{"tongyi", []string{"Alibaba"}},
	{"gui-", []string{"Alibaba"}},
	{"doubao", []string{"Volcengine"}},
	{"skylark", []string{"Volcengine"}},
	{"grok", []string{"xAI"}},
	{"jamba", []string{"AI21"}},
	{"command", []string{"Cohere"}},
	{"hunyuan", []string{"Tencent"}},
}

// InferModelTags 根据模型名称和供应商 code 推断完整的搜索标签
// 合并模型品牌标签 + 供应商品牌标签，去重后返回逗号分隔字符串
func InferModelTags(modelName string, supplierCode string) string {
	seen := make(map[string]bool)
	var tags []string

	addTag := func(tag string) {
		if !seen[tag] {
			seen[tag] = true
			tags = append(tags, tag)
		}
	}

	// 1. 从模型名称推断品牌标签
	nameLower := strings.ToLower(modelName)
	for _, rule := range brandKeywordRules {
		keyword := rule.keyword
		if strings.HasPrefix(keyword, "prefix:") {
			prefix := strings.TrimPrefix(keyword, "prefix:")
			if strings.HasPrefix(nameLower, prefix) {
				for _, brand := range rule.brands {
					addTag(brand)
				}
			}
		} else {
			if strings.Contains(nameLower, keyword) {
				for _, brand := range rule.brands {
					addTag(brand)
				}
			}
		}
	}

	// 2. 注入供应商品牌标签
	if supplierCode != "" {
		if brand, ok := supplierBrandMap[supplierCode]; ok {
			addTag(brand)
		}
	}

	if len(tags) == 0 {
		return ""
	}
	return strings.Join(tags, ",")
}

// inferTagsFromModelName 根据模型名称推断搜索标签（无供应商信息时使用）
func inferTagsFromModelName(modelName string) string {
	return InferModelTags(modelName, "")
}

// InferModelTagsWithFeatures 在品牌标签基础上叠加 features 能力标签
func InferModelTagsWithFeatures(modelName, supplierCode string, modelType string, features model.JSON) string {
	base := InferModelTags(modelName, supplierCode)
	seen := make(map[string]bool)
	var tags []string
	for _, t := range strings.Split(base, ",") {
		t = strings.TrimSpace(t)
		if t != "" && !seen[t] {
			seen[t] = true
			tags = append(tags, t)
		}
	}
	addTag := func(tag string) {
		if !seen[tag] {
			seen[tag] = true
			tags = append(tags, tag)
		}
	}

	// 从 features JSON 追加能力标签
	if len(features) > 0 {
		var feats map[string]interface{}
		if json.Unmarshal(features, &feats) == nil {
			if v, _ := feats["supports_thinking"].(bool); v {
				addTag("推理增强")
			}
			if v, _ := feats["supports_web_search"].(bool); v {
				addTag("联网搜索")
			}
			if v, _ := feats["supports_vision"].(bool); v {
				addTag("视觉理解")
			}
			if v, _ := feats["supports_function_call"].(bool); v {
				addTag("工具调用")
			}
			if v, _ := feats["supports_json_mode"].(bool); v {
				addTag("JSON输出")
			}
		}
	}

	// 从模型类型追加分类标签
	switch strings.ToUpper(modelType) {
	case "EMBEDDING":
		addTag("文本嵌入")
	case "VIDEOGENERATION":
		addTag("视频生成")
	case "IMAGEGENERATION":
		addTag("图像生成")
	case "TTS":
		addTag("语音合成")
	case "ASR":
		addTag("语音识别")
	}

	if len(tags) == 0 {
		return ""
	}
	return strings.Join(tags, ",")
}

// mergeSupplierDefaultFeatures 合并供应商默认能力 + 强制流式标记
// 优先级：MatchStreamOnly 覆盖 > 供应商默认值
// 若 supplierDefaults 为空且模型不需要强制流式，则返回空 JSON
func mergeSupplierDefaultFeatures(supplierDefaults model.JSON, supplierCode, modelName, modelType string, inputModalities, taskTypes model.JSON) model.JSON {
	feats := make(map[string]interface{})

	// 1. 继承供应商默认能力
	if len(supplierDefaults) > 0 {
		_ = json.Unmarshal(supplierDefaults, &feats)
	}

	// 1.5 根据模型名称推断能力（补全或覆盖默认值）
	InferFeaturesForModel(supplierCode, modelName, modelType, inputModalities, taskTypes, feats)

	// 2. 强制流式标记覆盖
	if MatchStreamOnly(modelName) {
		feats["requires_stream"] = true
	}

	if len(feats) == 0 {
		return nil
	}
	out, err := json.Marshal(feats)
	if err != nil {
		return nil
	}
	return model.JSON(out)
}

// mergeVolcengineFeatures 将火山引擎 API 返回的能力信息与供应商默认值合并
// Volcengine API 的字段优先级最高，其次是供应商默认值，最后是 MatchStreamOnly
func mergeVolcengineFeatures(volcFeatures model.JSON, supplierDefaults model.JSON, supplierCode, modelName, modelType string, inputModalities, taskTypes model.JSON) model.JSON {
	feats := make(map[string]interface{})

	// 1. 先叠入供应商默认值
	if len(supplierDefaults) > 0 {
		_ = json.Unmarshal(supplierDefaults, &feats)
	}

	// 2. Volcengine API 的能力字段覆盖默认值
	if len(volcFeatures) > 0 {
		var volcMap map[string]interface{}
		if json.Unmarshal(volcFeatures, &volcMap) == nil {
			for k, v := range volcMap {
				feats[k] = v
			}
		}
	}

	// 2.5 按模型名/类型做保守修正，避免供应商默认能力过度传播。
	InferFeaturesForModel(supplierCode, modelName, modelType, inputModalities, taskTypes, feats)

	// 3. 强制流式标记覆盖
	if MatchStreamOnly(modelName) {
		feats["requires_stream"] = true
	}

	if len(feats) == 0 {
		return nil
	}
	out, err := json.Marshal(feats)
	if err != nil {
		return nil
	}
	return model.JSON(out)
}

// autoCreateRouteForDefault 为新增的 ChannelModel 自动创建默认 CustomChannel 路由
// 确保新同步的模型能立即通过 custom_channel_routes 被检测和使用
func (s *DiscoveryService) autoCreateRouteForDefault(channelID uint, standardModelID, vendorModelID string) {
	// 查找默认 CustomChannel
	var defaultCC model.CustomChannel
	if err := s.db.Where("is_default = ? AND is_active = ?", true, true).First(&defaultCC).Error; err != nil {
		return // 无默认渠道，跳过
	}

	// 使用 FirstOrCreate 避免重复创建
	route := model.CustomChannelRoute{
		CustomChannelID: defaultCC.ID,
		AliasModel:      standardModelID,
		ChannelID:       channelID,
		ActualModel:     vendorModelID,
		Weight:          100,
		Priority:        0,
		IsActive:        true,
	}
	s.db.Where("custom_channel_id = ? AND alias_model = ? AND channel_id = ?",
		defaultCC.ID, standardModelID, channelID).
		FirstOrCreate(&route)
}

// isSupplierWithoutPriceAPI 判断供应商是否没有公开的价格 API
// 这类供应商的价格完全依赖人工维护的硬编码表，
// 新模型在未录入前应默认停用，避免按 0 元进入计费链路
func isSupplierWithoutPriceAPI(supplierCode string) bool {
	switch strings.ToLower(strings.TrimSpace(supplierCode)) {
	case "tencent_hunyuan", "hunyuan":
		return true
	default:
		return false
	}
}

// isOldDatedModel 检测模型名称是否带日期后缀，表示老旧版本
// 例如 qwen-max-1201, qwen-max-0428, qwen-plus-0806 等
func isOldDatedModel(modelName string) bool {
	name := strings.ToLower(modelName)
	// 匹配末尾 4 位数字（MMDD 格式日期后缀）
	if len(name) < 5 {
		return false
	}
	suffix := name[len(name)-4:]
	sep := name[len(name)-5]
	if sep != '-' {
		return false
	}
	for _, c := range suffix {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// inferModelTypeFromID 根据模型 ID 关键词推断模型类型
// 阿里云仅返回 4 字段，需要通过模型 ID 名称推断类型
func inferModelTypeFromID(modelID string) string {
	id := strings.ToLower(modelID)
	switch {
	case containsAny(id, "image", "wan", "seedream", "cogview", "dall-e", "gpt-image", "imagen", "hunyuan-image", "stable-diffusion"):
		return "ImageGeneration"
	case containsAny(id, "video", "seaweed", "seedance", "wanx-video", "cogvideo", "veo", "hunyuan-video"):
		return "VideoGeneration"
	case containsAny(id, "embedding", "text-embedding", "bge-large", "bge-small", "tao-8k"):
		return "Embedding"
	case containsAny(id, "tts", "cosyvoice", "speech-synthesis", "speech-02"):
		return "TTS"
	case containsAny(id, "asr", "paraformer", "sensevoice", "whisper", "recording"):
		return "ASR"
	case containsAny(id, "rerank"):
		return "Rerank"
	case containsAny(id, "vl", "omni", "-mm"):
		return "VLM"
	default:
		return "LLM"
	}
}

// inferPricingUnitFromID 根据模型 ID 和类型推断计费单位
// 返回值为 model.UnitPer* 常量，供 syncStandardModels/syncVolcengineModels 在创建/更新时写入
func inferPricingUnitFromID(modelID, modelType string) string {
	id := strings.ToLower(modelID)
	switch modelType {
	case "ImageGeneration":
		// 图像生成默认按张计费（Seedance 等特殊视频/图像混合模型由种子数据覆盖）
		return "per_image"
	case "VideoGeneration":
		// 部分视频模型按 token 计费（如 Seedance），其他按秒
		if containsAny(id, "seedance") {
			return "per_million_tokens"
		}
		return "per_second"
	case "TTS", "SpeechSynthesis", "TextToSpeech":
		// OpenAI/Qwen 系按百万字符；豆包按万字符
		if containsAny(id, "qwen", "openai", "tts-1", "speech-02", "minimax-speech") {
			return "per_million_characters"
		}
		return "per_10k_characters"
	case "ASR", "SpeechRecognition", "SpeechToText":
		// whisper 按分钟；paraformer/doubao 按小时
		if containsAny(id, "whisper") {
			return "per_minute"
		}
		return "per_hour"
	case "Rerank":
		return "per_call"
	default:
		// LLM / VLM / Embedding 默认按百万 token 计费
		return "per_million_tokens"
	}
}

// containsAny 检查 s 是否包含 substrs 中的任意一个子串
func containsAny(s string, substrs ...string) bool {
	for _, sub := range substrs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

// FetchProviderModelNames 拉取指定供应商 API 当前返回的所有模型名称
// 用于与数据库模型对比，发现可能已下线的模型
// 逻辑：查找该供应商下第一个活跃渠道，调用其 /v1/models 接口
func (s *DiscoveryService) FetchProviderModelNames(supplierID uint) ([]string, error) {
	// 查找该供应商下活跃且非 custom 协议的渠道
	var channel model.Channel
	if err := s.db.Where("supplier_id = ? AND status IN ? AND api_protocol != ?",
		supplierID,
		[]string{"active", "unverified"},
		"custom",
	).First(&channel).Error; err != nil {
		return nil, fmt.Errorf("未找到该供应商的可用渠道: %w", err)
	}

	// 构建请求 URL
	modelsURL := s.buildModelsURL(channel.Endpoint, channel.ApiProtocol)
	req, err := http.NewRequest("GET", modelsURL, nil)
	if err != nil {
		return nil, fmt.Errorf("构建请求失败: %w", err)
	}
	s.setAuthHeaders(req, channel)

	resp, err := s.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("请求供应商API失败: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("供应商API返回 %d: %s", resp.StatusCode, string(body))
	}

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("读取响应体失败: %w", err)
	}

	// 解析标准 OpenAI 兼容格式
	var modelsResp openAIModelResponse
	if err := json.Unmarshal(bodyBytes, &modelsResp); err != nil {
		return nil, fmt.Errorf("解析模型列表失败: %w", err)
	}

	// 提取模型名称（兼容标准格式和火山引擎格式）
	var names []string
	if s.isVolcengineChannel(channel) {
		var volcModels []VolcengineModel
		if err := json.Unmarshal(modelsResp.Data, &volcModels); err == nil {
			for _, m := range volcModels {
				if m.ID != "" {
					names = append(names, m.ID)
				}
			}
		}
	} else {
		var standardModels []openAIModelID
		if err := json.Unmarshal(modelsResp.Data, &standardModels); err == nil {
			for _, m := range standardModels {
				if m.ID != "" {
					names = append(names, m.ID)
				}
			}
		}
	}
	return names, nil
}

// ModelNameWithStatus 模型名称及其上游状态
type ModelNameWithStatus struct {
	Name     string
	Shutdown bool // true = 火山引擎 Shutdown/Deprecated，其他供应商不设此字段
}

// FetchProviderModelNamesWithStatus 与 FetchProviderModelNames 相同，但额外返回每个模型的下架状态
// 目前仅火山引擎会填充 Shutdown 字段（status=Shutdown/Deprecated）
func (s *DiscoveryService) FetchProviderModelNamesWithStatus(supplierID uint) ([]ModelNameWithStatus, error) {
	var channel model.Channel
	if err := s.db.Where("supplier_id = ? AND status IN ? AND api_protocol != ?",
		supplierID, []string{"active", "unverified"}, "custom",
	).First(&channel).Error; err != nil {
		return nil, fmt.Errorf("未找到该供应商的可用渠道: %w", err)
	}

	modelsURL := s.buildModelsURL(channel.Endpoint, channel.ApiProtocol)
	req, err := http.NewRequest("GET", modelsURL, nil)
	if err != nil {
		return nil, fmt.Errorf("构建请求失败: %w", err)
	}
	s.setAuthHeaders(req, channel)

	resp, err := s.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("请求供应商API失败: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("供应商API返回 %d: %s", resp.StatusCode, string(body))
	}

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("读取响应体失败: %w", err)
	}

	var modelsResp openAIModelResponse
	if err := json.Unmarshal(bodyBytes, &modelsResp); err != nil {
		return nil, fmt.Errorf("解析模型列表失败: %w", err)
	}

	var result []ModelNameWithStatus
	if s.isVolcengineChannel(channel) {
		var volcModels []VolcengineModel
		if err := json.Unmarshal(modelsResp.Data, &volcModels); err == nil {
			for _, m := range volcModels {
				if m.ID != "" {
					st := strings.ToLower(m.Status)
					result = append(result, ModelNameWithStatus{
						Name:     m.ID,
						Shutdown: st == "shutdown" || st == "deprecated",
					})
				}
			}
		}
	} else {
		var standardModels []openAIModelID
		if err := json.Unmarshal(modelsResp.Data, &standardModels); err == nil {
			for _, m := range standardModels {
				if m.ID != "" {
					result = append(result, ModelNameWithStatus{Name: m.ID})
				}
			}
		}
	}
	return result, nil
}

// disableModelsWithoutSellPrice 停用所有未配置售价（model_pricings）的启用模型。
// 被停用的模型追加 "NeedsSellPrice" 标签，管理员配置售价后自动恢复。
// 免费模型（含 "Free" 标签）豁免检查。
func (s *DiscoveryService) disableModelsWithoutSellPrice() {
	var rows []model.AIModel
	// 查找 is_active=true 且在 model_pricings 中没有记录的模型（排除免费模型）
	s.db.Select("id, tags").
		Where("is_active = true").
		Where("tags NOT LIKE '%Free%'").
		Where("id NOT IN (SELECT model_id FROM model_pricings)").
		Find(&rows)

	if len(rows) == 0 {
		return
	}

	ids := make([]uint, 0, len(rows))
	for _, m := range rows {
		ids = append(ids, m.ID)
		newTags := addTagToStr(m.Tags, "NeedsSellPrice")
		s.db.Table("ai_models").Where("id = ?", m.ID).
			Updates(map[string]interface{}{"tags": newTags})
	}
	s.db.Table("ai_models").Where("id IN ?", ids).
		Update("is_active", false)
}

// addTagToStr 向逗号分隔的标签字符串中追加一个标签（已存在则跳过）
func addTagToStr(tags, tag string) string {
	if tags == "" {
		return tag
	}
	for _, t := range strings.Split(tags, ",") {
		if strings.TrimSpace(t) == tag {
			return tags
		}
	}
	return tags + "," + tag
}

// removeTagFromStr 从逗号分隔的标签字符串中删除一个标签
func removeTagFromStr(tags, tag string) string {
	if tags == "" {
		return ""
	}
	parts := strings.Split(tags, ",")
	result := parts[:0]
	for _, t := range parts {
		if strings.TrimSpace(t) != tag {
			result = append(result, t)
		}
	}
	return strings.Join(result, ",")
}
