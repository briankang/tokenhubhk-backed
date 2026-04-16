package admin

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"tokenhub-server/internal/cron"
	"tokenhub-server/internal/pkg/response"
)

// CronTaskHandler 定时任务管理接口处理器
type CronTaskHandler struct {
	scheduler *cron.Scheduler
}

// NewCronTaskHandler 创建定时任务管理Handler实例
func NewCronTaskHandler(scheduler *cron.Scheduler) *CronTaskHandler {
	return &CronTaskHandler{scheduler: scheduler}
}

// Register 注册路由
func (h *CronTaskHandler) Register(rg *gin.RouterGroup) {
	rg.GET("/cron-tasks", h.ListTasks)
	rg.PUT("/cron-tasks/:name/toggle", h.ToggleTask)
	rg.PUT("/cron-tasks/batch-toggle", h.BatchToggle)
}

// ListTasks 获取所有定时任务列表
// GET /api/v1/admin/cron-tasks
func (h *CronTaskHandler) ListTasks(c *gin.Context) {
	tasks := h.scheduler.GetTasks()
	response.Success(c, tasks)
}

// toggleReq 启停请求体
type toggleReq struct {
	Enabled bool `json:"enabled"`
}

// ToggleTask 启用/禁用单个任务
// PUT /api/v1/admin/cron-tasks/:name/toggle
func (h *CronTaskHandler) ToggleTask(c *gin.Context) {
	name := c.Param("name")
	var req toggleReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if err := h.scheduler.SetTaskEnabled(name, req.Enabled); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
		return
	}
	response.Success(c, gin.H{"name": name, "enabled": req.Enabled})
}

// batchToggleReq 批量启停请求体
type batchToggleReq struct {
	Names   []string `json:"names" binding:"required"`
	Enabled bool     `json:"enabled"`
}

// BatchToggle 批量启用/禁用任务
// PUT /api/v1/admin/cron-tasks/batch-toggle
func (h *CronTaskHandler) BatchToggle(c *gin.Context) {
	var req batchToggleReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	results := make([]gin.H, 0)
	for _, name := range req.Names {
		err := h.scheduler.SetTaskEnabled(name, req.Enabled)
		if err != nil {
			results = append(results, gin.H{"name": name, "success": false, "error": err.Error()})
		} else {
			results = append(results, gin.H{"name": name, "success": true, "enabled": req.Enabled})
		}
	}
	response.Success(c, results)
}
