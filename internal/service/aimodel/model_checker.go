package aimodel

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"go.uber.org/zap"
	"gorm.io/gorm"

	"tokenhub-server/internal/middleware"
	"tokenhub-server/internal/model"
	"tokenhub-server/internal/pkg/logger"
	"tokenhub-server/internal/pkg/safego"
	"tokenhub-server/internal/service/modeldiscovery"
)

// wenxinProbeEndpoints 文心一言探针端点映射（与 provider/wenxin.go 保持一致）
var wenxinProbeEndpoints = map[string]string{
	"ernie-4.0-8k":    "completions_pro",
	"ernie-3.5-8k":    "completions",
	"ernie-speed-128k": "ernie_speed",
}

// FailureThreshold 连续失败 N 次才下线（软规则下的观察窗口阈值）
// 单次失败容易误判（瞬时网络/限流/上游抖动），连续 3 次失败才视为真正下线
const FailureThreshold = 3

// FailureWindow 连续失败计数的查询窗口
// 仅查询近 24h 的检测日志参与连续失败统计，避免历史日志干扰
const FailureWindow = 24 * time.Hour

// 上游对照状态枚举
const (
	UpstreamDeprecated = "deprecated_upstream" // 上游官网已下架（硬下线）
	UpstreamActive     = "upstream_active"     // 上游官网仍存在（软下线，进入观察窗口）
	UpstreamUnknown    = "unknown"             // 上游清单未拉取成功 / 模型类型不在清单覆盖范围内
	UpstreamManual     = "manual_override"     // 管理员手动重新上线后写入的标记
)

// ModelCheckResult 单个模型检测结果
type ModelCheckResult struct {
	ModelID      uint   `json:"model_id"`
	ModelName    string `json:"model_name"`
	SupplierID   uint   `json:"supplier_id,omitempty"`
	ChannelID    uint   `json:"channel_id"`
	ChannelName  string `json:"channel_name"`
	SupplierName string `json:"supplier_name"`
	Available    bool   `json:"available"`
	LatencyMs    int64  `json:"latency_ms"`
	StatusCode   int    `json:"status_code"`
	Error        string `json:"error,omitempty"`
	AutoDisabled bool   `json:"auto_disabled"`

	// --- 2026-04-15 新增：错误分类 + 上游对照 + 观察窗口 ---
	ErrorCategory       string `json:"error_category,omitempty"`
	UpstreamStatus      string `json:"upstream_status,omitempty"`
	ConsecutiveFailures int    `json:"consecutive_failures,omitempty"`
	DisableReason       string `json:"disable_reason,omitempty"` // 下线原因: deprecated_upstream / consecutive_failures
}

// BatchCheckProgress 批量检测进度
type BatchCheckProgress struct {
	Total     int `json:"total"`
	Checked   int `json:"checked"`
	Available int `json:"available"`
	Failed    int `json:"failed"`
	Disabled  int `json:"disabled"`
	Recovered int `json:"recovered"` // 从 offline 恢复为 online 的数量
}

// ModelChecker 模型可用性检测器
type ModelChecker struct {
	db         *gorm.DB
	logger     *zap.Logger
	discovery  *modeldiscovery.DiscoveryService // 用于拉取供应商上游清单做对照
	httpClient *http.Client                     // 共享 HTTP 客户端（关闭 HTTP/2，开启连接池）
}

// newProbeHTTPClient 创建探针专用 HTTP 客户端
// 关闭 HTTP/2 防止 Docker 环境下的 EOF 问题，开启连接池减少 TLS 握手开销
func newProbeHTTPClient() *http.Client {
	transport := &http.Transport{
		TLSClientConfig: &tls.Config{
			MinVersion: tls.VersionTLS12,
		},
		// 禁用 HTTP/2：Docker 网络中 HTTP/2 容易出现 EOF/GOAWAY，强制 HTTP/1.1 更稳定
		TLSNextProto: make(map[string]func(authority string, c *tls.Conn) http.RoundTripper),
		DialContext: (&net.Dialer{
			Timeout:   15 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		TLSHandshakeTimeout:   15 * time.Second,
		ResponseHeaderTimeout: 35 * time.Second,
		MaxIdleConns:          100,
		MaxIdleConnsPerHost:   10,
		IdleConnTimeout:       90 * time.Second,
		ForceAttemptHTTP2:     false,
	}
	// 不设全局 Timeout，依赖各探针通过 context.WithTimeout 独立控制
	return &http.Client{Transport: transport}
}

// NewModelChecker 创建模型检测器
// 内部自动初始化 DiscoveryService 用于上游清单对照（无须额外参数）
func NewModelChecker(db *gorm.DB) *ModelChecker {
	return &ModelChecker{
		db:         db,
		logger:     logger.L,
		discovery:  modeldiscovery.NewDiscoveryService(db),
		httpClient: newProbeHTTPClient(),
	}
}

// DB 返回数据库连接（供 handler 层查询关联数据）
func (mc *ModelChecker) DB() *gorm.DB {
	return mc.db
}

// channelRoute 内部结构，表示一个模型到渠道的映射
type channelRoute struct {
	ChannelID    uint
	ChannelName  string
	SupplierName string
	ActualModel  string
	Endpoint     string
	APIKey       string
	OrgID        string // 供百度文心等 OAuth2 供应商使用（client_secret）
}

// BatchCheck 批量检测所有 online 模型的可用性
// progressCh 可选，用于实时推送进度（为 nil 则不推送）
// 返回检测结果列表
func (mc *ModelChecker) BatchCheck(ctx context.Context, progressCh chan<- BatchCheckProgress) ([]ModelCheckResult, error) {
	// 1. 查询所有活跃模型（包含 online 和 offline，排除 is_active=false 的已禁用模型）
	var models []model.AIModel
	if err := mc.db.WithContext(ctx).
		Where("is_active = ?", true).
		Find(&models).Error; err != nil {
		return nil, fmt.Errorf("查询活跃模型失败: %w", err)
	}

	if len(models) == 0 {
		return nil, nil
	}

	// 2. 并发拉取上游模型清单（用于失败结果的 deprecated_upstream / upstream_active 分类）
	// 复用 DiscoveryService.FetchProviderModelNames，结果仅用于本次检测，不入缓存
	upstreamSnapshots := mc.loadUpstreamSnapshots(ctx, models)

	// 3. 为每个模型查找路由（channel）
	routeMap := mc.buildRouteMap(ctx, models)

	// 3. 并发检测（最多3个并发，按供应商限流）
	var (
		results   = make([]ModelCheckResult, len(models))
		wg        sync.WaitGroup
		sem       = make(chan struct{}, 3) // 全局并发限制
		checked   int64
		available int64
		failed    int64
		disabled  int64
		// 按供应商/Endpoint限流：同一 endpoint 的请求间隔至少 500ms，避免触发供应商 QPS 限制
		supplierMu   sync.Mutex
		supplierLast = make(map[string]time.Time) // endpoint → 上次请求时间
	)

	progress := BatchCheckProgress{Total: len(models)}
	if progressCh != nil {
		progressCh <- progress
	}

	for i, m := range models {
		wg.Add(1)
		idx, aiModel := i, m // 兼容 Go<1.22 的 range 变量捕获
		safego.Go("model-checker-batch", func() {
			defer wg.Done()
			sem <- struct{}{}        // 获取令牌
			defer func() { <-sem }() // 释放令牌

			route, ok := routeMap[aiModel.ModelName]
			if !ok {
				// 无路由：先判断类型是否为"跳过型"（ASR/Video/Rerank/Audio）
				// 这些类型在有路由时也会直接跳过（Available=true），无路由时保持同样行为，
				// 避免"无路由"误将本不应检测的模型标记为不可用。
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
					// 其他类型（Embedding/TTS/Image 等）→ 标记失败，进入观察窗口
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
				// 按供应商限流：同一 endpoint 请求间隔 500ms
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
					Total:     len(models),
					Checked:   int(c),
					Available: int(atomic.LoadInt64(&available)),
					Failed:    int(atomic.LoadInt64(&failed)),
					Disabled:  int(atomic.LoadInt64(&disabled)),
				}
			}
		})
	}

	wg.Wait()

	// 4. 统一处理结果：分类 → 决定下线 → 写日志 → 联动公告
	// applyCheckResults 内部处理：
	//   - 单次失败不再立即下线，连续 FailureThreshold 次失败才下线（软规则）
	//   - 上游已下架的模型立即下线（硬规则）+ 自动创建公告
	//   - 写入 model_check_logs 含 ErrorCategory / UpstreamStatus / ConsecutiveFailures
	recoveredCount, _ := mc.applyCheckResults(ctx, results, models, upstreamSnapshots, true)
	recovered := recoveredCount
	// 重新统计 disabled 数量（applyCheckResults 内部基于观察窗口决定是否下线）
	disabled = 0
	for _, r := range results {
		if r.AutoDisabled {
			disabled++
		}
	}

	// 最终进度
	if progressCh != nil {
		progressCh <- BatchCheckProgress{
			Total:     len(models),
			Checked:   len(models),
			Available: int(available),
			Failed:    int(failed),
			Disabled:  int(disabled),
			Recovered: int(recovered),
		}
		close(progressCh)
	}

	// 有模型状态变化时，清除公开模型列表缓存
	if disabled > 0 || recovered > 0 {
		middleware.CacheInvalidate("cache:/api/v1/public/models*")
	}

	mc.logger.Info("模型批量检测完成",
		zap.Int("total", len(models)),
		zap.Int64("available", available),
		zap.Int64("failed", failed),
		zap.Int64("disabled", disabled),
		zap.Int64("recovered", recovered))

	return results, nil
}

// CheckByIDs 检测指定 ID 的模型可用性
// 支持用户手动勾选模型批量检测，不限制 is_active 状态
func (mc *ModelChecker) CheckByIDs(ctx context.Context, modelIDs []uint, progressCh chan<- BatchCheckProgress) ([]ModelCheckResult, error) {
	if len(modelIDs) == 0 {
		return nil, nil
	}

	var models []model.AIModel
	if err := mc.db.WithContext(ctx).
		Where("id IN ?", modelIDs).
		Find(&models).Error; err != nil {
		return nil, fmt.Errorf("查询指定模型失败: %w", err)
	}

	if len(models) == 0 {
		return nil, nil
	}

	// 拉取上游清单（与 BatchCheck 相同的对照机制）
	upstreamSnapshots := mc.loadUpstreamSnapshots(ctx, models)
	routeMap := mc.buildRouteMap(ctx, models)

	var (
		results   = make([]ModelCheckResult, len(models))
		wg        sync.WaitGroup
		sem       = make(chan struct{}, 10)
		checked   int64
		available int64
		failed    int64
		disabled  int64
	)

	progress := BatchCheckProgress{Total: len(models)}
	if progressCh != nil {
		progressCh <- progress
	}

	for i, m := range models {
		wg.Add(1)
		idx, aiModel := i, m // 兼容 Go<1.22 range 变量捕获
		safego.Go("model-checker-preview", func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			route, ok := routeMap[aiModel.ModelName]
			if !ok {
				// 跳过型（ASR/Video/Rerank）无路由 → 视为可用（与有路由时行为一致）
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
					Total:     len(models),
					Checked:   int(c),
					Available: int(atomic.LoadInt64(&available)),
					Failed:    int(atomic.LoadInt64(&failed)),
					Disabled:  int(atomic.LoadInt64(&disabled)),
				}
			}
		})
	}

	wg.Wait()

	// 统一处理：分类 → 观察窗口 → 写日志 → 联动公告
	mc.applyCheckResults(ctx, results, models, upstreamSnapshots, true)
	disabled = 0
	for _, r := range results {
		if r.AutoDisabled {
			disabled++
		}
	}

	if progressCh != nil {
		progressCh <- BatchCheckProgress{
			Total:     len(models),
			Checked:   len(models),
			Available: int(available),
			Failed:    int(failed),
			Disabled:  int(disabled),
		}
		close(progressCh)
	}

	// 有模型状态变化时清除缓存
	middleware.CacheInvalidate("cache:/api/v1/public/models*")

	mc.logger.Info("指定模型检测完成",
		zap.Int("total", len(models)),
		zap.Int64("available", available),
		zap.Int64("failed", failed),
		zap.Int64("disabled", disabled))

	return results, nil
}

