package admin

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"

	"tokenhub-server/internal/middleware"
	"tokenhub-server/internal/model"
	"tokenhub-server/internal/pkg/credits"
	"tokenhub-server/internal/pkg/errcode"
	"tokenhub-server/internal/pkg/response"
	balancesvc "tokenhub-server/internal/service/balance"
	permissionsvc "tokenhub-server/internal/service/permission"
	referralsvc "tokenhub-server/internal/service/referral"
)

// BatchUserHandler 批量用户管理接口处理器
type BatchUserHandler struct {
	db         *gorm.DB
	balanceSvc *balancesvc.BalanceService
	referralSvc *referralsvc.ReferralService
}

// NewBatchUserHandler 创建批量用户管理Handler实例
func NewBatchUserHandler(db *gorm.DB) *BatchUserHandler {
	return &BatchUserHandler{
		db:          db,
		balanceSvc:  balancesvc.NewBalanceService(db, nil),
		referralSvc: referralsvc.NewReferralService(db),
	}
}

// BatchCreateUserRequest 单个用户创建请求
type BatchCreateUserRequest struct {
	Email          string `json:"email" binding:"required,email"`     // 用户邮箱
	Name           string `json:"name" binding:"required"`            // 用户名称
	Password       string `json:"password" binding:"required,min=8"`  // 密码（最少8位）
	Role           string `json:"role"`                               // 角色：USER/AGENT_L1/AGENT_L2/AGENT_L3/ADMIN
	InitialCredits int64  `json:"initial_credits"`                    // 初始积分（credits）
}

// BatchCreateRequest 批量创建请求
type BatchCreateRequest struct {
	Users []BatchCreateUserRequest `json:"users" binding:"required,dive"`
}

// BatchCreateResult 单个用户创建结果
type BatchCreateResult struct {
	Email  string `json:"email"`  // 用户邮箱
	Status string `json:"status"` // 状态：created/skipped/failed
	Reason string `json:"reason"` // 原因（跳过或失败时）
	UserID uint   `json:"user_id,omitempty"` // 成功时返回用户ID
}

// BatchCreateResponse 批量创建响应
type BatchCreateResponse struct {
	Total    int                  `json:"total"`    // 总数
	Created  int                  `json:"created"`  // 成功创建数
	Skipped  int                  `json:"skipped"`  // 跳过数（邮箱已存在）
	Failed   int                  `json:"failed"`   // 失败数
	Results  []BatchCreateResult  `json:"results"`  // 详细结果
}

