package user

import (
	"context"
	"errors"
	"fmt"

	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"

	"tokenhub-server/internal/model"
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
func (s *UserService) List(ctx context.Context, tenantID uint, page, pageSize int) ([]model.User, int64, error) {
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

// Deactivate 停用指定用户账号
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
	return nil
}
