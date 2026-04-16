package configaudit

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"

	"gorm.io/gorm"

	"tokenhub-server/internal/model"
)

// Action 常量
const (
	ActionCreate = "CREATE"
	ActionUpdate = "UPDATE"
	ActionDelete = "DELETE"
	ActionToggle = "TOGGLE"
)

// Service 统一配置审计服务
// 所有 admin 配置 handler 复用此服务:保存前读旧记录,保存后 diff 出差异字段,批量写 config_audit_logs
type Service struct {
	db *gorm.DB
}

// NewService 创建审计服务实例
func NewService(db *gorm.DB) *Service {
	return &Service{db: db}
}

// ActorCtx 操作上下文:谁在什么环境下做了修改
type ActorCtx struct {
	AdminID    uint
	AdminEmail string
	IP         string
	UserAgent  string
}

// LogCreate 记录创建操作(只记 new 值整体 JSON)
func (s *Service) LogCreate(ctx context.Context, actor ActorCtx, configTable string, configID uint, newEntity interface{}) error {
	return s.write(ctx, actor, configTable, configID, ActionCreate, "", "", jsonOf(newEntity))
}

// LogDelete 记录删除操作(只记 old 值整体 JSON)
func (s *Service) LogDelete(ctx context.Context, actor ActorCtx, configTable string, configID uint, oldEntity interface{}) error {
	return s.write(ctx, actor, configTable, configID, ActionDelete, "", jsonOf(oldEntity), "")
}

// LogUpdate 记录更新操作,按字段 diff 出差异,每个差异字段写一行
// oldEntity / newEntity 必须是相同类型的 struct(或指针)
// 仅记录导出字段(首字母大写),忽略 BaseModel 中的 CreatedAt/UpdatedAt/DeletedAt 噪音字段
func (s *Service) LogUpdate(ctx context.Context, actor ActorCtx, configTable string, configID uint, oldEntity, newEntity interface{}) error {
	diffs := structDiff(oldEntity, newEntity)
	if len(diffs) == 0 {
		return nil
	}
	for _, d := range diffs {
		if err := s.write(ctx, actor, configTable, configID, ActionUpdate, d.Field, d.Old, d.New); err != nil {
			return err
		}
	}
	return nil
}

// LogToggle 记录布尔字段切换
func (s *Service) LogToggle(ctx context.Context, actor ActorCtx, configTable string, configID uint, field string, oldVal, newVal bool) error {
	return s.write(ctx, actor, configTable, configID, ActionToggle, field,
		fmt.Sprintf("%v", oldVal), fmt.Sprintf("%v", newVal))
}

// List 分页查询审计日志,支持 table / id / action 筛选
func (s *Service) List(ctx context.Context, configTable string, configID uint, action string, page, pageSize int) ([]model.ConfigAuditLog, int64, error) {
	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > 200 {
		pageSize = 50
	}

	q := s.db.WithContext(ctx).Model(&model.ConfigAuditLog{})
	if configTable != "" {
		q = q.Where("config_table = ?", configTable)
	}
	if configID > 0 {
		q = q.Where("config_id = ?", configID)
	}
	if action != "" {
		q = q.Where("action = ?", action)
	}

	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}

	var list []model.ConfigAuditLog
	err := q.Order("created_at DESC").
		Offset((page - 1) * pageSize).
		Limit(pageSize).
		Find(&list).Error
	return list, total, err
}

// write 写入单条审计日志
func (s *Service) write(ctx context.Context, actor ActorCtx, table string, id uint, action, field, oldVal, newVal string) error {
	row := model.ConfigAuditLog{
		AdminID:     actor.AdminID,
		AdminEmail:  actor.AdminEmail,
		ConfigTable: table,
		ConfigID:    id,
		Action:      action,
		FieldName:   field,
		OldValue:    oldVal,
		NewValue:    newVal,
		IP:          actor.IP,
		UserAgent:   trunc(actor.UserAgent, 500),
	}
	return s.db.WithContext(ctx).Create(&row).Error
}

// ---- 工具函数 ----

type fieldDiff struct {
	Field string
	Old   string
	New   string
}

// 忽略的字段名(BaseModel 时间戳噪音)
var ignoredFields = map[string]bool{
	"ID":        true,
	"CreatedAt": true,
	"UpdatedAt": true,
	"DeletedAt": true,
}

// structDiff 反射对比两个同型 struct 的导出字段,返回差异集合
// 嵌入的 BaseModel 字段会被展开比较,但时间戳类字段被忽略
func structDiff(oldV, newV interface{}) []fieldDiff {
	ov := reflect.ValueOf(oldV)
	nv := reflect.ValueOf(newV)
	if ov.Kind() == reflect.Ptr {
		ov = ov.Elem()
	}
	if nv.Kind() == reflect.Ptr {
		nv = nv.Elem()
	}
	if !ov.IsValid() || !nv.IsValid() {
		return nil
	}
	if ov.Type() != nv.Type() || ov.Kind() != reflect.Struct {
		return nil
	}

	var diffs []fieldDiff
	collectDiff(ov, nv, &diffs)
	return diffs
}

func collectDiff(ov, nv reflect.Value, out *[]fieldDiff) {
	t := ov.Type()
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if !f.IsExported() {
			continue
		}
		name := f.Name
		// 嵌入结构(如 BaseModel)展开
		if f.Anonymous && ov.Field(i).Kind() == reflect.Struct {
			collectDiff(ov.Field(i), nv.Field(i), out)
			continue
		}
		if ignoredFields[name] {
			continue
		}
		oval := ov.Field(i).Interface()
		nval := nv.Field(i).Interface()
		if reflect.DeepEqual(oval, nval) {
			continue
		}
		*out = append(*out, fieldDiff{
			Field: name,
			Old:   fmt.Sprintf("%v", oval),
			New:   fmt.Sprintf("%v", nval),
		})
	}
}

func jsonOf(v interface{}) string {
	if v == nil {
		return ""
	}
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprintf("%v", v)
	}
	return trunc(string(b), 4000)
}

func trunc(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return strings.ToValidUTF8(s[:max], "") + "..."
}
