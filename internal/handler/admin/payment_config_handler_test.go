package admin

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"tokenhub-server/internal/model"
	paymentsvc "tokenhub-server/internal/service/payment"
)

// ==================== 测试基础设施 ====================

func init() {
	gin.SetMode(gin.TestMode)
}

// setupHandlerTest 准备独立 Gin 引擎 + SQLite 内存 DB
func setupHandlerTest(t *testing.T) (*gin.Engine, *PaymentConfigHandler, *gorm.DB) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := db.AutoMigrate(
		&model.PaymentProviderConfig{},
		&model.PaymentMethod{},
		&model.BankAccount{},
	); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	svc := paymentsvc.NewPaymentConfigService(db)
	handler := NewPaymentConfigHandler(svc)

	r := gin.New()
	rg := r.Group("/admin")
	handler.Register(rg)

	// 公开接口（GetActivePaymentMethods）
	r.GET("/public/payment-methods", handler.GetActivePaymentMethods)

	return r, handler, db
}

// seedRouterProviders 写入4个网关种子记录
func seedRouterProviders(t *testing.T, db *gorm.DB) {
	t.Helper()
	for _, p := range []model.PaymentProviderConfig{
		{ProviderType: "WECHAT", DisplayName: "微信支付", IsActive: false, IsSandbox: true, SortOrder: 1},
		{ProviderType: "ALIPAY", DisplayName: "支付宝", IsActive: false, IsSandbox: true, SortOrder: 2},
		{ProviderType: "STRIPE", DisplayName: "Stripe", IsActive: false, IsSandbox: true, SortOrder: 3},
		{ProviderType: "PAYPAL", DisplayName: "PayPal", IsActive: false, IsSandbox: true, SortOrder: 4},
	} {
		if err := db.Create(&p).Error; err != nil {
			t.Fatalf("seed provider %s: %v", p.ProviderType, err)
		}
	}
}

// seedRouterMethods 写入5个付款方式种子记录
// PaymentMethod.IsActive 有 gorm:"default:true"，先全部创建，再 SQL 设 4 个网关为 false
func seedRouterMethods(t *testing.T, db *gorm.DB) {
	t.Helper()
	for _, m := range []model.PaymentMethod{
		{Type: "WECHAT", DisplayName: "微信支付", Icon: "wechat", SortOrder: 1},
		{Type: "ALIPAY", DisplayName: "支付宝", Icon: "alipay", SortOrder: 2},
		{Type: "STRIPE", DisplayName: "Stripe", Icon: "stripe", SortOrder: 3},
		{Type: "PAYPAL", DisplayName: "PayPal", Icon: "paypal", SortOrder: 4},
		{Type: "BANK_TRANSFER", DisplayName: "对公转账", Icon: "bank", SortOrder: 5},
	} {
		if err := db.Create(&m).Error; err != nil {
			t.Fatalf("seed method %s: %v", m.Type, err)
		}
	}
	if err := db.Exec("UPDATE payment_methods SET is_active = 0 WHERE type != ?", "BANK_TRANSFER").Error; err != nil {
		t.Fatalf("set methods inactive: %v", err)
	}
}

// doRequest 发送 HTTP 测试请求并解析响应
func doTestRequest(t *testing.T, r *gin.Engine, method, path string, body interface{}) (int, map[string]interface{}) {
	t.Helper()
	var buf *bytes.Buffer
	if body != nil {
		b, _ := json.Marshal(body)
		buf = bytes.NewBuffer(b)
	} else {
		buf = bytes.NewBuffer(nil)
	}
	req := httptest.NewRequest(method, path, buf)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	var result map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &result)
	return w.Code, result
}

// ==================== GET /admin/payment/providers ====================

func TestHandler_GetAllProviders_Empty(t *testing.T) {
	r, _, _ := setupHandlerTest(t)
	code, resp := doTestRequest(t, r, http.MethodGet, "/admin/payment/providers", nil)
	if code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %v", code, resp)
	}
}

