package aimodel

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"

	"tokenhub-server/internal/model"
)

// upstreamSnapshot 单个供应商的上游模型清单快照
type upstreamSnapshot struct {
	SupplierID         uint
	Names              map[string]bool // key: 小写模型名（含 Shutdown 的也在此集合）
	ShutdownNames      map[string]bool // key: 小写模型名，仅包含 status=Shutdown/Deprecated 的模型
	Available          bool            // 是否成功拉取（false 表示 API 调用失败/无渠道）
	Err                string
	ReturnedModelTypes map[string]bool // 该供应商 /models API 覆盖的模型类型（非 LLM 类型在此集合内才能做 deprecated 判定）
	// 火山引擎 /api/v3/models 实际返回类型（经验证）：
	//   LLM / VLM / Embedding / ImageGeneration / VideoGeneration / 3DGeneration / Router
	//   注意：TTS（大模型语音合成）和 ASR（流式语音识别）使用独立服务 API，不在 /api/v3/models 列表中
}

// loadUpstreamSnapshots 并发拉取涉及的所有 supplier_id 的上游模型清单
//
// 用途：在批量检测前拉取一次"官网现存模型清单"，失败结果与之对照，
// 以判定模型是"官网已下架（硬下线）"还是"官网仍存在（软下线观察窗口）"。
//
// 复用 DiscoveryService.FetchProviderModelNamesWithStatus 实现，获取名字+状态（Shutdown/Active）。
//
// ReturnedModelTypes 逻辑：
//   - 火山引擎（volcengine）：/api/v3/models 覆盖 LLM/VLM/Embedding/Image/Video（但不含 TTS/ASR）
//   - 其他供应商：nil（仅靠 isLLMLikeType 判定 LLM 类型）
func (mc *ModelChecker) loadUpstreamSnapshots(ctx context.Context, models []model.AIModel) map[uint]*upstreamSnapshot {
	// 收集涉及的 supplier_id
	supplierSet := make(map[uint]bool)
	for _, m := range models {
		if m.SupplierID > 0 {
			supplierSet[m.SupplierID] = true
		}
	}
	if len(supplierSet) == 0 {
		return nil
	}

	// 批量查询供应商 code，判断哪些供应商返回全类型模型列表
	supplierIDs := make([]uint, 0, len(supplierSet))
	for sid := range supplierSet {
		supplierIDs = append(supplierIDs, sid)
	}
	var supplierRows []model.Supplier
	mc.db.WithContext(ctx).Select("id, code").Where("id IN ?", supplierIDs).Find(&supplierRows)
	supplierCodes := make(map[uint]string, len(supplierRows))
	for _, s := range supplierRows {
		supplierCodes[s.ID] = s.Code
	}

	snapshots := make(map[uint]*upstreamSnapshot, len(supplierSet))
	var mu sync.Mutex
	var wg sync.WaitGroup

	for sid := range supplierSet {
		wg.Add(1)
		go func(supplierID uint) {
			defer wg.Done()
			snap := &upstreamSnapshot{
				SupplierID:         supplierID,
				Names:              make(map[string]bool),
				ShutdownNames:      make(map[string]bool),
				ReturnedModelTypes: supplierReturnedModelTypes(supplierCodes[supplierID]),
			}
			modelsWithStatus, err := mc.discovery.FetchProviderModelNamesWithStatus(supplierID)
			if err != nil {
				snap.Available = false
				snap.Err = err.Error()
			} else {
				snap.Available = true
				for _, m := range modelsWithStatus {
					key := strings.ToLower(strings.TrimSpace(m.Name))
					snap.Names[key] = true
					if m.Shutdown {
						snap.ShutdownNames[key] = true
					}
				}
			}
			mu.Lock()
			snapshots[supplierID] = snap
			mu.Unlock()
		}(sid)
	}
	wg.Wait()
	return snapshots
}

