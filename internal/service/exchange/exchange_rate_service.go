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
	"tokenhub-server/internal/pkg/dbctx"
)

const (
	// CurrencyUSD 缇庡厓
	CurrencyUSD = "USD"
	// CurrencyCNY 人民币
	CurrencyCNY = "CNY"

	// SourcePrimary 来自主接口
	SourcePrimary = "aliyun_primary"
	// SourceBackup 来自备用接口
	SourceBackup = "aliyun_backup"
	// SourcePublic 鏉ヨ嚜鍏紑鍏嶈垂鎺ュ彛锛坥pen.er-api.com锛屽紑绠卞嵆鐢ㄥ厹搴曪級
	SourcePublic = "public_free"
	// SourceManual 管理员手动覆盖
	SourceManual = "manual"
	// SourceDefault 默认值
	SourceDefault = "default"

	// 姹囩巼鍚堢悊鑼冨洿锛堥槻鑴忔暟鎹級
	minSaneRate = 0.1
	maxSaneRate = 100.0

	// 公开免费汇率接口 URL
	defaultPublicURL = "https://open.er-api.com/v6/latest/USD"
)

// Config 姹囩巼鏈嶅姟閰嶇疆
type Config struct {
	PrimaryURL     string
	BackupURL      string
	PublicURL      string // 公开免费 fallback URL
	AppCode        string
	AppKey         string
	AppSecret      string
	CacheTTL       time.Duration // 榛樿 24h
	DefaultRate    float64       // 榛樿 7.2
	RequestTimeout time.Duration // 榛樿 10s
}

// HTTPDoer 用于 mock 测试的 HTTP 客户端接口
type HTTPDoer interface {
	Do(req *http.Request) (*http.Response, error)
}

// EventSink 抽象事件日志写入
type EventSink interface {
	LogExchangeEvent(ctx context.Context, eventType, source string, success bool, payload, result interface{}, err error, durationMs int64)
}

// ExchangeRateService 姹囩巼鏈嶅姟锛堜富澶?fallback + Redis 缂撳瓨 + DB 鎸佷箙鍖栵級
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

// SetHTTPDoer 娉ㄥ叆鑷畾涔?HTTP 瀹㈡埛绔紙娴嬭瘯鐢級
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

