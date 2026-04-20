package exchange

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	neturl "net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	goredis "github.com/redis/go-redis/v9"
	"go.uber.org/zap"
	"gorm.io/gorm"

	"tokenhub-server/internal/model"
	"tokenhub-server/internal/pkg/logger"
)

const (
	// CurrencyUSD 美元
	CurrencyUSD = "USD"
	// CurrencyCNY 人民币
	CurrencyCNY = "CNY"

	// SourcePrimary 来自主接口（阿里云市场 cmapi00064402）
	SourcePrimary = "aliyun_primary"
	// SourceBackup 来自备用接口（阿里云市场 cmapi00063890）
	SourceBackup = "aliyun_backup"
	// SourcePublic 来自公开免费接口（open.er-api.com，开箱即用兜底）
	SourcePublic = "public_free"
	// SourceManual 管理员手动覆盖
	SourceManual = "manual"
	// SourceDefault 默认值
	SourceDefault = "default"

	// 汇率合理范围（防脏数据）
	minSaneRate = 0.1
	maxSaneRate = 100.0

	// 公开免费汇率接口 URL（无需 key、无需签名；作为第三级 fallback）
	// API: https://www.exchangerate-api.com/docs/free （开放、每日更新、响应 <100ms）
	defaultPublicURL = "https://open.er-api.com/v6/latest/USD"
)

// Config 汇率服务配置
type Config struct {
	PrimaryURL     string
	BackupURL      string
	PublicURL      string // 公开免费 fallback URL（open.er-api.com 兼容格式；默认 defaultPublicURL）
	AppCode        string
	AppKey         string
	AppSecret      string
	CacheTTL       time.Duration // 默认 24h
	DefaultRate    float64       // 默认 7.2
	RequestTimeout time.Duration // 默认 10s
}

// HTTPDoer 用于 mock 测试的 HTTP 客户端接口
type HTTPDoer interface {
	Do(req *http.Request) (*http.Response, error)
}

// EventSink 抽象事件日志写入（避免循环依赖 service/payment）
type EventSink interface {
	LogExchangeEvent(ctx context.Context, eventType, source string, success bool, payload, result interface{}, err error, durationMs int64)
}

// ExchangeRateService 汇率服务（主备 fallback + Redis 缓存 + DB 持久化）
type ExchangeRateService struct {
	db        *gorm.DB
	redis     *goredis.Client
	cfg       Config
	httpDoer  HTTPDoer
	eventSink EventSink

	// singleflight 防并发重复拉取
	fetchMu      sync.Mutex
	lastFetchAt  time.Time
	lastFetchVal float64
}

// New 构造函数
func New(db *gorm.DB, redis *goredis.Client, cfg Config) *ExchangeRateService {
	if cfg.CacheTTL <= 0 {
		cfg.CacheTTL = 24 * time.Hour
	}
	if cfg.DefaultRate <= 0 {
		cfg.DefaultRate = 7.2
	}
	if cfg.RequestTimeout <= 0 {
		cfg.RequestTimeout = 10 * time.Second
	}
	if cfg.PublicURL == "" {
		cfg.PublicURL = defaultPublicURL
	}
	return &ExchangeRateService{
		db:       db,
		redis:    redis,
		cfg:      cfg,
		httpDoer: &http.Client{Timeout: cfg.RequestTimeout},
	}
}

// SetHTTPDoer 注入自定义 HTTP 客户端（测试用）
func (s *ExchangeRateService) SetHTTPDoer(d HTTPDoer) {
	s.httpDoer = d
}

// SetEventSink 注入事件日志写入器
func (s *ExchangeRateService) SetEventSink(sink EventSink) {
	s.eventSink = sink
}

// rateCacheKey 缓存键
func (s *ExchangeRateService) rateCacheKey(from, to string) string {
	return fmt.Sprintf("fx:%s_%s", from, to)
}

