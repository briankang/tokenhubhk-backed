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

// SyncResult 单个渠道的同步结果
type SyncResult struct {
	ChannelID     uint     `json:"channel_id"`
	ChannelName   string   `json:"channel_name"`
	ModelsFound   int      `json:"models_found"`   // 从供应商API发现的模型总数
	ModelsAdded   int      `json:"models_added"`   // 新增写入的模型数
	ModelsUpdated int      `json:"models_updated"` // 更新已有模型数
	ModelsSkipped int      `json:"models_skipped"` // 已存在跳过的模型数
	Errors        []string `json:"errors,omitempty"`
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
	ID      string `json:"id"`
	Name    string `json:"name"`    // 模型显示名称
	Version string `json:"version"` // 版本号
	Object  string `json:"object"`
	Created int64  `json:"created"`
	Domain  string `json:"domain"` // 领域: LLM/VLM/Embedding 等
	Status  string `json:"status"` // 状态: Active/Deprecated 等
	Modalities struct {
		InputModalities  []string `json:"input_modalities"`  // 输入模态: ["text","image"]
		OutputModalities []string `json:"output_modalities"` // 输出模态: ["text"]
	} `json:"modalities"`
	TaskType    []string        `json:"task_type"`    // 任务类型列表
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
		// 如果 endpoint 已经包含 /v1，不再追加
		if strings.HasSuffix(endpoint, "/v1") {
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
					// 非 LLM 类型，更新 model_type
					updates["model_type"] = inferredType
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

		// --- 写入 AIModel（新增或更新扩展字段） ---
		modelName := standardModelID
		if !aiModelSet[modelName] {
			// 查找默认分类ID
			var defaultCategoryID uint = 1
			var cat model.ModelCategory
			if err := s.db.First(&cat).Error; err == nil {
				defaultCategoryID = cat.ID
			}

			// 阿里云: display_name 就是完整的模型 ID
			aiModel := model.AIModel{
				ModelName:      modelName,
				DisplayName:    modelName,
				CategoryID:     defaultCategoryID,
				SupplierID:     channel.SupplierID,
				Status:         "offline",
				Source:         "auto",
				IsActive:       true,
				LastSyncedAt:   &now,
				ModelType:      inferModelTypeFromID(modelName), // 根据 ID 关键词推断类型
				ApiCreatedAt:   m.Created,
			}
			if err := s.db.Create(&aiModel).Error; err != nil {
				if strings.Contains(err.Error(), "Duplicate") || strings.Contains(err.Error(), "UNIQUE") {
					aiModelSet[modelName] = true
				} else {
					result.Errors = append(result.Errors, fmt.Sprintf("写入 AIModel [%s] 失败: %v", modelName, err))
				}
			} else {
				aiModelSet[modelName] = true
			}
		} else {
			// 已存在的 AIModel: 更新同步时间 + 推断类型（仅更新默认值）
			updates := map[string]interface{}{
				"last_synced_at": now,
			}
			inferredType := inferModelTypeFromID(modelName)
			if inferredType != "LLM" {
				updates["model_type"] = inferredType
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

		// --- 写入 AIModel ---
		modelName := standardModelID
		if modelName == "" {
			modelName = vendorModelID
		}

		if !aiModelSet[modelName] {
			var defaultCategoryID uint = 1
			var cat model.ModelCategory
			if err := s.db.First(&cat).Error; err == nil {
				defaultCategoryID = cat.ID
			}

			// 火山引擎: 直接映射丰富字段
			aiModel := model.AIModel{
				ModelName:        modelName,
				DisplayName:      volcFields.displayName,
				CategoryID:       defaultCategoryID,
				SupplierID:       channel.SupplierID,
				Status:           "offline",
				Source:           "auto",
				IsActive:         true,
				LastSyncedAt:     &now,
				ModelType:        volcFields.modelType,
				Version:          volcFields.version,
				Domain:           volcFields.domain,
				TaskTypes:        volcFields.taskTypes,
				InputModalities:  volcFields.inputModalities,
				OutputModalities: volcFields.outputModalities,
				ContextWindow:    volcFields.contextWindow,
				MaxInputTokens:   volcFields.maxInputTokens,
				MaxOutputTokens:  volcFields.maxOutputTokens,
				Features:         volcFields.features,
				SupplierStatus:   volcFields.supplierStatus,
				ApiCreatedAt:     volcFields.apiCreatedAt,
			}

			if err := s.db.Create(&aiModel).Error; err != nil {
				if strings.Contains(err.Error(), "Duplicate") || strings.Contains(err.Error(), "UNIQUE") {
					aiModelSet[modelName] = true
				} else {
					result.Errors = append(result.Errors, fmt.Sprintf("写入 AIModel [%s] 失败: %v", modelName, err))
				}
			} else {
				aiModelSet[modelName] = true
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
		displayName:    m.Name,
		version:        m.Version,
		domain:         m.Domain,
		modelType:      m.Domain, // 火山引擎 Domain 就是模型类型（LLM/VLM/Embedding 等）
		contextWindow:  m.TokenLimits.ContextWindow,
		maxInputTokens: m.TokenLimits.MaxInputTokenLength,
		maxOutputTokens: m.TokenLimits.MaxOutputTokenLength,
		supplierStatus: m.Status,
		apiCreatedAt:   m.Created,
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

// inferModelTypeFromID 根据模型 ID 关键词推断模型类型
// 阿里云仅返回 4 字段，需要通过模型 ID 名称推断类型
func inferModelTypeFromID(modelID string) string {
	id := strings.ToLower(modelID)
	switch {
	case containsAny(id, "image", "wan"):
		return "ImageGeneration"
	case containsAny(id, "video", "seaweed"):
		return "VideoGeneration"
	case containsAny(id, "embedding", "text-embedding"):
		return "Embedding"
	case containsAny(id, "tts", "cosyvoice", "speech-synthesis"):
		return "SpeechSynthesis"
	case containsAny(id, "asr", "paraformer", "sensevoice"):
		return "SpeechRecognition"
	case containsAny(id, "vl", "omni", "-mm"):
		return "VLM"
	default:
		return "LLM"
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