// FetchAndCacheDaily 姣忔棩瀹氭椂浠诲姟鍏ュ彛
//  1. 璋冧富鎺ュ彛 鈫?鍐?DB + Redis
//  2. 涓绘帴鍙ｅけ璐?鈫?璋冨鐢ㄦ帴鍙?鈫?鍐?DB + Redis锛坰ource=aliyun_backup锛?//   3. 涓や釜閮藉け璐?鈫?涓嶈鐩栫紦瀛橈紝璁板綍 failed 浜嬩欢
func (s *ExchangeRateService) FetchAndCacheDaily(ctx context.Context) error {
	s.fetchMu.Lock()
	defer s.fetchMu.Unlock()

	// singleflight锛? 绉掑唴閲嶅璋冪敤鐩存帴澶嶇敤缁撴灉
	if time.Since(s.lastFetchAt) < 5*time.Second && s.lastFetchVal > 0 {
		return nil
	}

	// 涓夌骇 fallback 閾撅細
	//   L1 aliyun_primary锛堝鏋?URL 宸查厤缃級 鈫?	//   L2 aliyun_backup锛堝鏋?URL 宸查厤缃級 鈫?	//   L3 public_free锛坥pen.er-api.com 鍏嶈垂銆佸紑鏀撅紱淇濊瘉寮€绠卞嵆鐢級
	var rate float64
	var source, raw string
	var lastErr error

	// L1 主接口。
	if s.cfg.PrimaryURL != "" {
		r, src, body, err := s.fetchFromPrimary(ctx)
		if err != nil {
			log().Warn("exchange rate L1 primary failed, falling back to L2 backup",
				zap.String("url", s.cfg.PrimaryURL), zap.Error(err))
			s.logEvent(ctx, model.EventExchangeRateFallback, SourcePrimary, false, nil, nil, err, 0)
			lastErr = err
		} else {
			rate, source, raw = r, src, body
		}
	}

	// L2 澶囩敤锛堜粎褰?L1 鏈垚鍔?+ URL 宸查厤缃椂鎵嶅皾璇曪級
	if rate == 0 && s.cfg.BackupURL != "" {
		r, src, body, err := s.fetchFromBackup(ctx)
		if err != nil {
			log().Warn("exchange rate L2 backup failed, falling back to L3 public",
				zap.String("url", s.cfg.BackupURL), zap.Error(err))
			s.logEvent(ctx, model.EventExchangeRateFallback, SourceBackup, false, nil, nil, err, 0)
			lastErr = err
		} else {
			rate, source, raw = r, src, body
		}
	}

	// L3 公开免费接口兜底
	if rate == 0 {
		r, src, body, err := s.fetchFromPublic(ctx)
		if err != nil {
			log().Error("exchange rate ALL sources failed (L1/L2/L3 all down)",
				zap.Error(err), zap.Any("last_err", lastErr))
			s.logEvent(ctx, model.EventExchangeRateFailed, SourcePublic, false, nil, nil, err, 0)
			return fmt.Errorf("all exchange rate sources failed: last=%w public=%v", lastErr, err)
		}
		rate, source, raw = r, src, body
	}

	if !s.isSaneRate(rate) {
		err := fmt.Errorf("rate out of sane range: %f", rate)
		log().Error("exchange rate sanity check failed", zap.Float64("rate", rate))
		s.logEvent(ctx, model.EventExchangeRateFailed, source, false, nil, rate, err, 0)
		return err
	}

	// 鍐?DB
	hist := &model.ExchangeRateHistory{
		FromCurrency: CurrencyUSD,
		ToCurrency:   CurrencyCNY,
		Rate:         rate,
		Source:       source,
		RawResponse:  raw,
		FetchedAt:    time.Now(),
	}
	if err := s.db.WithContext(ctx).Create(hist).Error; err != nil {
		log().Error("failed to persist exchange rate history", zap.Error(err))
		// DB 澶辫触涓嶉樆濉烇紝浠嶅啓 Redis
	}

	// 鍐?Redis
	if s.redis != nil {
		key := s.rateCacheKey(CurrencyUSD, CurrencyCNY)
		val := strconv.FormatFloat(rate, 'f', 8, 64)
		if err := s.redis.Set(ctx, key, val, s.cfg.CacheTTL).Err(); err != nil {
			log().Warn("failed to cache exchange rate to redis", zap.Error(err))
		}
		// 鍚屾鍏冧俊鎭紙鏇存柊鏃堕棿銆佹潵婧愶級
		_ = s.redis.Set(ctx, key+":meta", fmt.Sprintf("%d|%s", time.Now().Unix(), source), s.cfg.CacheTTL).Err()
		// 澶辨晥鍏紑鎺ュ彛 HTTP 缂撳瓨锛圕acheMiddleware 鐨?2h 缂撳瓨锛夛紝璁╃敤鎴风珛鍗崇湅鍒版柊姹囩巼
		s.invalidateHTTPCache(ctx)
	}

	s.lastFetchAt = time.Now()
	s.lastFetchVal = rate

	s.logEvent(ctx, model.EventExchangeRateFetched, source, true, nil, rate, nil, 0)
	log().Info("exchange rate fetched and cached",
		zap.Float64("rate", rate),
		zap.String("source", source),
	)
	return nil
}

// invalidateHTTPCache 娓呴櫎 Nginx/CacheMiddleware 鐨?HTTP 缂撳瓨 key
// 褰撴眹鐜囨洿鏂板悗绔嬪嵆鍙锛岄伩鍏嶇敤鎴风瓑 2h 缂撳瓨 TTL
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

// GetUSDToCNY 鑾峰彇 USD鈫扖NY 姹囩巼
// 优先级：Redis -> DB 最近一次 -> 默认值
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

	// 2. DB 最近一次，使用短超时避免阻塞
	dbCtx, dbCancel := dbctx.Short(ctx)
	defer dbCancel()
	var hist model.ExchangeRateHistory
	err := s.db.WithContext(dbCtx).
		Where("from_currency = ? AND to_currency = ?", CurrencyUSD, CurrencyCNY).
		Order("fetched_at DESC").
		First(&hist).Error
	if err == nil && s.isSaneRate(hist.Rate) {
		return hist.Rate, nil
	}

	// 3. 默认值
	log().Warn("falling back to default exchange rate", zap.Float64("default", s.cfg.DefaultRate))
	return s.cfg.DefaultRate, nil
}

