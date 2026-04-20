package permission

import (
	"testing"

	"tokenhub-server/internal/model"
)

// TestBuiltinRoles_AllHaveCodeAndName 验证内置角色定义完整性
func TestBuiltinRoles_AllHaveCodeAndName(t *testing.T) {
	if len(BuiltinRoles) == 0 {
		t.Fatal("BuiltinRoles is empty")
	}
	seen := make(map[string]struct{})
	for _, r := range BuiltinRoles {
		if r.Code == "" {
			t.Errorf("role has empty Code: %+v", r)
		}
		if r.Name == "" {
			t.Errorf("role %s has empty Name", r.Code)
		}
		if r.DataScope == "" {
			t.Errorf("role %s has empty DataScope", r.Code)
		}
		if _, dup := seen[r.Code]; dup {
			t.Errorf("duplicate role code: %s", r.Code)
		}
		seen[r.Code] = struct{}{}
	}
}

// TestBuiltinRoles_RequiredCodesPresent 验证关键角色齐全
func TestBuiltinRoles_RequiredCodesPresent(t *testing.T) {
	required := []string{
		"SUPER_ADMIN",
		"FINANCE_MANAGER",
		"OPERATION_MANAGER",
		"AI_RESOURCE_MANAGER",
		"AUDITOR",
		"USER",
	}
	present := make(map[string]bool)
	for _, r := range BuiltinRoles {
		present[r.Code] = true
	}
	for _, code := range required {
		if !present[code] {
			t.Errorf("required built-in role missing: %s", code)
		}
	}
}

// TestBuiltinRoles_SuperAdminHasAllPermissions 验证超管角色配置正确
func TestBuiltinRoles_SuperAdminHasAllPermissions(t *testing.T) {
	for _, r := range BuiltinRoles {
		if r.Code != "SUPER_ADMIN" {
			continue
		}
		if !r.AllPermissions {
			t.Error("SUPER_ADMIN.AllPermissions should be true")
		}
		if r.DataScope != "all" {
			t.Errorf("SUPER_ADMIN.DataScope = %q, want \"all\"", r.DataScope)
		}
		return
	}
	t.Fatal("SUPER_ADMIN not found")
}

// TestBuiltinRoles_UserIsOwnOnly 验证普通用户数据范围为仅自己
func TestBuiltinRoles_UserIsOwnOnly(t *testing.T) {
	for _, r := range BuiltinRoles {
		if r.Code != "USER" {
			continue
		}
		if r.DataScope != "own_only" {
			t.Errorf("USER.DataScope = %q, want \"own_only\"", r.DataScope)
		}
		if r.AllPermissions {
			t.Error("USER.AllPermissions should be false")
		}
		if len(r.ExtraCodes) == 0 {
			t.Error("USER.ExtraCodes should not be empty")
		}
		return
	}
	t.Fatal("USER not found")
}

// TestLegacyRoleMapping_CoversAllKnownRoles 验证遗留角色字符串映射完整
func TestLegacyRoleMapping_CoversAllKnownRoles(t *testing.T) {
	required := []string{"ADMIN", "USER", "AGENT_L1", "AGENT_L2", "AGENT_L3"}
	for _, legacy := range required {
		if _, ok := LegacyRoleMapping[legacy]; !ok {
			t.Errorf("legacy role %q missing from LegacyRoleMapping", legacy)
		}
	}
	if LegacyRoleMapping["ADMIN"] != "SUPER_ADMIN" {
		t.Errorf("ADMIN should map to SUPER_ADMIN, got %q", LegacyRoleMapping["ADMIN"])
	}
}

// TestIsBuiltinRoleCode 验证内置角色判定
func TestIsBuiltinRoleCode(t *testing.T) {
	tests := []struct {
		code string
		want bool
	}{
		{"SUPER_ADMIN", true},
		{"USER", true},
		{"FINANCE_MANAGER", true},
		{"custom_xxx", false},
		{"", false},
		{"ADMIN", false}, // 旧角色码不是新角色码
	}
	for _, tt := range tests {
		if got := IsBuiltinRoleCode(tt.code); got != tt.want {
			t.Errorf("IsBuiltinRoleCode(%q) = %v, want %v", tt.code, got, tt.want)
		}
	}
}

