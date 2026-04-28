package permission

import (
	"encoding/json"
	"reflect"
	"sort"
	"testing"

	"tokenhub-server/internal/model"
)

func mkScope(t string, tenants ...uint) model.JSON {
	m := map[string]interface{}{"type": t}
	if len(tenants) > 0 {
		m["tenant_ids"] = tenants
	}
	b, _ := json.Marshal(m)
	return b
}

// TestMergeDataScopes_EmptyRoles 无角色 → own_only
func TestMergeDataScopes_EmptyRoles(t *testing.T) {
	got := mergeDataScopes(nil, 5)
	if got.Type != DataScopeOwnOnly {
		t.Errorf("empty roles should yield own_only, got %q", got.Type)
	}
}

// TestMergeDataScopes_AnyAllWins all 最宽，短路
func TestMergeDataScopes_AnyAllWins(t *testing.T) {
	roles := []roleRow{
		{ID: 1, Code: "FINANCE_MANAGER", DataScope: mkScope(DataScopeAll)},
		{ID: 2, Code: "custom", DataScope: mkScope(DataScopeCustomTenants, 5, 7)},
	}
	got := mergeDataScopes(roles, 3)
	if got.Type != DataScopeAll {
		t.Errorf("any all should yield all, got %q", got.Type)
	}
	if len(got.TenantIDs) != 0 {
		t.Errorf("all scope should have no tenant_ids, got %v", got.TenantIDs)
	}
}

// TestMergeDataScopes_OnlyOwnOnly 仅 own_only → own_only
func TestMergeDataScopes_OnlyOwnOnly(t *testing.T) {
	roles := []roleRow{
		{ID: 1, Code: "USER", DataScope: mkScope(DataScopeOwnOnly)},
	}
	got := mergeDataScopes(roles, 3)
	if got.Type != DataScopeOwnOnly {
		t.Errorf("only own_only should yield own_only, got %q", got.Type)
	}
}

// TestMergeDataScopes_OnlyOwnTenant 仅 own_tenant → own_tenant
func TestMergeDataScopes_OnlyOwnTenant(t *testing.T) {
	roles := []roleRow{
		{ID: 1, Code: "ops", DataScope: mkScope(DataScopeOwnTenant)},
	}
	got := mergeDataScopes(roles, 3)
	if got.Type != DataScopeOwnTenant {
		t.Errorf("only own_tenant should yield own_tenant, got %q", got.Type)
	}
}

// TestMergeDataScopes_CustomTenantsUnion 多个 custom_tenants → 并集
func TestMergeDataScopes_CustomTenantsUnion(t *testing.T) {
	roles := []roleRow{
		{ID: 1, Code: "r1", DataScope: mkScope(DataScopeCustomTenants, 5, 7)},
		{ID: 2, Code: "r2", DataScope: mkScope(DataScopeCustomTenants, 7, 9)},
	}
	got := mergeDataScopes(roles, 3)
	if got.Type != DataScopeCustomTenants {
		t.Errorf("want custom_tenants, got %q", got.Type)
	}
	want := []uint{5, 7, 9}
	if !reflect.DeepEqual(got.TenantIDs, want) {
		t.Errorf("tenant_ids = %v, want %v", got.TenantIDs, want)
	}
}

// TestMergeDataScopes_OwnTenantAndCustom own_tenant + custom_tenants → custom_tenants + 用户租户 ID 并入
func TestMergeDataScopes_OwnTenantAndCustom(t *testing.T) {
	roles := []roleRow{
		{ID: 1, Code: "r1", DataScope: mkScope(DataScopeOwnTenant)},
		{ID: 2, Code: "r2", DataScope: mkScope(DataScopeCustomTenants, 5, 7)},
	}
	got := mergeDataScopes(roles, 3)
	if got.Type != DataScopeCustomTenants {
		t.Errorf("want custom_tenants, got %q", got.Type)
	}
	want := []uint{3, 5, 7}
	sort.Slice(got.TenantIDs, func(i, j int) bool { return got.TenantIDs[i] < got.TenantIDs[j] })
	if !reflect.DeepEqual(got.TenantIDs, want) {
		t.Errorf("tenant_ids = %v, want %v (user_tenant_id=3 should be merged in)", got.TenantIDs, want)
	}
}

