package admin

import (
	"math"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"tokenhub-server/internal/model"
	"tokenhub-server/internal/pkg/errcode"
	"tokenhub-server/internal/pkg/response"
)

// CustomChannelHandler 自定义渠道管理接口处理器
type CustomChannelHandler struct {
	db *gorm.DB
}

// NewCustomChannelHandler 创建自定义渠道管理Handler实例
func NewCustomChannelHandler(db *gorm.DB) *CustomChannelHandler {
	return &CustomChannelHandler{db: db}
}

// -------- 请求体结构 --------

// createCustomChannelReq 创建自定义渠道请求体
type createCustomChannelReq struct {
	Name        string `json:"name" binding:"required"`
	Description string `json:"description"`
	Strategy    string `json:"strategy"`
	IsDefault   bool   `json:"is_default"`
	AutoRoute   bool   `json:"auto_route"`
	Visibility  string `json:"visibility"`
}

// updateCustomChannelReq 更新自定义渠道请求体
type updateCustomChannelReq struct {
	Name        *string `json:"name"`
	Description *string `json:"description"`
	Strategy    *string `json:"strategy"`
	IsDefault   *bool   `json:"is_default"`
	AutoRoute   *bool   `json:"auto_route"`
	Visibility  *string `json:"visibility"`
}

// updateAccessReq 更新访问控制列表请求体
type updateAccessReq struct {
	UserIDs []uint `json:"user_ids"`
}

// batchRouteReq 单条路由规则
type batchRouteReq struct {
	AliasModel  string `json:"alias_model" binding:"required"`
	ChannelID   uint   `json:"channel_id" binding:"required"`
	ActualModel string `json:"actual_model" binding:"required"`
	Weight      int    `json:"weight"`
	Priority    int    `json:"priority"`
}

// batchRoutesReq 批量设置路由请求体
type batchRoutesReq struct {
	Routes []batchRouteReq `json:"routes" binding:"required"`
}

// importRoutesReq 导入路由请求体
type importRoutesReq struct {
	ChannelID uint `json:"channel_id" binding:"required"`
}

// -------- Handler 方法 --------

// List 获取自定义渠道列表
// GET /api/v1/admin/custom-channels
// 支持查询参数: page, page_size, search(名称搜索), is_default, visibility
// 预加载 Routes(含 Channel.Supplier) + AccessList
func (h *CustomChannelHandler) List(c *gin.Context) {
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "20"))
	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > 100 {
		pageSize = 20
	}

	query := h.db.Model(&model.CustomChannel{})

	// 按名称搜索
	if search := c.Query("search"); search != "" {
		query = query.Where("name LIKE ?", "%"+search+"%")
	}
	// 按 is_default 过滤
	if isDefault := c.Query("is_default"); isDefault != "" {
		query = query.Where("is_default = ?", isDefault == "true" || isDefault == "1")
	}
	// 按 visibility 过滤
	if visibility := c.Query("visibility"); visibility != "" {
		query = query.Where("visibility = ?", visibility)
	}

	// 统计总数
	var total int64
	if err := query.Count(&total).Error; err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, err.Error())
		return
	}

	// 分页查询，预加载关联数据
	var list []model.CustomChannel
	if err := query.
		Preload("Routes", func(db *gorm.DB) *gorm.DB {
			return db.Order("priority DESC, weight DESC")
		}).
		Preload("Routes.Channel").
		Preload("Routes.Channel.Supplier").
		Preload("AccessList").
		Order("is_default DESC, created_at DESC").
		Offset((page - 1) * pageSize).
		Limit(pageSize).
		Find(&list).Error; err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, err.Error())
		return
	}

	response.PageResult(c, list, total, page, pageSize)
}

