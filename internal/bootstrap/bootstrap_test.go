package bootstrap

import (
	"testing"

	"tokenhub-server/internal/config"
)

// TestServiceConfigRoles 验证 ServiceConfig 角色判断方法
func TestServiceConfigRoles(t *testing.T) {
	tests := []struct {
		role       string
		isGateway  bool
		isBackend  bool
		isWorker   bool
		isMonolith bool
		migrate    bool
		scheduler  bool
	}{
		{"gateway", true, false, false, false, false, false},
		{"backend", false, true, false, false, true, false},
		{"worker", false, false, true, false, false, true},
		{"", false, false, false, true, true, true},
	}
	for _, tt := range tests {
		t.Run("role="+tt.role, func(t *testing.T) {
			cfg := config.ServiceConfig{Role: tt.role}
			if tt.role == "backend" {
				cfg.RunMigrations = true
			}
			if cfg.IsGateway() != tt.isGateway {
				t.Errorf("IsGateway() = %v, want %v", cfg.IsGateway(), tt.isGateway)
			}
			if cfg.IsBackend() != tt.isBackend {
				t.Errorf("IsBackend() = %v, want %v", cfg.IsBackend(), tt.isBackend)
			}
			if cfg.IsWorker() != tt.isWorker {
				t.Errorf("IsWorker() = %v, want %v", cfg.IsWorker(), tt.isWorker)
			}
			if cfg.IsMonolith() != tt.isMonolith {
				t.Errorf("IsMonolith() = %v, want %v", cfg.IsMonolith(), tt.isMonolith)
			}
			if cfg.ShouldRunMigrations() != tt.migrate {
				t.Errorf("ShouldRunMigrations() = %v, want %v", cfg.ShouldRunMigrations(), tt.migrate)
			}
			if cfg.ShouldRunScheduler() != tt.scheduler {
				t.Errorf("ShouldRunScheduler() = %v, want %v", cfg.ShouldRunScheduler(), tt.scheduler)
			}
		})
	}
}

// TestServiceRoleConstants 验证角色常量值
func TestServiceRoleConstants(t *testing.T) {
	if config.RoleGateway != "gateway" {
		t.Errorf("RoleGateway = %q, want gateway", config.RoleGateway)
	}
	if config.RoleBackend != "backend" {
		t.Errorf("RoleBackend = %q, want backend", config.RoleBackend)
	}
	if config.RoleWorker != "worker" {
		t.Errorf("RoleWorker = %q, want worker", config.RoleWorker)
	}
	if config.RoleMonolith != "" {
		t.Errorf("RoleMonolith = %q, want empty", config.RoleMonolith)
	}
}
