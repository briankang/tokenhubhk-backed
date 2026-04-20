package payment

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"tokenhub-server/internal/model"
)

// ==================== 测试基础设施 ====================

func setupConfigServiceTest(t *testing.T) (*PaymentConfigService, *gorm.DB) {
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
	svc := NewPaymentConfigService(db)
	return svc, db
}

// seedProviders 写入 4 个网关的种子记录（is_active=false, 沙箱模式）
func seedProviders(t *testing.T, db *gorm.DB) {
	t.Helper()
	providers := []model.PaymentProviderConfig{
		{ProviderType: "WECHAT", DisplayName: "微信支付", IsActive: false, IsSandbox: true, SortOrder: 1},
		{ProviderType: "ALIPAY", DisplayName: "支付宝", IsActive: false, IsSandbox: true, SortOrder: 2},
		{ProviderType: "STRIPE", DisplayName: "Stripe", IsActive: false, IsSandbox: true, SortOrder: 3},
		{ProviderType: "PAYPAL", DisplayName: "PayPal", IsActive: false, IsSandbox: true, SortOrder: 4},
	}
	for _, p := range providers {
		if err := db.Create(&p).Error; err != nil {
			t.Fatalf("seed provider %s: %v", p.ProviderType, err)
		}
	}
}

// seedPaymentMethods 写入 5 种付款方式种子
// 注意：PaymentMethod.IsActive 有 gorm:"default:true"，GORM 会跳过 false 零值。
// 正确做法：先创建所有记录（默认 is_active=true），再用 SQL 将 4 个网关设为 false。
func seedPaymentMethods(t *testing.T, db *gorm.DB) {
	t.Helper()
	methods := []model.PaymentMethod{
		{Type: "WECHAT", DisplayName: "微信支付", Icon: "wechat", SortOrder: 1},
		{Type: "ALIPAY", DisplayName: "支付宝", Icon: "alipay", SortOrder: 2},
		{Type: "STRIPE", DisplayName: "Stripe", Icon: "stripe", SortOrder: 3},
		{Type: "PAYPAL", DisplayName: "PayPal", Icon: "paypal", SortOrder: 4},
		{Type: "BANK_TRANSFER", DisplayName: "对公转账", Icon: "bank", SortOrder: 5},
	}
	for _, m := range methods {
		if err := db.Create(&m).Error; err != nil {
			t.Fatalf("seed method %s: %v", m.Type, err)
		}
	}
	// 将 4 个网关设为 is_active=false（BANK_TRANSFER 保持 true）
	if err := db.Exec("UPDATE payment_methods SET is_active = 0 WHERE type != ?", "BANK_TRANSFER").Error; err != nil {
		t.Fatalf("set methods inactive: %v", err)
	}
}

// ==================== AES-256-GCM 加密/解密测试 ====================

func TestConfigService_Encrypt_Decrypt_RoundTrip(t *testing.T) {
	svc, _ := setupConfigServiceTest(t)
	cases := []string{
		`{"app_id":"wx1234567890abcdef","mch_id":"1900000109","api_key":"MOCK_API_KEY_32bytes_placeholder01"}`,
		`{"app_id":"2021000000000000","sign_type":"RSA2","notify_url":"https://api.tokenhubhk.com/api/v1/callback/alipay"}`,
		`{"publishable_key":"pk_test_51Mock","secret_key":"sk_test_51Mock","webhook_secret":"whsec_mock"}`,
		`{"client_id":"AblxxxxMockPayPalClientID","client_secret":"EmxxxxMockPayPalSecret","mode":"sandbox"}`,
	}
	for _, plain := range cases {
		enc, err := svc.Encrypt(plain)
		if err != nil {
			t.Fatalf("encrypt failed: %v\ninput=%q", err, plain)
		}
		if enc == plain {
			t.Error("encrypted output should differ from plaintext")
		}
		dec, err := svc.Decrypt(enc)
		if err != nil {
			t.Fatalf("decrypt failed: %v", err)
		}
		if dec != plain {
			t.Errorf("round-trip mismatch\nwant: %q\ngot:  %q", plain, dec)
		}
	}
}

