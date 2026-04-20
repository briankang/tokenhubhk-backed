package admin

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"tokenhub-server/internal/pkg/errcode"
	pkgredis "tokenhub-server/internal/pkg/redis"
	"tokenhub-server/internal/pkg/response"
	"tokenhub-server/internal/service/pricescraper"
)

// PriceScraperHandler 价格爬虫管理 handler
// 提供价格预览、应用更新、同步历史查询功能
type PriceScraperHandler struct {
	scraperService *pricescraper.PriceScraperService
}

// NewPriceScraperHandler 创建价格爬虫处理器实例
func NewPriceScraperHandler(scraperService *pricescraper.PriceScraperService) *PriceScraperHandler {
	return &PriceScraperHandler{
		scraperService: scraperService,
	}
}

// previewPricesRequest 预览价格变更的请求体
type previewPricesRequest struct {
	SupplierID uint `json:"supplier_id" binding:"required,min=1"`
}

// applyPricesRequest 应用价格更新的请求体
type applyPricesRequest struct {
	SupplierID uint                             `json:"supplier_id" binding:"required,min=1"`
	Updates    []pricescraper.PriceUpdateRequest `json:"updates" binding:"required,min=1"`
}

// PreviewPrices 预览价格变更（不写入数据库）
// POST /api/v1/admin/models/preview-prices
// 请求体: { "supplier_id": 1 }
// 响应: PriceDiffResult（爬取并对比后的价格差异）
// 注意: 爬虫请求可能耗时较长，已设置 2 分钟超时
func (h *PriceScraperHandler) PreviewPrices(c *gin.Context) {
	var req previewPricesRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Error(c, http.StatusBadRequest, errcode.ErrValidation)
		return
	}

	// 爬虫请求可能耗时较长，设置 2 分钟超时
	ctx, cancel := context.WithTimeout(c.Request.Context(), 2*time.Minute)
	defer cancel()

	result, err := h.scraperService.ScrapeAndPreview(ctx, req.SupplierID)
	if err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, err.Error())
		return
	}

	response.Success(c, result)
}

// ApplyPrices 应用价格更新
// POST /api/v1/admin/models/apply-prices
// 请求体: { "supplier_id": 1, "updates": [{ "model_id": 123, "input_cost_rmb": 0.003, "output_cost_rmb": 0.006 }] }
// 响应: ApplyResult（更新计数、跳过计数、错误信息）
// 前端确认后发送更新列表，事务写入数据库并记录同步日志
func (h *PriceScraperHandler) ApplyPrices(c *gin.Context) {
	var req applyPricesRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Error(c, http.StatusBadRequest, errcode.ErrValidation)
		return
	}

	result, err := h.scraperService.ApplyPrices(c.Request.Context(), req.SupplierID, req.Updates)
	if err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, err.Error())
		return
	}

	response.Success(c, result)
}

// GetSyncLogs 获取价格同步历史
// GET /api/v1/admin/models/price-sync-logs?supplier_id=1&page=1&page_size=20
// 响应: { "list": [...], "total": 45 }
// 支持按供应商筛选和分页查询，按同步时间倒序排列
func (h *PriceScraperHandler) GetSyncLogs(c *gin.Context) {
	// 解析分页参数
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "20"))
	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > 100 {
		pageSize = 20
	}

	// 供应商 ID（可选，0 表示查询全部）
	var supplierID uint
	if sid := c.Query("supplier_id"); sid != "" {
		if id, err := strconv.ParseUint(sid, 10, 64); err == nil {
			supplierID = uint(id)
		}
	}

	logs, total, err := h.scraperService.GetSyncLogs(c.Request.Context(), supplierID, page, pageSize)
	if err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, err.Error())
		return
	}

	response.PageResult(c, logs, total, page, pageSize)
}

// =====================================================
// 批量按模型ID精准抓取价格
// =====================================================

// batchScrapeRequest 批量抓取请求体
type batchScrapeRequest struct {
	ModelIDs []uint `json:"model_ids" binding:"required,min=1"`
}

