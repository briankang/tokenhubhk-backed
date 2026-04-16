package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"tokenhub-server/internal/model"
	"tokenhub-server/internal/pkg/errcode"
	"tokenhub-server/internal/pkg/response"
	"tokenhub-server/internal/service/aimodel"
	"tokenhub-server/internal/service/audit"
	"tokenhub-server/internal/service/modeldiscovery"
	"tokenhub-server/internal/taskqueue"
)

// ModelSyncHandler 模型同步管理接口处理器
// 提供模型自动发现、渠道-模型映射查询和编辑功能。
// 当 bridge 不为 nil 时，SyncAll 等重操作委派给 Worker 执行。
type ModelSyncHandler struct {
	discoveryService *modeldiscovery.DiscoveryService
	db               *gorm.DB
	auditService     *audit.AuditService
	modelChecker     *aimodel.ModelChecker
	bridge           *taskqueue.SSEBridge // nil=单体模式，非nil=委派模式
}

// NewModelSyncHandler 创建模型同步处理器实例
func NewModelSyncHandler(db *gorm.DB, bridge ...*taskqueue.SSEBridge) *ModelSyncHandler {
	h := &ModelSyncHandler{
		discoveryService: modeldiscovery.NewDiscoveryService(db),
		db:               db,
		auditService:     audit.NewAuditService(db),
		modelChecker:     aimodel.NewModelChecker(db),
	}
	if len(bridge) > 0 {
		h.bridge = bridge[0]
	}
	return h
}

// syncAllResponse 同步全部模型的响应（含检测结果）
type syncAllResponse struct {
	Results         []modeldiscovery.SyncResult `json:"results"`
	Total           int                         `json:"total"`
	ModelsChecked   int                         `json:"models_checked"`
	ModelsAvailable int                         `json:"models_available"`
	ModelsUnavailable int                       `json:"models_unavailable"`
}

// SyncAll 全量同步所有活跃接入点的模型
// POST /api/v1/admin/models/sync
// 遍历所有 active 状态的渠道，调用供应商 /v1/models API 拉取模型列表
// 同步完成后自动检测新增模型可用性，并写入审计日志
func (h *ModelSyncHandler) SyncAll(c *gin.Context) {
	// 三服务模式：委派给 Worker
	if h.bridge != nil {
		result, err := h.bridge.PublishAndWait(c.Request.Context(), taskqueue.TaskModelSyncAll, nil)
		if err != nil {
			response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, err.Error())
			return
		}
		response.Success(c, json.RawMessage(result.Data))
		return
	}

	// 单体模式：本地执行
	result, err := h.discoveryService.SyncAllActive()
	if err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, err.Error())
		return
	}

	resp := syncAllResponse{
		Results: result.Results,
		Total:   result.Total,
	}

	// 收集所有新增模型 ID
	var newModelIDs []uint
	totalAdded := 0
	totalFound := 0
	for _, r := range result.Results {
		newModelIDs = append(newModelIDs, r.NewModelIDs...)
		totalAdded += r.ModelsAdded
		totalFound += r.ModelsFound
	}

	// 对新增模型做增量可用性检测
	if len(newModelIDs) > 0 {
		checkResults, checkErr := h.modelChecker.CheckByIDs(context.Background(), newModelIDs, nil)
		if checkErr == nil && len(checkResults) > 0 {
			resp.ModelsChecked = len(checkResults)
			for _, cr := range checkResults {
				if cr.Available {
					resp.ModelsAvailable++
				} else {
					resp.ModelsUnavailable++
				}
			}
		}
	}

	// 写入审计日志
	operatorID, _ := c.Get("userId")
	var uid uint
	if id, ok := operatorID.(uint); ok {
		uid = id
	}
	details := map[string]interface{}{
		"channels_synced": result.Total,
		"models_found":    totalFound,
		"models_added":    totalAdded,
		"models_checked":  resp.ModelsChecked,
		"models_available": resp.ModelsAvailable,
	}
	detailsJSON, _ := json.Marshal(details)
	auditLog := &model.AuditLog{
		UserID:     uid,
		OperatorID: uid,
		Action:     "SYNC",
		Resource:   "MODEL",
		Details:    detailsJSON,
		IP:         c.ClientIP(),
		RequestID:  c.GetString("requestId"),
		Remark:     fmt.Sprintf("同步%d个渠道，发现%d个模型，新增%d个", result.Total, totalFound, totalAdded),
	}
	_ = h.auditService.Create(c.Request.Context(), auditLog)

	response.Success(c, resp)
}

