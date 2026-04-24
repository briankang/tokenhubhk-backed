package user

import (
	"context"
	"net/http"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"tokenhub-server/internal/pkg/errcode"
	"tokenhub-server/internal/pkg/logger"
	"tokenhub-server/internal/pkg/response"
	permissionsvc "tokenhub-server/internal/service/permission"
	"tokenhub-server/internal/service/usercache"
	usersvc "tokenhub-server/internal/service/user"
)

// ProfileHandler 用户个人资料接口处理器
type ProfileHandler struct {
	svc      *usersvc.UserService
	resolver *permissionsvc.Resolver // 可选；为 nil 时 /profile 响应不含 permissions 字段
}

// NewProfileHandler 创建用户资料Handler实例
func NewProfileHandler(svc *usersvc.UserService) *ProfileHandler {
	if svc == nil {
		panic("profile handler: service is nil")
	}
	return &ProfileHandler{svc: svc}
}

// SetResolver 注入权限解析器（可选，router.go 在 redis 就绪后调用）
func (h *ProfileHandler) SetResolver(r *permissionsvc.Resolver) {
	h.resolver = r
}

// GetProfile 获取用户个人资料 GET /api/v1/user/profile
// v4.0: 响应中透出 permissions/data_scope/role_codes 字段，供前端 RBAC UI 消费
// 缓存：user:profile:{uid}，5min TTL；UpdateProfile / ChangePassword / 角色变更时失效
func (h *ProfileHandler) GetProfile(c *gin.Context) {
	userID, ok := c.Get("userId")
	if !ok {
		response.Error(c, http.StatusUnauthorized, errcode.ErrUnauthorized)
		return
	}

	uid, ok := userID.(uint)
	if !ok {
		response.Error(c, http.StatusUnauthorized, errcode.ErrUnauthorized)
		return
	}

	out, err := usercache.GetOrLoadProfile[map[string]any](c.Request.Context(), uid, func(ctx context.Context) (map[string]any, error) {
		return h.buildProfile(ctx, uid)
	})
	if err != nil {
		response.ErrorMsg(c, http.StatusNotFound, errcode.ErrUserNotFound.Code, err.Error())
		return
	}

	response.Success(c, out)
}

// buildProfile 聚合用户信息 + 权限（供 loader 回源使用）
func (h *ProfileHandler) buildProfile(ctx context.Context, uid uint) (map[string]any, error) {
	user, err := h.svc.GetByID(ctx, uid)
	if err != nil {
		return nil, err
	}

	// 构造响应（用 map 方式注入 user 字段 + 权限字段，避免改 model.User 结构）
	out := map[string]any{
		"id":            user.ID,
		"tenant_id":     user.TenantID,
		"email":         user.Email,
		"name":          user.Name,
		// v4.0: user.Role 字段已移除；前端应改用 role_codes[]
		"is_active":     user.IsActive,
		"language":      user.Language,
		"country_code":  user.CountryCode, // v5.0 注册地区，只读
		"last_login_at": user.LastLoginAt,
		"referral_code": user.ReferralCode,
		"created_at":    user.CreatedAt,
		"updated_at":    user.UpdatedAt,
	}

	// v4.0: 附加权限信息（Resolver 未就绪时静默降级）
	resolver := h.resolver
	if resolver == nil {
		resolver = permissionsvc.Default
	}
	if resolver != nil {
		perms, resolveErr := resolver.Resolve(ctx, uid)
		if resolveErr != nil {
			if logger.L != nil {
				logger.L.Warn("profile: resolve permissions failed",
					zap.Uint("user_id", uid),
					zap.Error(resolveErr),
				)
			}
			out["permissions"] = []string{}
			out["data_scope"] = map[string]any{"type": permissionsvc.DataScopeOwnOnly}
			out["role_codes"] = []string{}
		} else {
			out["permissions"] = perms.Codes
			out["data_scope"] = perms.DataScope
			out["role_codes"] = perms.RoleCodes
		}
	}
	return out, nil
}

// UpdateProfile 更新用户个人资料 PUT /api/v1/user/profile
func (h *ProfileHandler) UpdateProfile(c *gin.Context) {
	userID, ok := c.Get("userId")
	if !ok {
		response.Error(c, http.StatusUnauthorized, errcode.ErrUnauthorized)
		return
	}

	uid, ok := userID.(uint)
	if !ok {
		response.Error(c, http.StatusUnauthorized, errcode.ErrUnauthorized)
		return
	}

	var req struct {
		Name     string `json:"name"`
		Language string `json:"language"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Error(c, http.StatusBadRequest, errcode.ErrValidation)
		return
	}

	if err := h.svc.UpdateProfile(c.Request.Context(), uid, req.Name, req.Language); err != nil {
		response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrBadRequest.Code, err.Error())
		return
	}

	// 失效 profile 缓存（service 层也会调用，handler 兜底）
	usercache.InvalidateProfile(c.Request.Context(), uid)

	response.Success(c, gin.H{"message": "profile updated"})
}

// ChangePassword 修改用户密码 PUT /api/v1/user/password
func (h *ProfileHandler) ChangePassword(c *gin.Context) {
	userID, ok := c.Get("userId")
	if !ok {
		response.Error(c, http.StatusUnauthorized, errcode.ErrUnauthorized)
		return
	}

	uid, ok := userID.(uint)
	if !ok {
		response.Error(c, http.StatusUnauthorized, errcode.ErrUnauthorized)
		return
	}

	var req struct {
		OldPassword string `json:"old_password" binding:"required"`
		NewPassword string `json:"new_password" binding:"required,min=8"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Error(c, http.StatusBadRequest, errcode.ErrValidation)
		return
	}

	if err := h.svc.ChangePassword(c.Request.Context(), uid, req.OldPassword, req.NewPassword); err != nil {
		response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrBadRequest.Code, err.Error())
		return
	}

	// 密码变更后失效全部用户缓存（profile 含 role / 下次重新拉）
	usercache.InvalidateAll(c.Request.Context(), uid)

	response.Success(c, gin.H{"message": "password changed"})
}