// BatchCreateUsers 批量创建用户 POST /api/v1/admin/users/batch
// 逻辑：
// 1. 检查每个邮箱是否已存在，跳过已存在的
// 2. 密码使用 bcrypt 哈希
// 3. 创建用户后自动初始化 UserBalance
// 4. 自动生成邀请码
// 5. 返回成功/失败/跳过的数量和详情
func (h *BatchUserHandler) BatchCreateUsers(c *gin.Context) {
	var req BatchCreateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrBadRequest.Code, err.Error())
		return
	}

	if len(req.Users) == 0 {
		response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrBadRequest.Code, "users list is empty")
		return
	}

	// 限制单次批量创建数量
	if len(req.Users) > 100 {
		response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrBadRequest.Code, "max 100 users per batch")
		return
	}

	// 获取操作人ID
	operatorID, _ := c.Get("userId")
	opID, _ := operatorID.(uint)

	// 获取默认租户ID
	var defaultTenant model.Tenant
	if err := h.db.Where("parent_id IS NULL AND level = 1").First(&defaultTenant).Error; err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, "no default tenant available")
		return
	}

	result := BatchCreateResponse{
		Total:   len(req.Users),
		Results: make([]BatchCreateResult, 0, len(req.Users)),
	}

	// 逐个处理用户
	for _, userReq := range req.Users {
		createResult := BatchCreateResult{
			Email: userReq.Email,
		}

		// 检查邮箱是否已存在
		var existingCount int64
		if err := h.db.Model(&model.User{}).Where("email = ?", userReq.Email).Count(&existingCount).Error; err != nil {
			createResult.Status = "failed"
			createResult.Reason = "database error"
			result.Failed++
			result.Results = append(result.Results, createResult)
			continue
		}
		if existingCount > 0 {
			createResult.Status = "skipped"
			createResult.Reason = "email already exists"
			result.Skipped++
			result.Results = append(result.Results, createResult)
			continue
		}

		// 哈希密码
		hash, err := bcrypt.GenerateFromPassword([]byte(userReq.Password), 12)
		if err != nil {
			createResult.Status = "failed"
			createResult.Reason = "password hash failed"
			result.Failed++
			result.Results = append(result.Results, createResult)
			continue
		}

		// v4.0: 角色通过 user_roles 表分配，legacy role 字段不再写入
		// 支持传入 USER/ADMIN，映射到 USER/SUPER_ADMIN 角色 code
		requestedRole := userReq.Role
		if requestedRole == "" {
			requestedRole = "USER"
		}
		roleMapping := map[string]string{"USER": "USER", "ADMIN": "SUPER_ADMIN"}
		targetRoleCode, ok := roleMapping[requestedRole]
		if !ok {
			targetRoleCode = "USER"
		}

		// 创建用户
		user := &model.User{
			TenantID:     defaultTenant.ID,
			Email:        userReq.Email,
			PasswordHash: string(hash),
			Name:         userReq.Name,
			IsActive:     true,
			Language:     "zh",
		}

		if err := h.db.Create(user).Error; err != nil {
			createResult.Status = "failed"
			createResult.Reason = "create user failed: " + err.Error()
			result.Failed++
			result.Results = append(result.Results, createResult)
			continue
		}

		// v4.0: 分配角色到 user_roles 表
		var targetRoleID uint
		if err := h.db.Table("roles").
			Where("code = ? AND deleted_at IS NULL", targetRoleCode).
			Select("id").Scan(&targetRoleID).Error; err == nil && targetRoleID != 0 {
			_ = h.db.Create(&model.UserRole{
				UserID: user.ID, RoleID: targetRoleID, GrantedBy: 0, GrantedAt: time.Now(),
			}).Error
		}

		// 初始化用户余额
		if err := h.balanceSvc.InitBalance(context.Background(), user.ID, user.TenantID); err != nil {
			// 记录日志但不影响用户创建
			fmt.Printf("InitBalance failed for user %d: %v\n", user.ID, err)
		}

		// 如果有初始积分，则充值
		if userReq.InitialCredits > 0 {
			if _, err := h.balanceSvc.Recharge(context.Background(), user.ID, user.TenantID, userReq.InitialCredits, "管理员批量充值", ""); err != nil {
				fmt.Printf("Initial credits recharge failed for user %d: %v\n", user.ID, err)
			}
		}

		// 生成邀请码
		if _, err := h.referralSvc.GetOrCreateLink(context.Background(), user.ID, user.TenantID); err != nil {
			fmt.Printf("Generate referral code failed for user %d: %v\n", user.ID, err)
		}

		createResult.Status = "created"
		createResult.UserID = user.ID
		result.Created++
		result.Results = append(result.Results, createResult)
	}

	// 记录审计日志
	h.logAudit(c.Request.Context(), opID, "BATCH_CREATE_USERS", "users", fmt.Sprintf("created=%d, skipped=%d, failed=%d", result.Created, result.Skipped, result.Failed))

	response.Success(c, result)
}

// UpdateRoleRequest 角色更新请求
type UpdateRoleRequest struct {
	Role string `json:"role" binding:"required"` // 角色：USER/ADMIN
}

