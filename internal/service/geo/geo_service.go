package geo

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	goredis "github.com/redis/go-redis/v9"
	"tokenhub-server/internal/config"
)

// ========================================================================
// GeoService — IP 地理位置服务
// 根据客户端 IP 地址判断所属国家，映射为平台支持的语言代码。
// 数据源架构：
//   L0: 阿里云市场 cmapi021970（https://c2ba.api.huachen.cn/ip，APPCODE 鉴权）
//   L1-L6: 6 个免费 API 并发竞速（ip-api / ipinfo / ip.sb / ipwho / country.is / geojs）
// 查询结果缓存在 Redis 中（TTL 可配，默认 1 年），避免重复调用外部 API。
// ========================================================================

const (
	redisKeyPrefix = "geo:ip:"
)

// GeoResult 地理位置查询结果
type GeoResult struct {
	Locale      string `json:"locale"`       // 推荐的语言代码 (如 "zh", "en", "ja")
	CountryCode string `json:"country_code"` // ISO 3166-1 alpha-2 国家代码
	Source      string `json:"source"`       // 数据来源标识 ("aliyun" / "ip-api" / "ipinfo" / "ip.sb" / "ipwho" / "country.is" / "geojs" / "cache" / "default")
}

// GeoService IP 地理位置服务，支持多数据源 fallback 和 Redis 缓存
type GeoService struct {
	redisClient      *goredis.Client
	cfg              config.GeoConfig
	httpClient       *http.Client
	aliyunTimeout    time.Duration
	fallbackTimeout  time.Duration
	providerTimeout  time.Duration
	cacheTTL         time.Duration
}

// NewGeoService 创建地理位置服务实例
// 参数:
//   - redisClient: Redis 客户端（可为 nil，此时不启用缓存）
//   - cfg: 地理位置服务配置（可传零值 config.GeoConfig{}，内部会应用安全默认值）
func NewGeoService(redisClient *goredis.Client, cfg ...config.GeoConfig) *GeoService {
	var c config.GeoConfig
	if len(cfg) > 0 {
		c = cfg[0]
	}
	// 零值防护（测试场景或漏配）
	if c.AliyunURL == "" {
		c.AliyunURL = "https://c2ba.api.huachen.cn/ip"
	}
	if c.AppCode == "" {
		c.AppCode = "dcbcacff20e7413ab50231113b364655"
	}
	if c.CacheTTL == 0 {
		c.CacheTTL = 31536000
	}
	if c.AliyunTimeoutMs == 0 {
		c.AliyunTimeoutMs = 2500
	}
	if c.FallbackTimeoutMs == 0 {
		c.FallbackTimeoutMs = 3000
	}
	if c.SingleProviderTimeout == 0 {
		c.SingleProviderTimeout = 2500
	}
	return &GeoService{
		redisClient: redisClient,
		cfg:         c,
		httpClient: &http.Client{
			// 整体 client 超时兜底（最长路径 = aliyun + fallback）
			Timeout: time.Duration(c.AliyunTimeoutMs+c.FallbackTimeoutMs+500) * time.Millisecond,
		},
		aliyunTimeout:   time.Duration(c.AliyunTimeoutMs) * time.Millisecond,
		fallbackTimeout: time.Duration(c.FallbackTimeoutMs) * time.Millisecond,
		providerTimeout: time.Duration(c.SingleProviderTimeout) * time.Millisecond,
		cacheTTL:        time.Duration(c.CacheTTL) * time.Second,
	}
}