// FetchAndCacheDaily 每日定时任务入口
//   1. 调主接口 → 写 DB + Redis
//   2. 主接口失败 → 调备用接口 → 写 DB + Redis（source=aliyun_backup）
//   3. 两个都失败 → 不覆盖缓存，记录 failed 事件
func (s *ExchangeRateService) FetchAndCacheDaily(ctx context.Context) error {
	s.fetchMu.Lock()
	defer s.fetchMu.Unlock()

	// singleflight：5 秒内重复调用直接复用结果
	if time.Since(s.lastFetchAt) < 5*time.Second && s.lastFetchVal > 0 {
		return nil
	}

	// 三级 fallback 链：
	//   L1 aliyun_primary（如果 URL 已配置） →
	//   L2 aliyun_backup（如果 URL 已配置） →
	//   L3 public_free（open.er-api.com 免费、开放；保证开箱即用）
	var rate float64
	var source, raw string
	var lastErr error

	// L1 主接口（仅当配置了 URL 时才尝试）
	if s.cfg.PrimaryURL != "" {
		r, src, body, err := s.fetchFromPrimary(ctx)
		if err != nil {
			logger.L.Warn("exchange rate L1 primary failed, falling back to L2 backup",
				zap.String("url", s.cfg.PrimaryURL), zap.Error(err))
			s.logEvent(ctx, model.EventExchangeRateFallback, SourcePrimary, false, nil, nil, err, 0)
			lastErr = err
		} else {
			rate, source, raw = r, src, body
		}
	}

	// L2 备用（仅当 L1 未成功 + URL 已配置时才尝试）
	if rate == 0 && s.cfg.BackupURL != "" {
		r, src, body, err := s.fetchFromBackup(ctx)
		if err != nil {
			logger.L.Warn("exchange rate L2 backup failed, falling back to L3 public",
				zap.String("url", s.cfg.BackupURL), zap.Error(err))
			s.logEvent(ctx, model.EventExchangeRateFallback, SourceBackup, false, nil, nil, err, 0)
			lastErr = err
		} else {
			rate, source, raw = r, src, body
		}
	}

	// L3 公开免费接口（兜底，必跑）
	if rate == 0 {
		r, src, body, err := s.fetchFromPublic(ctx)
		if err != nil {
			logger.L.Error("exchange rate ALL sources failed (L1/L2/L3 all down)",
				zap.Error(err), zap.Any("last_err", lastErr))
			s.logEvent(ctx, model.EventExchangeRateFailed, SourcePublic, false, nil, nil, err, 0)
			return fmt.Errorf("all exchange rate sources failed: last=%w public=%v", lastErr, err)
		}
		rate, source, raw = r, src, body
	}

	if !s.isSaneRate(rate) {
		err := fmt.Errorf("rate out of sane range: %f", rate)
		logger.L.Error("exchange rate sanity check failed", zap.Float64("rate", rate))
		s.logEvent(ctx, model.EventExchangeRateFailed, source, false, nil, rate, err, 0)
		return err
	}

	// 写 DB
	hist := &model.ExchangeRateHistory{
		FromCurrency: CurrencyUSD,
		ToCurrency:   CurrencyCNY,
		Rate:         rate,
		Source:       source,
		RawResponse:  raw,
		FetchedAt:    time.Now(),
	}
	if err := s.db.WithContext(ctx).Create(hist).Error; err != nil {
		logger.L.Error("failed to persist exchange rate history", zap.Error(err))
		// DB 失败不阻塞，仍写 Redis
	}

	// 写 Redis
	if s.redis != nil {
		key := s.rateCacheKey(CurrencyUSD, CurrencyCNY)
		val := strconv.FormatFloat(rate, 'f', 8, 64)
		if err := s.redis.Set(ctx, key, val, s.cfg.CacheTTL).Err(); err != nil {
			logger.L.Warn("failed to cache exchange rate to redis", zap.Error(err))
		}
		// 同步元信息（更新时间、来源）
		_ = s.redis.Set(ctx, key+":meta", fmt.Sprintf("%d|%s", time.Now().Unix(), source), s.cfg.CacheTTL).Err()
		// 失效公开接口 HTTP 缓存（CacheMiddleware 的 2h 缓存），让用户立即看到新汇率
		s.invalidateHTTPCache(ctx)
	}

	s.lastFetchAt = time.Now()
	s.lastFetchVal = rate

	s.logEvent(ctx, model.EventExchangeRateFetched, source, true, nil, rate, nil, 0)
	logger.L.Info("exchange rate fetched and cached",
		zap.Float64("rate", rate),
		zap.String("source", source),
	)
	return nil
}

