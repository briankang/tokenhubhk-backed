package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"tokenhub-server/internal/cron"
	"tokenhub-server/internal/database"
	"tokenhub-server/internal/model"
	"tokenhub-server/internal/pkg/response"
	aimodelsvc "tokenhub-server/internal/service/aimodel"
	"tokenhub-server/internal/taskqueue"
)

// CapabilityTestHandler 模型能力测试管理后台 API
type CapabilityTestHandler struct {
	db       *gorm.DB
	tester   *aimodelsvc.CapabilityTester
	baseline *aimodelsvc.BaselineService
	bridge   *taskqueue.SSEBridge // 非 nil 时委派给 Worker
}

func NewCapabilityTestHandler(
	db *gorm.DB,
	tester *aimodelsvc.CapabilityTester,
	baseline *aimodelsvc.BaselineService,
	bridge ...*taskqueue.SSEBridge,
) *CapabilityTestHandler {
	h := &CapabilityTestHandler{db: db, tester: tester, baseline: baseline}
	if len(bridge) > 0 {
		h.bridge = bridge[0]
	}
	return h
}

// Register 注册路由到 /admin/capability-test/*
func (h *CapabilityTestHandler) Register(rg *gin.RouterGroup) {
	g := rg.Group("/capability-test")
	g.GET("/cases", h.ListCases)
	g.POST("/cases", h.CreateCase)
	g.PUT("/cases/:id", h.UpdateCase)
	g.DELETE("/cases/:id", h.DeleteCase)
	g.POST("/cases/seed", h.SeedCases)
	g.POST("/estimate", h.Estimate)
	g.POST("/run", h.Run)
	g.GET("/untested-count", h.UntestedCount)
	g.POST("/run-untested", h.RunUntested)
	g.GET("/hot-sample-info", h.HotSampleInfo)
	g.POST("/run-hot-sample", h.RunHotSample)
	g.GET("/tasks", h.ListTasks)
	g.GET("/tasks/:id", h.GetTask)
	g.GET("/tasks/:id/results", h.GetTaskResults)
	g.GET("/tasks/:id/suggestions", h.GetSuggestions)
	g.POST("/tasks/:id/apply", h.ApplySuggestions)
	g.POST("/tasks/:id/auto-apply", h.AutoApply)
	g.POST("/tasks/:id/propagate", h.PropagateToModels)
	g.POST("/tasks/:id/promote-baseline", h.PromoteBaseline)
	g.GET("/tasks/:id/regressions", h.GetRegressions)
	g.GET("/baselines", h.ListBaselines)
	g.DELETE("/baselines/:id", h.DeleteBaseline)
}

// ===== Cases CRUD =====

func (h *CapabilityTestHandler) ListCases(c *gin.Context) {
	var items []model.CapabilityTestCase
	q := h.db.Order("priority asc, id asc")
	if category := c.Query("category"); category != "" {
		q = q.Where("category = ?", category)
	}
	if mt := c.Query("model_type"); mt != "" {
		q = q.Where("model_type = ?", mt)
	}
	if enabled := c.Query("enabled"); enabled != "" {
		q = q.Where("enabled = ?", enabled == "true")
	}
	if err := q.Find(&items).Error; err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, 50001, err.Error())
		return
	}
	response.Success(c, gin.H{"list": items, "total": len(items)})
}

func (h *CapabilityTestHandler) CreateCase(c *gin.Context) {
	var tc model.CapabilityTestCase
	if err := c.ShouldBindJSON(&tc); err != nil {
		response.ErrorMsg(c, http.StatusBadRequest, 40001, err.Error())
		return
	}
	if tc.Name == "" || tc.RequestTemplate == "" {
		response.ErrorMsg(c, http.StatusBadRequest, 40002, "name 和 request_template 必填")
		return
	}
	if tc.Assertions == "" {
		tc.Assertions = "[]"
	}
	if err := h.db.Create(&tc).Error; err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, 50001, err.Error())
		return
	}
	response.Success(c, tc)
}