// batchScrapeApplyRequest 应用请求体
type batchScrapeApplyRequest struct {
	TaskID string                    `json:"task_id" binding:"required"`
	Items  []pricescraper.AppliedItem `json:"items" binding:"required,min=1"`
}

// Redis key 规则
func batchScrapeResultKey(taskID string) string   { return "batch_scrape:result:" + taskID }
func batchScrapeDedupKey(hash string) string      { return "batch_scrape:dedup:" + hash }

// BatchScrape 批量按 model_ids 抓取价格（同步执行，返回完整结果）
// 单体/本地开发模式下直接同步调用；生产三服务模式可切换为 SSEBridge 委派（本 PR 先实现同步版本）
// POST /api/v1/admin/models/batch-scrape
func (h *PriceScraperHandler) BatchScrape(c *gin.Context) {
	var req batchScrapeRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Error(c, http.StatusBadRequest, errcode.ErrValidation)
		return
	}
	if len(req.ModelIDs) > 50 {
		response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrValidation.Code, "单次最多 50 个模型")
		return
	}

	// 去重：同一组 model_ids 60 秒内返回同一 task_id
	dedupHash := hashModelIDs(req.ModelIDs)
	ctx := c.Request.Context()
	var existingTaskID string
	if err := pkgredis.GetJSON(ctx, batchScrapeDedupKey(dedupHash), &existingTaskID); err == nil && existingTaskID != "" {
		// 命中已有结果
		var cached pricescraper.BatchScrapeResult
		if err := pkgredis.GetJSON(ctx, batchScrapeResultKey(existingTaskID), &cached); err == nil {
			response.Success(c, cached)
			return
		}
	}

	// 执行抓取（最长 3 分钟）
	scrapeCtx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()

	result, err := h.scraperService.ScrapeSpecificModels(scrapeCtx, req.ModelIDs, nil)
	if err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, err.Error())
		return
	}

	// 生成 TaskID 并缓存结果（30 分钟 TTL）
	result.TaskID = uuid.New().String()
	_ = pkgredis.SetJSON(ctx, batchScrapeResultKey(result.TaskID), result, 30*time.Minute)
	_ = pkgredis.SetJSON(ctx, batchScrapeDedupKey(dedupHash), result.TaskID, 60*time.Second)

	response.Success(c, result)
}

// GetBatchScrapeResult 查询已缓存的抓取结果
// GET /api/v1/admin/models/batch-scrape/:task_id/result
func (h *PriceScraperHandler) GetBatchScrapeResult(c *gin.Context) {
	taskID := c.Param("task_id")
	if taskID == "" {
		response.Error(c, http.StatusBadRequest, errcode.ErrValidation)
		return
	}
	var cached pricescraper.BatchScrapeResult
	if err := pkgredis.GetJSON(c.Request.Context(), batchScrapeResultKey(taskID), &cached); err != nil {
		response.ErrorMsg(c, http.StatusNotFound, errcode.ErrValidation.Code, "结果已过期或不存在")
		return
	}
	response.Success(c, cached)
}

// ApplyBatchScrape 应用用户勾选的字段
// POST /api/v1/admin/models/batch-scrape/apply
func (h *PriceScraperHandler) ApplyBatchScrape(c *gin.Context) {
	var req batchScrapeApplyRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Error(c, http.StatusBadRequest, errcode.ErrValidation)
		return
	}

	var cached pricescraper.BatchScrapeResult
	if err := pkgredis.GetJSON(c.Request.Context(), batchScrapeResultKey(req.TaskID), &cached); err != nil {
		response.ErrorMsg(c, http.StatusNotFound, errcode.ErrValidation.Code, "结果已过期，请重新抓取")
		return
	}

	report, err := h.scraperService.ApplySpecificItems(c.Request.Context(), &cached, req.Items)
	if err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, err.Error())
		return
	}
	response.Success(c, report)
}

// hashModelIDs 对模型 ID 列表排序后计算 sha1（用于去重 key）
func hashModelIDs(ids []uint) string {
	sorted := make([]uint, len(ids))
	copy(sorted, ids)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	h := sha1.New()
	for _, id := range sorted {
		fmt.Fprintf(h, "%d,", id)
	}
	return hex.EncodeToString(h.Sum(nil))
}