// IsModelMarkedUnavailable 检查模型是否在最近检测中被确认不可用
// 用于模型同步时跳过已知下线模型
//
// 2026-04-15 起委托给 IsModelMarkedUnavailableSoft 实现放宽规则：
//   - 最近一次成功 → 不跳过
//   - 最近 N 条全部失败 + 上游已下架 → 跳过
//   - 否则 → 不跳过（避免临时失败永久打压同步流程）
func (mc *ModelChecker) IsModelMarkedUnavailable(ctx context.Context, modelName string) bool {
	return mc.IsModelMarkedUnavailableSoft(ctx, modelName)
}

// GetCheckHistory 获取检测历史记录（分页）
func (mc *ModelChecker) GetCheckHistory(ctx context.Context, page, pageSize int) ([]model.ModelCheckLog, int64, error) {
	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > 100 {
		pageSize = 50
	}

	var total int64
	query := mc.db.WithContext(ctx).Model(&model.ModelCheckLog{})
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}

	var logs []model.ModelCheckLog
	offset := (page - 1) * pageSize
	if err := query.Order("checked_at DESC, id DESC").Offset(offset).Limit(pageSize).Find(&logs).Error; err != nil {
		return nil, 0, err
	}
	return logs, total, nil
}

// GetLatestCheckSummary 获取最近一次批量检测的汇总（按 checked_at 分组）
func (mc *ModelChecker) GetLatestCheckSummary(ctx context.Context) ([]model.ModelCheckLog, error) {
	// 找到最近一次检测的时间
	var latest model.ModelCheckLog
	if err := mc.db.WithContext(ctx).Order("checked_at DESC").First(&latest).Error; err != nil {
		return nil, err
	}

	var logs []model.ModelCheckLog
	if err := mc.db.WithContext(ctx).
		Where("checked_at = ?", latest.CheckedAt).
		Order("available ASC, model_name ASC").
		Find(&logs).Error; err != nil {
		return nil, err
	}
	return logs, nil
}

// CheckResultDetail 详细检测结果（含解决方案建议）
type CheckResultDetail struct {
	ModelCheckResult
	ErrorCategory string `json:"error_category"` // 错误分类
	Suggestion    string `json:"suggestion"`      // 解决方案建议
}

// SupplierCheckGroup 按供应商分组的检测结果
type SupplierCheckGroup struct {
	SupplierID   uint                `json:"supplier_id"`
	SupplierName string              `json:"supplier_name"`
	Total        int                 `json:"total"`
	Available    int                 `json:"available"`
	Failed       int                 `json:"failed"`
	Disabled     int                 `json:"disabled"`
	Details      []CheckResultDetail `json:"details"`
}

// CheckSummaryDetail 检测汇总（按错误类型分组 + 按供应商分组）
type CheckSummaryDetail struct {
	Total          int                  `json:"total"`
	Available      int                  `json:"available"`
	Failed         int                  `json:"failed"`
	Disabled       int                  `json:"disabled"`
	Groups         []CheckErrorGroup    `json:"groups"`          // 按错误类型分组
	Details        []CheckResultDetail  `json:"details"`         // 完整详情列表
	SupplierGroups []SupplierCheckGroup `json:"supplier_groups"` // 按供应商分组（新增）
}

// CheckErrorGroup 错误分组
type CheckErrorGroup struct {
	Category   string   `json:"category"`    // 错误分类
	Count      int      `json:"count"`       // 该类错误数量
	Suggestion string   `json:"suggestion"`  // 通用解决方案
	Models     []string `json:"models"`      // 涉及的模型名
}