// DetectLocale 根据 IP 地址检测推荐语言
// 处理流程: private-IP 短路 → Redis 缓存 → 阿里云 L0 → 免费源并发竞速 → 默认 "en"
func (s *GeoService) DetectLocale(ctx context.Context, ip string) *GeoResult {
	if isPrivateIP(ip) {
		return &GeoResult{Locale: "en", CountryCode: "", Source: "default"}
	}

	// L0-: Redis 缓存
	if s.redisClient != nil {
		if cached := s.getFromCache(ctx, ip); cached != nil {
			return cached
		}
	}

	// L0: 阿里云市场主源（独立超时，不走竞速）
	aliyunCtx, cancelAliyun := context.WithTimeout(ctx, s.aliyunTimeout)
	country, err := s.queryAliyun(aliyunCtx, ip)
	cancelAliyun()
	if err == nil && country != "" {
		result := &GeoResult{
			Locale:      CountryToLocale(country),
			CountryCode: country,
			Source:      "aliyun",
		}
		if s.redisClient != nil {
			go s.setCache(context.Background(), ip, result)
		}
		return result
	}

	// L1+: 6 个免费源并发竞速
	providers := []struct {
		name string
		fn   func(context.Context, string) (string, error)
	}{
		{"ip-api", s.queryIPAPI},
		{"ipinfo", s.queryIPInfo},
		{"ip.sb", s.queryIPSB},
		{"ipwho", s.queryIPWho},
		{"country.is", s.queryCountryIs},
		{"geojs", s.queryGeoJS},
	}
	if name, cc, ok := s.raceProviders(ctx, ip, providers); ok {
		result := &GeoResult{
			Locale:      CountryToLocale(cc),
			CountryCode: cc,
			Source:      name,
		}
		if s.redisClient != nil {
			go s.setCache(context.Background(), ip, result)
		}
		return result
	}

	// 全部失败 → 默认 "en"
	return &GeoResult{Locale: "en", CountryCode: "", Source: "default"}
}

// raceProviders 并发请求多个免费数据源，返回第一个成功结果
// 未被选中的 goroutine 由 context.Cancel 统一终止，不会泄漏
func (s *GeoService) raceProviders(
	parent context.Context,
	ip string,
	providers []struct {
		name string
		fn   func(context.Context, string) (string, error)
	},
) (string, string, bool) {
	ctx, cancel := context.WithTimeout(parent, s.fallbackTimeout)
	defer cancel()

	type result struct {
		name    string
		country string
	}
	ch := make(chan result, len(providers))
	for _, p := range providers {
		p := p
		go func() {
			cc, err := p.fn(ctx, ip)
			if err == nil && cc != "" {
				select {
				case ch <- result{name: p.name, country: strings.ToUpper(cc)}:
				case <-ctx.Done():
				}
			} else {
				// 非成功结果也投递（name="" 标识失败），保证主循环不会全等直到超时
				select {
				case ch <- result{name: "", country: ""}:
				case <-ctx.Done():
				}
			}
		}()
	}

	failed := 0
	for {
		select {
		case r := <-ch:
			if r.name != "" && r.country != "" {
				return r.name, r.country, true
			}
			failed++
			if failed >= len(providers) {
				return "", "", false
			}
		case <-ctx.Done():
			return "", "", false
		}
	}
}

// ========================================================================
// Redis 缓存操作
// ========================================================================

func (s *GeoService) getFromCache(ctx context.Context, ip string) *GeoResult {
	key := redisKeyPrefix + ip
	val, err := s.redisClient.Get(ctx, key).Result()
	if err != nil {
		return nil
	}
	var result GeoResult
	if err := json.Unmarshal([]byte(val), &result); err != nil {
		return nil
	}
	result.Source = "cache"
	return &result
}

func (s *GeoService) setCache(ctx context.Context, ip string, result *GeoResult) {
	key := redisKeyPrefix + ip
	data, err := json.Marshal(result)
	if err != nil {
		return
	}
	_ = s.redisClient.Set(ctx, key, string(data), s.cacheTTL).Err()
}

// ========================================================================
// 外部 API 数据源实现
// ========================================================================

// aliyunResponse 阿里云市场 cmapi021970 返回结构
// 实测响应：{"ret":200,"msg":"success","data":{"country_id":"CN","country":"中国","region":"广东",...}}
type aliyunResponse struct {
	Ret  int    `json:"ret"`
	Msg  string `json:"msg"`
	Data struct {
		IP        string `json:"ip"`
		CountryID string `json:"country_id"` // ISO 两位码，如 "CN"、"US"
		Country   string `json:"country"`    // 中文名
		Region    string `json:"region"`     // 省份中文名，对台港澳特殊处理依据
	} `json:"data"`
}