func (h *CapabilityTestHandler) UpdateCase(c *gin.Context) {
	id, _ := strconv.ParseUint(c.Param("id"), 10, 64)
	var tc model.CapabilityTestCase
	if err := h.db.First(&tc, id).Error; err != nil {
		response.ErrorMsg(c, http.StatusNotFound, 40401, "用例不存在")
		return
	}
	var update model.CapabilityTestCase
	if err := c.ShouldBindJSON(&update); err != nil {
		response.ErrorMsg(c, http.StatusBadRequest, 40001, err.Error())
		return
	}
	update.ID = tc.ID
	if err := h.db.Save(&update).Error; err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, 50001, err.Error())
		return
	}
	response.Success(c, update)
}

func (h *CapabilityTestHandler) DeleteCase(c *gin.Context) {
	id, _ := strconv.ParseUint(c.Param("id"), 10, 64)
	if err := h.db.Delete(&model.CapabilityTestCase{}, id).Error; err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, 50001, err.Error())
		return
	}
	response.Success(c, gin.H{"deleted": true})
}

func (h *CapabilityTestHandler) SeedCases(c *gin.Context) {
	if err := database.ResetSeedCapabilityCases(h.db); err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, 50001, err.Error())
		return
	}
	response.Success(c, gin.H{"reset": true})
}

// ===== Estimate / Run =====

type runRequest struct {
	ModelIDs  []uint `json:"model_ids"`
	CaseIDs   []uint `json:"case_ids"`
	AutoApply bool   `json:"auto_apply"` // 完成后自动应用高置信度建议
}

func (h *CapabilityTestHandler) Estimate(c *gin.Context) {
	var req runRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.ErrorMsg(c, http.StatusBadRequest, 40001, err.Error())
		return
	}
	est, err := h.tester.EstimateCost(c.Request.Context(), req.ModelIDs, req.CaseIDs)
	if err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, 50001, err.Error())
		return
	}
	response.Success(c, est)
}

// Run 创建任务并触发执行（SSE 流式）
func (h *CapabilityTestHandler) Run(c *gin.Context) {
	var req runRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.ErrorMsg(c, http.StatusBadRequest, 40001, err.Error())
		return
	}

	// 1. 创建任务记录
	modelIDsJSON, _ := json.Marshal(req.ModelIDs)
	caseIDsJSON, _ := json.Marshal(req.CaseIDs)
	uid, _ := c.Get("user_id")
	triggeredBy := uint(0)
	if v, ok := uid.(uint); ok {
		triggeredBy = v
	}
	task := model.CapabilityTestTask{
		Status:      "pending",
		ModelIDs:    string(modelIDsJSON),
		CaseIDs:     string(caseIDsJSON),
		TriggeredBy: triggeredBy,
		AutoApply:   req.AutoApply,
	}
	if err := h.db.Create(&task).Error; err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, 50001, err.Error())
		return
	}

	// 2. 委派或本地执行
	payload := taskqueue.CapabilityTestPayload{
		TaskID:               task.ID,
		ModelIDs:             req.ModelIDs,
		CaseIDs:              req.CaseIDs,
		AutoApplySuggestions: req.AutoApply,
		SkipKnownDisabled:    req.AutoApply, // 自动模式下跳过已知不可用能力
	}

	if h.bridge != nil {
		h.bridge.PublishAndStream(c, taskqueue.TaskCapabilityTest, payload)
		return
	}

	// 单体模式：本地 goroutine 执行，立即返回任务 ID
	go func() {
		_ = h.tester.RunTests(context.Background(), aimodelsvc.RunTestsInput{
			TaskID:               task.ID,
			ModelIDs:             req.ModelIDs,
			CaseIDs:              req.CaseIDs,
			AutoApplySuggestions: req.AutoApply,
			SkipKnownDisabled:    req.AutoApply,
		}, nil)
	}()
	response.Success(c, gin.H{"task_id": task.ID, "status": "running"})
}

// ===== Tasks =====

func (h *CapabilityTestHandler) ListTasks(c *gin.Context) {
	var items []model.CapabilityTestTask
	if err := h.db.Order("id desc").Limit(100).Find(&items).Error; err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, 50001, err.Error())
		return
	}
	response.Success(c, gin.H{"list": items, "total": len(items)})
}

func (h *CapabilityTestHandler) GetTask(c *gin.Context) {
	id, _ := strconv.ParseUint(c.Param("id"), 10, 64)
	var task model.CapabilityTestTask
	if err := h.db.First(&task, id).Error; err != nil {
		response.ErrorMsg(c, http.StatusNotFound, 40401, "任务不存在")
		return
	}
	response.Success(c, task)
}