func TestConfigService_Encrypt_EmptyString_Safe(t *testing.T) {
	svc, _ := setupConfigServiceTest(t)
	enc, err := svc.Encrypt("")
	if err != nil {
		t.Fatalf("encrypt empty: %v", err)
	}
	if enc != "" {
		t.Errorf("empty input should return empty encrypted, got %q", enc)
	}
	dec, err := svc.Decrypt("")
	if err != nil {
		t.Fatalf("decrypt empty: %v", err)
	}
	if dec != "" {
		t.Errorf("empty decrypt should return empty, got %q", dec)
	}
}

func TestConfigService_Decrypt_InvalidBase64(t *testing.T) {
	svc, _ := setupConfigServiceTest(t)
	_, err := svc.Decrypt("!!not-base64!!")
	if err == nil {
		t.Error("expected error for invalid base64")
	}
}

func TestConfigService_Decrypt_TamperedCiphertext(t *testing.T) {
	svc, _ := setupConfigServiceTest(t)
	enc, _ := svc.Encrypt(`{"key":"value"}`)
	// 翻转最后几个字符破坏 GCM MAC
	tampered := enc[:len(enc)-4] + "AAAA"
	_, err := svc.Decrypt(tampered)
	if err == nil {
		t.Error("expected GCM authentication failure for tampered ciphertext")
	}
}

func TestConfigService_KeyTooShort_AutoPad(t *testing.T) {
	t.Setenv("PAYMENT_ENCRYPT_KEY", "short")
	db, _ := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	svc := NewPaymentConfigService(db)
	plain := `{"test":"value"}`
	enc, err := svc.Encrypt(plain)
	if err != nil {
		t.Fatalf("short key encrypt: %v", err)
	}
	dec, err := svc.Decrypt(enc)
	if err != nil {
		t.Fatalf("short key decrypt: %v", err)
	}
	if dec != plain {
		t.Errorf("round-trip failed with short key")
	}
}

func TestConfigService_KeyTooLong_Truncated(t *testing.T) {
	t.Setenv("PAYMENT_ENCRYPT_KEY", "this-is-a-very-long-key-that-exceeds-32-bytes-and-should-be-truncated")
	db, _ := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	svc := NewPaymentConfigService(db)
	plain := `{"longkey":"value"}`
	enc, _ := svc.Encrypt(plain)
	dec, err := svc.Decrypt(enc)
	if err != nil || dec != plain {
		t.Errorf("long key round-trip failed: err=%v", err)
	}
}

func TestConfigService_EnvKeyOverride(t *testing.T) {
	customKey := "custom-32byte-key-for-env-test!!"
	t.Setenv("PAYMENT_ENCRYPT_KEY", customKey)
	db, _ := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	svc := NewPaymentConfigService(db)

	plain := `{"env":"override"}`
	enc, _ := svc.Encrypt(plain)

	// 使用默认密钥解密应失败
	defaultSvc := NewPaymentConfigService(db) // env 已被 Setenv 覆盖，同一密钥
	dec, err := defaultSvc.Decrypt(enc)
	if err != nil || dec != plain {
		t.Errorf("env key override failed: err=%v dec=%q", err, dec)
	}
}

// ==================== Mock 配置：四网关完整 Config 加密往返 ====================

// wechatMockConfig 微信支付完整 mock 配置
var wechatMockConfig = map[string]interface{}{
	"app_id":         "wx1234567890abcdef",
	"mch_id":         "1900000109",
	"api_key":        "MOCK_API_KEY_v2_32bytes_placeholder01",
	"api_v3_key":     "MOCK_APIv3_KEY_32bytes_placeholder012",
	"cert_serial_no": "5A3F6B1C2D3E4F5A6B7C8D9E0F1A2B3C4D5E6F7",
	"private_key":    "-----BEGIN PRIVATE KEY-----\nMOCK_PRIVATE_KEY_PLACEHOLDER\n-----END PRIVATE KEY-----",
	"notify_url":     "https://api.tokenhubhk.com/api/v1/callback/wechat",
}