// TestCollectPermissionIDs_AllPermissions 验证超管角色展开为全部权限
func TestCollectPermissionIDs_AllPermissions(t *testing.T) {
	perms := map[string]*model.Permission{
		"user_update":   {BaseModel: model.BaseModel{ID: 1}, Code: "user_update", Menu: "用户管理", IsRead: false},
		"user_list":     {BaseModel: model.BaseModel{ID: 2}, Code: "user_list", Menu: "用户管理", IsRead: true},
		"refund_approve": {BaseModel: model.BaseModel{ID: 3}, Code: "refund_approve", Menu: "退款管理", IsRead: false},
	}
	br := BuiltinRole{Code: "SUPER_ADMIN", AllPermissions: true}
	got := collectPermissionIDs(br, perms)
	if len(got) != 3 {
		t.Errorf("AllPermissions should yield 3 ids, got %d", len(got))
	}
}

// TestCollectPermissionIDs_AllReadOnly 验证只读角色仅展开 is_read=true 权限
func TestCollectPermissionIDs_AllReadOnly(t *testing.T) {
	perms := map[string]*model.Permission{
		"user_update": {BaseModel: model.BaseModel{ID: 1}, Code: "user_update", IsRead: false},
		"user_list":   {BaseModel: model.BaseModel{ID: 2}, Code: "user_list", IsRead: true},
		"refund_list": {BaseModel: model.BaseModel{ID: 3}, Code: "refund_list", IsRead: true},
	}
	br := BuiltinRole{Code: "AUDITOR", AllReadOnly: true}
	got := collectPermissionIDs(br, perms)
	if len(got) != 2 {
		t.Errorf("AllReadOnly should yield 2 ids (is_read=true only), got %d", len(got))
	}
	// 验证返回的都是 read 权限
	gotSet := make(map[uint]struct{})
	for _, id := range got {
		gotSet[id] = struct{}{}
	}
	if _, has := gotSet[1]; has {
		t.Error("AllReadOnly should not include write permission user_update")
	}
}

// TestCollectPermissionIDs_MenuAndExtraCodes 验证菜单 ∪ 额外码组合
func TestCollectPermissionIDs_MenuAndExtraCodes(t *testing.T) {
	perms := map[string]*model.Permission{
		"user_update":   {BaseModel: model.BaseModel{ID: 1}, Code: "user_update", Menu: "用户管理"},
		"user_delete":   {BaseModel: model.BaseModel{ID: 2}, Code: "user_delete", Menu: "用户管理"},
		"refund_approve": {BaseModel: model.BaseModel{ID: 3}, Code: "refund_approve", Menu: "退款管理"},
		"apikey_create": {BaseModel: model.BaseModel{ID: 4}, Code: "apikey_create", Menu: "API Keys"},
	}
	br := BuiltinRole{
		Menus:      []string{"用户管理"},
		ExtraCodes: []string{"apikey_create"},
	}
	got := collectPermissionIDs(br, perms)
	if len(got) != 3 {
		t.Errorf("Menu+Extra should yield 3 ids (2 user_* + 1 apikey), got %d", len(got))
	}
	gotSet := make(map[uint]struct{})
	for _, id := range got {
		gotSet[id] = struct{}{}
	}
	if _, has := gotSet[3]; has {
		t.Error("refund_approve should not be included (different menu, not in ExtraCodes)")
	}
}

// TestCollectPermissionIDs_EmptyResult 验证无匹配菜单时返回空
func TestCollectPermissionIDs_EmptyResult(t *testing.T) {
	perms := map[string]*model.Permission{
		"user_update": {BaseModel: model.BaseModel{ID: 1}, Code: "user_update", Menu: "用户管理"},
	}
	br := BuiltinRole{Menus: []string{"不存在的菜单"}}
	got := collectPermissionIDs(br, perms)
	if len(got) != 0 {
		t.Errorf("unmatched menu should yield 0 ids, got %d", len(got))
	}
}