// invalidateHTTPCache 清除 Nginx/CacheMiddleware 的 HTTP 缓存 key
// 当汇率更新后立即可见，避免用户等 2h 缓存 TTL
func (s *ExchangeRateService) invalidateHTTPCache(ctx context.Context) {
	if s.redis == nil {
		return
	}
	keys := []string{
		"cache:/api/v1/public/exchange-rate",
	}
	for _, k := range keys {
		_ = s.redis.Del(ctx, k).Err()
	}
}

// GetUSDToCNY 获取 USD→CNY 汇率
//   优先级：Redis → DB 最近一次 → 默认值
func (s *ExchangeRateService) GetUSDToCNY(ctx context.Context) (float64, error) {
	// 1. Redis
	if s.redis != nil {
		key := s.rateCacheKey(CurrencyUSD, CurrencyCNY)
		val, err := s.redis.Get(ctx, key).Result()
		if err == nil && val != "" {
			if rate, perr := strconv.ParseFloat(val, 64); perr == nil && s.isSaneRate(rate) {
				return rate, nil
			}
		}
	}

	// 2. DB 最近一次
	var hist model.ExchangeRateHistory
	err := s.db.WithContext(ctx).
		Where("from_currency = ? AND to_currency = ?", CurrencyUSD, CurrencyCNY).
		Order("fetched_at DESC").
		First(&hist).Error
	if err == nil && s.isSaneRate(hist.Rate) {
		return hist.Rate, nil
	}

	// 3. 默认值
	logger.L.Warn("falling back to default exchange rate", zap.Float64("default", s.cfg.DefaultRate))
	return s.cfg.DefaultRate, nil
}

// GetUSDToCNYWithMeta 返回汇率及元信息（来源、更新时间）
func (s *ExchangeRateService) GetUSDToCNYWithMeta(ctx context.Context) (rate float64, source string, updatedAt time.Time, err error) {
	// 优先 Redis
	if s.redis != nil {
		key := s.rateCacheKey(CurrencyUSD, CurrencyCNY)
		val, gerr := s.redis.Get(ctx, key).Result()
		if gerr == nil && val != "" {
			if r, perr := strconv.ParseFloat(val, 64); perr == nil && s.isSaneRate(r) {
				meta, _ := s.redis.Get(ctx, key+":meta").Result()
				ts, src := parseMeta(meta)
				return r, src, ts, nil
			}
		}
	}

	var hist model.ExchangeRateHistory
	dberr := s.db.WithContext(ctx).
		Where("from_currency = ? AND to_currency = ?", CurrencyUSD, CurrencyCNY).
		Order("fetched_at DESC").
		First(&hist).Error
	if dberr == nil && s.isSaneRate(hist.Rate) {
		return hist.Rate, hist.Source, hist.FetchedAt, nil
	}

	return s.cfg.DefaultRate, SourceDefault, time.Now(), nil
}

// ConvertUSDToCNY 将 USD 金额换算为 CNY
func (s *ExchangeRateService) ConvertUSDToCNY(ctx context.Context, usd float64) float64 {
	rate, _ := s.GetUSDToCNY(ctx)
	return round2(usd * rate)
}

// ConvertCNYToUSD 将 CNY 金额换算为 USD
func (s *ExchangeRateService) ConvertCNYToUSD(ctx context.Context, cny float64) float64 {
	rate, _ := s.GetUSDToCNY(ctx)
	if rate <= 0 {
		return 0
	}
	return round2(cny / rate)
}

// ManualOverride 管理员手动覆盖汇率（应急）
func (s *ExchangeRateService) ManualOverride(ctx context.Context, rate float64, adminID uint64, reason string) error {
	if !s.isSaneRate(rate) {
		return fmt.Errorf("rate out of sane range: %f", rate)
	}
	hist := &model.ExchangeRateHistory{
		FromCurrency: CurrencyUSD,
		ToCurrency:   CurrencyCNY,
		Rate:         rate,
		Source:       SourceManual,
		RawResponse:  fmt.Sprintf(`{"admin_id":%d,"reason":%q}`, adminID, reason),
		FetchedAt:    time.Now(),
	}
	if err := s.db.WithContext(ctx).Create(hist).Error; err != nil {
		return err
	}
	if s.redis != nil {
		key := s.rateCacheKey(CurrencyUSD, CurrencyCNY)
		_ = s.redis.Set(ctx, key, strconv.FormatFloat(rate, 'f', 8, 64), s.cfg.CacheTTL).Err()
		_ = s.redis.Set(ctx, key+":meta", fmt.Sprintf("%d|%s", time.Now().Unix(), SourceManual), s.cfg.CacheTTL).Err()
		s.invalidateHTTPCache(ctx) // 同步失效 HTTP 缓存
	}
	s.logEvent(ctx, model.EventExchangeRateOverride, SourceManual, true, map[string]interface{}{
		"admin_id": adminID,
		"reason":   reason,
	}, rate, nil, 0)
	return nil
}

