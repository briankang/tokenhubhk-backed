package admin

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"tokenhub-server/internal/cron"
	"tokenhub-server/internal/pkg/errcode"
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
	rg.GET("/cron-task-runs", h.ListRuns)
	rg.GET("/cron-tasks/:name/runs", h.ListRuns)
	rg.PUT("/cron-tasks/:name/toggle", h.ToggleTask)
	rg.PUT("/cron-tasks/batch-toggle", h.BatchToggle)
}

// ListTasks 获取所有定时任务列表
// GET /api/v1/admin/cron-tasks
func (h *CronTaskHandler) ListTasks(c *gin.Context) {
	pageRaw := c.Query("page")
	pageSizeRaw := c.Query("page_size")
	search := c.Query("search")
	if pageRaw == "" && pageSizeRaw == "" && search == "" {
		tasks := h.scheduler.GetTasks()
		response.Success(c, tasks)
		return
	}

	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "20"))
	tasks, total := h.scheduler.ListTasks(search, page, pageSize)
	response.PageResult(c, tasks, total, page, pageSize)
}

// ListRuns 获取定时任务运行历史。
// GET /api/v1/admin/cron-task-runs
// GET /api/v1/admin/cron-tasks/:name/runs
func (h *CronTaskHandler) ListRuns(c *gin.Context) {
	name := c.Param("name")
	if name == "" {
		name = c.Query("task_name")
	}
	status := c.Query("status")
	search := c.Query("search")
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "20"))
	runs, total, err := h.scheduler.ListTaskRuns(name, status, search, page, pageSize)
	if err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, err.Error())
		return
	}
	response.PageResult(c, runs, total, page, pageSize)
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
