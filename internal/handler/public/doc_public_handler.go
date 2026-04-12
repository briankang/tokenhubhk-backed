package public

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"tokenhub-server/internal/pkg/errcode"
	"tokenhub-server/internal/pkg/response"
	docsvc "tokenhub-server/internal/service/doc"
)

// DocPublicHandler 公开文档接口处理器（无需认证）
// 提供分类树、文档列表、文档详情、全文搜索等公开接口
type DocPublicHandler struct {
	docSvc     *docsvc.DocService
	catSvc     *docsvc.DocCategoryService
	articleSvc *docsvc.DocArticleService
}

// NewDocPublicHandler 创建公开文档Handler实例
func NewDocPublicHandler(docSvc *docsvc.DocService, catSvc *docsvc.DocCategoryService) *DocPublicHandler {
	return &DocPublicHandler{docSvc: docSvc, catSvc: catSvc}
}

// SetArticleService 设置文档文章服务（注入 DocArticleService）
func (h *DocPublicHandler) SetArticleService(svc *docsvc.DocArticleService) {
	h.articleSvc = svc
}

// Register 注册公开文档路由到路由组
// 路由结构：
//   - GET /categories            分类树（含文档数量和子分类）
//   - GET /categories/:slug/articles  分类下文档列表
//   - GET /articles/:slug        文档详情
//   - GET /search?q=keyword      全文搜索
//   - GET /                      已发布文档列表（旧接口兼容）
//   - GET /:slug                 文档详情（旧接口兼容）
func (h *DocPublicHandler) Register(rg *gin.RouterGroup) {
	// 新版文档接口
	rg.GET("/categories", h.GetCategoryTree)
	rg.GET("/categories/:slug/articles", h.GetArticlesByCategory)
	rg.GET("/articles/:slug", h.GetArticleBySlug)
	rg.GET("/search", h.SearchArticles)

	// 旧版兼容接口
	rg.GET("", h.ListDocs)
	rg.GET("/:slug", h.GetBySlug)
}

// GetCategoryTree 获取文档分类树 GET /docs/categories
// 返回嵌套结构：一级分类 → 子分类 → 文档列表
func (h *DocPublicHandler) GetCategoryTree(c *gin.Context) {
	tree, err := h.catSvc.GetCategoryTree(c.Request.Context())
	if err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, err.Error())
		return
	}
	response.Success(c, tree)
}

// GetArticlesByCategory 获取分类下的文档列表 GET /docs/categories/:slug/articles
func (h *DocPublicHandler) GetArticlesByCategory(c *gin.Context) {
	slug := c.Param("slug")
	if slug == "" {
		response.Error(c, http.StatusBadRequest, errcode.ErrBadRequest)
		return
	}

	articles, err := h.catSvc.GetArticlesByCategorySlug(c.Request.Context(), slug)
	if err != nil {
		response.Error(c, http.StatusNotFound, errcode.ErrNotFound)
		return
	}
	response.Success(c, articles)
}

// GetArticleBySlug 获取文档详情 GET /docs/articles/:slug
func (h *DocPublicHandler) GetArticleBySlug(c *gin.Context) {
	slug := c.Param("slug")
	if slug == "" {
		response.Error(c, http.StatusBadRequest, errcode.ErrBadRequest)
		return
	}

	if h.articleSvc != nil {
		article, err := h.articleSvc.GetBySlug(c.Request.Context(), slug)
		if err != nil {
			response.Error(c, http.StatusNotFound, errcode.ErrNotFound)
			return
		}
		response.Success(c, article)
		return
	}

	// 降级到旧 Doc 接口
	doc, err := h.docSvc.GetBySlug(c.Request.Context(), slug)
	if err != nil {
		response.Error(c, http.StatusNotFound, errcode.ErrNotFound)
		return
	}
	response.Success(c, doc)
}

// SearchArticles 全文搜索文档 GET /docs/search?q=keyword
// 搜索范围：标题、内容、摘要、标签
func (h *DocPublicHandler) SearchArticles(c *gin.Context) {
	keyword := c.Query("q")
	if keyword == "" {
		response.Error(c, http.StatusBadRequest, errcode.ErrBadRequest)
		return
	}

	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "20"))

	// 优先使用新的文章搜索
	if h.articleSvc != nil {
		articles, total, err := h.articleSvc.Search(c.Request.Context(), keyword, page, pageSize)
		if err != nil {
			response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, err.Error())
			return
		}
		response.PageResult(c, articles, total, page, pageSize)
		return
	}

	// 降级到旧搜索
	docs, total, err := h.docSvc.Search(c.Request.Context(), keyword, page, pageSize)
	if err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, err.Error())
		return
	}
	response.PageResult(c, docs, total, page, pageSize)
}

// ListDocs 获取已发布文档列表 GET /docs（旧接口兼容）
func (h *DocPublicHandler) ListDocs(c *gin.Context) {
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "50"))

	var categoryID *uint
	if v := c.Query("category_id"); v != "" {
		id, err := strconv.ParseUint(v, 10, 64)
		if err == nil {
			uid := uint(id)
			categoryID = &uid
		}
	}

	published := true
	docs, total, err := h.docSvc.List(c.Request.Context(), categoryID, &published, page, pageSize)
	if err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, err.Error())
		return
	}

	response.PageResult(c, docs, total, page, pageSize)
}

// GetBySlug 根据别名获取单篇已发布文档 GET /docs/:slug（旧接口兼容）
func (h *DocPublicHandler) GetBySlug(c *gin.Context) {
	slug := c.Param("slug")
	if slug == "" {
		response.Error(c, http.StatusBadRequest, errcode.ErrBadRequest)
		return
	}

	// 先在新的 DocArticle 表中查找
	if h.articleSvc != nil {
		article, err := h.articleSvc.GetBySlug(c.Request.Context(), slug)
		if err == nil {
			response.Success(c, article)
			return
		}
	}

	// 降级到旧 Doc 表查找
	doc, err := h.docSvc.GetBySlug(c.Request.Context(), slug)
	if err != nil {
		response.Error(c, http.StatusNotFound, errcode.ErrNotFound)
		return
	}
	response.Success(c, doc)
}
