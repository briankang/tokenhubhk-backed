package task

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"tokenhub-server/internal/model"
)

func newTaskTestDB(t *testing.T) *gorm.DB {
	t.Helper()
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
	return db
}

func TestTaskServiceRecoverStaleTasksMarksPendingAndRunningFailed(t *testing.T) {
	db := newTaskTestDB(t)
	completedAt := time.Now().Add(-time.Hour)
	tasks := []model.BackgroundTask{
		{TaskType: model.TaskTypeModelSync, Status: model.TaskStatusPending, Progress: 10},
		{TaskType: model.TaskTypeModelCheck, Status: model.TaskStatusRunning, Progress: 50},
		{TaskType: model.TaskTypePriceScrape, Status: model.TaskStatusCompleted, Progress: 100, CompletedAt: &completedAt},
	}
	if err := db.Create(&tasks).Error; err != nil {
		t.Fatalf("seed tasks: %v", err)
	}

	NewTaskService(db)

	var pending model.BackgroundTask
	if err := db.First(&pending, tasks[0].ID).Error; err != nil {
		t.Fatalf("load pending task: %v", err)
	}
	if pending.Status != model.TaskStatusFailed || pending.CompletedAt == nil || pending.ErrorMessage == "" {
		t.Fatalf("pending task not recovered as failed: %#v", pending)
	}
	var running model.BackgroundTask
	if err := db.First(&running, tasks[1].ID).Error; err != nil {
		t.Fatalf("load running task: %v", err)
	}
	if running.Status != model.TaskStatusFailed || running.CompletedAt == nil || running.ErrorMessage == "" {
		t.Fatalf("running task not recovered as failed: %#v", running)
	}
	var completed model.BackgroundTask
	if err := db.First(&completed, tasks[2].ID).Error; err != nil {
		t.Fatalf("load completed task: %v", err)
	}
	if completed.Status != model.TaskStatusCompleted || completed.CompletedAt == nil || !completed.CompletedAt.Equal(completedAt) {
		t.Fatalf("completed task should not be recovered: %#v", completed)
	}
}

func TestTaskServiceListTasksNormalizesPaginationAndFilters(t *testing.T) {
	db := newTaskTestDB(t)
	for i := 0; i < 25; i++ {
		if err := db.Create(&model.BackgroundTask{
			TaskType: model.TaskTypePriceScrape,
			Status:   model.TaskStatusCompleted,
			Progress: 100,
		}).Error; err != nil {
			t.Fatalf("seed price scrape task %d: %v", i, err)
		}
	}
	if err := db.Create(&model.BackgroundTask{TaskType: model.TaskTypeModelSync, Status: model.TaskStatusCompleted}).Error; err != nil {
		t.Fatalf("seed model sync task: %v", err)
	}
	svc := NewTaskService(db)

	list, total, err := svc.ListTasks(model.TaskTypePriceScrape, 0, 0)
	if err != nil {
		t.Fatalf("ListTasks default pagination: %v", err)
	}
	if total != 25 || len(list) != 20 {
		t.Fatalf("default pagination total=%d len=%d, want total 25 len 20", total, len(list))
	}
	list, total, err = svc.ListTasks(model.TaskTypePriceScrape, 2, 10)
	if err != nil {
		t.Fatalf("ListTasks second page: %v", err)
	}
	if total != 25 || len(list) != 10 {
		t.Fatalf("second page total=%d len=%d, want total 25 len 10", total, len(list))
	}
	list, total, err = svc.ListTasks(model.TaskTypeModelSync, 1, 500)
	if err != nil {
		t.Fatalf("ListTasks oversized page size: %v", err)
	}
	if total != 1 || len(list) != 1 {
		t.Fatalf("filtered oversized total=%d len=%d, want 1", total, len(list))
	}
}

func TestTaskServiceCancelNonRunningTaskDoesNotMutateCompletedTask(t *testing.T) {
	db := newTaskTestDB(t)
	completedAt := time.Now().Add(-time.Minute)
	task := model.BackgroundTask{
		TaskType:    model.TaskTypePriceScrape,
		Status:      model.TaskStatusCompleted,
		Progress:    100,
		CompletedAt: &completedAt,
	}
	if err := db.Create(&task).Error; err != nil {
		t.Fatalf("seed completed task: %v", err)
	}
	svc := NewTaskService(db)

	err := svc.CancelTask(task.ID)
	if err == nil {
		t.Fatalf("CancelTask error = %v, want not running", err)
	}
	var reloaded model.BackgroundTask
	if err := db.First(&reloaded, task.ID).Error; err != nil {
		t.Fatalf("load task after cancel: %v", err)
	}
	if reloaded.Status != model.TaskStatusCompleted || reloaded.Progress != 100 {
		t.Fatalf("completed task mutated by failed cancel: %#v", reloaded)
	}
}