// Create 创建自定义渠道
// POST /api/v1/admin/custom-channels
// 如果 is_default=true，确保其他渠道的 is_default 设为 false（同一时间只有一个默认渠道）
func (h *CustomChannelHandler) Create(c *gin.Context) {
	var req createCustomChannelReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Error(c, http.StatusBadRequest, errcode.ErrBadRequest)
		return
	}

	// 设置默认值
	if req.Strategy == "" {
		req.Strategy = "weighted"
	}
	if req.Visibility == "" {
		req.Visibility = "all"
	}

	cc := model.CustomChannel{
		Name:        req.Name,
		Description: req.Description,
		Strategy:    req.Strategy,
		IsDefault:   req.IsDefault,
		AutoRoute:   req.AutoRoute,
		Visibility:  req.Visibility,
		IsActive:    true,
	}

	// 事务：如果设为默认，先将其他渠道的 is_default 置为 false
	txErr := h.db.Transaction(func(tx *gorm.DB) error {
		if req.IsDefault {
			// 同一时间只有一个默认渠道，将其他所有渠道的 is_default 设为 false
			if err := tx.Model(&model.CustomChannel{}).
				Where("is_default = ?", true).
				Update("is_default", false).Error; err != nil {
				return err
			}
		}
		return tx.Create(&cc).Error
	})

	if txErr != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, txErr.Error())
		return
	}

	// 重新加载完整数据（含关联）
	h.db.Preload("Routes").Preload("Routes.Channel").Preload("Routes.Channel.Supplier").
		Preload("AccessList").First(&cc, cc.ID)

	response.Success(c, cc)
}

// Update 更新自定义渠道
// PUT /api/v1/admin/custom-channels/:id
func (h *CustomChannelHandler) Update(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || id == 0 {
		response.Error(c, http.StatusBadRequest, errcode.ErrBadRequest)
		return
	}

	var cc model.CustomChannel
	if err := h.db.First(&cc, uint(id)).Error; err != nil {
		response.Error(c, http.StatusNotFound, errcode.ErrNotFound)
		return
	}

	var req updateCustomChannelReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Error(c, http.StatusBadRequest, errcode.ErrBadRequest)
		return
	}

	// 构造更新字段
	updates := make(map[string]interface{})
	if req.Name != nil {
		updates["name"] = *req.Name
	}
	if req.Description != nil {
		updates["description"] = *req.Description
	}
	if req.Strategy != nil {
		updates["strategy"] = *req.Strategy
	}
	if req.AutoRoute != nil {
		updates["auto_route"] = *req.AutoRoute
	}
	if req.Visibility != nil {
		updates["visibility"] = *req.Visibility
	}

	// 事务处理：如果设为默认，先将其他渠道取消默认
	txErr := h.db.Transaction(func(tx *gorm.DB) error {
		if req.IsDefault != nil && *req.IsDefault {
			// 将其他所有渠道的 is_default 设为 false
			if err := tx.Model(&model.CustomChannel{}).
				Where("id != ? AND is_default = ?", cc.ID, true).
				Update("is_default", false).Error; err != nil {
				return err
			}
			updates["is_default"] = true
		} else if req.IsDefault != nil {
			updates["is_default"] = false
		}

		if len(updates) > 0 {
			return tx.Model(&cc).Updates(updates).Error
		}
		return nil
	})

	if txErr != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, txErr.Error())
		return
	}

	// 重新加载
	h.db.Preload("Routes").Preload("Routes.Channel").Preload("Routes.Channel.Supplier").
		Preload("AccessList").First(&cc, cc.ID)

	response.Success(c, cc)
}

// Delete 删除自定义渠道
// DELETE /api/v1/admin/custom-channels/:id
// 不允许删除默认渠道
func (h *CustomChannelHandler) Delete(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || id == 0 {
		response.Error(c, http.StatusBadRequest, errcode.ErrBadRequest)
		return
	}

	var cc model.CustomChannel
	if err := h.db.First(&cc, uint(id)).Error; err != nil {
		response.Error(c, http.StatusNotFound, errcode.ErrNotFound)
		return
	}

	// 默认渠道不允许删除
	if cc.IsDefault {
		response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrBadRequest.Code, "不允许删除默认渠道")
		return
	}

	// 事务：先删路由和访问控制，再删主表
	txErr := h.db.Transaction(func(tx *gorm.DB) error {
		// 删除关联的路由规则
		if err := tx.Where("custom_channel_id = ?", cc.ID).Delete(&model.CustomChannelRoute{}).Error; err != nil {
			return err
		}
		// 删除关联的访问控制列表
		if err := tx.Where("custom_channel_id = ?", cc.ID).Delete(&model.CustomChannelAccess{}).Error; err != nil {
			return err
		}
		return tx.Delete(&cc).Error
	})

	if txErr != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, txErr.Error())
		return
	}

	response.Success(c, nil)
}