// categorizeCheckError 对检测错误进行分类并给出解决建议
func categorizeCheckError(result ModelCheckResult) (category, suggestion string) {
	errLower := strings.ToLower(result.Error)

	switch {
	case result.Error == "无可用渠道路由":
		return "no_route", "模型未配置渠道路由。请在「渠道管理」中为该模型添加路由，或执行「刷新默认渠道」以自动从供应商接入点导入路由。"

	case strings.Contains(errLower, "model_not_found") || strings.Contains(errLower, "does not exist") || strings.Contains(errLower, "model not found"):
		return "model_not_found", "供应商 API 返回模型不存在。可能该模型已下线或模型名称不正确。建议：1) 到供应商控制台确认模型是否可用；2) 检查 actual_model 名称是否正确。"

	case strings.Contains(errLower, "eof") || strings.Contains(errLower, "connection reset"):
		return "connection_error", "连接被中断（EOF/Reset）。可能是供应商限流或网络不稳定。建议：1) 稍后重试；2) 减少并发检测数量；3) 检查供应商是否有 QPS 限制。"

	case strings.Contains(errLower, "timeout") || strings.Contains(errLower, "deadline exceeded"):
		return "timeout", "请求超时。可能是网络延迟高或供应商响应慢。建议：1) 稍后重试；2) 检查网络连通性；3) 确认供应商服务状态。"

	case result.StatusCode == 401 || strings.Contains(errLower, "unauthorized") || strings.Contains(errLower, "authentication"):
		return "auth_error", "认证失败（401）。供应商 API Key 无效或已过期。建议：到「供应商接入点」页面更新 API Key。"

	case result.StatusCode == 403 || strings.Contains(errLower, "forbidden") || strings.Contains(errLower, "permission"):
		return "permission_denied", "权限不足（403）。API Key 无权访问该模型。建议：1) 确认 API Key 是否已开通该模型的访问权限；2) 联系供应商确认订阅状态。"

	case result.StatusCode == 429:
		return "rate_limited", "速率限制（429）。请求过于频繁。建议：1) 模型本身可用，无需处理；2) 如持续限流，考虑提升配额或分散请求。"

	case strings.Contains(errLower, "not activated") || strings.Contains(errLower, "not_activated"):
		return "product_not_activated", "供应商产品未激活。建议：到供应商控制台激活该模型/产品后重试。"

	case strings.Contains(errLower, "does not support this api") || strings.Contains(errLower, "not support this api"):
		return "api_mismatch", "模型不支持当前 API 端点。可能是 Embedding/翻译/图像模型被错误地发送到了 Chat Completions 端点。建议：1) 确认模型类型是否正确；2) 检查渠道路由配置。"

	case strings.Contains(errLower, "insufficient_quota") || strings.Contains(errLower, "billing") || strings.Contains(errLower, "balance"):
		return "quota_exhausted", "配额不足或账户余额为零。建议：到供应商控制台充值或检查账单状态。"

	case strings.Contains(errLower, "invalid_request") || strings.Contains(errLower, "bad request"):
		return "invalid_request", "请求参数错误（400）。可能是模型不支持当前请求格式。建议：1) 确认模型类型是否为 LLM/VLM；2) 检查模型是否需要特殊参数。"

	case strings.Contains(errLower, "image") && (strings.Contains(errLower, "not supported") || strings.Contains(errLower, "generation")):
		return "image_not_supported", "当前渠道不支持该图像模型。建议：1) 确认渠道 endpoint 指向正确的图像生成 API；2) 阿里云图像模型需使用 DashScope 原生异步端点，而非 compatible-mode。"

	case strings.Contains(errLower, "跳过"):
		return "skipped", result.Error // 跳过类保留原始说明

	case result.StatusCode >= 500:
		return "server_error", fmt.Sprintf("供应商服务端错误（%d）。建议：稍后重试，若持续出现请联系供应商确认服务状态。", result.StatusCode)

	default:
		return "unknown", fmt.Sprintf("未分类的错误（HTTP %d）。建议：查看详细错误信息排查原因。", result.StatusCode)
	}
}

// BuildDetailedSummary 从检测结果构建详细汇总（含分组和解决方案）
func BuildDetailedSummary(results []ModelCheckResult) *CheckSummaryDetail {
	summary := &CheckSummaryDetail{
		Total: len(results),
	}

	groupMap := make(map[string]*CheckErrorGroup)
	supplierMap := make(map[uint]*SupplierCheckGroup)

	for _, r := range results {
		detail := CheckResultDetail{
			ModelCheckResult: r,
		}

		if r.Available {
			summary.Available++
			if r.Error != "" && strings.HasPrefix(r.Error, "跳过") {
				detail.ErrorCategory = "skipped"
				detail.Suggestion = r.Error
			}
		} else {
			summary.Failed++
			if r.AutoDisabled {
				summary.Disabled++
			}
			category, suggestion := categorizeCheckError(r)
			detail.ErrorCategory = category
			detail.Suggestion = suggestion

			// 按错误类型分组
			if g, ok := groupMap[category]; ok {
				g.Count++
				g.Models = append(g.Models, r.ModelName)
			} else {
				groupMap[category] = &CheckErrorGroup{
					Category:   category,
					Count:      1,
					Suggestion: suggestion,
					Models:     []string{r.ModelName},
				}
			}
		}

		// 按供应商分组（无论成功/失败都加入）
		sg, exists := supplierMap[r.SupplierID]
		if !exists {
			sg = &SupplierCheckGroup{
				SupplierID:   r.SupplierID,
				SupplierName: r.SupplierName,
			}
			supplierMap[r.SupplierID] = sg
		}
		// 保留 SupplierName（若之前为空）
		if sg.SupplierName == "" && r.SupplierName != "" {
			sg.SupplierName = r.SupplierName
		}
		sg.Total++
		if r.Available {
			sg.Available++
		} else {
			sg.Failed++
		}
		if r.AutoDisabled {
			sg.Disabled++
		}
		sg.Details = append(sg.Details, detail)

		summary.Details = append(summary.Details, detail)
	}

	// 错误分组转为有序切片
	for _, g := range groupMap {
		summary.Groups = append(summary.Groups, *g)
	}

	// 供应商分组转为有序切片
	for _, sg := range supplierMap {
		summary.SupplierGroups = append(summary.SupplierGroups, *sg)
	}

	return summary
}