// SyncByChannel 单个渠道同步模型
// POST /api/v1/admin/models/sync/:channelId
// 根据指定的 channelId 调用对应供应商的模型列表 API
func (h *ModelSyncHandler) SyncByChannel(c *gin.Context) {
	channelID, err := strconv.ParseUint(c.Param("channelId"), 10, 64)
	if err != nil || channelID == 0 {
		response.Error(c, http.StatusBadRequest, errcode.ErrValidation)
		return
	}

	// 三服务模式：委派给 Worker
	if h.bridge != nil {
		payload := taskqueue.ModelSyncPayload{ChannelID: uint(channelID)}
		result, err := h.bridge.PublishAndWait(c.Request.Context(), taskqueue.TaskModelSync, payload)
		if err != nil {
			response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, err.Error())
			return
		}
		response.Success(c, json.RawMessage(result.Data))
		return
	}

	// 单体模式
	result, err := h.discoveryService.SyncFromChannel(uint(channelID))
	if err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, err.Error())
		return
	}

	response.Success(c, result)
}

// ListChannelModels 获取渠道-模型映射列表
// GET /api/v1/admin/channel-models
// 支持查询参数:
//   - channel_id: 按渠道ID筛选
//   - standard_model_id: 按标准模型ID筛选
//   - source: 按来源筛选 (auto/manual)
//   - page: 页码，默认1
//   - page_size: 每页数量，默认20
func (h *ModelSyncHandler) ListChannelModels(c *gin.Context) {
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "20"))

	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > 2000 {
		pageSize = 20
	}

	// 构建查询条件
	query := h.db.Model(&model.ChannelModel{}).Preload("Channel").Preload("Channel.Supplier")

	// 按渠道ID筛选
	if channelID := c.Query("channel_id"); channelID != "" {
		query = query.Where("channel_id = ?", channelID)
	}

	// 按供应商ID筛选（通过 channels 表 join）
	if supplierID := c.Query("supplier_id"); supplierID != "" {
		query = query.Joins("JOIN channels ON channels.id = channel_models.channel_id AND channels.deleted_at IS NULL").
			Where("channels.supplier_id = ?", supplierID)
	}

	// 按标准模型ID筛选
	if standardModelID := c.Query("standard_model_id"); standardModelID != "" {
		query = query.Where("standard_model_id LIKE ?", "%"+standardModelID+"%")
	}

	// 按来源筛选
	if source := c.Query("source"); source != "" {
		query = query.Where("source = ?", source)
	}

	// 查询总数
	var total int64
	query.Count(&total)

	// 分页查询
	var mappings []model.ChannelModel
	offset := (page - 1) * pageSize
	if err := query.Offset(offset).Limit(pageSize).Order("id DESC").Find(&mappings).Error; err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, err.Error())
		return
	}

	response.PageResult(c, mappings, total, page, pageSize)
}

// BatchUpdateModelStatus 批量修改模型状态（上线/下线）
// PUT /api/v1/admin/models/batch-status
// 请求体: { "ids": [1,2,3], "is_active": true/false }
func (h *ModelSyncHandler) BatchUpdateModelStatus(c *gin.Context) {
	var req struct {
		IDs      []uint `json:"ids" binding:"required,min=1"`
		IsActive *bool  `json:"is_active" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Error(c, http.StatusBadRequest, errcode.ErrValidation)
		return
	}

	// 批量更新 is_active 字段
	tx := h.db.Model(&model.AIModel{}).Where("id IN ?", req.IDs).Update("is_active", *req.IsActive)
	if tx.Error != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrDatabase.Code, tx.Error.Error())
		return
	}

	response.Success(c, gin.H{
		"message":  "批量更新成功",
		"affected": tx.RowsAffected,
	})
}

// BatchDeleteModels 批量删除模型（软删除）
// DELETE /api/v1/admin/models/batch-delete
// 请求体: { "ids": [1,2,3] }
func (h *ModelSyncHandler) BatchDeleteModels(c *gin.Context) {
	var req struct {
		IDs []uint `json:"ids" binding:"required,min=1"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Error(c, http.StatusBadRequest, errcode.ErrValidation)
		return
	}

	// 批量软删除
	tx := h.db.Where("id IN ?", req.IDs).Delete(&model.AIModel{})
	if tx.Error != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrDatabase.Code, tx.Error.Error())
		return
	}

	response.Success(c, gin.H{
		"message":  "批量删除成功",
		"affected": tx.RowsAffected,
	})
}

