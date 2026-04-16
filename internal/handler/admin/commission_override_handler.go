package admin

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"tokenhub-server/internal/database"
	"tokenhub-server/internal/model"
	"tokenhub-server/internal/pkg/errcode"
	"tokenhub-server/internal/pkg/response"
)

// CommissionOverrideHandler 特殊用户加佣配置管理
// v3.1 新增:针对 KOL/合作方/内部员工等特殊身份可设高于基础 10% 的专属佣金率
// 约束:同一 user_id 在 is_active=true 时唯一,rate 硬上限 0.80
type CommissionOverrideHandler struct{}

// NewCommissionOverrideHandler 创建 handler 实例
func NewCommissionOverrideHandler() *CommissionOverrideHandler {
	return &CommissionOverrideHandler{}
}

// Register 注册路由
func (h *CommissionOverrideHandler) Register(rg *gin.RouterGroup) {
	rg.GET("/commission-overrides", h.List)
	rg.POST("/commission-overrides", h.Create)
	rg.POST("/commission-overrides/batch", h.BatchCreate)
	rg.PUT("/commission-overrides/:id", h.Update)
	rg.DELETE("/commission-overrides/:id", h.Delete)
	rg.GET("/commission-overrides/user/:user_id", h.ListByUser)
}

// commissionOverrideDTO 返回给前端的数据结构(携带用户邮箱便于展示)
type commissionOverrideDTO struct {
	ID                   uint       `json:"id"`
	UserID               uint       `json:"userId"`
	UserEmail            string     `json:"userEmail"`
	IsActive             bool       `json:"isActive"`
	CommissionRate       float64    `json:"commissionRate"`
	AttributionDays      *int       `json:"attributionDays"`
	LifetimeCapCredits   *int64     `json:"lifetimeCapCredits"`
	MinPaidCreditsUnlock *int64     `json:"minPaidCreditsUnlock"`
	EffectiveFrom        time.Time  `json:"effectiveFrom"`
	EffectiveTo          *time.Time `json:"effectiveTo"`
	Note                 string     `json:"note"`
	CreatedBy            uint       `json:"createdBy"`
	CreatedAt            time.Time  `json:"createdAt"`
	UpdatedAt            time.Time  `json:"updatedAt"`
}

func toDTO(o *model.UserCommissionOverride, email string) commissionOverrideDTO {
	// IsActive 为 *bool:指针非空且解引用为 true 时才算活跃
	active := false
	if o.IsActive != nil && *o.IsActive {
		active = true
	}
	return commissionOverrideDTO{
		ID:                   o.ID,
		UserID:               o.UserID,
		UserEmail:            email,
		IsActive:             active,
		CommissionRate:       o.CommissionRate,
		AttributionDays:      o.AttributionDays,
		LifetimeCapCredits:   o.LifetimeCapCredits,
		MinPaidCreditsUnlock: o.MinPaidCreditsUnlock,
		EffectiveFrom:        o.EffectiveFrom,
		EffectiveTo:          o.EffectiveTo,
		Note:                 o.Note,
		CreatedBy:            o.CreatedBy,
		CreatedAt:            o.CreatedAt,
		UpdatedAt:            o.UpdatedAt,
	}
}

