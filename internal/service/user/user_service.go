package user

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"

	"tokenhub-server/internal/model"
	"tokenhub-server/internal/service/usercache"
)

const bcryptCost = 12

// UserService 用户管理服务，处理用户的增删改查操作
type UserService struct {
	db *gorm.DB
}

// NewUserService 创建用户服务实例，db 不能为 nil 否则 panic
func NewUserService(db *gorm.DB) *UserService {
	if db == nil {
		panic("user service: db is nil")
	}
	return &UserService{db: db}
}

// GetByID 根据用户 ID 查询用户信息
func (s *UserService) GetByID(ctx context.Context, id uint) (*model.User, error) {
	if id == 0 {
		return nil, fmt.Errorf("user id is required")
	}
	var user model.User
	if err := s.db.WithContext(ctx).First(&user, id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, fmt.Errorf("user not found")
		}
		return nil, fmt.Errorf("failed to get user: %w", err)
	}
	return &user, nil
}

// List 分页查询用户列表，可按租户 ID 过滤
func (s *UserService) List(ctx context.Context, tenantID uint, search string, page, pageSize int) ([]model.User, int64, error) {
	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > 100 {
		pageSize = 20
	}

	var total int64
	query := s.db.WithContext(ctx).Model(&model.User{})
	if tenantID > 0 {
		query = query.Where("tenant_id = ?", tenantID)
	}
	search = strings.TrimSpace(search)
	if search != "" {
		like := "%" + escapeLike(search) + "%"
		if id, err := strconv.ParseUint(search, 10, 64); err == nil && id > 0 {
			query = query.Where("id = ? OR email LIKE ? OR name LIKE ?", id, like, like)
		} else {
			query = query.Where("email LIKE ? OR name LIKE ?", like, like)
		}
	}

	if err := query.Count(&total).Error; err != nil {
		return nil, 0, fmt.Errorf("failed to count users: %w", err)
	}

	var users []model.User
	offset := (page - 1) * pageSize
	if err := query.Offset(offset).Limit(pageSize).Order("id DESC").Find(&users).Error; err != nil {
		return nil, 0, fmt.Errorf("failed to list users: %w", err)
	}

	return users, total, nil
}

// Update 根据用户 ID 更新指定字段，禁止直接修改敏感字段
func escapeLike(value string) string {
	value = strings.ReplaceAll(value, "\\", "\\\\")
	value = strings.ReplaceAll(value, "%", "\\%")
	value = strings.ReplaceAll(value, "_", "\\_")
	return value
}

func (s *UserService) Update(ctx context.Context, id uint, updates map[string]interface{}) error {
	if id == 0 {
		return fmt.Errorf("user id is required")
	}
	if len(updates) == 0 {
		return fmt.Errorf("no updates provided")
	}

	// 禁止直接修改敏感字段
	delete(updates, "password_hash")
	delete(updates, "id")

	result := s.db.WithContext(ctx).Model(&model.User{}).Where("id = ?", id).Updates(updates)
	if result.Error != nil {
		return fmt.Errorf("failed to update user: %w", result.Error)
	}
	if result.RowsAffected == 0 {
		return fmt.Errorf("user not found")
	}
	return nil
}

// Deactivate 停用指定用户账号（仅置 is_active=0,保留 email/数据,可恢复）
//
// 用于"暂停账号但保留所有信息"场景。被停用账号无法登录,但 email 仍占用唯一索引槽位,
// 同邮箱无法用于新账号注册。如需释放邮箱请用 Delete。
func (s *UserService) Deactivate(ctx context.Context, id uint) error {
	if id == 0 {
		return fmt.Errorf("user id is required")
	}
	result := s.db.WithContext(ctx).Model(&model.User{}).Where("id = ?", id).Update("is_active", false)
	if result.Error != nil {
		return fmt.Errorf("failed to deactivate user: %w", result.Error)
	}
	if result.RowsAffected == 0 {
		return fmt.Errorf("user not found")
	}
	return nil
}

