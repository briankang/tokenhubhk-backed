package public

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"tokenhub-server/internal/pkg/response"
	announcementsvc "tokenhub-server/internal/service/announcement"
)

// ========================================================================
// AnnouncementPublicHandler — 公开 Banner 公告（无需认证，带缓存）
// 供前端 dashboard 滚动条展示使用
// ========================================================================

// AnnouncementPublicHandler 公开公告处理器
type AnnouncementPublicHandler struct {
	svc *announcementsvc.Service
}

// NewAnnouncementPublicHandler 创建处理器实例
func NewAnnouncementPublicHandler(db *gorm.DB) *AnnouncementPublicHandler {
	return &AnnouncementPublicHandler{svc: announcementsvc.NewService(db)}
}

// RegisterPublicBanner 注册公开 banner 路由（带缓存中间件的 publicGroup 下）
func (h *AnnouncementPublicHandler) RegisterPublicBanner(rg *gin.RouterGroup) {
	rg.GET("/announcements/banners", h.GetBanners)
}

// GetBanners GET /public/announcements/banners
// 返回当前活跃的、允许展示为 Banner 的公告列表（按优先级排序）
func (h *AnnouncementPublicHandler) GetBanners(c *gin.Context) {
	banners, err := h.svc.GetActiveBanners(c.Request.Context())
	if err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, 50001, "查询失败")
		return
	}
	response.Success(c, banners)
}
