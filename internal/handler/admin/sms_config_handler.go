package admin

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"tokenhub-server/internal/model"
	"tokenhub-server/internal/pkg/errcode"
	"tokenhub-server/internal/pkg/response"
	smssvc "tokenhub-server/internal/service/sms"
)

type SMSConfigHandler struct {
	db  *gorm.DB
	svc *smssvc.Service
}

func NewSMSConfigHandler(db *gorm.DB, svc *smssvc.Service) *SMSConfigHandler {
	return &SMSConfigHandler{db: db, svc: svc}
}

func (h *SMSConfigHandler) Register(rg *gin.RouterGroup) {
	g := rg.Group("/security")
	g.GET("/sms-provider", h.GetSMSProvider)
	g.PUT("/sms-provider", h.UpsertSMSProvider)
	g.POST("/sms-provider/test", h.TestSMSProvider)
	g.GET("/captcha-provider", h.GetCaptchaProvider)
	g.PUT("/captcha-provider", h.UpsertCaptchaProvider)
	g.POST("/captcha-provider/test", h.TestCaptchaProvider)
	g.GET("/sms-risk", h.GetSMSRisk)
	g.PUT("/sms-risk", h.UpdateSMSRisk)
	g.GET("/sms-logs", h.ListSMSLogs)
	g.GET("/phone-risk-rules", h.ListPhoneRiskRules)
	g.POST("/phone-risk-rules", h.CreatePhoneRiskRule)
	g.DELETE("/phone-risk-rules/:id", h.DeletePhoneRiskRule)
}

func (h *SMSConfigHandler) GetSMSProvider(c *gin.Context) {
	dto, err := h.svc.GetSMSProvider(c.Request.Context())
	if err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, err.Error())
		return
	}
	response.Success(c, dto)
}

func (h *SMSConfigHandler) UpsertSMSProvider(c *gin.Context) {
	var req smssvc.SMSProviderUpsert
	if err := c.ShouldBindJSON(&req); err != nil {
		response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrBadRequest.Code, err.Error())
		return
	}
	dto, err := h.svc.UpsertSMSProvider(c.Request.Context(), req)
	if err != nil {
		response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrValidation.Code, err.Error())
		return
	}
	response.Success(c, dto)
}

func (h *SMSConfigHandler) TestSMSProvider(c *gin.Context) {
	var req struct {
		Phone string `json:"phone" binding:"required"`
		Code  string `json:"code"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrBadRequest.Code, err.Error())
		return
	}
	result, err := h.svc.TestSMSProvider(c.Request.Context(), req.Phone, req.Code)
	if err != nil {
		response.ErrorMsg(c, http.StatusBadGateway, errcode.ErrThirdParty.Code, err.Error())
		return
	}
	response.Success(c, result)
}

func (h *SMSConfigHandler) GetCaptchaProvider(c *gin.Context) {
	dto, err := h.svc.GetCaptchaProvider(c.Request.Context())
	if err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, err.Error())
		return
	}
	response.Success(c, dto)
}

func (h *SMSConfigHandler) UpsertCaptchaProvider(c *gin.Context) {
	var req smssvc.CaptchaProviderUpsert
	if err := c.ShouldBindJSON(&req); err != nil {
		response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrBadRequest.Code, err.Error())
		return
	}
	dto, err := h.svc.UpsertCaptchaProvider(c.Request.Context(), req)
	if err != nil {
		response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrValidation.Code, err.Error())
		return
	}
	response.Success(c, dto)
}

func (h *SMSConfigHandler) TestCaptchaProvider(c *gin.Context) {
	var req struct {
		CaptchaVerifyParam string `json:"captcha_verify_param" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrBadRequest.Code, err.Error())
		return
	}
	result, err := h.svc.TestCaptchaProvider(c.Request.Context(), req.CaptchaVerifyParam)
	if err != nil {
		response.ErrorMsg(c, http.StatusBadGateway, errcode.ErrThirdParty.Code, err.Error())
		return
	}
	response.Success(c, result)
}

func (h *SMSConfigHandler) GetSMSRisk(c *gin.Context) {
	response.Success(c, h.svc.GetRiskConfig(c.Request.Context()))
}

func (h *SMSConfigHandler) UpdateSMSRisk(c *gin.Context) {
	var req model.SMSRiskConfig
	if err := c.ShouldBindJSON(&req); err != nil {
		response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrBadRequest.Code, err.Error())
		return
	}
	if err := h.svc.UpdateRiskConfig(c.Request.Context(), &req); err != nil {
		response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrValidation.Code, err.Error())
		return
	}
	response.Success(c, h.svc.GetRiskConfig(c.Request.Context()))
}

func (h *SMSConfigHandler) ListSMSLogs(c *gin.Context) {
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "20"))
	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > 100 {
		pageSize = 20
	}
	q := h.db.WithContext(c.Request.Context()).Model(&model.SMSSendLog{})
	if phone := c.Query("phone"); phone != "" {
		q = q.Where("phone_e164 LIKE ? OR masked_phone LIKE ?", "%"+phone+"%", "%"+phone+"%")
	}
	if status := c.Query("status"); status != "" {
		q = q.Where("status = ?", status)
	}
	var total int64
	q.Count(&total)
	var rows []model.SMSSendLog
	if err := q.Order("id DESC").Offset((page - 1) * pageSize).Limit(pageSize).Find(&rows).Error; err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, err.Error())
		return
	}
	response.PageResult(c, rows, total, page, pageSize)
}

func (h *SMSConfigHandler) ListPhoneRiskRules(c *gin.Context) {
	var rows []model.PhoneRiskRule
	if err := h.db.WithContext(c.Request.Context()).Where("is_active = ?", true).Order("id DESC").Find(&rows).Error; err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, err.Error())
		return
	}
	response.Success(c, rows)
}

func (h *SMSConfigHandler) CreatePhoneRiskRule(c *gin.Context) {
	var req struct {
		RuleType string `json:"rule_type" binding:"required"`
		Pattern  string `json:"pattern" binding:"required"`
		Reason   string `json:"reason"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrBadRequest.Code, err.Error())
		return
	}
	row := model.PhoneRiskRule{RuleType: req.RuleType, Pattern: req.Pattern, Reason: req.Reason, IsActive: true}
	if err := h.db.WithContext(c.Request.Context()).Create(&row).Error; err != nil {
		response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrBadRequest.Code, err.Error())
		return
	}
	response.Success(c, row)
}

func (h *SMSConfigHandler) DeletePhoneRiskRule(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || id == 0 {
		response.Error(c, http.StatusBadRequest, errcode.ErrBadRequest)
		return
	}
	if err := h.db.WithContext(c.Request.Context()).Model(&model.PhoneRiskRule{}).Where("id = ?", id).Update("is_active", false).Error; err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, err.Error())
		return
	}
	response.Success(c, gin.H{"id": id})
}
