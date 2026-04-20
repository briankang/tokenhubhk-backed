package exchange

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	goredis "github.com/redis/go-redis/v9"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"tokenhub-server/internal/model"
)

// mockHTTPDoer 模拟 HTTP 客户端
type mockHTTPDoer struct {
	mu        sync.Mutex
	responses map[string]mockResponse // url → resp
	calls     []string
}

type mockResponse struct {
	status int
	body   string
	err    error
}

func (m *mockHTTPDoer) Do(req *http.Request) (*http.Response, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls = append(m.calls, req.URL.String())
	for pattern, resp := range m.responses {
		if strings.Contains(req.URL.String(), pattern) {
			if resp.err != nil {
				return nil, resp.err
			}
			return &http.Response{
				StatusCode: resp.status,
				Body:       io.NopCloser(bytes.NewReader([]byte(resp.body))),
			}, nil
		}
	}
	return nil, errors.New("no mock response for " + req.URL.String())
}

// setupTestDB 内存 SQLite
func setupTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	if err := db.AutoMigrate(&model.ExchangeRateHistory{}); err != nil {
		t.Fatalf("automigrate: %v", err)
	}
	return db
}

// setupTestRedis 返回 miniredis + client
func setupTestRedis(t *testing.T) (*miniredis.Miniredis, *goredis.Client) {
	t.Helper()
	mr := miniredis.RunT(t)
	client := goredis.NewClient(&goredis.Options{Addr: mr.Addr()})
	return mr, client
}

// 常用 mock 响应（对齐真实阿里云云市场接口响应）
const (
	// 主接口 cmapi00064402 (jmhlcx)
	primarySuccessBody = `{"data":{"money":"7.2345"},"msg":"成功","success":true,"code":200,"taskNo":"507706989209647401353906"}`
	primaryInvalidJSON = `not a json`
	primaryErrorCode   = `{"code":400,"msg":"APPCODE invalid","success":false}`

	// 备用接口 cmapi00063890 (smkjzgyhss - 中国银行实时汇率)
	// zhesuan=712.34 ÷ 100 = 7.1234
	backupSuccessBody = `{"msg":"成功","success":true,"code":200,"data":{"list":[{"code":"USD","name":"美元","zhesuan":"712.34","hui_in":"710.95","hui_out":"713.82","chao_in":"710.95","chao_out":"713.82","time":"05:30:00","day":"2026-04-18"}],"ret_code":"0"}}`
	backupEmptyList   = `{"msg":"成功","success":true,"code":200,"data":{"list":[]}}`
	backupNoUSD       = `{"msg":"成功","success":true,"code":200,"data":{"list":[{"code":"EUR","zhesuan":"780.00"}]}}`
)

func newSvc(t *testing.T, doer *mockHTTPDoer) (*ExchangeRateService, *gorm.DB, *goredis.Client, *miniredis.Miniredis) {
	db := setupTestDB(t)
	mr, client := setupTestRedis(t)
	cfg := Config{
		PrimaryURL:     "https://primary/exchange_rate/info",
		BackupURL:      "https://backup/waihui-list2",
		AppCode:        "TEST_CODE",
		CacheTTL:       24 * time.Hour,
		DefaultRate:    7.2,
		RequestTimeout: 2 * time.Second,
	}
	svc := New(db, client, cfg)
	svc.SetHTTPDoer(doer)
	return svc, db, client, mr
}

func TestExchangeRateService_FetchFromPrimary_Success(t *testing.T) {
	doer := &mockHTTPDoer{responses: map[string]mockResponse{
		"primary": {status: 200, body: primarySuccessBody},
	}}
	svc, db, _, _ := newSvc(t, doer)
	if err := svc.FetchAndCacheDaily(context.Background()); err != nil {
		t.Fatalf("expected success, got %v", err)
	}
	var hist model.ExchangeRateHistory
	if err := db.First(&hist).Error; err != nil {
		t.Fatalf("history not persisted: %v", err)
	}
	if hist.Rate != 7.2345 || hist.Source != SourcePrimary {
		t.Errorf("unexpected history: %+v", hist)
	}
}

func TestExchangeRateService_FetchFromPrimary_InvalidJSON_FallsBackToBackup(t *testing.T) {
	doer := &mockHTTPDoer{responses: map[string]mockResponse{
		"primary": {status: 200, body: primaryInvalidJSON},
		"backup":  {status: 200, body: backupSuccessBody},
	}}
	svc, db, _, _ := newSvc(t, doer)
	if err := svc.FetchAndCacheDaily(context.Background()); err != nil {
		t.Fatalf("expected success via backup, got %v", err)
	}
	var hist model.ExchangeRateHistory
	if err := db.First(&hist).Error; err != nil {
		t.Fatalf("history not persisted: %v", err)
	}
	if hist.Source != SourceBackup {
		t.Errorf("expected backup source, got %s", hist.Source)
	}
	if hist.Rate != 7.1234 {
		t.Errorf("expected rate 7.1234, got %f", hist.Rate)
	}
}

