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

// ModelCommissionHandler 模型佣金配置管理处理器
// 超级管理员可通过此接口为不同供应商和模型设置不同代理等级的返佣比例
type ModelCommissionHandler struct {
	db *gorm.DB
}

// NewModelCommissionHandler 创建模型佣金配置处理器实例
func NewModelCommissionHandler(db *gorm.DB) *ModelCommissionHandler {
	return &ModelCommissionHandler{db: db}
}

// Register 注册模型佣金配置管理路由
// 路由组: /api/v1/admin
func (h *ModelCommissionHandler) Register(rg *gin.RouterGroup) {
	// 模型佣金配置管理
	rg.GET("/model-commissions", h.List)
	rg.POST("/model-commissions", h.Create)
	rg.GET("/model-commissions/:id", h.GetByID)
	rg.PUT("/model-commissions/:id", h.Update)
	rg.DELETE("/model-commissions/:id", h.Delete)
	rg.PATCH("/model-commissions/:id/toggle", h.ToggleStatus)
}

// List 获取模型佣金配置列表
// GET /api/v1/admin/model-commissions
// 支持分页、关键词搜索和供应商筛选
func (h *ModelCommissionHandler) List(c *gin.Context) {
	// 解析分页参数
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("pageSize", "20"))
	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > 100 {
		pageSize = 20
	}

	// 解析搜索关键词和供应商筛选
	keyword := c.Query("keyword")
	supplierIDStr := c.Query("supplier_id")

	// 构建查询，关联供应商表
	query := h.db.Model(&model.ModelCommissionConfig{}).Preload("Supplier")

	// 关键词搜索（模型名称或备注）
	if keyword != "" {
		query = query.Where("model_name LIKE ? OR remark LIKE ?", "%"+keyword+"%", "%"+keyword+"%")
	}

	// 供应商筛选
	if supplierIDStr != "" {
		if supplierID, err := strconv.ParseUint(supplierIDStr, 10, 32); err == nil {
			query = query.Where("supplier_id = ?", supplierID)
		}
	}

	// 查询总数
	var total int64
	if err := query.Count(&total).Error; err != nil {
		response.Error(c, http.StatusInternalServerError, errcode.ErrDatabase)
		return
	}

	// 查询数据
	var configs []model.ModelCommissionConfig
	offset := (page - 1) * pageSize
	if err := query.Order("created_at DESC").Offset(offset).Limit(pageSize).Find(&configs).Error; err != nil {
		response.Error(c, http.StatusInternalServerError, errcode.ErrDatabase)
		return
	}

	// 填充供应商名称（如果Preload未成功）
	for i := range configs {
		if configs[i].Supplier.ID > 0 {
			configs[i].SupplierName = configs[i].Supplier.Name
		}
	}

	response.Success(c, gin.H{
		"list":     configs,
		"total":    total,
		"page":     page,
		"pageSize": pageSize,
	})
}

// GetByID 根据ID获取模型佣金配置详情
// GET /api/v1/admin/model-commissions/:id
func (h *ModelCommissionHandler) GetByID(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 32)
	if err != nil {
		response.Error(c, http.StatusBadRequest, errcode.ErrBadRequest)
		return
	}

	var config model.ModelCommissionConfig
	if err := h.db.Preload("Supplier").First(&config, id).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			response.Error(c, http.StatusNotFound, errcode.ErrNotFound)
			return
		}
		response.Error(c, http.StatusInternalServerError, errcode.ErrDatabase)
		return
	}

	// 填充供应商名称
	if config.Supplier.ID > 0 {
		config.SupplierName = config.Supplier.Name
	}

	response.Success(c, config)
}

// CreateRequest 创建模型佣金配置请求参数
type CreateRequest struct {
	SupplierID uint    `json:"supplier_id" binding:"required"`   // 供应商ID
	ModelName  string  `json:"model_name" binding:"required"`    // 模型名称
	A0Rate     float64 `json:"a0_rate" binding:"min=0,max=1"`    // A0推广员佣金比例
	A1Rate     float64 `json:"a1_rate" binding:"min=0,max=1"`    // A1青铜佣金比例
	A2Rate     float64 `json:"a2_rate" binding:"min=0,max=1"`    // A2白银佣金比例
	A3Rate     float64 `json:"a3_rate" binding:"min=0,max=1"`    // A3黄金佣金比例
	A4Rate     float64 `json:"a4_rate" binding:"min=0,max=1"`    // A4铂金佣金比例
	IsActive   bool    `json:"is_active"`                        // 是否启用
	Remark     string  `json:"remark"`                           // 备注
}

