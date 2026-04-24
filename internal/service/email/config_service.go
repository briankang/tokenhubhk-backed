// Package email 提供邮件发送管理功能。
//
// 职责分层：
//   - config_service：SendCloud 凭证 CRUD + AES-256-GCM 加解密
//   - template_service：本地模板 CRUD + 变量校验 + html/template 渲染
//   - sender：SendCloud HTTP 客户端封装（send / sendtemplate / 附件 multipart）
//   - email_service：业务门面，SendByTemplate(ctx, req) 供注册/发票等调用
package email

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"

	"gorm.io/gorm"

	"tokenhub-server/internal/model"
	"tokenhub-server/internal/pkg/dbctx"
)

// 默认加密密钥（与 payment 模块保持隔离，独立 key）
const defaultEncryptKey = "tokenhub-email-secret-key-32byt!"

// MaskedSecret 前端可见占位符，保存时检测到此值则跳过更新 apiKey
const MaskedSecret = "***"

// ConfigService SendCloud 凭证管理
type ConfigService struct {
	db         *gorm.DB
	encryptKey []byte
}

// NewConfigService 构造
func NewConfigService(db *gorm.DB) *ConfigService {
	key := os.Getenv("EMAIL_ENCRYPT_KEY")
	if key == "" {
		key = defaultEncryptKey
	}
	keyBytes := []byte(key)
	if len(keyBytes) < 32 {
		padded := make([]byte, 32)
		copy(padded, keyBytes)
		keyBytes = padded
	} else if len(keyBytes) > 32 {
		keyBytes = keyBytes[:32]
	}
	return &ConfigService{db: db, encryptKey: keyBytes}
}

// ============ AES-256-GCM ============

func (s *ConfigService) encrypt(plaintext string) (string, error) {
	if plaintext == "" {
		return "", nil
	}
	block, err := aes.NewCipher(s.encryptKey)
	if err != nil {
		return "", fmt.Errorf("create aes cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("create gcm: %w", err)
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", fmt.Errorf("generate nonce: %w", err)
	}
	ct := gcm.Seal(nonce, nonce, []byte(plaintext), nil)
	return base64.StdEncoding.EncodeToString(ct), nil
}

func (s *ConfigService) decrypt(encoded string) (string, error) {
	if encoded == "" {
		return "", nil
	}
	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return "", fmt.Errorf("base64 decode: %w", err)
	}
	block, err := aes.NewCipher(s.encryptKey)
	if err != nil {
		return "", fmt.Errorf("create aes cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("create gcm: %w", err)
	}
	nonceSize := gcm.NonceSize()
	if len(data) < nonceSize {
		return "", errors.New("ciphertext too short")
	}
	nonce, ct := data[:nonceSize], data[nonceSize:]
	pt, err := gcm.Open(nil, nonce, ct, nil)
	if err != nil {
		return "", fmt.Errorf("decrypt: %w", err)
	}
	return string(pt), nil
}

// ============ CRUD ============

// List 列出所有 channel 配置（apiKey 不返回，仅以是否已设置的 bool 表示）
func (s *ConfigService) List(ctx context.Context) ([]model.EmailProviderConfig, error) {
	cctx, cancel := dbctx.Short(ctx)
	defer cancel()
	var configs []model.EmailProviderConfig
	if err := s.db.WithContext(cctx).Order("channel ASC").Find(&configs).Error; err != nil {
		return nil, err
	}
	return configs, nil
}

// Get 按 channel 取配置（不含明文 apiKey）
func (s *ConfigService) Get(ctx context.Context, channel string) (*model.EmailProviderConfig, error) {
	cctx, cancel := dbctx.Short(ctx)
	defer cancel()
	var cfg model.EmailProviderConfig
	if err := s.db.WithContext(cctx).Where("channel = ?", channel).First(&cfg).Error; err != nil {
		return nil, err
	}
	return &cfg, nil
}

// GetDecrypted 获取包含明文 apiKey 的配置（供 sender 调用上游时用）
func (s *ConfigService) GetDecrypted(ctx context.Context, channel string) (*model.EmailProviderConfig, string, error) {
	cfg, err := s.Get(ctx, channel)
	if err != nil {
		return nil, "", err
	}
	apiKey, err := s.decrypt(cfg.APIKeyEncrypted)
	if err != nil {
		return nil, "", fmt.Errorf("decrypt api key: %w", err)
	}
	return cfg, apiKey, nil
}

// UpsertRequest 写入/更新请求
type UpsertRequest struct {
	Channel    string
	APIUser    string
	APIKey     string // 空或 "***" 时保留原值
	FromEmail  string
	FromName   string
	ReplyTo    string
	Domain     string
	IsActive   *bool
	DailyLimit *int
}

// Upsert 写入/更新配置
func (s *ConfigService) Upsert(ctx context.Context, req UpsertRequest) (*model.EmailProviderConfig, error) {
	if req.Channel != model.EmailChannelNotification && req.Channel != model.EmailChannelMarketing {
		return nil, fmt.Errorf("invalid channel: %s", req.Channel)
	}
	cctx, cancel := dbctx.Short(ctx)
	defer cancel()

	var existing model.EmailProviderConfig
	err := s.db.WithContext(cctx).Where("channel = ?", req.Channel).First(&existing).Error
	isNew := errors.Is(err, gorm.ErrRecordNotFound)
	if err != nil && !isNew {
		return nil, err
	}

	// 新建场景：apiKey 必填
	if isNew && (req.APIKey == "" || req.APIKey == MaskedSecret) {
		return nil, errors.New("api_key is required when creating new provider config")
	}

	target := existing
	if isNew {
		target = model.EmailProviderConfig{Channel: req.Channel}
	}
	if req.APIUser != "" {
		target.APIUser = req.APIUser
	}
	if req.APIKey != "" && req.APIKey != MaskedSecret {
		enc, err := s.encrypt(req.APIKey)
		if err != nil {
			return nil, err
		}
		target.APIKeyEncrypted = enc
	}
	if req.FromEmail != "" {
		target.FromEmail = req.FromEmail
	}
	target.FromName = req.FromName
	target.ReplyTo = req.ReplyTo
	if req.Domain != "" {
		target.Domain = req.Domain
	}
	if req.IsActive != nil {
		target.IsActive = *req.IsActive
	} else if isNew {
		target.IsActive = true
	}
	if req.DailyLimit != nil {
		target.DailyLimit = *req.DailyLimit
	}

	if isNew {
		if err := s.db.WithContext(cctx).Create(&target).Error; err != nil {
			return nil, err
		}
	} else {
		if err := s.db.WithContext(cctx).Save(&target).Error; err != nil {
			return nil, err
		}
	}
	return &target, nil
}