func TestExchangeRateService_BothFail_NoHistory_FallbackToDefault(t *testing.T) {
	doer := &mockHTTPDoer{responses: map[string]mockResponse{
		"primary": {err: errors.New("network")},
		"backup":  {err: errors.New("network")},
	}}
	svc, _, _, _ := newSvc(t, doer)
	err := svc.FetchAndCacheDaily(context.Background())
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
	// Get should return default
	rate, _ := svc.GetUSDToCNY(context.Background())
	if rate != 7.2 {
		t.Errorf("expected default 7.2, got %f", rate)
	}
}

func TestExchangeRateService_BothFail_ReturnLastFromDB(t *testing.T) {
	doer := &mockHTTPDoer{responses: map[string]mockResponse{
		"primary": {err: errors.New("network")},
		"backup":  {err: errors.New("network")},
	}}
	svc, db, _, _ := newSvc(t, doer)
	// 预填 DB
	db.Create(&model.ExchangeRateHistory{
		FromCurrency: "USD",
		ToCurrency:   "CNY",
		Rate:         6.8765,
		Source:       SourceManual,
		FetchedAt:    time.Now().Add(-1 * time.Hour),
	})
	rate, _ := svc.GetUSDToCNY(context.Background())
	if rate != 6.8765 {
		t.Errorf("expected 6.8765 from DB, got %f", rate)
	}
}

func TestExchangeRateService_GetUSDToCNY_RedisHit(t *testing.T) {
	doer := &mockHTTPDoer{}
	svc, _, client, _ := newSvc(t, doer)
	ctx := context.Background()
	client.Set(ctx, "fx:USD_CNY", "7.5000", time.Hour)
	rate, _ := svc.GetUSDToCNY(ctx)
	if rate != 7.5 {
		t.Errorf("expected 7.5 from redis, got %f", rate)
	}
}

func TestExchangeRateService_ConvertUSDToCNY(t *testing.T) {
	doer := &mockHTTPDoer{}
	svc, _, client, _ := newSvc(t, doer)
	client.Set(context.Background(), "fx:USD_CNY", "7.2345", time.Hour)
	cny := svc.ConvertUSDToCNY(context.Background(), 100)
	// 100 * 7.2345 = 723.45
	if cny < 723.44 || cny > 723.46 {
		t.Errorf("expected ~723.45, got %f", cny)
	}
}

func TestExchangeRateService_ConvertCNYToUSD(t *testing.T) {
	doer := &mockHTTPDoer{}
	svc, _, client, _ := newSvc(t, doer)
	client.Set(context.Background(), "fx:USD_CNY", "7.2345", time.Hour)
	usd := svc.ConvertCNYToUSD(context.Background(), 723.45)
	// 723.45 / 7.2345 ≈ 100.00
	if usd < 99.99 || usd > 100.01 {
		t.Errorf("expected ~100, got %f", usd)
	}
}

func TestExchangeRateService_ManualOverride(t *testing.T) {
	doer := &mockHTTPDoer{}
	svc, db, _, _ := newSvc(t, doer)
	if err := svc.ManualOverride(context.Background(), 7.55, 999, "test"); err != nil {
		t.Fatalf("manual override: %v", err)
	}
	var hist model.ExchangeRateHistory
	db.First(&hist)
	if hist.Source != SourceManual || hist.Rate != 7.55 {
		t.Errorf("bad override: %+v", hist)
	}
}

func TestExchangeRateService_RateSanityCheck(t *testing.T) {
	// 超过 maxSaneRate → rejected
	doer := &mockHTTPDoer{responses: map[string]mockResponse{
		"primary": {status: 200, body: `{"showapi_res_code":0,"showapi_res_body":{"ret_code":0,"result":{"money":9999}}}`},
		"backup":  {err: errors.New("skip")},
	}}
	svc, _, _, _ := newSvc(t, doer)
	err := svc.FetchAndCacheDaily(context.Background())
	if err == nil {
		t.Errorf("expected sanity error, got nil")
	}
}

func TestExchangeRateService_ManualOverride_BadRate(t *testing.T) {
	doer := &mockHTTPDoer{}
	svc, _, _, _ := newSvc(t, doer)
	if err := svc.ManualOverride(context.Background(), 0, 1, "x"); err == nil {
		t.Errorf("expected sane error, got nil")
	}
	if err := svc.ManualOverride(context.Background(), 1000, 1, "x"); err == nil {
		t.Errorf("expected sane error, got nil")
	}
}

