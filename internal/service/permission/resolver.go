package permission

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	goredis "github.com/redis/go-redis/v9"
	"go.uber.org/zap"
	"gorm.io/gorm"

	"tokenhub-server/internal/model"
	"tokenhub-server/internal/pkg/logger"
)

// DataScopeType 数据范围类型常量
const (
	DataScopeAll           = "all"            // 全部数据（管理员类）
	DataScopeOwnTenant     = "own_tenant"     // 仅当前租户
	DataScopeCustomTenants = "custom_tenants" // 指定租户集合
	DataScopeOwnOnly       = "own_only"       // 仅自己
)

// DataScopePolicy 合并后的数据范围
type DataScopePolicy struct {
	Type      string `json:"type"`
	TenantIDs []uint `json:"tenant_ids,omitempty"`
}

// SubjectPerms 请求主体的完整权限视图
// 由 Resolver.Resolve 构造，带 Redis 缓存；LoadSubjectPerms 中间件读取后写入 context。
type SubjectPerms struct {
	UserID    uint            `json:"user_id"`
	TenantID  uint            `json:"tenant_id"`
	RoleCodes []string        `json:"role_codes"` // 展示用
	Codes     []string        `json:"codes"`      // 权限码数组（序列化友好；业务用 CodeSet()）
	DataScope DataScopePolicy `json:"data_scope"`
	codeSet   map[string]struct{}
}

// Has 判断是否拥有指定权限码
func (s *SubjectPerms) Has(code string) bool {
	if s == nil {
		return false
	}
	if s.codeSet == nil {
		s.rebuildCodeSet()
	}
	_, ok := s.codeSet[code]
	return ok
}

// HasAny 判断是否拥有任意一个权限码
func (s *SubjectPerms) HasAny(codes ...string) bool {
	if s == nil {
		return false
	}
	if s.codeSet == nil {
		s.rebuildCodeSet()
	}
	for _, c := range codes {
		if _, ok := s.codeSet[c]; ok {
			return true
		}
	}
	return false
}

// IsSuperAdmin 判断是否超级管理员角色
func (s *SubjectPerms) IsSuperAdmin() bool {
	if s == nil {
		return false
	}
	for _, c := range s.RoleCodes {
		if c == "SUPER_ADMIN" {
			return true
		}
	}
	return false
}

func (s *SubjectPerms) rebuildCodeSet() {
	s.codeSet = make(map[string]struct{}, len(s.Codes))
	for _, c := range s.Codes {
		s.codeSet[c] = struct{}{}
	}
}

// Resolver 权限解析器：从 DB 加载用户角色并合并为 SubjectPerms，带 Redis 缓存
type Resolver struct {
	db    *gorm.DB
	redis *goredis.Client
	ttl   time.Duration
}

// Default 全局默认 Resolver 实例（在 bootstrap/main.go 初始化后赋值）
var Default *Resolver

// NewResolver 创建解析器实例。redis 可为 nil（降级为每次查 DB）。
func NewResolver(db *gorm.DB, redis *goredis.Client) *Resolver {
	if db == nil {
		panic("permission resolver: db is nil")
	}
	return &Resolver{
		db:    db,
		redis: redis,
		ttl:   5 * time.Minute,
	}
}

// cacheKey Redis 缓存键
func cacheKey(uid uint) string {
	return fmt.Sprintf("user_perms:%d", uid)
}