// GetUSDToCNYWithMeta 杩斿洖姹囩巼鍙婂厓淇℃伅锛堟潵婧愩€佹洿鏂版椂闂达級
func (s *ExchangeRateService) GetUSDToCNYWithMeta(ctx context.Context) (rate float64, source string, updatedAt time.Time, err error) {
	// 浼樺厛 Redis
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

	// DB 鏌ヨ甯?2s 瓒呮椂锛岄槻姝㈡參鏌ヨ瀵艰嚧 /public/exchange-rate 姘镐箙闃诲
	dbCtx, dbCancel := dbctx.Short(ctx)
	defer dbCancel()
	var hist model.ExchangeRateHistory
	dberr := s.db.WithContext(dbCtx).
		Where("from_currency = ? AND to_currency = ?", CurrencyUSD, CurrencyCNY).
		Order("fetched_at DESC").
		First(&hist).Error
	if dberr == nil && s.isSaneRate(hist.Rate) {
		return hist.Rate, hist.Source, hist.FetchedAt, nil
	}

	return s.cfg.DefaultRate, SourceDefault, time.Now(), nil
}

// ConvertUSDToCNY 灏?USD 閲戦鎹㈢畻涓?CNY
func (s *ExchangeRateService) ConvertUSDToCNY(ctx context.Context, usd float64) float64 {
	rate, _ := s.GetUSDToCNY(ctx)
	return round2(usd * rate)
}

// ConvertCNYToUSD 灏?CNY 閲戦鎹㈢畻涓?USD
func (s *ExchangeRateService) ConvertCNYToUSD(ctx context.Context, cny float64) float64 {
	rate, _ := s.GetUSDToCNY(ctx)
	if rate <= 0 {
		return 0
	}
	return round2(cny / rate)
}

// ManualOverride 绠＄悊鍛樻墜鍔ㄨ鐩栨眹鐜囷紙搴旀€ワ級
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
		s.invalidateHTTPCache(ctx) // 鍚屾澶辨晥 HTTP 缂撳瓨
	}
	s.logEvent(ctx, model.EventExchangeRateOverride, SourceManual, true, map[string]interface{}{
		"admin_id": adminID,
		"reason":   reason,
	}, rate, nil, 0)
	return nil
}

// ListHistory 列出历史汇率
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

// fetchFromPrimary 璋冧富鎺ュ彛锛堥樋閲屼簯浜戝競鍦?cmapi00064402 - 鑱氱編鏅烘暟 姹囩巼杞崲锛?//
//
//	POST https://jmhlcx.market.alicloudapi.com/exchange-rate/convert
//	Headers: Authorization: APPCODE {appcode}
//	         Content-Type: application/x-www-form-urlencoded
//	Body:    fromCode=USD&toCode=CNY&money=1
//	鍝嶅簲锛?  {"data":{"money":"6.8095"},"msg":"鎴愬姛","success":true,"code":200,"taskNo":"..."}
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

