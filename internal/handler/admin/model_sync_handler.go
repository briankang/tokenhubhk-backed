package admin

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"tokenhub-server/internal/model"
	"tokenhub-server/internal/pkg/errcode"
	"tokenhub-server/internal/pkg/response"
	"tokenhub-server/internal/service/modeldiscovery"
)

// ModelSyncHandler 模型同步管理接口处理器
// 提供模型自动发现、渠道-模型映射查询和编辑功能
type ModelSyncHandler struct {
	discoveryService *modeldiscovery.DiscoveryService
	db               *gorm.DB
}

// NewModelSyncHandler 创建模型同步处理器实例
func NewModelSyncHandler(db *gorm.DB) *ModelSyncHandler {
	return &ModelSyncHandler{
		discoveryService: modeldiscovery.NewDiscoveryService(db),
		db:               db,
	}
}

// SyncAll 全量同步所有活跃接入点的模型
// POST /api/v1/admin/models/sync
// 遍历所有 active 状态的渠道，调用供应商 /v1/models API 拉取模型列表
func (h *ModelSyncHandler) SyncAll(c *gin.Context) {
	result, err := h.discoveryService.SyncAllActive()
	if err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, err.Error())
		return
	}

	response.Success(c, result)
}

// SyncByChannel 单个渠道同步模型
// POST /api/v1/admin/models/sync/:channelId
// 根据指定的 channelId 调用对应供应商的模型列表 API
func (h *ModelSyncHandler) SyncByChannel(c *gin.Context) {
	// 解析渠道 ID
	channelID, err := strconv.ParseUint(c.Param("channelId"), 10, 64)
	if err != nil || channelID == 0 {
		response.Error(c, http.StatusBadRequest, errcode.ErrValidation)
		return
	}

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
	if pageSize < 1 || pageSize > 100 {
		pageSize = 20
	}

	// 构建查询条件
	query := h.db.Model(&model.ChannelModel{}).Preload("Channel").Preload("Channel.Supplier")

	// 按渠道ID筛选
	if channelID := c.Query("channel_id"); channelID != "" {
		query = query.Where("channel_id = ?", channelID)
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