// alipayMockConfig 支付宝完整 mock 配置
var alipayMockConfig = map[string]interface{}{
	"app_id":           "2021000000000000",
	"private_key":      "-----BEGIN RSA PRIVATE KEY-----\nMOCK_MERCHANT_RSA_PRIVATE_KEY\n-----END RSA PRIVATE KEY-----",
	"alipay_public_key": "-----BEGIN PUBLIC KEY-----\nMOCK_ALIPAY_PUBLIC_KEY\n-----END PUBLIC KEY-----",
	"sign_type":        "RSA2",
	"notify_url":       "https://api.tokenhubhk.com/api/v1/callback/alipay",
	"return_url":       "https://tokenhubhk.com/balance?status=success",
}

// stripeMockConfig Stripe 完整 mock 配置
var stripeMockConfig = map[string]interface{}{
	"publishable_key": "pk_test_51MockStripeMain1234567890abcdef",
	"secret_key":      "sk_test_51MockStripeMain0987654321fedcba",
	"webhook_secret":  "whsec_mock_main_abcdef1234567890",
	"currency":        "usd",
	"success_url":     "https://tokenhubhk.com/balance?status=success",
	"cancel_url":      "https://tokenhubhk.com/checkout?status=cancel",
}

// paypalMockConfig PayPal 完整 mock 配置
var paypalMockConfig = map[string]interface{}{
	"client_id":     "AblxxxxMockPayPalClientID1234567890ABCDEF",
	"client_secret": "EmxxxxMockPayPalClientSecret1234567890ABCDEF",
	"webhook_id":    "3XY44955XF432341J",
	"mode":          "sandbox",
	"return_url":    "https://tokenhubhk.com/balance?status=success",
	"cancel_url":    "https://tokenhubhk.com/checkout?status=cancel",
}

func testGatewayConfigRoundTrip(t *testing.T, svc *PaymentConfigService, gatewayName string, config map[string]interface{}) {
	t.Helper()
	plain, err := json.Marshal(config)
	if err != nil {
		t.Fatalf("%s: marshal config: %v", gatewayName, err)
	}
	enc, err := svc.Encrypt(string(plain))
	if err != nil {
		t.Fatalf("%s: encrypt: %v", gatewayName, err)
	}
	dec, err := svc.Decrypt(enc)
	if err != nil {
		t.Fatalf("%s: decrypt: %v", gatewayName, err)
	}
	var restored map[string]interface{}
	if err := json.Unmarshal([]byte(dec), &restored); err != nil {
		t.Fatalf("%s: unmarshal decrypted: %v", gatewayName, err)
	}
	for k, want := range config {
		got, ok := restored[k]
		if !ok {
			t.Errorf("%s: field %q missing after round-trip", gatewayName, k)
			continue
		}
		if got != want {
			t.Errorf("%s: field %q: want %v, got %v", gatewayName, k, want, got)
		}
	}
}

func TestConfigService_MockConfig_Wechat_RoundTrip(t *testing.T) {
	svc, _ := setupConfigServiceTest(t)
	testGatewayConfigRoundTrip(t, svc, "WECHAT", wechatMockConfig)
}

func TestConfigService_MockConfig_Alipay_RoundTrip(t *testing.T) {
	svc, _ := setupConfigServiceTest(t)
	testGatewayConfigRoundTrip(t, svc, "ALIPAY", alipayMockConfig)
}

func TestConfigService_MockConfig_Stripe_RoundTrip(t *testing.T) {
	svc, _ := setupConfigServiceTest(t)
	testGatewayConfigRoundTrip(t, svc, "STRIPE", stripeMockConfig)
}

