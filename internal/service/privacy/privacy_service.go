package privacy

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"

	"tokenhub-server/internal/model"
	"tokenhub-server/internal/pkg/dbctx"
)

var terminalStatuses = map[string]bool{
	model.PrivacyRequestStatusCompleted: true,
	model.PrivacyRequestStatusRejected:  true,
	model.PrivacyRequestStatusCancelled: true,
}

var validTypes = map[string]bool{
	model.PrivacyRequestExportData:      true,
	model.PrivacyRequestDeleteAccount:   true,
	model.PrivacyRequestDeleteAPILogs:   true,
	model.PrivacyRequestMarketingOptOut: true,
}

var validStatuses = map[string]bool{
	model.PrivacyRequestStatusReceived:   true,
	model.PrivacyRequestStatusVerify:     true,
	model.PrivacyRequestStatusInReview:   true,
	model.PrivacyRequestStatusProcessing: true,
	model.PrivacyRequestStatusCompleted:  true,
	model.PrivacyRequestStatusRejected:   true,
	model.PrivacyRequestStatusCancelled:  true,
}

type Service struct {
	db *gorm.DB
}

func New(db *gorm.DB) *Service {
	return &Service{db: db}
}

type CreateInput struct {
	UserID   uint
	Email    string
	Type     string
	Region   string
	Language string
	Reason   string
	Scope    string
	Metadata map[string]any
}

type ListUserFilter struct {
	UserID   uint
	Type     string
	Status   string
	Page     int
	PageSize int
}

type ListAdminFilter struct {
	UserID   uint
	Type     string
	Status   string
	Region   string
	Keyword  string
	Page     int
	PageSize int
}

type UpdateInput struct {
	Status         string
	AssignedTo     uint
	ResolutionNote string
	RejectReason   string
	Verified       bool
}

func (s *Service) Create(ctx context.Context, in CreateInput) (*model.PrivacyRequest, error) {
	if in.UserID == 0 {
		return nil, fmt.Errorf("user_id is required")
	}
	in.Type = strings.TrimSpace(in.Type)
	if !validTypes[in.Type] {
		return nil, fmt.Errorf("invalid privacy request type: %s", in.Type)
	}

	ctx2, cancel := dbctx.Short(ctx)
	defer cancel()

	email := strings.TrimSpace(in.Email)
	if email == "" {
		if resolved, err := s.lookupUserEmail(ctx2, in.UserID); err == nil {
			email = resolved
		}
	}
	if email == "" {
		email = fmt.Sprintf("user-%d", in.UserID)
	}

	var existing model.PrivacyRequest
	err := s.db.WithContext(ctx2).
		Where("user_id = ? AND type = ? AND status NOT IN ?", in.UserID, in.Type, []string{
			model.PrivacyRequestStatusCompleted,
			model.PrivacyRequestStatusRejected,
			model.PrivacyRequestStatusCancelled,
		}).
		Order("created_at DESC").
		First(&existing).Error
	if err == nil {
		return &existing, nil
	}
	if err != nil && err != gorm.ErrRecordNotFound {
		return nil, err
	}

	meta, _ := json.Marshal(in.Metadata)
	dueAt := time.Now().Add(30 * 24 * time.Hour)
	status := model.PrivacyRequestStatusReceived
	if in.Type == model.PrivacyRequestMarketingOptOut {
		status = model.PrivacyRequestStatusCompleted
		now := time.Now()
		req := &model.PrivacyRequest{
			UserID:         in.UserID,
			Email:          email,
			Type:           in.Type,
			Status:         status,
			Region:         strings.ToUpper(strings.TrimSpace(in.Region)),
			Language:       strings.TrimSpace(in.Language),
			Reason:         strings.TrimSpace(in.Reason),
			Scope:          strings.TrimSpace(in.Scope),
			Metadata:       model.JSON(meta),
			DueAt:          &dueAt,
			ClosedAt:       &now,
			ResolutionNote: "Marketing opt-out recorded.",
		}
		if err := s.db.WithContext(ctx2).Create(req).Error; err != nil {
			return nil, err
		}
		return req, nil
	}

	req := &model.PrivacyRequest{
		UserID:   in.UserID,
		Email:    email,
		Type:     in.Type,
		Status:   status,
		Region:   strings.ToUpper(strings.TrimSpace(in.Region)),
		Language: strings.TrimSpace(in.Language),
		Reason:   strings.TrimSpace(in.Reason),
		Scope:    strings.TrimSpace(in.Scope),
		Metadata: model.JSON(meta),
		DueAt:    &dueAt,
	}
	if err := s.db.WithContext(ctx2).Create(req).Error; err != nil {
		return nil, err
	}
	return req, nil
}

