package payment

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"os"

	"go.uber.org/zap"
	"gorm.io/gorm"

	"tokenhub-server/internal/model"
	"tokenhub-server/internal/pkg/logger"
)

// 默认加密密钥（32字节，AES-256）
const defaultEncryptKey = "tokenhub-payment-secret-key-32b!"

// PaymentConfigService 支付配置管理服务
type PaymentConfigService struct {
	db         *gorm.DB
	encryptKey []byte
}

// NewPaymentConfigService 创建支付配置管理服务实例
func NewPaymentConfigService(db *gorm.DB) *PaymentConfigService {
	key := os.Getenv("PAYMENT_ENCRYPT_KEY")
	if key == "" {
		key = defaultEncryptKey
	}
	// 确保密钥长度为 32 字节
	keyBytes := []byte(key)
	if len(keyBytes) < 32 {
		padded := make([]byte, 32)
		copy(padded, keyBytes)
		keyBytes = padded
	} else if len(keyBytes) > 32 {
		keyBytes = keyBytes[:32]
	}
	return &PaymentConfigService{db: db, encryptKey: keyBytes}
}

// ==================== AES-256-GCM 加密/解密 ====================

// encrypt 使用 AES-256-GCM 加密明文
func (s *PaymentConfigService) encrypt(plaintext string) (string, error) {
	if plaintext == "" {
		return "", nil
	}
	block, err := aes.NewCipher(s.encryptKey)
	if err != nil {
		return "", fmt.Errorf("创建 AES cipher 失败: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("创建 GCM 失败: %w", err)
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", fmt.Errorf("生成 nonce 失败: %w", err)
	}
	ciphertext := gcm.Seal(nonce, nonce, []byte(plaintext), nil)
	return base64.StdEncoding.EncodeToString(ciphertext), nil
}

// decrypt 使用 AES-256-GCM 解密密文
func (s *PaymentConfigService) decrypt(encoded string) (string, error) {
	if encoded == "" {
		return "", nil
	}
	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return "", fmt.Errorf("base64 解码失败: %w", err)
	}
	block, err := aes.NewCipher(s.encryptKey)
	if err != nil {
		return "", fmt.Errorf("创建 AES cipher 失败: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("创建 GCM 失败: %w", err)
	}
	nonceSize := gcm.NonceSize()
	if len(data) < nonceSize {
		return "", fmt.Errorf("密文长度不足")
	}
	nonce, ciphertext := data[:nonceSize], data[nonceSize:]
	plaintext, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return "", fmt.Errorf("解密失败: %w", err)
	}
	return string(plaintext), nil
}

// ==================== 支付渠道配置 CRUD ====================

// GetAllProviders 获取所有支付渠道配置
func (s *PaymentConfigService) GetAllProviders(ctx context.Context) ([]model.PaymentProviderConfig, error) {
	var providers []model.PaymentProviderConfig
	if err := s.db.WithContext(ctx).Order("sort_order ASC, id ASC").Find(&providers).Error; err != nil {
		return nil, err
	}
	// 解密配置，返回给管理员
	for i := range providers {
		if providers[i].ConfigJSON != "" {
			decrypted, err := s.decrypt(providers[i].ConfigJSON)
			if err != nil {
				logger.L.Warn("解密支付配置失败", zap.String("provider", providers[i].ProviderType), zap.Error(err))
				providers[i].ConfigJSON = "{}"
			} else {
				providers[i].ConfigJSON = decrypted
			}
		}
	}
	return providers, nil
}

// GetProvider 获取单个支付渠道配置
func (s *PaymentConfigService) GetProvider(ctx context.Context, providerType string) (*model.PaymentProviderConfig, error) {
	var provider model.PaymentProviderConfig
	if err := s.db.WithContext(ctx).Where("provider_type = ?", providerType).First(&provider).Error; err != nil {
		return nil, err
	}
	if provider.ConfigJSON != "" {
		decrypted, err := s.decrypt(provider.ConfigJSON)
		if err != nil {
			logger.L.Warn("解密支付配置失败", zap.String("provider", providerType), zap.Error(err))
			provider.ConfigJSON = "{}"
		} else {
			provider.ConfigJSON = decrypted
		}
	}
	return &provider, nil
}

// UpdateProvider 更新支付渠道配置
func (s *PaymentConfigService) UpdateProvider(ctx context.Context, providerType string, updates map[string]interface{}) error {
	// 如果包含配置 JSON，先加密
	if configJSON, ok := updates["config_json"]; ok {
		var jsonStr string
		switch v := configJSON.(type) {
		case string:
			jsonStr = v
		default:
			data, err := json.Marshal(v)
			if err != nil {
				return fmt.Errorf("序列化配置失败: %w", err)
			}
			jsonStr = string(data)
		}
		encrypted, err := s.encrypt(jsonStr)
		if err != nil {
			return fmt.Errorf("加密配置失败: %w", err)
		}
		updates["config_json"] = encrypted
	}
	return s.db.WithContext(ctx).Model(&model.PaymentProviderConfig{}).
		Where("provider_type = ?", providerType).Updates(updates).Error
}