// List 列表查询 GET /api/v1/admin/commission-overrides?page=1&page_size=20&search=email_or_uid&active=true
func (h *CommissionOverrideHandler) List(c *gin.Context) {
	db := database.DB
	page := 1
	pageSize := 20
	if p := c.Query("page"); p != "" {
		if v, err := strconv.Atoi(p); err == nil && v > 0 {
			page = v
		}
	}
	if ps := c.Query("page_size"); ps != "" {
		if v, err := strconv.Atoi(ps); err == nil && v > 0 && v <= 100 {
			pageSize = v
		}
	}
	search := c.Query("search")
	activeStr := c.Query("active")

	q := db.WithContext(c.Request.Context()).Model(&model.UserCommissionOverride{})
	// is_active 为 NULL 表示已失效,true 表示活跃
	if activeStr == "true" {
		q = q.Where("is_active = ?", true)
	} else if activeStr == "false" {
		q = q.Where("is_active IS NULL")
	}

	// 搜索按邮箱模糊或按 user_id 精确
	if search != "" {
		// 如果是纯数字视为 user_id
		if uid, err := strconv.Atoi(search); err == nil && uid > 0 {
			q = q.Where("user_id = ?", uid)
		} else {
			// 通过 email 反查 user_ids
			var userIDs []uint
			db.WithContext(c.Request.Context()).Model(&model.User{}).
				Where("email LIKE ?", "%"+search+"%").
				Pluck("id", &userIDs)
			if len(userIDs) == 0 {
				response.PageResult(c, []commissionOverrideDTO{}, 0, page, pageSize)
				return
			}
			q = q.Where("user_id IN ?", userIDs)
		}
	}

	var total int64
	if err := q.Count(&total).Error; err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, err.Error())
		return
	}

	var list []model.UserCommissionOverride
	if err := q.Order("created_at DESC").Offset((page - 1) * pageSize).Limit(pageSize).Find(&list).Error; err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, err.Error())
		return
	}

	// 批量加载 email 以便前端展示
	emailMap := make(map[uint]string, len(list))
	if len(list) > 0 {
		ids := make([]uint, 0, len(list))
		for i := range list {
			ids = append(ids, list[i].UserID)
		}
		var users []model.User
		db.WithContext(c.Request.Context()).
			Where("id IN ?", ids).
			Select("id, email").
			Find(&users)
		for i := range users {
			emailMap[users[i].ID] = users[i].Email
		}
	}

	result := make([]commissionOverrideDTO, 0, len(list))
	for i := range list {
		result = append(result, toDTO(&list[i], emailMap[list[i].UserID]))
	}

	response.PageResult(c, result, total, page, pageSize)
}

// ListByUser 查某用户的 override 历史 GET /api/v1/admin/commission-overrides/user/:user_id
func (h *CommissionOverrideHandler) ListByUser(c *gin.Context) {
	uidStr := c.Param("user_id")
	uid, err := strconv.ParseUint(uidStr, 10, 64)
	if err != nil || uid == 0 {
		response.Error(c, http.StatusBadRequest, errcode.ErrBadRequest)
		return
	}

	db := database.DB
	var list []model.UserCommissionOverride
	if err := db.WithContext(c.Request.Context()).
		Where("user_id = ?", uint(uid)).
		Order("created_at DESC").
		Find(&list).Error; err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, err.Error())
		return
	}

	var email string
	var user model.User
	if err := db.WithContext(c.Request.Context()).Select("email").First(&user, uint(uid)).Error; err == nil {
		email = user.Email
	}

	result := make([]commissionOverrideDTO, 0, len(list))
	for i := range list {
		result = append(result, toDTO(&list[i], email))
	}
	response.Success(c, result)
}

// createOverrideReq 创建请求体
type createOverrideReq struct {
	UserID               uint       `json:"userId" binding:"required"`
	CommissionRate       float64    `json:"commissionRate" binding:"required"`
	AttributionDays      *int       `json:"attributionDays"`      // NULL=继承全局
	LifetimeCapCredits   *int64     `json:"lifetimeCapCredits"`   // NULL=继承全局; 0=无上限; >0=自定义上限
	MinPaidCreditsUnlock *int64     `json:"minPaidCreditsUnlock"` // NULL=继承全局; 0=立即解锁; >0=自定义门槛
	EffectiveFrom        *time.Time `json:"effectiveFrom"`
	EffectiveTo          *time.Time `json:"effectiveTo"`
	Note                 string     `json:"note"`
}