// UpdateUserRole 更新用户角色 PUT /api/v1/admin/users/:id/role
// v4.0: 改为操作 user_roles 表。legacy 角色字符串 → role.code 映射：
//   ADMIN → SUPER_ADMIN；USER → USER
// 其他自定义角色分配请用 Phase 6 的 /admin/users/:id/roles 端点。
func (h *BatchUserHandler) UpdateUserRole(c *gin.Context) {
	userIDStr := c.Param("id")
	uid, err := strconv.ParseUint(userIDStr, 10, 64)
	if err != nil || uid == 0 {
		response.Error(c, http.StatusBadRequest, errcode.ErrBadRequest)
		return
	}

	var req UpdateRoleRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrBadRequest.Code, err.Error())
		return
	}

	// v4.1: 接受任意 role code（向下兼容 legacy "user"/"admin" 小写）
	roleCodeInput := req.Role
	legacyMap := map[string]string{"user": "USER", "admin": "SUPER_ADMIN"}
	if mapped, ok := legacyMap[roleCodeInput]; ok {
		roleCodeInput = mapped
	}
	newRoleCode := roleCodeInput
	if newRoleCode == "" {
		response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrBadRequest.Code, "role code is required")
		return
	}

	operatorID, _ := c.Get("userId")
	opID, _ := operatorID.(uint)

	// 查询目标用户是否存在 + 查当前角色（用于审计）
	var user model.User
	if err := h.db.First(&user, uid).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			response.ErrorMsg(c, http.StatusNotFound, errcode.ErrUserNotFound.Code, "user not found")
			return
		}
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, err.Error())
		return
	}
	// 受保护管理员账号守卫: 角色变更视为高危,即使 admin 本人也禁止
	// (避免把自己从 SUPER_ADMIN 降级导致后续无法管理)
	if middleware.ShouldBlockProtectedAdminCritical(user.Email) {
		response.ErrorMsg(c, http.StatusForbidden, 40301,
			"cannot modify role of protected admin account; this is a critical operation forbidden by CLAUDE.md covenant")
		return
	}
	var oldRoleCodes []string
	h.db.Table("roles").
		Joins("JOIN user_roles ON roles.id = user_roles.role_id").
		Where("user_roles.user_id = ?", uid).
		Pluck("roles.code", &oldRoleCodes)

	// 查询目标角色 ID
	var newRoleID uint
	if err := h.db.Table("roles").
		Where("code = ? AND deleted_at IS NULL", newRoleCode).
		Select("id").Scan(&newRoleID).Error; err != nil || newRoleID == 0 {
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, "target role not found in DB")
		return
	}

	// 事务：删除旧 user_roles，插入新 user_roles
	tx := h.db.Begin()
	if err := tx.Where("user_id = ?", uid).Delete(&model.UserRole{}).Error; err != nil {
		tx.Rollback()
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, err.Error())
		return
	}
	ur := model.UserRole{UserID: uint(uid), RoleID: newRoleID, GrantedBy: opID, GrantedAt: time.Now()}
	if err := tx.Create(&ur).Error; err != nil {
		tx.Rollback()
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, err.Error())
		return
	}
	if err := tx.Commit().Error; err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, err.Error())
		return
	}

	// 显式失效 Redis 缓存，让下次请求走 DB 重建
	if permissionsvc.Default != nil {
		_ = permissionsvc.Default.InvalidateUserPerms(c.Request.Context(), uint(uid))
	}

	h.logAudit(c.Request.Context(), opID, "UPDATE_USER_ROLE", fmt.Sprintf("user:%d", uid),
		fmt.Sprintf("roles changed from %v to [%s]", oldRoleCodes, newRoleCode))

	response.Success(c, gin.H{
		"message":   "role updated",
		"user_id":   uid,
		"old_roles": oldRoleCodes,
		"new_role":  newRoleCode,
	})
}

// RechargeRMBRequest RMB充值请求
type RechargeRMBRequest struct {
	AmountRMB float64 `json:"amount_rmb" binding:"required,gt=0"` // 充值金额（人民币）
	Remark    string  `json:"remark"`                            // 备注
}

// RechargeRMBResponse RMB充值响应
type RechargeRMBResponse struct {
	UserID        uint    `json:"user_id"`        // 用户ID
	AmountRMB     float64 `json:"amount_rmb"`     // 充值金额（人民币）
	Credits       int64   `json:"credits"`        // 换算积分
	Balance       int64   `json:"balance"`        // 充值后余额（积分）
	BalanceRMB    float64 `json:"balance_rmb"`    // 充值后余额（人民币）
	FreeQuota     int64   `json:"free_quota"`     // 免费额度（积分）
	TotalBalance  int64   `json:"total_balance"`  // 总可用余额（积分）
}

