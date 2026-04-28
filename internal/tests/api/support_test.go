package api_test

import (
	"encoding/json"
	"net/http"
	"strconv"
	"testing"
)

func TestSupportHotQuestionsAndProviderDocs(t *testing.T) {
	requireUser(t)

	resp, statusCode, err := doGet(baseURL+"/api/v1/support/hot-questions", userToken)
	if err != nil {
		t.Fatal(err)
	}
	skipIfNotFound(t, statusCode)
	if statusCode >= http.StatusInternalServerError {
		t.Fatalf("hot questions returned server error: status=%d msg=%s", statusCode, resp.Message)
	}
	if statusCode == http.StatusOK && resp.Code != 0 {
		t.Fatalf("hot questions code=%d msg=%s", resp.Code, resp.Message)
	}

	resp, statusCode, err = doGet(baseURL+"/api/v1/support/provider-docs?q=openai", userToken)
	if err != nil {
		t.Fatal(err)
	}
	skipIfNotFound(t, statusCode)
	if statusCode >= http.StatusInternalServerError {
		t.Fatalf("provider docs returned server error: status=%d msg=%s", statusCode, resp.Message)
	}
	if statusCode == http.StatusOK && resp.Code != 0 {
		t.Fatalf("provider docs code=%d msg=%s", resp.Code, resp.Message)
	}
}

func TestSupportTicketLifecycle(t *testing.T) {
	requireUser(t)

	invalid := map[string]string{
		"title":       "bad category",
		"description": "invalid support category should be rejected",
		"category":    "not-a-category",
	}
	resp, statusCode, err := doPost(baseURL+"/api/v1/support/tickets", invalid, userToken)
	if err != nil {
		t.Fatal(err)
	}
	skipIfNotFound(t, statusCode)
	if statusCode != http.StatusBadRequest {
		t.Fatalf("invalid category status=%d code=%d msg=%s, want 400", statusCode, resp.Code, resp.Message)
	}

	valid := map[string]string{
		"title":         uniqueName("support_ticket"),
		"description":   "API test ticket for support lifecycle coverage",
		"category":      "api",
		"priority":      "normal",
		"contact_email": userEmail,
	}
	resp, statusCode, err = doPost(baseURL+"/api/v1/support/tickets", valid, userToken)
	if err != nil {
		t.Fatal(err)
	}
	if statusCode != http.StatusOK || resp.Code != 0 {
		t.Fatalf("create ticket status=%d code=%d msg=%s", statusCode, resp.Code, resp.Message)
	}
	var created struct {
		ID       uint   `json:"id"`
		TicketNo string `json:"ticket_no"`
	}
	if err := json.Unmarshal(resp.Data, &created); err != nil {
		t.Fatal(err)
	}
	if created.ID == 0 || created.TicketNo == "" {
		t.Fatalf("created ticket missing id/no: %#v", created)
	}

	apiFeedback := map[string]string{
		"title":         uniqueName("api_interface_feedback"),
		"description":   "Model API documentation feedback for support queue routing",
		"category":      "api_interface_feedback",
		"priority":      "normal",
		"contact_email": userEmail,
	}
	resp, statusCode, err = doPost(baseURL+"/api/v1/support/tickets", apiFeedback, userToken)
	if err != nil {
		t.Fatal(err)
	}
	if statusCode != http.StatusOK || resp.Code != 0 {
		t.Fatalf("create API interface feedback ticket status=%d code=%d msg=%s", statusCode, resp.Code, resp.Message)
	}

	resp, statusCode, err = doGet(baseURL+"/api/v1/support/tickets?page=1&page_size=10", userToken)
	if err != nil {
		t.Fatal(err)
	}
	if statusCode != http.StatusOK || resp.Code != 0 {
		t.Fatalf("list tickets status=%d code=%d msg=%s", statusCode, resp.Code, resp.Message)
	}
	page, err := parsePageData(resp)
	if err != nil {
		t.Fatal(err)
	}
	if page.Total < 1 {
		t.Fatalf("ticket list total=%d, want at least 1", page.Total)
	}

	resp, statusCode, err = doGet(baseURL+"/api/v1/support/tickets/"+created.TicketNo, userToken)
	if err != nil {
		t.Fatal(err)
	}
	if statusCode != http.StatusOK || resp.Code != 0 {
		t.Fatalf("get ticket status=%d code=%d msg=%s", statusCode, resp.Code, resp.Message)
	}

	reply := map[string]string{"content": "Additional information from API test"}
	resp, statusCode, err = doPost(baseURL+"/api/v1/support/tickets/"+supportID(created.ID)+"/replies", reply, userToken)
	if err != nil {
		t.Fatal(err)
	}
	if statusCode != http.StatusOK || resp.Code != 0 {
		t.Fatalf("reply ticket status=%d code=%d msg=%s", statusCode, resp.Code, resp.Message)
	}

	resp, statusCode, err = doPost(baseURL+"/api/v1/support/tickets/"+supportID(created.ID)+"/resolve", nil, userToken)
	if err != nil {
		t.Fatal(err)
	}
	if statusCode != http.StatusOK || resp.Code != 0 {
		t.Fatalf("resolve ticket status=%d code=%d msg=%s", statusCode, resp.Code, resp.Message)
	}

	resp, statusCode, err = doPost(baseURL+"/api/v1/support/tickets/"+supportID(created.ID)+"/reopen", nil, userToken)
	if err != nil {
		t.Fatal(err)
	}
	if statusCode != http.StatusOK || resp.Code != 0 {
		t.Fatalf("reopen ticket status=%d code=%d msg=%s", statusCode, resp.Code, resp.Message)
	}
}

func supportID(v uint) string {
	return strconv.FormatUint(uint64(v), 10)
}