// ListHistory 列出历史汇率（管理后台用）
func (s *ExchangeRateService) ListHistory(ctx context.Context, limit int) ([]model.ExchangeRateHistory, error) {
	if limit <= 0 || limit > 1000 {
		limit = 30
	}
	var rows []model.ExchangeRateHistory
	err := s.db.WithContext(ctx).
		Where("from_currency = ? AND to_currency = ?", CurrencyUSD, CurrencyCNY).
		Order("fetched_at DESC").
		Limit(limit).
		Find(&rows).Error
	return rows, err
}

// isSaneRate 汇率合理性校验
func (s *ExchangeRateService) isSaneRate(rate float64) bool {
	return rate >= minSaneRate && rate <= maxSaneRate
}

// fetchFromPrimary 调主接口（阿里云云市场 cmapi00064402 - 聚美智数 汇率转换）
//
//	POST https://jmhlcx.market.alicloudapi.com/exchange-rate/convert
//	Headers: Authorization: APPCODE {appcode}
//	         Content-Type: application/x-www-form-urlencoded
//	Body:    fromCode=USD&toCode=CNY&money=1
//	响应：   {"data":{"money":"6.8095"},"msg":"成功","success":true,"code":200,"taskNo":"..."}
func (s *ExchangeRateService) fetchFromPrimary(ctx context.Context) (rate float64, source string, raw string, err error) {
	if s.cfg.PrimaryURL == "" {
		return 0, SourcePrimary, "", fmt.Errorf("primary url empty")
	}
	body, err := s.doHTTPPostForm(ctx, s.cfg.PrimaryURL, map[string]string{
		"fromCode": CurrencyUSD,
		"toCode":   CurrencyCNY,
		"money":    "1",
	})
	if err != nil {
		return 0, SourcePrimary, "", err
	}
	rate, perr := parsePrimaryResponse(body)
	if perr != nil {
		return 0, SourcePrimary, string(body), perr
	}
	return rate, SourcePrimary, string(body), nil
}

// fetchFromBackup 调备用接口（阿里云云市场 cmapi00063890 - 数脉API 中国银行汇率）
//
//	GET https://smkjzgyhss.market.alicloudapi.com/exchange_rate/realtime?code=USD
//	Headers: Authorization: APPCODE {appcode}
//	响应：   {"success":true,"code":200,"data":{"list":[{"code":"USD","zhesuan":"686.22",...}]}}
//	说明：   zhesuan 为"100 外币折算人民币"（例如 USD 一组为 100 美元对应 686.22 元）
//	        取 USD 一行 zhesuan/100 得到 1 USD → CNY 的汇率（~6.8622）
func (s *ExchangeRateService) fetchFromBackup(ctx context.Context) (rate float64, source string, raw string, err error) {
	if s.cfg.BackupURL == "" {
		return 0, SourceBackup, "", fmt.Errorf("backup url empty")
	}
	// 使用 URL 参数 code=USD 让服务端过滤；但实际测试表明 code 参数在"一行返回"/"多行返回"上不稳定，
	// 无论传什么参数都倾向于返回全列表。我们依旧加上参数以保持协议一致。
	url := fmt.Sprintf("%s?code=USD", s.cfg.BackupURL)
	body, err := s.doHTTP(ctx, url)
	if err != nil {
		return 0, SourceBackup, "", err
	}
	rate, perr := parseBackupResponse(body)
	if perr != nil {
		return 0, SourceBackup, string(body), perr
	}
	return rate, SourceBackup, string(body), nil
}

