package admin

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"tokenhub-server/internal/pkg/errcode"
	"tokenhub-server/internal/pkg/response"
	"tokenhub-server/internal/service/task"
)

// TaskHandler 后台任务管理 handler
type TaskHandler struct {
	taskService *task.TaskService
}

// NewTaskHandler 创建后台任务处理器
func NewTaskHandler(taskService *task.TaskService) *TaskHandler {
	return &TaskHandler{taskService: taskService}
}

// createTaskRequest 创建任务的请求体
type createTaskRequest struct {
	TaskType string                 `json:"task_type" binding:"required"`
	Params   map[string]interface{} `json:"params"`
}

// CreateTask 创建并执行后台任务
// POST /api/v1/admin/tasks
func (h *TaskHandler) CreateTask(c *gin.Context) {
	var req createTaskRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Error(c, http.StatusBadRequest, errcode.ErrValidation)
		return
	}

	// 获取操作者 ID
	var operatorID uint
	if id, ok := c.Get("userId"); ok {
		if uid, ok := id.(uint); ok {
			operatorID = uid
		}
	}

	t, err := h.taskService.CreateAndRun(req.TaskType, req.Params, operatorID)
	if err != nil {
		response.ErrorMsg(c, http.StatusConflict, errcode.ErrInternal.Code, err.Error())
		return
	}

	response.Success(c, t)
}

// ListTasks 查询任务列表
// GET /api/v1/admin/tasks?task_type=model_sync&page=1&page_size=20
func (h *TaskHandler) ListTasks(c *gin.Context) {
	taskType := c.Query("task_type")
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "20"))
	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > 100 {
		pageSize = 20
	}

	tasks, total, err := h.taskService.ListTasks(taskType, page, pageSize)
	if err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, err.Error())
		return
	}

	response.PageResult(c, tasks, total, page, pageSize)
}

// GetTask 获取单个任务详情
// GET /api/v1/admin/tasks/:id
func (h *TaskHandler) GetTask(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || id == 0 {
		response.Error(c, http.StatusBadRequest, errcode.ErrValidation)
		return
	}

	t, err := h.taskService.GetTask(uint(id))
	if err != nil {
		response.ErrorMsg(c, http.StatusNotFound, errcode.ErrNotFound.Code, "任务不存在")
		return
	}

	response.Success(c, t)
}

// CancelTask 取消运行中的任务
// POST /api/v1/admin/tasks/:id/cancel
func (h *TaskHandler) CancelTask(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || id == 0 {
		response.Error(c, http.StatusBadRequest, errcode.ErrValidation)
		return
	}

	if err := h.taskService.CancelTask(uint(id)); err != nil {
		response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrInternal.Code, err.Error())
		return
	}

	response.Success(c, gin.H{"message": "任务已取消"})
}

// applyTaskPricesRequest 应用价格的请求体
type applyTaskPricesRequest struct {
	ModelIDs []uint `json:"model_ids"` // 可选，为空则应用全部
}

// ApplyTaskPrices 应用价格抓取任务的结果
// POST /api/v1/admin/tasks/:id/apply-prices
func (h *TaskHandler) ApplyTaskPrices(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || id == 0 {
		response.Error(c, http.StatusBadRequest, errcode.ErrValidation)
		return
	}

	var req applyTaskPricesRequest
	_ = c.ShouldBindJSON(&req) // model_ids 可选

	result, err := h.taskService.ApplyTaskPrices(uint(id), req.ModelIDs)
	if err != nil {
		response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrInternal.Code, err.Error())
		return
	}

	response.Success(c, result)
}
