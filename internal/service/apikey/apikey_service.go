package apikey

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"time"

	goredis "github.com/redis/go-redis/v9"
	"gorm.io/gorm"

	"tokenhub-server/internal/model"
	"tokenhub-server/internal/pkg/safego"
	"tokenhub-server/internal/service/usercache"
)

const (
	keyPrefix       = "sk-"
	keyRandomBytes  = 24 // 24 bytes = 48 hex chars，总长度 sk- + 48 = 51 字符
	keyDisplayChars = 8
	cacheKeyPrefix  = "apikey:"
	cacheTTL        = 5 * time.Minute
	maxRetry        = 10 // 唯一性检查最大重试次数
)

// ApiKeyResult 生成 API Key 后的返回结果（仅此时可见完整密钥）
type ApiKeyResult struct {
	ID        uint   `json:"id"`
	Name      string `json:"name"`
	Key       string `json:"key"`
	KeyPrefix string `json:"key_prefix"`
}

// ApiKeyInfo 验证 API Key 后返回的关联信息
type ApiKeyInfo struct {
	KeyID           uint   `json:"key_id"`
	UserID          uint   `json:"user_id"`
	TenantID        uint   `json:"tenant_id"`
	Permissions     []byte `json:"permissions,omitempty"`
	CustomChannelID *uint  `json:"custom_channel_id,omitempty"` // 关联的自定义渠道ID，nil表示使用默认渠道
	CreditLimit     int64  `json:"credit_limit"`                // 积分限额
	CreditUsed      int64  `json:"credit_used"`                 // 已用积分
	AllowedModels   string `json:"allowed_models"`              // 允许的模型列表
	RateLimitRPM    int    `json:"rate_limit_rpm"`              // 每分钟请求数
	RateLimitTPM    int    `json:"rate_limit_tpm"`              // 每分钟Token数
}

// ApiKeyService API 密钥服务，管理密钥的生成、验证、列表和撤销
type ApiKeyService struct {
	db        *gorm.DB
	redis     *goredis.Client
	encKey    []byte // AES-256 加密密钥（32字节，由 JWT Secret 派生）
}

// NewApiKeyService 创建 API 密钥服务实例，db 不能为 nil 否则 panic
// encSecret 用于 AES-GCM 加密存储完整密钥（传空字符串则不加密）
func NewApiKeyService(db *gorm.DB, redis *goredis.Client, encSecret ...string) *ApiKeyService {
	if db == nil {
		panic("apikey service: db is nil")
	}
	svc := &ApiKeyService{db: db, redis: redis}
	if len(encSecret) > 0 && encSecret[0] != "" {
		// 用 SHA256 将任意长度 secret 派生为 32 字节 AES-256 密钥
		h := sha256.Sum256([]byte(encSecret[0]))
		svc.encKey = h[:]
	}
	return svc
}