// supplierReturnedModelTypes 返回指定供应商的 /models API 实际覆盖的模型类型集合
//
// 只有列在集合中的非 LLM 类型，才能在"不在清单"时做 deprecated_upstream 判定。
// 未列出的类型（如 TTS/ASR 使用独立 API）保持 unknown 不误判。
//
// 已知覆盖情况：
//   - volcengine：/api/v3/models 覆盖 LLM/VLM/Embedding/ImageGeneration/VideoGeneration
//     但 TTS（大模型语音合成）/ ASR（流式识别）使用独立服务端点，不在该列表中
//   - 其他供应商：默认只覆盖 LLM/VLM（标准 OpenAI /v1/models 仅返回文本模型）
func supplierReturnedModelTypes(supplierCode string) map[string]bool {
	switch supplierCode {
	case "volcengine":
		// 实测 /api/v3/models 返回的非 LLM domain（2026-04 验证）：
		//   Embedding / ImageGeneration / VideoGeneration / 3DGeneration / Router
		// 注意：TTS（doubao-tts-*）和 ASR（doubao-asr-*）使用独立服务 API，不在此列表
		return map[string]bool{
			"LLM": true, "VLM": true, "Vision": true, "Reasoning": true,
			"Embedding": true, "ImageGeneration": true, "VideoGeneration": true,
		}
	default:
		// 其他供应商默认只覆盖 LLM 类型（isLLMLikeType 范围）
		return nil
	}
}

// classifyAgainstUpstream 根据上游清单分类一个模型的失败状态
//
// 规则（按优先级）：
//  1. 上游清单未拉取成功 → unknown（保守，走观察窗口）
//  2. 模型名在清单中且上游状态为 Shutdown/Deprecated → deprecated_upstream（确认下架）
//  3. 模型名在清单中且上游状态为 Active/Retiring → upstream_active（模型仍在官网，是配置/权限问题）
//  4. 模型名不在清单中：
//     a. 供应商的 /models API 覆盖当前模型类型（ReturnedModelTypes 中有该 type）→ deprecated_upstream（确认下架）
//     b. 供应商 /models API 未覆盖该类型（如 TTS/ASR 使用独立服务）→ 按 isLLMLikeType 回退
//     c. LLM/VLM 类型 + 不在清单 → deprecated_upstream（LLM 清单里没有 = 下架）
//     d. 其他非 LLM + 未覆盖 → unknown（不能从该清单判断是否下架）
//
// 注意：本函数仅对失败结果调用，成功（Available=true）的模型不应进入此函数。
//
// 火山引擎说明：/api/v3/models 返回所有模型（包括 status=Shutdown 的已下架模型），
// 因此需要检查 ShutdownNames 来区分"仍在运营"和"已关停但仍在列表中"。
func classifyAgainstUpstream(m model.AIModel, snapshots map[uint]*upstreamSnapshot) string {
	if snapshots == nil {
		return UpstreamUnknown
	}
	snap, ok := snapshots[m.SupplierID]
	if !ok || snap == nil || !snap.Available {
		return UpstreamUnknown
	}

	modelNameLower := strings.ToLower(strings.TrimSpace(m.ModelName))

	// 规则 2/3：名字在清单中 → 检查是否 Shutdown
	if snap.Names[modelNameLower] {
		// 火山引擎等供应商会返回 Shutdown/Deprecated 状态的模型
		if snap.ShutdownNames[modelNameLower] {
			return UpstreamDeprecated
		}
		return UpstreamActive
	}

	// 规则 4：名字不在清单中
	modelType := strings.TrimSpace(m.ModelType)

	// a. 供应商的 /models API 覆盖了当前类型 → 不在清单 = 确认下架
	if len(snap.ReturnedModelTypes) > 0 && snap.ReturnedModelTypes[modelType] {
		return UpstreamDeprecated
	}

	// b/c. 按 LLM-like 类型判断（适用于标准 /v1/models 供应商和 ReturnedModelTypes 未覆盖的类型）
	if isLLMLikeType(modelType) {
		return UpstreamDeprecated
	}

	// d. 非 LLM 且未被覆盖的类型（如 TTS/ASR 使用独立 API）→ unknown
	return UpstreamUnknown
}

