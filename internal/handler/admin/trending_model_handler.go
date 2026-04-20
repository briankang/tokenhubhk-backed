package admin

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"tokenhub-server/internal/model"
	"tokenhub-server/internal/pkg/errcode"
	"tokenhub-server/internal/pkg/response"
)

// TrendingModelHandler 全球热门模型参考库管理
type TrendingModelHandler struct {
	db *gorm.DB
}

func NewTrendingModelHandler(db *gorm.DB) *TrendingModelHandler {
	return &TrendingModelHandler{db: db}
}

func (h *TrendingModelHandler) Register(rg *gin.RouterGroup) {
	rg.GET("/trending-models", h.List)
	rg.POST("/trending-models", h.Create)
	rg.PUT("/trending-models/:id", h.Update)
	rg.DELETE("/trending-models/:id", h.Delete)
}

// List GET /admin/trending-models?supplier=OpenAI&sort=launch_date&page=1&page_size=50&keyword=gpt
func (h *TrendingModelHandler) List(c *gin.Context) {
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	if page <= 0 {
		page = 1
	}
	pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "200"))
	if pageSize <= 0 || pageSize > 500 {
		pageSize = 200
	}

	supplier := strings.TrimSpace(c.Query("supplier"))
	sortBy := c.DefaultQuery("sort", "launch_date")
	keyword := strings.TrimSpace(c.Query("keyword"))

	query := h.db.Model(&model.TrendingModel{})
	if supplier != "" {
		query = query.Where("supplier_name = ?", supplier)
	}
	if keyword != "" {
		like := "%" + keyword + "%"
		query = query.Where("model_name LIKE ? OR display_name LIKE ? OR description LIKE ?", like, like, like)
	}

	switch sortBy {
	case "stars":
		query = query.Order("popularity_stars DESC, launch_year_month DESC")
	default:
		query = query.Order("launch_year_month DESC, popularity_stars DESC")
	}

	var total int64
	query.Count(&total)

	var items []model.TrendingModel
	query.Offset((page - 1) * pageSize).Limit(pageSize).Find(&items)

	response.PageResult(c, items, total, page, pageSize)
}

// Create POST /admin/trending-models
func (h *TrendingModelHandler) Create(c *gin.Context) {
	var req model.TrendingModel
	if err := c.ShouldBindJSON(&req); err != nil {
		response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrBadRequest.Code, err.Error())
		return
	}
	if req.ModelName == "" || req.SupplierName == "" || req.SourceURL == "" {
		response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrBadRequest.Code, "model_name, supplier_name, source_url 不能为空")
		return
	}
	if req.PopularityStars < 1 || req.PopularityStars > 5 {
		req.PopularityStars = 3
	}
	if err := h.db.Create(&req).Error; err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, "创建失败: "+err.Error())
		return
	}
	response.Success(c, req)
}

// Update PUT /admin/trending-models/:id
func (h *TrendingModelHandler) Update(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))
	if id <= 0 {
		response.Error(c, http.StatusBadRequest, errcode.ErrBadRequest)
		return
	}
	var req map[string]interface{}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrBadRequest.Code, err.Error())
		return
	}
	if err := h.db.Model(&model.TrendingModel{}).Where("id = ?", id).Updates(req).Error; err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, "更新失败")
		return
	}
	response.Success(c, nil)
}

// Delete DELETE /admin/trending-models/:id
func (h *TrendingModelHandler) Delete(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))
	if id <= 0 {
		response.Error(c, http.StatusBadRequest, errcode.ErrBadRequest)
		return
	}
	if err := h.db.Delete(&model.TrendingModel{}, id).Error; err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, "删除失败")
		return
	}
	response.Success(c, nil)
}
