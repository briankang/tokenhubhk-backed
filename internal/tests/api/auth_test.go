package api_test

import (
	"encoding/json"
	"net/http"
	"testing"
)

func TestRegister_Success(t *testing.T) {
	email := uniqueEmail("reg_ok")
	resp, statusCode, err := doPost(baseURL+"/api/v1/auth/register", map[string]string{
		"email":    email,
		"password": "Test@123456",
		"name":     "RegTestUser",
	}, "")
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	skipIfNotImplemented(t, statusCode)

	if statusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", statusCode, resp.Message)
	}
	if resp.Code != 0 {
		t.Fatalf("expected code 0, got %d: %s", resp.Code, resp.Message)
	}

	var data struct {
		ID    uint   `json:"id"`
		Email string `json:"email"`
	}
	if err := json.Unmarshal(resp.Data, &data); err != nil {
		t.Fatalf("parse data: %v", err)
	}
	if data.Email != email {
		t.Errorf("expected email %s, got %s", email, data.Email)
	}
}

func TestRegister_DuplicateEmail(t *testing.T) {
	email := uniqueEmail("reg_dup")

	// First registration
	_, statusCode, err := doPost(baseURL+"/api/v1/auth/register", map[string]string{
		"email":    email,
		"password": "Test@123456",
		"name":     "DupUser",
	}, "")
	if err != nil {
		t.Fatalf("first register failed: %v", err)
	}
	skipIfNotImplemented(t, statusCode)

	// Second registration with same email
	resp, statusCode2, err := doPost(baseURL+"/api/v1/auth/register", map[string]string{
		"email":    email,
		"password": "Test@123456",
		"name":     "DupUser2",
	}, "")
	if err != nil {
		t.Fatalf("second register failed: %v", err)
	}

	if statusCode2 == http.StatusOK && resp.Code == 0 {
		t.Error("expected duplicate email to be rejected, but registration succeeded")
	}
}

func TestRegister_InvalidEmail(t *testing.T) {
	resp, statusCode, err := doPost(baseURL+"/api/v1/auth/register", map[string]string{
		"email":    "not-an-email",
		"password": "Test@123456",
		"name":     "BadEmail",
	}, "")
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	skipIfNotImplemented(t, statusCode)

	if statusCode == http.StatusOK && resp.Code == 0 {
		t.Error("expected invalid email to be rejected, but registration succeeded")
	}
}

func TestLogin_Success(t *testing.T) {
	email := uniqueEmail("login_ok")

	// Register first
	_, statusCode, err := doPost(baseURL+"/api/v1/auth/register", map[string]string{
		"email":    email,
		"password": "Test@123456",
		"name":     "LoginUser",
	}, "")
	if err != nil {
		t.Fatalf("register failed: %v", err)
	}
	skipIfNotImplemented(t, statusCode)

	// Login
	resp, statusCode, err := doPost(baseURL+"/api/v1/auth/login", map[string]string{
		"email":    email,
		"password": "Test@123456",
	}, "")
	if err != nil {
		t.Fatalf("login request failed: %v", err)
	}
	skipIfNotImplemented(t, statusCode)

	if statusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", statusCode, resp.Message)
	}

	var lr loginResponse
	if err := json.Unmarshal(resp.Data, &lr); err != nil {
		t.Fatalf("parse login response: %v", err)
	}
	if lr.AccessToken == "" {
		t.Error("expected non-empty access token")
	}
}

func TestLogin_WrongPassword(t *testing.T) {
	email := uniqueEmail("login_wp")

	// Register
	_, statusCode, _ := doPost(baseURL+"/api/v1/auth/register", map[string]string{
		"email":    email,
		"password": "Test@123456",
		"name":     "WrongPwdUser",
	}, "")
	skipIfNotImplemented(t, statusCode)

	// Login with wrong password
	resp, statusCode2, err := doPost(baseURL+"/api/v1/auth/login", map[string]string{
		"email":    email,
		"password": "WrongPassword!",
	}, "")
	if err != nil {
		t.Fatalf("login request failed: %v", err)
	}
	skipIfNotImplemented(t, statusCode2)

	if statusCode2 == http.StatusOK && resp.Code == 0 {
		t.Error("expected wrong password to fail, but login succeeded")
	}
}