// ToggleProvider 启用/停用支付渠道
func (s *PaymentConfigService) ToggleProvider(ctx context.Context, providerType string) (*model.PaymentProviderConfig, error) {
	var provider model.PaymentProviderConfig
	if err := s.db.WithContext(ctx).Where("provider_type = ?", providerType).First(&provider).Error; err != nil {
		return nil, err
	}
	provider.IsActive = !provider.IsActive
	if err := s.db.WithContext(ctx).Save(&provider).Error; err != nil {
		return nil, err
	}
	return &provider, nil
}

// ==================== 银行账号 CRUD ====================

// GetAllBankAccounts 获取所有银行账号
func (s *PaymentConfigService) GetAllBankAccounts(ctx context.Context) ([]model.BankAccount, error) {
	var accounts []model.BankAccount
	if err := s.db.WithContext(ctx).Order("sort_order ASC, id ASC").Find(&accounts).Error; err != nil {
		return nil, err
	}
	return accounts, nil
}

// CreateBankAccount 创建银行账号
func (s *PaymentConfigService) CreateBankAccount(ctx context.Context, account *model.BankAccount) error {
	return s.db.WithContext(ctx).Create(account).Error
}

// UpdateBankAccount 更新银行账号
func (s *PaymentConfigService) UpdateBankAccount(ctx context.Context, id uint, updates map[string]interface{}) error {
	return s.db.WithContext(ctx).Model(&model.BankAccount{}).Where("id = ?", id).Updates(updates).Error
}

// DeleteBankAccount 删除银行账号
func (s *PaymentConfigService) DeleteBankAccount(ctx context.Context, id uint) error {
	return s.db.WithContext(ctx).Delete(&model.BankAccount{}, id).Error
}

// ==================== 付款方式 CRUD ====================

// GetAllPaymentMethods 获取所有付款方式
func (s *PaymentConfigService) GetAllPaymentMethods(ctx context.Context) ([]model.PaymentMethod, error) {
	var methods []model.PaymentMethod
	if err := s.db.WithContext(ctx).Order("sort_order ASC, id ASC").Find(&methods).Error; err != nil {
		return nil, err
	}
	return methods, nil
}

// UpdatePaymentMethod 更新付款方式
func (s *PaymentConfigService) UpdatePaymentMethod(ctx context.Context, methodType string, updates map[string]interface{}) error {
	return s.db.WithContext(ctx).Model(&model.PaymentMethod{}).
		Where("type = ?", methodType).Updates(updates).Error
}

// TogglePaymentMethod 启用/停用付款方式
func (s *PaymentConfigService) TogglePaymentMethod(ctx context.Context, methodType string) (*model.PaymentMethod, error) {
	var method model.PaymentMethod
	if err := s.db.WithContext(ctx).Where("type = ?", methodType).First(&method).Error; err != nil {
		return nil, err
	}
	method.IsActive = !method.IsActive
	if err := s.db.WithContext(ctx).Save(&method).Error; err != nil {
		return nil, err
	}
	return &method, nil
}

// ==================== 公开接口 ====================

// ActivePaymentMethodInfo 公开返回的付款方式（含银行账号信息）
type ActivePaymentMethodInfo struct {
	Type         string              `json:"type"`
	DisplayName  string              `json:"display_name"`
	Icon         string              `json:"icon"`
	Description  string              `json:"description"`
	BankAccounts []model.BankAccount `json:"bank_accounts,omitempty"`
}

// GetActivePaymentMethods 获取启用的付款方式列表（公开接口）
func (s *PaymentConfigService) GetActivePaymentMethods(ctx context.Context) ([]ActivePaymentMethodInfo, error) {
	var methods []model.PaymentMethod
	if err := s.db.WithContext(ctx).Where("is_active = ?", true).Order("sort_order ASC").Find(&methods).Error; err != nil {
		return nil, err
	}

	// 获取活跃的银行账号
	var bankAccounts []model.BankAccount
	if err := s.db.WithContext(ctx).Where("is_active = ?", true).Order("sort_order ASC").Find(&bankAccounts).Error; err != nil {
		return nil, err
	}

	result := make([]ActivePaymentMethodInfo, 0, len(methods))
	for _, m := range methods {
		info := ActivePaymentMethodInfo{
			Type:        m.Type,
			DisplayName: m.DisplayName,
			Icon:        m.Icon,
			Description: m.Description,
		}
		// 对公转账方式附带银行账号信息
		if m.Type == "BANK_TRANSFER" {
			info.BankAccounts = bankAccounts
		}
		result = append(result, info)
	}
	return result, nil
}

// Encrypt 公开加密方法（用于测试）
func (s *PaymentConfigService) Encrypt(plaintext string) (string, error) {
	return s.encrypt(plaintext)
}

// Decrypt 公开解密方法（用于测试）
func (s *PaymentConfigService) Decrypt(encoded string) (string, error) {
	return s.decrypt(encoded)
}
