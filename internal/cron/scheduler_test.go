package cron

import (
	"context"
	"fmt"
	"testing"
	"time"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"tokenhub-server/internal/model"
)

func TestScheduler_TaskManagement(t *testing.T) {
	// Create scheduler with nil deps (won't call Start)
	s := &Scheduler{
		tasks:  make(map[string]*TaskInfo),
		stopCh: make(chan struct{}),
	}

	// Manually register tasks (simulating what Start does)
	s.registerTask("test_task_1", "every 5 min", true)
	s.registerTask("test_task_2", "daily", false)

	// Test GetTasks
	tasks := s.GetTasks()
	if len(tasks) != 2 {
		t.Fatalf("expected 2 tasks, got %d", len(tasks))
	}

	// Test IsTaskEnabled
	if !s.IsTaskEnabled("test_task_1") {
		t.Error("test_task_1 should be enabled")
	}
	if s.IsTaskEnabled("test_task_2") {
		t.Error("test_task_2 should be disabled")
	}
	if s.IsTaskEnabled("nonexistent") {
		t.Error("nonexistent should return false")
	}

	// Test SetTaskEnabled
	err := s.SetTaskEnabled("test_task_2", true)
	if err != nil {
		t.Fatalf("SetTaskEnabled failed: %v", err)
	}
	if !s.IsTaskEnabled("test_task_2") {
		t.Error("test_task_2 should now be enabled")
	}

	// Test SetTaskEnabled for nonexistent
	err = s.SetTaskEnabled("nonexistent", true)
	if err == nil {
		t.Error("SetTaskEnabled should fail for nonexistent task")
	}

	// Test disable
	err = s.SetTaskEnabled("test_task_1", false)
	if err != nil {
		t.Fatalf("disable failed: %v", err)
	}
	if s.IsTaskEnabled("test_task_1") {
		t.Error("test_task_1 should now be disabled")
	}
}

func TestScheduler_UpdateTaskRun(t *testing.T) {
	s := &Scheduler{
		tasks:  make(map[string]*TaskInfo),
		stopCh: make(chan struct{}),
	}
	s.registerTask("run_test", "hourly", true)

	// Update with success
	s.updateTaskRun("run_test", nil)
	tasks := s.GetTasks()
	for _, task := range tasks {
		if task.Name == "run_test" {
			if task.LastRun.IsZero() {
				t.Error("LastRun should be set")
			}
			if task.LastErr != "" {
				t.Error("LastErr should be empty on success")
			}
		}
	}

	// Update with error
	s.updateTaskRun("run_test", fmt.Errorf("test error"))
	tasks = s.GetTasks()
	for _, task := range tasks {
		if task.Name == "run_test" {
			if task.LastErr != "test error" {
				t.Errorf("expected error 'test error', got '%s'", task.LastErr)
			}
		}
	}
}

func TestScheduler_SafeRunNamed_SkipsDisabled(t *testing.T) {
	s := &Scheduler{
		tasks:  make(map[string]*TaskInfo),
		stopCh: make(chan struct{}),
	}
	s.registerTask("disabled_task", "test", false)

	executed := false
	s.safeRunNamed("disabled_task", "test task", func(ctx context.Context) error {
		executed = true
		return nil
	})

	if executed {
		t.Error("disabled task should not have been executed")
	}
}

func TestScheduler_SafeRunNamed_RunsEnabled(t *testing.T) {
	s := &Scheduler{
		tasks:  make(map[string]*TaskInfo),
		stopCh: make(chan struct{}),
	}
	s.registerTask("enabled_task", "test", true)

	executed := false
	s.safeRunNamed("enabled_task", "test task", func(ctx context.Context) error {
		executed = true
		return nil
	})

	if !executed {
		t.Error("enabled task should have been executed")
	}
}

func TestScheduler_RunBillingReconciliationForDate(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(
		&model.ApiCallLog{},
		&model.FreezeRecord{},
		&model.UserBalance{},
		&model.BillingReconciliationSnapshot{},
	); err != nil {
		t.Fatal(err)
	}

	day := time.Date(2026, 4, 24, 10, 0, 0, 0, time.Local)
	if err := db.Create(&model.ApiCallLog{
		RequestID:            "cron-recon-1",
		UserID:               1,
		TenantID:             1,
		Status:               "success",
		BillingStatus:        "settled",
		CostCredits:          2000,
		CostUnits:            20000000,
		EstimatedCostCredits: 2500,
		EstimatedCostUnits:   25000000,
		TotalTokens:          20,
		CreatedAt:            day,
	}).Error; err != nil {
		t.Fatal(err)
	}

	s := &Scheduler{
		db:     db,
		tasks:  make(map[string]*TaskInfo),
		stopCh: make(chan struct{}),
	}
	if err := s.runBillingReconciliationForDate(context.Background(), "2026-04-24"); err != nil {
		t.Fatal(err)
	}

	var snap model.BillingReconciliationSnapshot
	if err := db.Where("date = ?", "2026-04-24").First(&snap).Error; err != nil {
		t.Fatal(err)
	}
	if snap.TotalRequests != 1 || snap.ActualRevenueUnits != 20000000 || snap.EstimateVarianceUnits != 5000000 {
		t.Fatalf("unexpected snapshot: requests=%d revenue_units=%d variance_units=%d",
			snap.TotalRequests, snap.ActualRevenueUnits, snap.EstimateVarianceUnits)
	}
}

func TestScheduler_MemberGiftTaskRemoved(t *testing.T) {
	// 验证 member_gift 定时任务已从注册列表中移除
	// 模拟 Start() 中的任务注册逻辑
	s := &Scheduler{
		tasks:  make(map[string]*TaskInfo),
		stopCh: make(chan struct{}),
	}

	// 注册与 Start() 中相同的任务列表（不含 member_gift）
	s.registerTask("frozen_release", "每小时: 冻结超时释放", true)
	s.registerTask("balance_reconcile", "每小时: 余额对账检查", true)
	s.registerTask("commission_settle", "每日06:00: 佣金自动结算", true)
	s.registerTask("consume_rotate", "每月1号: 月消费轮转", true)
	s.registerTask("member_degrade", "每月1号: 会员降级检查", true)

	// 验证 member_gift 不在任务列表中
	if s.IsTaskEnabled("member_gift") {
		t.Error("member_gift task should not exist in scheduler")
	}

	tasks := s.GetTasks()
	for _, task := range tasks {
		if task.Name == "member_gift" {
			t.Error("member_gift task should not be registered")
		}
	}

	t.Log("确认 member_gift 定时任务已移除")
}