// Create 新增 override POST /api/v1/admin/commission-overrides
func (h *CommissionOverrideHandler) Create(c *gin.Context) {
	var req createOverrideReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Error(c, http.StatusBadRequest, errcode.ErrBadRequest)
		return
	}

	// 硬上限校验:rate [0.01, 0.80]
	if req.CommissionRate < 0.01 || req.CommissionRate > 0.80 {
		response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrBadRequest.Code, "commissionRate must be in [0.01, 0.80]")
		return
	}
	if len(req.Note) > 500 {
		response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrBadRequest.Code, "note too long (max 500)")
		return
	}
	// AttributionDays 校验:NULL=继承全局;非空时范围 [7, 3650]
	if req.AttributionDays != nil {
		if *req.AttributionDays < 7 || *req.AttributionDays > 3650 {
			response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrBadRequest.Code, "attributionDays must be in [7, 3650]")
			return
		}
	}
	// LifetimeCapCredits 校验:NULL=继承全局;非空时 >= 0(0 表示无上限)
	if req.LifetimeCapCredits != nil && *req.LifetimeCapCredits < 0 {
		response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrBadRequest.Code, "lifetimeCapCredits must be >= 0")
		return
	}
	// MinPaidCreditsUnlock 校验:NULL=继承全局;非空时 >= 0(0 表示立即解锁)
	if req.MinPaidCreditsUnlock != nil && *req.MinPaidCreditsUnlock < 0 {
		response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrBadRequest.Code, "minPaidCreditsUnlock must be >= 0")
		return
	}

	db := database.DB

	// 校验用户存在
	var user model.User
	if err := db.WithContext(c.Request.Context()).First(&user, req.UserID).Error; err != nil {
		response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrBadRequest.Code, fmt.Sprintf("user %d not found", req.UserID))
		return
	}

	// 幂等:若已有活跃 override,先置为 is_active=NULL(失效)再创建新的
	// 保证"同一 user 同一时刻仅 1 条活跃"(唯一索引 idx_user_active)
	if err := db.WithContext(c.Request.Context()).
		Model(&model.UserCommissionOverride{}).
		Where("user_id = ? AND is_active = ?", req.UserID, true).
		Update("is_active", gorm.Expr("NULL")).Error; err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, err.Error())
		return
	}

	effectiveFrom := time.Now()
	if req.EffectiveFrom != nil {
		effectiveFrom = *req.EffectiveFrom
	}
	if req.EffectiveTo != nil && !req.EffectiveTo.After(effectiveFrom) {
		response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrBadRequest.Code, "effectiveTo must be after effectiveFrom")
		return
	}

	// 读取操作者 ID
	var createdBy uint
	if v, ok := c.Get("userId"); ok {
		if u, ok := v.(uint); ok {
			createdBy = u
		}
	}

	activeTrue := true
	o := model.UserCommissionOverride{
		UserID:               req.UserID,
		IsActive:             &activeTrue,
		CommissionRate:       req.CommissionRate,
		AttributionDays:      req.AttributionDays,
		LifetimeCapCredits:   req.LifetimeCapCredits,
		MinPaidCreditsUnlock: req.MinPaidCreditsUnlock,
		EffectiveFrom:        effectiveFrom,
		EffectiveTo:          req.EffectiveTo,
		Note:                 req.Note,
		CreatedBy:            createdBy,
	}
	if err := db.WithContext(c.Request.Context()).Create(&o).Error; err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, err.Error())
		return
	}

	response.Success(c, toDTO(&o, user.Email))
}