// Toggle 启用/禁用自定义渠道
// PATCH /api/v1/admin/custom-channels/:id/toggle
func (h *CustomChannelHandler) Toggle(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || id == 0 {
		response.Error(c, http.StatusBadRequest, errcode.ErrBadRequest)
		return
	}

	var cc model.CustomChannel
	if err := h.db.First(&cc, uint(id)).Error; err != nil {
		response.Error(c, http.StatusNotFound, errcode.ErrNotFound)
		return
	}

	// 切换 is_active 状态
	newActive := !cc.IsActive
	if err := h.db.Model(&cc).Update("is_active", newActive).Error; err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, err.Error())
		return
	}

	response.Success(c, gin.H{"id": cc.ID, "is_active": newActive})
}

// SetDefault 设置为默认渠道
// PATCH /api/v1/admin/custom-channels/:id/set-default
// 事务内：将其他渠道 is_default 设为 false，目标渠道设为 true
func (h *CustomChannelHandler) SetDefault(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || id == 0 {
		response.Error(c, http.StatusBadRequest, errcode.ErrBadRequest)
		return
	}

	var cc model.CustomChannel
	if err := h.db.First(&cc, uint(id)).Error; err != nil {
		response.Error(c, http.StatusNotFound, errcode.ErrNotFound)
		return
	}

	// 事务保证同一时间只有一个默认渠道
	txErr := h.db.Transaction(func(tx *gorm.DB) error {
		// 先将所有渠道的 is_default 设为 false
		if err := tx.Model(&model.CustomChannel{}).
			Where("is_default = ?", true).
			Update("is_default", false).Error; err != nil {
			return err
		}
		// 将目标渠道设为默认
		return tx.Model(&cc).Update("is_default", true).Error
	})

	if txErr != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, txErr.Error())
		return
	}

	response.Success(c, gin.H{"id": cc.ID, "is_default": true})
}

// UpdateAccess 更新访问控制列表
// PUT /api/v1/admin/custom-channels/:id/access
// Body: { user_ids: [1, 2, 3] }
// 全量替换 CustomChannelAccess 记录
func (h *CustomChannelHandler) UpdateAccess(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || id == 0 {
		response.Error(c, http.StatusBadRequest, errcode.ErrBadRequest)
		return
	}

	var cc model.CustomChannel
	if err := h.db.First(&cc, uint(id)).Error; err != nil {
		response.Error(c, http.StatusNotFound, errcode.ErrNotFound)
		return
	}

	var req updateAccessReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Error(c, http.StatusBadRequest, errcode.ErrBadRequest)
		return
	}

	// 事务：全量替换访问控制列表（先删后建）
	txErr := h.db.Transaction(func(tx *gorm.DB) error {
		// 删除旧的访问控制记录
		if err := tx.Where("custom_channel_id = ?", cc.ID).Delete(&model.CustomChannelAccess{}).Error; err != nil {
			return err
		}
		// 批量创建新的访问控制记录
		for _, uid := range req.UserIDs {
			access := model.CustomChannelAccess{
				CustomChannelID: cc.ID,
				UserID:          uid,
			}
			if err := tx.Create(&access).Error; err != nil {
				return err
			}
		}
		return nil
	})

	if txErr != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, txErr.Error())
		return
	}

	// 返回更新后的访问列表
	var accessList []model.CustomChannelAccess
	h.db.Where("custom_channel_id = ?", cc.ID).Find(&accessList)

	response.Success(c, gin.H{"custom_channel_id": cc.ID, "access_list": accessList})
}