// isLLMLikeType 判定 ModelType 是否为 LLM/VLM 类型（即上游清单理论应包含的类型）
// 空字符串视为 LLM（按数据库默认值 default:'LLM'）
//
// 注意：数据库实际存储的 ModelType 值见 model/ai_model.go 的常量定义：
//   LLM / Vision / Embedding / ImageGeneration / VideoGeneration / TTS / ASR / Rerank
// 历史/兼容值还有: VLM / Reasoning（部分供应商）
// 此函数返回 true 的类型应能在 /v1/models 返回的清单中找到
func isLLMLikeType(modelType string) bool {
	switch strings.TrimSpace(modelType) {
	case "", "LLM", "VLM", "Vision", "Reasoning":
		return true
	}
	return false
}

// countRecentFailures 查询模型最近 FailureWindow 内的连续失败次数
//
// 返回值：包含本次结果的连续失败次数（即调用方应为 1 + 历史连续失败数）
//
// 实现说明：仅查询 FailureWindow 内的日志，按 checked_at 降序遍历，
// 遇到第一个 available=true 即停止统计，返回此时的连续失败次数。
func (mc *ModelChecker) countRecentFailures(ctx context.Context, modelID uint) int {
	since := time.Now().Add(-FailureWindow)
	var rows []struct {
		Available bool
	}
	if err := mc.db.WithContext(ctx).
		Model(&model.ModelCheckLog{}).
		Select("available").
		Where("model_id = ? AND checked_at >= ?", modelID, since).
		Order("checked_at DESC").
		Limit(FailureThreshold). // 最多需要 threshold-1 条历史 + 本次
		Scan(&rows).Error; err != nil {
		return 1
	}
	count := 1 // 本次失败
	for _, r := range rows {
		if r.Available {
			break
		}
		count++
	}
	return count
}

