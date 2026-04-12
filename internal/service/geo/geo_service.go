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
)

// ========================================================================
// GeoService — IP 地理位置服务
// 根据客户端 IP 地址判断所属国家，映射为平台支持的语言代码。
// 支持三级 fallback 外部数据源: ip-api.com → ipinfo.io → ip.sb
// 查询结果缓存在 Redis 中（TTL 24h），避免重复调用外部 API。
// ========================================================================

const (
	// redisTTL 地理位置缓存过期时间 24 小时
	redisTTL = 24 * time.Hour
	// redisKeyPrefix Redis 缓存键前缀
	redisKeyPrefix = "geo:ip:"
	// httpTimeout 外部 API 请求超时
	httpTimeout = 5 * time.Second
)

// GeoResult 地理位置查询结果
type GeoResult struct {
	Locale      string `json:"locale"`       // 推荐的语言代码 (如 "zh", "en", "ja")
	CountryCode string `json:"country_code"` // ISO 3166-1 alpha-2 国家代码
	Source      string `json:"source"`       // 数据来源标识 ("ip-api" / "ipinfo" / "ip.sb" / "cache" / "default")
}

// GeoService IP 地理位置服务，支持多数据源 fallback 和 Redis 缓存
type GeoService struct {
	redisClient *goredis.Client
	httpClient  *http.Client
}

// NewGeoService 创建地理位置服务实例
// 参数:
//   - redisClient: Redis 客户端（可为 nil，此时不启用缓存）
func NewGeoService(redisClient *goredis.Client) *GeoService {
	return &GeoService{
		redisClient: redisClient,
		httpClient: &http.Client{
			Timeout: httpTimeout,
		},
	}
}

// DetectLocale 根据 IP 地址检测推荐语言
// 处理流程: Redis 缓存 → ip-api.com → ipinfo.io → ip.sb → 默认 "en"
// 参数:
//   - ctx: 上下文
//   - ip: 客户端 IP 地址
//
// 返回:
//   - *GeoResult: 检测结果（语言代码、国家代码、数据来源）
func (s *GeoService) DetectLocale(ctx context.Context, ip string) *GeoResult {
	// 私有/本地 IP 直接返回默认语言
	if isPrivateIP(ip) {
		return &GeoResult{Locale: "en", CountryCode: "", Source: "default"}
	}

	// 尝试从 Redis 缓存读取
	if s.redisClient != nil {
		if cached := s.getFromCache(ctx, ip); cached != nil {
			return cached
		}
	}

	// 依次尝试三个外部 API 数据源
	providers := []struct {
		name string
		fn   func(ctx context.Context, ip string) (string, error)
	}{
		{"ip-api", s.queryIPAPI},
		{"ipinfo", s.queryIPInfo},
		{"ip.sb", s.queryIPSB},
	}

	for _, p := range providers {
		countryCode, err := p.fn(ctx, ip)
		if err == nil && countryCode != "" {
			locale := CountryToLocale(countryCode)
			result := &GeoResult{
				Locale:      locale,
				CountryCode: countryCode,
				Source:      p.name,
			}
			// 异步写入 Redis 缓存
			if s.redisClient != nil {
				go s.setCache(context.Background(), ip, result)
			}
			return result
		}
	}

	// 所有数据源均失败，返回默认语言
	return &GeoResult{Locale: "en", CountryCode: "", Source: "default"}
}

// ========================================================================
// Redis 缓存操作
// ========================================================================

// getFromCache 从 Redis 缓存中读取 IP 对应的地理位置结果
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

// setCache 将地理位置结果写入 Redis 缓存
func (s *GeoService) setCache(ctx context.Context, ip string, result *GeoResult) {
	key := redisKeyPrefix + ip
	data, err := json.Marshal(result)
	if err != nil {
		return
	}
	_ = s.redisClient.Set(ctx, key, string(data), redisTTL).Err()
}

// ========================================================================
// 外部 API 数据源 (三级 fallback)
// ========================================================================

// ipAPIResponse ip-api.com 返回结构
type ipAPIResponse struct {
	CountryCode string `json:"countryCode"`
	Country     string `json:"country"`
	Status      string `json:"status"`
}