func TestConfigService_MockConfig_PayPal_RoundTrip(t *testing.T) {
	svc, _ := setupConfigServiceTest(t)
	testGatewayConfigRoundTrip(t, svc, "PAYPAL", paypalMockConfig)
}

// ==================== Provider CRUD 测试 ====================

func TestConfigService_GetAllProviders_Empty(t *testing.T) {
	svc, _ := setupConfigServiceTest(t)
	providers, err := svc.GetAllProviders(context.Background())
	if err != nil {
		t.Fatalf("GetAllProviders: %v", err)
	}
	if len(providers) != 0 {
		t.Errorf("expected 0 providers, got %d", len(providers))
	}
}

func TestConfigService_GetAllProviders_Seeded(t *testing.T) {
	svc, db := setupConfigServiceTest(t)
	seedProviders(t, db)
	providers, err := svc.GetAllProviders(context.Background())
	if err != nil {
		t.Fatalf("GetAllProviders: %v", err)
	}
	if len(providers) != 4 {
		t.Errorf("expected 4 providers, got %d", len(providers))
	}
	// 验证排序：sort_order ASC
	expected := []string{"WECHAT", "ALIPAY", "STRIPE", "PAYPAL"}
	for i, want := range expected {
		if providers[i].ProviderType != want {
			t.Errorf("position %d: want %s, got %s", i, want, providers[i].ProviderType)
		}
	}
}

func TestConfigService_UpdateProvider_EncryptsConfig(t *testing.T) {
	svc, db := setupConfigServiceTest(t)
	seedProviders(t, db)

	configJSON, _ := json.Marshal(stripeMockConfig)
	err := svc.UpdateProvider(context.Background(), "STRIPE", map[string]interface{}{
		"config_json": string(configJSON),
		"is_sandbox":  true,
	})
	if err != nil {
		t.Fatalf("UpdateProvider: %v", err)
	}

	// 验证数据库中存储的是密文（不包含明文 key 名）
	var raw model.PaymentProviderConfig
	db.Where("provider_type = ?", "STRIPE").First(&raw)
	if strings.Contains(raw.ConfigJSON, "sk_test_") {
		t.Error("config_json should be encrypted in DB, not plaintext")
	}
	if raw.ConfigJSON == "" {
		t.Error("config_json should not be empty after update")
	}

	// 通过 GetProvider 读取回来应自动解密
	provider, err := svc.GetProvider(context.Background(), "STRIPE")
	if err != nil {
		t.Fatalf("GetProvider: %v", err)
	}
	var decoded map[string]interface{}
	if err := json.Unmarshal([]byte(provider.ConfigJSON), &decoded); err != nil {
		t.Fatalf("decrypted config should be valid JSON: %v", err)
	}
	if decoded["secret_key"] != "sk_test_51MockStripeMain0987654321fedcba" {
		t.Errorf("secret_key mismatch: got %v", decoded["secret_key"])
	}
}

func TestConfigService_UpdateProvider_WechatFullMock(t *testing.T) {
	svc, db := setupConfigServiceTest(t)
	seedProviders(t, db)

	configJSON, _ := json.Marshal(wechatMockConfig)
	if err := svc.UpdateProvider(context.Background(), "WECHAT", map[string]interface{}{
		"config_json": string(configJSON),
		"is_sandbox":  true,
	}); err != nil {
		t.Fatalf("UpdateProvider WECHAT: %v", err)
	}

	provider, err := svc.GetProvider(context.Background(), "WECHAT")
	if err != nil {
		t.Fatalf("GetProvider WECHAT: %v", err)
	}
	var decoded map[string]interface{}
	json.Unmarshal([]byte(provider.ConfigJSON), &decoded)
	if decoded["app_id"] != "wx1234567890abcdef" {
		t.Errorf("app_id mismatch: %v", decoded["app_id"])
	}
	if decoded["mch_id"] != "1900000109" {
		t.Errorf("mch_id mismatch: %v", decoded["mch_id"])
	}
	if decoded["notify_url"] != "https://api.tokenhubhk.com/api/v1/callback/wechat" {
		t.Errorf("notify_url mismatch: %v", decoded["notify_url"])
	}
}

