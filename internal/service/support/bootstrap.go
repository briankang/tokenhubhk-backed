package support

import (
	"context"
	"strings"

	goredis "github.com/redis/go-redis/v9"
	"go.uber.org/zap"
	"gorm.io/gorm"

	"tokenhub-server/internal/config"
	"tokenhub-server/internal/model"
	"tokenhub-server/internal/pkg/logger"
	"tokenhub-server/internal/service/apikey"
)

// Services AI 客服子系统的全部服务引用。
// 由 Bootstrap 一次性构造，供 handler 层注入使用。
type Services struct {
	Orchestrator *ChatOrchestrator
	SessionSvc   *SessionService
	MessageSvc   *MessageService
	MemorySvc    *MemoryService
	TicketSvc    *TicketService
	ProviderSvc  *ProviderDocService
	HotQuestion  *HotQuestionService
	Rebuilder    *KnowledgeRebuilder
	Retriever    *KnowledgeRetriever
	Budget       *BudgetGuard
	Selector     *ModelSelector

	Enabled  bool   // 当前是否可对外提供服务（内部 API Key 缺失时为 false）
	Disabled string // Enabled=false 时的原因，用于 handler 返回 503
}

// Bootstrap 从 DB + Redis + AppConfig 组装全部 support 服务。
// 即使 InternalAPIKey 缺失也会返回非 nil *Services（Enabled=false），
// handler 可据此返回 503 而不是导致整个 backend 启动失败。
func Bootstrap(db *gorm.DB, redis *goredis.Client) *Services {
	cfg := config.Global.Support.WithDefaults()

	apiKey := resolveInternalAPIKey(db, cfg.InternalAPIKey)
	baseURL := strings.TrimRight(cfg.InternalBaseURL, "/")

	// 始终构造底层服务（DB/Redis 相关，无外部依赖）
	sessionSvc := NewSessionService(db)
	msgSvc := NewMessageService(db)
	memorySvc := NewMemoryService(db)
	ticketSvc := NewTicketService(db)
	providerSvc := NewProviderDocService(db)
	hotQuestionSvc := NewHotQuestionService(db)
	resolver := NewDynamicValueResolver(db, redis)
	selector := NewModelSelector(db)
	budget := NewBudgetGuard(redis, cfg.BudgetMonthlyCredits)
	if cfg.BudgetEconomyPct > 0 || cfg.BudgetEmergencyPct > 0 {
		budget.SetThresholds(cfg.BudgetEconomyPct, cfg.BudgetEmergencyPct)
	}
	translator := NewTranslator(db, redis)

	// Embedding + Retriever + Rebuilder 依赖内部 API Key
	var (
		embed     *EmbeddingClient
		retriever *KnowledgeRetriever
		rebuilder *KnowledgeRebuilder
	)
	if apiKey != "" && baseURL != "" {
		embed = NewEmbeddingClient(baseURL, apiKey, cfg.EmbeddingModel)
		retriever = NewKnowledgeRetriever(db, redis, embed, resolver)
		// Phase A3: 切换向量存储模式（默认 memory，可配 polardb 走 HNSW）
		retriever.SetVectorStore(cfg.VectorStore)
		rebuilder = NewKnowledgeRebuilder(db, embed)
		// Phase A4: 让 rebuilder 知道是否需要写 embedding_vec（PolarDB only）
		rebuilder.SetVectorStore(cfg.VectorStore)
	}

	prompt := NewPromptBuilder(resolver)

	var orch *ChatOrchestrator
	if apiKey != "" && baseURL != "" && retriever != nil {
		orch = NewChatOrchestrator(Dependencies{
			DB:           db,
			Redis:        redis,
			Retriever:    retriever,
			Resolver:     resolver,
			Translator:   translator,
			Selector:     selector,
			Budget:       budget,
			Prompt:       prompt,
			Embed:        embed,
			ProviderSvc:  providerSvc,
			SessionSvc:   sessionSvc,
			MessageSvc:   msgSvc,
			MemorySvc:    memorySvc,
			InternalBase: baseURL,
			InternalKey:  apiKey,
		})
	}

	enabled := orch != nil
	reason := ""
	if !enabled {
		if apiKey == "" {
			reason = "support.internal_api_key not configured"
		} else {
			reason = "support bootstrap incomplete"
		}
		logger.L.Warn("AI support disabled",
			zap.String("reason", reason),
			zap.String("base_url", baseURL),
			zap.Bool("has_key", apiKey != ""))
	} else {
		logger.L.Info("AI support ready",
			zap.String("base_url", baseURL),
			zap.String("embedding_model", cfg.EmbeddingModel),
			zap.Int64("budget_credits", cfg.BudgetMonthlyCredits))
	}

	return &Services{
		Orchestrator: orch,
		SessionSvc:   sessionSvc,
		MessageSvc:   msgSvc,
		MemorySvc:    memorySvc,
		TicketSvc:    ticketSvc,
		ProviderSvc:  providerSvc,
		HotQuestion:  hotQuestionSvc,
		Rebuilder:    rebuilder,
		Retriever:    retriever,
		Budget:       budget,
		Selector:     selector,
		Enabled:      enabled,
		Disabled:     reason,
	}
}

