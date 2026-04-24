package admin

import (
	"errors"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
	"gorm.io/gorm"

	"tokenhub-server/internal/bootstrap"
	"tokenhub-server/internal/database"
	"tokenhub-server/internal/model"
	"tokenhub-server/internal/pkg/logger"
	"tokenhub-server/internal/pkg/response"
)

// SystemHandler 系统级管理接口处理器（schema 升级 / 版本查询等运维操作）。
//
// 这些操作在启动时**不会自动执行**，只通过本 handler 的接口手动触发，
// 避免大流量场景下意外重扫全部表。
type SystemHandler struct {
	db *gorm.DB
}

// NewSystemHandler 创建 SystemHandler 实例
func NewSystemHandler(db *gorm.DB) *SystemHandler {
	if db == nil {
		panic("admin system handler: db is nil")
	}
	return &SystemHandler{db: db}
}

// GetSchemaVersion 返回当前代码期望版本 + 数据库中实际版本
// GET /api/v1/admin/system/schema-version
func (h *SystemHandler) GetSchemaVersion(c *gin.Context) {
	codeVersion := database.CurrentSchemaVersion

	resp := gin.H{
		"code_version": codeVersion,
		"db_version":   "",
		"table_exists": h.db.Migrator().HasTable(&model.SystemConfig{}),
		"match":        false,
	}

	if resp["table_exists"].(bool) {
		var cfg model.SystemConfig
		if err := h.db.Where("`key` = ?", "schema_version").First(&cfg).Error; err == nil {
			resp["db_version"] = cfg.Value
			resp["match"] = cfg.Value == codeVersion
		}
	}

	response.Success(c, resp)
}

// MigrateRequest 升级接口请求体
type MigrateRequest struct {
	// RunSchema: 是否执行 schema 初始化（dropLegacy + AutoMigrate）
	RunSchema bool `json:"run_schema"`
	// RunSeeds: 是否执行全量种子数据写入（会覆盖部分 upsert 数据）
	RunSeeds bool `json:"run_seeds"`
	// RunDataMigrations: 是否执行数据迁移（字段回填、索引建立等）
	RunDataMigrations bool `json:"run_data_migrations"`
}

// Migrate 一键执行 schema 升级 / 数据迁移 / 种子刷新
// POST /api/v1/admin/system/migrate
//
// 请求体示例（默认全部执行）：
//   {"run_schema": true, "run_seeds": false, "run_data_migrations": true}
//
// 默认行为（未传参）：只执行 RunDataMigrations，不改 schema，不覆盖种子
// 典型使用：
//   - 发版带新字段 → {run_schema: true, run_data_migrations: true}
//   - 修复历史脏数据 → {run_data_migrations: true}
//   - 重新初始化权限 → {run_seeds: true}
func (h *SystemHandler) Migrate(c *gin.Context) {
	var req MigrateRequest
	_ = c.ShouldBindJSON(&req) // 允许空 body

	// 未传任何参数时默认只做幂等的数据迁移
	if !req.RunSchema && !req.RunSeeds && !req.RunDataMigrations {
		req.RunDataMigrations = true
	}

	steps := []gin.H{}
	start := time.Now()

	// 1. Schema 初始化（最危险，放最前；失败则中断）
	if req.RunSchema {
		stepStart := time.Now()
		logger.L.Info("admin migrate: running schema init")
		if err := database.RunSchemaInit(h.db); err != nil {
			logger.L.Error("admin migrate: schema init failed", zap.Error(err))
			response.ErrorMsg(c, http.StatusInternalServerError, 50001, "schema init failed: "+err.Error())
			return
		}
		steps = append(steps, gin.H{
			"step":        "schema_init",
			"status":      "ok",
			"duration_ms": time.Since(stepStart).Milliseconds(),
		})
	}

	// 2. 数据迁移（字段回填、索引建立等）
	if req.RunDataMigrations {
		stepStart := time.Now()
		logger.L.Info("admin migrate: running data migrations")
		// RunDataMigrations 内部对失败只记 warn 不 panic，可安全直接调用
		bootstrap.RunDataMigrations()
		steps = append(steps, gin.H{
			"step":        "data_migrations",
			"status":      "ok",
			"duration_ms": time.Since(stepStart).Milliseconds(),
		})
	}

	// 3. 种子数据（允许重复调用，内部有幂等检查）
	if req.RunSeeds {
		stepStart := time.Now()
		logger.L.Info("admin migrate: running seeds")
		database.RunAllSeeds(h.db)
		steps = append(steps, gin.H{
			"step":        "seeds",
			"status":      "ok",
			"duration_ms": time.Since(stepStart).Milliseconds(),
		})
	}

	// 4. 更新 schema_version 标记（只在 schema 或 seeds 执行时更新）
	if req.RunSchema || req.RunSeeds {
		if err := database.MarkSchemaVersion(h.db); err != nil {
			logger.L.Warn("admin migrate: mark schema version failed", zap.Error(err))
		}
	}

	response.Success(c, gin.H{
		"version":     database.CurrentSchemaVersion,
		"steps":       steps,
		"total_ms":    time.Since(start).Milliseconds(),
		"executed_at": time.Now().Format(time.RFC3339),
	})
}

// GetSystemConfig 读取指定 key 的 system_configs 值
// GET /api/v1/admin/system/config/:key
func (h *SystemHandler) GetSystemConfig(c *gin.Context) {
	key := c.Param("key")
	var cfg model.SystemConfig
	if err := h.db.WithContext(c.Request.Context()).
		Where("`key` = ?", key).First(&cfg).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			response.Success(c, gin.H{"key": key, "value": ""})
			return
		}
		response.Success(c, gin.H{"key": key, "value": ""})
		return
	}
	response.Success(c, gin.H{"key": cfg.Key, "value": cfg.Value})
}

// updateSysConfigReq 更新系统配置请求体
type updateSysConfigReq struct {
	Value string `json:"value"`
}

// UpdateSystemConfig 更新或创建指定 key 的 system_configs 值
// PUT /api/v1/admin/system/config/:key
func (h *SystemHandler) UpdateSystemConfig(c *gin.Context) {
	key := c.Param("key")
	var req updateSysConfigReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"code": 40000, "message": "bad request"})
		return
	}
	ctx := c.Request.Context()
	var cfg model.SystemConfig
	err := h.db.WithContext(ctx).Where("`key` = ?", key).First(&cfg).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		cfg = model.SystemConfig{Key: key, Value: req.Value}
		h.db.WithContext(ctx).Create(&cfg)
	} else if err == nil {
		h.db.WithContext(ctx).Model(&cfg).Update("value", req.Value)
	}
	response.Success(c, gin.H{"key": key, "value": req.Value})
}