// buildRouteMap 为每个模型名构建 channel 路由映射
// 优先从 custom_channel_routes 查找，未命中则回退到 channel_models 自动发现
// 与 SelectChannel 的路由逻辑保持一致
func (mc *ModelChecker) buildRouteMap(ctx context.Context, models []model.AIModel) map[string]channelRoute {
	// Step 1: 从 custom_channel_routes 查找显式路由
	type routeRow struct {
		AliasModel   string
		ActualModel  string
		ChannelID    uint
		ChannelName  string
		SupplierName string
		Endpoint     string
		APIKey       string
		OrgID        string // 文心等 OAuth2 供应商：client_secret 存于 custom_params
	}

	var rows []routeRow
	mc.db.WithContext(ctx).Raw(`
		SELECT cr.alias_model, cr.actual_model, ch.id as channel_id, ch.name as channel_name,
		       COALESCE(s.name, '') as supplier_name, ch.endpoint, ch.api_key,
		       COALESCE(JSON_UNQUOTE(JSON_EXTRACT(ch.custom_params, '$.client_secret')), '') as org_id
		FROM custom_channel_routes cr
		JOIN custom_channels cc ON cr.custom_channel_id = cc.id
		JOIN channels ch ON cr.channel_id = ch.id
		LEFT JOIN suppliers s ON ch.supplier_id = s.id
		WHERE cc.is_active = 1 AND cr.is_active = 1 AND ch.status = 'active'
		AND cr.deleted_at IS NULL AND cc.deleted_at IS NULL AND ch.deleted_at IS NULL
	`).Scan(&rows)

	result := make(map[string]channelRoute, len(rows))
	for _, r := range rows {
		// 优先使用第一个匹配的路由
		if _, exists := result[r.AliasModel]; !exists {
			result[r.AliasModel] = channelRoute{
				ChannelID:    r.ChannelID,
				ChannelName:  r.ChannelName,
				SupplierName: r.SupplierName,
				ActualModel:  r.ActualModel,
				Endpoint:     r.Endpoint,
				APIKey:       r.APIKey,
				OrgID:        r.OrgID,
			}
		}
	}

	// Step 2: 对于在 custom_channel_routes 中未找到的模型，回退到 channel_models 自动发现
	// 收集未命中的模型名
	var missingModels []string
	for _, m := range models {
		if _, exists := result[m.ModelName]; !exists {
			missingModels = append(missingModels, m.ModelName)
		}
	}

	if len(missingModels) > 0 {
		// 从 channel_models 查找（与 SelectChannel 的 autoDiscoverByCost 逻辑对应）
		var cmRows []routeRow
		mc.db.WithContext(ctx).Raw(`
			SELECT cm.standard_model_id as alias_model, cm.vendor_model_id as actual_model,
				   ch.id as channel_id, ch.name as channel_name,
				   COALESCE(s.name, '') as supplier_name, ch.endpoint, ch.api_key,
				   COALESCE(JSON_UNQUOTE(JSON_EXTRACT(ch.custom_params, '$.client_secret')), '') as org_id
			FROM channel_models cm
			JOIN channels ch ON cm.channel_id = ch.id
			LEFT JOIN suppliers s ON ch.supplier_id = s.id
			WHERE cm.is_active = 1 AND ch.status = 'active'
			AND cm.standard_model_id IN ?
			AND ch.deleted_at IS NULL
		`, missingModels).Scan(&cmRows)

		for _, r := range cmRows {
			if _, exists := result[r.AliasModel]; !exists {
				result[r.AliasModel] = channelRoute{
					ChannelID:    r.ChannelID,
					ChannelName:  r.ChannelName,
					SupplierName: r.SupplierName,
					ActualModel:  r.ActualModel,
					Endpoint:     r.Endpoint,
					APIKey:       r.APIKey,
					OrgID:        r.OrgID,
				}
			}
		}
	}

	// Step 3: 仍未命中（如非 LLM 模型未在 channel_models 注册）→ 按 supplier_id 兜底
	// 用于阿里云 ImageGeneration/VideoGeneration/TTS/ASR 等模型未配置 channel_models 时，
	// 至少拿到供应商任意活跃渠道的 endpoint+APIKey，进入 checkSingleModel 的 skip 分支
	// （ASR/Video/TTS 等会被识别后跳过，而非"无可用渠道路由"直接失败）
	stillMissingByID := make(map[uint][]string) // supplier_id → []model_name
	stillMissingNames := make(map[string]uint)  // model_name → supplier_id（反查）
	for _, m := range models {
		if _, exists := result[m.ModelName]; exists {
			continue
		}
		if m.SupplierID == 0 {
			continue
		}
		stillMissingByID[m.SupplierID] = append(stillMissingByID[m.SupplierID], m.ModelName)
		stillMissingNames[m.ModelName] = m.SupplierID
	}

	if len(stillMissingByID) > 0 {
		supplierIDs := make([]uint, 0, len(stillMissingByID))
		for sid := range stillMissingByID {
			supplierIDs = append(supplierIDs, sid)
		}

		// 为每个供应商找一个活跃渠道（按 priority 优先，否则按 id）
		type fallbackRow struct {
			SupplierID   uint
			ChannelID    uint
			ChannelName  string
			SupplierName string
			Endpoint     string
			APIKey       string
			OrgID        string
		}
		var fbRows []fallbackRow
		mc.db.WithContext(ctx).Raw(`
			SELECT ch.supplier_id, ch.id as channel_id, ch.name as channel_name,
			       COALESCE(s.name, '') as supplier_name, ch.endpoint, ch.api_key,
			       COALESCE(JSON_UNQUOTE(JSON_EXTRACT(ch.custom_params, '$.client_secret')), '') as org_id
			FROM channels ch
			LEFT JOIN suppliers s ON ch.supplier_id = s.id
			WHERE ch.status = 'active' AND ch.deleted_at IS NULL
			AND ch.supplier_id IN ?
			ORDER BY ch.supplier_id, ch.priority DESC, ch.id ASC
		`, supplierIDs).Scan(&fbRows)

		// 每个 supplier 取首个匹配
		fbBySupplier := make(map[uint]fallbackRow, len(fbRows))
		for _, r := range fbRows {
			if _, exists := fbBySupplier[r.SupplierID]; !exists {
				fbBySupplier[r.SupplierID] = r
			}
		}

		// 应用兜底路由：actual_model 直接用 model_name（保持调用方原意）
		for modelName, sid := range stillMissingNames {
			if fb, ok := fbBySupplier[sid]; ok {
				if _, exists := result[modelName]; !exists {
					result[modelName] = channelRoute{
						ChannelID:    fb.ChannelID,
						ChannelName:  fb.ChannelName,
						SupplierName: fb.SupplierName,
						ActualModel:  modelName,
						Endpoint:     fb.Endpoint,
						APIKey:       fb.APIKey,
						OrgID:        fb.OrgID,
					}
				}
			}
		}
	}

	return result
}

// isSkipWorthyType 判断模型类型是否属于"无法自动探针、只能跳过"的类型
//
// 这些类型需要真实异步输入（视频帧/音频文件/query+documents），无法用最小化 HTTP 探针验证：
//   - VideoGeneration：任务异步，几十秒到几分钟
//   - ASR/SpeechRecognition/SpeechToText：需要上传真实音频文件
//   - Rerank：需要 query+documents 对
//   - Audio：通用音频类（不区分 TTS/ASR）
//
// 当这些类型的模型找不到路由时，不能因此标记为失败——应与有路由时的行为一致（跳过=可用）。
func isSkipWorthyType(modelType string) bool {
	switch strings.TrimSpace(modelType) {
	case "VideoGeneration", "ASR", "SpeechRecognition", "SpeechToText", "Rerank", "Audio":
		return true
	}
	return false
}

// inferModelTypeByName 根据模型名称推断类型（当数据库 model_type 为空时用作兜底）
// 覆盖常见图像/视频/TTS/ASR/Embedding/Rerank 等非 chat 模型命名模式
func inferModelTypeByName(name string) string {
	lower := strings.ToLower(name)

	// 视频生成：wan2-xxx-t2v / -i2v / -flf2v / sora / kling / pixverse / runway / cogvideo / seedance
	if strings.Contains(lower, "-t2v") || strings.Contains(lower, "-i2v") ||
		strings.Contains(lower, "-flf2v") || strings.Contains(lower, "sora") ||
		strings.Contains(lower, "kling") || strings.Contains(lower, "pixverse") ||
		strings.Contains(lower, "runway") || strings.Contains(lower, "wanx2") ||
		strings.Contains(lower, "cogvideo") || strings.Contains(lower, "seedance") ||
		(strings.Contains(lower, "wan2-") && strings.Contains(lower, "video")) {
		return "VideoGeneration"
	}

	// 腾讯混元视觉理解（Vision/VLM）：hunyuan-vision / hunyuan-turbo-vision
	// 注意：必须在 ImageGeneration 块之前，避免被 "vision" 泛化匹配误伤
	if strings.Contains(lower, "hunyuan") && strings.Contains(lower, "vision") {
		return "Vision"
	}

	// 图像生成：seedream / seededit / -t2i / qwen-image / z-image / flux /
	//           stable-diffusion / dall-e / midjourney / ideogram / wan*-image / cogview / hidream
	if strings.Contains(lower, "seedream") || strings.Contains(lower, "seededit") ||
		strings.Contains(lower, "-t2i") || strings.Contains(lower, "qwen-image") ||
		strings.Contains(lower, "z-image") || strings.Contains(lower, "flux") ||
		strings.Contains(lower, "stable-diffusion") || strings.Contains(lower, "dall-e") ||
		strings.Contains(lower, "midjourney") || strings.Contains(lower, "ideogram") ||
		strings.Contains(lower, "cogview") || strings.Contains(lower, "hidream") ||
		strings.Contains(lower, "gpt-image") ||
		(strings.Contains(lower, "wan") && strings.Contains(lower, "image")) {
		return "ImageGeneration"
	}

	// 语音合成 TTS：cosyvoice / sambert- / -tts / bytes-tts / minimax-speech / chattts
	if strings.Contains(lower, "cosyvoice") || strings.Contains(lower, "sambert-") ||
		strings.Contains(lower, "-tts") || strings.HasSuffix(lower, "-tts") ||
		strings.HasPrefix(lower, "tts-") || strings.Contains(lower, "bytes-tts") ||
		strings.Contains(lower, "minimax-speech") || strings.Contains(lower, "chattts") {
		return "TextToSpeech"
	}

	// 语音识别 ASR：paraformer / fun-asr / whisper / sensevoice / -asr
	if strings.Contains(lower, "paraformer") || strings.Contains(lower, "fun-asr") ||
		strings.Contains(lower, "whisper") || strings.Contains(lower, "sensevoice") ||
		strings.Contains(lower, "-asr") || strings.HasSuffix(lower, "-asr") {
		return "SpeechRecognition"
	}

	// Rerank：bge-reranker / -rerank / -reranker
	if strings.Contains(lower, "rerank") || strings.Contains(lower, "reranker") {
		return "Rerank"
	}

	// Embedding
	if strings.Contains(lower, "embedding") || strings.Contains(lower, "text-embed") ||
		strings.Contains(lower, "bge-") || strings.Contains(lower, "m3e-") {
		return "Embedding"
	}

	// 翻译专用模型：doubao-seed-translation 等
	if strings.Contains(lower, "translation") {
		return "Translation"
	}

	// 百度 ERNIE 系列
	// Embedding: ernie-embedding-v1 / ernie-text-embedding / tao-8k
	if strings.Contains(lower, "ernie-embedding") || strings.Contains(lower, "tao-8k") {
		return "Embedding"
	}
	// 推理/思考模型：ernie-x1（包含链式思考）→ 仍是 LLM，不单独分类
	// 其余 ernie-* 均为 LLM（ernie-4.0 / ernie-3.5 / ernie-speed / ernie-x1 etc.）

	return ""
}

