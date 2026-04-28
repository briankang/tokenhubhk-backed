// Package middleware - admin account protection
//
// 受保护管理员账号 (admin@tokenhubhk.com) 的纵深防御层。
//
// 设计原则:
//  1. 拦截"非 admin 本人"通过业务 API 修改 admin 账号的敏感字段(密码/邮箱/角色/is_active)
//  2. 放行"admin 本人"对自己的合法修改(自助改密 / 个人设置页改邮箱等)
//  3. Delete 和 SUPER_ADMIN 角色撤销视为高风险,即使 admin 本人也禁止
//
// 与权限系统的关系:
//   - PermissionGate 校验"调用方是否有 user_update 这类操作权限码"
//   - 此守卫额外增加"目标用户是不是受保护账号"的二次校验
//   - 两层守卫互不替代,共同构成纵深防御
//
// 详见 CLAUDE.md 顶部《管理员账号固定公约》。
package middleware

import (
	"strings"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"tokenhub-server/internal/model"
	"tokenhub-server/internal/pkg/ctxutil"
)

// ProtectedAdminEmail 是受保护的固定管理员账号邮箱。
// 该值与 backend/internal/database/seed.go 的 adminEmail 常量保持一致。
const ProtectedAdminEmail = "admin@tokenhubhk.com"

// IsProtectedAdminEmail 判断给定邮箱是否为受保护账号。
// 大小写不敏感,会做 trim 空格处理。
func IsProtectedAdminEmail(email string) bool {
	return strings.EqualFold(strings.TrimSpace(email), ProtectedAdminEmail)
}

// IsCurrentUserProtectedAdmin 判断当前 JWT 持有者本人是否为受保护账号。
// 任何错误(JWT 缺失 / 用户不存在 / DB 异常)均返回 false,由调用方按"非 admin 本人"处理。
func IsCurrentUserProtectedAdmin(c *gin.Context, db *gorm.DB) bool {
	if db == nil {
		return false
	}
	uid, ok := ctxutil.UserID(c)
	if !ok || uid == 0 {
		return false
	}
	var u model.User
	if err := db.Select("email").Where("id = ?", uid).First(&u).Error; err != nil {
		return false
	}
	return IsProtectedAdminEmail(u.Email)
}

// ShouldBlockProtectedAdminWrite 判断是否应该拦截一个针对受保护账号的写操作。
//
// 返回 true 表示调用方应该立即返回 403。
// 返回 false 表示该写操作可以继续执行。
//
// 拦截规则:
//   - 目标不是受保护账号 → 放行 (不干涉普通用户的管理)
//   - 目标是受保护账号 且 当前调用者就是 admin 本人 → 放行 (允许 admin 自助改自己)
//   - 目标是受保护账号 且 当前调用者不是 admin 本人 → 拦截
//
// 适用场景: PUT /admin/users/:id (改密码/邮箱/is_active 等普通字段)。
// 不适用于 Delete 和角色撤销,后者请使用 ShouldBlockProtectedAdminCritical。
func ShouldBlockProtectedAdminWrite(c *gin.Context, db *gorm.DB, targetEmail string) bool {
	if !IsProtectedAdminEmail(targetEmail) {
		return false
	}
	if IsCurrentUserProtectedAdmin(c, db) {
		return false
	}
	return true
}

// ShouldBlockProtectedAdminCritical 判断是否应该拦截一个针对受保护账号的高危写操作。
//
// 返回 true 表示调用方应该立即返回 403。
// 与 ShouldBlockProtectedAdminWrite 的区别:
//   - 即使 admin 本人也无法对自己执行此类操作 (避免账号被自我损坏)
//
// 适用场景: 删除 admin / 撤销 admin 的 SUPER_ADMIN 角色 / 把 admin 设为 inactive。
func ShouldBlockProtectedAdminCritical(targetEmail string) bool {
	return IsProtectedAdminEmail(targetEmail)
}
