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

	"tokenhub-server/internal/model"
	"tokenhub-server/internal/pkg/credits"
	"tokenhub-server/internal/pkg/errcode"
	"tokenhub-server/internal/pkg/response"
	balancesvc "tokenhub-server/internal/service/balance"
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

		// 设置默认角色
		role := userReq.Role
		if role == "" {
			role = "USER"
		}
		// 验证角色是否合法
		validRoles := map[string]bool{"USER": true, "AGENT_L1": true, "AGENT_L2": true, "AGENT_L3": true, "ADMIN": true}
		if !validRoles[role] {
			role = "USER"
		}

		// 创建用户
		user := &model.User{
			TenantID:     defaultTenant.ID,
			Email:        userReq.Email,
			PasswordHash: string(hash),
			Name:         userReq.Name,
			Role:         role,
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

		// 如果是代理商角色，创建 UserAgentProfile
		if role != "USER" && role != "ADMIN" {
			if err := h.createAgentProfile(user.ID, role); err != nil {
				fmt.Printf("Create agent profile failed for user %d: %v\n", user.ID, err)
			}
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
	Role          string `json:"role" binding:"required"`            // 角色：USER/AGENT_L1/AGENT_L2/AGENT_L3/ADMIN
	AgentLevelCode string `json:"agent_level_code"`                  // 代理等级编码：A0/A1/A2/A3/A4
}

// UpdateUserRole 更新用户角色 PUT /api/v1/admin/users/:id/role
// 支持的角色: USER, AGENT_L1, AGENT_L2, AGENT_L3, ADMIN
// 逻辑：
// 1. 更新 User.Role 字段
// 2. 如果提升为代理商角色：检查/创建 UserAgentProfile
// 3. 如果从代理商降为普通用户：更新 UserAgentProfile.Status = "SUSPENDED"
// 4. 记录审计日志
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

	// 验证角色是否合法
	validRoles := map[string]bool{"USER": true, "AGENT_L1": true, "AGENT_L2": true, "AGENT_L3": true, "ADMIN": true}
	if !validRoles[req.Role] {
		response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrBadRequest.Code, "invalid role")
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

	oldRole := user.Role

	// 更新用户角色
	if err := h.db.Model(&user).Update("role", req.Role).Error; err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, err.Error())
		return
	}

	// 处理代理商档案
	isOldAgent := (oldRole == "AGENT_L1" || oldRole == "AGENT_L2" || oldRole == "AGENT_L3")
	isNewAgent := (req.Role == "AGENT_L1" || req.Role == "AGENT_L2" || req.Role == "AGENT_L3")

	if isNewAgent && !isOldAgent {
		// 提升为代理商：检查/创建 UserAgentProfile
		if err := h.createAgentProfile(uint(uid), req.Role); err != nil {
			// 记录错误但继续
			fmt.Printf("Create agent profile failed: %v\n", err)
		}
	} else if !isNewAgent && isOldAgent {
		// 从代理商降级：更新 UserAgentProfile.Status
		h.db.Model(&model.UserAgentProfile{}).
			Where("user_id = ?", uid).
			Update("status", "SUSPENDED")
	} else if isNewAgent && isOldAgent {
		// 代理商等级变更：更新 AgentLevelID
		if req.AgentLevelCode != "" {
			h.updateAgentLevel(uint(uid), req.AgentLevelCode)
		}
	}

	// 记录审计日志
	h.logAudit(c.Request.Context(), opID, "UPDATE_USER_ROLE", fmt.Sprintf("user:%d", uid), fmt.Sprintf("role changed from %s to %s", oldRole, req.Role))

	response.Success(c, gin.H{
		"message":  "role updated",
		"user_id":  uid,
		"old_role": oldRole,
		"new_role": req.Role,
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

// createAgentProfile 创建用户代理档案
func (h *BatchUserHandler) createAgentProfile(userID uint, role string) error {
	// 根据角色确定等级编码
	levelCode := "A0" // 默认推广员
	switch role {
	case "AGENT_L1":
		levelCode = "A1"
	case "AGENT_L2":
		levelCode = "A2"
	case "AGENT_L3":
		levelCode = "A3"
	}

	// 查询代理等级ID
	var agentLevel model.AgentLevel
	if err := h.db.Where("level_code = ?", levelCode).First(&agentLevel).Error; err != nil {
		// 如果等级不存在，使用默认等级
		if err := h.db.Where("level_code = ?", "A0").First(&agentLevel).Error; err != nil {
			return fmt.Errorf("agent level not found: %w", err)
		}
	}

	// 检查是否已存在档案
	var existing model.UserAgentProfile
	err := h.db.Where("user_id = ?", userID).First(&existing).Error
	if err == nil {
		// 已存在，更新状态
		return h.db.Model(&existing).Updates(map[string]interface{}{
			"agent_level_id": agentLevel.ID,
			"status":         "ACTIVE",
			"approved_at":    time.Now(),
		}).Error
	}
	if err != gorm.ErrRecordNotFound {
		return err
	}

	// 创建新档案
	profile := &model.UserAgentProfile{
		UserID:       userID,
		AgentLevelID: agentLevel.ID,
		Status:       "ACTIVE",
		AppliedAt:    time.Now(),
		ApprovedAt:   ptrTime(time.Now()),
	}
	return h.db.Create(profile).Error
}

// updateAgentLevel 更新代理商等级
func (h *BatchUserHandler) updateAgentLevel(userID uint, levelCode string) error {
	var agentLevel model.AgentLevel
	if err := h.db.Where("level_code = ?", levelCode).First(&agentLevel).Error; err != nil {
		return err
	}
	return h.db.Model(&model.UserAgentProfile{}).
		Where("user_id = ?", userID).
		Update("agent_level_id", agentLevel.ID).Error
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

// ptrTime 返回时间的指针
func ptrTime(t time.Time) *time.Time {
	return &t
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

	// 更新用户状态
	if err := h.db.Model(&user).Update("is_active", req.IsActive).Error; err != nil {
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