func TestLogin_NonExistentUser(t *testing.T) {
	resp, statusCode, err := doPost(baseURL+"/api/v1/auth/login", map[string]string{
		"email":    "nonexistent_user_xyz@test.com",
		"password": "Test@123456",
	}, "")
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	skipIfNotImplemented(t, statusCode)

	if statusCode == http.StatusOK && resp.Code == 0 {
		t.Error("expected non-existent user login to fail, but it succeeded")
	}
}

func TestRefreshToken_Success(t *testing.T) {
	email := uniqueEmail("refresh_ok")

	// Register + Login
	_, statusCode, _ := doPost(baseURL+"/api/v1/auth/register", map[string]string{
		"email":    email,
		"password": "Test@123456",
		"name":     "RefreshUser",
	}, "")
	skipIfNotImplemented(t, statusCode)

	loginResp, statusCode, err := doPost(baseURL+"/api/v1/auth/login", map[string]string{
		"email":    email,
		"password": "Test@123456",
	}, "")
	if err != nil {
		t.Fatalf("login failed: %v", err)
	}
	skipIfNotImplemented(t, statusCode)

	var lr loginResponse
	if err := json.Unmarshal(loginResp.Data, &lr); err != nil {
		t.Fatalf("parse login: %v", err)
	}
	if lr.RefreshToken == "" {
		t.Skip("no refresh token returned")
	}

	// Refresh
	resp, statusCode, err := doPost(baseURL+"/api/v1/auth/refresh", map[string]string{
		"refresh_token": lr.RefreshToken,
	}, "")
	if err != nil {
		t.Fatalf("refresh request failed: %v", err)
	}
	skipIfNotImplemented(t, statusCode)

	if statusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", statusCode, resp.Message)
	}

	var newLr loginResponse
	if err := json.Unmarshal(resp.Data, &newLr); err != nil {
		t.Fatalf("parse refresh response: %v", err)
	}
	if newLr.AccessToken == "" {
		t.Error("expected non-empty access token after refresh")
	}
}

func TestLogout_Success(t *testing.T) {
	email := uniqueEmail("logout_ok")

	// Register + Login
	_, statusCode, _ := doPost(baseURL+"/api/v1/auth/register", map[string]string{
		"email":    email,
		"password": "Test@123456",
		"name":     "LogoutUser",
	}, "")
	skipIfNotImplemented(t, statusCode)

	loginResp, statusCode, err := doPost(baseURL+"/api/v1/auth/login", map[string]string{
		"email":    email,
		"password": "Test@123456",
	}, "")
	if err != nil {
		t.Fatalf("login failed: %v", err)
	}
	skipIfNotImplemented(t, statusCode)

	var lr loginResponse
	if err := json.Unmarshal(loginResp.Data, &lr); err != nil {
		t.Fatalf("parse login: %v", err)
	}

	// Logout - try the authenticated logout endpoint
	resp, statusCode, err := doPost(baseURL+"/api/v1/auth/logout", nil, lr.AccessToken)
	if err != nil {
		t.Fatalf("logout request failed: %v", err)
	}
	skipIfNotImplemented(t, statusCode)
	skipIfNotFound(t, statusCode)

	if statusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", statusCode, resp.Message)
	}
}

func TestProtectedRoute_NoToken(t *testing.T) {
	// Access a protected route without token
	_, statusCode, err := doGet(baseURL+"/api/v1/user/profile", "")
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	skipIfNotImplemented(t, statusCode)

	if statusCode != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", statusCode)
	}
}

func TestProtectedRoute_InvalidToken(t *testing.T) {
	// Access a protected route with an invalid token
	_, statusCode, err := doGet(baseURL+"/api/v1/user/profile", "invalid.jwt.token")
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	skipIfNotImplemented(t, statusCode)

	if statusCode != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", statusCode)
	}
}