// updateOverrideReq 更新请求体(所有字段可选,指针)
type updateOverrideReq struct {
	CommissionRate            *float64   `json:"commissionRate"`
	AttributionDays           *int       `json:"attributionDays"`           // 传值=设置,nil+clearAttributionDays=false=不变
	ClearAttributionDays      bool       `json:"clearAttributionDays"`      // true 时将归因周期置 NULL(继承全局)
	LifetimeCapCredits        *int64     `json:"lifetimeCapCredits"`        // 传值=设置,nil+clearLifetimeCap=false=不变
	ClearLifetimeCapCredits   bool       `json:"clearLifetimeCapCredits"`   // true 时将终身上限置 NULL(继承全局)
	MinPaidCreditsUnlock      *int64     `json:"minPaidCreditsUnlock"`      // 传值=设置,nil+clearMinPaidCreditsUnlock=false=不变
	ClearMinPaidCreditsUnlock bool       `json:"clearMinPaidCreditsUnlock"` // true 时将解锁门槛置 NULL(继承全局)
	EffectiveFrom             *time.Time `json:"effectiveFrom"`
	EffectiveTo               *time.Time `json:"effectiveTo"`
	Note                      *string    `json:"note"`
	IsActive                  *bool      `json:"isActive"`
}

// Update 更新 override PUT /api/v1/admin/commission-overrides/:id
func (h *CommissionOverrideHandler) Update(c *gin.Context) {
	idStr := c.Param("id")
	id, err := strconv.ParseUint(idStr, 10, 64)
	if err != nil || id == 0 {
		response.Error(c, http.StatusBadRequest, errcode.ErrBadRequest)
		return
	}

	var req updateOverrideReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Error(c, http.StatusBadRequest, errcode.ErrBadRequest)
		return
	}

	db := database.DB
	var o model.UserCommissionOverride
	if err := db.WithContext(c.Request.Context()).First(&o, uint(id)).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			response.Error(c, http.StatusNotFound, errcode.ErrNotFound)
			return
		}
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, err.Error())
		return
	}

	updates := map[string]interface{}{}
	if req.CommissionRate != nil {
		v := *req.CommissionRate
		if v < 0.01 || v > 0.80 {
			response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrBadRequest.Code, "commissionRate must be in [0.01, 0.80]")
			return
		}
		updates["commission_rate"] = v
	}
	if req.ClearAttributionDays {
		updates["attribution_days"] = gorm.Expr("NULL")
	} else if req.AttributionDays != nil {
		v := *req.AttributionDays
		if v < 7 || v > 3650 {
			response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrBadRequest.Code, "attributionDays must be in [7, 3650]")
			return
		}
		updates["attribution_days"] = v
	}
	if req.ClearLifetimeCapCredits {
		updates["lifetime_cap_credits"] = gorm.Expr("NULL")
	} else if req.LifetimeCapCredits != nil {
		if *req.LifetimeCapCredits < 0 {
			response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrBadRequest.Code, "lifetimeCapCredits must be >= 0")
			return
		}
		updates["lifetime_cap_credits"] = *req.LifetimeCapCredits
	}
	if req.ClearMinPaidCreditsUnlock {
		updates["min_paid_credits_unlock"] = gorm.Expr("NULL")
	} else if req.MinPaidCreditsUnlock != nil {
		if *req.MinPaidCreditsUnlock < 0 {
			response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrBadRequest.Code, "minPaidCreditsUnlock must be >= 0")
			return
		}
		updates["min_paid_credits_unlock"] = *req.MinPaidCreditsUnlock
	}
	if req.EffectiveFrom != nil {
		updates["effective_from"] = *req.EffectiveFrom
	}
	if req.EffectiveTo != nil {
		updates["effective_to"] = *req.EffectiveTo
	}
	if req.Note != nil {
		if len(*req.Note) > 500 {
			response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrBadRequest.Code, "note too long (max 500)")
			return
		}
		updates["note"] = *req.Note
	}
	if req.IsActive != nil {
		if *req.IsActive {
			// 激活:先将同 user 其它活跃记录失效(NULL)再激活本条
			if err := db.WithContext(c.Request.Context()).
				Model(&model.UserCommissionOverride{}).
				Where("user_id = ? AND id <> ? AND is_active = ?", o.UserID, o.ID, true).
				Update("is_active", gorm.Expr("NULL")).Error; err != nil {
				response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, err.Error())
				return
			}
			updates["is_active"] = true
		} else {
			// 失效用 NULL 以避免唯一索引冲突
			updates["is_active"] = gorm.Expr("NULL")
		}
	}

	if len(updates) == 0 {
		response.Success(c, toDTO(&o, ""))
		return
	}

	// 注意:使用 Model(&o) + Updates(map[含 gorm.Expr]) 时 GORM 偶发丢失主键推导,
	// 触发 ErrMissingWhereClause — 此处显式指定 WHERE id = ? 以彻底规避
	if err := db.WithContext(c.Request.Context()).
		Model(&model.UserCommissionOverride{}).
		Where("id = ?", o.ID).
		Updates(updates).Error; err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, err.Error())
		return
	}

	// 重新加载
	db.WithContext(c.Request.Context()).First(&o, uint(id))
	var email string
	var user model.User
	if err := db.WithContext(c.Request.Context()).Select("email").First(&user, o.UserID).Error; err == nil {
		email = user.Email
	}
	response.Success(c, toDTO(&o, email))
}

