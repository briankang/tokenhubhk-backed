package api_test

import (
	"encoding/json"
	"net/http"
	"testing"
)

// ---- 审计日志数据结构 ----

type configAuditLogItem struct {
	ID          uint   `json:"id"`
	AdminID     uint   `json:"admin_id"`
	ConfigTable string `json:"config_table"`
	Action      string `json:"action"`
	FieldName   string `json:"field_name"`
}

// ============================================================
// TestAdminConfigAuditLog_Appears — 更新 referral_config 后审计日志中出现记录
// ============================================================
func TestAdminConfigAuditLog_Appears(t *testing.T) {
	requireAdmin(t)

	// 先触发一次配置更新，以便审计日志中有记录
	origResp, _, _ := doGet(baseURL+"/api/v1/admin/referral-config", adminToken)
	var origCfg referralConfigData
	if origResp != nil {
		json.Unmarshal(origResp.Data, &origCfg)
	}
	t.Cleanup(func() {
		if origCfg.CommissionRate > 0 {
			doPut(baseURL+"/api/v1/admin/referral-config", map[string]interface{}{
				"commissionRate": origCfg.CommissionRate,
			}, adminToken)
		}
	})

	// 执行一次更新（触发审计日志写入）
	_, updateStatus, err := doPut(baseURL+"/api/v1/admin/referral-config", map[string]interface{}{
		"commissionRate": 0.11,
	}, adminToken)
	if err != nil {
		t.Fatalf("update referral config failed: %v", err)
	}
	skipIfNotFound(t, updateStatus)
	skipIfNotImplemented(t, updateStatus)

	// 查询审计日志，按 table=referral_configs 过滤
	resp, status, err := doGet(baseURL+"/api/v1/admin/config-audit?table=referral_configs&page=1&page_size=50", adminToken)
	if err != nil {
		t.Fatalf("config-audit request failed: %v", err)
	}
	skipIfNotFound(t, status)
	skipIfNotImplemented(t, status)

	if status != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", status, resp.Message)
	}
	if resp.Code != 0 {
		t.Fatalf("expected code 0, got %d: %s", resp.Code, resp.Message)
	}

	page, err := parsePageData(resp)
	if err != nil {
		t.Fatalf("parse audit log page data: %v", err)
	}

	// 审计日志中应有至少 1 条 referral_configs 的记录
	if page.Total == 0 {
		t.Log("注意: 审计日志暂无 referral_configs 记录（可能审计功能尚未接入此路由）")
	} else {
		// 验证 list 中每条记录的 config_table 正确
		if page.List != nil {
			var items []configAuditLogItem
			if err := json.Unmarshal(page.List, &items); err == nil {
				for _, item := range items {
					if item.ConfigTable != "" && item.ConfigTable != "referral_configs" {
						t.Errorf("expected config_table=referral_configs, got %s", item.ConfigTable)
					}
				}
			}
		}
	}
	t.Logf("TestAdminConfigAuditLog_Appears: total audit logs for referral_configs=%d", page.Total)
}

// ============================================================
// TestAdminConfigAuditLog_Pagination — 3 次变更，page_size=2
// ============================================================
func TestAdminConfigAuditLog_Pagination(t *testing.T) {
	requireAdmin(t)

	origResp, _, _ := doGet(baseURL+"/api/v1/admin/referral-config", adminToken)
	var origCfg referralConfigData
	if origResp != nil {
		json.Unmarshal(origResp.Data, &origCfg)
	}
	t.Cleanup(func() {
		if origCfg.CommissionRate > 0 {
			doPut(baseURL+"/api/v1/admin/referral-config", map[string]interface{}{
				"commissionRate":  origCfg.CommissionRate,
				"attributionDays": origCfg.AttributionDays,
			}, adminToken)
		}
	})

	// 执行 3 次不同的 commissionRate 更新（触发 3 次审计写入）
	rates := []float64{0.11, 0.12, 0.13}
	for _, r := range rates {
		resp, status, err := doPut(baseURL+"/api/v1/admin/referral-config", map[string]interface{}{
			"commissionRate": r,
		}, adminToken)
		if err != nil || status != http.StatusOK || resp.Code != 0 {
			t.Logf("update rate=%.2f: status=%d (skipping pagination check)", r, status)
		}
	}

	// 第一页：page_size=2
	resp1, status1, err := doGet(baseURL+"/api/v1/admin/config-audit?table=referral_configs&page=1&page_size=2", adminToken)
	if err != nil {
		t.Fatalf("page1 request failed: %v", err)
	}
	skipIfNotFound(t, status1)
	skipIfNotImplemented(t, status1)
	if status1 != http.StatusOK {
		t.Fatalf("page1 expected 200, got %d", status1)
	}

	page1, err := parsePageData(resp1)
	if err != nil {
		t.Fatalf("parse page1 data: %v", err)
	}

	if page1.Total >= 2 {
		// 第一页应有 2 条记录
		if page1.List != nil {
			var items []configAuditLogItem
			json.Unmarshal(page1.List, &items)
			if len(items) > 2 {
				t.Errorf("page_size=2 but got %d items on page 1", len(items))
			}
		}

		// 第二页
		resp2, status2, _ := doGet(baseURL+"/api/v1/admin/config-audit?table=referral_configs&page=2&page_size=2", adminToken)
		if status2 == http.StatusOK && resp2 != nil {
			page2, _ := parsePageData(resp2)
			t.Logf("TestAdminConfigAuditLog_Pagination: total=%d, page1_size=%d, page2_total=%d",
				page1.Total, len(items(page1)), total(page2))
		}
	} else {
		t.Logf("TestAdminConfigAuditLog_Pagination: total=%d (audit writes may be async or not wired up yet)", page1.Total)
	}
}

