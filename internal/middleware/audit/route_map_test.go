package audit

import (
	"strings"
	"testing"
)

// TestRouteMap_NoKeyCollision 写操作表和读操作表不能有重复键
func TestRouteMap_NoKeyCollision(t *testing.T) {
	for key := range readRouteMap {
		if _, dup := routeMap[key]; dup {
			t.Errorf("key %q exists in both routeMap and readRouteMap", key)
		}
	}
}

// TestRouteMap_DuplicateActionWarning 共享 action 是有意的（两条路径共用一个权限），
// seed.go 会 dedup；此测试仅打印警告便于审核。
// 跨 routeMap ↔ readRouteMap 共享 action 会失败（读写权限语义必须分离）。
func TestRouteMap_DuplicateActionWarning(t *testing.T) {
	writeActions := make(map[string]string)
	for key, meta := range routeMap {
		if prev, dup := writeActions[meta.Action]; dup {
			t.Logf("info: write action %q shared by %q and %q (seed will dedup)", meta.Action, key, prev)
		}
		writeActions[meta.Action] = key
	}
	readActions := make(map[string]string)
	for key, meta := range readRouteMap {
		if prev, dup := readActions[meta.Action]; dup {
			t.Logf("info: read action %q shared by %q and %q", meta.Action, key, prev)
		}
		readActions[meta.Action] = key
	}
	// 跨读写表共享 action 是 bug（语义混淆：同一权限码既是读又是写）
	for code, readKey := range readActions {
		if writeKey, cross := writeActions[code]; cross {
			t.Errorf("action %q appears in BOTH writeMap (%q) and readMap (%q)", code, writeKey, readKey)
		}
	}
}

// TestRouteMap_AllMetaFieldsPresent 每条 RouteMeta 必须有 Menu/Feature/Action
func TestRouteMap_AllMetaFieldsPresent(t *testing.T) {
	check := func(name string, m map[string]RouteMeta) {
		for key, meta := range m {
			if meta.Menu == "" {
				t.Errorf("%s[%q] has empty Menu", name, key)
			}
			if meta.Feature == "" {
				t.Errorf("%s[%q] has empty Feature", name, key)
			}
			if meta.Action == "" {
				t.Errorf("%s[%q] has empty Action", name, key)
			}
		}
	}
	check("routeMap", routeMap)
	check("readRouteMap", readRouteMap)
}

// TestRouteMap_WriteMapOnlyWrites routeMap 只能有 POST/PUT/PATCH/DELETE
func TestRouteMap_WriteMapOnlyWrites(t *testing.T) {
	for key := range routeMap {
		method := strings.SplitN(key, " ", 2)[0]
		if method == "GET" || method == "HEAD" || method == "OPTIONS" {
			t.Errorf("routeMap contains read method %q at %q (should be in readRouteMap)", method, key)
		}
	}
}

// TestRouteMap_ReadMapOnlyGet readRouteMap 必须全部是 GET
func TestRouteMap_ReadMapOnlyGet(t *testing.T) {
	for key := range readRouteMap {
		method := strings.SplitN(key, " ", 2)[0]
		if method != "GET" {
			t.Errorf("readRouteMap contains non-GET method %q at %q (should be in routeMap)", method, key)
		}
	}
}

// TestRouteMap_ReadActionsSuffixed 读权限 action 建议以 _read 结尾（不强制但警告）
func TestRouteMap_ReadActionsSuffixed(t *testing.T) {
	for key, meta := range readRouteMap {
		if !strings.HasSuffix(meta.Action, "_read") {
			t.Logf("warn: read action %q at %q does not end with _read", meta.Action, key)
		}
	}
}

// TestLookup_HitsBothMaps Lookup 应能查到 write + read 两张表
func TestLookup_HitsBothMaps(t *testing.T) {
	// 取 routeMap 第一个键作为写操作样本
	for key, wantMeta := range routeMap {
		parts := strings.SplitN(key, " ", 2)
		got, ok := Lookup(parts[0], parts[1])
		if !ok {
			t.Errorf("Lookup(%q, %q) = !ok, want ok", parts[0], parts[1])
		}
		if got.Action != wantMeta.Action {
			t.Errorf("Lookup action mismatch: got %q want %q", got.Action, wantMeta.Action)
		}
		break
	}
	// 取 readRouteMap 第一个键作为读操作样本
	for key, wantMeta := range readRouteMap {
		parts := strings.SplitN(key, " ", 2)
		got, ok := Lookup(parts[0], parts[1])
		if !ok {
			t.Errorf("Lookup(%q, %q) = !ok, want ok", parts[0], parts[1])
		}
		if got.Action != wantMeta.Action {
			t.Errorf("Lookup action mismatch: got %q want %q", got.Action, wantMeta.Action)
		}
		break
	}
}

// TestIsAuditRelevant 验证审计判定
func TestIsAuditRelevant(t *testing.T) {
	// 写操作必须审计
	for key := range routeMap {
		parts := strings.SplitN(key, " ", 2)
		if !IsAuditRelevant(parts[0], parts[1]) {
			t.Errorf("write route %q should be audit-relevant", key)
		}
		break
	}
	// 读操作不审计
	for key := range readRouteMap {
		parts := strings.SplitN(key, " ", 2)
		if IsAuditRelevant(parts[0], parts[1]) {
			t.Errorf("read route %q should NOT be audit-relevant", key)
		}
		break
	}
	// 未映射路径不审计
	if IsAuditRelevant("GET", "/unknown/path") {
		t.Error("unknown path should not be audit-relevant")
	}
}

// TestRouteMapEntries_Completeness 返回的条目数等于两表之和
func TestRouteMapEntries_Completeness(t *testing.T) {
	entries := RouteMapEntries()
	want := len(routeMap) + len(readRouteMap)
	if len(entries) != want {
		t.Errorf("RouteMapEntries len = %d, want %d", len(entries), want)
	}
	var reads, writes int
	for _, e := range entries {
		if e.IsRead {
			reads++
		} else {
			writes++
		}
	}
	if writes != len(routeMap) {
		t.Errorf("writes = %d, want %d", writes, len(routeMap))
	}
	if reads != len(readRouteMap) {
		t.Errorf("reads = %d, want %d", reads, len(readRouteMap))
	}
}

// TestSplitKey 内部辅助函数
func TestSplitKey(t *testing.T) {
	method, path := splitKey("GET /api/v1/admin/users")
	if method != "GET" || path != "/api/v1/admin/users" {
		t.Errorf("splitKey got (%q, %q)", method, path)
	}
	method, path = splitKey("no-space")
	if method != "no-space" || path != "" {
		t.Errorf("splitKey fallback got (%q, %q)", method, path)
	}
}