func (h *CapabilityTestHandler) GetTaskResults(c *gin.Context) {
	id, _ := strconv.ParseUint(c.Param("id"), 10, 64)
	var items []model.CapabilityTestResult
	q := h.db.Where("task_id = ?", id).Order("id asc")
	if status := c.Query("status"); status != "" {
		q = q.Where("status = ?", status)
	}
	if err := q.Find(&items).Error; err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, 50001, err.Error())
		return
	}
	response.Success(c, gin.H{"list": items, "total": len(items)})
}

func (h *CapabilityTestHandler) GetSuggestions(c *gin.Context) {
	id, _ := strconv.ParseUint(c.Param("id"), 10, 64)
	var task model.CapabilityTestTask
	if err := h.db.First(&task, id).Error; err != nil {
		response.ErrorMsg(c, http.StatusNotFound, 40401, "任务不存在")
		return
	}
	if task.ResultJSON == "" {
		// 按需计算（任务中途中断时 result_json 为空）
		report, err := h.tester.ComputeSuggestionsForTask(c.Request.Context(), uint(id))
		if err != nil {
			response.ErrorMsg(c, http.StatusInternalServerError, 50001, err.Error())
			return
		}
		response.Success(c, report)
		return
	}
	var report interface{}
	_ = json.Unmarshal([]byte(task.ResultJSON), &report)
	response.Success(c, report)
}

// AutoApply 无人值守自动应用高置信度建议 + 同步模型可用状态
// POST /admin/capability-test/tasks/:id/auto-apply
func (h *CapabilityTestHandler) AutoApply(c *gin.Context) {
	id, _ := strconv.ParseUint(c.Param("id"), 10, 64)
	// 1. 同步模型可用状态（baseline 类用例结果）
	h.tester.SyncModelStatusFromBaseline(c.Request.Context(), uint(id))
	// 2. 应用高置信度建议
	applied, skipped, err := h.tester.AutoApplySuggestions(c.Request.Context(), uint(id))
	if err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, 50001, err.Error())
		return
	}
	response.Success(c, gin.H{"applied": applied, "skipped_mixed": skipped})
}

type applyRequest struct {
	Selected map[string]string `json:"selected"` // key=modelID:capability
}

func (h *CapabilityTestHandler) ApplySuggestions(c *gin.Context) {
	id, _ := strconv.ParseUint(c.Param("id"), 10, 64)
	var req applyRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.ErrorMsg(c, http.StatusBadRequest, 40001, err.Error())
		return
	}
	applied, err := h.tester.ApplySuggestions(c.Request.Context(), uint(id), req.Selected)
	if err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, 50001, err.Error())
		return
	}
	response.Success(c, gin.H{"applied": applied})
}

// ===== Baselines =====

func (h *CapabilityTestHandler) PromoteBaseline(c *gin.Context) {
	id, _ := strconv.ParseUint(c.Param("id"), 10, 64)
	uid, _ := c.Get("user_id")
	adminID := uint(0)
	if v, ok := uid.(uint); ok {
		adminID = v
	}
	count, err := h.baseline.PromoteTaskAsBaseline(c.Request.Context(), uint(id), adminID)
	if err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, 50001, err.Error())
		return
	}
	response.Success(c, gin.H{"count": count})
}

func (h *CapabilityTestHandler) GetRegressions(c *gin.Context) {
	id, _ := strconv.ParseUint(c.Param("id"), 10, 64)
	items, err := h.baseline.ListTaskRegressions(c.Request.Context(), uint(id))
	if err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, 50001, err.Error())
		return
	}
	response.Success(c, gin.H{"list": items, "total": len(items)})
}

func (h *CapabilityTestHandler) ListBaselines(c *gin.Context) {
	var modelIDPtr *uint
	if mid := c.Query("model_id"); mid != "" {
		if v, err := strconv.ParseUint(mid, 10, 64); err == nil {
			u := uint(v)
			modelIDPtr = &u
		}
	}
	items, err := h.baseline.ListBaselines(c.Request.Context(), modelIDPtr)
	if err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, 50001, err.Error())
		return
	}
	response.Success(c, gin.H{"list": items, "total": len(items)})
}