func TestHandler_GetAllProviders_Seeded(t *testing.T) {
	r, _, db := setupHandlerTest(t)
	seedRouterProviders(t, db)

	code, resp := doTestRequest(t, r, http.MethodGet, "/admin/payment/providers", nil)
	if code != http.StatusOK {
		t.Fatalf("expected 200, got %d", code)
	}

	data, ok := resp["data"].([]interface{})
	if !ok || len(data) != 4 {
		t.Errorf("expected 4 providers, got: %v", resp["data"])
	}
}

// ==================== PUT /admin/payment/providers/:type ====================

func TestHandler_UpdateProvider_Wechat_MockConfig(t *testing.T) {
	r, _, db := setupHandlerTest(t)
	seedRouterProviders(t, db)

	body := map[string]interface{}{
		"config_json": `{"app_id":"wx1234567890abcdef","mch_id":"1900000109","api_key":"MOCK_API_KEY_v2_32bytes","api_v3_key":"MOCK_APIv3_KEY_32bytes012","cert_serial_no":"5A3F6B1C","private_key":"-----BEGIN PRIVATE KEY-----\nMOCK\n-----END PRIVATE KEY-----","notify_url":"https://api.tokenhubhk.com/api/v1/callback/wechat"}`,
		"is_sandbox":  true,
	}
	code, resp := doTestRequest(t, r, http.MethodPut, "/admin/payment/providers/WECHAT", body)
	if code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %v", code, resp)
	}

	// 读回验证密文存储后能解密
	code2, resp2 := doTestRequest(t, r, http.MethodGet, "/admin/payment/providers", nil)
	if code2 != http.StatusOK {
		t.Fatalf("get after update: %d", code2)
	}
	data := resp2["data"].([]interface{})
	for _, item := range data {
		p := item.(map[string]interface{})
		if p["provider_type"] == "WECHAT" {
			cj, _ := p["config_json"].(string)
			var decoded map[string]interface{}
			if err := json.Unmarshal([]byte(cj), &decoded); err != nil {
				t.Errorf("config_json should be valid JSON (decrypted): %v", err)
			}
			if decoded["app_id"] != "wx1234567890abcdef" {
				t.Errorf("app_id mismatch: %v", decoded["app_id"])
			}
			return
		}
	}
	t.Error("WECHAT provider not found in response")
}

func TestHandler_UpdateProvider_Alipay_MockConfig(t *testing.T) {
	r, _, db := setupHandlerTest(t)
	seedRouterProviders(t, db)

	body := map[string]interface{}{
		"config_json": `{"app_id":"2021000000000000","private_key":"-----BEGIN RSA PRIVATE KEY-----\nMOCK\n-----END RSA PRIVATE KEY-----","alipay_public_key":"-----BEGIN PUBLIC KEY-----\nMOCK\n-----END PUBLIC KEY-----","sign_type":"RSA2","notify_url":"https://api.tokenhubhk.com/api/v1/callback/alipay","return_url":"https://tokenhubhk.com/balance?status=success"}`,
	}
	code, _ := doTestRequest(t, r, http.MethodPut, "/admin/payment/providers/ALIPAY", body)
	if code != http.StatusOK {
		t.Fatalf("expected 200, got %d", code)
	}
}

func TestHandler_UpdateProvider_Stripe_MockConfig(t *testing.T) {
	r, _, db := setupHandlerTest(t)
	seedRouterProviders(t, db)

	body := map[string]interface{}{
		"config_json": `{"publishable_key":"pk_test_51MockStripe","secret_key":"sk_test_51MockStripe","webhook_secret":"whsec_mock_test","currency":"usd","success_url":"https://tokenhubhk.com/balance?status=success","cancel_url":"https://tokenhubhk.com/checkout?status=cancel"}`,
		"is_sandbox":  true,
	}
	code, _ := doTestRequest(t, r, http.MethodPut, "/admin/payment/providers/STRIPE", body)
	if code != http.StatusOK {
		t.Fatalf("expected 200, got %d", code)
	}
}

