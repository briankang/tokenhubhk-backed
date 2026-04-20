package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"tokenhub-server/internal/pkg/errcode"
	"tokenhub-server/internal/pkg/response"
)

// configCheck 单项校验结果
type configCheck struct {
	Field   string `json:"field"`
	Label   string `json:"label"`
	Status  string `json:"status"`  // "pass" | "warn" | "fail"
	Message string `json:"message"`
}

// configTestResult 整体测试结果
type configTestResult struct {
	Valid    bool          `json:"valid"`
	Checks   []configCheck `json:"checks"`
	Summary  string        `json:"summary"`
}

// TestProvider handles POST /admin/payment/providers/:type/test
// 接受请求体中的 config 字段（当前表单状态），或回退为 DB 中已保存的配置
func (h *PaymentConfigHandler) TestProvider(c *gin.Context) {
	providerType := strings.ToUpper(strings.TrimSpace(c.Param("type")))
	allowed := map[string]bool{"WECHAT": true, "ALIPAY": true, "STRIPE": true, "PAYPAL": true}
	if !allowed[providerType] {
		response.Error(c, http.StatusBadRequest, errcode.ErrValidation)
		return
	}

	// 从请求体读取当前表单配置（测试前无需先保存）
	var reqBody struct {
		Config map[string]interface{} `json:"config"`
	}
	_ = c.ShouldBindJSON(&reqBody)

	cfg := reqBody.Config
	if len(cfg) == 0 {
		// 未提供则从 DB 加载
		provider, err := h.svc.GetProvider(c.Request.Context(), providerType)
		if err == nil && provider != nil && provider.ConfigJSON != "" {
			if jsonErr := json.Unmarshal([]byte(provider.ConfigJSON), &cfg); jsonErr != nil {
				cfg = map[string]interface{}{}
			}
		}
	}
	if cfg == nil {
		cfg = map[string]interface{}{}
	}

	result := runProviderConfigTest(c.Request.Context(), providerType, cfg)
	response.Success(c, result)
}

// ==================== 各网关校验逻辑 ====================

func runProviderConfigTest(ctx context.Context, providerType string, cfg map[string]interface{}) configTestResult {
	var checks []configCheck

	switch providerType {
	case "WECHAT":
		checks = testWechatConfig(cfg)
	case "ALIPAY":
		checks = testAlipayConfig(cfg)
	case "STRIPE":
		checks = testStripeConfig(ctx, cfg)
	case "PAYPAL":
		checks = testPayPalConfig(cfg)
	default:
		return configTestResult{Valid: false, Summary: "不支持的支付网关类型"}
	}

	valid := true
	failCount := 0
	for _, ch := range checks {
		if ch.Status == "fail" {
			valid = false
			failCount++
		}
	}

	summary := fmt.Sprintf("全部 %d 项检查通过 ✓", len(checks))
	if !valid {
		summary = fmt.Sprintf("%d 项检查未通过，请修正后重试", failCount)
	}

	return configTestResult{Valid: valid, Checks: checks, Summary: summary}
}

func testWechatConfig(cfg map[string]interface{}) []configCheck {
	return []configCheck{
		checkPrefix(cfg, "app_id", "应用 ID (AppID)", "wx"),
		checkLen(cfg, "mch_id", "商户号 (MchID)", 8, 20),
		checkLen(cfg, "api_key", "API Key (v2)", 32, 32),
		checkLen(cfg, "api_v3_key", "API V3 Key", 32, 32),
		checkRequired(cfg, "cert_serial_no", "证书序列号"),
		checkPEM(cfg, "private_key", "商户私钥"),
		checkHTTPS(cfg, "notify_url", "异步通知地址"),
	}
}

func testAlipayConfig(cfg map[string]interface{}) []configCheck {
	checks := []configCheck{
		checkLen(cfg, "app_id", "应用 ID (AppID)", 14, 20),
		checkPEM(cfg, "private_key", "商户私钥"),
		checkPEM(cfg, "alipay_public_key", "支付宝公钥"),
	}

	signType := cfgStr(cfg, "sign_type")
	if signType == "" {
		checks = append(checks, configCheck{Field: "sign_type", Label: "签名类型", Status: "warn", Message: "未填写，建议明确设置为 RSA2"})
	} else if signType != "RSA2" {
		checks = append(checks, configCheck{Field: "sign_type", Label: "签名类型", Status: "fail", Message: "必须为 RSA2，当前值：" + signType})
	} else {
		checks = append(checks, configCheck{Field: "sign_type", Label: "签名类型", Status: "pass", Message: "RSA2 ✓"})
	}

	checks = append(checks, checkHTTPS(cfg, "notify_url", "异步通知地址"))
	return checks
}

func testStripeConfig(ctx context.Context, cfg map[string]interface{}) []configCheck {
	checks := []configCheck{
		checkPrefix(cfg, "publishable_key", "Publishable Key", "pk_"),
		checkPrefix(cfg, "secret_key", "Secret Key", "sk_"),
		checkPrefix(cfg, "webhook_secret", "Webhook 密钥", "whsec_"),
		checkRequired(cfg, "currency", "默认币种"),
	}

	// Stripe 真实 API 连通测试（GET /v1/balance）
	secretKey := cfgStr(cfg, "secret_key")
	if secretKey != "" {
		checks = append(checks, stripeConnectTest(ctx, secretKey))
	}

	return checks
}