func (h *CapabilityTestHandler) DeleteBaseline(c *gin.Context) {
	id, _ := strconv.ParseUint(c.Param("id"), 10, 64)
	if err := h.baseline.DeleteBaseline(c.Request.Context(), uint(id)); err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, 50001, err.Error())
		return
	}
	response.Success(c, gin.H{"deleted": true})
}

// ===== 推广建议到同系列模型 =====

type propagateRequest struct {
	TargetModelIDs []uint `json:"target_model_ids"`
}

func (h *CapabilityTestHandler) PropagateToModels(c *gin.Context) {
	id, _ := strconv.ParseUint(c.Param("id"), 10, 64)
	var req propagateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.ErrorMsg(c, http.StatusBadRequest, 40001, err.Error())
		return
	}
	if len(req.TargetModelIDs) == 0 {
		response.ErrorMsg(c, http.StatusBadRequest, 40002, "target_model_ids 不能为空")
		return
	}
	propagated, err := h.tester.PropagateToModels(c.Request.Context(), uint(id), req.TargetModelIDs)
	if err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, 50001, err.Error())
		return
	}
	response.Success(c, gin.H{"propagated": propagated})
}

// ===== 历史模型一键测试 =====

// UntestedCount 查询从未被能力测试过的在线模型数量（无副作用）
func (h *CapabilityTestHandler) UntestedCount(c *gin.Context) {
	// "未被测试"：没有任何 passed/failed 结果的模型（仅有 skipped 不算真正测试过）
	var count int64
	h.db.Raw(`
		SELECT COUNT(*) FROM ai_models
		WHERE status = 'online'
		  AND deleted_at IS NULL
		  AND id NOT IN (
		      SELECT DISTINCT model_id FROM capability_test_results
		      WHERE status IN ('passed','failed','regression')
		  )
	`).Scan(&count)
	response.Success(c, gin.H{"untested_count": count})
}

// RunUntested 对"从未被能力测试过的 online 模型"触发一次全量测试（auto_apply=true）
func (h *CapabilityTestHandler) RunUntested(c *gin.Context) {
	// 使用 Scan+struct 避免 Pluck 与 Raw 的兼容性问题
	// "未被测试"：没有任何 passed/failed 结果的模型
	type idRow struct{ ID uint }
	var rows []idRow
	h.db.Raw(`
		SELECT id FROM ai_models
		WHERE status = 'online'
		  AND deleted_at IS NULL
		  AND id NOT IN (
		      SELECT DISTINCT model_id FROM capability_test_results
		      WHERE status IN ('passed','failed','regression')
		  )
	`).Scan(&rows)
	modelIDs := make([]uint, 0, len(rows))
	for _, r := range rows {
		modelIDs = append(modelIDs, r.ID)
	}

	if len(modelIDs) == 0 {
		response.Success(c, gin.H{"message": "所有在线模型已被测试过", "count": 0})
		return
	}

	modelIDsJSON, _ := json.Marshal(modelIDs)
	uid, _ := c.Get("user_id")
	triggeredBy := uint(0)
	if v, ok := uid.(uint); ok {
		triggeredBy = v
	}
	task := model.CapabilityTestTask{
		Status:          "pending",
		ModelIDs:        string(modelIDsJSON),
		CaseIDs:         "[]",
		TriggeredBy:     triggeredBy,
		AutoApply:       true,
		SystemTriggered: false,
	}
	if err := h.db.Create(&task).Error; err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, 50001, err.Error())
		return
	}

	payload := taskqueue.CapabilityTestPayload{
		TaskID:               task.ID,
		ModelIDs:             modelIDs,
		CaseIDs:              []uint{},
		AutoApplySuggestions: true,
		SkipKnownDisabled:    false, // 新模型：全量测试发现能力
	}
	if h.bridge != nil {
		h.bridge.PublishAndStream(c, taskqueue.TaskCapabilityTest, payload)
		return
	}
	go func() {
		_ = h.tester.RunTests(context.Background(), aimodelsvc.RunTestsInput{
			TaskID:               task.ID,
			ModelIDs:             modelIDs,
			AutoApplySuggestions: true,
			SkipKnownDisabled:    false,
		}, nil)
	}()
	response.Success(c, gin.H{"task_id": task.ID, "model_count": len(modelIDs)})
}