// queryIPAPI 通过 ip-api.com 查询国家代码（免费，45 req/min 限制）
func (s *GeoService) queryIPAPI(ctx context.Context, ip string) (string, error) {
	url := fmt.Sprintf("http://ip-api.com/json/%s?fields=countryCode,country,status", ip)
	body, err := s.doGet(ctx, url)
	if err != nil {
		return "", err
	}
	var resp ipAPIResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return "", fmt.Errorf("ip-api 解析失败: %w", err)
	}
	if resp.Status != "success" {
		return "", fmt.Errorf("ip-api 返回失败状态: %s", resp.Status)
	}
	return strings.ToUpper(resp.CountryCode), nil
}

// ipInfoResponse ipinfo.io 返回结构
type ipInfoResponse struct {
	Country string `json:"country"`
}

// queryIPInfo 通过 ipinfo.io 查询国家代码（免费额度 50K/月）
func (s *GeoService) queryIPInfo(ctx context.Context, ip string) (string, error) {
	url := fmt.Sprintf("https://ipinfo.io/%s/json", ip)
	body, err := s.doGet(ctx, url)
	if err != nil {
		return "", err
	}
	var resp ipInfoResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return "", fmt.Errorf("ipinfo 解析失败: %w", err)
	}
	if resp.Country == "" {
		return "", fmt.Errorf("ipinfo 未返回国家代码")
	}
	return strings.ToUpper(resp.Country), nil
}

// ipSBResponse ip.sb 返回结构
type ipSBResponse struct {
	CountryCode string `json:"country_code"`
}

// queryIPSB 通过 ip.sb 查询国家代码（免费）
func (s *GeoService) queryIPSB(ctx context.Context, ip string) (string, error) {
	url := fmt.Sprintf("https://api.ip.sb/geoip/%s", ip)
	body, err := s.doGet(ctx, url)
	if err != nil {
		return "", err
	}
	var resp ipSBResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return "", fmt.Errorf("ip.sb 解析失败: %w", err)
	}
	if resp.CountryCode == "" {
		return "", fmt.Errorf("ip.sb 未返回国家代码")
	}
	return strings.ToUpper(resp.CountryCode), nil
}

// doGet 执行 HTTP GET 请求，返回响应体
func (s *GeoService) doGet(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "TokenHub-GeoService/1.0")
	req.Header.Set("Accept", "application/json")

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 10240)) // 限制读取 10KB
	if err != nil {
		return nil, err
	}
	return body, nil
}

// ========================================================================
// IP 工具函数
// ========================================================================

// isPrivateIP 判断 IP 是否为私有/本地地址
// 包括: 127.0.0.0/8, 10.0.0.0/8, 172.16.0.0/12, 192.168.0.0/16, ::1, fc00::/7
func isPrivateIP(ipStr string) bool {
	if ipStr == "" || ipStr == "::1" || ipStr == "localhost" {
		return true
	}
	ip := net.ParseIP(ipStr)
	if ip == nil {
		return true // 无法解析的 IP 视为私有
	}
	// 常见私有网段
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
// 参数:
//   - countryCode: 两位大写国家代码 (如 "CN", "US")
//
// 返回:
//   - 对应的语言代码，未匹配时返回 "en"
func CountryToLocale(countryCode string) string {
	code := strings.ToUpper(strings.TrimSpace(countryCode))
	if locale, ok := countryToLocaleMap[code]; ok {
		return locale
	}
	return "en"
}

// GetClientIP 从 HTTP 请求中提取客户端真实 IP
// 优先级: X-Forwarded-For → X-Real-IP → RemoteAddr
func GetClientIP(r *http.Request) string {
	// X-Forwarded-For 可能包含多个 IP（经过多层代理），取第一个
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		parts := strings.Split(xff, ",")
		ip := strings.TrimSpace(parts[0])
		if ip != "" {
			return ip
		}
	}
	// X-Real-IP 通常由 Nginx 设置
	if xri := r.Header.Get("X-Real-IP"); xri != "" {
		return strings.TrimSpace(xri)
	}
	// 降级使用 RemoteAddr（可能包含端口号）
	ip, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return ip
}