func TestHandler_UpdateProvider_PayPal_MockConfig(t *testing.T) {
	r, _, db := setupHandlerTest(t)
	seedRouterProviders(t, db)

	body := map[string]interface{}{
		"config_json": `{"client_id":"AblxxxxMockPayPalID","client_secret":"EmxxxxMockPayPalSecret","webhook_id":"3XY44955XF432341J","mode":"sandbox","return_url":"https://tokenhubhk.com/balance?status=success","cancel_url":"https://tokenhubhk.com/checkout?status=cancel"}`,
		"is_sandbox":  true,
	}
	code, _ := doTestRequest(t, r, http.MethodPut, "/admin/payment/providers/PAYPAL", body)
	if code != http.StatusOK {
		t.Fatalf("expected 200, got %d", code)
	}
}

func TestHandler_UpdateProvider_MissingType_400(t *testing.T) {
	// 路由无法匹配到空 type，应返回 404
	r, _, _ := setupHandlerTest(t)
	code, _ := doTestRequest(t, r, http.MethodPut, "/admin/payment/providers/", nil)
	if code == http.StatusOK {
		t.Error("should not return 200 for empty provider type")
	}
}

func TestHandler_UpdateProvider_InvalidJSON_400(t *testing.T) {
	r, _, db := setupHandlerTest(t)
	seedRouterProviders(t, db)

	req := httptest.NewRequest(http.MethodPut, "/admin/payment/providers/WECHAT", bytes.NewBufferString("not-json"))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code == http.StatusOK {
		t.Error("invalid JSON body should return non-200")
	}
}

// ==================== PATCH /admin/payment/providers/:type/toggle ====================

func TestHandler_ToggleProvider_Wechat(t *testing.T) {
	r, _, db := setupHandlerTest(t)
	seedRouterProviders(t, db)

	// 初始 is_active=false → toggle → true
	code, resp := doTestRequest(t, r, http.MethodPatch, "/admin/payment/providers/WECHAT/toggle", nil)
	if code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %v", code, resp)
	}
	data := resp["data"].(map[string]interface{})
	if data["is_active"] != true {
		t.Errorf("is_active should be true after toggle, got: %v", data["is_active"])
	}
}

func TestHandler_ToggleProvider_AllFour(t *testing.T) {
	r, _, db := setupHandlerTest(t)
	seedRouterProviders(t, db)

	gateways := []string{"WECHAT", "ALIPAY", "STRIPE", "PAYPAL"}
	for _, gw := range gateways {
		code, resp := doTestRequest(t, r, http.MethodPatch, "/admin/payment/providers/"+gw+"/toggle", nil)
		if code != http.StatusOK {
			t.Errorf("%s toggle: expected 200, got %d: %v", gw, code, resp)
		}
	}
}

func TestHandler_ToggleProvider_IdempotentDouble(t *testing.T) {
	r, _, db := setupHandlerTest(t)
	seedRouterProviders(t, db)

	// 第一次 toggle: false → true
	doTestRequest(t, r, http.MethodPatch, "/admin/payment/providers/STRIPE/toggle", nil)
	// 第二次 toggle: true → false
	code, resp := doTestRequest(t, r, http.MethodPatch, "/admin/payment/providers/STRIPE/toggle", nil)
	if code != http.StatusOK {
		t.Fatalf("expected 200, got %d", code)
	}
	data := resp["data"].(map[string]interface{})
	if data["is_active"] != false {
		t.Errorf("is_active should be false after second toggle, got: %v", data["is_active"])
	}
}

// ==================== /admin/payment/bank-accounts CRUD ====================

func TestHandler_BankAccount_Create(t *testing.T) {
	r, _, _ := setupHandlerTest(t)

	body := map[string]interface{}{
		"account_name":   "TokenHub Technology Ltd",
		"bank_name":      "Bank of China",
		"branch_name":    "Central Branch",
		"account_number": "6228480000000001",
		"swift_code":     "BKCHCNBJ",
		"currency":       "CNY",
		"remark":         "Test",
		"is_active":      true,
	}
	code, resp := doTestRequest(t, r, http.MethodPost, "/admin/payment/bank-accounts", body)
	if code != http.StatusOK {
		t.Fatalf("create: expected 200, got %d: %v", code, resp)
	}
	data := resp["data"].(map[string]interface{})
	if data["id"] == nil {
		t.Error("id should be set after create")
	}
}