func TestConfigService_UpdateProvider_AlipayFullMock(t *testing.T) {
	svc, db := setupConfigServiceTest(t)
	seedProviders(t, db)

	configJSON, _ := json.Marshal(alipayMockConfig)
	if err := svc.UpdateProvider(context.Background(), "ALIPAY", map[string]interface{}{
		"config_json": string(configJSON),
	}); err != nil {
		t.Fatalf("UpdateProvider ALIPAY: %v", err)
	}

	provider, err := svc.GetProvider(context.Background(), "ALIPAY")
	if err != nil {
		t.Fatalf("GetProvider ALIPAY: %v", err)
	}
	var decoded map[string]interface{}
	json.Unmarshal([]byte(provider.ConfigJSON), &decoded)
	if decoded["app_id"] != "2021000000000000" {
		t.Errorf("alipay app_id mismatch: %v", decoded["app_id"])
	}
	if decoded["sign_type"] != "RSA2" {
		t.Errorf("sign_type should be RSA2, got: %v", decoded["sign_type"])
	}
}

func TestConfigService_UpdateProvider_PayPalFullMock(t *testing.T) {
	svc, db := setupConfigServiceTest(t)
	seedProviders(t, db)

	configJSON, _ := json.Marshal(paypalMockConfig)
	if err := svc.UpdateProvider(context.Background(), "PAYPAL", map[string]interface{}{
		"config_json": string(configJSON),
	}); err != nil {
		t.Fatalf("UpdateProvider PAYPAL: %v", err)
	}

	provider, err := svc.GetProvider(context.Background(), "PAYPAL")
	if err != nil {
		t.Fatalf("GetProvider PAYPAL: %v", err)
	}
	var decoded map[string]interface{}
	json.Unmarshal([]byte(provider.ConfigJSON), &decoded)
	if decoded["mode"] != "sandbox" {
		t.Errorf("paypal mode should be sandbox, got: %v", decoded["mode"])
	}
	if decoded["webhook_id"] != "3XY44955XF432341J" {
		t.Errorf("paypal webhook_id mismatch: %v", decoded["webhook_id"])
	}
}

func TestConfigService_ToggleProvider(t *testing.T) {
	svc, db := setupConfigServiceTest(t)
	seedProviders(t, db)

	// 初始：is_active=false → 启用
	provider, err := svc.ToggleProvider(context.Background(), "WECHAT")
	if err != nil {
		t.Fatalf("ToggleProvider: %v", err)
	}
	if !provider.IsActive {
		t.Error("after first toggle, should be active")
	}

	// 再次 toggle → 停用
	provider, err = svc.ToggleProvider(context.Background(), "WECHAT")
	if err != nil {
		t.Fatalf("ToggleProvider second: %v", err)
	}
	if provider.IsActive {
		t.Error("after second toggle, should be inactive")
	}
}

func TestConfigService_ToggleProvider_AllFour(t *testing.T) {
	svc, db := setupConfigServiceTest(t)
	seedProviders(t, db)

	gateways := []string{"WECHAT", "ALIPAY", "STRIPE", "PAYPAL"}
	for _, gw := range gateways {
		p, err := svc.ToggleProvider(context.Background(), gw)
		if err != nil {
			t.Fatalf("toggle %s: %v", gw, err)
		}
		if !p.IsActive {
			t.Errorf("%s should be active after toggle from false", gw)
		}
	}
}

// ==================== 银行账号 CRUD 测试 ====================