// Delete 软删除(is_active=NULL) DELETE /api/v1/admin/commission-overrides/:id
// 将 is_active 置为 NULL 表示失效,NULL 不参与唯一索引,允许同 user 有多条历史失效记录
func (h *CommissionOverrideHandler) Delete(c *gin.Context) {
	idStr := c.Param("id")
	id, err := strconv.ParseUint(idStr, 10, 64)
	if err != nil || id == 0 {
		response.Error(c, http.StatusBadRequest, errcode.ErrBadRequest)
		return
	}

	db := database.DB
	result := db.WithContext(c.Request.Context()).
		Model(&model.UserCommissionOverride{}).
		Where("id = ?", uint(id)).
		Update("is_active", gorm.Expr("NULL"))
	if result.Error != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, result.Error.Error())
		return
	}
	if result.RowsAffected == 0 {
		response.Error(c, http.StatusNotFound, errcode.ErrNotFound)
		return
	}
	response.Success(c, gin.H{"id": id, "is_active": nil})
}

// batchCreateOverrideReq 批量创建请求体
type batchCreateOverrideReq struct {
	UserIDs              []uint     `json:"userIds" binding:"required"`
	CommissionRate       float64    `json:"commissionRate" binding:"required"`
	AttributionDays      *int       `json:"attributionDays"`
	LifetimeCapCredits   *int64     `json:"lifetimeCapCredits"`
	MinPaidCreditsUnlock *int64     `json:"minPaidCreditsUnlock"`
	EffectiveFrom        *time.Time `json:"effectiveFrom"`
	EffectiveTo          *time.Time `json:"effectiveTo"`
	Note                 string     `json:"note"`
}

// batchCreateResultItem 单个用户的批量创建结果
type batchCreateResultItem struct {
	UserID    uint   `json:"userId"`
	UserEmail string `json:"userEmail,omitempty"`
	Success   bool   `json:"success"`
	Error     string `json:"error,omitempty"`
	// 成功时返回新创建的 override id
	OverrideID uint `json:"overrideId,omitempty"`
}