func (s *Service) ListUser(ctx context.Context, f ListUserFilter) ([]model.PrivacyRequest, int64, error) {
	if f.UserID == 0 {
		return nil, 0, fmt.Errorf("user_id is required")
	}
	page, pageSize := normalizePage(f.Page, f.PageSize, 100)
	ctx2, cancel := dbctx.Medium(ctx)
	defer cancel()

	tx := s.db.WithContext(ctx2).Model(&model.PrivacyRequest{}).Where("user_id = ?", f.UserID)
	tx = applyTypeStatus(tx, f.Type, f.Status)

	var total int64
	if err := tx.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var list []model.PrivacyRequest
	if err := tx.Order("created_at DESC").Offset((page - 1) * pageSize).Limit(pageSize).Find(&list).Error; err != nil {
		return nil, 0, err
	}
	return list, total, nil
}

func (s *Service) ListAdmin(ctx context.Context, f ListAdminFilter) ([]model.PrivacyRequest, int64, error) {
	page, pageSize := normalizePage(f.Page, f.PageSize, 200)
	ctx2, cancel := dbctx.Medium(ctx)
	defer cancel()

	tx := s.db.WithContext(ctx2).Model(&model.PrivacyRequest{})
	tx = applyTypeStatus(tx, f.Type, f.Status)
	if f.UserID > 0 {
		tx = tx.Where("user_id = ?", f.UserID)
	}
	if region := strings.TrimSpace(f.Region); region != "" {
		tx = tx.Where("region = ?", strings.ToUpper(region))
	}
	if kw := strings.TrimSpace(f.Keyword); kw != "" {
		like := "%" + kw + "%"
		tx = tx.Where("email LIKE ? OR reason LIKE ? OR resolution_note LIKE ?", like, like, like)
	}

	var total int64
	if err := tx.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var list []model.PrivacyRequest
	if err := tx.Order("created_at DESC").Offset((page - 1) * pageSize).Limit(pageSize).Find(&list).Error; err != nil {
		return nil, 0, err
	}
	return list, total, nil
}

func (s *Service) GetByID(ctx context.Context, id uint) (*model.PrivacyRequest, error) {
	ctx2, cancel := dbctx.Short(ctx)
	defer cancel()

	var req model.PrivacyRequest
	if err := s.db.WithContext(ctx2).First(&req, id).Error; err != nil {
		return nil, err
	}
	return &req, nil
}

func (s *Service) GetUserRequest(ctx context.Context, id, userID uint) (*model.PrivacyRequest, error) {
	ctx2, cancel := dbctx.Short(ctx)
	defer cancel()

	var req model.PrivacyRequest
	if err := s.db.WithContext(ctx2).Where("id = ? AND user_id = ?", id, userID).First(&req).Error; err != nil {
		return nil, err
	}
	return &req, nil
}

func (s *Service) Update(ctx context.Context, id uint, in UpdateInput) (*model.PrivacyRequest, error) {
	if id == 0 {
		return nil, fmt.Errorf("id is required")
	}
	if strings.TrimSpace(in.Status) != "" && !validStatuses[in.Status] {
		return nil, fmt.Errorf("invalid privacy request status: %s", in.Status)
	}

	ctx2, cancel := dbctx.Short(ctx)
	defer cancel()

	var out model.PrivacyRequest
	err := s.db.WithContext(ctx2).Transaction(func(tx *gorm.DB) error {
		if err := tx.First(&out, id).Error; err != nil {
			return err
		}
		updates := map[string]any{}
		if in.Status != "" {
			updates["status"] = in.Status
			if terminalStatuses[in.Status] {
				now := time.Now()
				updates["closed_at"] = &now
			} else {
				updates["closed_at"] = nil
			}
		}
		if in.AssignedTo > 0 {
			updates["assigned_to"] = in.AssignedTo
		}
		if in.ResolutionNote != "" {
			updates["resolution_note"] = strings.TrimSpace(in.ResolutionNote)
		}
		if in.RejectReason != "" {
			updates["reject_reason"] = strings.TrimSpace(in.RejectReason)
		}
		if in.Verified {
			now := time.Now()
			updates["verified_at"] = &now
		}
		if len(updates) == 0 {
			return nil
		}
		if err := tx.Model(&out).Updates(updates).Error; err != nil {
			return err
		}
		return tx.First(&out, id).Error
	})
	if err != nil {
		return nil, err
	}
	return &out, nil
}

func applyTypeStatus(tx *gorm.DB, reqType, status string) *gorm.DB {
	if reqType = strings.TrimSpace(reqType); reqType != "" {
		tx = tx.Where("type = ?", reqType)
	}
	if status = strings.TrimSpace(status); status != "" {
		tx = tx.Where("status = ?", status)
	}
	return tx
}

func normalizePage(page, pageSize, maxPageSize int) (int, int) {
	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > maxPageSize {
		pageSize = 20
	}
	return page, pageSize
}

func (s *Service) lookupUserEmail(ctx context.Context, userID uint) (string, error) {
	var user model.User
	if err := s.db.WithContext(ctx).Select("email").First(&user, userID).Error; err != nil {
		return "", err
	}
	return strings.TrimSpace(user.Email), nil
}
