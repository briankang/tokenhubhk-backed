package public

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"tokenhub-server/internal/pkg/response"
	"tokenhub-server/internal/service/exchange"
)

// ExchangeRateHandler 公开汇率接口
type ExchangeRateHandler struct {
	svc *exchange.ExchangeRateService
}

// NewExchangeRateHandler 构造函数
func NewExchangeRateHandler(svc *exchange.ExchangeRateService) *ExchangeRateHandler {
	return &ExchangeRateHandler{svc: svc}
}

// Register 注册公开路由
func (h *ExchangeRateHandler) Register(rg *gin.RouterGroup) {
	rg.GET("/exchange-rate", h.Get)
}

// exchangeRateResp 响应结构
type exchangeRateResp struct {
	USDToCNY  float64 `json:"usd_to_cny"`
	CNYToUSD  float64 `json:"cny_to_usd"`
	UpdatedAt string  `json:"updated_at"`
	Source    string  `json:"source"`
}

// Get GET /public/exchange-rate
func (h *ExchangeRateHandler) Get(c *gin.Context) {
	if h.svc == nil {
		c.JSON(http.StatusOK, response.R{Code: 0, Message: "ok", Data: exchangeRateResp{
			USDToCNY: 7.2, CNYToUSD: 0.139, Source: "default", UpdatedAt: time.Now().Format(time.RFC3339),
		}})
		return
	}
	rate, source, updatedAt, _ := h.svc.GetUSDToCNYWithMeta(c.Request.Context())
	cnyToUsd := 0.0
	if rate > 0 {
		cnyToUsd = 1 / rate
	}
	ts := ""
	if !updatedAt.IsZero() {
		ts = updatedAt.Format(time.RFC3339)
	} else {
		ts = time.Now().Format(time.RFC3339)
	}
	response.Success(c, exchangeRateResp{
		USDToCNY:  rate,
		CNYToUSD:  round4(cnyToUsd),
		UpdatedAt: ts,
		Source:    source,
	})
}

func round4(v float64) float64 {
	return float64(int64(v*10000+0.5)) / 10000
}
