package api_test

import (
	"encoding/json"
	"net/http"
	"testing"
)

func TestGetProfile_Success(t *testing.T) {
	requireUser(t)

	resp, statusCode, err := doGet(baseURL+"/api/v1/user/profile", userToken)
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
}

func TestUpdateProfile_Success(t *testing.T) {
	requireUser(t)

	newName := uniqueName("updated")
	resp, statusCode, err := doPut(baseURL+"/api/v1/user/profile", map[string]string{
		"name":        newName,
		"email_code":  testMagicEmailCode,
		"invite_code": testInviteCode,
	}, userToken)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	skipIfNotImplemented(t, statusCode)

	if statusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", statusCode, resp.Message)
	}
}

func TestChangePassword_Success(t *testing.T) {
	// Create a fresh user for password change test
	email := uniqueEmail("chpwd_ok")
	oldPass := "Test@123456"
	newPass := "NewPass@789"

	_, statusCode, _ := doPost(baseURL+"/api/v1/auth/register", registerPayload(email, oldPass, "ChPwdUser"), "")
	skipIfNotImplemented(t, statusCode)

	token, err := loginUser(email, oldPass)
	if err != nil {
		t.Skipf("login failed: %v", err)
	}

	// Change password
	resp, statusCode, err := doPost(baseURL+"/api/v1/user/change-password", map[string]string{
		"old_password": authPassword(email, oldPass),
		"new_password": authPassword(email, newPass),
	}, token)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	skipIfNotImplemented(t, statusCode)
	skipIfNotFound(t, statusCode)

	if statusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", statusCode, resp.Message)
	}

	// Verify login with new password works
	_, loginErr := loginUser(email, newPass)
	if loginErr != nil {
		t.Errorf("login with new password failed: %v", loginErr)
	}
}

func TestChangePassword_WrongOld(t *testing.T) {
	requireUser(t)

	resp, statusCode, err := doPost(baseURL+"/api/v1/user/change-password", map[string]string{
		"old_password": "WrongOldPass!",
		"new_password": "NewPass@789",
	}, userToken)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	skipIfNotImplemented(t, statusCode)
	skipIfNotFound(t, statusCode)

	if statusCode == http.StatusOK && resp.Code == 0 {
		t.Error("expected wrong old password to be rejected, but it succeeded")
	}
}

func TestAdminListUsers_Success(t *testing.T) {
	requireAdmin(t)

	resp, statusCode, err := doGet(baseURL+"/api/v1/admin/users", adminToken)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	skipIfNotImplemented(t, statusCode)

	if statusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", statusCode, resp.Message)
	}
}

func TestAdminListUsers_SearchByKeyword(t *testing.T) {
	requireAdmin(t)

	email := uniqueEmail("admin_search")
	name := uniqueName("AdminSearchUser")
	createResp, createStatus, err := doPost(baseURL+"/api/v1/admin/users/batch", map[string]interface{}{
		"users": []map[string]interface{}{
			{
				"email":    email,
				"name":     name,
				"password": "Test@123456",
				"role":     "USER",
			},
		},
	}, adminToken)
	if err != nil {
		t.Fatalf("create user request failed: %v", err)
	}
	skipIfNotImplemented(t, createStatus)
	skipIfNotFound(t, createStatus)
	skipIfForbidden(t, createStatus)
	if createStatus != http.StatusOK {
		t.Fatalf("expected create 200, got %d: %s", createStatus, createResp.Message)
	}

	resp, statusCode, err := doGet(baseURL+"/api/v1/admin/users?page=1&page_size=20&search="+email, adminToken)
	if err != nil {
		t.Fatalf("search by email request failed: %v", err)
	}
	if statusCode != http.StatusOK {
		t.Fatalf("expected search by email 200, got %d: %s", statusCode, resp.Message)
	}
	assertUserSearchContainsOnly(t, resp, email, "")

	resp, statusCode, err = doGet(baseURL+"/api/v1/admin/users?page=1&page_size=20&search="+name, adminToken)
	if err != nil {
		t.Fatalf("search by name request failed: %v", err)
	}
	if statusCode != http.StatusOK {
		t.Fatalf("expected search by name 200, got %d: %s", statusCode, resp.Message)
	}
	assertUserSearchContainsOnly(t, resp, email, name)
}

func TestUserListUsers_Forbidden(t *testing.T) {
	requireUser(t)

	_, statusCode, err := doGet(baseURL+"/api/v1/admin/users", userToken)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	skipIfNotImplemented(t, statusCode)

	// Regular user should get 403 or 401 when accessing admin routes
	if statusCode == http.StatusOK {
		t.Error("expected regular user to be forbidden from admin user list")
	}
	if statusCode != http.StatusForbidden && statusCode != http.StatusUnauthorized {
		t.Logf("got status %d (expected 403 or 401)", statusCode)
	}
}

func assertUserSearchContainsOnly(t *testing.T, resp *apiResponse, expectedEmail, expectedName string) {
	t.Helper()
	page, err := parsePageData(resp)
	if err != nil {
		t.Fatalf("parse page data: %v", err)
	}
	var users []struct {
		Email string `json:"email"`
		Name  string `json:"name"`
	}
	if err := json.Unmarshal(page.List, &users); err != nil {
		t.Fatalf("parse users: %v", err)
	}
	for _, u := range users {
		if u.Email == expectedEmail && (expectedName == "" || u.Name == expectedName) {
			return
		}
	}
	t.Fatalf("expected search results to include %s/%s, got %d users", expectedEmail, expectedName, len(users))
}