// UpdateChannelModel 编辑渠道-模型映射
// PUT /api/v1/admin/channel-models/:id
// 主要用于火山引擎 ep-xxx 映射到标准模型ID
// 请求体: { "standard_model_id": "deepseek-r1", "is_active": true }
func (h *ModelSyncHandler) UpdateChannelModel(c *gin.Context) {
	// 解析映射记录 ID
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || id == 0 {
		response.Error(c, http.StatusBadRequest, errcode.ErrValidation)
		return
	}

	// 检查记录是否存在
	var existing model.ChannelModel
	if err := h.db.First(&existing, id).Error; err != nil {
		response.ErrorMsg(c, http.StatusNotFound, errcode.ErrNotFound.Code, "渠道-模型映射不存在")
		return
	}

	// 解析更新字段
	var updates map[string]interface{}
	if err := c.ShouldBindJSON(&updates); err != nil {
		response.Error(c, http.StatusBadRequest, errcode.ErrValidation)
		return
	}

	// 只允许更新指定字段，防止越权修改
	allowedFields := map[string]bool{
		"standard_model_id": true,
		"vendor_model_id":   true,
		"is_active":         true,
	}
	safeUpdates := make(map[string]interface{})
	for k, v := range updates {
		if allowedFields[k] {
			safeUpdates[k] = v
		}
	}

	if len(safeUpdates) == 0 {
		response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrValidation.Code, "无有效更新字段")
		return
	}

	// 执行更新
	if err := h.db.Model(&model.ChannelModel{}).Where("id = ?", id).Updates(safeUpdates).Error; err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrDatabase.Code, err.Error())
		return
	}

	response.Success(c, gin.H{"message": "updated"})
}

