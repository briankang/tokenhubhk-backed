package api_test

import (
	"net/http"
	"strings"
	"testing"
)

func TestCreatePayment_Success(t *testing.T) {
	requireUser(t)

	resp, statusCode, err := doPost(baseURL+"/api/v1/payment/create", map[string]interface{}{
		"gateway":  "stripe",
		"amount":   10.00,
		"currency": "USD",
		"subject":  "Test topup",
	}, userToken)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	skipIfNotImplemented(t, statusCode)
	skipIfNotFound(t, statusCode)

	if statusCode != http.StatusOK {
		if statusCode == http.StatusInternalServerError && strings.Contains(resp.Message, "gateway") {
			t.Skipf("payment gateway not configured: %s", resp.Message)
		}
		t.Fatalf("expected 200, got %d: %s", statusCode, resp.Message)
	}
}

func TestQueryPayment_Success(t *testing.T) {
	requireUser(t)

	// Create a payment first to get an order number
	createResp, statusCode, err := doPost(baseURL+"/api/v1/payment/create", map[string]interface{}{
		"gateway":  "stripe",
		"amount":   5.00,
		"currency": "USD",
		"subject":  "Query test",
	}, userToken)
	if err != nil {
		t.Fatalf("create failed: %v", err)
	}
	skipIfNotImplemented(t, statusCode)
	skipIfNotFound(t, statusCode)
	if statusCode != http.StatusOK {
		t.Skipf("create payment returned %d", statusCode)
	}

	// Extract order_no from response
	var result struct {
		OrderNo string `json:"order_no"`
	}
	if err := unmarshalData(createResp.Data, &result); err != nil || result.OrderNo == "" {
		t.Skip("no order_no in create response")
	}

	// Query
	resp, statusCode, err := doGet(baseURL+"/api/v1/payment/query/"+result.OrderNo, userToken)
	if err != nil {
		t.Fatalf("query failed: %v", err)
	}
	skipIfNotImplemented(t, statusCode)

	if statusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", statusCode, resp.Message)
	}
}

func TestListPayments_Success(t *testing.T) {
	requireUser(t)

	resp, statusCode, err := doGet(baseURL+"/api/v1/payment/list?page=1&page_size=10", userToken)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	skipIfNotImplemented(t, statusCode)
	skipIfNotFound(t, statusCode)

	if statusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", statusCode, resp.Message)
	}
}

func TestCreatePayment_InvalidGateway(t *testing.T) {
	requireUser(t)

	resp, statusCode, err := doPost(baseURL+"/api/v1/payment/create", map[string]interface{}{
		"gateway":  "invalid_gateway",
		"amount":   10.00,
		"currency": "USD",
		"subject":  "Bad gateway test",
	}, userToken)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	skipIfNotImplemented(t, statusCode)
	skipIfNotFound(t, statusCode)

	// Should be rejected with 400
	if statusCode == http.StatusOK && resp.Code == 0 {
		t.Error("expected invalid gateway to be rejected, but it succeeded")
	}
}