func TestExchangeRateService_GetUSDToCNYWithMeta(t *testing.T) {
	doer := &mockHTTPDoer{}
	svc, db, _, _ := newSvc(t, doer)
	now := time.Now()
	db.Create(&model.ExchangeRateHistory{
		FromCurrency: "USD", ToCurrency: "CNY", Rate: 7.0, Source: SourcePrimary, FetchedAt: now,
	})
	rate, source, updatedAt, err := svc.GetUSDToCNYWithMeta(context.Background())
	if err != nil {
		t.Fatalf("meta err: %v", err)
	}
	if rate != 7.0 || source != SourcePrimary {
		t.Errorf("bad meta: rate=%f source=%s", rate, source)
	}
	if updatedAt.Unix() != now.Unix() {
		t.Errorf("timestamp mismatch")
	}
}

func TestExchangeRateService_ListHistory(t *testing.T) {
	doer := &mockHTTPDoer{}
	svc, db, _, _ := newSvc(t, doer)
	// 插入 5 条
	for i := 0; i < 5; i++ {
		db.Create(&model.ExchangeRateHistory{
			FromCurrency: "USD", ToCurrency: "CNY", Rate: 7.0 + float64(i)*0.01,
			Source: SourcePrimary, FetchedAt: time.Now().Add(-time.Duration(i) * time.Hour),
		})
	}
	list, err := svc.ListHistory(context.Background(), 3)
	if err != nil {
		t.Fatalf("list history: %v", err)
	}
	if len(list) != 3 {
		t.Errorf("expected 3, got %d", len(list))
	}
}

// ============================================================
// v3.2.1: 三级 fallback 链测试（public_free 作为第三级兜底）
// ============================================================

const publicSuccessBody = `{"result":"success","base_code":"USD","rates":{"CNY":7.1900,"EUR":0.93,"JPY":156}}`

func TestExchangeRateService_L3_PublicOnly_Success(t *testing.T) {
	// 既无 primary 也无 backup，仅公开接口可用
	doer := &mockHTTPDoer{responses: map[string]mockResponse{
		"open.er-api.com": {status: 200, body: publicSuccessBody},
	}}
	db := setupTestDB(t)
	mr, client := setupTestRedis(t)
	defer mr.Close()
	svc := New(db, client, Config{
		PublicURL:      "https://open.er-api.com/v6/latest/USD",
		CacheTTL:       time.Hour,
		DefaultRate:    7.2,
		RequestTimeout: 2 * time.Second,
	})
	svc.SetHTTPDoer(doer)

	if err := svc.FetchAndCacheDaily(context.Background()); err != nil {
		t.Fatalf("expected success via public, got %v", err)
	}
	var hist model.ExchangeRateHistory
	if err := db.First(&hist).Error; err != nil {
		t.Fatalf("history not persisted: %v", err)
	}
	if hist.Source != SourcePublic {
		t.Errorf("expected public source, got %s", hist.Source)
	}
	if hist.Rate != 7.19 {
		t.Errorf("expected 7.19, got %f", hist.Rate)
	}
}

func TestExchangeRateService_L1L2Fail_L3PublicSuccess(t *testing.T) {
	doer := &mockHTTPDoer{responses: map[string]mockResponse{
		"primary":         {err: errors.New("network")},
		"backup":          {err: errors.New("network")},
		"open.er-api.com": {status: 200, body: publicSuccessBody},
	}}
	db := setupTestDB(t)
	mr, client := setupTestRedis(t)
	defer mr.Close()
	svc := New(db, client, Config{
		PrimaryURL:     "https://primary/api",
		BackupURL:      "https://backup/api",
		PublicURL:      "https://open.er-api.com/v6/latest/USD",
		CacheTTL:       time.Hour,
		DefaultRate:    7.2,
		RequestTimeout: 2 * time.Second,
	})
	svc.SetHTTPDoer(doer)
	if err := svc.FetchAndCacheDaily(context.Background()); err != nil {
		t.Fatalf("expected L3 to succeed after L1/L2 fail, got %v", err)
	}
	var hist model.ExchangeRateHistory
	db.First(&hist)
	if hist.Source != SourcePublic {
		t.Errorf("expected public source, got %s", hist.Source)
	}
}