// RechargeUserRMB 管理员为用户充值RMB POST /api/v1/admin/users/:id/recharge-rmb
// 逻辑：
// 1. 接收 RMB 金额
// 2. 换算为积分：credits = int64(amount_rmb * 10000)
// 3. 调用 BalanceService.Recharge() 增加用户余额
// 4. 创建 BalanceRecord 记录
// 5. 记录审计日志
// 6. 返回充值结果
func (h *BatchUserHandler) RechargeUserRMB(c *gin.Context) {
	userIDStr := c.Param("id")
	uid, err := strconv.ParseUint(userIDStr, 10, 64)
	if err != nil || uid == 0 {
		response.Error(c, http.StatusBadRequest, errcode.ErrBadRequest)
		return
	}

	var req RechargeRMBRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrBadRequest.Code, err.Error())
		return
	}

	// 获取操作人ID
	operatorID, _ := c.Get("userId")
	opID, _ := operatorID.(uint)

	// 获取用户信息
	var user model.User
	if err := h.db.First(&user, uid).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			response.ErrorMsg(c, http.StatusNotFound, errcode.ErrUserNotFound.Code, "user not found")
			return
		}
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, err.Error())
		return
	}

	// RMB 转换为积分（1 RMB = 10000 credits）
	creditsAmount := credits.RMBToCredits(req.AmountRMB)

	// 获取租户ID
	tenantID, _ := c.Get("tenantId")
	tid, _ := tenantID.(uint)
	if tid == 0 {
		tid = user.TenantID
	}

	// 构造备注
	remark := req.Remark
	if remark == "" {
		remark = fmt.Sprintf("管理员充值 ¥%.4f", req.AmountRMB)
	}

	// 调用充值服务
	ub, err := h.balanceSvc.Recharge(context.Background(), uint(uid), tid, creditsAmount, remark, "")
	if err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, err.Error())
		return
	}

	// 记录审计日志
	h.logAudit(c.Request.Context(), opID, "ADMIN_RECHARGE_RMB", fmt.Sprintf("user:%d", uid), fmt.Sprintf("amount=%.4f RMB, credits=%d", req.AmountRMB, creditsAmount))

	response.Success(c, RechargeRMBResponse{
		UserID:       uint(uid),
		AmountRMB:    req.AmountRMB,
		Credits:      creditsAmount,
		Balance:      ub.Balance,
		BalanceRMB:   ub.BalanceRMB,
		FreeQuota:    ub.FreeQuota,
		TotalBalance: ub.Balance + ub.FreeQuota,
	})
}

// logAudit 记录审计日志
func (h *BatchUserHandler) logAudit(ctx context.Context, userID uint, action, resource, details string) {
	log := &model.AuditLog{
		UserID:   userID,
		Action:   action,
		Resource: resource,
		Details:  model.JSON(details),
	}
	_ = h.db.WithContext(ctx).Create(log).Error
}

// UpdateUserStatusRequest 用户状态更新请求
type UpdateUserStatusRequest struct {
	IsActive bool `json:"is_active"` // 是否启用
}

// UpdateUserStatus 更新用户状态 PUT /api/v1/admin/users/:id/status
// 逻辑：
// 1. 更新 User.IsActive 字段
// 2. 记录审计日志
func (h *BatchUserHandler) UpdateUserStatus(c *gin.Context) {
	userIDStr := c.Param("id")
	uid, err := strconv.ParseUint(userIDStr, 10, 64)
	if err != nil || uid == 0 {
		response.Error(c, http.StatusBadRequest, errcode.ErrBadRequest)
		return
	}

	var req UpdateUserStatusRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrBadRequest.Code, err.Error())
		return
	}

	// 获取操作人ID
	operatorID, _ := c.Get("userId")
	opID, _ := operatorID.(uint)

	// 获取用户信息
	var user model.User
	if err := h.db.First(&user, uid).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			response.ErrorMsg(c, http.StatusNotFound, errcode.ErrUserNotFound.Code, "user not found")
			return
		}
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, err.Error())
		return
	}
	// 受保护管理员账号守卫: 即使 admin 本人也禁止禁用自己 (避免账号被锁死)
	if !req.IsActive && middleware.ShouldBlockProtectedAdminCritical(user.Email) {
		response.ErrorMsg(c, http.StatusForbidden, 40301,
			"cannot deactivate protected admin account; this is a critical operation forbidden by CLAUDE.md covenant")
		return
	}

	// 更新用户状态
	if err := h.db.Model(&model.User{}).Where("id = ?", user.ID).Update("is_active", req.IsActive).Error; err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, err.Error())
		return
	}

	// 记录审计日志
	statusStr := "enabled"
	if !req.IsActive {
		statusStr = "disabled"
	}
	h.logAudit(c.Request.Context(), opID, "UPDATE_USER_STATUS", fmt.Sprintf("user:%d", uid), fmt.Sprintf("status changed to %s", statusStr))

	response.Success(c, gin.H{
		"message":   "status updated",
		"user_id":   uid,
		"is_active": req.IsActive,
	})
}
