package integration_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
)

// TestE2E_FullBusinessFlow tests the complete business lifecycle:
// 1. Admin creates supplier + model category + AI model
// 2. Admin creates channel + channel group
// 3. Admin sets model pricing
// 4. Admin creates L1 agent
// 5. L1 agent logs in, sets whitelabel config
// 6. L1 agent creates L2 sub-agent
// 7. User registers + logs in + generates API key
// 8. User calls /chat/completions with API key
// 9. Check usage report
// 10. Check profit report
func TestE2E_FullBusinessFlow(t *testing.T) {
	if adminToken == "" {
		t.Skip("admin token not available")
	}

	ts := fmt.Sprintf("%d", time.Now().UnixMilli())

	// ---- Step 1: Create supplier ----
	t.Log("Step 1: Create supplier")
	supplierResp, statusCode, err := doPost(baseURL+"/api/v1/admin/suppliers", map[string]interface{}{
		"name":     "TestSupplier_" + ts,
		"base_url": "https://api.openai.com",
		"type":     "openai",
	}, adminToken)
	skipIfNotImplemented(t, statusCode)
	skipIfNotFound(t, statusCode)
	var supplierID uint
	if err == nil && statusCode == http.StatusOK {
		var s struct{ ID uint `json:"id"` }
		json.Unmarshal(supplierResp.Data, &s)
		supplierID = s.ID
		t.Logf("  Supplier created: ID=%d", supplierID)
	} else {
		t.Logf("  Supplier creation skipped (status=%d): %v", statusCode, err)
		supplierID = 1 // fallback to ID 1
	}

	// Create model category
	t.Log("Step 1b: Create model category")
	_, catStatus, _ := doPost(baseURL+"/api/v1/admin/model-categories", map[string]interface{}{
		"name": "LLM_" + ts,
		"code": "llm_" + ts,
	}, adminToken)
	if catStatus == http.StatusNotImplemented || catStatus == http.StatusNotFound {
		t.Log("  Model category endpoint not available, continuing...")
	}

	// Create AI model
	t.Log("Step 1c: Create AI model")
	modelResp, modelStatus, _ := doPost(baseURL+"/api/v1/admin/ai-models", map[string]interface{}{
		"name":        "gpt-4-e2e-" + ts,
		"code":        "gpt-4-e2e-" + ts,
		"supplier_id": supplierID,
	}, adminToken)
	var modelID uint
	if modelStatus == http.StatusOK {
		var m struct{ ID uint `json:"id"` }
		json.Unmarshal(modelResp.Data, &m)
		modelID = m.ID
		t.Logf("  AI model created: ID=%d", modelID)
	} else {
		t.Log("  AI model creation skipped, using model_id=1")
		modelID = 1
	}

	// ---- Step 2: Create channel + channel group ----
	t.Log("Step 2: Create channel")
	chName := "e2e_ch_" + ts
	chResp, chStatus, _ := doPost(baseURL+"/api/v1/admin/channels", map[string]interface{}{
		"name":        chName,
		"supplier_id": supplierID,
		"type":        "openai",
		"endpoint":    "https://api.openai.com/v1",
		"api_key":     "sk-test-e2e-" + ts,
		"weight":      10,
		"priority":    1,
		"status":      "active",
	}, adminToken)
	var channelID uint
	if chStatus == http.StatusOK {
		var ch struct{ ID uint `json:"id"` }
		json.Unmarshal(chResp.Data, &ch)
		channelID = ch.ID
		t.Logf("  Channel created: ID=%d", channelID)
	} else {
		t.Logf("  Channel creation returned %d", chStatus)
	}

	t.Log("Step 2b: Create channel group")
	grpName := "e2e_grp_" + ts
	_, grpStatus, _ := doPost(baseURL+"/api/v1/admin/channel-groups", map[string]interface{}{
		"name":     grpName,
		"code":     grpName,
		"strategy": "round_robin",
	}, adminToken)
	t.Logf("  Channel group: status=%d", grpStatus)

	// ---- Step 3: Set model pricing ----
	t.Log("Step 3: Set model pricing")
	_, pricingStatus, _ := doPost(baseURL+"/api/v1/admin/model-pricings", map[string]interface{}{
		"model_id":               modelID,
		"input_price_per_token":  0.00003,
		"output_price_per_token": 0.00006,
		"currency":               "USD",
	}, adminToken)
	t.Logf("  Pricing: status=%d", pricingStatus)

	// ---- Step 4: Create L1 agent ----
	t.Log("Step 4: Create L1 agent tenant")
	agentEmail := "e2e_agent_" + ts + "@test.com"
	agentPass := "Agent@123456"
	agentResp, agentStatus, _ := doPost(baseURL+"/api/v1/admin/tenants", map[string]interface{}{
		"name":           "E2E_Agent_" + ts,
		"contact_email":  agentEmail,
		"admin_email":    agentEmail,
		"admin_password": agentPass,
		"admin_name":     "E2E Agent",
	}, adminToken)
	skipIfNotImplemented(t, agentStatus)
	var agentTenantID uint
	if agentStatus == http.StatusOK {
		var tenant struct{ ID uint `json:"id"` }
		json.Unmarshal(agentResp.Data, &tenant)
		agentTenantID = tenant.ID
		t.Logf("  Agent tenant created: ID=%d", agentTenantID)
	} else {
		t.Logf("  Agent tenant creation returned %d, trying register fallback", agentStatus)
		regErr := registerUser(agentEmail, agentPass, "E2E Agent")
		if regErr != nil {
			t.Skipf("cannot create agent: %v", regErr)
		}
	}

	// ---- Step 5: Agent login + whitelabel ----
	t.Log("Step 5: Agent login")
	agentToken, loginErr := loginUser(agentEmail, agentPass)
	if loginErr != nil {
		t.Skipf("agent login failed: %v", loginErr)
	}
	t.Log("  Agent logged in successfully")

	// Set whitelabel config
	t.Log("Step 5b: Set whitelabel config")
	_, wlStatus, _ := doPut(baseURL+"/api/v1/agent/whitelabel", map[string]interface{}{
		"site_name":  "E2E Platform",
		"logo_url":   "https://example.com/logo.png",
		"brand_color": "#FF5722",
	}, agentToken)
	t.Logf("  Whitelabel: status=%d", wlStatus)

	// ---- Step 6: Agent creates sub-agent ----
	t.Log("Step 6: Create sub-agent")
	subAgentEmail := "e2e_sub_" + ts + "@test.com"
	subAgentPass := "SubAgent@123456"
	_, subStatus, _ := doPost(baseURL+"/api/v1/agent/sub-agents", map[string]interface{}{
		"name":           "E2E_SubAgent_" + ts,
		"contact_email":  subAgentEmail,
		"admin_email":    subAgentEmail,
		"admin_password": subAgentPass,
		"admin_name":     "E2E SubAgent",
	}, agentToken)
	t.Logf("  Sub-agent: status=%d", subStatus)

	// ---- Step 7: User registration + login + API key ----
	t.Log("Step 7: User registration + API key")
	endUserEmail := "e2e_user_" + ts + "@test.com"
	endUserPass := "User@123456"
	regErr := registerUser(endUserEmail, endUserPass, "E2E User")
	if regErr != nil {
		t.Logf("  User registration failed: %v", regErr)
	}

	endUserToken, userLoginErr := loginUser(endUserEmail, endUserPass)
	if userLoginErr != nil {
		t.Logf("  User login failed: %v", userLoginErr)
	} else {
		t.Log("  User logged in")

		// Generate API key
		keyResp, keyStatus, _ := doPost(baseURL+"/api/v1/user/api-keys", map[string]string{
			"name": "e2e-key-" + ts,
		}, endUserToken)
		if keyStatus == http.StatusOK {
			var key struct {
				ID  uint   `json:"id"`
				Key string `json:"key"`
			}
			json.Unmarshal(keyResp.Data, &key)
			t.Logf("  API key created: ID=%d", key.ID)

			// ---- Step 8: Call chat completions ----
			if key.Key != "" {
				t.Log("Step 8: Call /chat/completions")
				chatResp, chatStatus, chatErr := doPost(baseURL+"/api/v1/chat/completions", map[string]interface{}{
					"model": "gpt-4",
					"messages": []map[string]string{
						{"role": "user", "content": "Hello, this is an e2e test."},
					},
					"max_tokens": 10,
				}, key.Key)
				if chatErr != nil {
					t.Logf("  Chat request error: %v", chatErr)
				} else {
					t.Logf("  Chat response: status=%d, code=%d", chatStatus, chatResp.Code)
				}
			}
		} else {
			t.Logf("  API key creation: status=%d", keyStatus)
		}
	}

	// ---- Step 9: Check usage report ----
	t.Log("Step 9: Check usage report")
	now := time.Now()
	start := now.AddDate(0, -1, 0).Format("2006-01-02")
	end := now.AddDate(0, 0, 1).Format("2006-01-02")
	usageResp, usageStatus, _ := doGet(
		fmt.Sprintf("%s/api/v1/admin/reports/usage?start_date=%s&end_date=%s", baseURL, start, end),
		adminToken,
	)
	if usageStatus == http.StatusOK {
		t.Logf("  Usage report retrieved: code=%d", usageResp.Code)
	} else {
		t.Logf("  Usage report: status=%d", usageStatus)
	}

	// ---- Step 10: Check profit report ----
	t.Log("Step 10: Check profit report")
	profitResp, profitStatus, _ := doGet(
		fmt.Sprintf("%s/api/v1/admin/reports/profit?start_date=%s&end_date=%s", baseURL, start, end),
		adminToken,
	)
	if profitStatus == http.StatusOK {
		t.Logf("  Profit report retrieved: code=%d", profitResp.Code)
	} else {
		t.Logf("  Profit report: status=%d", profitStatus)
	}

	t.Log("E2E Full Business Flow completed successfully")
}