// BatchCreate 批量创建 override POST /api/v1/admin/commission-overrides/batch
// 对每个 userId 执行:失效旧 active override → 创建新 override
// 单个用户失败不影响其它用户,返回逐个结果
func (h *CommissionOverrideHandler) BatchCreate(c *gin.Context) {
	var req batchCreateOverrideReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Error(c, http.StatusBadRequest, errcode.ErrBadRequest)
		return
	}
	if len(req.UserIDs) == 0 {
		response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrBadRequest.Code, "userIds is required")
		return
	}
	if len(req.UserIDs) > 200 {
		response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrBadRequest.Code, "batch size too large (max 200)")
		return
	}
	// 通用字段校验(与单条 Create 保持一致)
	if req.CommissionRate < 0.01 || req.CommissionRate > 0.80 {
		response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrBadRequest.Code, "commissionRate must be in [0.01, 0.80]")
		return
	}
	if len(req.Note) > 500 {
		response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrBadRequest.Code, "note too long (max 500)")
		return
	}
	if req.AttributionDays != nil {
		if *req.AttributionDays < 7 || *req.AttributionDays > 3650 {
			response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrBadRequest.Code, "attributionDays must be in [7, 3650]")
			return
		}
	}
	if req.LifetimeCapCredits != nil && *req.LifetimeCapCredits < 0 {
		response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrBadRequest.Code, "lifetimeCapCredits must be >= 0")
		return
	}
	if req.MinPaidCreditsUnlock != nil && *req.MinPaidCreditsUnlock < 0 {
		response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrBadRequest.Code, "minPaidCreditsUnlock must be >= 0")
		return
	}

	effectiveFrom := time.Now()
	if req.EffectiveFrom != nil {
		effectiveFrom = *req.EffectiveFrom
	}
	if req.EffectiveTo != nil && !req.EffectiveTo.After(effectiveFrom) {
		response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrBadRequest.Code, "effectiveTo must be after effectiveFrom")
		return
	}

	var createdBy uint
	if v, ok := c.Get("userId"); ok {
		if u, ok := v.(uint); ok {
			createdBy = u
		}
	}

	db := database.DB
	ctx := c.Request.Context()

	// 去重 userIds
	idSet := make(map[uint]struct{}, len(req.UserIDs))
	uniqueIDs := make([]uint, 0, len(req.UserIDs))
	for _, uid := range req.UserIDs {
		if uid == 0 {
			continue
		}
		if _, exists := idSet[uid]; exists {
			continue
		}
		idSet[uid] = struct{}{}
		uniqueIDs = append(uniqueIDs, uid)
	}

	// 预加载所有用户邮箱
	emailMap := make(map[uint]string, len(uniqueIDs))
	if len(uniqueIDs) > 0 {
		var users []model.User
		db.WithContext(ctx).Where("id IN ?", uniqueIDs).Select("id, email").Find(&users)
		for i := range users {
			emailMap[users[i].ID] = users[i].Email
		}
	}

	results := make([]batchCreateResultItem, 0, len(uniqueIDs))
	successCount := 0
	for _, uid := range uniqueIDs {
		item := batchCreateResultItem{UserID: uid, UserEmail: emailMap[uid]}
		if _, exists := emailMap[uid]; !exists {
			item.Error = fmt.Sprintf("user %d not found", uid)
			results = append(results, item)
			continue
		}

		// 单用户事务:失效旧 active + 创建新
		err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			if err := tx.Model(&model.UserCommissionOverride{}).
				Where("user_id = ? AND is_active = ?", uid, true).
				Update("is_active", gorm.Expr("NULL")).Error; err != nil {
				return err
			}
			activeTrue := true
			o := model.UserCommissionOverride{
				UserID:               uid,
				IsActive:             &activeTrue,
				CommissionRate:       req.CommissionRate,
				AttributionDays:      req.AttributionDays,
				LifetimeCapCredits:   req.LifetimeCapCredits,
				MinPaidCreditsUnlock: req.MinPaidCreditsUnlock,
				EffectiveFrom:        effectiveFrom,
				EffectiveTo:          req.EffectiveTo,
				Note:                 req.Note,
				CreatedBy:            createdBy,
			}
			if err := tx.Create(&o).Error; err != nil {
				return err
			}
			item.OverrideID = o.ID
			return nil
		})
		if err != nil {
			item.Error = err.Error()
		} else {
			item.Success = true
			successCount++
		}
		results = append(results, item)
	}

	response.Success(c, gin.H{
		"total":        len(uniqueIDs),
		"successCount": successCount,
		"failedCount":  len(uniqueIDs) - successCount,
		"results":      results,
	})
}
