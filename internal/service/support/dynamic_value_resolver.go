package support

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"

	goredis "github.com/redis/go-redis/v9"
	"go.uber.org/zap"
	"gorm.io/gorm"

	"tokenhub-server/internal/model"
	"tokenhub-server/internal/pkg/logger"
)

// DynamicValueResolver 动态值解析器
//
// 负责把文档/FAQ/热门问题中的占位符 {{ref:namespace.key}} 替换为系统当前实际值。
// 这样管理员调整邀请返佣比例、模型价格后，AI 客服回答会**立即反映最新数值**，
// 无需重建 embedding 向量（向量基于语义不受具体数字影响，数字只在最终 prompt 注入时替换）。
//
// 支持的命名空间：
//   referral.*          - 读 referral_configs 表
//   quota.*             - 读 quota_configs 表
//   pricing.model.<key> - 读 ai_models + model_pricings
//   member.levels_summary - 会员等级摘要
//   system.*            - 读 system_configs
//
// 语法示例：
//   {{ref:referral.commission_rate_pct}}  -> "10"
//   {{ref:referral.attribution_days}}     -> "90"
//   {{ref:referral.min_withdraw_rmb}}     -> "100"
//   {{ref:quota.default_free_rmb}}        -> "0.3"
//   {{ref:quota.inviter_bonus_rmb}}       -> "1"
//   {{ref:pricing.model.glm-4.display}}   -> "¥5.00 / ¥5.00 每百万 tokens"
//
// 结果带 Redis 缓存（5 min TTL），管理员改配置后可调 InvalidateCache() 强制刷新。
type DynamicValueResolver struct {
	db    *gorm.DB
	redis *goredis.Client
}

// NewDynamicValueResolver 构造解析器
func NewDynamicValueResolver(db *gorm.DB, redis *goredis.Client) *DynamicValueResolver {
	return &DynamicValueResolver{db: db, redis: redis}
}

// 占位符语法：{{ref:namespace.key}} 或 {{ref:namespace.key.subkey}}
// key 允许 . _ - 字母数字
var placeholderRegex = regexp.MustCompile(`\{\{ref:([a-zA-Z][a-zA-Z0-9._\-]*)\}\}`)

// Resolve 替换文本中的所有占位符
// 未匹配到的占位符保留原样（不抛错，降级容错）
func (r *DynamicValueResolver) Resolve(ctx context.Context, text string) string {
	if text == "" || !strings.Contains(text, "{{ref:") {
		return text
	}
	values := r.loadAllValues(ctx)
	return placeholderRegex.ReplaceAllStringFunc(text, func(match string) string {
		// match 形如 "{{ref:xxx.yyy}}"
		sub := placeholderRegex.FindStringSubmatch(match)
		if len(sub) < 2 {
			return match
		}
		key := sub[1]
		if v, ok := values[key]; ok {
			return v
		}
		// 未命中：保留占位符但去掉 ref: 前缀，提示管理员修正
		return "<未知值:" + key + ">"
	})
}

// ResolveMany 批量渲染（节省一次 loadAllValues）
func (r *DynamicValueResolver) ResolveMany(ctx context.Context, texts []string) []string {
	if len(texts) == 0 {
		return texts
	}
	// 先检查是否有任何文本包含占位符
	hasAny := false
	for _, t := range texts {
		if strings.Contains(t, "{{ref:") {
			hasAny = true
			break
		}
	}
	if !hasAny {
		return texts
	}
	values := r.loadAllValues(ctx)
	out := make([]string, len(texts))
	for i, t := range texts {
		out[i] = placeholderRegex.ReplaceAllStringFunc(t, func(match string) string {
			sub := placeholderRegex.FindStringSubmatch(match)
			if len(sub) < 2 {
				return match
			}
			if v, ok := values[sub[1]]; ok {
				return v
			}
			return "<未知值:" + sub[1] + ">"
		})
	}
	return out
}

// InvalidateCache 强制清理缓存（管理员改配置后调用）
func (r *DynamicValueResolver) InvalidateCache(ctx context.Context) {
	if r.redis == nil {
		return
	}
	_ = r.redis.Del(ctx, "support:dyn_values").Err()
}