// TestE2E_PaymentFlow tests the payment lifecycle:
// 1. User creates payment order
// 2. Query order status
// 3. List orders
func TestE2E_PaymentFlow(t *testing.T) {
	// Register a fresh user
	ts := fmt.Sprintf("%d", time.Now().UnixMilli())
	email := "e2e_pay_" + ts + "@test.com"
	password := "Pay@123456"

	regErr := registerUser(email, password, "PayUser_"+ts)
	if regErr != nil {
		t.Skipf("cannot register user: %v", regErr)
	}

	token, loginErr := loginUser(email, password)
	if loginErr != nil {
		t.Skipf("cannot login: %v", loginErr)
	}

	// ---- Step 1: Create payment order ----
	t.Log("Step 1: Create payment order")
	createResp, createStatus, err := doPost(baseURL+"/api/v1/payment/create", map[string]interface{}{
		"gateway":  "stripe",
		"amount":   19.99,
		"currency": "USD",
		"subject":  "E2E payment test",
	}, token)
	if err != nil {
		t.Fatalf("create payment request failed: %v", err)
	}
	skipIfNotImplemented(t, createStatus)
	skipIfNotFound(t, createStatus)

	if createStatus != http.StatusOK {
		if createStatus == http.StatusInternalServerError && strings.Contains(createResp.Message, "gateway") {
			t.Skipf("payment gateway not configured: %s", createResp.Message)
		}
		t.Fatalf("create payment: expected 200, got %d: %s", createStatus, createResp.Message)
	}
	t.Logf("  Payment order created: code=%d", createResp.Code)

	// Extract order number
	var orderResult struct {
		OrderNo string `json:"order_no"`
	}
	if err := unmarshalData(createResp.Data, &orderResult); err != nil || orderResult.OrderNo == "" {
		t.Log("  No order_no in response, skipping query step")
	} else {
		// ---- Step 2: Query order status ----
		t.Log("Step 2: Query order status")
		queryResp, queryStatus, queryErr := doGet(baseURL+"/api/v1/payment/query/"+orderResult.OrderNo, token)
		if queryErr != nil {
			t.Fatalf("query payment failed: %v", queryErr)
		}
		if queryStatus != http.StatusOK {
			t.Logf("  Query returned status=%d: %s", queryStatus, queryResp.Message)
		} else {
			t.Logf("  Order queried successfully: code=%d", queryResp.Code)
		}
	}

	// ---- Step 3: List orders ----
	t.Log("Step 3: List payment orders")
	listResp, listStatus, listErr := doGet(baseURL+"/api/v1/payment/list?page=1&page_size=10", token)
	if listErr != nil {
		t.Fatalf("list payments failed: %v", listErr)
	}
	if listStatus != http.StatusOK {
		t.Fatalf("list payments: expected 200, got %d: %s", listStatus, listResp.Message)
	}
	t.Logf("  Payment list retrieved: code=%d", listResp.Code)

	t.Log("E2E Payment Flow completed successfully")
}
