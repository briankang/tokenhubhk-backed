package aimodel

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"tokenhub-server/internal/model"
)

// AnnouncementDeprecation 模型被某条公告标记为下线的元数据
type AnnouncementDeprecation struct {
	AnnouncementID    uint
	AnnouncementTitle string
	ExpiresAt         *time.Time
	OfflineDate       time.Time
}

// LoadAnnouncementDeprecatedModels 查询所有"被活跃公告确认下线"的模型 ID 集合
//
// 规则：
//   - 公告必须是 type='model_deprecation' AND status='active'
//   - 公告未过期（expires_at IS NULL 或 > NOW()）
//   - 公告的 ModelIDs JSON 字段非空（历史公告若未填写则不计入）
//
// 同一模型被多条公告覆盖时，保留最近一条的元数据（按 created_at DESC）
func (mc *ModelChecker) LoadAnnouncementDeprecatedModels(ctx context.Context) map[uint]AnnouncementDeprecation {
	var anns []model.Announcement
	now := time.Now()
	if err := mc.db.WithContext(ctx).
		Where("type = ? AND status = ? AND (expires_at IS NULL OR expires_at > ?)",
			"model_deprecation", "active", now).
		Order("created_at DESC").
		Find(&anns).Error; err != nil {
		return nil
	}

	result := make(map[uint]AnnouncementDeprecation)
	for _, a := range anns {
		if len(a.ModelIDs) == 0 {
			continue
		}
		var ids []uint
		if err := json.Unmarshal(a.ModelIDs, &ids); err != nil {
			continue
		}
		for _, id := range ids {
			// 第一次出现的（即最新公告）保留，后续覆盖跳过
			if _, exists := result[id]; exists {
				continue
			}
			result[id] = AnnouncementDeprecation{
				AnnouncementID:    a.ID,
				AnnouncementTitle: a.Title,
				ExpiresAt:         a.ExpiresAt,
				OfflineDate:       a.CreatedAt,
			}
		}
	}
	return result
}

// ConfirmedDeprecatedItem 已被公告确认下线的模型（直接从 DB 取，不再实际检测）
type ConfirmedDeprecatedItem struct {
	ModelID           uint       `json:"model_id"`
	ModelName         string     `json:"model_name"`
	DisplayName       string     `json:"display_name,omitempty"`
	SupplierID        uint       `json:"supplier_id,omitempty"`
	SupplierName      string     `json:"supplier_name,omitempty"`
	ModelType         string     `json:"model_type,omitempty"`
	Status            string     `json:"status"`              // 当前 ai_models.status
	AnnouncementID    uint       `json:"announcement_id"`
	AnnouncementTitle string     `json:"announcement_title"`
	OfflineDate       time.Time  `json:"offline_date"`        // 公告创建时间
	ExpiresAt         *time.Time `json:"expires_at,omitempty"`
}

// AvailableItem 检测可用的模型（含跳过类型如 ASR/Video）
type AvailableItem struct {
	ModelID      uint   `json:"model_id"`
	ModelName    string `json:"model_name"`
	DisplayName  string `json:"display_name,omitempty"`
	SupplierName string `json:"supplier_name,omitempty"`
	ChannelName  string `json:"channel_name,omitempty"`
	ModelType    string `json:"model_type,omitempty"`
	StatusCode   int    `json:"status_code,omitempty"`
	LatencyMs    int64  `json:"latency_ms,omitempty"`
	Note         string `json:"note,omitempty"` // 如 "rate limited (model is available)" / "跳过 ASR 模型..."
}