// loadAllValues 从 Redis 缓存或 DB 读取全部动态值
func (r *DynamicValueResolver) loadAllValues(ctx context.Context) map[string]string {
	const cacheKey = "support:dyn_values"
	// 优先从 Redis 读
	if r.redis != nil {
		if str, err := r.redis.Get(ctx, cacheKey).Result(); err == nil && str != "" {
			return parseCachedValues(str)
		}
	}

	values := make(map[string]string)
	r.loadReferralValues(ctx, values)
	r.loadQuotaValues(ctx, values)
	r.loadMemberSummary(ctx, values)
	r.loadPricingValues(ctx, values)
	// 默认兜底：支持联系方式
	values["system.support_email"] = getSystemConfig(r.db, ctx, "support.contact_email", "support@tokenhubhk.com")

	// 写缓存 5 分钟
	if r.redis != nil {
		_ = r.redis.Set(ctx, cacheKey, serializeCachedValues(values), 5*time.Minute).Err()
	}
	return values
}

// loadReferralValues 读取邀请返佣配置 (referral_configs, is_active=true)
func (r *DynamicValueResolver) loadReferralValues(ctx context.Context, out map[string]string) {
	var cfg model.ReferralConfig
	if err := r.db.WithContext(ctx).Where("is_active = ?", true).First(&cfg).Error; err != nil {
		// DB 无记录时写兜底默认值
		out["referral.commission_rate_pct"] = "10"
		out["referral.attribution_days"] = "90"
		out["referral.lifetime_cap_rmb"] = "3000"
		out["referral.min_paid_rmb"] = "10"
		out["referral.min_withdraw_rmb"] = "100"
		out["referral.settle_days"] = "7"
		return
	}
	out["referral.commission_rate_pct"] = fmt.Sprintf("%.0f", cfg.CommissionRate*100)
	out["referral.attribution_days"] = fmt.Sprintf("%d", cfg.AttributionDays)
	out["referral.lifetime_cap_rmb"] = creditsToRMBString(cfg.LifetimeCapCredits)
	out["referral.min_paid_rmb"] = creditsToRMBString(cfg.MinPaidCreditsUnlock)
	out["referral.min_withdraw_rmb"] = creditsToRMBString(cfg.MinWithdrawAmount)
	out["referral.settle_days"] = fmt.Sprintf("%d", cfg.SettleDays)
}

// loadQuotaValues 读取注册赠送配置
func (r *DynamicValueResolver) loadQuotaValues(ctx context.Context, out map[string]string) {
	var cfg model.QuotaConfig
	if err := r.db.WithContext(ctx).Where("is_active = ?", true).First(&cfg).Error; err != nil {
		out["quota.default_free_rmb"] = "0.3"
		out["quota.invitee_bonus_rmb"] = "0.5"
		out["quota.invitee_unlock_rmb"] = "1"
		out["quota.inviter_bonus_rmb"] = "1"
		out["quota.inviter_unlock_rmb"] = "10"
		out["quota.inviter_monthly_cap"] = "10"
		return
	}
	out["quota.default_free_rmb"] = creditsToRMBString(cfg.DefaultFreeQuota)
	out["quota.registration_bonus_rmb"] = creditsToRMBString(cfg.RegistrationBonus)
	out["quota.invitee_bonus_rmb"] = creditsToRMBString(cfg.InviteeBonus)
	out["quota.invitee_unlock_rmb"] = creditsToRMBString(cfg.InviteeUnlockCredits)
	out["quota.inviter_bonus_rmb"] = creditsToRMBString(cfg.InviterBonus)
	out["quota.inviter_unlock_rmb"] = creditsToRMBString(cfg.InviterUnlockPaidRMB)
	out["quota.inviter_monthly_cap"] = fmt.Sprintf("%d", cfg.InviterMonthlyCap)
}

// loadMemberSummary 读会员等级做摘要
func (r *DynamicValueResolver) loadMemberSummary(ctx context.Context, out map[string]string) {
	var levels []model.MemberLevel
	if err := r.db.WithContext(ctx).Where("is_active = ?", true).Order("level_rank ASC").Find(&levels).Error; err != nil || len(levels) == 0 {
		out["member.levels_summary"] = "V0-V4 五级会员，按累计消费自动升级，高级别享受折扣。"
		return
	}
	var parts []string
	for _, lv := range levels {
		discPct := int((1.0 - lv.ModelDiscount) * 100)
		if discPct <= 0 {
			parts = append(parts, fmt.Sprintf("%s %s", lv.LevelCode, lv.LevelName))
		} else {
			parts = append(parts, fmt.Sprintf("%s %s（%d%% 折扣）", lv.LevelCode, lv.LevelName, discPct))
		}
	}
	out["member.levels_summary"] = strings.Join(parts, "；")
}