// resolveInternalAPIKey 按优先级解析内部 API Key：
//  1. 配置文件 / 环境变量 (support.internal_api_key)
//  2. system_configs 表 key="support.internal_api_key"（上次自动发现/生成的结果，走解密）
//  3. 自动发现：取 role=ADMIN 的用户的任一 active APIKey，解密后使用，并写回 system_configs
//  4. 若 admin 没有任何 APIKey，自动为其创建一个名为 "system-ai-support" 的密钥
//
// 所有自动发现/创建的明文 Key **仅存放于内存**，落地到 system_configs 的是 AES-256-GCM 密文（ApiKeyService.encryptKey 相同算法），
// 与 api_keys.key_encrypted 共用同一把 JWT-Secret 派生密钥。
func resolveInternalAPIKey(db *gorm.DB, fromConfig string) string {
	ctx := context.Background()
	// 1. 显式配置（明文）
	if s := strings.TrimSpace(fromConfig); s != "" {
		return s
	}

	// 构造 ApiKeyService 供解密 / 生成 / 验证
	jwtSecret := config.Global.JWT.Secret
	keySvc := apikey.NewApiKeyService(db, nil, jwtSecret)

	// 2. system_configs 中的密文
	var row model.SystemConfig
	if err := db.WithContext(ctx).Where("`key` = ?", "support.internal_api_key_encrypted").First(&row).Error; err == nil && row.Value != "" {
		if plain, derr := keySvc.DecryptValue(row.Value); derr == nil && strings.TrimSpace(plain) != "" {
			// 二次校验：确认 API Key 仍然有效（用户未禁用 / 未撤销）
			if _, verr := keySvc.Verify(ctx, plain); verr == nil {
				return plain
			}
			logger.L.Warn("cached support key no longer valid, re-discovering")
		}
	}

	// 3. 自动发现：取首个 SUPER_ADMIN 角色用户的第一条 active APIKey
	// 注意：RBAC v4.0 已删除 users.role 列，改用 user_roles 表查询
	var admin model.User
	if err := db.WithContext(ctx).
		Joins("JOIN user_roles ur ON ur.user_id = users.id").
		Joins("JOIN roles r ON r.id = ur.role_id AND r.code = ?", "SUPER_ADMIN").
		Order("users.id ASC").First(&admin).Error; err != nil {
		logger.L.Warn("no SUPER_ADMIN user found for support auto-key", zap.Error(err))
		return ""
	}

	var ak model.ApiKey
	err := db.WithContext(ctx).Where("user_id = ? AND is_active = ?", admin.ID, true).
		Order("id ASC").First(&ak).Error
	if err == nil && ak.KeyEncrypted != "" {
		if plain, derr := keySvc.DecryptValue(ak.KeyEncrypted); derr == nil && strings.TrimSpace(plain) != "" {
			persistSupportKey(db, keySvc, plain)
			logger.L.Info("support auto-discovered admin api key",
				zap.Uint("admin_id", admin.ID),
				zap.String("key_prefix", ak.KeyPrefix))
			return plain
		}
	}

	// 4. 全部 fallback 失败 → 为 admin 自动生成一个 "system-ai-support" 密钥
	tenantID := admin.TenantID
	if tenantID == 0 {
		tenantID = 1
	}
	generated, gerr := keySvc.Generate(ctx, admin.ID, tenantID, "system-ai-support")
	if gerr != nil || generated == nil || generated.Key == "" {
		logger.L.Warn("failed to auto-generate support api key", zap.Error(gerr))
		return ""
	}
	persistSupportKey(db, keySvc, generated.Key)
	logger.L.Info("support auto-generated system api key",
		zap.Uint("admin_id", admin.ID),
		zap.String("key_prefix", generated.KeyPrefix))
	return generated.Key
}

// persistSupportKey 把自动发现/生成的明文 Key 以密文形式写入 system_configs，
// 下次启动可直接解密复用，避免重复查询 api_keys 表。
func persistSupportKey(db *gorm.DB, keySvc *apikey.ApiKeyService, plain string) {
	encrypted, err := keySvc.EncryptValue(plain)
	if err != nil {
		logger.L.Warn("encrypt support key failed", zap.Error(err))
		return
	}
	row := model.SystemConfig{Key: "support.internal_api_key_encrypted", Value: encrypted}
	// upsert
	db.Where("`key` = ?", row.Key).Assign(map[string]any{"value": encrypted}).FirstOrCreate(&row)
}