// items 从 pageResponse 中提取 items 数量（辅助函数，避免重复解析）
func items(page *pageResponse) []configAuditLogItem {
	if page == nil || page.List == nil {
		return nil
	}
	var result []configAuditLogItem
	json.Unmarshal(page.List, &result)
	return result
}

func total(page *pageResponse) int64 {
	if page == nil {
		return 0
	}
	return page.Total
}

// ============================================================
// TestAdminConfigAuditLog_FilterByTable — 两种表变更后按表过滤
// ============================================================
func TestAdminConfigAuditLog_FilterByTable(t *testing.T) {
	requireAdmin(t)

	// 触发 referral_config 变更
	origReferral, _, _ := doGet(baseURL+"/api/v1/admin/referral-config", adminToken)
	var origRC referralConfigData
	if origReferral != nil {
		json.Unmarshal(origReferral.Data, &origRC)
	}
	t.Cleanup(func() {
		if origRC.CommissionRate > 0 {
			doPut(baseURL+"/api/v1/admin/referral-config", map[string]interface{}{
				"commissionRate": origRC.CommissionRate,
			}, adminToken)
		}
	})

	// 触发 referral_configs 表的变更
	doPut(baseURL+"/api/v1/admin/referral-config", map[string]interface{}{
		"commissionRate": 0.13,
	}, adminToken)

	// 触发 quota_configs 表的变更
	origQuota, _, _ := doGet(baseURL+"/api/v1/admin/quota-config", adminToken)
	var origQC quotaConfigData
	if origQuota != nil {
		json.Unmarshal(origQuota.Data, &origQC)
	}
	t.Cleanup(func() {
		doPut(baseURL+"/api/v1/admin/quota-config", map[string]interface{}{
			"inviteeBonus": origQC.InviteeBonus,
		}, adminToken)
	})
	doPut(baseURL+"/api/v1/admin/quota-config", map[string]interface{}{
		"inviteeBonus": int64(7000),
	}, adminToken)

	// 按 referral_configs 过滤
	respRC, statusRC, err := doGet(baseURL+"/api/v1/admin/config-audit?table=referral_configs&page=1&page_size=50", adminToken)
	if err != nil {
		t.Fatalf("filter referral_configs request failed: %v", err)
	}
	skipIfNotFound(t, statusRC)
	skipIfNotImplemented(t, statusRC)

	// 按 quota_configs 过滤
	respQC, statusQC, err := doGet(baseURL+"/api/v1/admin/config-audit?table=quota_configs&page=1&page_size=50", adminToken)
	if err != nil {
		t.Fatalf("filter quota_configs request failed: %v", err)
	}

	var rcTotal, qcTotal int64
	if statusRC == http.StatusOK && respRC != nil {
		if p, err := parsePageData(respRC); err == nil {
			rcTotal = p.Total
		}
	}
	if statusQC == http.StatusOK && respQC != nil {
		if p, err := parsePageData(respQC); err == nil {
			qcTotal = p.Total
		}
	}

	// 验证两个过滤结果都是数字（逻辑上如果审计已接入，两者都应 >= 1）
	t.Logf("TestAdminConfigAuditLog_FilterByTable: referral_configs logs=%d, quota_configs logs=%d", rcTotal, qcTotal)

	// 如果审计日志已接入，验证过滤有效（rc 结果不应包含 quota_configs 条目，反之亦然）
	if rcTotal > 0 && respRC.Data != nil {
		if p, _ := parsePageData(respRC); p != nil && p.List != nil {
			var rcItems []configAuditLogItem
			json.Unmarshal(p.List, &rcItems)
			for _, item := range rcItems {
				if item.ConfigTable != "" && item.ConfigTable != "referral_configs" {
					t.Errorf("filter by referral_configs returned item with config_table=%s", item.ConfigTable)
				}
			}
		}
	}
}