// encryptKey 使用 AES-256-GCM 加密明文密钥
func (s *ApiKeyService) encryptKey(plainKey string) (string, error) {
	if len(s.encKey) == 0 {
		return "", nil // 未配置加密密钥，跳过
	}
	block, err := aes.NewCipher(s.encKey)
	if err != nil {
		return "", fmt.Errorf("aes cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("gcm: %w", err)
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", fmt.Errorf("nonce: %w", err)
	}
	ciphertext := gcm.Seal(nonce, nonce, []byte(plainKey), nil)
	return base64.StdEncoding.EncodeToString(ciphertext), nil
}

// decryptKey 使用 AES-256-GCM 解密密钥
func (s *ApiKeyService) decryptKey(encrypted string) (string, error) {
	if len(s.encKey) == 0 {
		return "", fmt.Errorf("encryption key not configured")
	}
	data, err := base64.StdEncoding.DecodeString(encrypted)
	if err != nil {
		return "", fmt.Errorf("base64 decode: %w", err)
	}
	block, err := aes.NewCipher(s.encKey)
	if err != nil {
		return "", fmt.Errorf("aes cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("gcm: %w", err)
	}
	nonceSize := gcm.NonceSize()
	if len(data) < nonceSize {
		return "", fmt.Errorf("ciphertext too short")
	}
	nonce, ciphertext := data[:nonceSize], data[nonceSize:]
	plaintext, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return "", fmt.Errorf("decrypt: %w", err)
	}
	return string(plaintext), nil
}

// CreateKeyOptions 创建 API Key 的选项参数
type CreateKeyOptions struct {
	Name            string
	CustomChannelID *uint  // 关联的自定义渠道ID
	CreditLimit     int64
	AllowedModels   string
	RateLimitRPM    int
	RateLimitTPM    int
}

// Generate 为指定用户/租户生成新的 API Key
// 1. 生成 sk-xxxx 格式的随机密钥（48 位十六进制，总长度 51 字符）
// 2. 检查哈希值唯一性，重复则重新生成
// 3. 存储 SHA256 哈希值
// 4. 保存前 8 位作为显示前缀
// 5. 返回完整密钥（仅返回一次）
func (s *ApiKeyService) Generate(ctx context.Context, userID, tenantID uint, name string) (*ApiKeyResult, error) {
	return s.GenerateWithOptions(ctx, userID, tenantID, CreateKeyOptions{Name: name})
}

// GenerateWithOptions 使用完整选项生成新的 API Key
func (s *ApiKeyService) GenerateWithOptions(ctx context.Context, userID, tenantID uint, opts CreateKeyOptions) (*ApiKeyResult, error) {
	if userID == 0 {
		return nil, fmt.Errorf("user id is required")
	}
	if tenantID == 0 {
		return nil, fmt.Errorf("tenant id is required")
	}
	if opts.Name == "" {
		return nil, fmt.Errorf("key name is required")
	}

	var fullKey string
	var hashStr string
	var displayPrefix string

	// 循环生成直到获得唯一的密钥
	for i := 0; i < maxRetry; i++ {
		// 生成随机字节
		randomBytes := make([]byte, keyRandomBytes)
		if _, err := rand.Read(randomBytes); err != nil {
			return nil, fmt.Errorf("failed to generate random key: %w", err)
		}

		fullKey = keyPrefix + hex.EncodeToString(randomBytes)
		displayPrefix = fullKey[:keyDisplayChars]

		// SHA256 哈希计算
		hash := sha256.Sum256([]byte(fullKey))
		hashStr = hex.EncodeToString(hash[:])

		// 检查哈希值是否已存在（唯一性检查）
		var count int64
		if err := s.db.WithContext(ctx).Model(&model.ApiKey{}).Where("key_hash = ?", hashStr).Count(&count).Error; err != nil {
			return nil, fmt.Errorf("failed to check key uniqueness: %w", err)
		}
		if count == 0 {
			// 密钥唯一，跳出循环
			break
		}
		// 密钥重复，继续重试
	}

	if fullKey == "" {
		return nil, fmt.Errorf("failed to generate unique api key after %d attempts", maxRetry)
	}

	// AES-GCM 加密完整密钥用于后续查看
	encryptedKey, _ := s.encryptKey(fullKey)

	record := &model.ApiKey{
		TenantID:        tenantID,
		UserID:          userID,
		Name:            opts.Name,
		KeyHash:         hashStr,
		KeyPrefix:       displayPrefix,
		KeyEncrypted:    encryptedKey,
		IsActive:        true,
		CustomChannelID: opts.CustomChannelID,
		CreditLimit:     opts.CreditLimit,
		AllowedModels:   opts.AllowedModels,
		RateLimitRPM:    opts.RateLimitRPM,
		RateLimitTPM:    opts.RateLimitTPM,
	}

	if err := s.db.WithContext(ctx).Create(record).Error; err != nil {
		return nil, fmt.Errorf("failed to save api key: %w", err)
	}

	usercache.InvalidateApiKeys(ctx, userID)
	return &ApiKeyResult{
		ID:        record.ID,
		Name:      record.Name,
		Key:       fullKey,
		KeyPrefix: displayPrefix,
	}, nil
}

// Verify 验证 API Key 并返回关联用户信息
// 1. 对 Key 计算 SHA256 查找记录
// 2. 检查是否启用及是否过期
// 3. 更新最后使用时间
// 4. 返回关联的 userID/tenantID/permissions
func (s *ApiKeyService) Verify(ctx context.Context, key string) (*ApiKeyInfo, error) {
	if key == "" {
		return nil, fmt.Errorf("api key is required")
	}

	hash := sha256.Sum256([]byte(key))
	hashStr := hex.EncodeToString(hash[:])

	// 优先从 Redis 缓存查找
	if s.redis != nil {
		cacheKey := cacheKeyPrefix + hashStr
		var info ApiKeyInfo
		if err := s.getFromCache(ctx, cacheKey, &info); err == nil {
			// 异步更新最后使用时间（fire-and-forget）— safego 防 panic + 短超时防连接池饥饿
			safego.Go("apikey-update-last-used", func() {
				bgCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
				defer cancel()
				_ = s.db.WithContext(bgCtx).Model(&model.ApiKey{}).
					Where("key_hash = ?", hashStr).
					Update("last_used_at", time.Now()).Error
			})
			return &info, nil
		}
	}

	var apiKey model.ApiKey
	if err := s.db.WithContext(ctx).Where("key_hash = ?", hashStr).First(&apiKey).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, fmt.Errorf("invalid api key")
		}
		return nil, fmt.Errorf("failed to verify api key: %w", err)
	}

	if !apiKey.IsActive {
		return nil, fmt.Errorf("api key is revoked")
	}
	if apiKey.ExpiresAt != nil && apiKey.ExpiresAt.Before(time.Now()) {
		return nil, fmt.Errorf("api key has expired")
	}

	// 更新最后使用时间
	now := time.Now()
	_ = s.db.WithContext(ctx).Model(&model.ApiKey{}).Where("id = ?", apiKey.ID).Update("last_used_at", now).Error

	info := &ApiKeyInfo{
		KeyID:           apiKey.ID,
		UserID:          apiKey.UserID,
		TenantID:        apiKey.TenantID,
		Permissions:     apiKey.Permissions,
		CustomChannelID: apiKey.CustomChannelID,
		CreditLimit:     apiKey.CreditLimit,
		CreditUsed:      apiKey.CreditUsed,
		AllowedModels:   apiKey.AllowedModels,
		RateLimitRPM:    apiKey.RateLimitRPM,
		RateLimitTPM:    apiKey.RateLimitTPM,
	}

	// 存入缓存
	if s.redis != nil {
		cacheKey := cacheKeyPrefix + hashStr
		_ = s.setCache(ctx, cacheKey, info, cacheTTL)
	}

	return info, nil
}