// checkSingleModel 检测单个模型可用性（按模型类型分发到对应探针）
func (mc *ModelChecker) checkSingleModel(ctx context.Context, aiModel model.AIModel, route channelRoute) ModelCheckResult {
	base := ModelCheckResult{
		ModelID:      aiModel.ID,
		ModelName:    aiModel.ModelName,
		ChannelID:    route.ChannelID,
		ChannelName:  route.ChannelName,
		SupplierName: route.SupplierName,
	}

	modelNameLower := strings.ToLower(aiModel.ModelName)

	// 1. 归一化类型：优先使用数据库字段，兜底按模型名推断
	effectiveType := aiModel.ModelType
	if effectiveType == "" {
		effectiveType = inferModelTypeByName(aiModel.ModelName)
	}
	// 1.1 名称强信号覆盖：DB 中 model_type 可能被错误设置为 LLM/VLM/Vision（历史数据或
	// 模型同步时推断失误），导致 Embedding/TTS/ASR/Rerank/Translation/Image/Video 被错
	// 误发送到 Chat Completions 端点产生 400/404 误判失败。若名称命中非 chat 类型的
	// 强信号模式，优先使用推断值。参考 docs/batch_check_analysis_2026_04_17.md。
	if effectiveType == "LLM" || effectiveType == "VLM" || effectiveType == "Vision" {
		inferred := inferModelTypeByName(aiModel.ModelName)
		switch inferred {
		case "Embedding", "TextToSpeech", "SpeechRecognition", "Rerank", "Translation",
			"ImageGeneration", "VideoGeneration":
			effectiveType = inferred
		}
	}

	// 2. 按类型分发
	// 注意：数据库实际存储的 ModelType 值在 internal/model/ai_model.go 定义为
	//   LLM / Vision / Embedding / ImageGeneration / VideoGeneration / TTS / ASR / Rerank
	// 历史/兼容值还有: VLM / TextToSpeech / SpeechSynthesis / SpeechToText / SpeechRecognition
	// 下面 case 必须覆盖所有同义命名，否则会掉入 LLM 路径误判
	switch effectiveType {
	case "ImageGeneration":
		return mc.checkImageModel(ctx, aiModel, route, base)
	case "VideoGeneration":
		// 视频生成任务异步耗时 30s-10min，不适合批量检测
		base.Available = true
		base.Error = "跳过 VideoGeneration 模型（异步耗时长，暂不支持自动检测）"
		return base
	case "Embedding":
		return mc.checkEmbeddingModel(ctx, aiModel, route, base)
	case "TextToSpeech", "SpeechSynthesis", "TTS":
		return mc.checkTTSModel(ctx, aiModel, route, base)
	case "SpeechToText", "SpeechRecognition", "ASR":
		// ASR 需要真实音频文件做探针，跳过；仅检查渠道声明 ASR 能力即视为可用
		base.Available = true
		base.Error = "跳过 ASR 模型（需真实音频输入，暂不支持自动检测）"
		return base
	case "Rerank":
		// Rerank 需要 query+documents 输入做探针，跳过自动检测
		base.Available = true
		base.Error = "跳过 Rerank 模型（需 query+documents 输入，暂不支持自动检测）"
		return base
	case "Audio":
		// 通用 Audio 类型（既不是纯 TTS 也不是纯 ASR）跳过
		base.Available = true
		base.Error = "跳过 Audio 模型（暂不支持自动检测）"
		return base
	case "Translation":
		// 翻译模型（如 doubao-seed-translation）使用专用 API 格式（源语言/目标语言字段），
		// 不适合通过 Chat Completions 端点自动探测，标记为可用跳过。
		base.Available = true
		base.Error = "跳过 Translation 模型（需专用 API 参数，暂不支持自动检测）"
		return base
	}

	// 3. 跳过 realtime（实时流式）模型 — 不支持标准 HTTP 调用
	if strings.Contains(modelNameLower, "-realtime") {
		base.Available = true
		base.Error = "跳过 realtime 模型（仅支持 WebSocket 实时流式调用）"
		return base
	}

	// 4. TTS/语音合成模型 — 按模型名兜底分发到 TTS 探针
	if strings.Contains(modelNameLower, "-tts-") || strings.HasSuffix(modelNameLower, "-tts") {
		return mc.checkTTSModel(ctx, aiModel, route, base)
	}

	// 5. 跳过 ASR/语音识别模型（需真实音频，暂不自动检测）
	if strings.Contains(modelNameLower, "-asr-") || strings.HasSuffix(modelNameLower, "-asr") {
		base.Available = true
		base.Error = "跳过 ASR 模型（需真实音频输入，暂不支持自动检测）"
		return base
	}

	// 6. 其他类型但不是 LLM/VLM → 跳过
	if effectiveType != "" && effectiveType != "LLM" && effectiveType != "VLM" && effectiveType != "Vision" && effectiveType != "Reasoning" {
		base.Available = true
		base.Error = fmt.Sprintf("跳过 %s 类型模型（不支持 Chat Completion 检测）", effectiveType)
		return base
	}

	// 7. 百度文心一言特殊处理（OAuth2 + 自定义 endpoint 路径）
	endpointLowerCheck := strings.ToLower(route.Endpoint)
	if strings.Contains(endpointLowerCheck, "aip.baidubce.com") || strings.Contains(endpointLowerCheck, "wenxinworkshop") {
		return mc.checkWenxinModel(ctx, aiModel, route, base)
	}

	// 8. LLM/VLM → Chat Completion 探针
	result := base

	// 构造最小化 Chat Completion 请求
	// max_tokens 最小设为 10（部分模型如 omni 系列要求 >= 10）
	reqBody := map[string]interface{}{
		"model": route.ActualModel,
		"messages": []map[string]interface{}{
			{"role": "user", "content": "hi"},
		},
		"max_tokens": 10,
	}

	// qwen3 系列默认开启 thinking，非流式请求必须关闭
	if strings.Contains(modelNameLower, "qwen3") || strings.Contains(modelNameLower, "qwq") {
		reqBody["enable_thinking"] = false
	}

	// 火山引擎 thinking 模型需要显式关闭 thinking（格式不同于阿里云）
	if strings.Contains(modelNameLower, "doubao") && strings.Contains(modelNameLower, "thinking") {
		reqBody["thinking"] = map[string]string{"type": "disabled"}
	}

	// 仅支持流式的模型（如 qwq-plus），使用流式请求
	if strings.Contains(modelNameLower, "qwq") {
		reqBody["stream"] = true
	}
	bodyBytes, _ := json.Marshal(reqBody)

	// 拼接 URL：根据 endpoint 后缀选择正确路径
	// /v1 → /v1/chat/completions（OpenAI 标准）
	// /v2 → /v2/chat/completions（百度千帆 V2 / 兼容）
	// /v3 → /v3/chat/completions（火山引擎）
	// 其他 → 追加 /v1/chat/completions
	endpoint := strings.TrimRight(route.Endpoint, "/")
	var url string
	if strings.HasSuffix(endpoint, "/v1") || strings.HasSuffix(endpoint, "/v2") || strings.HasSuffix(endpoint, "/v3") {
		url = endpoint + "/chat/completions"
	} else {
		url = endpoint + "/v1/chat/completions"
	}
	checkCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(checkCtx, http.MethodPost, url, bytes.NewReader(bodyBytes))
	if err != nil {
		result.Error = fmt.Sprintf("创建请求失败: %v", err)
		return result
	}
	req.Header.Set("Authorization", "Bearer "+route.APIKey)
	req.Header.Set("Content-Type", "application/json")

	start := time.Now()

	// 支持 1 次重试（针对 EOF/连接中断等瞬时错误）
	var resp *http.Response
	for attempt := 0; attempt < 2; attempt++ {
		if attempt > 0 {
			time.Sleep(time.Duration(attempt) * time.Second) // 重试前等待
			req, _ = http.NewRequestWithContext(checkCtx, http.MethodPost, url, bytes.NewReader(bodyBytes))
			req.Header.Set("Authorization", "Bearer "+route.APIKey)
			req.Header.Set("Content-Type", "application/json")
		}
		resp, err = mc.httpClient.Do(req)
		if err == nil {
			break
		}
		// 仅对连接类错误重试（EOF、connection reset 等）
		if !strings.Contains(err.Error(), "EOF") && !strings.Contains(err.Error(), "connection reset") {
			break
		}
	}
	result.LatencyMs = time.Since(start).Milliseconds()

	if err != nil {
		result.Error = fmt.Sprintf("请求失败: %v", err)
		return result
	}
	defer resp.Body.Close()

	result.StatusCode = resp.StatusCode

	// 读取响应体（最多4KB）用于错误信息
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))

	// 2xx = 可用, 4xx 中的 model_not_found / 404 = 不可用, 其他看情况
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		result.Available = true
	} else if resp.StatusCode == 429 {
		// 速率限制 = 模型可用，只是被限流
		result.Available = true
		result.Error = "rate limited (model is available)"
	} else {
		result.Error = string(respBody)
		// 截断过长的错误信息
		if len(result.Error) > 500 {
			result.Error = result.Error[:500]
		}
	}

	return result
}