func TestHandler_BankAccount_Create_MissingRequired_400(t *testing.T) {
	r, _, _ := setupHandlerTest(t)

	// 缺少 account_number
	body := map[string]interface{}{
		"account_name": "TokenHub Ltd",
		"bank_name":    "ICBC",
	}
	code, _ := doTestRequest(t, r, http.MethodPost, "/admin/payment/bank-accounts", body)
	if code == http.StatusOK {
		t.Error("missing account_number should return error")
	}
}

func TestHandler_BankAccount_FullCRUD(t *testing.T) {
	r, _, _ := setupHandlerTest(t)

	// Create
	body := map[string]interface{}{
		"account_name":   "TokenHub Ltd",
		"bank_name":      "ICBC",
		"account_number": "0000000001",
		"is_active":      true,
	}
	_, createResp := doTestRequest(t, r, http.MethodPost, "/admin/payment/bank-accounts", body)
	data := createResp["data"].(map[string]interface{})
	id := int(data["id"].(float64))

	// List
	code, listResp := doTestRequest(t, r, http.MethodGet, "/admin/payment/bank-accounts", nil)
	if code != http.StatusOK {
		t.Fatalf("list: expected 200, got %d", code)
	}
	accounts := listResp["data"].([]interface{})
	if len(accounts) != 1 {
		t.Errorf("expected 1 account, got %d", len(accounts))
	}

	// Update
	code, _ = doTestRequest(t, r, http.MethodPut,
		"/admin/payment/bank-accounts/"+jsonIntToStr(id), map[string]interface{}{"remark": "Updated"})
	if code != http.StatusOK {
		t.Fatalf("update: expected 200, got %d", code)
	}

	// Delete
	code, _ = doTestRequest(t, r, http.MethodDelete,
		"/admin/payment/bank-accounts/"+jsonIntToStr(id), nil)
	if code != http.StatusOK {
		t.Fatalf("delete: expected 200, got %d", code)
	}

	// Verify deleted
	_, afterDelete := doTestRequest(t, r, http.MethodGet, "/admin/payment/bank-accounts", nil)
	remaining := afterDelete["data"].([]interface{})
	if len(remaining) != 0 {
		t.Errorf("expected 0 after delete, got %d", len(remaining))
	}
}

func TestHandler_BankAccount_Delete_InvalidID(t *testing.T) {
	r, _, _ := setupHandlerTest(t)
	code, _ := doTestRequest(t, r, http.MethodDelete, "/admin/payment/bank-accounts/abc", nil)
	if code == http.StatusOK {
		t.Error("invalid id should return error")
	}
}

// ==================== /admin/payment/methods ====================

func TestHandler_GetAllPaymentMethods_Seeded(t *testing.T) {
	r, _, db := setupHandlerTest(t)
	seedRouterMethods(t, db)

	code, resp := doTestRequest(t, r, http.MethodGet, "/admin/payment/methods", nil)
	if code != http.StatusOK {
		t.Fatalf("expected 200, got %d", code)
	}
	data := resp["data"].([]interface{})
	if len(data) != 5 {
		t.Errorf("expected 5 methods, got %d", len(data))
	}
}

func TestHandler_TogglePaymentMethod_Alipay(t *testing.T) {
	r, _, db := setupHandlerTest(t)
	seedRouterMethods(t, db) // ALIPAY 经 SQL 设为 is_active=false

	// toggle: false → true
	code, resp := doTestRequest(t, r, http.MethodPatch, "/admin/payment/methods/ALIPAY/toggle", nil)
	if code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %v", code, resp)
	}
	data := resp["data"].(map[string]interface{})
	if data["is_active"] != true {
		t.Errorf("ALIPAY should be active after toggle from false, got: %v", data["is_active"])
	}
}

func TestHandler_UpdatePaymentMethod_Stripe(t *testing.T) {
	r, _, db := setupHandlerTest(t)
	seedRouterMethods(t, db)

	code, _ := doTestRequest(t, r, http.MethodPut, "/admin/payment/methods/STRIPE", map[string]interface{}{
		"display_name": "Stripe 国际信用卡",
		"description":  "支持 Visa/Mastercard/AmEx",
	})
	if code != http.StatusOK {
		t.Fatalf("expected 200, got %d", code)
	}
}