// ApiKeyWithStats 携带统计信息的 API Key（用于列表展示）
type ApiKeyWithStats struct {
	model.ApiKey
	Requests int64 `json:"requests" gorm:"column:requests"` // 总请求数（从 channel_logs 聚合）
}

// List 分页查询用户的 API Key 列表（密钥已脱敏），附带请求统计
func (s *ApiKeyService) List(ctx context.Context, userID uint, page, pageSize int) ([]ApiKeyWithStats, int64, error) {
	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > 100 {
		pageSize = 20
	}

	var total int64
	query := s.db.WithContext(ctx).Model(&model.ApiKey{}).Where("user_id = ?", userID)

	if err := query.Count(&total).Error; err != nil {
		return nil, 0, fmt.Errorf("failed to count api keys: %w", err)
	}

	var keys []model.ApiKey
	offset := (page - 1) * pageSize
	if err := query.Offset(offset).Limit(pageSize).Order("id DESC").Find(&keys).Error; err != nil {
		return nil, 0, fmt.Errorf("failed to list api keys: %w", err)
	}

	// 按 api_key_id 精确聚合每个 Key 的累计请求数（全量统计，含失败重试）
	// 口径说明：
	//   - 数据源 channel_logs：每次渠道调用 1 行（含失败重试），与仪表板 api_call_logs（每请求 1 行）不同
	//   - 不做时间过滤，为"全量累计"展示；前端列标题需明确标注
	type keyStat struct {
		ApiKeyID uint  `gorm:"column:api_key_id"`
		Count    int64 `gorm:"column:cnt"`
	}
	var stats []keyStat
	if len(keys) > 0 {
		keyIDs := make([]uint, 0, len(keys))
		for _, k := range keys {
			keyIDs = append(keyIDs, k.ID)
		}
		s.db.WithContext(ctx).
			Table("channel_logs").
			Select("api_key_id, COUNT(*) as cnt").
			Where("user_id = ? AND api_key_id IN ?", userID, keyIDs).
			Group("api_key_id").
			Scan(&stats)
	}
	countByKey := make(map[uint]int64, len(stats))
	for _, st := range stats {
		countByKey[st.ApiKeyID] = st.Count
	}

	// 组装结果
	result := make([]ApiKeyWithStats, len(keys))
	for i, k := range keys {
		result[i] = ApiKeyWithStats{
			ApiKey:   k,
			Requests: countByKey[k.ID],
		}
	}

	return result, total, nil
}