// checkImageModel 图像生成模型探针：按 endpoint 分发到 DashScope / OpenAI 兼容
//
// 设计目标：在不实际生成图片（避免计费）的前提下，验证模型+渠道链路可用：
//   - DashScope 异步：提交任务取 task_id，不轮询最终结果
//   - OpenAI 兼容同步：发送一个会被校验拒绝的最小请求（空 prompt），
//     通过 400 的错误类型区分"endpoint/model 可用但参数无效"和"model 不存在/认证失败"
func (mc *ModelChecker) checkImageModel(ctx context.Context, aiModel model.AIModel, route channelRoute, result ModelCheckResult) ModelCheckResult {
	endpointLower := strings.ToLower(route.Endpoint)

	// 阿里云 DashScope：独立异步端点（不经过 compatible-mode）
	if strings.Contains(endpointLower, "dashscope") {
		return mc.checkDashScopeImage(ctx, route, result)
	}

	// 默认按 OpenAI 兼容同步端点处理（火山引擎 /api/v3、OpenAI 等）
	return mc.checkOpenAICompatibleImage(ctx, route, result)
}

// checkDashScopeImage 阿里云 DashScope 图像生成探针（异步任务提交）
// 仅验证提交成功返回 task_id，不轮询最终生成结果，避免计费与长耗时
//
// 容错策略：DashScope 图像模型路由/参数多样（qwen-image 系列、wan2.x 系列、edit 系列
// 各自有不同的 endpoint 和必填参数），单一 text2image 探针无法覆盖所有模型。
// 因此本探针仅把【明确可证不可用】的状态（401/403 鉴权失败、404 model_not_found、5xx）
// 标记为 offline；其它（400 参数类错误、非鉴权的 InvalidParameter 如"url error"）
// 视为"探针不支持但模型可能可用"，保持 Available=true 以免误伤。
func (mc *ModelChecker) checkDashScopeImage(ctx context.Context, route channelRoute, result ModelCheckResult) ModelCheckResult {
	const submitURL = "https://dashscope.aliyuncs.com/api/v1/services/aigc/text2image/image-synthesis"

	// 最小化提交请求：DashScope 要求 input.prompt 非空
	reqBody := map[string]interface{}{
		"model": route.ActualModel,
		"input": map[string]interface{}{
			"prompt": "health probe",
		},
		"parameters": map[string]interface{}{
			"n":    1,
			"size": "512*512",
		},
	}
	bodyBytes, _ := json.Marshal(reqBody)

	checkCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(checkCtx, http.MethodPost, submitURL, bytes.NewReader(bodyBytes))
	if err != nil {
		result.Error = fmt.Sprintf("创建请求失败: %v", err)
		return result
	}
	req.Header.Set("Authorization", "Bearer "+route.APIKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-DashScope-Async", "enable")

	start := time.Now()
	resp, err := mc.httpClient.Do(req)
	result.LatencyMs = time.Since(start).Milliseconds()
	if err != nil {
		// 网络错误 → 不可用
		result.Error = fmt.Sprintf("请求失败: %v", err)
		return result
	}
	defer resp.Body.Close()

	result.StatusCode = resp.StatusCode
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	bodyStr := string(respBody)
	bodyLower := strings.ToLower(bodyStr)

	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		// 解析 output.task_id 确认任务已排队（endpoint+model+auth 全通）
		var parsed struct {
			Output struct {
				TaskID     string `json:"task_id"`
				TaskStatus string `json:"task_status"`
			} `json:"output"`
			Message string `json:"message"`
		}
		if err := json.Unmarshal(respBody, &parsed); err == nil && parsed.Output.TaskID != "" {
			result.Available = true
			return result
		}
		// 2xx 但没有 task_id → 可能是非致命提示；谨慎起见视为可用（不主动下线）
		result.Available = true
		result.Error = "probe ok (non-standard 2xx response: " + truncate(bodyStr, 200) + ")"
	case resp.StatusCode == 429:
		result.Available = true
		result.Error = "rate limited (model is available)"
	case resp.StatusCode == 401 || resp.StatusCode == 403:
		// 鉴权失败 → 确定不可用
		result.Error = fmt.Sprintf("auth failed: %s", truncate(bodyStr, 200))
	case resp.StatusCode == 404:
		// 明确 model not found → 不可用
		result.Error = fmt.Sprintf("model not found: %s", truncate(bodyStr, 200))
	case resp.StatusCode == 400:
		// 400 参数错误：区分是否为明确的 model_not_found
		if strings.Contains(bodyLower, "model_not_found") || strings.Contains(bodyLower, "model not found") ||
			strings.Contains(bodyLower, "invalidapikey") || strings.Contains(bodyLower, "invalid api key") {
			result.Error = fmt.Sprintf("model rejected: %s", truncate(bodyStr, 200))
			return result
		}
		// 其它 400（"url error" / 缺少 image 参数 / 模型要求 multimodal-generation 端点 等）
		// → 探针不覆盖此模型变体，不能证明不可用，保守放行
		result.Available = true
		result.Error = "probe skipped (endpoint/params mismatch for this model variant; trust catalog)"
	default:
		// 5xx → 不可用
		result.Error = truncate(bodyStr, 500)
	}

	return result
}

// truncate 截断字符串到指定长度
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	// 按字节截断时，向前退到合法 UTF-8 字符边界，避免截断多字节中文字符导致 DB 写入失败
	for n > 0 && (s[n]&0xC0) == 0x80 {
		n--
	}
	return s[:n]
}