// queryAliyun 通过阿里云市场 cmapi021970 查询国家代码
// 注意：阿里云对台港澳返回 country_id="CN" + region="台湾/香港/澳门"，需按 region 覆盖
func (s *GeoService) queryAliyun(ctx context.Context, ip string) (string, error) {
	url := fmt.Sprintf("%s?ip=%s", s.cfg.AliyunURL, ip)
	body, err := s.doGetWithHeaders(ctx, url, map[string]string{
		"Authorization": "APPCODE " + s.cfg.AppCode,
	})
	if err != nil {
		return "", err
	}
	var resp aliyunResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return "", fmt.Errorf("aliyun 解析失败: %w", err)
	}
	if resp.Ret != 200 {
		return "", fmt.Errorf("aliyun 返回失败状态 ret=%d msg=%s", resp.Ret, resp.Msg)
	}
	cc := strings.ToUpper(strings.TrimSpace(resp.Data.CountryID))
	if cc == "" {
		return "", fmt.Errorf("aliyun 未返回 country_id")
	}
	// 台港澳特殊处理：country_id=CN 但 region 为台湾/香港/澳门时覆盖为对应 ISO
	if cc == "CN" {
		switch strings.TrimSpace(resp.Data.Region) {
		case "台湾", "臺灣":
			return "TW", nil
		case "香港":
			return "HK", nil
		case "澳门", "澳門":
			return "MO", nil
		}
	}
	return cc, nil
}

// ipAPIResponse ip-api.com 返回结构
type ipAPIResponse struct {
	CountryCode string `json:"countryCode"`
	Status      string `json:"status"`
}

func (s *GeoService) queryIPAPI(ctx context.Context, ip string) (string, error) {
	url := fmt.Sprintf("http://ip-api.com/json/%s?fields=countryCode,status", ip)
	body, err := s.doGet(ctx, url)
	if err != nil {
		return "", err
	}
	var resp ipAPIResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return "", err
	}
	if resp.Status != "success" {
		return "", fmt.Errorf("ip-api status=%s", resp.Status)
	}
	return strings.ToUpper(resp.CountryCode), nil
}

type ipInfoResponse struct {
	Country string `json:"country"`
}

func (s *GeoService) queryIPInfo(ctx context.Context, ip string) (string, error) {
	url := fmt.Sprintf("https://ipinfo.io/%s/json", ip)
	body, err := s.doGet(ctx, url)
	if err != nil {
		return "", err
	}
	var resp ipInfoResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return "", err
	}
	if resp.Country == "" {
		return "", fmt.Errorf("ipinfo empty country")
	}
	return strings.ToUpper(resp.Country), nil
}

type ipSBResponse struct {
	CountryCode string `json:"country_code"`
}

func (s *GeoService) queryIPSB(ctx context.Context, ip string) (string, error) {
	url := fmt.Sprintf("https://api.ip.sb/geoip/%s", ip)
	body, err := s.doGet(ctx, url)
	if err != nil {
		return "", err
	}
	var resp ipSBResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return "", err
	}
	if resp.CountryCode == "" {
		return "", fmt.Errorf("ip.sb empty country_code")
	}
	return strings.ToUpper(resp.CountryCode), nil
}

// ipWhoResponse ipwho.is 返回结构
type ipWhoResponse struct {
	Success     bool   `json:"success"`
	CountryCode string `json:"country_code"`
}

func (s *GeoService) queryIPWho(ctx context.Context, ip string) (string, error) {
	url := fmt.Sprintf("https://ipwho.is/%s", ip)
	body, err := s.doGet(ctx, url)
	if err != nil {
		return "", err
	}
	var resp ipWhoResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return "", err
	}
	if !resp.Success || resp.CountryCode == "" {
		return "", fmt.Errorf("ipwho unsuccessful")
	}
	return strings.ToUpper(resp.CountryCode), nil
}

// countryIsResponse country.is 返回结构：{"ip":"x","country":"CN"}
type countryIsResponse struct {
	Country string `json:"country"`
}

func (s *GeoService) queryCountryIs(ctx context.Context, ip string) (string, error) {
	url := fmt.Sprintf("https://api.country.is/%s", ip)
	body, err := s.doGet(ctx, url)
	if err != nil {
		return "", err
	}
	var resp countryIsResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return "", err
	}
	if resp.Country == "" {
		return "", fmt.Errorf("country.is empty country")
	}
	return strings.ToUpper(resp.Country), nil
}