// Create 创建模型佣金配置
// POST /api/v1/admin/model-commissions
func (h *ModelCommissionHandler) Create(c *gin.Context) {
	var req CreateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Error(c, http.StatusBadRequest, errcode.ErrValidation)
		return
	}

	// 验证供应商是否存在
	var supplier model.Supplier
	if err := h.db.First(&supplier, req.SupplierID).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			response.Error(c, http.StatusBadRequest, errcode.ErrValidation)
			return
		}
		response.Error(c, http.StatusInternalServerError, errcode.ErrDatabase)
		return
	}

	// 检查供应商ID+模型名称组合是否已存在
	var existing model.ModelCommissionConfig
	if err := h.db.Where("supplier_id = ? AND model_name = ?", req.SupplierID, req.ModelName).First(&existing).Error; err == nil {
		response.Error(c, http.StatusConflict, errcode.ErrDuplicate)
		return
	}

	// 创建配置
	config := model.ModelCommissionConfig{
		SupplierID: req.SupplierID,
		ModelName:  req.ModelName,
		A0Rate:     req.A0Rate,
		A1Rate:     req.A1Rate,
		A2Rate:     req.A2Rate,
		A3Rate:     req.A3Rate,
		A4Rate:     req.A4Rate,
		IsActive:   req.IsActive,
		Remark:     req.Remark,
	}

	if err := h.db.Create(&config).Error; err != nil {
		response.Error(c, http.StatusInternalServerError, errcode.ErrDatabase)
		return
	}

	// 填充供应商信息返回
	config.Supplier = supplier
	config.SupplierName = supplier.Name

	response.Success(c, config)
}

// UpdateRequest 更新模型佣金配置请求参数
type UpdateRequest struct {
	A0Rate   *float64 `json:"a0_rate,omitempty" binding:"omitempty,min=0,max=1"` // A0推广员佣金比例
	A1Rate   *float64 `json:"a1_rate,omitempty" binding:"omitempty,min=0,max=1"` // A1青铜佣金比例
	A2Rate   *float64 `json:"a2_rate,omitempty" binding:"omitempty,min=0,max=1"` // A2白银佣金比例
	A3Rate   *float64 `json:"a3_rate,omitempty" binding:"omitempty,min=0,max=1"` // A3黄金佣金比例
	A4Rate   *float64 `json:"a4_rate,omitempty" binding:"omitempty,min=0,max=1"` // A4铂金佣金比例
	IsActive *bool    `json:"is_active,omitempty"`                               // 是否启用
	Remark   *string  `json:"remark,omitempty"`                                  // 备注
}

// Update 更新模型佣金配置
// PUT /api/v1/admin/model-commissions/:id
func (h *ModelCommissionHandler) Update(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 32)
	if err != nil {
		response.Error(c, http.StatusBadRequest, errcode.ErrBadRequest)
		return
	}

	var req UpdateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Error(c, http.StatusBadRequest, errcode.ErrValidation)
		return
	}

	// 查询现有配置
	var config model.ModelCommissionConfig
	if err := h.db.First(&config, id).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			response.Error(c, http.StatusNotFound, errcode.ErrNotFound)
			return
		}
		response.Error(c, http.StatusInternalServerError, errcode.ErrDatabase)
		return
	}

	// 构建更新数据
	updates := make(map[string]interface{})

	if req.A0Rate != nil {
		updates["a0_rate"] = *req.A0Rate
	}
	if req.A1Rate != nil {
		updates["a1_rate"] = *req.A1Rate
	}
	if req.A2Rate != nil {
		updates["a2_rate"] = *req.A2Rate
	}
	if req.A3Rate != nil {
		updates["a3_rate"] = *req.A3Rate
	}
	if req.A4Rate != nil {
		updates["a4_rate"] = *req.A4Rate
	}
	if req.IsActive != nil {
		updates["is_active"] = *req.IsActive
	}
	if req.Remark != nil {
		updates["remark"] = *req.Remark
	}

	// 执行更新
	if len(updates) > 0 {
		if err := h.db.Model(&config).Updates(updates).Error; err != nil {
			response.Error(c, http.StatusInternalServerError, errcode.ErrDatabase)
			return
		}
	}

	// 返回更新后的数据，包含供应商信息
	h.db.Preload("Supplier").First(&config, id)
	if config.Supplier.ID > 0 {
		config.SupplierName = config.Supplier.Name
	}

	response.Success(c, config)
}

// Delete 删除模型佣金配置
// DELETE /api/v1/admin/model-commissions/:id
func (h *ModelCommissionHandler) Delete(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 32)
	if err != nil {
		response.Error(c, http.StatusBadRequest, errcode.ErrBadRequest)
		return
	}

	// 查询配置
	var config model.ModelCommissionConfig
	if err := h.db.First(&config, id).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			response.Error(c, http.StatusNotFound, errcode.ErrNotFound)
			return
		}
		response.Error(c, http.StatusInternalServerError, errcode.ErrDatabase)
		return
	}

	// 删除配置
	if err := h.db.Delete(&config).Error; err != nil {
		response.Error(c, http.StatusInternalServerError, errcode.ErrDatabase)
		return
	}

	response.Success(c, gin.H{"message": "删除成功"})
}

// ToggleStatus 切换模型佣金配置启用状态
// PATCH /api/v1/admin/model-commissions/:id/toggle
func (h *ModelCommissionHandler) ToggleStatus(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 32)
	if err != nil {
		response.Error(c, http.StatusBadRequest, errcode.ErrBadRequest)
		return
	}

	// 查询配置
	var config model.ModelCommissionConfig
	if err := h.db.First(&config, id).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			response.Error(c, http.StatusNotFound, errcode.ErrNotFound)
			return
		}
		response.Error(c, http.StatusInternalServerError, errcode.ErrDatabase)
		return
	}

	// 切换状态
	newStatus := !config.IsActive
	if err := h.db.Model(&config).Update("is_active", newStatus).Error; err != nil {
		response.Error(c, http.StatusInternalServerError, errcode.ErrDatabase)
		return
	}

	response.Success(c, gin.H{
		"id":        config.ID,
		"is_active": newStatus,
		"message":   "状态切换成功",
	})
}