// TestMergeDataScopes_OwnTenantPlusOwnOnly own_tenant + own_only → own_tenant (宽者胜)
func TestMergeDataScopes_OwnTenantPlusOwnOnly(t *testing.T) {
	roles := []roleRow{
		{ID: 1, Code: "r1", DataScope: mkScope(DataScopeOwnOnly)},
		{ID: 2, Code: "r2", DataScope: mkScope(DataScopeOwnTenant)},
	}
	got := mergeDataScopes(roles, 3)
	if got.Type != DataScopeOwnTenant {
		t.Errorf("want own_tenant (wider), got %q", got.Type)
	}
}

// TestMergeDataScopes_BadJSON 损坏的 JSON 不应 panic，应当被跳过
func TestMergeDataScopes_BadJSON(t *testing.T) {
	roles := []roleRow{
		{ID: 1, Code: "broken", DataScope: []byte(`{garbage}`)},
		{ID: 2, Code: "USER", DataScope: mkScope(DataScopeOwnOnly)},
	}
	got := mergeDataScopes(roles, 3)
	// 损坏的 scope 被跳过，剩 own_only
	if got.Type != DataScopeOwnOnly {
		t.Errorf("bad json should be skipped, fallback own_only, got %q", got.Type)
	}
}

// TestMergeDataScopes_EmptyScope nil DataScope 应当被跳过
func TestMergeDataScopes_EmptyScope(t *testing.T) {
	roles := []roleRow{
		{ID: 1, Code: "empty", DataScope: nil},
	}
	got := mergeDataScopes(roles, 3)
	if got.Type != DataScopeOwnOnly {
		t.Errorf("empty scope should fallback to own_only, got %q", got.Type)
	}
}

// TestSubjectPerms_Has 权限判定基础路径
func TestSubjectPerms_Has(t *testing.T) {
	s := &SubjectPerms{
		Codes: []string{"user_update", "refund_approve"},
	}
	if !s.Has("user_update") {
		t.Error("should have user_update")
	}
	if !s.HasAny("foo", "refund_approve") {
		t.Error("HasAny should return true")
	}
	if s.Has("nonexistent") {
		t.Error("should not have nonexistent")
	}
}

// TestSubjectPerms_Nil nil 安全
func TestSubjectPerms_Nil(t *testing.T) {
	var s *SubjectPerms
	if s.Has("x") {
		t.Error("nil SubjectPerms.Has should return false")
	}
	if s.HasAny("x", "y") {
		t.Error("nil SubjectPerms.HasAny should return false")
	}
	if s.IsSuperAdmin() {
		t.Error("nil SubjectPerms.IsSuperAdmin should return false")
	}
}

// TestSubjectPerms_IsSuperAdmin 超管判定
func TestSubjectPerms_IsSuperAdmin(t *testing.T) {
	if !(&SubjectPerms{RoleCodes: []string{"SUPER_ADMIN"}}).IsSuperAdmin() {
		t.Error("SUPER_ADMIN role should match")
	}
	if !(&SubjectPerms{RoleCodes: []string{"ADMIN"}}).IsSuperAdmin() {
		t.Error("legacy ADMIN role should be treated as SUPER_ADMIN")
	}
	if (&SubjectPerms{RoleCodes: []string{"FINANCE_MANAGER", "AUDITOR"}}).IsSuperAdmin() {
		t.Error("non-SUPER_ADMIN roles should not match")
	}
	if (&SubjectPerms{RoleCodes: nil}).IsSuperAdmin() {
		t.Error("empty roles should not match")
	}
}

// TestSortUintSet 辅助函数正确排序
func TestSortUintSet(t *testing.T) {
	got := sortUintSet(map[uint]struct{}{5: {}, 1: {}, 3: {}})
	want := []uint{1, 3, 5}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

// TestCacheKey 缓存键格式稳定
func TestCacheKey(t *testing.T) {
	if got := cacheKey(42); got != "user_perms:42" {
		t.Errorf("cacheKey(42) = %q, want %q", got, "user_perms:42")
	}
}
