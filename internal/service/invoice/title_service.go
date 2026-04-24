package invoice

import (
	"context"
	"fmt"
	"strings"

	"gorm.io/gorm"

	"tokenhub-server/internal/model"
	"tokenhub-server/internal/pkg/dbctx"
)

// TitleInput 抬头创建/更新入参
type TitleInput struct {
	UserID      uint
	TenantID    uint
	Region      string
	InvoiceType string
	Title       string
	TaxID       string
	BankName    string
	BankAccount string
	Address     string
	Phone       string
	Country     string
	Email       string
	Alias       string
	IsDefault   bool
}

func (in *TitleInput) validate() error {
	if in.UserID == 0 {
		return fmt.Errorf("invalid user")
	}
	if in.Region != model.InvoiceRegionCN && in.Region != model.InvoiceRegionOverseas {
		return fmt.Errorf("invalid region: %s", in.Region)
	}
	if in.InvoiceType != model.InvoiceTypePersonal && in.InvoiceType != model.InvoiceTypeCompany && in.InvoiceType != model.InvoiceTypeVATInvoice {
		return fmt.Errorf("invalid invoice_type: %s", in.InvoiceType)
	}
	if strings.TrimSpace(in.Title) == "" {
		return fmt.Errorf("title required")
	}
	if (in.InvoiceType == model.InvoiceTypeCompany || in.InvoiceType == model.InvoiceTypeVATInvoice) && strings.TrimSpace(in.TaxID) == "" {
		return fmt.Errorf("tax_id required for company/vat invoice")
	}
	return nil
}

// ListTitles 查询用户自己保存的抬头(按默认 + 创建时间倒序)
func (s *Service) ListTitles(ctx context.Context, userID uint) ([]model.InvoiceTitle, error) {
	ctx2, cancel := dbctx.Short(ctx)
	defer cancel()

	var list []model.InvoiceTitle
	if err := s.db.WithContext(ctx2).
		Where("user_id = ?", userID).
		Order("is_default DESC, created_at DESC").
		Limit(50).
		Find(&list).Error; err != nil {
		return nil, err
	}
	return list, nil
}

// CreateTitle 保存新抬头
func (s *Service) CreateTitle(ctx context.Context, in TitleInput) (*model.InvoiceTitle, error) {
	if err := in.validate(); err != nil {
		return nil, err
	}
	ctx2, cancel := dbctx.Short(ctx)
	defer cancel()

	t := &model.InvoiceTitle{
		UserID:      in.UserID,
		TenantID:    in.TenantID,
		Region:      in.Region,
		InvoiceType: in.InvoiceType,
		Title:       in.Title,
		TaxID:       in.TaxID,
		BankName:    in.BankName,
		BankAccount: in.BankAccount,
		Address:     in.Address,
		Phone:       in.Phone,
		Country:     in.Country,
		Email:       in.Email,
		Alias:       in.Alias,
		IsDefault:   in.IsDefault,
	}

	err := s.db.WithContext(ctx2).Transaction(func(tx *gorm.DB) error {
		if in.IsDefault {
			if err := tx.Model(&model.InvoiceTitle{}).
				Where("user_id = ? AND region = ?", in.UserID, in.Region).
				Update("is_default", false).Error; err != nil {
				return err
			}
		}
		return tx.Create(t).Error
	})
	if err != nil {
		return nil, err
	}
	return t, nil
}

// UpdateTitle 更新抬头(仅本人)
func (s *Service) UpdateTitle(ctx context.Context, id, userID uint, in TitleInput) error {
	if err := in.validate(); err != nil {
		return err
	}
	ctx2, cancel := dbctx.Short(ctx)
	defer cancel()

	return s.db.WithContext(ctx2).Transaction(func(tx *gorm.DB) error {
		var t model.InvoiceTitle
		if err := tx.First(&t, id).Error; err != nil {
			return err
		}
		if t.UserID != userID {
			return fmt.Errorf("forbidden")
		}
		if in.IsDefault {
			if err := tx.Model(&model.InvoiceTitle{}).
				Where("user_id = ? AND region = ? AND id != ?", userID, in.Region, id).
				Update("is_default", false).Error; err != nil {
				return err
			}
		}
		return tx.Model(&model.InvoiceTitle{}).Where("id = ?", id).Updates(map[string]interface{}{
			"region":       in.Region,
			"invoice_type": in.InvoiceType,
			"title":        in.Title,
			"tax_id":       in.TaxID,
			"bank_name":    in.BankName,
			"bank_account": in.BankAccount,
			"address":      in.Address,
			"phone":        in.Phone,
			"country":      in.Country,
			"email":        in.Email,
			"alias":        in.Alias,
			"is_default":   in.IsDefault,
		}).Error
	})
}

// DeleteTitle 删除抬头(仅本人)
func (s *Service) DeleteTitle(ctx context.Context, id, userID uint) error {
	ctx2, cancel := dbctx.Short(ctx)
	defer cancel()

	var t model.InvoiceTitle
	if err := s.db.WithContext(ctx2).First(&t, id).Error; err != nil {
		return err
	}
	if t.UserID != userID {
		return fmt.Errorf("forbidden")
	}
	return s.db.WithContext(ctx2).Unscoped().Delete(&model.InvoiceTitle{}, id).Error
}
