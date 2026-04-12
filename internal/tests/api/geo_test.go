package api_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"testing"

	"tokenhub-server/internal/service/geo"
)

// ========================================================================
// GeoIP 功能测试
// 覆盖: 国家代码→语言映射(1) + 私有 IP 返回默认(1) + API 响应结构(1)
//       + 未知国家代码默认 en(1) + 完整映射表覆盖(1) = 5 用例
// ========================================================================

// TestCountryToLocaleMapping 测试国家代码到语言代码的映射
func TestCountryToLocaleMapping(t *testing.T) {
	testCases := []struct {
		name        string
		countryCode string
		expected    string
	}{
		{"中国大陆→简体中文", "CN", "zh"},
		{"台湾→繁体中文", "TW", "zh-TW"},
		{"香港→繁体中文", "HK", "zh-TW"},
		{"澳门→繁体中文", "MO", "zh-TW"},
		{"美国→英语", "US", "en"},
		{"英国→英语", "GB", "en"},
		{"澳大利亚→英语", "AU", "en"},
		{"加拿大→英语", "CA", "en"},
		{"新西兰→英语", "NZ", "en"},
		{"日本→日语", "JP", "ja"},
		{"韩国→韩语", "KR", "ko"},
		{"德国→德语", "DE", "de"},
		{"奥地利→德语", "AT", "de"},
		{"瑞士→德语", "CH", "de"},
		{"法国→法语", "FR", "fr"},
		{"西班牙→西班牙语", "ES", "es"},
		{"墨西哥→西班牙语", "MX", "es"},
		{"巴西→葡萄牙语", "BR", "pt"},
		{"俄罗斯→俄语", "RU", "ru"},
		{"意大利→意大利语", "IT", "it"},
		{"荷兰→荷兰语", "NL", "nl"},
		{"波兰→波兰语", "PL", "pl"},
		{"土耳其→土耳其语", "TR", "tr"},
		{"泰国→泰语", "TH", "th"},
		{"越南→越南语", "VN", "vi"},
		{"印度尼西亚→印尼语", "ID", "id"},
		{"马来西亚→马来语", "MY", "ms"},
		{"菲律宾→菲律宾语", "PH", "tl"},
		{"沙特→阿拉伯语", "SA", "ar"},
		{"阿联酋→阿拉伯语", "AE", "ar"},
		{"伊朗→波斯语", "IR", "fa"},
		{"以色列→希伯来语", "IL", "he"},
		{"印度→印地语", "IN", "hi"},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			result := geo.CountryToLocale(tc.countryCode)
			if result != tc.expected {
				t.Errorf("CountryToLocale(%q) = %q, 期望 %q", tc.countryCode, result, tc.expected)
			}
		})
	}
}

// TestCountryToLocaleUnknown 测试未知国家代码返回默认语言 "en"
func TestCountryToLocaleUnknown(t *testing.T) {
	unknowns := []string{"XX", "ZZ", "??", "", "   "}
	for _, code := range unknowns {
		result := geo.CountryToLocale(code)
		if result != "en" {
			t.Errorf("CountryToLocale(%q) = %q, 期望 \"en\"", code, result)
		}
	}
}

// TestCountryToLocaleCaseInsensitive 测试大小写不敏感
func TestCountryToLocaleCaseInsensitive(t *testing.T) {
	testCases := []struct {
		input    string
		expected string
	}{
		{"cn", "zh"},
		{"Cn", "zh"},
		{"jp", "ja"},
		{"us", "en"},
	}
	for _, tc := range testCases {
		result := geo.CountryToLocale(tc.input)
		if result != tc.expected {
			t.Errorf("CountryToLocale(%q) = %q, 期望 %q", tc.input, result, tc.expected)
		}
	}
}

// TestDetectLocalePrivateIP 测试私有/本地 IP 返回默认语言
func TestDetectLocalePrivateIP(t *testing.T) {
	svc := geo.NewGeoService(nil) // 不需要 Redis

	privateIPs := []string{
		"127.0.0.1",
		"10.0.0.1",
		"172.16.0.1",
		"192.168.1.1",
		"::1",
		"localhost",
		"",
	}

	for _, ip := range privateIPs {
		t.Run(ip, func(t *testing.T) {
			result := svc.DetectLocale(context.Background(), ip)
			if result.Locale != "en" {
				t.Errorf("私有 IP %q 应返回 \"en\", 实际 %q", ip, result.Locale)
			}
			if result.Source != "default" {
				t.Errorf("私有 IP %q source 应为 \"default\", 实际 %q", ip, result.Source)
			}
		})
	}
}

// TestDetectLocaleAPIEndpoint 测试 detect-locale API 端点的响应结构
func TestDetectLocaleAPIEndpoint(t *testing.T) {
	url := fmt.Sprintf("%s/api/v1/public/detect-locale", baseURL)
	resp, err := http.Get(url)
	if err != nil {
		t.Skipf("服务器不可用，跳过: %v", err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("期望 200, 实际 %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("读取响应失败: %v", err)
	}

	var result struct {
		Code    int `json:"code"`
		Data    struct {
			Locale  string `json:"locale"`
			Country string `json:"country"`
			Source  string `json:"source"`
		} `json:"data"`
		Message string `json:"message"`
	}

	if err := json.Unmarshal(body, &result); err != nil {
		t.Fatalf("解析响应 JSON 失败: %v", err)
	}

	if result.Code != 0 {
		t.Errorf("code 应为 0, 实际 %d", result.Code)
	}

	// locale 应为非空字符串
	if result.Data.Locale == "" {
		t.Error("locale 不应为空")
	}

	// source 应为已知来源之一
	validSources := map[string]bool{
		"ip-api": true, "ipinfo": true, "ip.sb": true, "cache": true, "default": true,
	}
	if !validSources[result.Data.Source] {
		t.Errorf("source %q 不在有效来源列表中", result.Data.Source)
	}

	t.Logf("detect-locale 响应: locale=%s, country=%s, source=%s",
		result.Data.Locale, result.Data.Country, result.Data.Source)
}