// fetchFromBackup 璋冨鐢ㄦ帴鍙ｏ紙闃块噷浜戜簯甯傚満 cmapi00063890 - 鏁拌剦API 涓浗閾惰姹囩巼锛?//
//
//	GET https://smkjzgyhss.market.alicloudapi.com/exchange_rate/realtime?code=USD
//	Headers: Authorization: APPCODE {appcode}
//	鍝嶅簲锛?  {"success":true,"code":200,"data":{"list":[{"code":"USD","zhesuan":"686.22",...}]}}
//	璇存槑锛?  zhesuan 涓?100 澶栧竵鎶樼畻浜烘皯甯?锛堜緥濡?USD 涓€缁勪负 100 缇庡厓瀵瑰簲 686.22 鍏冿級
//	        鍙?USD 涓€琛?zhesuan/100 寰楀埌 1 USD 鈫?CNY 鐨勬眹鐜囷紙~6.8622锛?
func (s *ExchangeRateService) fetchFromBackup(ctx context.Context) (rate float64, source string, raw string, err error) {
	if s.cfg.BackupURL == "" {
		return 0, SourceBackup, "", fmt.Errorf("backup url empty")
	}
	// 使用 URL 参数 code=USD，让服务端尽量只返回 USD 汇率。
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

// fetchFromPublic 璋冨叕寮€鍏嶈垂鎺ュ彛锛堝厹搴曪紝鏃犻渶 AppCode锛?// 鎺ュ彛锛歰pen.er-api.com/v6/latest/USD
// 鍝嶅簲绀轰緥锛?//
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

// doHTTPNoAuth 涓嶅甫 AppCode 鐨?GET锛堝叕寮€鎺ュ彛鐢級
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

// doHTTPPostForm 鍙戣捣 POST form-urlencoded 璇锋眰锛屽甫 APPCODE 閴存潈
// 闃块噷浜戜簯甯傚満 API 涓绘帴鍙ｏ紙cmapi00064402锛変娇鐢ㄦ鏂规硶
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

// doHTTP 鍙戣捣 GET 璇锋眰骞堕檮甯?APPCODE 閴存潈
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

// parsePrimaryResponse 瑙ｆ瀽闃块噷浜?cmapi00064402 涓绘帴鍙ｅ搷搴?//
//
//	{"data":{"money":"6.8095"},"msg":"鎴愬姛","success":true,"code":200,"taskNo":"..."}
//
// 鐢变簬 money 瀛楁鏄瓧绗︿覆锛堜笉鏄?float锛夛紝浣跨敤 json.Number 瑙ｆ瀽閬垮厤绮惧害涓㈠け
func parsePrimaryResponse(body []byte) (float64, error) {
	var resp struct {
		Code    int    `json:"code"`
		Msg     string `json:"msg"`
		Success bool   `json:"success"`
		Data    struct {
			// money 鏃㈠彲鑳芥槸瀛楃涓?"6.8095" 涔熷彲鑳芥槸鏁板瓧 6.8095锛岀敤 json.Number 鍏滃簳
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

// parsePublicResponse 解析公开免费接口响应
func parsePublicResponse(body []byte) (float64, error) {
	var resp struct {
		Result   string             `json:"result"`
		BaseCode string             `json:"base_code"`
		Rates    map[string]float64 `json:"rates"`
		// 备用兼容：有些 mirror 使用 conversion_rates 字段
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

// parseBackupResponse 瑙ｆ瀽闃块噷浜?cmapi00063890 澶囩敤鎺ュ彛鍝嶅簲锛堜腑鍥介摱琛屽疄鏃舵眹鐜囷級
//
//	{
//	  "success": true, "code": 200, "msg": "鎴愬姛",
//	  "data": {
//	    "list": [
//	      {"code":"USD","zhesuan":"686.22","hui_in":"680.95","hui_out":"683.82",
//	       "chao_out":"683.82","chao_in":"680.95","name":"缇庡厓","time":"05:30:00","day":"2026-04-18"},
//	      ...
//	    ]
//	  }
//	}
//
// 鍙栧€艰鍒欙細
//  1. 鍦?list 涓壘 code="USD" 鐨勮
//  2. 优先 zhesuan；除以 100
//  3. 若 zhesuan 缺失，使用买入/卖出均值
func parseBackupResponse(body []byte) (float64, error) {
	var resp struct {
		Code    int    `json:"code"`
		Msg     string `json:"msg"`
		Success bool   `json:"success"`
		Data    struct {
			List []struct {
				Code    string      `json:"code"`
				Name    string      `json:"name"`
				Zhesuan json.Number `json:"zhesuan"`
				HuiIn   json.Number `json:"hui_in"`
				HuiOut  json.Number `json:"hui_out"`
				ChaoIn  json.Number `json:"chao_in"`
				ChaoOut json.Number `json:"chao_out"`
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
		// 棣栭€?zhesuan
		if z := numFrom(item.Zhesuan); z > 0 {
			return z / 100.0, nil
		}
		// 鍏滃簳锛氱幇姹囦拱鍏?鍗栧嚭鍙栦腑闂翠环
		huiIn := numFrom(item.HuiIn)
		huiOut := numFrom(item.HuiOut)
		if huiIn > 0 && huiOut > 0 {
			return (huiIn + huiOut) / 2.0 / 100.0, nil
		}
		// 鍐嶅厹搴曪細鐜伴挒涔板叆+鍗栧嚭
		chaoIn := numFrom(item.ChaoIn)
		chaoOut := numFrom(item.ChaoOut)
		if chaoIn > 0 && chaoOut > 0 {
			return (chaoIn + chaoOut) / 2.0 / 100.0, nil
		}
		return 0, fmt.Errorf("backup USD row has no valid rate fields")
	}
	return 0, fmt.Errorf("backup: USD not found in list of %d items", len(resp.Data.List))
}

// parseMeta 瑙ｆ瀽 Redis 涓瓨鍌ㄧ殑鍏冧俊鎭?"{unix_ts}|{source}"
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

// logEvent 寮傛鍐欎簨浠舵棩蹇楋紙闈為樆濉烇級
func (s *ExchangeRateService) logEvent(ctx context.Context, eventType, source string, success bool, payload, result interface{}, err error, durationMs int64) {
	if s.eventSink == nil {
		return
	}
	go s.eventSink.LogExchangeEvent(ctx, eventType, source, success, payload, result, err, durationMs)
}

// round2 保留 2 位小数。
func round2(v float64) float64 {
	return float64(int64(v*100+0.5)) / 100
}