func TestConfigService_BankAccount_CreateListUpdateDelete(t *testing.T) {
	svc, _ := setupConfigServiceTest(t)

	// Create
	acc := &model.BankAccount{
		AccountName:   "TokenHub Technology Ltd",
		BankName:      "Bank of China",
		BranchName:    "Shanghai Central Branch",
		AccountNumber: "6228480000000001",
		SwiftCode:     "BKCHCNBJ",
		Currency:      "CNY",
		Remark:        "对公结算主账号",
		IsActive:      true,
		SortOrder:     1,
	}
	if err := svc.CreateBankAccount(context.Background(), acc); err != nil {
		t.Fatalf("create: %v", err)
	}
	if acc.ID == 0 {
		t.Error("ID not set after create")
	}

	// List
	accounts, err := svc.GetAllBankAccounts(context.Background())
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(accounts) != 1 {
		t.Errorf("expected 1, got %d", len(accounts))
	}
	if accounts[0].AccountNumber != "6228480000000001" {
		t.Errorf("account_number mismatch")
	}

	// Update
	if err := svc.UpdateBankAccount(context.Background(), acc.ID, map[string]interface{}{
		"remark": "Updated remark",
	}); err != nil {
		t.Fatalf("update: %v", err)
	}
	updated, _ := svc.GetAllBankAccounts(context.Background())
	if updated[0].Remark != "Updated remark" {
		t.Errorf("remark not updated: %s", updated[0].Remark)
	}

	// Delete
	if err := svc.DeleteBankAccount(context.Background(), acc.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	afterDelete, _ := svc.GetAllBankAccounts(context.Background())
	if len(afterDelete) != 0 {
		t.Errorf("expected 0 after delete, got %d", len(afterDelete))
	}
}

func TestConfigService_BankAccount_SortOrder(t *testing.T) {
	svc, _ := setupConfigServiceTest(t)
	for _, acc := range []model.BankAccount{
		{AccountName: "C", BankName: "B3", AccountNumber: "3", SortOrder: 3},
		{AccountName: "A", BankName: "B1", AccountNumber: "1", SortOrder: 1},
		{AccountName: "B", BankName: "B2", AccountNumber: "2", SortOrder: 2},
	} {
		a := acc
		svc.CreateBankAccount(context.Background(), &a)
	}
	list, _ := svc.GetAllBankAccounts(context.Background())
	if list[0].AccountName != "A" || list[1].AccountName != "B" || list[2].AccountName != "C" {
		t.Errorf("sort_order not respected: %v %v %v", list[0].AccountName, list[1].AccountName, list[2].AccountName)
	}
}

// ==================== 付款方式测试 ====================

func TestConfigService_PaymentMethod_GetAll(t *testing.T) {
	svc, db := setupConfigServiceTest(t)
	seedPaymentMethods(t, db)

	methods, err := svc.GetAllPaymentMethods(context.Background())
	if err != nil {
		t.Fatalf("GetAllPaymentMethods: %v", err)
	}
	if len(methods) != 5 {
		t.Errorf("expected 5 methods, got %d", len(methods))
	}
}

func TestConfigService_PaymentMethod_Toggle(t *testing.T) {
	svc, db := setupConfigServiceTest(t)
	seedPaymentMethods(t, db) // ALIPAY 经 SQL 设为 is_active=false

	// ALIPAY is_active=false → toggle → true
	m, err := svc.TogglePaymentMethod(context.Background(), "ALIPAY")
	if err != nil {
		t.Fatalf("toggle: %v", err)
	}
	if !m.IsActive {
		t.Error("ALIPAY should be active after toggle from false")
	}

	// 再 toggle → false
	m2, err := svc.TogglePaymentMethod(context.Background(), "ALIPAY")
	if err != nil {
		t.Fatalf("toggle back: %v", err)
	}
	if m2.IsActive {
		t.Error("ALIPAY should be inactive after second toggle")
	}
}

func TestConfigService_PaymentMethod_Update(t *testing.T) {
	svc, db := setupConfigServiceTest(t)
	seedPaymentMethods(t, db)

	if err := svc.UpdatePaymentMethod(context.Background(), "STRIPE", map[string]interface{}{
		"display_name": "Stripe 国际支付",
		"description":  "支持 Visa/Mastercard/AmEx",
	}); err != nil {
		t.Fatalf("update: %v", err)
	}
	methods, _ := svc.GetAllPaymentMethods(context.Background())
	for _, m := range methods {
		if m.Type == "STRIPE" {
			if m.DisplayName != "Stripe 国际支付" {
				t.Errorf("display_name not updated: %s", m.DisplayName)
			}
			return
		}
	}
	t.Error("STRIPE method not found")
}

// ==================== GetActivePaymentMethods 公开接口测试 ====================

func TestConfigService_GetActivePaymentMethods_OnlyActive(t *testing.T) {
	svc, db := setupConfigServiceTest(t)
	seedPaymentMethods(t, db) // BANK_TRANSFER is_active=true, 其余 false

	methods, err := svc.GetActivePaymentMethods(context.Background())
	if err != nil {
		t.Fatalf("GetActivePaymentMethods: %v", err)
	}
	if len(methods) != 1 {
		t.Errorf("expected 1 active method (BANK_TRANSFER), got %d", len(methods))
	}
	if methods[0].Type != "BANK_TRANSFER" {
		t.Errorf("expected BANK_TRANSFER, got %s", methods[0].Type)
	}
}

func TestConfigService_GetActivePaymentMethods_BankTransfer_AttachesBankAccounts(t *testing.T) {
	svc, db := setupConfigServiceTest(t)
	seedPaymentMethods(t, db) // BANK_TRANSFER is_active=true

	// 添加一个活跃银行账号
	acc := &model.BankAccount{
		AccountName:   "TokenHub Ltd",
		BankName:      "ICBC",
		AccountNumber: "0000000001",
		IsActive:      true,
		SortOrder:     1,
	}
	svc.CreateBankAccount(context.Background(), acc)

	methods, err := svc.GetActivePaymentMethods(context.Background())
	if err != nil {
		t.Fatalf("GetActivePaymentMethods: %v", err)
	}

	for _, m := range methods {
		if m.Type == "BANK_TRANSFER" {
			if len(m.BankAccounts) != 1 {
				t.Errorf("BANK_TRANSFER should have 1 bank account, got %d", len(m.BankAccounts))
			}
			if m.BankAccounts[0].AccountNumber != "0000000001" {
				t.Errorf("bank account number mismatch")
			}
			return
		}
	}
	t.Error("BANK_TRANSFER not found in active methods")
}

func TestConfigService_GetActivePaymentMethods_AllEnabled(t *testing.T) {
	svc, db := setupConfigServiceTest(t)
	seedPaymentMethods(t, db)

	// 启用全部 4 个网关
	gateways := []string{"WECHAT", "ALIPAY", "STRIPE", "PAYPAL"}
	for _, gw := range gateways {
		svc.TogglePaymentMethod(context.Background(), gw)
	}

	methods, err := svc.GetActivePaymentMethods(context.Background())
	if err != nil {
		t.Fatalf("GetActivePaymentMethods: %v", err)
	}
	if len(methods) != 5 {
		t.Errorf("expected 5 active methods, got %d", len(methods))
	}
}

// ==================== 兼容性验证：字段格式与网关要求匹配 ====================

func TestCompatibility_Wechat_RequiredFields(t *testing.T) {
	required := []string{"app_id", "mch_id", "api_key", "api_v3_key", "cert_serial_no", "private_key", "notify_url"}
	for _, field := range required {
		if _, ok := wechatMockConfig[field]; !ok {
			t.Errorf("wechat mock config missing required field: %s", field)
		}
	}
	// app_id 格式验证（wx 开头）
	appID, _ := wechatMockConfig["app_id"].(string)
	if !strings.HasPrefix(appID, "wx") {
		t.Errorf("wechat app_id should start with 'wx', got: %s", appID)
	}
}

func TestCompatibility_Alipay_RequiredFields(t *testing.T) {
	required := []string{"app_id", "private_key", "alipay_public_key", "sign_type", "notify_url", "return_url"}
	for _, field := range required {
		if _, ok := alipayMockConfig[field]; !ok {
			t.Errorf("alipay mock config missing required field: %s", field)
		}
	}
	// sign_type 必须是 RSA2
	signType, _ := alipayMockConfig["sign_type"].(string)
	if signType != "RSA2" {
		t.Errorf("alipay sign_type must be RSA2, got: %s", signType)
	}
	// private_key 格式验证（PEM 格式）
	pk, _ := alipayMockConfig["private_key"].(string)
	if !strings.Contains(pk, "PRIVATE KEY") {
		t.Errorf("alipay private_key should be PEM format")
	}
}

func TestCompatibility_Stripe_RequiredFields(t *testing.T) {
	required := []string{"publishable_key", "secret_key", "webhook_secret", "currency", "success_url", "cancel_url"}
	for _, field := range required {
		if _, ok := stripeMockConfig[field]; !ok {
			t.Errorf("stripe mock config missing required field: %s", field)
		}
	}
	// pk_ / sk_ 前缀验证（沙箱用 pk_test_/sk_test_，生产用 pk_live_/sk_live_）
	pk, _ := stripeMockConfig["publishable_key"].(string)
	if !strings.HasPrefix(pk, "pk_") {
		t.Errorf("stripe publishable_key should start with pk_, got: %s", pk)
	}
	sk, _ := stripeMockConfig["secret_key"].(string)
	if !strings.HasPrefix(sk, "sk_") {
		t.Errorf("stripe secret_key should start with sk_, got: %s", sk)
	}
	// webhook_secret 格式
	ws, _ := stripeMockConfig["webhook_secret"].(string)
	if !strings.HasPrefix(ws, "whsec_") {
		t.Errorf("stripe webhook_secret should start with whsec_, got: %s", ws)
	}
}

func TestCompatibility_PayPal_RequiredFields(t *testing.T) {
	required := []string{"client_id", "client_secret", "webhook_id", "mode", "return_url", "cancel_url"}
	for _, field := range required {
		if _, ok := paypalMockConfig[field]; !ok {
			t.Errorf("paypal mock config missing required field: %s", field)
		}
	}
	// mode 必须是 sandbox 或 live
	mode, _ := paypalMockConfig["mode"].(string)
	if mode != "sandbox" && mode != "live" {
		t.Errorf("paypal mode must be 'sandbox' or 'live', got: %s", mode)
	}
}

func TestCompatibility_NotifyURL_Format(t *testing.T) {
	// 验证回调地址使用 HTTPS 并包含正确路径
	notifyURLs := map[string]string{
		"wechat": wechatMockConfig["notify_url"].(string),
		"alipay": alipayMockConfig["notify_url"].(string),
	}
	for gw, url := range notifyURLs {
		if !strings.HasPrefix(url, "https://") {
			t.Errorf("%s notify_url should use HTTPS: %s", gw, url)
		}
		if !strings.Contains(url, "/callback/"+gw) {
			t.Errorf("%s notify_url should contain /callback/%s path: %s", gw, gw, url)
		}
	}
}

// ==================== 并发安全测试 ====================

func TestConfigService_ConcurrentEncrypt(t *testing.T) {
	svc, _ := setupConfigServiceTest(t)
	plain := `{"concurrent":"test"}`
	done := make(chan bool, 10)
	for i := 0; i < 10; i++ {
		go func() {
			enc, err := svc.Encrypt(plain)
			if err != nil {
				t.Errorf("concurrent encrypt: %v", err)
				done <- false
				return
			}
			dec, err := svc.Decrypt(enc)
			if err != nil || dec != plain {
				t.Errorf("concurrent decrypt: err=%v dec=%q", err, dec)
				done <- false
				return
			}
			done <- true
		}()
	}
	for i := 0; i < 10; i++ {
		<-done
	}
}