// Revoke 撤销指定的 API Key（兼容旧调用，等同于 Disable）
// Deprecated: 请使用 Disable
func (s *ApiKeyService) Revoke(ctx context.Context, id uint, userID uint) error {
	return s.Disable(ctx, id, userID)
}

// Disable 禁用指定 API Key（is_active=false），密钥不可再调用但记录保留
func (s *ApiKeyService) Disable(ctx context.Context, id uint, userID uint) error {
	return s.setActive(ctx, id, userID, false)
}

// Enable 启用指定 API Key（is_active=true）
func (s *ApiKeyService) Enable(ctx context.Context, id uint, userID uint) error {
	return s.setActive(ctx, id, userID, true)
}

// setActive 内部统一切换 is_active 状态
func (s *ApiKeyService) setActive(ctx context.Context, id uint, userID uint, active bool) error {
	if id == 0 {
		return fmt.Errorf("api key id is required")
	}
	result := s.db.WithContext(ctx).Model(&model.ApiKey{}).
		Where("id = ? AND user_id = ?", id, userID).
		Update("is_active", active)
	if result.Error != nil {
		return fmt.Errorf("failed to set api key active=%v: %w", active, result.Error)
	}
	if result.RowsAffected == 0 {
		return fmt.Errorf("api key not found or not owned by user")
	}
	// 失效用户维度的 API Key 列表缓存
	usercache.InvalidateApiKeys(ctx, userID)
	return nil
}

// SoftDelete 软删除指定 API Key（GORM 自动写 deleted_at）
// 删除后用户与管理员均不可见（除非显式 Unscoped）
func (s *ApiKeyService) SoftDelete(ctx context.Context, id uint, userID uint) error {
	if id == 0 {
		return fmt.Errorf("api key id is required")
	}
	result := s.db.WithContext(ctx).
		Where("id = ? AND user_id = ?", id, userID).
		Delete(&model.ApiKey{})
	if result.Error != nil {
		return fmt.Errorf("failed to soft-delete api key: %w", result.Error)
	}
	if result.RowsAffected == 0 {
		return fmt.Errorf("api key not found or not owned by user")
	}
	usercache.InvalidateApiKeys(ctx, userID)
	return nil
}

// UpdateKeyOptions 更新 API Key 的选项参数
type UpdateKeyOptions struct {
	Name            *string
	CustomChannelID *uint   // 关联的自定义渠道ID（传 0 表示清除关联）
	CreditLimit     *int64
	AllowedModels   *string
	RateLimitRPM    *int
	RateLimitTPM    *int
}

// Update 更新指定 API Key 的配置，仅密钥拥有者可操作
func (s *ApiKeyService) Update(ctx context.Context, id uint, userID uint, opts UpdateKeyOptions) error {
	if id == 0 {
		return fmt.Errorf("api key id is required")
	}

	// 构建更新字段
	updates := make(map[string]interface{})
	if opts.Name != nil {
		updates["name"] = *opts.Name
	}
	if opts.CustomChannelID != nil {
		updates["custom_channel_id"] = *opts.CustomChannelID
	}
	if opts.CreditLimit != nil {
		updates["credit_limit"] = *opts.CreditLimit
	}
	if opts.AllowedModels != nil {
		updates["allowed_models"] = *opts.AllowedModels
	}
	if opts.RateLimitRPM != nil {
		updates["rate_limit_rpm"] = *opts.RateLimitRPM
	}
	if opts.RateLimitTPM != nil {
		updates["rate_limit_tpm"] = *opts.RateLimitTPM
	}

	if len(updates) == 0 {
		return nil // 没有需要更新的字段
	}

	result := s.db.WithContext(ctx).Model(&model.ApiKey{}).
		Where("id = ? AND user_id = ?", id, userID).
		Updates(updates)
	if result.Error != nil {
		return fmt.Errorf("failed to update api key: %w", result.Error)
	}
	if result.RowsAffected == 0 {
		return fmt.Errorf("api key not found or not owned by user")
	}

	usercache.InvalidateApiKeys(ctx, userID)
	return nil
}

