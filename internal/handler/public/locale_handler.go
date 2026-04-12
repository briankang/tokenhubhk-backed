package public

import (
	"github.com/gin-gonic/gin"

	"tokenhub-server/internal/pkg/response"
	"tokenhub-server/internal/service/geo"
)

// ========================================================================
// LocaleHandler — 语言检测接口
// 根据客户端 IP 地理位置自动推荐适合的语言代码，供前端首次访问时使用。
// ========================================================================

// LocaleHandler 语言检测请求处理器
type LocaleHandler struct {
	geoSvc *geo.GeoService
}

// NewLocaleHandler 创建语言检测处理器实例
// 参数:
//   - geoSvc: IP 地理位置服务
func NewLocaleHandler(geoSvc *geo.GeoService) *LocaleHandler {
	return &LocaleHandler{geoSvc: geoSvc}
}

// Register 注册语言检测路由
func (h *LocaleHandler) Register(rg *gin.RouterGroup) {
	rg.GET("/detect-locale", h.DetectLocale)
}

// DetectLocale 根据请求 IP 地址检测并返回建议的语言代码
// GET /api/v1/public/detect-locale
// 响应示例: {"code": 0, "data": {"locale": "zh", "country": "CN", "source": "ip-api"}}
func (h *LocaleHandler) DetectLocale(c *gin.Context) {
	// 获取客户端真实 IP（兼容代理场景）
	ip := geo.GetClientIP(c.Request)

	// 调用 GeoService 检测语言
	result := h.geoSvc.DetectLocale(c.Request.Context(), ip)

	response.Success(c, gin.H{
		"locale":  result.Locale,
		"country": result.CountryCode,
		"source":  result.Source,
	})
}