// Resolve 返回 userID 对应的 SubjectPerms，优先读 Redis，缓存未命中时查 DB 并回填
func (r *Resolver) Resolve(ctx context.Context, userID uint) (*SubjectPerms, error) {
	if userID == 0 {
		return nil, errors.New("resolve: userID is zero")
	}

	// 1. 查 Redis 缓存
	if r.redis != nil {
		key := cacheKey(userID)
		raw, err := r.redis.Get(ctx, key).Result()
		if err == nil && raw != "" {
			var cached SubjectPerms
			if unmarshalErr := json.Unmarshal([]byte(raw), &cached); unmarshalErr == nil {
				cached.rebuildCodeSet()
				return &cached, nil
			}
			// JSON 损坏：清掉缓存，走 DB 重建
			_ = r.redis.Del(ctx, key).Err()
		}
	}

	// 2. 从 DB 加载
	perms, err := r.loadFromDB(ctx, userID)
	if err != nil {
		return nil, err
	}

	// 3. 回填 Redis（Set 失败不影响返回值）
	if r.redis != nil {
		if data, marshalErr := json.Marshal(perms); marshalErr == nil {
			if setErr := r.redis.Set(ctx, cacheKey(userID), string(data), r.ttl).Err(); setErr != nil {
				if logger.L != nil {
					logger.L.Warn("resolver: cache set failed",
						zap.Uint("user_id", userID),
						zap.Error(setErr),
					)
				}
			}
		}
	}
	return perms, nil
}

// roleRow 单条角色记录（内部使用，用于合并计算）
type roleRow struct {
	ID        uint
	Code      string
	DataScope model.JSON
}

// loadFromDB 从数据库一次性拉取用户的全部角色、权限码、数据范围并合并
func (r *Resolver) loadFromDB(ctx context.Context, userID uint) (*SubjectPerms, error) {
	// a. 用户信息（tenant_id + legacy role）
	var user model.User
	if err := r.db.WithContext(ctx).Select("id, tenant_id, role").First(&user, userID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, fmt.Errorf("resolve: user %d not found", userID)
		}
		return nil, fmt.Errorf("resolve: load user: %w", err)
	}

	// b. 该用户所有角色（roles.code + data_scope）
	var roles []roleRow
	if err := r.db.WithContext(ctx).Raw(`
		SELECT r.id, r.code, r.data_scope
		FROM user_roles ur
		JOIN roles r ON r.id = ur.role_id
		WHERE ur.user_id = ? AND r.deleted_at IS NULL
	`, userID).Scan(&roles).Error; err != nil {
		return nil, fmt.Errorf("resolve: load roles: %w", err)
	}

	// b.1 过渡期回退：若无 user_roles 记录（如 Phase 1 seed 之后注册的新用户），
	// 按 users.role 字符串 + LegacyRoleMapping 动态获取对应角色。
	// Phase 4 删除 users.role 字段后，此分支随之移除。
	if len(roles) == 0 {
		fallbackCode, ok := LegacyRoleMapping[user.Role]
		if !ok {
			fallbackCode = "USER"
		}
		var fallback roleRow
		if err := r.db.WithContext(ctx).Raw(`
			SELECT id, code, data_scope FROM roles
			WHERE code = ? AND deleted_at IS NULL
			LIMIT 1
		`, fallbackCode).Scan(&fallback).Error; err == nil && fallback.ID != 0 {
			roles = []roleRow{fallback}
			if logger.L != nil {
				logger.L.Debug("resolver: legacy role fallback",
					zap.Uint("user_id", userID),
					zap.String("legacy_role", user.Role),
					zap.String("resolved", fallbackCode),
				)
			}
		}
	}

	// c. 该用户所有权限码（去重）
	var codes []string
	if len(roles) > 0 {
		roleIDs := make([]uint, 0, len(roles))
		for _, role := range roles {
			roleIDs = append(roleIDs, role.ID)
		}
		if err := r.db.WithContext(ctx).Raw(`
			SELECT DISTINCT p.code
			FROM role_permissions rp
			JOIN permissions p ON p.id = rp.permission_id
			WHERE rp.role_id IN (?) AND p.deleted_at IS NULL
		`, roleIDs).Scan(&codes).Error; err != nil {
			return nil, fmt.Errorf("resolve: load codes: %w", err)
		}
	}
	sort.Strings(codes)

	// d. 合并数据范围
	policy := mergeDataScopes(roles, user.TenantID)

	// e. 提取角色 code 列表
	roleCodes := make([]string, 0, len(roles))
	for _, role := range roles {
		roleCodes = append(roleCodes, role.Code)
	}
	sort.Strings(roleCodes)

	perms := &SubjectPerms{
		UserID:    user.ID,
		TenantID:  user.TenantID,
		RoleCodes: roleCodes,
		Codes:     codes,
		DataScope: policy,
	}
	perms.rebuildCodeSet()
	return perms, nil
}

