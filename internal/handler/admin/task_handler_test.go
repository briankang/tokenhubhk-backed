package admin

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"tokenhub-server/internal/model"
	tasksvc "tokenhub-server/internal/service/task"
)

func setupTaskHandlerTest(t *testing.T) (*gin.Engine, *gorm.DB) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	name := strings.NewReplacer("/", "_", " ", "_").Replace(t.Name())
	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:%s?mode=memory&cache=shared", name)), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(&model.BackgroundTask{}); err != nil {
		t.Fatalf("migrate background tasks: %v", err)
	}
	r := gin.New()
	h := NewTaskHandler(tasksvc.NewTaskService(db))
	g := r.Group("/admin")
	g.POST("/tasks", h.CreateTask)
	g.GET("/tasks", h.ListTasks)
	g.GET("/tasks/:id", h.GetTask)
	g.POST("/tasks/:id/cancel", h.CancelTask)
	g.POST("/tasks/:id/apply-prices", h.ApplyTaskPrices)
	return r, db
}

func taskHandlerRequest(t *testing.T, r *gin.Engine, method, path string) (int, map[string]interface{}) {
	t.Helper()
	req := httptest.NewRequest(method, path, nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	var payload map[string]interface{}
	_ = json.Unmarshal(w.Body.Bytes(), &payload)
	return w.Code, payload
}

func TestTaskHandlerListNormalizesPaginationBoundaries(t *testing.T) {
	r, db := setupTaskHandlerTest(t)
	for i := 0; i < 25; i++ {
		if err := db.Create(&model.BackgroundTask{
			TaskType: model.TaskTypePriceScrape,
			Status:   model.TaskStatusCompleted,
			Progress: 100,
		}).Error; err != nil {
			t.Fatalf("seed task %d: %v", i, err)
		}
	}

	code, resp := taskHandlerRequest(t, r, http.MethodGet, "/admin/tasks?task_type=price_scrape&page=0&page_size=999")
	if code != http.StatusOK {
		t.Fatalf("list code=%d resp=%v", code, resp)
	}
	data := resp["data"].(map[string]interface{})
	if data["page"].(float64) != 1 || data["page_size"].(float64) != 20 || data["total"].(float64) != 25 {
		t.Fatalf("pagination not normalized: %#v", data)
	}
	if got := len(data["list"].([]interface{})); got != 20 {
		t.Fatalf("list len=%d, want 20", got)
	}
}

func TestTaskHandlerRejectsInvalidIDsAndNonRunningCancel(t *testing.T) {
	r, db := setupTaskHandlerTest(t)
	completed := model.BackgroundTask{
		TaskType: model.TaskTypeModelSync,
		Status:   model.TaskStatusCompleted,
		Progress: 100,
	}
	if err := db.Create(&completed).Error; err != nil {
		t.Fatalf("seed completed task: %v", err)
	}

	code, _ := taskHandlerRequest(t, r, http.MethodGet, "/admin/tasks/not-a-number")
	if code != http.StatusBadRequest {
		t.Fatalf("invalid id status = %d, want 400", code)
	}
	code, resp := taskHandlerRequest(t, r, http.MethodPost, fmt.Sprintf("/admin/tasks/%d/cancel", completed.ID))
	if code != http.StatusBadRequest {
		t.Fatalf("cancel completed status = %d resp=%v, want 400", code, resp)
	}
}