// ===== 热卖模型抽样检测 =====

// HotSampleInfo 预览本次抽样将选中哪些模型（无副作用）
func (h *CapabilityTestHandler) HotSampleInfo(c *gin.Context) {
	grouped, selectedIDs, err := cron.SelectHotModelSample(h.db)
	if err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, 50001, err.Error())
		return
	}
	type modelBrief struct {
		ID                uint   `json:"id"`
		ModelName         string `json:"model_name"`
		ModelType         string `json:"model_type"`
		Status            string `json:"status"`
		IsActive          bool   `json:"is_active"`
		Selected          bool   `json:"selected"`
		Tags              string `json:"tags,omitempty"`
		SupportsThinking  bool   `json:"supports_thinking"`
		SupportsWebSearch bool   `json:"supports_web_search"`
		SupportsJsonMode  bool   `json:"supports_json_mode"`
		RequiresStream    bool   `json:"requires_stream"`
	}
	type typeGroup struct {
		ModelType string       `json:"model_type"`
		Total     int          `json:"total"`
		Models    []modelBrief `json:"models"`
	}
	var groups []typeGroup
	selectedSet := make(map[uint]bool, len(selectedIDs))
	for _, id := range selectedIDs {
		selectedSet[id] = true
	}
	for mt, models := range grouped {
		var briefs []modelBrief
		for _, m := range models {
			briefs = append(briefs, modelBrief{
				ID:                m.ID,
				ModelName:         m.ModelName,
				ModelType:         m.ModelType,
				Status:            m.Status,
				IsActive:          m.IsActive,
				Selected:          selectedSet[m.ID],
				Tags:              m.Tags,
				SupportsThinking:  extractBoolFeature(m.Features, "supports_thinking"),
				SupportsWebSearch: extractBoolFeature(m.Features, "supports_web_search"),
				SupportsJsonMode:  extractBoolFeature(m.Features, "supports_json_mode"),
				RequiresStream:    m.RequiresStream(),
			})
		}
		groups = append(groups, typeGroup{ModelType: mt, Total: len(models), Models: briefs})
	}
	response.Success(c, gin.H{
		"groups":       groups,
		"selected_ids": selectedIDs,
		"total_types":  len(groups),
		"total_selected": len(selectedIDs),
	})
}

// RunHotSample 触发热卖模型抽样能力测试（auto_apply=true，full discovery）
func (h *CapabilityTestHandler) RunHotSample(c *gin.Context) {
	_, selectedIDs, err := cron.SelectHotModelSample(h.db)
	if err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, 50001, err.Error())
		return
	}
	if len(selectedIDs) == 0 {
		response.Success(c, gin.H{"message": "未找到热卖模型", "count": 0})
		return
	}

	modelIDsJSON, _ := json.Marshal(selectedIDs)
	uid, _ := c.Get("user_id")
	triggeredBy := uint(0)
	if v, ok := uid.(uint); ok {
		triggeredBy = v
	}
	task := model.CapabilityTestTask{
		Status:          "pending",
		ModelIDs:        string(modelIDsJSON),
		CaseIDs:         "[]",
		TriggeredBy:     triggeredBy,
		AutoApply:       true,
		SystemTriggered: false,
	}
	if err := h.db.Create(&task).Error; err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, 50001, err.Error())
		return
	}

	payload := taskqueue.CapabilityTestPayload{
		TaskID:               task.ID,
		ModelIDs:             selectedIDs,
		CaseIDs:              []uint{},
		AutoApplySuggestions: true,
		SkipKnownDisabled:    false,
	}
	if h.bridge != nil {
		h.bridge.PublishAndStream(c, taskqueue.TaskCapabilityTest, payload)
		return
	}
	go func() {
		_ = h.tester.RunTests(context.Background(), aimodelsvc.RunTestsInput{
			TaskID:               task.ID,
			ModelIDs:             selectedIDs,
			AutoApplySuggestions: true,
			SkipKnownDisabled:    false,
		}, nil)
	}()
	response.Success(c, gin.H{"task_id": task.ID, "model_count": len(selectedIDs), "selected_ids": selectedIDs})
}