// checkOpenAICompatibleImage OpenAI 兼容的图像生成端点探针（火山引擎 / OpenAI DALL-E）
//
// 策略：发送 prompt="" 的请求，期待 400 返回并包含 prompt 相关错误，
// 以此验证 endpoint+auth+model 通路，同时避免产生实际生成费用。
// 若某些供应商接受空 prompt 并生成（极少数），按 2xx 视为可用。
func (mc *ModelChecker) checkOpenAICompatibleImage(ctx context.Context, route channelRoute, result ModelCheckResult) ModelCheckResult {
	// 拼接 URL：/v1, /v2 或 /v3 保留，否则追加 /v1
	endpoint := strings.TrimRight(route.Endpoint, "/")
	var url string
	if strings.HasSuffix(endpoint, "/v1") || strings.HasSuffix(endpoint, "/v2") || strings.HasSuffix(endpoint, "/v3") {
		url = endpoint + "/images/generations"
	} else {
		url = endpoint + "/v1/images/generations"
	}

	// 空 prompt 触发参数校验失败，不会进入实际生成流程
	reqBody := map[string]interface{}{
		"model":  route.ActualModel,
		"prompt": "",
		"n":      1,
	}
	bodyBytes, _ := json.Marshal(reqBody)

	checkCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(checkCtx, http.MethodPost, url, bytes.NewReader(bodyBytes))
	if err != nil {
		result.Error = fmt.Sprintf("创建请求失败: %v", err)
		return result
	}
	req.Header.Set("Authorization", "Bearer "+route.APIKey)
	req.Header.Set("Content-Type", "application/json")

	start := time.Now()
	resp, err := mc.httpClient.Do(req)
	result.LatencyMs = time.Since(start).Milliseconds()
	if err != nil {
		result.Error = fmt.Sprintf("请求失败: %v", err)
		return result
	}
	defer resp.Body.Close()

	result.StatusCode = resp.StatusCode
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	bodyStr := string(respBody)
	bodyLower := strings.ToLower(bodyStr)

	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		// 极少数模型接受空 prompt 并生成 → 视为可用
		result.Available = true
	case resp.StatusCode == 429:
		result.Available = true
		result.Error = "rate limited (model is available)"
	case resp.StatusCode == 400:
		// 400：参数校验失败。若错误提到 prompt/input/empty 说明 endpoint+model+auth 均正常
		if strings.Contains(bodyLower, "prompt") || strings.Contains(bodyLower, "input") ||
			strings.Contains(bodyLower, "empty") || strings.Contains(bodyLower, "required") ||
			strings.Contains(bodyLower, "missing") || strings.Contains(bodyLower, "invalid_request") {
			result.Available = true
			result.Error = "probe ok (prompt validation rejected empty prompt)"
			return result
		}
		// 400 但错误不是 prompt 相关（如 model_not_found）→ 不可用
		result.Error = bodyStr
	default:
		result.Error = bodyStr
	}

	if len(result.Error) > 500 {
		result.Error = result.Error[:500]
	}
	return result
}

// checkEmbeddingModel Embedding 模型探针：调用 /embeddings 端点
func (mc *ModelChecker) checkEmbeddingModel(ctx context.Context, aiModel model.AIModel, route channelRoute, result ModelCheckResult) ModelCheckResult {
	endpoint := strings.TrimRight(route.Endpoint, "/")
	var url string
	if strings.HasSuffix(endpoint, "/v1") || strings.HasSuffix(endpoint, "/v2") || strings.HasSuffix(endpoint, "/v3") {
		url = endpoint + "/embeddings"
	} else {
		url = endpoint + "/v1/embeddings"
	}

	reqBody := map[string]interface{}{
		"model": route.ActualModel,
		"input": "hi",
	}
	bodyBytes, _ := json.Marshal(reqBody)

	checkCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(checkCtx, http.MethodPost, url, bytes.NewReader(bodyBytes))
	if err != nil {
		result.Error = fmt.Sprintf("创建请求失败: %v", err)
		return result
	}
	req.Header.Set("Authorization", "Bearer "+route.APIKey)
	req.Header.Set("Content-Type", "application/json")

	start := time.Now()
	resp, err := mc.httpClient.Do(req)
	result.LatencyMs = time.Since(start).Milliseconds()
	if err != nil {
		result.Error = fmt.Sprintf("请求失败: %v", err)
		return result
	}
	defer resp.Body.Close()

	result.StatusCode = resp.StatusCode
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))

	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		result.Available = true
	case resp.StatusCode == 429:
		result.Available = true
		result.Error = "rate limited (model is available)"
	default:
		result.Error = string(respBody)
		if len(result.Error) > 500 {
			result.Error = result.Error[:500]
		}
	}
	return result
}

// checkTTSModel TTS / 语音合成模型探针：调用 /audio/speech 端点
//
// 策略：发送最小化 TTS 请求（input="测试"），判断响应状态码：
//   - 2xx + 非空响应体 → 可用
//   - 429 → 可用（被限流）
//   - 400 若错误提示 voice/input 参数问题 → endpoint+model 通，视为可用
//   - 其他 → 不可用
func (mc *ModelChecker) checkTTSModel(ctx context.Context, aiModel model.AIModel, route channelRoute, result ModelCheckResult) ModelCheckResult {
	endpoint := strings.TrimRight(route.Endpoint, "/")
	endpointLower := strings.ToLower(endpoint)

	// 阿里云 DashScope 的 compatible-mode 没有实现 OpenAI 兼容的 /audio/speech 端点，
	// 请求会直接 404。qwen-tts / qwen3-tts 走原生 /api/v1/services/audio/tts/generation
	// 或多模态 /services/aigc/multimodal-generation/generation，参数与音色体系各不相同，
	// 单一探针难以覆盖。为避免误伤，对 DashScope 端点直接跳过探测，信任目录。
	if strings.Contains(endpointLower, "dashscope") {
		result.Available = true
		result.Error = "probe skipped (DashScope TTS uses native endpoints, not OpenAI-compat /audio/speech)"
		return result
	}

	var url string
	if strings.HasSuffix(endpoint, "/v1") || strings.HasSuffix(endpoint, "/v2") || strings.HasSuffix(endpoint, "/v3") {
		url = endpoint + "/audio/speech"
	} else {
		url = endpoint + "/v1/audio/speech"
	}

	reqBody := map[string]interface{}{
		"model":           route.ActualModel,
		"input":           "测试",
		"voice":           "alloy",
		"response_format": "mp3",
	}
	bodyBytes, _ := json.Marshal(reqBody)

	checkCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(checkCtx, http.MethodPost, url, bytes.NewReader(bodyBytes))
	if err != nil {
		result.Error = fmt.Sprintf("创建请求失败: %v", err)
		return result
	}
	req.Header.Set("Authorization", "Bearer "+route.APIKey)
	req.Header.Set("Content-Type", "application/json")

	start := time.Now()
	resp, err := mc.httpClient.Do(req)
	result.LatencyMs = time.Since(start).Milliseconds()
	if err != nil {
		result.Error = fmt.Sprintf("请求失败: %v", err)
		return result
	}
	defer resp.Body.Close()

	result.StatusCode = resp.StatusCode
	// 最多读 4KB 响应头用于错误诊断（TTS 成功响应是二进制音频，不需读完）
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	bodyLower := strings.ToLower(string(respBody))

	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		// Content-Type 应为 audio/*；或至少响应体非空
		ct := strings.ToLower(resp.Header.Get("Content-Type"))
		if strings.HasPrefix(ct, "audio/") || len(respBody) > 0 {
			result.Available = true
		} else {
			result.Available = false
			result.Error = "empty audio response"
		}
	case resp.StatusCode == 429:
		result.Available = true
		result.Error = "rate limited (model is available)"
	case resp.StatusCode == 400:
		// 400 若是参数校验（voice/input/format）→ 说明 endpoint+model+auth 正常
		if strings.Contains(bodyLower, "voice") || strings.Contains(bodyLower, "input") ||
			strings.Contains(bodyLower, "format") || strings.Contains(bodyLower, "required") ||
			strings.Contains(bodyLower, "invalid_request") {
			result.Available = true
			result.Error = "probe ok (param validation)"
			return result
		}
		result.Error = string(respBody)
	default:
		result.Error = string(respBody)
	}

	if len(result.Error) > 500 {
		result.Error = result.Error[:500]
	}
	return result
}

// ─────────────────────────────────────────────────────────────────────────────
// 百度文心一言探针（OAuth2 access_token 认证）
// ─────────────────────────────────────────────────────────────────────────────