func TestExchangeRateService_AllThreeFail(t *testing.T) {
	doer := &mockHTTPDoer{responses: map[string]mockResponse{
		"primary":         {err: errors.New("net")},
		"backup":          {err: errors.New("net")},
		"open.er-api.com": {err: errors.New("net")},
	}}
	db := setupTestDB(t)
	mr, client := setupTestRedis(t)
	defer mr.Close()
	svc := New(db, client, Config{
		PrimaryURL:     "https://primary/api",
		BackupURL:      "https://backup/api",
		PublicURL:      "https://open.er-api.com/v6/latest/USD",
		CacheTTL:       time.Hour,
		DefaultRate:    7.2,
		RequestTimeout: 2 * time.Second,
	})
	svc.SetHTTPDoer(doer)
	err := svc.FetchAndCacheDaily(context.Background())
	if err == nil {
		t.Errorf("expected error when all three fail")
	}
	// 验证 default fallback 在 Get 时生效
	rate, _ := svc.GetUSDToCNY(context.Background())
	if rate != 7.2 {
		t.Errorf("should fall back to default 7.2, got %f", rate)
	}
}

func TestParsePublicResponse(t *testing.T) {
	cases := []struct {
		name    string
		body    string
		wantErr bool
		wantV   float64
	}{
		{"valid open.er-api", publicSuccessBody, false, 7.19},
		{"valid conversion_rates mirror", `{"result":"success","conversion_rates":{"CNY":7.15}}`, false, 7.15},
		{"result=error", `{"result":"error","error-type":"invalid-key"}`, true, 0},
		{"no CNY", `{"result":"success","rates":{"EUR":0.9}}`, true, 0},
		{"bad json", `not json`, true, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			v, err := parsePublicResponse([]byte(c.body))
			if (err != nil) != c.wantErr {
				t.Errorf("err=%v wantErr=%v", err, c.wantErr)
			}
			if !c.wantErr && v != c.wantV {
				t.Errorf("got %f, want %f", v, c.wantV)
			}
		})
	}
}

func TestParsePrimaryResponse(t *testing.T) {
	tests := []struct {
		name    string
		body    string
		wantErr bool
		wantV   float64
	}{
		{"valid string money", primarySuccessBody, false, 7.2345},
		{"valid numeric money", `{"code":200,"data":{"money":6.8095},"success":true}`, false, 6.8095},
		{"bad json", primaryInvalidJSON, true, 0},
		{"api error code", primaryErrorCode, true, 0},
		{"empty money", `{"code":200,"data":{"money":""},"success":true}`, true, 0},
		{"zero money", `{"code":200,"data":{"money":"0"},"success":true}`, true, 0},
		{"negative money", `{"code":200,"data":{"money":"-1"},"success":true}`, true, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v, err := parsePrimaryResponse([]byte(tt.body))
			if (err != nil) != tt.wantErr {
				t.Errorf("err = %v, wantErr = %v", err, tt.wantErr)
			}
			if !tt.wantErr && v != tt.wantV {
				t.Errorf("got %f, want %f", v, tt.wantV)
			}
		})
	}
}

func TestParseBackupResponse(t *testing.T) {
	tests := []struct {
		name    string
		body    string
		wantErr bool
		wantV   float64
	}{
		{"valid zhesuan=712.34 → 7.1234", backupSuccessBody, false, 7.1234},
		{"valid hui_in+hui_out fallback", `{"code":200,"data":{"list":[{"code":"USD","hui_in":"680","hui_out":"690"}]}}`, false, 6.85},
		{"valid chao fallback", `{"code":200,"data":{"list":[{"code":"USD","chao_in":"680","chao_out":"690"}]}}`, false, 6.85},
		{"empty list", backupEmptyList, true, 0},
		{"no USD row", backupNoUSD, true, 0},
		{"bad json", `xxx`, true, 0},
		{"api error code", `{"code":400,"msg":"APPCODE invalid"}`, true, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v, err := parseBackupResponse([]byte(tt.body))
			if (err != nil) != tt.wantErr {
				t.Errorf("err = %v, wantErr = %v", err, tt.wantErr)
			}
			if !tt.wantErr && (v < tt.wantV-0.001 || v > tt.wantV+0.001) {
				t.Errorf("got %f, want ~%f", v, tt.wantV)
			}
		})
	}
}

func TestParseMeta(t *testing.T) {
	ts := time.Now().Unix()
	s := fmtInt64(ts) + "|aliyun_primary"
	tm, src := parseMeta(s)
	if tm.Unix() != ts {
		t.Errorf("time mismatch: got %d want %d", tm.Unix(), ts)
	}
	if src != "aliyun_primary" {
		t.Errorf("source mismatch: %s", src)
	}
	tm2, src2 := parseMeta("")
	if !tm2.IsZero() || src2 != SourceDefault {
		t.Errorf("empty should return default")
	}
}

func fmtInt64(v int64) string {
	// 避免直接 import strconv 冲突（文件内多处已使用）；采用简单手写
	const digits = "0123456789"
	if v == 0 {
		return "0"
	}
	buf := make([]byte, 0, 20)
	for v > 0 {
		buf = append([]byte{digits[v%10]}, buf...)
		v /= 10
	}
	return string(buf)
}