// fetchFromPublic 调公开免费接口（兜底，无需 AppCode）
// 接口：open.er-api.com/v6/latest/USD
// 响应示例：
//
//	{
//	  "result": "success",
//	  "base_code": "USD",
//	  "time_last_update_utc": "Wed, 17 Apr 2026 00:00:01 +0000",
//	  "rates": {"CNY": 7.2345, "EUR": 0.93, ...}
//	}
func (s *ExchangeRateService) fetchFromPublic(ctx context.Context) (rate float64, source string, raw string, err error) {
	url := s.cfg.PublicURL
	if url == "" {
		url = defaultPublicURL
	}
	body, err := s.doHTTPNoAuth(ctx, url)
	if err != nil {
		return 0, SourcePublic, "", err
	}
	rate, perr := parsePublicResponse(body)
	if perr != nil {
		return 0, SourcePublic, string(body), perr
	}
	return rate, SourcePublic, string(body), nil
}

// doHTTPNoAuth 不带 AppCode 的 GET（公开接口用）
func (s *ExchangeRateService) doHTTPNoAuth(ctx context.Context, url string) ([]byte, error) {
	reqCtx, cancel := context.WithTimeout(ctx, s.cfg.RequestTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "TokenHubHK/1.0 (+ExchangeRate)")

	resp, err := s.httpDoer.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("public http %d: %s", resp.StatusCode, string(body))
	}
	return io.ReadAll(io.LimitReader(resp.Body, 65536))
}

// doHTTPPostForm 发起 POST form-urlencoded 请求，带 APPCODE 鉴权
// 阿里云云市场 API 主接口（cmapi00064402）使用此方法
func (s *ExchangeRateService) doHTTPPostForm(ctx context.Context, apiURL string, form map[string]string) ([]byte, error) {
	reqCtx, cancel := context.WithTimeout(ctx, s.cfg.RequestTimeout)
	defer cancel()

	values := neturl.Values{}
	for k, v := range form {
		values.Set(k, v)
	}
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, apiURL, strings.NewReader(values.Encode()))
	if err != nil {
		return nil, err
	}
	if s.cfg.AppCode != "" {
		req.Header.Set("Authorization", "APPCODE "+s.cfg.AppCode)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded; charset=UTF-8")
	req.Header.Set("Accept", "application/json")

	resp, err := s.httpDoer.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("http %d: %s", resp.StatusCode, string(body))
	}
	return io.ReadAll(io.LimitReader(resp.Body, 65536))
}

// doHTTP 发起 GET 请求并附带 APPCODE 鉴权
func (s *ExchangeRateService) doHTTP(ctx context.Context, url string) ([]byte, error) {
	reqCtx, cancel := context.WithTimeout(ctx, s.cfg.RequestTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	if s.cfg.AppCode != "" {
		req.Header.Set("Authorization", "APPCODE "+s.cfg.AppCode)
	}
	req.Header.Set("Accept", "application/json")

	resp, err := s.httpDoer.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("http %d: %s", resp.StatusCode, string(body))
	}
	return io.ReadAll(io.LimitReader(resp.Body, 65536))
}