// BatchRoutes 批量设置路由规则
// POST /api/v1/admin/custom-channels/:id/routes/batch
// Body: { routes: [{ alias_model, channel_id, actual_model, weight, priority }] }
// 全量替换该渠道下的所有路由
func (h *CustomChannelHandler) BatchRoutes(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || id == 0 {
		response.Error(c, http.StatusBadRequest, errcode.ErrBadRequest)
		return
	}

	var cc model.CustomChannel
	if err := h.db.First(&cc, uint(id)).Error; err != nil {
		response.Error(c, http.StatusNotFound, errcode.ErrNotFound)
		return
	}

	var req batchRoutesReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Error(c, http.StatusBadRequest, errcode.ErrBadRequest)
		return
	}

	// 事务：全量替换路由（先删后建）
	txErr := h.db.Transaction(func(tx *gorm.DB) error {
		// 删除旧路由
		if err := tx.Where("custom_channel_id = ?", cc.ID).Delete(&model.CustomChannelRoute{}).Error; err != nil {
			return err
		}
		// 批量创建新路由
		for _, r := range req.Routes {
			route := model.CustomChannelRoute{
				CustomChannelID: cc.ID,
				AliasModel:      r.AliasModel,
				ChannelID:       r.ChannelID,
				ActualModel:     r.ActualModel,
				Weight:          r.Weight,
				Priority:        r.Priority,
				IsActive:        true,
			}
			// 默认权重
			if route.Weight <= 0 {
				route.Weight = 100
			}
			if err := tx.Create(&route).Error; err != nil {
				return err
			}
		}
		return nil
	})

	if txErr != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, txErr.Error())
		return
	}

	// 重新加载路由数据
	h.db.Preload("Routes", func(db *gorm.DB) *gorm.DB {
		return db.Order("priority DESC, weight DESC")
	}).Preload("Routes.Channel").Preload("Routes.Channel.Supplier").First(&cc, cc.ID)

	response.Success(c, cc)
}

// ImportRoutes 从供应商接入点导入路由
// POST /api/v1/admin/custom-channels/:id/routes/import
// Body: { channel_id: 1 }
// 读取 ChannelModel 表中该接入点的所有映射，批量创建 CustomChannelRoute
func (h *CustomChannelHandler) ImportRoutes(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || id == 0 {
		response.Error(c, http.StatusBadRequest, errcode.ErrBadRequest)
		return
	}

	var cc model.CustomChannel
	if err := h.db.First(&cc, uint(id)).Error; err != nil {
		response.Error(c, http.StatusNotFound, errcode.ErrNotFound)
		return
	}

	var req importRoutesReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Error(c, http.StatusBadRequest, errcode.ErrBadRequest)
		return
	}

	// 查询指定接入点的所有模型映射
	var channelModels []model.ChannelModel
	if err := h.db.Where("channel_id = ? AND is_active = ?", req.ChannelID, true).
		Find(&channelModels).Error; err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, err.Error())
		return
	}

	if len(channelModels) == 0 {
		response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrBadRequest.Code, "该接入点没有可导入的模型映射")
		return
	}

	// 批量创建路由（追加模式，不删除现有路由）
	imported := 0
	for _, cm := range channelModels {
		route := model.CustomChannelRoute{
			CustomChannelID: cc.ID,
			AliasModel:      cm.StandardModelID, // 标准模型名作为别名
			ChannelID:       cm.ChannelID,
			ActualModel:     cm.VendorModelID, // 供应商特定ID作为实际模型
			Weight:          100,
			Priority:        0,
			IsActive:        true,
		}
		// 忽略唯一约束冲突（已存在的路由跳过）
		if err := h.db.Where("custom_channel_id = ? AND alias_model = ? AND channel_id = ?",
			cc.ID, route.AliasModel, route.ChannelID).
			FirstOrCreate(&route).Error; err != nil {
			continue
		}
		imported++
	}

	// 重新加载路由数据
	h.db.Preload("Routes", func(db *gorm.DB) *gorm.DB {
		return db.Order("priority DESC, weight DESC")
	}).Preload("Routes.Channel").Preload("Routes.Channel.Supplier").First(&cc, cc.ID)

	response.Success(c, gin.H{
		"imported":       imported,
		"total_mappings": len(channelModels),
		"channel":        cc,
	})
}