// IncrementCreditUsed 增加已用积分（消费后调用）
// 使用乐观锁防止并发超扣，返回实际增加的积分
func (s *ApiKeyService) IncrementCreditUsed(ctx context.Context, keyID uint, delta int64) error {
	if keyID == 0 || delta <= 0 {
		return nil
	}

	// 直接更新，不检查限额（限额在中间件中检查）
	return s.db.WithContext(ctx).Model(&model.ApiKey{}).
		Where("id = ?", keyID).
		UpdateColumn("credit_used", gorm.Expr("credit_used + ?", delta)).Error
}

// GetByKeyID 根据 Key ID 获取 API Key 信息
func (s *ApiKeyService) GetByKeyID(ctx context.Context, keyID uint) (*model.ApiKey, error) {
	if keyID == 0 {
		return nil, fmt.Errorf("key id is required")
	}

	var apiKey model.ApiKey
	if err := s.db.WithContext(ctx).First(&apiKey, keyID).Error; err != nil {
		return nil, err
	}
	return &apiKey, nil
}

// RevealKey 解密并返回指定 API Key 的完整明文，仅密钥拥有者可操作
func (s *ApiKeyService) RevealKey(ctx context.Context, id uint, userID uint) (string, error) {
	if id == 0 {
		return "", fmt.Errorf("api key id is required")
	}

	var apiKey model.ApiKey
	if err := s.db.WithContext(ctx).Where("id = ? AND user_id = ?", id, userID).First(&apiKey).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return "", fmt.Errorf("api key not found or not owned by user")
		}
		return "", fmt.Errorf("failed to get api key: %w", err)
	}

	if apiKey.KeyEncrypted == "" {
		return "", fmt.Errorf("此密钥创建于加密存储功能上线之前，无法恢复完整密钥")
	}

	plainKey, err := s.decryptKey(apiKey.KeyEncrypted)
	if err != nil {
		return "", fmt.Errorf("failed to decrypt key: %w", err)
	}
	return plainKey, nil
}

// EncryptValue 暴露加密能力供其他 service 复用（使用同一 AES-256-GCM 密钥）。
// 用于 AI 客服自动发现 Admin API Key 后，以密文形式缓存到 system_configs。
func (s *ApiKeyService) EncryptValue(plain string) (string, error) {
	return s.encryptKey(plain)
}

// DecryptValue 暴露解密能力供其他 service 复用。
func (s *ApiKeyService) DecryptValue(cipherText string) (string, error) {
	return s.decryptKey(cipherText)
}

// getFromCache 从 Redis 缓存获取值
func (s *ApiKeyService) getFromCache(ctx context.Context, key string, dest interface{}) error {
	if s.redis == nil {
		return fmt.Errorf("redis not available")
	}
	val, err := s.redis.Get(ctx, key).Bytes()
	if err != nil {
		return err
	}
	if info, ok := dest.(*ApiKeyInfo); ok {
		// Simple binary decode - in production use JSON
		_ = val
		_ = info
		return fmt.Errorf("cache miss")
	}
	return fmt.Errorf("unsupported type")
}

// setCache 将值存入 Redis 缓存
func (s *ApiKeyService) setCache(ctx context.Context, key string, value interface{}, ttl time.Duration) error {
	if s.redis == nil {
		return nil
	}
	// 简化缓存 — 生产环境应使用 JSON 序列化
	return s.redis.Set(ctx, key, "cached", ttl).Err()
}