// applyCheckResults 统一处理检测结果：分类 → 决定下线 → 写日志 → 联动公告
//
// 替代 BatchCheck/CheckByIDs 中"单次失败立即下线"的旧逻辑。
//
// 流程：
//  1. 对每条失败结果调用 categorizeCheckError（已存在）+ classifyAgainstUpstream
//  2. 决策：
//     - rate_limited / skipped → 视为可用，不下线
//     - UpstreamStatus == deprecated_upstream → 立即下线（硬规则）
//     - 其他失败：查询近 24h 连续失败次数，>= FailureThreshold 才下线
//  3. 写入 model_check_logs（含 ErrorCategory / UpstreamStatus / ConsecutiveFailures）
//  4. 可用 → 自动恢复 status=online；统计 recovered
//  5. 末尾：对 UpstreamStatus=deprecated_upstream 且本次新下线的模型，自动生成下线公告
//
// autoCreateAnnouncement: 是否对官网下架的模型自动创建公告（默认 true）
//
// 返回 (recovered count, announcement_id_or_zero)
func (mc *ModelChecker) applyCheckResults(
	ctx context.Context,
	results []ModelCheckResult,
	models []model.AIModel,
	snapshots map[uint]*upstreamSnapshot,
	autoCreateAnnouncement bool,
) (recovered int64, announcementID uint) {
	// 构造 model_id → AIModel 索引，便于按 supplier_id 分组
	modelByID := make(map[uint]model.AIModel, len(models))
	for _, m := range models {
		modelByID[m.ID] = m
	}

	now := time.Now()
	var deprecatedModels []model.AIModel // 本次因官网下架被自动下线的模型（用于公告）

	for i := range results {
		r := &results[i]
		if r.ModelID == 0 {
			continue
		}
		aiModel, hasModel := modelByID[r.ModelID]

		// === 1. 可用：自动恢复 status=online ===
		if r.Available {
			tx := mc.db.WithContext(ctx).Model(&model.AIModel{}).
				Where("id = ? AND status != ?", r.ModelID, "online").
				Update("status", "online")
			if tx.RowsAffected > 0 {
				recovered++
			}
			r.UpstreamStatus = "" // 成功无需上游分类
			r.ConsecutiveFailures = 0
			mc.writeCheckLog(ctx, r, now)
			continue
		}

		// === 2. 失败：先分类 ===
		category, _ := categorizeCheckError(*r)
		r.ErrorCategory = category
		if hasModel {
			r.UpstreamStatus = classifyAgainstUpstream(aiModel, snapshots)
		} else {
			r.UpstreamStatus = UpstreamUnknown
		}

		// === 3. 决策是否下线 ===
		shouldDisable := false
		disableReason := ""

		switch {
		case category == "skipped" || category == "rate_limited":
			// 跳过/限流不算失败
			shouldDisable = false

		case r.UpstreamStatus == UpstreamDeprecated:
			// 硬规则：上游已明确下架 → 立即下线
			shouldDisable = true
			disableReason = "deprecated_upstream"

		default:
			// 软规则：连续 N 次失败才下线
			r.ConsecutiveFailures = mc.countRecentFailures(ctx, r.ModelID)
			if r.ConsecutiveFailures >= FailureThreshold {
				shouldDisable = true
				disableReason = "consecutive_failures"
			}
		}

		// === 4. 执行下线 ===
		if shouldDisable {
			if err := mc.db.WithContext(ctx).Model(&model.AIModel{}).
				Where("id = ?", r.ModelID).
				Update("status", "offline").Error; err != nil {
				mc.logger.Error("停用模型失败", zap.Uint("model_id", r.ModelID), zap.Error(err))
			} else {
				r.AutoDisabled = true
				r.DisableReason = disableReason
				if disableReason == "deprecated_upstream" && hasModel {
					deprecatedModels = append(deprecatedModels, aiModel)
				}
			}
		} else {
			r.AutoDisabled = false
		}

		// === 5. 写入日志 ===
		mc.writeCheckLog(ctx, r, now)
	}

	// === 6. 联动公告 ===
	if autoCreateAnnouncement && len(deprecatedModels) > 0 {
		announcementID = mc.createDeprecationAnnouncements(ctx, deprecatedModels)
	}

	return recovered, announcementID
}

// writeCheckLog 写入一条 model_check_log（含新增字段）
func (mc *ModelChecker) writeCheckLog(ctx context.Context, r *ModelCheckResult, checkedAt time.Time) {
	log := &model.ModelCheckLog{
		ModelID:             r.ModelID,
		ModelName:           r.ModelName,
		ChannelID:           r.ChannelID,
		Available:           r.Available,
		LatencyMs:           r.LatencyMs,
		StatusCode:          r.StatusCode,
		Error:               r.Error,
		CheckedAt:           checkedAt,
		AutoDisabled:        r.AutoDisabled,
		ErrorCategory:       r.ErrorCategory,
		UpstreamStatus:      r.UpstreamStatus,
		ConsecutiveFailures: r.ConsecutiveFailures,
	}
	if err := mc.db.WithContext(ctx).Create(log).Error; err != nil {
		mc.logger.Error("写入检测日志失败", zap.Error(err))
	}
}