// checkWenxinModel 百度文心一言模型探针
//
// 认证方式：client_credentials OAuth2
//   - channel.api_key  = client_id
//   - channel.custom_params.client_secret = client_secret（route.OrgID 字段携带）
//
// 若 OrgID 为空则尝试将 APIKey 按 "::" 分割解析（兼容旧格式：clientId::clientSecret）
func (mc *ModelChecker) checkWenxinModel(ctx context.Context, aiModel model.AIModel, route channelRoute, result ModelCheckResult) ModelCheckResult {
	clientID := route.APIKey
	clientSecret := route.OrgID

	// 兼容 "clientId::clientSecret" 一体格式
	if clientSecret == "" && strings.Contains(clientID, "::") {
		parts := strings.SplitN(clientID, "::", 2)
		clientID = parts[0]
		clientSecret = parts[1]
	}

	if clientID == "" || clientSecret == "" {
		result.Available = false
		result.Error = "文心一言渠道未配置 client_id/client_secret（需在渠道 custom_params 中设置 client_secret）"
		return result
	}

	// 1. 获取 access_token
	token, err := mc.getWenxinAccessToken(ctx, clientID, clientSecret)
	if err != nil {
		result.Available = false
		result.Error = fmt.Sprintf("文心 access_token 获取失败: %v", err)
		return result
	}

	// 2. 确定模型端点路径
	ep, ok := wenxinProbeEndpoints[route.ActualModel]
	if !ok {
		ep = "completions" // 默认端点
	}

	// 3. 构建 URL（使用标准百度文心端点）
	baseURL := "https://aip.baidubce.com/rpc/2.0/ai_custom/v1/wenxinworkshop/chat"
	url := fmt.Sprintf("%s/%s?access_token=%s", baseURL, ep, token)

	// 4. 构造最小请求体
	reqBody := map[string]interface{}{
		"messages": []map[string]interface{}{
			{"role": "user", "content": "hi"},
		},
	}
	bodyBytes, _ := json.Marshal(reqBody)

	checkCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(checkCtx, http.MethodPost, url, bytes.NewReader(bodyBytes))
	if err != nil {
		result.Error = fmt.Sprintf("创建请求失败: %v", err)
		return result
	}
	req.Header.Set("Content-Type", "application/json")

	start := time.Now()
	resp, err := mc.httpClient.Do(req)
	result.LatencyMs = time.Since(start).Milliseconds()
	if err != nil {
		result.Error = fmt.Sprintf("请求失败: %v", err)
		return result
	}
	defer resp.Body.Close()

	result.StatusCode = resp.StatusCode
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	bodyStr := string(respBody)

	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		var parsed struct {
			ErrorCode int    `json:"error_code"`
			ErrorMsg  string `json:"error_msg"`
		}
		_ = json.Unmarshal(respBody, &parsed)
		if parsed.ErrorCode != 0 {
			result.Error = fmt.Sprintf("业务错误 %d: %s", parsed.ErrorCode, parsed.ErrorMsg)
		} else {
			result.Available = true
		}
	case resp.StatusCode == 429:
		result.Available = true
		result.Error = "rate limited (model is available)"
	default:
		result.Error = truncate(bodyStr, 500)
	}
	return result
}

// getWenxinAccessToken 获取百度文心一言 OAuth2 access_token
func (mc *ModelChecker) getWenxinAccessToken(ctx context.Context, clientID, clientSecret string) (string, error) {
	url := fmt.Sprintf(
		"https://aip.baidubce.com/oauth/2.0/token?grant_type=client_credentials&client_id=%s&client_secret=%s",
		clientID, clientSecret,
	)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, nil)
	if err != nil {
		return "", err
	}
	resp, err := mc.httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	var tokenResp struct {
		AccessToken string `json:"access_token"`
		Error       string `json:"error"`
		ErrorDesc   string `json:"error_description"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&tokenResp); err != nil {
		return "", fmt.Errorf("decode token response: %w", err)
	}
	if tokenResp.Error != "" {
		return "", fmt.Errorf("%s: %s", tokenResp.Error, tokenResp.ErrorDesc)
	}
	return tokenResp.AccessToken, nil
}

// ─────────────────────────────────────────────────────────────────────────────
// 后台检测任务管理
// ─────────────────────────────────────────────────────────────────────────────

// CreateCheckTask 创建检测任务记录并立即在后台运行
func (mc *ModelChecker) CreateCheckTask(name, triggerType string) (uint, error) {
	task := &model.ModelCheckTask{
		Name:        name,
		TriggerType: triggerType,
		Status:      "pending",
	}
	if err := mc.db.Create(task).Error; err != nil {
		return 0, fmt.Errorf("创建检测任务失败: %w", err)
	}
	go mc.runCheckTask(task.ID)
	return task.ID, nil
}

// runCheckTask 后台执行全量检测并将结果写回任务记录
func (mc *ModelChecker) runCheckTask(taskID uint) {
	now := time.Now()
	mc.db.Model(&model.ModelCheckTask{}).Where("id = ?", taskID).Updates(map[string]interface{}{
		"status":     "running",
		"started_at": now,
	})

	ctx := context.Background()

	// 创建进度通道，启动 goroutine 将实时进度写入数据库（节流：每 2 秒最多写一次）
	progressCh := make(chan BatchCheckProgress, 100)
	safego.Go("model-checker-progress-writer", func() {
		var lastWrite time.Time
		for p := range progressCh {
			// 节流：2 秒内最多写一次，除非是最后一条（Checked == Total）
			if time.Since(lastWrite) < 2*time.Second && p.Checked < p.Total {
				continue
			}
			lastWrite = time.Now()
			pct := 0
			if p.Total > 0 {
				pct = p.Checked * 100 / p.Total
			}
			msg := fmt.Sprintf("已检测 %d/%d，可用 %d，失败 %d", p.Checked, p.Total, p.Available, p.Failed)
			mc.db.Model(&model.ModelCheckTask{}).Where("id = ?", taskID).Updates(map[string]interface{}{
				"total":          p.Total,
				"available":      p.Available,
				"failed_count":   p.Failed,
				"disabled_count": p.Disabled,
				"progress":       pct,
				"progress_msg":   msg,
			})
		}
	})

	results, err := mc.BatchCheck(ctx, progressCh)

	completedAt := time.Now()
	if err != nil {
		mc.db.Model(&model.ModelCheckTask{}).Where("id = ?", taskID).Updates(map[string]interface{}{
			"status":        "failed",
			"completed_at":  completedAt,
			"progress":      100,
			"progress_msg":  "任务失败: " + err.Error(),
			"error_message": err.Error(),
		})
		mc.logger.Error("后台检测任务失败", zap.Uint("task_id", taskID), zap.Error(err))
		return
	}

	summary := BuildDetailedSummary(results)
	resultJSON, _ := json.Marshal(summary)

	mc.db.Model(&model.ModelCheckTask{}).Where("id = ?", taskID).Updates(map[string]interface{}{
		"status":         "completed",
		"total":          summary.Total,
		"available":      summary.Available,
		"failed_count":   summary.Failed,
		"disabled_count": summary.Disabled,
		"completed_at":   completedAt,
		"progress":       100,
		"progress_msg":   fmt.Sprintf("检测完成，共 %d 个模型", summary.Total),
		"result_json":    string(resultJSON),
	})
	mc.logger.Info("后台检测任务完成",
		zap.Uint("task_id", taskID),
		zap.Int("total", summary.Total),
		zap.Int("available", summary.Available),
		zap.Int("failed", summary.Failed))
}

// GetCheckTasks 获取任务列表（分页，最新在前）
func (mc *ModelChecker) GetCheckTasks(ctx context.Context, page, pageSize int) ([]model.ModelCheckTask, int64, error) {
	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > 100 {
		pageSize = 20
	}
	var total int64
	if err := mc.db.WithContext(ctx).Model(&model.ModelCheckTask{}).Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var tasks []model.ModelCheckTask
	offset := (page - 1) * pageSize
	if err := mc.db.WithContext(ctx).
		Order("id DESC").
		Offset(offset).Limit(pageSize).
		Find(&tasks).Error; err != nil {
		return nil, 0, err
	}
	return tasks, total, nil
}

// GetCheckTaskDetail 获取任务详情（含完整结果和供应商分组）
func (mc *ModelChecker) GetCheckTaskDetail(ctx context.Context, taskID uint) (*model.ModelCheckTask, *CheckSummaryDetail, error) {
	var task model.ModelCheckTask
	if err := mc.db.WithContext(ctx).First(&task, taskID).Error; err != nil {
		return nil, nil, err
	}
	var summary *CheckSummaryDetail
	if task.ResultJSON != "" {
		summary = &CheckSummaryDetail{}
		if err := json.Unmarshal([]byte(task.ResultJSON), summary); err != nil {
			mc.logger.Warn("解析任务结果 JSON 失败", zap.Uint("task_id", taskID), zap.Error(err))
			summary = nil
		}
	}
	return &task, summary, nil
}
