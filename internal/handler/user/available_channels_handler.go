package user

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"tokenhub-server/internal/model"
	"tokenhub-server/internal/pkg/errcode"
	"tokenhub-server/internal/pkg/response"
)

// AvailableChannelsHandler 用户可用渠道列表处理器
type AvailableChannelsHandler struct {
	db *gorm.DB
}

// NewAvailableChannelsHandler 创建用户可用渠道列表处理器
func NewAvailableChannelsHandler(db *gorm.DB) *AvailableChannelsHandler {
	return &AvailableChannelsHandler{db: db}
}

// Register 注册路由
func (h *AvailableChannelsHandler) Register(rg *gin.RouterGroup) {
	rg.GET("/available-channels", h.List)
}

// availableChannelInfo 用户可用渠道响应结构
type availableChannelInfo struct {
	ID          uint   `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	IsDefault   bool   `json:"is_default"`
}

// List 获取用户可用的自定义渠道
// GET /api/v1/user/available-channels
// 返回用户可见的所有自定义渠道（visibility=all 或 在 access_list 中）
// 响应: [{ id, name, description, is_default }]
func (h *AvailableChannelsHandler) List(c *gin.Context) {
	// 从上下文获取当前用户 ID（与 available_models_handler 保持一致的取值方式）
	userIDVal, exists := c.Get("userId")
	if !exists {
		response.Error(c, http.StatusUnauthorized, errcode.ErrUnauthorized)
		return
	}
	userID, ok := userIDVal.(uint)
	if !ok {
		response.Error(c, http.StatusUnauthorized, errcode.ErrUnauthorized)
		return
	}

	// 查询所有已激活的自定义渠道
	var allChannels []model.CustomChannel
	if err := h.db.WithContext(c.Request.Context()).
		Where("is_active = ?", true).
		Order("is_default DESC, name ASC").
		Find(&allChannels).Error; err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, err.Error())
		return
	}

	// 查询当前用户在访问控制列表中的渠道ID集合
	var accessRecords []model.CustomChannelAccess
	h.db.WithContext(c.Request.Context()).
		Where("user_id = ?", userID).
		Find(&accessRecords)

	// 构建用户有权限的渠道ID集合
	accessSet := make(map[uint]bool)
	for _, a := range accessRecords {
		accessSet[a.CustomChannelID] = true
	}

	// 过滤：visibility=all 的直接可见，visibility=specific 的需要在 access_list 中
	result := make([]availableChannelInfo, 0)
	for _, ch := range allChannels {
		if ch.Visibility == "all" || accessSet[ch.ID] {
			result = append(result, availableChannelInfo{
				ID:          ch.ID,
				Name:        ch.Name,
				Description: ch.Description,
				IsDefault:   ch.IsDefault,
			})
		}
	}

	response.Success(c, result)
}
