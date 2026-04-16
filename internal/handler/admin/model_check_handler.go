package admin

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"

	"tokenhub-server/internal/pkg/errcode"
	"tokenhub-server/internal/pkg/response"
	aimodelsvc "tokenhub-server/internal/service/aimodel"
	"tokenhub-server/internal/taskqueue"
)

// ModelCheckHandler 模型可用性检测 API 处理器
// 当 bridge 不为 nil 时，重操作委派给 Worker 异步执行（三服务模式）；
// 当 bridge 为 nil 时，在本进程内执行（单体模式兼容）。
type ModelCheckHandler struct {
	checker *aimodelsvc.ModelChecker
	bridge  *taskqueue.SSEBridge // nil=单体模式（本地执行），非nil=委派给 Worker
}

// NewModelCheckHandler 创建模型检测处理器
func NewModelCheckHandler(checker *aimodelsvc.ModelChecker, bridge ...*taskqueue.SSEBridge) *ModelCheckHandler {
	h := &ModelCheckHandler{checker: checker}
	if len(bridge) > 0 {
		h.bridge = bridge[0]
	}
	return h
}

// BatchCheck 一键批量检测所有在线模型 POST /api/v1/admin/models/batch-check
// 使用 SSE 实时推送进度，最后返回完整结果
func (h *ModelCheckHandler) BatchCheck(c *gin.Context) {
	// 三服务模式：委派给 Worker
	if h.bridge != nil {
		h.bridge.PublishAndStream(c, taskqueue.TaskBatchCheck, taskqueue.BatchCheckPayload{})
		return
	}

	// 单体模式：本地执行（原有逻辑）
	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Header("X-Accel-Buffering", "no")

	progressCh := make(chan aimodelsvc.BatchCheckProgress, 100)

	type checkResult struct {
		results []aimodelsvc.ModelCheckResult
		err     error
	}
	resultCh := make(chan checkResult, 1)

	go func() {
		results, err := h.checker.BatchCheck(c.Request.Context(), progressCh)
		resultCh <- checkResult{results, err}
	}()

	flusher, ok := c.Writer.(http.Flusher)
	if !ok {
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, "streaming not supported")
		return
	}

	for progress := range progressCh {
		fmt.Fprintf(c.Writer, "data: {\"type\":\"progress\",\"total\":%d,\"checked\":%d,\"available\":%d,\"failed\":%d,\"disabled\":%d}\n\n",
			progress.Total, progress.Checked, progress.Available, progress.Failed, progress.Disabled)
		flusher.Flush()
	}

	res := <-resultCh
	if res.err != nil {
		fmt.Fprintf(c.Writer, "data: {\"type\":\"error\",\"message\":\"%s\"}\n\n", res.err.Error())
		flusher.Flush()
		return
	}

	available := 0
	failed := 0
	disabled := 0
	for _, r := range res.results {
		if r.Available {
			available++
		} else {
			failed++
		}
		if r.AutoDisabled {
			disabled++
		}
	}

	fmt.Fprintf(c.Writer, "data: {\"type\":\"done\",\"total\":%d,\"available\":%d,\"failed\":%d,\"disabled\":%d}\n\n",
		len(res.results), available, failed, disabled)
	flusher.Flush()
}

// BatchCheckSync 同步版本的批量检测（非SSE，等全部完成后一次返回）
// POST /api/v1/admin/models/batch-check-sync
func (h *ModelCheckHandler) BatchCheckSync(c *gin.Context) {
	// 三服务模式：委派给 Worker 并等待结果
	if h.bridge != nil {
		result, err := h.bridge.PublishAndWait(c.Request.Context(), taskqueue.TaskBatchCheck, taskqueue.BatchCheckPayload{})
		if err != nil {
			response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, err.Error())
			return
		}
		response.Success(c, json.RawMessage(result.Data))
		return
	}

	// 单体模式
	results, err := h.checker.BatchCheck(c.Request.Context(), nil)
	if err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, err.Error())
		return
	}

	summary := aimodelsvc.BuildDetailedSummary(results)
	response.Success(c, summary)
}

// PreviewSync 一键扫描预览（同步版） POST /api/v1/admin/models/check-preview-sync
//
// 与 BatchCheckSync 的核心区别：
//   - 自动跳过已被公告确认下线的模型（status=offline 且在 active model_deprecation 公告里）
//   - 不修改 ai_models.status / 不写 model_check_logs / 不创建公告（dry-run）
//   - 失败模型按详细错误分类返回到 PendingReview，等待管理员人工审核后调用 BulkDeprecate
//
// 返回三段：ConfirmedDeprecated（公告确认下线，跳过检测）/ Available（检测正常）/ PendingReview（待人工确认）
func (h *ModelCheckHandler) PreviewSync(c *gin.Context) {
	result, err := h.checker.BatchCheckPreview(c.Request.Context(), nil)
	if err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, err.Error())
		return
	}
	response.Success(c, result)
}