// loadPricingValues 读取主力模型的当前售价（示例：glm-4 / qwen-plus）
// 完整所有模型价格不在此列（太多），仅预置几个高频引用的
func (r *DynamicValueResolver) loadPricingValues(ctx context.Context, out map[string]string) {
	highlighted := []string{"glm-4", "qwen-plus", "qwen-turbo", "doubao-pro", "deepseek-chat", "kimi-k2"}
	for _, name := range highlighted {
		inCost, outCost := queryModelPricing(r.db, ctx, name)
		if inCost < 0 {
			continue
		}
		out[fmt.Sprintf("pricing.model.%s.input_rmb_per_m", name)] = formatPrice(inCost)
		out[fmt.Sprintf("pricing.model.%s.output_rmb_per_m", name)] = formatPrice(outCost)
		out[fmt.Sprintf("pricing.model.%s.display", name)] = fmt.Sprintf("输入 ¥%s / 输出 ¥%s 每百万 tokens", formatPrice(inCost), formatPrice(outCost))
	}
}

// queryModelPricing 查模型售价
// 优先从 model_pricings（平台售价，已含渠道折扣/加价后的对外价格）
// fallback 到 ai_models.input_cost_rmb（成本价，若无售价配置时兜底）
// 返回 (inputPerMRMB, outputPerMRMB)；查询失败返回 (-1, -1)
func queryModelPricing(db *gorm.DB, ctx context.Context, modelName string) (float64, float64) {
	var mp struct {
		Input  float64 `gorm:"column:input_price_rmb"`
		Output float64 `gorm:"column:output_price_rmb"`
	}
	err := db.WithContext(ctx).Raw(`
		SELECT mp.input_price_rmb, mp.output_price_rmb
		FROM model_pricings mp
		JOIN ai_models am ON am.id = mp.model_id
		WHERE am.name = ?
		ORDER BY mp.created_at DESC
		LIMIT 1
	`, modelName).Scan(&mp).Error
	if err == nil && (mp.Input > 0 || mp.Output > 0) {
		return mp.Input, mp.Output
	}
	// fallback 到成本价（客服答复时标注「参考价」）
	var cost struct {
		Input  float64 `gorm:"column:input_cost_rmb"`
		Output float64 `gorm:"column:output_cost_rmb"`
	}
	if err := db.WithContext(ctx).Raw(
		"SELECT input_cost_rmb, output_cost_rmb FROM ai_models WHERE name = ? LIMIT 1",
		modelName,
	).Scan(&cost).Error; err != nil {
		logger.L.Debug("pricing lookup failed", zap.String("model", modelName), zap.Error(err))
		return -1, -1
	}
	if cost.Input == 0 && cost.Output == 0 {
		return -1, -1
	}
	return cost.Input, cost.Output
}

// getSystemConfig 读 system_configs 表
func getSystemConfig(db *gorm.DB, ctx context.Context, key, fallback string) string {
	var cfg model.SystemConfig
	if err := db.WithContext(ctx).Where("`key` = ?", key).First(&cfg).Error; err != nil {
		return fallback
	}
	if cfg.Value == "" {
		return fallback
	}
	return cfg.Value
}

// creditsToRMBString 积分转 RMB 字符串（1 RMB = 10000 积分，去末尾零）
func creditsToRMBString(credits int64) string {
	rmb := float64(credits) / 10000.0
	if rmb == float64(int64(rmb)) {
		return fmt.Sprintf("%d", int64(rmb))
	}
	s := fmt.Sprintf("%.2f", rmb)
	// 去末尾 0
	for strings.HasSuffix(s, "0") {
		s = strings.TrimSuffix(s, "0")
	}
	s = strings.TrimSuffix(s, ".")
	return s
}

func formatPrice(v float64) string {
	if v == float64(int64(v)) {
		return fmt.Sprintf("%d", int64(v))
	}
	return strings.TrimRight(strings.TrimRight(fmt.Sprintf("%.4f", v), "0"), ".")
}

// 简单 map<->string 序列化（使用 | 和 = 分隔，避免 JSON 引入额外开销）
func serializeCachedValues(m map[string]string) string {
	var sb strings.Builder
	first := true
	for k, v := range m {
		if !first {
			sb.WriteByte('|')
		}
		first = false
		sb.WriteString(k)
		sb.WriteByte('=')
		// 转义 | 和 =
		sb.WriteString(strings.NewReplacer("|", `\|`, "=", `\=`).Replace(v))
	}
	return sb.String()
}

func parseCachedValues(s string) map[string]string {
	out := make(map[string]string)
	if s == "" {
		return out
	}
	// 简单解析（不支持嵌套转义，生产可换 JSON）
	parts := strings.Split(s, "|")
	for _, p := range parts {
		idx := strings.Index(p, "=")
		if idx <= 0 {
			continue
		}
		k := p[:idx]
		v := strings.NewReplacer(`\|`, "|", `\=`, "=").Replace(p[idx+1:])
		out[k] = v
	}
	return out
}