// PreviewItem 检测失败 → 待人工确认下线的模型项
type PreviewItem struct {
	ModelID        uint       `json:"model_id"`
	ModelName      string     `json:"model_name"`
	DisplayName    string     `json:"display_name,omitempty"`
	SupplierID     uint       `json:"supplier_id,omitempty"`
	SupplierName   string     `json:"supplier_name,omitempty"`
	ChannelID      uint       `json:"channel_id,omitempty"`
	ChannelName    string     `json:"channel_name,omitempty"`
	ModelType      string     `json:"model_type,omitempty"`
	Status         string     `json:"status,omitempty"`        // 当前 ai_models.status
	StatusCode     int        `json:"status_code,omitempty"`
	Error          string     `json:"error,omitempty"`
	ErrorCategory  string     `json:"error_category,omitempty"`
	UpstreamStatus string     `json:"upstream_status,omitempty"` // deprecated_upstream/upstream_active/unknown
	Suggestion     string     `json:"suggestion,omitempty"`
	LatencyMs      int64      `json:"latency_ms,omitempty"`
	LastSuccessAt  *time.Time `json:"last_success_at,omitempty"`     // 最近一次成功检测的时间
	RecentFailures int        `json:"recent_failures,omitempty"`     // 最近 24h 失败次数（含本次）
}

// BatchCheckPreviewResult 一键扫描预览结果（dry-run，不修改数据库）
type BatchCheckPreviewResult struct {
	TotalScanned        int                       `json:"total_scanned"`        // 实际扫描的模型数（不含 ConfirmedDeprecated）
	TotalSkipped        int                       `json:"total_skipped"`        // 跳过检测的模型数（已被公告确认下线）
	ConfirmedDeprecated []ConfirmedDeprecatedItem `json:"confirmed_deprecated"` // 已被公告确认下线（跳过检测）
	Available           []AvailableItem           `json:"available"`            // 检测正常
	PendingReview       []PreviewItem             `json:"pending_review"`       // 待人工确认下线
}