// BatchUpdateSellingPrice 批量修改模型售价
// PUT /api/v1/admin/models/batch-selling-price
// 请求体: { "ids": [1,2,3], "discount": 0.9 }
// discount: 基于官方成本价的折扣比例，如 0.9 表示9折，0.85 表示85折
func (h *ModelSyncHandler) BatchUpdateSellingPrice(c *gin.Context) {
	var req struct {
		IDs      []uint  `json:"ids" binding:"required,min=1"`
		Discount float64 `json:"discount" binding:"required,gt=0,lte=10"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Error(c, http.StatusBadRequest, errcode.ErrValidation)
		return
	}

	// 查询选中模型的成本价
	var models []model.AIModel
	if err := h.db.Where("id IN ?", req.IDs).Find(&models).Error; err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrDatabase.Code, err.Error())
		return
	}

	updated := 0
	skipped := 0
	for _, m := range models {
		// 跳过成本价为0的模型，避免创建无意义的0元售价记录
		if m.InputCostRMB == 0 && m.OutputCostRMB == 0 {
			skipped++
			continue
		}

		sellingInputRMB := float64(int(m.InputCostRMB*req.Discount*10000+0.5)) / 10000
		sellingOutputRMB := float64(int(m.OutputCostRMB*req.Discount*10000+0.5)) / 10000

		var pricing model.ModelPricing
		err := h.db.Where("model_id = ?", m.ID).First(&pricing).Error
		if err != nil {
			pricing = model.ModelPricing{ModelID: m.ID}
		}
		pricing.InputPriceRMB = sellingInputRMB
		pricing.InputPricePerToken = int64(sellingInputRMB * 10000)
		pricing.OutputPriceRMB = sellingOutputRMB
		pricing.OutputPricePerToken = int64(sellingOutputRMB * 10000)

		if pricing.ID == 0 {
			h.db.Create(&pricing)
		} else {
			h.db.Save(&pricing)
		}
		updated++
	}

	invalidatePublicModelsCache()
	response.Success(c, gin.H{
		"message": "批量售价更新成功",
		"updated": updated,
		"skipped": skipped,
	})
}

// FillSellingPrices 一键补全售价
// POST /api/v1/admin/models/fill-selling-prices
// 请求体: { "discount": 0.9 } — 可选，默认0.9（9折）
// 为所有有成本价但无有效售价的模型自动创建 ModelPricing 记录
func (h *ModelSyncHandler) FillSellingPrices(c *gin.Context) {
	var req struct {
		Discount float64 `json:"discount"`
	}
	_ = c.ShouldBindJSON(&req)
	if req.Discount <= 0 || req.Discount > 10 {
		req.Discount = 0.9
	}

	// 查找有成本价但无有效售价的模型：
	// 1) 无 ModelPricing 记录
	// 2) 有 ModelPricing 记录但 input_price_rmb = 0 且 output_price_rmb = 0
	var models []model.AIModel
	err := h.db.
		Where("(input_cost_rmb > 0 OR output_cost_rmb > 0)").
		Preload("Pricing").
		Find(&models).Error
	if err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrDatabase.Code, err.Error())
		return
	}

	created := 0
	updated := 0
	for _, m := range models {
		needsFill := m.Pricing == nil ||
			(m.Pricing.InputPriceRMB == 0 && m.Pricing.OutputPriceRMB == 0)
		if !needsFill {
			continue
		}

		sellingInputRMB := float64(int(m.InputCostRMB*req.Discount*10000+0.5)) / 10000
		sellingOutputRMB := float64(int(m.OutputCostRMB*req.Discount*10000+0.5)) / 10000

		if m.Pricing == nil {
			// 创建新记录
			pricing := model.ModelPricing{
				ModelID:             m.ID,
				InputPriceRMB:       sellingInputRMB,
				InputPricePerToken:  int64(sellingInputRMB * 10000),
				OutputPriceRMB:      sellingOutputRMB,
				OutputPricePerToken: int64(sellingOutputRMB * 10000),
			}
			h.db.Create(&pricing)
			created++
		} else {
			// 更新已有的0值记录
			m.Pricing.InputPriceRMB = sellingInputRMB
			m.Pricing.InputPricePerToken = int64(sellingInputRMB * 10000)
			m.Pricing.OutputPriceRMB = sellingOutputRMB
			m.Pricing.OutputPricePerToken = int64(sellingOutputRMB * 10000)
			h.db.Save(m.Pricing)
			updated++
		}
	}

	invalidatePublicModelsCache()
	response.Success(c, gin.H{
		"message":  fmt.Sprintf("售价补全完成：新建 %d 条，更新 %d 条", created, updated),
		"created":  created,
		"updated":  updated,
		"discount": req.Discount,
	})
}

// BatchUpdateDiscount 批量修改模型独立折扣
// PUT /api/v1/admin/models/batch-discount
// 请求体: { "ids": [1,2,3], "discount": 0.85 }
// discount: 模型独立折扣（0=清除，恢复继承供应商折扣；>0 如0.85=85折）
func (h *ModelSyncHandler) BatchUpdateDiscount(c *gin.Context) {
	var req struct {
		IDs      []uint  `json:"ids" binding:"required,min=1"`
		Discount float64 `json:"discount" binding:"min=0,lte=10"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Error(c, http.StatusBadRequest, errcode.ErrValidation)
		return
	}

	result := h.db.Model(&model.AIModel{}).
		Where("id IN ?", req.IDs).
		Update("discount", req.Discount)
	if result.Error != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrDatabase.Code, result.Error.Error())
		return
	}

	response.Success(c, gin.H{
		"message": "批量折扣更新成功",
		"updated": result.RowsAffected,
	})
}