// Delete 软删除用户并释放原邮箱(tombstone 模式)
//
// 实现:在同一事务内
//  1. 把 email 改写成 deleted_<id>_<unix_ts>@deleted.tombstone(占住唯一索引槽位但与真实邮箱完全隔离)
//  2. 把 referral_code 改成 deleted_<id>_<unix_ts>(防止它被新注册者复用)
//  3. is_active=0
//  4. GORM 软删除(自动写 deleted_at)
//
// 副作用:
//   - 原 email 立即可被新注册者复用
//   - 原 referral_code 不再有效(归因/邀请校验默认 WHERE deleted_at IS NULL)
//   - 关联的 referral_attributions / user_balances / api_keys 等不动,保留审计与历史归因
//   - 受 GORM 软删除特性影响,GetByID/List 默认不会返回该用户
//
// 该方法是 Delete 的目标语义("彻底删除/释放邮箱"),与 Deactivate(暂停)语义区分。
func (s *UserService) Delete(ctx context.Context, id uint) error {
	if id == 0 {
		return fmt.Errorf("user id is required")
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// 先确认用户存在(否则 RowsAffected=0 时无法区分"已删除"和"不存在")
		var user model.User
		if err := tx.First(&user, id).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return fmt.Errorf("user not found")
			}
			return fmt.Errorf("failed to load user: %w", err)
		}

		// Step 1+2+3: tombstone email + referral_code, 停用
		ts := time.Now().Unix()
		tombstoneEmail := fmt.Sprintf("deleted_%d_%d@deleted.tombstone", id, ts)
		tombstoneRef := fmt.Sprintf("deleted_%d_%d", id, ts)
		updates := map[string]interface{}{
			"email":         tombstoneEmail,
			"referral_code": tombstoneRef,
			"is_active":     false,
		}
		if err := tx.Model(&model.User{}).Where("id = ?", id).Updates(updates).Error; err != nil {
			return fmt.Errorf("failed to tombstone user: %w", err)
		}

		// Step 4: 软删除(GORM 自动写 deleted_at)
		if err := tx.Where("id = ?", id).Delete(&model.User{}).Error; err != nil {
			return fmt.Errorf("failed to soft delete user: %w", err)
		}
		return nil
	})
}

// EmailExists 查询邮箱是否已被注册
//
// 仅返回是否存在,不暴露用户 ID、姓名等敏感信息;供前端注册页 onBlur 预检使用。
// GORM 默认 WHERE deleted_at IS NULL,所以软删除的 tombstone 用户不会命中。
func (s *UserService) EmailExists(ctx context.Context, email string) (bool, error) {
	email = strings.TrimSpace(strings.ToLower(email))
	if email == "" {
		return false, fmt.Errorf("email is required")
	}
	var count int64
	if err := s.db.WithContext(ctx).Model(&model.User{}).Where("LOWER(email) = ?", email).Count(&count).Error; err != nil {
		return false, fmt.Errorf("failed to check email existence: %w", err)
	}
	return count > 0, nil
}

// ChangePassword 修改密码，验证旧密码后设置新密码（最少 8 位）
func (s *UserService) ChangePassword(ctx context.Context, id uint, oldPwd, newPwd string) error {
	if id == 0 {
		return fmt.Errorf("user id is required")
	}
	if oldPwd == "" || newPwd == "" {
		return fmt.Errorf("both old and new passwords are required")
	}
	if len(newPwd) < 8 {
		return fmt.Errorf("new password must be at least 8 characters")
	}

	var user model.User
	if err := s.db.WithContext(ctx).First(&user, id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return fmt.Errorf("user not found")
		}
		return fmt.Errorf("failed to get user: %w", err)
	}

	if err := bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(oldPwd)); err != nil {
		return fmt.Errorf("incorrect old password")
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(newPwd), bcryptCost)
	if err != nil {
		return fmt.Errorf("failed to hash new password: %w", err)
	}

	if err := s.db.WithContext(ctx).Model(&model.User{}).Where("id = ?", user.ID).Update("password_hash", string(hash)).Error; err != nil {
		return fmt.Errorf("failed to update password: %w", err)
	}
	usercache.InvalidateAll(ctx, id)
	return nil
}

// UpdateProfile 更新用户的姓名和语言偏好
func (s *UserService) UpdateProfile(ctx context.Context, id uint, name, language string) error {
	if id == 0 {
		return fmt.Errorf("user id is required")
	}

	updates := make(map[string]interface{})
	if name != "" {
		updates["name"] = name
	}
	if language != "" {
		updates["language"] = language
	}
	if len(updates) == 0 {
		return nil
	}

	result := s.db.WithContext(ctx).Model(&model.User{}).Where("id = ?", id).Updates(updates)
	if result.Error != nil {
		return fmt.Errorf("failed to update profile: %w", result.Error)
	}
	if result.RowsAffected == 0 {
		return fmt.Errorf("user not found")
	}
	usercache.InvalidateProfile(ctx, id)
	return nil
}