// BatchCheckPreview 一键扫描预览（dry-run，不写日志/不改 status/不创建公告）
//
// 流程：
//  1. 加载所有 is_active=1 模型
//  2. 加载已被公告确认下线的模型 ID 集合 → 直接归入 ConfirmedDeprecated（不检测）
//  3. 对剩余模型并发执行 checkSingleModel + 上游清单对照
//  4. 失败模型：分类 + 建议 + 最近成功时间，归入 PendingReview 等待人工审核
//  5. 成功模型：归入 Available
//
// 调用方应展示三段结果，由管理员勾选 PendingReview 中真正需要下线的模型，
// 调用现有 BulkDeprecate API 走批量下线 + 公告流程。
func (mc *ModelChecker) BatchCheckPreview(ctx context.Context, progressCh chan<- BatchCheckProgress) (*BatchCheckPreviewResult, error) {
	var models []model.AIModel
	if err := mc.db.WithContext(ctx).Where("is_active = ?", true).Find(&models).Error; err != nil {
		return nil, fmt.Errorf("查询活跃模型失败: %w", err)
	}

	result := &BatchCheckPreviewResult{
		ConfirmedDeprecated: []ConfirmedDeprecatedItem{},
		Available:           []AvailableItem{},
		PendingReview:       []PreviewItem{},
	}
	if len(models) == 0 {
		return result, nil
	}

	// 1. 加载已被公告确认下线的模型
	confirmedMap := mc.LoadAnnouncementDeprecatedModels(ctx)

	// 2. 加载供应商名称
	supplierNameMap := mc.loadSupplierNames(ctx, models)

	// 3. 分流
	var toCheck []model.AIModel
	for _, m := range models {
		if dep, ok := confirmedMap[m.ID]; ok {
			result.ConfirmedDeprecated = append(result.ConfirmedDeprecated, ConfirmedDeprecatedItem{
				ModelID:           m.ID,
				ModelName:         m.ModelName,
				DisplayName:       m.DisplayName,
				SupplierID:        m.SupplierID,
				SupplierName:      supplierNameMap[m.SupplierID],
				ModelType:         m.ModelType,
				Status:            m.Status,
				AnnouncementID:    dep.AnnouncementID,
				AnnouncementTitle: dep.AnnouncementTitle,
				OfflineDate:       dep.OfflineDate,
				ExpiresAt:         dep.ExpiresAt,
			})
		} else {
			toCheck = append(toCheck, m)
		}
	}
	result.TotalSkipped = len(result.ConfirmedDeprecated)
	result.TotalScanned = len(toCheck)

	if len(toCheck) == 0 {
		if progressCh != nil {
			close(progressCh)
		}
		return result, nil
	}

	// 4. 上游清单 + 路由表
	upstreamSnapshots := mc.loadUpstreamSnapshots(ctx, toCheck)
	routeMap := mc.buildRouteMap(ctx, toCheck)

	// 5. 并发检测（不写日志，不改 status）
	results := make([]ModelCheckResult, len(toCheck))
	var (
		wg           sync.WaitGroup
		sem          = make(chan struct{}, 3)
		checked      int64
		available    int64
		failed       int64
		supplierMu   sync.Mutex
		supplierLast = make(map[string]time.Time)
	)

	if progressCh != nil {
		progressCh <- BatchCheckProgress{Total: len(toCheck)}
	}

	for i, m := range toCheck {
		wg.Add(1)
		go func(idx int, aiModel model.AIModel) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			route, ok := routeMap[aiModel.ModelName]
			if !ok {
				// 跳过型（ASR/Video/Rerank/Audio）无路由 → 视为可用（与有路由时行为一致）
				effectiveType := aiModel.ModelType
				if effectiveType == "" {
					effectiveType = inferModelTypeByName(aiModel.ModelName)
				}
				if isSkipWorthyType(effectiveType) {
					results[idx] = ModelCheckResult{
						ModelID:    aiModel.ID,
						ModelName:  aiModel.ModelName,
						SupplierID: aiModel.SupplierID,
						Available:  true,
						Error:      fmt.Sprintf("跳过 %s 模型（无路由+不支持自动检测）", effectiveType),
					}
					atomic.AddInt64(&available, 1)
				} else {
					results[idx] = ModelCheckResult{
						ModelID:    aiModel.ID,
						ModelName:  aiModel.ModelName,
						SupplierID: aiModel.SupplierID,
						Available:  false,
						Error:      "无可用渠道路由",
					}
					atomic.AddInt64(&failed, 1)
				}
			} else {
				// 同一 endpoint 限流 500ms
				endpoint := strings.TrimRight(route.Endpoint, "/")
				supplierMu.Lock()
				if last, ok := supplierLast[endpoint]; ok {
					elapsed := time.Since(last)
					if elapsed < 500*time.Millisecond {
						supplierMu.Unlock()
						time.Sleep(500*time.Millisecond - elapsed)
						supplierMu.Lock()
					}
				}
				supplierLast[endpoint] = time.Now()
				supplierMu.Unlock()

				results[idx] = mc.checkSingleModel(ctx, aiModel, route)
				results[idx].SupplierID = aiModel.SupplierID
				if results[idx].Available {
					atomic.AddInt64(&available, 1)
				} else {
					atomic.AddInt64(&failed, 1)
				}
			}

			c := atomic.AddInt64(&checked, 1)
			if progressCh != nil {
				progressCh <- BatchCheckProgress{
					Total:     len(toCheck),
					Checked:   int(c),
					Available: int(atomic.LoadInt64(&available)),
					Failed:    int(atomic.LoadInt64(&failed)),
				}
			}
		}(i, m)
	}
	wg.Wait()
	if progressCh != nil {
		close(progressCh)
	}

	// 6. 失败模型加载最近成功时间和近 24h 失败次数
	failedIDs := make([]uint, 0)
	for _, r := range results {
		if r.ModelID > 0 && !r.Available {
			failedIDs = append(failedIDs, r.ModelID)
		}
	}
	lastSuccessMap := mc.loadLastSuccessTimes(ctx, failedIDs)
	recentFailMap := mc.loadRecentFailureCounts(ctx, failedIDs)

	// 7. 归类结果
	modelMap := make(map[uint]model.AIModel, len(toCheck))
	for _, m := range toCheck {
		modelMap[m.ID] = m
	}

	for _, r := range results {
		if r.ModelID == 0 {
			continue
		}
		m := modelMap[r.ModelID]
		if r.Available {
			note := ""
			if strings.HasPrefix(r.Error, "跳过") || strings.HasPrefix(r.Error, "rate limited") || strings.HasPrefix(r.Error, "probe") {
				note = r.Error
			}
			result.Available = append(result.Available, AvailableItem{
				ModelID:      m.ID,
				ModelName:    m.ModelName,
				DisplayName:  m.DisplayName,
				SupplierName: supplierNameMap[m.SupplierID],
				ChannelName:  r.ChannelName,
				ModelType:    m.ModelType,
				StatusCode:   r.StatusCode,
				LatencyMs:    r.LatencyMs,
				Note:         note,
			})
		} else {
			category, suggestion := categorizeCheckError(r)
			upstreamStatus := classifyAgainstUpstream(m, upstreamSnapshots)
			var lastSuccess *time.Time
			if t, ok := lastSuccessMap[m.ID]; ok {
				ts := t
				lastSuccess = &ts
			}
			result.PendingReview = append(result.PendingReview, PreviewItem{
				ModelID:        m.ID,
				ModelName:      m.ModelName,
				DisplayName:    m.DisplayName,
				SupplierID:     m.SupplierID,
				SupplierName:   supplierNameMap[m.SupplierID],
				ChannelID:      r.ChannelID,
				ChannelName:    r.ChannelName,
				ModelType:      m.ModelType,
				Status:         m.Status,
				StatusCode:     r.StatusCode,
				Error:          previewTruncate(r.Error, 500),
				ErrorCategory:  category,
				UpstreamStatus: upstreamStatus,
				Suggestion:     suggestion,
				LatencyMs:      r.LatencyMs,
				LastSuccessAt:  lastSuccess,
				RecentFailures: recentFailMap[m.ID] + 1, // +本次
			})
		}
	}

	return result, nil
}

