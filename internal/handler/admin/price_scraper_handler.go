package admin

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"

	"tokenhub-server/internal/pkg/errcode"
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
