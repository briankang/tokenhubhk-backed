package admin

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"sync"
	"sync/atomic"

	"github.com/gin-gonic/gin"
	goredis "github.com/redis/go-redis/v9"
	"go.uber.org/zap"
	"gorm.io/gorm"

	"tokenhub-server/internal/model"
	"tokenhub-server/internal/pkg/errcode"
	"tokenhub-server/internal/pkg/response"
	channelsvc "tokenhub-server/internal/service/channel"
)

// ==================== 默认渠道路由刷新：异步任务存储 ====================

// routeRefreshMu 防重入锁：同一时刻只允许一个刷新任务执行
var routeRefreshMu sync.Mutex

// currentRefreshJob 保存最近一次（正在运行 或 已完成）的刷新任务
// 使用 atomic.Value 读写 *channelsvc.RouteRefreshJob，避免读写竞争
var currentRefreshJob atomic.Value // stores *channelsvc.RouteRefreshJob

// loadCurrentRefreshJob 读取最近一次任务（可能为 nil）
func loadCurrentRefreshJob() *channelsvc.RouteRefreshJob {
	v := currentRefreshJob.Load()
	if v == nil {
		return nil
	}
	job, _ := v.(*channelsvc.RouteRefreshJob)
	return job
}

// CustomChannelHandler 自定义渠道管理接口处理器
type CustomChannelHandler struct {
	db    *gorm.DB
	redis *goredis.Client
}

// NewCustomChannelHandler 创建自定义渠道管理Handler实例
func NewCustomChannelHandler(db *gorm.DB, redis ...*goredis.Client) *CustomChannelHandler {
	h := &CustomChannelHandler{db: db}
	if len(redis) > 0 {
		h.redis = redis[0]
	}
	return h
}

// invalidateCustomChannelCache 清除自定义渠道相关的 Redis 缓存
func (h *CustomChannelHandler) invalidateCustomChannelCache(ccID uint) {
	if h.redis == nil {
		return
	}
	ctx := context.Background()
	// 清除指定渠道缓存
	_ = h.redis.Del(ctx, fmt.Sprintf("custom_channel:id:%d", ccID)).Err()
	// 清除默认渠道缓存
	_ = h.redis.Del(ctx, "custom_channel:default").Err()
	// 清除该渠道下的路由缓存（使用通配符扫描并删除）
	pattern := fmt.Sprintf("custom_channel_routes:%d:*", ccID)
	iter := h.redis.Scan(ctx, 0, pattern, 100).Iterator()
	for iter.Next(ctx) {
		_ = h.redis.Del(ctx, iter.Val()).Err()
	}
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

	h.invalidateCustomChannelCache(cc.ID)
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
			return tx.Model(&model.CustomChannel{}).Where("id = ?", cc.ID).Updates(updates).Error
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

	h.invalidateCustomChannelCache(cc.ID)
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
		// 删除关联的路由规则（硬删除，避免唯一索引冲突）
		if err := tx.Unscoped().Where("custom_channel_id = ?", cc.ID).Delete(&model.CustomChannelRoute{}).Error; err != nil {
			return err
		}
		// 删除关联的访问控制列表
		if err := tx.Unscoped().Where("custom_channel_id = ?", cc.ID).Delete(&model.CustomChannelAccess{}).Error; err != nil {
			return err
		}
		return tx.Delete(&cc).Error
	})

	if txErr != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, txErr.Error())
		return
	}

	h.invalidateCustomChannelCache(cc.ID)
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
	if err := h.db.Model(&model.CustomChannel{}).Where("id = ?", cc.ID).Update("is_active", newActive).Error; err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, err.Error())
		return
	}

	response.Success(c, gin.H{"id": cc.ID, "is_active": newActive})
	h.invalidateCustomChannelCache(cc.ID)
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
		return tx.Model(&model.CustomChannel{}).Where("id = ?", cc.ID).Update("is_default", true).Error
	})

	if txErr != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, txErr.Error())
		return
	}

	response.Success(c, gin.H{"id": cc.ID, "is_default": true})
	h.invalidateCustomChannelCache(cc.ID)
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
		// 删除旧路由（硬删除，避免唯一索引冲突）
		if err := tx.Unscoped().Where("custom_channel_id = ?", cc.ID).Delete(&model.CustomChannelRoute{}).Error; err != nil {
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

	h.invalidateCustomChannelCache(cc.ID)
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

	h.invalidateCustomChannelCache(cc.ID)
	response.Success(c, gin.H{
		"imported":       imported,
		"total_mappings": len(channelModels),
		"channel":        cc,
	})
}

// RefreshDefault 刷新默认渠道路由（按成本优先）
// POST /api/v1/admin/custom-channels/default/refresh
// 异步模式：接收请求后立即返回 job_id，后台 goroutine 执行实际刷新
// 前端通过 GetRefreshStatus 轮询进度。
// 同一时刻只允许一个刷新任务运行（routeRefreshMu 保护）。
func (h *CustomChannelHandler) RefreshDefault(c *gin.Context) {
	// 防重入：已有任务在跑直接拒绝
	if !routeRefreshMu.TryLock() {
		response.ErrorMsg(c, http.StatusConflict, errcode.ErrIdempotentRepeat.Code, "已有刷新任务运行中，请稍后再试")
		return
	}

	job := channelsvc.NewRouteRefreshJob("manual")
	currentRefreshJob.Store(job)

	// 后台执行
	go func() {
		defer routeRefreshMu.Unlock()
		defer func() {
			if r := recover(); r != nil {
				zap.L().Error("路由刷新 panic", zap.Any("panic", r))
				job.Fail(fmt.Errorf("panic: %v", r))
			}
		}()
		ctx, cancel := context.WithTimeout(context.Background(), 10*60*1000*1000*1000) // 10 分钟
		defer cancel()
		if err := channelsvc.RefreshDefaultRoutes(ctx, h.db, h.redis, job); err != nil {
			job.Fail(err)
		}
	}()

	// 使用标准响应包装器，前端 api 客户端依赖 {code, message, data} 格式
	response.Success(c, gin.H{
		"job_id":     job.ID,
		"status":     job.Status,
		"started_at": job.StartedAt,
	})
}

// GetRefreshStatus 返回最近一次（正在运行 或 已完成）的路由刷新任务状态快照
// 前端每 1-2s 轮询此接口，直到 status != "running" 表示完成
func (h *CustomChannelHandler) GetRefreshStatus(c *gin.Context) {
	job := loadCurrentRefreshJob()
	if job == nil {
		response.Success(c, gin.H{"job": nil, "message": "尚未执行过路由刷新"})
		return
	}
	snap := job.Snapshot()
	response.Success(c, gin.H{"job": snap})
}
