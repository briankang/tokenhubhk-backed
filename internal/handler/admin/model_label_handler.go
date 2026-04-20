package admin

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"tokenhub-server/internal/model"
	"tokenhub-server/internal/pkg/errcode"
	"tokenhub-server/internal/pkg/response"
)

// ModelLabelHandler 模型 k:v 标签管理处理器
type ModelLabelHandler struct {
	db *gorm.DB
}

// NewModelLabelHandler 创建 ModelLabelHandler 实例
func NewModelLabelHandler(db *gorm.DB) *ModelLabelHandler {
	return &ModelLabelHandler{db: db}
}

// upsertLabelReq 单个标签新增/更新请求
type upsertLabelReq struct {
	LabelKey   string `json:"label_key"   binding:"required,max=50"`
	LabelValue string `json:"label_value" binding:"required,max=100"`
}

// removeLabelReq 单个标签删除请求
type removeLabelReq struct {
	LabelKey   string `json:"label_key"   binding:"required,max=50"`
	LabelValue string `json:"label_value" binding:"required,max=100"`
}

// batchAssignLabelReq 批量添加标签请求
type batchAssignLabelReq struct {
	ModelIDs   []uint `json:"model_ids"   binding:"required,min=1"`
	LabelKey   string `json:"label_key"   binding:"required,max=50"`
	LabelValue string `json:"label_value" binding:"required,max=100"`
}

// batchRemoveLabelReq 批量删除标签请求
type batchRemoveLabelReq struct {
	ModelIDs []uint `json:"model_ids" binding:"required,min=1"`
	LabelKey string `json:"label_key" binding:"required,max=50"`
}

// ListByModel GET /admin/ai-models/:id/labels
// 获取指定模型的所有标签
func (h *ModelLabelHandler) ListByModel(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrBadRequest.Code, "invalid model id")
		return
	}

	var labels []model.ModelLabel
	if err := h.db.Where("model_id = ?", uint(id)).Find(&labels).Error; err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, err.Error())
		return
	}

	response.Success(c, labels)
}

// Upsert POST /admin/ai-models/:id/labels
// 为模型添加/更新一个 k:v 标签（已存在则跳过）
func (h *ModelLabelHandler) Upsert(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrBadRequest.Code, "invalid model id")
		return
	}

	var req upsertLabelReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrBadRequest.Code, err.Error())
		return
	}

	label := model.ModelLabel{
		ModelID:    uint(id),
		LabelKey:   req.LabelKey,
		LabelValue: req.LabelValue,
	}
	if err := h.db.FirstOrCreate(&label, model.ModelLabel{
		ModelID:    uint(id),
		LabelKey:   req.LabelKey,
		LabelValue: req.LabelValue,
	}).Error; err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, err.Error())
		return
	}

	invalidatePublicModelsCache()
	response.Success(c, label)
}

// Remove DELETE /admin/ai-models/:id/labels
// 删除模型上的指定 k:v 标签
func (h *ModelLabelHandler) Remove(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrBadRequest.Code, "invalid model id")
		return
	}

	var req removeLabelReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrBadRequest.Code, err.Error())
		return
	}

	// 使用 Unscoped 硬删除，避免软删除残留行占用 uidx_model_label 唯一索引，
	// 导致用户删除后无法重新添加同一标签
	if err := h.db.Unscoped().Where("model_id = ? AND label_key = ? AND label_value = ?",
		uint(id), req.LabelKey, req.LabelValue).
		Delete(&model.ModelLabel{}).Error; err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, err.Error())
		return
	}

	invalidatePublicModelsCache()
	response.Success(c, nil)
}

// BatchAssign POST /admin/models/batch-labels
// 批量为多个模型添加同一个 k:v 标签
func (h *ModelLabelHandler) BatchAssign(c *gin.Context) {
	var req batchAssignLabelReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrBadRequest.Code, err.Error())
		return
	}

	tx := h.db.Begin()
	affected := 0
	for _, mid := range req.ModelIDs {
		label := model.ModelLabel{
			ModelID:    mid,
			LabelKey:   req.LabelKey,
			LabelValue: req.LabelValue,
		}
		result := tx.FirstOrCreate(&label, model.ModelLabel{
			ModelID:    mid,
			LabelKey:   req.LabelKey,
			LabelValue: req.LabelValue,
		})
		if result.Error != nil {
			tx.Rollback()
			response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, result.Error.Error())
			return
		}
		if result.RowsAffected > 0 {
			affected++
		}
	}
	if err := tx.Commit().Error; err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, err.Error())
		return
	}

	invalidatePublicModelsCache()
	response.Success(c, gin.H{"affected": affected, "total": len(req.ModelIDs)})
}

// BatchRemove DELETE /admin/models/batch-labels
// 批量移除多个模型上的指定 label_key（不限值）
func (h *ModelLabelHandler) BatchRemove(c *gin.Context) {
	var req batchRemoveLabelReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrBadRequest.Code, err.Error())
		return
	}

	// 硬删除：避免软删除残留占用唯一索引，导致后续重新打标冲突
	result := h.db.Unscoped().Where("model_id IN ? AND label_key = ?", req.ModelIDs, req.LabelKey).
		Delete(&model.ModelLabel{})
	if result.Error != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, result.Error.Error())
		return
	}

	invalidatePublicModelsCache()
	response.Success(c, gin.H{"affected": result.RowsAffected})
}

// ListKeys GET /admin/models/label-keys
// 返回当前所有已使用的标签键（用于前端自动补全）
func (h *ModelLabelHandler) ListKeys(c *gin.Context) {
	var keys []string
	if err := h.db.Model(&model.ModelLabel{}).
		Distinct("label_key").
		Order("label_key").
		Pluck("label_key", &keys).Error; err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, err.Error())
		return
	}

	// value-only 简化版：所有标签统一使用 "tag" 作为 key
	systemKeys := []string{"tag"}
	seen := make(map[string]bool)
	for _, k := range keys {
		seen[k] = true
	}
	result := []string{}
	for _, k := range systemKeys {
		if !seen[k] {
			result = append(result, k)
			seen[k] = true
		}
	}
	result = append(result, keys...)

	response.Success(c, result)
}
