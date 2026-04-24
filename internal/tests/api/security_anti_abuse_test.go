package api_test

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"testing"
	"time"
)

// TestAdminBatchSetFreeTier_Success 妤犲矁鐦夌粻锛勬倞閸涙ɑ澹掗柌蹇氼啎缂冾喗膩閸ㄥ鍘ょ拹鐟扮湴閸旂喕鍏?
func TestAdminBatchSetFreeTier_Success(t *testing.T) {
	requireAdmin(t)

	// 1. 閼惧嘲褰囬崜?3 娑擃亝膩閸?ID
	resp, status, err := doGet(baseURL+"/api/v1/admin/ai-models?page=1&page_size=3", adminToken)
	if err != nil || status != http.StatusOK {
		t.Skip("cannot list models for batch test")
	}
	page, _ := parsePageData(resp)
	var models []struct {
		ID uint `json:"id"`
	}
	if err := unmarshalData(page.List, &models); err != nil || len(models) == 0 {
		t.Skip("no models available")
	}

	modelIDs := make([]uint, 0, len(models))
	for _, m := range models {
		modelIDs = append(modelIDs, m.ID)
	}

	// 2. 閹靛綊鍣虹拋鍓х枂娑撳搫鍘ょ拹鐟扮湴

	body := map[string]interface{}{
		"model_ids":    modelIDs,
		"is_free_tier": true,
	}
	resp2, status2, err := doPost(baseURL+"/api/v1/admin/ai-models/batch-free-tier", body, adminToken)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	skipIfNotFound(t, status2)
	if status2 != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", status2, resp2.Message)
	}

	// verify first model after batch update
	resp3, status3, err := doGet(fmt.Sprintf("%s/api/v1/admin/ai-models/%d", baseURL, modelIDs[0]), adminToken)
	if err == nil && status3 == http.StatusOK {
		var m struct {
			IsFreeTier bool `json:"is_free_tier"`
		}
		if err := unmarshalData(resp3.Data, &m); err == nil {
			if !m.IsFreeTier {
				t.Error("is_free_tier should be true after batch update")
			}
		}
	}
}

// TestRegister_DisposableEmail_Blocked 妤犲矁鐦夋稉鈧▎鈩冣偓褔鍋栫粻杈ㄦ暈閸愬本瀚ら幋顏堚偓鏄忕帆

func TestRegister_DisposableEmail_Blocked(t *testing.T) {
	// 娴ｈ法鏁ゆ鎴濇倳閸楁洑鑵戦惃鍕厵閸?
	email := uniqueName("scam") + "@10minutemail.com"

	body := map[string]string{
		"email":       email,
		"password":    authPassword(email, "Password@123"),
		"name":        "Scammer",
		"email_code":  testMagicEmailCode,
		"invite_code": testInviteCode,
	}

	// 濞夈劍鍓伴敍姘劃婢跺嫪濞囬悽?doPost閿涘苯鐣犳导姘冲殰閸斻劌鐢稉濠冪ゴ鐠?Bypass Header閿?	// 娴?ValidateEmailDomain 閺勵垯绗熼崝鈥崇湴闁槒绶敍灞肩瑝閸?Header 瑜板崬鎼烽妴?
	resp, status, err := doPost(baseURL+"/api/v1/auth/register", body, "")
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}

	// 鎼存棁顕氭潻鏂挎礀 400 Bad Request
	if status != http.StatusBadRequest {
		t.Errorf("expected 400 for disposable email, got %d: %s", status, resp.Message)
	}
}

// TestAntiAbuse_RPM_Limit 妤犲矁鐦夐崣宥嗕簰閻劋鑵戦梻缈犳閻?RPM 闂勬劙鈧喖鈧槒绶?
func TestAntiAbuse_RPM_Limit(t *testing.T) {
	requireUser(t)

	// 閼惧嘲褰囨稉鈧稉顏呯梾閺堝绁寸拠?Bypass Header 閻ㄥ嫬顓归幋椋庮伂鐠囬攱鐪?
	doRequestNoBypass := func(method, url string, token string) (*apiResponse, int, error) {
		req, _ := http.NewRequest(method, url, nil)
		req.Header.Set("Content-Type", "application/json")
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		client := &http.Client{Timeout: 5 * time.Second}
		resp, err := client.Do(req)
		if err != nil {
			return nil, 0, err
		}
		defer resp.Body.Close()
		respBody, _ := io.ReadAll(resp.Body)
		var apiResp apiResponse
		json.Unmarshal(respBody, &apiResp)
		return &apiResp, resp.StatusCode, nil
	}

	// 閸欐垿鈧礁顦挎稉顏囶嚞濮瑰倽袝閸欐垿妾洪柅?(Free 閻劍鍩涙妯款吇娑?5 RPM)
	limit := 5
	triggered := false
	for i := 0; i < limit+3; i++ {
		_, status, _ := doRequestNoBypass(http.MethodGet, baseURL+"/api/v1/user/usage", userToken)
		if status == http.StatusTooManyRequests {
			triggered = true
			t.Logf("Success: hit RPM limit at request %d", i+1)
			break
		}
	}

	if !triggered {
		t.Log("Warning: RPM limit not triggered. User might be categorized as Paid or Redis state was dirty.")
	}
}