// ==================== GET /public/payment-methods ====================

func TestHandler_PublicPaymentMethods_OnlyActive(t *testing.T) {
	r, _, db := setupHandlerTest(t)
	seedRouterMethods(t, db) // BANK_TRANSFER is_active=true，其余false

	code, resp := doTestRequest(t, r, http.MethodGet, "/public/payment-methods", nil)
	if code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %v", code, resp)
	}
	data := resp["data"].([]interface{})
	if len(data) != 1 {
		t.Errorf("expected 1 active method (BANK_TRANSFER), got %d", len(data))
	}
	first := data[0].(map[string]interface{})
	if first["type"] != "BANK_TRANSFER" {
		t.Errorf("expected BANK_TRANSFER, got %s", first["type"])
	}
}

func TestHandler_PublicPaymentMethods_AfterEnablingAll(t *testing.T) {
	r, _, db := setupHandlerTest(t)
	seedRouterMethods(t, db)

	// 启用所有网关
	for _, gw := range []string{"WECHAT", "ALIPAY", "STRIPE", "PAYPAL"} {
		doTestRequest(t, r, http.MethodPatch, "/admin/payment/methods/"+gw+"/toggle", nil)
	}

	code, resp := doTestRequest(t, r, http.MethodGet, "/public/payment-methods", nil)
	if code != http.StatusOK {
		t.Fatalf("expected 200, got %d", code)
	}
	data := resp["data"].([]interface{})
	if len(data) != 5 {
		t.Errorf("expected 5 active methods, got %d", len(data))
	}
}

// ==================== 路由路径正确性测试 ====================

func TestRoutes_Registered_Paths(t *testing.T) {
	r, _, _ := setupHandlerTest(t)

	// 验证所有管理路由正确注册（未种子时 404/200 均正常，关键是路径不 404 for bad method）
	routes := []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/admin/payment/providers"},
		{http.MethodGet, "/admin/payment/bank-accounts"},
		{http.MethodGet, "/admin/payment/methods"},
		{http.MethodGet, "/public/payment-methods"},
	}

	for _, route := range routes {
		req := httptest.NewRequest(route.method, route.path, nil)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		// 这些路径应被路由注册（非 404）
		if w.Code == http.StatusNotFound {
			t.Errorf("route %s %s should be registered (got 404)", route.method, route.path)
		}
	}
}

func TestRoutes_WrongMethod_405(t *testing.T) {
	r, _, _ := setupHandlerTest(t)
	r.HandleMethodNotAllowed = true

	// GET 路由用 POST 请求应 405
	req := httptest.NewRequest(http.MethodPost, "/admin/payment/providers", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected 405 for wrong method, got %d", w.Code)
	}
}

// ==================== 完整端到端配置流程（Mock 数据） ====================