// loadSupplierNames 批量加载模型涉及的供应商名称
func (mc *ModelChecker) loadSupplierNames(ctx context.Context, models []model.AIModel) map[uint]string {
	idSet := make(map[uint]bool)
	for _, m := range models {
		if m.SupplierID > 0 {
			idSet[m.SupplierID] = true
		}
	}
	if len(idSet) == 0 {
		return nil
	}
	ids := make([]uint, 0, len(idSet))
	for id := range idSet {
		ids = append(ids, id)
	}
	var rows []model.Supplier
	mc.db.WithContext(ctx).Where("id IN ?", ids).Find(&rows)
	out := make(map[uint]string, len(rows))
	for _, s := range rows {
		out[s.ID] = s.Name
	}
	return out
}

// loadLastSuccessTimes 批量加载这些模型最近一次成功检测的时间
func (mc *ModelChecker) loadLastSuccessTimes(ctx context.Context, modelIDs []uint) map[uint]time.Time {
	if len(modelIDs) == 0 {
		return nil
	}
	type row struct {
		ModelID   uint
		CheckedAt time.Time
	}
	var rows []row
	mc.db.WithContext(ctx).Raw(`
		SELECT model_id, MAX(checked_at) as checked_at
		FROM model_check_logs
		WHERE model_id IN ? AND available = 1
		GROUP BY model_id
	`, modelIDs).Scan(&rows)
	out := make(map[uint]time.Time, len(rows))
	for _, r := range rows {
		out[r.ModelID] = r.CheckedAt
	}
	return out
}

// loadRecentFailureCounts 批量加载这些模型最近 FailureWindow 内的失败次数
func (mc *ModelChecker) loadRecentFailureCounts(ctx context.Context, modelIDs []uint) map[uint]int {
	if len(modelIDs) == 0 {
		return nil
	}
	type row struct {
		ModelID uint
		Cnt     int
	}
	var rows []row
	since := time.Now().Add(-FailureWindow)
	mc.db.WithContext(ctx).Raw(`
		SELECT model_id, COUNT(*) as cnt
		FROM model_check_logs
		WHERE model_id IN ? AND available = 0 AND checked_at >= ?
		GROUP BY model_id
	`, modelIDs, since).Scan(&rows)
	out := make(map[uint]int, len(rows))
	for _, r := range rows {
		out[r.ModelID] = r.Cnt
	}
	return out
}

// previewTruncate 截断字符串
func previewTruncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