// geoJSResponse geojs.io 返回结构：{"country":"CN","ip":"x","name":"China"}
type geoJSResponse struct {
	Country string `json:"country"`
}

func (s *GeoService) queryGeoJS(ctx context.Context, ip string) (string, error) {
	url := fmt.Sprintf("https://get.geojs.io/v1/ip/country/%s.json", ip)
	body, err := s.doGet(ctx, url)
	if err != nil {
		return "", err
	}
	var resp geoJSResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return "", err
	}
	if resp.Country == "" {
		return "", fmt.Errorf("geojs empty country")
	}
	return strings.ToUpper(resp.Country), nil
}

// doGet 执行 HTTP GET 请求（免费源使用单次 providerTimeout）
func (s *GeoService) doGet(ctx context.Context, url string) ([]byte, error) {
	return s.doGetWithHeaders(ctx, url, nil)
}

// doGetWithHeaders 通用 HTTP GET，可自定义请求头
func (s *GeoService) doGetWithHeaders(ctx context.Context, url string, headers map[string]string) ([]byte, error) {
	// 为每个请求派生短超时（在父 ctx 之下），避免慢源拖累竞速
	perReqCtx, cancel := context.WithTimeout(ctx, s.providerTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(perReqCtx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "TokenHub-GeoService/1.0")
	req.Header.Set("Accept", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := s.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 10240))
}

// ========================================================================
// IP 工具函数
// ========================================================================

// isPrivateIP 判断 IP 是否为私有/本地地址
func isPrivateIP(ipStr string) bool {
	if ipStr == "" || ipStr == "::1" || ipStr == "localhost" {
		return true
	}
	ip := net.ParseIP(ipStr)
	if ip == nil {
		return true
	}
	privateRanges := []string{
		"127.0.0.0/8",
		"10.0.0.0/8",
		"172.16.0.0/12",
		"192.168.0.0/16",
		"fc00::/7",
		"fe80::/10",
	}
	for _, cidr := range privateRanges {
		_, network, err := net.ParseCIDR(cidr)
		if err != nil {
			continue
		}
		if network.Contains(ip) {
			return true
		}
	}
	return false
}

// ========================================================================
// 国家代码 → 语言代码映射
// ========================================================================

// countryToLocaleMap 国家代码到语言代码的映射表
// 覆盖主要国家和地区，未匹配的返回默认 "en"
var countryToLocaleMap = map[string]string{
	// 中文区
	"CN": "zh",
	"TW": "zh-TW",
	"HK": "zh-TW",
	"MO": "zh-TW",
	// 英语区
	"US": "en",
	"GB": "en",
	"AU": "en",
	"CA": "en",
	"NZ": "en",
	"IE": "en",
	"SG": "en",
	// 日韩
	"JP": "ja",
	"KR": "ko",
	// 欧洲
	"DE": "de",
	"AT": "de",
	"CH": "de",
	"FR": "fr",
	"BE": "fr",
	"ES": "es",
	"MX": "es",
	"AR": "es",
	"CO": "es",
	"CL": "es",
	"PE": "es",
	"PT": "pt",
	"BR": "pt",
	"RU": "ru",
	"IT": "it",
	"NL": "nl",
	"PL": "pl",
	"TR": "tr",
	// 东南亚
	"TH": "th",
	"VN": "vi",
	"ID": "id",
	"MY": "ms",
	"PH": "tl",
	// 中东
	"SA": "ar",
	"AE": "ar",
	"EG": "ar",
	"QA": "ar",
	"KW": "ar",
	"IR": "fa",
	"IL": "he",
	// 南亚
	"IN": "hi",
}

// CountryToLocale 将 ISO 3166-1 国家代码转换为平台支持的语言代码
func CountryToLocale(countryCode string) string {
	code := strings.ToUpper(strings.TrimSpace(countryCode))
	if locale, ok := countryToLocaleMap[code]; ok {
		return locale
	}
	return "en"
}

// GetClientIP 从 HTTP 请求中提取客户端真实 IP
func GetClientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		parts := strings.Split(xff, ",")
		ip := strings.TrimSpace(parts[0])
		if ip != "" {
			return ip
		}
	}
	if xri := r.Header.Get("X-Real-IP"); xri != "" {
		return strings.TrimSpace(xri)
	}
	ip, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return ip
}