// Preview 一键扫描预览（SSE 实时进度版） POST /api/v1/admin/models/check-preview
//
// 客户端通过 SSE 接收检测进度，最后一条 data 是完整的 BatchCheckPreviewResult
func (h *ModelCheckHandler) Preview(c *gin.Context) {
	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Header("X-Accel-Buffering", "no")

	progressCh := make(chan aimodelsvc.BatchCheckProgress, 100)

	type previewResult struct {
		result *aimodelsvc.BatchCheckPreviewResult
		err    error
	}
	resultCh := make(chan previewResult, 1)

	go func() {
		r, err := h.checker.BatchCheckPreview(c.Request.Context(), progressCh)
		resultCh <- previewResult{r, err}
	}()

	flusher, ok := c.Writer.(http.Flusher)
	if !ok {
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, "streaming not supported")
		return
	}

	for progress := range progressCh {
		fmt.Fprintf(c.Writer, "data: {\"type\":\"progress\",\"total\":%d,\"checked\":%d,\"available\":%d,\"failed\":%d}\n\n",
			progress.Total, progress.Checked, progress.Available, progress.Failed)
		flusher.Flush()
	}

	res := <-resultCh
	if res.err != nil {
		fmt.Fprintf(c.Writer, "data: {\"type\":\"error\",\"message\":\"%s\"}\n\n", res.err.Error())
		flusher.Flush()
		return
	}

	// 用 JSON 序列化结果
	body, _ := json.Marshal(map[string]interface{}{
		"type":   "done",
		"result": res.result,
	})
	fmt.Fprintf(c.Writer, "data: %s\n\n", string(body))
	flusher.Flush()
}

// CheckSelected 检测用户勾选的指定模型 POST /api/v1/admin/models/check-selected
// Body: { "model_ids": [1, 2, 3] }
func (h *ModelCheckHandler) CheckSelected(c *gin.Context) {
	var req struct {
		ModelIDs []uint `json:"model_ids" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || len(req.ModelIDs) == 0 {
		response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrBadRequest.Code, "请选择要检测的模型")
		return
	}

	results, err := h.checker.CheckByIDs(c.Request.Context(), req.ModelIDs, nil)
	if err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, err.Error())
		return
	}

	summary := aimodelsvc.BuildDetailedSummary(results)
	response.Success(c, summary)
}

// GetCheckHistory 获取检测历史 GET /api/v1/admin/models/check-history
func (h *ModelCheckHandler) GetCheckHistory(c *gin.Context) {
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "50"))

	logs, total, err := h.checker.GetCheckHistory(c.Request.Context(), page, pageSize)
	if err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, err.Error())
		return
	}

	response.PageResult(c, logs, total, page, pageSize)
}

// CreateCheckTask 创建后台检测任务 POST /api/v1/admin/models/check-task
// 立即返回 task_id，后台异步运行全量检测
func (h *ModelCheckHandler) CreateCheckTask(c *gin.Context) {
	now := time.Now().Format("2006-01-02 15:04")
	name := "全量检测 " + now

	taskID, err := h.checker.CreateCheckTask(name, "manual")
	if err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, err.Error())
		return
	}
	response.Success(c, gin.H{"task_id": taskID, "name": name, "status": "pending"})
}

// GetCheckTasks 获取检测任务列表 GET /api/v1/admin/models/check-tasks
func (h *ModelCheckHandler) GetCheckTasks(c *gin.Context) {
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "20"))

	tasks, total, err := h.checker.GetCheckTasks(c.Request.Context(), page, pageSize)
	if err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, err.Error())
		return
	}
	response.PageResult(c, tasks, total, page, pageSize)
}

// GetCheckTaskDetail 获取检测任务详情（含供应商分组结果）GET /api/v1/admin/models/check-tasks/:id
func (h *ModelCheckHandler) GetCheckTaskDetail(c *gin.Context) {
	idStr := c.Param("id")
	id, err := strconv.ParseUint(idStr, 10, 64)
	if err != nil {
		response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrBadRequest.Code, "无效的任务 ID")
		return
	}

	task, summary, err := h.checker.GetCheckTaskDetail(c.Request.Context(), uint(id))
	if err != nil {
		response.ErrorMsg(c, http.StatusNotFound, errcode.ErrNotFound.Code, "任务不存在")
		return
	}

	response.Success(c, gin.H{
		"task":    task,
		"summary": summary,
	})
}

// GetLatestSummary 获取最近一次检测汇总（含错误分类和解决方案）
// GET /api/v1/admin/models/check-latest
func (h *ModelCheckHandler) GetLatestSummary(c *gin.Context) {
	logs, err := h.checker.GetLatestCheckSummary(c.Request.Context())
	if err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, err.Error())
		return
	}

	// 将日志转为 ModelCheckResult 以复用分类逻辑
	// 批量加载渠道和供应商名称
	channelIDs := make([]uint, 0, len(logs))
	for _, l := range logs {
		if l.ChannelID > 0 {
			channelIDs = append(channelIDs, l.ChannelID)
		}
	}
	type chInfo struct {
		ID           uint   `json:"id"`
		Name         string `json:"name"`
		SupplierName string `json:"supplier_name"`
	}
	chMap := make(map[uint]chInfo)
	if len(channelIDs) > 0 {
		var chInfos []chInfo
		h.checker.DB().Raw(`
			SELECT ch.id, ch.name, COALESCE(s.name, '') as supplier_name
			FROM channels ch LEFT JOIN suppliers s ON ch.supplier_id = s.id
			WHERE ch.id IN ?
		`, channelIDs).Scan(&chInfos)
		for _, ci := range chInfos {
			chMap[ci.ID] = ci
		}
	}

	var results []aimodelsvc.ModelCheckResult
	for _, l := range logs {
		r := aimodelsvc.ModelCheckResult{
			ModelID:      l.ModelID,
			ModelName:    l.ModelName,
			ChannelID:    l.ChannelID,
			Available:    l.Available,
			LatencyMs:    l.LatencyMs,
			StatusCode:   l.StatusCode,
			Error:        l.Error,
			AutoDisabled: l.AutoDisabled,
		}
		if ci, ok := chMap[l.ChannelID]; ok {
			r.ChannelName = ci.Name
			r.SupplierName = ci.SupplierName
		}
		results = append(results, r)
	}

	summary := aimodelsvc.BuildDetailedSummary(results)
	response.Success(c, summary)
}