// TestFullConfigFlow_Wechat 模拟管理员配置微信支付的完整操作流程
func TestFullConfigFlow_Wechat(t *testing.T) {
	r, _, db := setupHandlerTest(t)
	seedRouterProviders(t, db)
	seedRouterMethods(t, db)

	// Step 1: 查询初始状态（is_active=false）
	_, initResp := doTestRequest(t, r, http.MethodGet, "/admin/payment/providers", nil)
	providers := initResp["data"].([]interface{})
	wechat := findProviderByType(providers, "WECHAT")
	if wechat == nil {
		t.Fatal("WECHAT not found in initial list")
	}
	if wechat["is_active"] != false {
		t.Errorf("initial is_active should be false, got %v", wechat["is_active"])
	}

	// Step 2: 填入完整 mock 配置
	config := map[string]interface{}{
		"config_json": `{"app_id":"wx1234567890abcdef","mch_id":"1900000109","api_key":"MOCK_API_KEY_32bytes_here_please","api_v3_key":"MOCK_V3_KEY_32bytes_placeholder!","cert_serial_no":"5A3F6B","private_key":"-----BEGIN PRIVATE KEY-----\nMOCK\n-----END PRIVATE KEY-----","notify_url":"https://api.tokenhubhk.com/api/v1/callback/wechat"}`,
		"is_sandbox":  true,
	}
	code, _ := doTestRequest(t, r, http.MethodPut, "/admin/payment/providers/WECHAT", config)
	if code != http.StatusOK {
		t.Fatalf("step2 update: expected 200, got %d", code)
	}

	// Step 3: 启用渠道
	code, toggleResp := doTestRequest(t, r, http.MethodPatch, "/admin/payment/providers/WECHAT/toggle", nil)
	if code != http.StatusOK {
		t.Fatalf("step3 toggle: expected 200, got %d", code)
	}
	if toggleResp["data"].(map[string]interface{})["is_active"] != true {
		t.Error("step3: is_active should be true after toggle")
	}

	// Step 4: 启用付款方式
	code, _ = doTestRequest(t, r, http.MethodPatch, "/admin/payment/methods/WECHAT/toggle", nil)
	if code != http.StatusOK {
		t.Fatalf("step4 toggle method: expected 200, got %d", code)
	}

	// Step 5: 公开接口确认微信出现在可用列表
	code, pubResp := doTestRequest(t, r, http.MethodGet, "/public/payment-methods", nil)
	if code != http.StatusOK {
		t.Fatalf("step5 public: expected 200, got %d", code)
	}
	pubMethods := pubResp["data"].([]interface{})
	found := false
	for _, m := range pubMethods {
		if m.(map[string]interface{})["type"] == "WECHAT" {
			found = true
		}
	}
	if !found {
		t.Error("step5: WECHAT should appear in public payment methods after enabling")
	}
}

// TestFullConfigFlow_Stripe_MultiAccount 模拟 Stripe 多账号配置流程
func TestFullConfigFlow_Stripe_MultiAccount(t *testing.T) {
	r, _, db := setupHandlerTest(t)
	seedRouterProviders(t, db)

	// 配置 Stripe 主账号
	body := map[string]interface{}{
		"config_json": `{"publishable_key":"pk_test_51MockUSMain","secret_key":"sk_test_51MockUSMain","webhook_secret":"whsec_us_main","currency":"usd","success_url":"https://tokenhubhk.com/balance?status=success","cancel_url":"https://tokenhubhk.com/checkout?status=cancel"}`,
		"is_sandbox":  true,
	}
	code, _ := doTestRequest(t, r, http.MethodPut, "/admin/payment/providers/STRIPE", body)
	if code != http.StatusOK {
		t.Fatalf("stripe config: expected 200, got %d", code)
	}

	// 启用 Stripe
	code, _ = doTestRequest(t, r, http.MethodPatch, "/admin/payment/providers/STRIPE/toggle", nil)
	if code != http.StatusOK {
		t.Fatalf("stripe toggle: expected 200, got %d", code)
	}

	// 验证配置已存储且可解密
	_, getResp := doTestRequest(t, r, http.MethodGet, "/admin/payment/providers", nil)
	providers := getResp["data"].([]interface{})
	stripe := findProviderByType(providers, "STRIPE")
	if stripe == nil {
		t.Fatal("STRIPE not found")
	}
	if stripe["is_active"] != true {
		t.Error("STRIPE should be active")
	}
	cj, _ := stripe["config_json"].(string)
	var decoded map[string]interface{}
	if err := json.Unmarshal([]byte(cj), &decoded); err != nil {
		t.Errorf("config_json should be valid JSON: %v", err)
	}
	if decoded["currency"] != "usd" {
		t.Errorf("currency mismatch: %v", decoded["currency"])
	}
}

// ==================== 辅助函数 ====================

func findProviderByType(providers []interface{}, providerType string) map[string]interface{} {
	for _, item := range providers {
		p := item.(map[string]interface{})
		if p["provider_type"] == providerType {
			return p
		}
	}
	return nil
}

func jsonIntToStr(id int) string {
	if id == 0 {
		return "0"
	}
	result := ""
	for id > 0 {
		result = string(rune('0'+id%10)) + result
		id /= 10
	}
	return result
}