// RefreshDefault 刷新默认渠道路由（按成本优先）
// POST /api/v1/admin/custom-channels/default/refresh
// 逻辑:
//  1. 查找 is_default=true 的渠道
//  2. 查询所有 active Channel + Supplier + ChannelModel
//  3. 按 standard_model_id 汇总，计算综合成本 = (InputPricePerM + OutputPricePerM) * Discount
//  4. 成本最低的 weight=100, priority=10；其余按成本倒数比例分配 weight
//  5. 全量替换默认渠道的 Routes
func (h *CustomChannelHandler) RefreshDefault(c *gin.Context) {
	// 1. 查找默认渠道
	var defaultCC model.CustomChannel
	if err := h.db.Where("is_default = ?", true).First(&defaultCC).Error; err != nil {
		response.ErrorMsg(c, http.StatusNotFound, errcode.ErrNotFound.Code, "未找到默认渠道")
		return
	}

	// 2. 查询所有已激活的接入点及其供应商信息和模型映射
	var channelModels []model.ChannelModel
	if err := h.db.
		Joins("JOIN channels ON channels.id = channel_models.channel_id").
		Joins("JOIN suppliers ON suppliers.id = channels.supplier_id").
		Where("channels.status = ? AND channel_models.is_active = ?", "active", true).
		Preload("Channel").
		Preload("Channel.Supplier").
		Find(&channelModels).Error; err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, err.Error())
		return
	}

	if len(channelModels) == 0 {
		response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrBadRequest.Code, "没有可用的接入点模型映射")
		return
	}

	// 3. 按 standard_model_id 汇总，选出每个模型成本最优的接入点
	// costEntry 保存每个候选路由的成本信息
	type costEntry struct {
		ChannelModel model.ChannelModel
		Cost         float64 // 综合成本 = (InputPricePerM + OutputPricePerM) * Discount
	}

	// modelCandidates: key=标准模型名, value=该模型的所有候选接入点（按成本排序）
	modelCandidates := make(map[string][]costEntry)
	for _, cm := range channelModels {
		supplier := cm.Channel.Supplier
		// 计算综合成本: (输入价格 + 输出价格) * 折扣
		cost := (supplier.InputPricePerM + supplier.OutputPricePerM) * supplier.Discount
		modelCandidates[cm.StandardModelID] = append(modelCandidates[cm.StandardModelID], costEntry{
			ChannelModel: cm,
			Cost:         cost,
		})
	}

	// 4. 生成路由规则：每个标准模型名选出所有候选接入点，按成本分配 weight
	var newRoutes []model.CustomChannelRoute
	for aliasModel, candidates := range modelCandidates {
		if len(candidates) == 0 {
			continue
		}

		// 找出最低成本
		minCost := math.MaxFloat64
		for _, ce := range candidates {
			if ce.Cost > 0 && ce.Cost < minCost {
				minCost = ce.Cost
			}
		}
		// 如果所有成本都为0，给一个默认值避免除0
		if minCost <= 0 || minCost == math.MaxFloat64 {
			minCost = 1.0
		}

		for _, ce := range candidates {
			weight := 100
			priority := 0

			if ce.Cost > 0 {
				// 成本最低的 weight=100, priority=10
				// 其余按成本倒数比例分配 weight: weight = int(minCost/cost * 100)
				ratio := minCost / ce.Cost
				weight = int(math.Round(ratio * 100))
				if weight < 1 {
					weight = 1
				}
				if weight >= 100 {
					priority = 10 // 成本最优的给最高优先级
				}
			}

			newRoutes = append(newRoutes, model.CustomChannelRoute{
				CustomChannelID: defaultCC.ID,
				AliasModel:      aliasModel,
				ChannelID:       ce.ChannelModel.ChannelID,
				ActualModel:     ce.ChannelModel.VendorModelID,
				Weight:          weight,
				Priority:        priority,
				IsActive:        true,
			})
		}
	}

	// 5. 事务：全量替换默认渠道的路由
	txErr := h.db.Transaction(func(tx *gorm.DB) error {
		// 删除旧路由
		if err := tx.Where("custom_channel_id = ?", defaultCC.ID).
			Delete(&model.CustomChannelRoute{}).Error; err != nil {
			return err
		}
		// 批量写入新路由
		for _, route := range newRoutes {
			if err := tx.Create(&route).Error; err != nil {
				return err
			}
		}
		return nil
	})

	if txErr != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, txErr.Error())
		return
	}

	// 重新加载默认渠道完整数据
	h.db.Preload("Routes", func(db *gorm.DB) *gorm.DB {
		return db.Order("priority DESC, weight DESC")
	}).Preload("Routes.Channel").Preload("Routes.Channel.Supplier").
		Preload("AccessList").First(&defaultCC, defaultCC.ID)

	response.Success(c, gin.H{
		"channel":      defaultCC,
		"total_routes": len(newRoutes),
		"models_count": len(modelCandidates),
	})
}