// createDeprecationAnnouncements 为本次因官网下架被自动下线的模型创建公告
//
// 设计：按 supplier_id 分组，每个供应商一条公告，避免单条公告条目过多。
// 公告标题/内容格式与 BulkDeprecate handler 保持一致（`type: model_deprecation`，`priority: high`）。
//
// 返回最后创建的 announcement_id（0 表示未创建）
func (mc *ModelChecker) createDeprecationAnnouncements(ctx context.Context, deprecated []model.AIModel) uint {
	if len(deprecated) == 0 {
		return 0
	}

	// 按 supplier_id 分组
	bySupplier := make(map[uint][]model.AIModel)
	for _, m := range deprecated {
		bySupplier[m.SupplierID] = append(bySupplier[m.SupplierID], m)
	}

	// 加载供应商名称
	var supplierIDs []uint
	for sid := range bySupplier {
		supplierIDs = append(supplierIDs, sid)
	}
	var suppliers []model.Supplier
	mc.db.WithContext(ctx).Where("id IN ?", supplierIDs).Find(&suppliers)
	supplierNameByID := make(map[uint]string, len(suppliers))
	for _, s := range suppliers {
		supplierNameByID[s.ID] = s.Name
	}

	now := time.Now()
	expiresAt := now.AddDate(0, 0, 7)

	var lastID uint
	for sid, list := range bySupplier {
		supplierName := supplierNameByID[sid]
		if supplierName == "" {
			supplierName = fmt.Sprintf("供应商#%d", sid)
		}
		title := fmt.Sprintf("【%s】部分模型已官方下架", supplierName)

		var contentBuilder strings.Builder
		contentBuilder.WriteString(fmt.Sprintf("以下模型已被供应商 **%s** 从官方 API 移除，系统已自动下线：\n\n", supplierName))
		for _, m := range list {
			name := m.DisplayName
			if name == "" {
				name = m.ModelName
			}
			contentBuilder.WriteString(fmt.Sprintf("- `%s`\n", name))
		}
		contentBuilder.WriteString("\n如需继续使用相关能力，请前往「模型市场」选择替代方案。本公告将在 7 天后自动过期。")

		// 持久化关联的模型 ID 列表（用于一键检测时跳过已确认下线模型）
		ids := make([]uint, 0, len(list))
		for _, m := range list {
			ids = append(ids, m.ID)
		}
		modelIDsJSON, _ := json.Marshal(ids)

		ann := &model.Announcement{
			Title:      title,
			Content:    contentBuilder.String(),
			Type:       "model_deprecation",
			Priority:   "high",
			Status:     "active",
			ShowBanner: true,
			ExpiresAt:  &expiresAt,
			ModelIDs:   modelIDsJSON,
		}
		if err := mc.db.WithContext(ctx).Create(ann).Error; err != nil {
			mc.logger.Error("自动创建下线公告失败",
				zap.Uint("supplier_id", sid),
				zap.Error(err))
			continue
		}
		lastID = ann.ID
		mc.logger.Info("自动创建模型下线公告",
			zap.String("supplier", supplierName),
			zap.Int("model_count", len(list)),
			zap.Uint("announcement_id", ann.ID))
	}

	return lastID
}

// IsModelMarkedUnavailableSoft 放宽版的失败标记查询（替代旧 IsModelMarkedUnavailable）
//
// 规则：
//   - 最近 1 条日志 available=true → 不跳过（最新成功覆盖历史失败）
//   - 最近 N 条日志全部 available=false 且其中至少一条 upstream_status='deprecated_upstream'
//     或 N >= FailureThreshold → 跳过
//   - 其他情况 → 不跳过（避免观察窗口内的临时失败永久打压同步流程）
//
// 用于 model 同步时判断"这个模型是否真的不可用"
func (mc *ModelChecker) IsModelMarkedUnavailableSoft(ctx context.Context, modelName string) bool {
	var logs []model.ModelCheckLog
	if err := mc.db.WithContext(ctx).
		Where("model_name = ?", modelName).
		Order("checked_at DESC").
		Limit(FailureThreshold).
		Find(&logs).Error; err != nil || len(logs) == 0 {
		return false
	}

	// 最近一条成功 → 不跳过
	if logs[0].Available {
		return false
	}

	// 最近 N 条全部失败时再看是否要跳过
	allFailed := true
	hasDeprecated := false
	for _, l := range logs {
		if l.Available {
			allFailed = false
			break
		}
		if l.UpstreamStatus == UpstreamDeprecated {
			hasDeprecated = true
		}
	}
	if !allFailed {
		return false
	}

	// 上游已下架 → 跳过；连续失败达到阈值 → 跳过
	return hasDeprecated || len(logs) >= FailureThreshold
}