// parsePrimaryResponse 解析阿里云 cmapi00064402 主接口响应
//
//	{"data":{"money":"6.8095"},"msg":"成功","success":true,"code":200,"taskNo":"..."}
//
// 由于 money 字段是字符串（不是 float），使用 json.Number 解析避免精度丢失
func parsePrimaryResponse(body []byte) (float64, error) {
	var resp struct {
		Code    int    `json:"code"`
		Msg     string `json:"msg"`
		Success bool   `json:"success"`
		Data    struct {
			// money 既可能是字符串 "6.8095" 也可能是数字 6.8095，用 json.Number 兜底
			Money json.Number `json:"money"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return 0, fmt.Errorf("parse primary json: %w", err)
	}
	if resp.Code != 200 && resp.Code != 0 {
		return 0, fmt.Errorf("primary api code=%d msg=%s", resp.Code, resp.Msg)
	}
	if resp.Data.Money == "" {
		return 0, fmt.Errorf("primary money empty")
	}
	money, err := resp.Data.Money.Float64()
	if err != nil {
		return 0, fmt.Errorf("primary money parse: %w", err)
	}
	if money <= 0 {
		return 0, fmt.Errorf("primary money invalid: %f", money)
	}
	return money, nil
}

// parsePublicResponse 解析公开免费接口响应（open.er-api.com 格式）
func parsePublicResponse(body []byte) (float64, error) {
	var resp struct {
		Result   string             `json:"result"`
		BaseCode string             `json:"base_code"`
		Rates    map[string]float64 `json:"rates"`
		// 备用兼容：有些 mirror 使用 "conversion_rates" 字段名
		ConversionRates map[string]float64 `json:"conversion_rates"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return 0, fmt.Errorf("parse public json: %w", err)
	}
	if resp.Result != "" && resp.Result != "success" {
		return 0, fmt.Errorf("public api result=%s", resp.Result)
	}
	rates := resp.Rates
	if len(rates) == 0 {
		rates = resp.ConversionRates
	}
	if rates == nil {
		return 0, fmt.Errorf("public response has no rates")
	}
	cny, ok := rates["CNY"]
	if !ok || cny <= 0 {
		return 0, fmt.Errorf("public response missing CNY rate")
	}
	return cny, nil
}

// parseBackupResponse 解析阿里云 cmapi00063890 备用接口响应（中国银行实时汇率）
//
//	{
//	  "success": true, "code": 200, "msg": "成功",
//	  "data": {
//	    "list": [
//	      {"code":"USD","zhesuan":"686.22","hui_in":"680.95","hui_out":"683.82",
//	       "chao_out":"683.82","chao_in":"680.95","name":"美元","time":"05:30:00","day":"2026-04-18"},
//	      ...
//	    ]
//	  }
//	}
//
// 取值规则：
//  1. 在 list 中找 code="USD" 的行
//  2. 优先 zhesuan（中行"折算价"，最接近中间价）；除以 100（银行报价为每 100 外币换人民币）
//  3. 若 zhesuan 缺失，用 (hui_in + hui_out)/2/100 作为中间价
func parseBackupResponse(body []byte) (float64, error) {
	var resp struct {
		Code    int    `json:"code"`
		Msg     string `json:"msg"`
		Success bool   `json:"success"`
		Data    struct {
			List []struct {
				Code     string      `json:"code"`
				Name     string      `json:"name"`
				Zhesuan  json.Number `json:"zhesuan"`
				HuiIn    json.Number `json:"hui_in"`
				HuiOut   json.Number `json:"hui_out"`
				ChaoIn   json.Number `json:"chao_in"`
				ChaoOut  json.Number `json:"chao_out"`
			} `json:"list"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return 0, fmt.Errorf("parse backup json: %w", err)
	}
	if resp.Code != 200 && resp.Code != 0 {
		return 0, fmt.Errorf("backup api code=%d msg=%s", resp.Code, resp.Msg)
	}

	numFrom := func(n json.Number) float64 {
		if n == "" {
			return 0
		}
		v, _ := n.Float64()
		return v
	}

	for _, item := range resp.Data.List {
		if item.Code != "USD" {
			continue
		}
		// 首选 zhesuan
		if z := numFrom(item.Zhesuan); z > 0 {
			return z / 100.0, nil
		}
		// 兜底：现汇买入+卖出取中间价
		huiIn := numFrom(item.HuiIn)
		huiOut := numFrom(item.HuiOut)
		if huiIn > 0 && huiOut > 0 {
			return (huiIn + huiOut) / 2.0 / 100.0, nil
		}
		// 再兜底：现钞买入+卖出
		chaoIn := numFrom(item.ChaoIn)
		chaoOut := numFrom(item.ChaoOut)
		if chaoIn > 0 && chaoOut > 0 {
			return (chaoIn + chaoOut) / 2.0 / 100.0, nil
		}
		return 0, fmt.Errorf("backup USD row has no valid rate fields")
	}
	return 0, fmt.Errorf("backup: USD not found in list of %d items", len(resp.Data.List))
}

// parseMeta 解析 Redis 中存储的元信息 "{unix_ts}|{source}"
func parseMeta(s string) (time.Time, string) {
	if s == "" {
		return time.Time{}, SourceDefault
	}
	for i := 0; i < len(s); i++ {
		if s[i] == '|' {
			ts, _ := strconv.ParseInt(s[:i], 10, 64)
			return time.Unix(ts, 0), s[i+1:]
		}
	}
	return time.Time{}, SourceDefault
}

// logEvent 异步写事件日志（非阻塞）
func (s *ExchangeRateService) logEvent(ctx context.Context, eventType, source string, success bool, payload, result interface{}, err error, durationMs int64) {
	if s.eventSink == nil {
		return
	}
	go s.eventSink.LogExchangeEvent(ctx, eventType, source, success, payload, result, err, durationMs)
}

// round2 保留 2 位小数
func round2(v float64) float64 {
	return float64(int64(v*100+0.5)) / 100
}