// mergeDataScopes 按优先级合并多个角色的 data_scope
// 规则：
//  1. 任意 all → all
//  2. 无角色或全部解析失败 → own_only（最安全的默认）
//  3. own_tenant + custom_tenants 共存 → custom_tenants，把 user_tenant_id 并入
//  4. 多个 custom_tenants → 租户 ID 并集
//  5. 只有 own_tenant → own_tenant
func mergeDataScopes(roles []roleRow, userTenantID uint) DataScopePolicy {
	if len(roles) == 0 {
		return DataScopePolicy{Type: DataScopeOwnOnly}
	}

	var (
		sawAll        bool
		sawOwnTenant  bool
		customTenants = make(map[uint]struct{})
	)

	for _, role := range roles {
		var raw struct {
			Type      string `json:"type"`
			TenantIDs []uint `json:"tenant_ids"`
		}
		if len(role.DataScope) == 0 {
			continue
		}
		if err := json.Unmarshal(role.DataScope, &raw); err != nil {
			if logger.L != nil {
				logger.L.Warn("resolver: bad data_scope json, skip",
					zap.String("role_code", role.Code),
					zap.Error(err),
				)
			}
			continue
		}
		switch raw.Type {
		case DataScopeAll:
			sawAll = true
		case DataScopeOwnTenant:
			sawOwnTenant = true
		case DataScopeCustomTenants:
			for _, tid := range raw.TenantIDs {
				if tid != 0 {
					customTenants[tid] = struct{}{}
				}
			}
		case DataScopeOwnOnly:
			// 最窄，不拉升合并结果
		}
	}

	// 优先级 1：all 最宽，直接返回
	if sawAll {
		return DataScopePolicy{Type: DataScopeAll}
	}

	// 优先级 2：own_tenant 或 custom_tenants 存在
	if sawOwnTenant && len(customTenants) > 0 {
		// 合并：把用户自身租户加入 custom 集合
		if userTenantID != 0 {
			customTenants[userTenantID] = struct{}{}
		}
		return DataScopePolicy{Type: DataScopeCustomTenants, TenantIDs: sortUintSet(customTenants)}
	}
	if len(customTenants) > 0 {
		return DataScopePolicy{Type: DataScopeCustomTenants, TenantIDs: sortUintSet(customTenants)}
	}
	if sawOwnTenant {
		return DataScopePolicy{Type: DataScopeOwnTenant}
	}

	// 默认：own_only
	return DataScopePolicy{Type: DataScopeOwnOnly}
}

// sortUintSet 把 uint 集合转为有序切片
func sortUintSet(set map[uint]struct{}) []uint {
	out := make([]uint, 0, len(set))
	for id := range set {
		out = append(out, id)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// InvalidateUserPerms 失效指定用户的权限缓存，应在 user_roles 变动后调用
func (r *Resolver) InvalidateUserPerms(ctx context.Context, userIDs ...uint) error {
	if r.redis == nil || len(userIDs) == 0 {
		return nil
	}
	keys := make([]string, 0, len(userIDs))
	for _, uid := range userIDs {
		if uid != 0 {
			keys = append(keys, cacheKey(uid))
		}
	}
	if len(keys) == 0 {
		return nil
	}
	return r.redis.Del(ctx, keys...).Err()
}

// InvalidateByRoleID 失效某个角色下所有用户的缓存，应在 role_permissions 或 roles.data_scope 变动后调用
func (r *Resolver) InvalidateByRoleID(ctx context.Context, roleID uint) error {
	if roleID == 0 {
		return nil
	}
	var userIDs []uint
	if err := r.db.WithContext(ctx).Model(&model.UserRole{}).
		Where("role_id = ?", roleID).
		Pluck("user_id", &userIDs).Error; err != nil {
		return fmt.Errorf("invalidate by role: %w", err)
	}
	return r.InvalidateUserPerms(ctx, userIDs...)
}
