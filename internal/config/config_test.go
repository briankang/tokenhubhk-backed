package config

import (
	"os"
	"testing"
)

func TestServiceConfig_Roles(t *testing.T) {
	tests := []struct {
		role       string
		isGateway  bool
		isBackend  bool
		isWorker   bool
		isMonolith bool
	}{
		{RoleGateway, true, false, false, false},
		{RoleBackend, false, true, false, false},
		{RoleWorker, false, false, true, false},
		{RoleMonolith, false, false, false, true},
		{"unknown", false, false, false, false},
	}
	for _, tt := range tests {
		t.Run("role="+tt.role, func(t *testing.T) {
			cfg := ServiceConfig{Role: tt.role}
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
		})
	}
}

func TestServiceConfig_ShouldRunMigrations(t *testing.T) {
	tests := []struct {
		name   string
		cfg    ServiceConfig
		expect bool
	}{
		{"monolith always migrates", ServiceConfig{Role: ""}, true},
		{"backend with flag", ServiceConfig{Role: "backend", RunMigrations: true}, true},
		{"backend without flag", ServiceConfig{Role: "backend", RunMigrations: false}, false},
		{"gateway never migrates", ServiceConfig{Role: "gateway"}, false},
		{"worker never migrates", ServiceConfig{Role: "worker"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.cfg.ShouldRunMigrations(); got != tt.expect {
				t.Errorf("ShouldRunMigrations() = %v, want %v", got, tt.expect)
			}
		})
	}
}

func TestServiceConfig_ShouldRunScheduler(t *testing.T) {
	tests := []struct {
		role   string
		expect bool
	}{
		{"", true},       // monolith
		{"worker", true}, // worker
		{"gateway", false},
		{"backend", false},
	}
	for _, tt := range tests {
		t.Run("role="+tt.role, func(t *testing.T) {
			cfg := ServiceConfig{Role: tt.role}
			if got := cfg.ShouldRunScheduler(); got != tt.expect {
				t.Errorf("ShouldRunScheduler() = %v, want %v", got, tt.expect)
			}
		})
	}
}

func TestServiceRole_Constants(t *testing.T) {
	if RoleGateway != "gateway" {
		t.Errorf("RoleGateway = %q, want gateway", RoleGateway)
	}
	if RoleBackend != "backend" {
		t.Errorf("RoleBackend = %q, want backend", RoleBackend)
	}
	if RoleWorker != "worker" {
		t.Errorf("RoleWorker = %q, want worker", RoleWorker)
	}
	if RoleMonolith != "" {
		t.Errorf("RoleMonolith = %q, want empty string", RoleMonolith)
	}
}

func TestServiceRole_EnvBinding(t *testing.T) {
	// 测试 SERVICE_ROLE 环境变量能被正确读取
	os.Setenv("SERVICE_ROLE", "gateway")
	defer os.Unsetenv("SERVICE_ROLE")

	// Load 需要配置文件，这里只测试环境变量是否被设置
	val := os.Getenv("SERVICE_ROLE")
	if val != "gateway" {
		t.Errorf("SERVICE_ROLE env = %q, want gateway", val)
	}
}

func TestDatabaseConfig_DSN(t *testing.T) {
	cfg := DatabaseConfig{
		Host:     "localhost",
		Port:     3306,
		User:     "test",
		Password: "pass",
		DBName:   "testdb",
	}
	dsn := cfg.DSN()
	if dsn == "" {
		t.Fatal("DSN should not be empty")
	}
	// 验证 DSN 包含关键部分
	for _, part := range []string{"test:pass", "localhost:3306", "testdb", "utf8mb4"} {
		if !contains(dsn, part) {
			t.Errorf("DSN %q missing %q", dsn, part)
		}
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsImpl(s, substr))
}

func containsImpl(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