func testPayPalConfig(cfg map[string]interface{}) []configCheck {
	checks := []configCheck{
		checkLen(cfg, "client_id", "Client ID", 40, 0),
		checkLen(cfg, "client_secret", "Client Secret", 40, 0),
		checkRequired(cfg, "webhook_id", "Webhook ID"),
	}

	mode := cfgStr(cfg, "mode")
	if mode != "sandbox" && mode != "live" {
		checks = append(checks, configCheck{
			Field: "mode", Label: "环境模式", Status: "fail",
			Message: fmt.Sprintf("必须为 sandbox 或 live，当前值：%q", mode),
		})
	} else {
		label := map[string]string{"sandbox": "沙箱模式", "live": "生产模式"}[mode]
		checks = append(checks, configCheck{Field: "mode", Label: "环境模式", Status: "pass", Message: label + " ✓"})
	}

	return checks
}

// ==================== Stripe API 连通测试 ====================

func stripeConnectTest(ctx context.Context, secretKey string) configCheck {
	reqCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, "https://api.stripe.com/v1/balance", nil)
	if err != nil {
		return configCheck{Field: "secret_key", Label: "Stripe API 连通", Status: "warn", Message: "无法创建请求：" + err.Error()}
	}
	req.Header.Set("Authorization", "Bearer "+secretKey)

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return configCheck{Field: "secret_key", Label: "Stripe API 连通", Status: "warn", Message: "网络请求失败：" + err.Error()}
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusOK:
		return configCheck{Field: "secret_key", Label: "Stripe API 连通", Status: "pass", Message: "API 连接成功，密钥有效 ✓"}
	case http.StatusUnauthorized:
		return configCheck{Field: "secret_key", Label: "Stripe API 连通", Status: "fail", Message: "密钥无效（401 Unauthorized）"}
	default:
		return configCheck{Field: "secret_key", Label: "Stripe API 连通", Status: "warn", Message: fmt.Sprintf("HTTP %d（非预期响应）", resp.StatusCode)}
	}
}

// ==================== 通用检查辅助函数 ====================

func cfgStr(cfg map[string]interface{}, key string) string {
	v, _ := cfg[key].(string)
	return strings.TrimSpace(v)
}

func checkRequired(cfg map[string]interface{}, key, label string) configCheck {
	if cfgStr(cfg, key) == "" {
		return configCheck{Field: key, Label: label, Status: "fail", Message: "必填字段不能为空"}
	}
	return configCheck{Field: key, Label: label, Status: "pass", Message: "已填写 ✓"}
}

func checkPrefix(cfg map[string]interface{}, key, label, prefix string) configCheck {
	v := cfgStr(cfg, key)
	if v == "" {
		return configCheck{Field: key, Label: label, Status: "fail", Message: "必填字段不能为空"}
	}
	if !strings.HasPrefix(v, prefix) {
		return configCheck{Field: key, Label: label, Status: "fail", Message: fmt.Sprintf("格式错误：应以 %s 开头", prefix)}
	}
	return configCheck{Field: key, Label: label, Status: "pass", Message: fmt.Sprintf("格式正确（%s...）✓", prefix)}
}

func checkLen(cfg map[string]interface{}, key, label string, minLen, maxLen int) configCheck {
	v := cfgStr(cfg, key)
	if v == "" {
		return configCheck{Field: key, Label: label, Status: "fail", Message: "必填字段不能为空"}
	}
	n := len(v)
	if n < minLen {
		return configCheck{Field: key, Label: label, Status: "fail", Message: fmt.Sprintf("长度不足（需 ≥%d 字节，当前 %d）", minLen, n)}
	}
	if maxLen > 0 && n > maxLen {
		return configCheck{Field: key, Label: label, Status: "fail", Message: fmt.Sprintf("长度超限（需 =%d 字节，当前 %d）", maxLen, n)}
	}
	if maxLen > 0 && n == maxLen {
		return configCheck{Field: key, Label: label, Status: "pass", Message: fmt.Sprintf("%d 字节 ✓", n)}
	}
	return configCheck{Field: key, Label: label, Status: "pass", Message: fmt.Sprintf("%d 字节 ✓", n)}
}

func checkPEM(cfg map[string]interface{}, key, label string) configCheck {
	v := cfgStr(cfg, key)
	if v == "" {
		return configCheck{Field: key, Label: label, Status: "fail", Message: "必填字段不能为空"}
	}
	if !strings.Contains(v, "-----BEGIN") {
		return configCheck{Field: key, Label: label, Status: "fail", Message: "格式错误：应为 PEM 格式（含 -----BEGIN 标头）"}
	}
	return configCheck{Field: key, Label: label, Status: "pass", Message: "PEM 格式正确 ✓"}
}

func checkHTTPS(cfg map[string]interface{}, key, label string) configCheck {
	v := cfgStr(cfg, key)
	if v == "" {
		return configCheck{Field: key, Label: label, Status: "warn", Message: "未配置（建议填写 HTTPS 回调地址）"}
	}
	if !strings.HasPrefix(v, "https://") {
		return configCheck{Field: key, Label: label, Status: "fail", Message: "必须使用 HTTPS 协议，当前值不符合要求"}
	}
	return configCheck{Field: key, Label: label, Status: "pass", Message: "HTTPS ✓"}
}
