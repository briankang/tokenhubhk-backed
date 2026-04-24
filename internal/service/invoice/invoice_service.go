// Package invoice 发票开具服务
//
// Phase 1 实现模式 B（管理员人工开票）:
//   - 用户提交申请 -> 管理员审批 -> 管理员上传 PDF -> 用户下载
//   - 海外用户可选择走同一流程（需要盖章正本），也可纯前端生成 PDF
//
// 关键业务规则:
//   - 仅 status=completed 且 paid_at 180 天内的订单可申请
//   - 同一订单同一时刻只能有一个未作废的申请（payments.invoice_request_id 唯一关联最新一次）
//   - 状态流转: pending -> approved -> issued / pending -> rejected
package invoice

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

// Service 发票服务
type Service struct {
	db *gorm.DB
}

// New 构造
func New(db *gorm.DB) *Service {
	return &Service{db: db}
}

// ── 用户端 ─────────────────────────────────────────────────────────────────

// SubmitInput 用户提交开票申请
type SubmitInput struct {
	UserID      uint
	TenantID    uint
	PaymentID   uint // Phase 1 单订单
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
	Remark      string
}

// Submit 提交发票申请。
func (s *Service) Submit(ctx context.Context, in SubmitInput) (*model.InvoiceRequest, error) {
	if in.UserID == 0 || in.PaymentID == 0 {
		return nil, fmt.Errorf("invalid user or payment id")
	}
	if in.Region != model.InvoiceRegionCN && in.Region != model.InvoiceRegionOverseas {
		return nil, fmt.Errorf("invalid region: %s", in.Region)
	}
	if in.InvoiceType != model.InvoiceTypePersonal && in.InvoiceType != model.InvoiceTypeCompany && in.InvoiceType != model.InvoiceTypeVATInvoice {
		return nil, fmt.Errorf("invalid invoice_type: %s", in.InvoiceType)
	}
	if strings.TrimSpace(in.Title) == "" {
		return nil, fmt.Errorf("title is required")
	}
	if strings.TrimSpace(in.Email) == "" {
		return nil, fmt.Errorf("email is required")
	}
	// 企业票 / VAT 专票必填税号
	if (in.InvoiceType == model.InvoiceTypeCompany || in.InvoiceType == model.InvoiceTypeVATInvoice) && strings.TrimSpace(in.TaxID) == "" {
		return nil, fmt.Errorf("tax_id is required for company/vat invoice")
	}
	// 国内专票必填开户行 + 账号 + 地址 + 电话
	if in.Region == model.InvoiceRegionCN && in.InvoiceType == model.InvoiceTypeVATInvoice {
		if strings.TrimSpace(in.BankName) == "" || strings.TrimSpace(in.BankAccount) == "" ||
			strings.TrimSpace(in.Address) == "" || strings.TrimSpace(in.Phone) == "" {
			return nil, fmt.Errorf("vat_invoice requires bank_name/bank_account/address/phone")
		}
	}

	// 校验订单
	ctx2, cancel := dbctx.Medium(ctx)
	defer cancel()

	var pay model.Payment
	if err := s.db.WithContext(ctx2).Where("id = ? AND user_id = ?", in.PaymentID, in.UserID).First(&pay).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, fmt.Errorf("payment not found")
		}
		return nil, err
	}
	if pay.Status != model.PaymentStatusCompleted && pay.Status != model.PaymentStatusPartialRefunded {
		return nil, fmt.Errorf("only completed orders can be invoiced")
	}
	// 180 天内
	if time.Since(pay.CreatedAt) > 180*24*time.Hour {
		return nil, fmt.Errorf("order too old (>180 days), cannot request invoice")
	}
	// 该订单已有未作废 / 未拒绝的申请 -> 拒绝
	if pay.InvoiceStatus == model.PaymentInvoiceStatusRequested || pay.InvoiceStatus == model.PaymentInvoiceStatusIssued {
		return nil, fmt.Errorf("invoice already requested or issued for this order")
	}

	// 开票金额 = 订单原 RMB 金额 - 已退金额
	amountRMB := pay.RMBAmount
	if amountRMB <= 0 {
		amountRMB = pay.Amount
	}
	if pay.RefundedAmount > 0 {
		amountRMB -= pay.RefundedAmount
	}
	if amountRMB <= 0 {
		return nil, fmt.Errorf("no invoiceable amount left on this order")
	}

	orderIDsJSON, _ := json.Marshal([]uint{in.PaymentID})

	req := &model.InvoiceRequest{
		TenantID:       in.TenantID,
		UserID:         in.UserID,
		Region:         in.Region,
		InvoiceType:    in.InvoiceType,
		Title:          in.Title,
		TaxID:          in.TaxID,
		BankName:       in.BankName,
		BankAccount:    in.BankAccount,
		Address:        in.Address,
		Phone:          in.Phone,
		Country:        in.Country,
		Email:          in.Email,
		OrderIDs:       model.JSON(orderIDsJSON),
		AmountRMB:      amountRMB,
		AmountOriginal: pay.Amount,
		Currency:       pay.OriginalCurrency,
		Status:         model.InvoiceStatusPending,
		Remark:         in.Remark,
	}

	// 事务:创建 invoice_request + 更新 payment.invoice_status
	err := s.db.WithContext(ctx2).Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(req).Error; err != nil {
			return err
		}
		return tx.Model(&model.Payment{}).Where("id = ?", in.PaymentID).Updates(map[string]interface{}{
			"invoice_status":     model.PaymentInvoiceStatusRequested,
			"invoice_request_id": req.ID,
		}).Error
	})
	if err != nil {
		return nil, err
	}
	return req, nil
}

// ListUserRequests 用户端分页查询自己的开票记录
func (s *Service) ListUserRequests(ctx context.Context, userID uint, page, pageSize int) ([]model.InvoiceRequest, int64, error) {
	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > 100 {
		pageSize = 20
	}
	ctx2, cancel := dbctx.Medium(ctx)
	defer cancel()

	var list []model.InvoiceRequest
	var total int64
	tx := s.db.WithContext(ctx2).Model(&model.InvoiceRequest{}).Where("user_id = ?", userID)
	if err := tx.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	if err := tx.Order("created_at DESC").Offset((page - 1) * pageSize).Limit(pageSize).Find(&list).Error; err != nil {
		return nil, 0, err
	}
	return list, total, nil
}

// GetByID 单条读取
func (s *Service) GetByID(ctx context.Context, id uint) (*model.InvoiceRequest, error) {
	ctx2, cancel := dbctx.Short(ctx)
	defer cancel()

	var r model.InvoiceRequest
	if err := s.db.WithContext(ctx2).First(&r, id).Error; err != nil {
		return nil, err
	}
	return &r, nil
}

// ── 管理员端 ───────────────────────────────────────────────────────────────

// ListAdminFilter 管理员列表过滤条件
type ListAdminFilter struct {
	Status      string
	Region      string
	InvoiceType string
	Keyword     string // 匹配 title / tax_id / email
	Page        int
	PageSize    int
}

// ListAdmin 管理员分页查询
func (s *Service) ListAdmin(ctx context.Context, f ListAdminFilter) ([]model.InvoiceRequest, int64, error) {
	if f.Page < 1 {
		f.Page = 1
	}
	if f.PageSize < 1 || f.PageSize > 200 {
		f.PageSize = 20
	}
	ctx2, cancel := dbctx.Medium(ctx)
	defer cancel()

	tx := s.db.WithContext(ctx2).Model(&model.InvoiceRequest{})
	if f.Status != "" {
		tx = tx.Where("status = ?", f.Status)
	}
	if f.Region != "" {
		tx = tx.Where("region = ?", f.Region)
	}
	if f.InvoiceType != "" {
		tx = tx.Where("invoice_type = ?", f.InvoiceType)
	}
	if kw := strings.TrimSpace(f.Keyword); kw != "" {
		like := "%" + kw + "%"
		tx = tx.Where("title LIKE ? OR tax_id LIKE ? OR email LIKE ?", like, like, like)
	}

	var total int64
	if err := tx.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var list []model.InvoiceRequest
	if err := tx.Order("created_at DESC").Offset((f.Page - 1) * f.PageSize).Limit(f.PageSize).Find(&list).Error; err != nil {
		return nil, 0, err
	}
	return list, total, nil
}

// HardDelete 物理删除发票申请并还原对应订单的 invoice_status(仅维护用途)
func (s *Service) HardDelete(ctx context.Context, id uint) error {
	ctx2, cancel := dbctx.Short(ctx)
	defer cancel()
	return s.db.WithContext(ctx2).Transaction(func(tx *gorm.DB) error {
		var req model.InvoiceRequest
		if err := tx.First(&req, id).Error; err != nil {
			return err
		}
		if err := s.resetPaymentInvoiceStatus(tx, &req); err != nil {
			return err
		}
		return tx.Unscoped().Delete(&model.InvoiceRequest{}, id).Error
	})
}

// Approve 通过申请（待上传 PDF）
func (s *Service) Approve(ctx context.Context, id, adminID uint, remark string) error {
	ctx2, cancel := dbctx.Short(ctx)
	defer cancel()

	return s.db.WithContext(ctx2).Model(&model.InvoiceRequest{}).
		Where("id = ? AND status = ?", id, model.InvoiceStatusPending).
		Updates(map[string]interface{}{
			"status":      model.InvoiceStatusApproved,
			"approved_by": adminID,
			"remark":      remark,
		}).Error
}

// Reject 拒绝申请
func (s *Service) Reject(ctx context.Context, id, adminID uint, reason string) error {
	if strings.TrimSpace(reason) == "" {
		return fmt.Errorf("reject reason is required")
	}
	ctx2, cancel := dbctx.Short(ctx)
	defer cancel()

	return s.db.WithContext(ctx2).Transaction(func(tx *gorm.DB) error {
		var req model.InvoiceRequest
		if err := tx.First(&req, id).Error; err != nil {
			return err
		}
		if req.Status != model.InvoiceStatusPending {
			return fmt.Errorf("only pending requests can be rejected")
		}
		if err := tx.Model(&model.InvoiceRequest{}).Where("id = ?", req.ID).Updates(map[string]interface{}{
			"status":        model.InvoiceStatusRejected,
			"approved_by":   adminID,
			"reject_reason": reason,
		}).Error; err != nil {
			return err
		}
		// 还原对应订单的发票状态
		return s.resetPaymentInvoiceStatus(tx, &req)
	})
}

// UploadPDF 上传开具的 PDF 并标记为已开具
func (s *Service) UploadPDF(ctx context.Context, id uint, pdfURL string) error {
	if strings.TrimSpace(pdfURL) == "" {
		return fmt.Errorf("pdf_url is required")
	}
	ctx2, cancel := dbctx.Short(ctx)
	defer cancel()

	now := time.Now()
	return s.db.WithContext(ctx2).Transaction(func(tx *gorm.DB) error {
		var req model.InvoiceRequest
		if err := tx.First(&req, id).Error; err != nil {
			return err
		}
		if req.Status != model.InvoiceStatusApproved && req.Status != model.InvoiceStatusPending {
			return fmt.Errorf("only pending/approved requests can have pdf uploaded, current: %s", req.Status)
		}
		if err := tx.Model(&model.InvoiceRequest{}).Where("id = ?", req.ID).Updates(map[string]interface{}{
			"status":    model.InvoiceStatusIssued,
			"pdf_url":   pdfURL,
			"issued_at": &now,
		}).Error; err != nil {
			return err
		}
		// 订单发票状态 -> issued
		return s.updatePaymentInvoiceStatus(tx, &req, model.PaymentInvoiceStatusIssued)
	})
}

// ── helpers ────────────────────────────────────────────────────────────────

func (s *Service) updatePaymentInvoiceStatus(tx *gorm.DB, req *model.InvoiceRequest, newStatus string) error {
	ids, err := parseOrderIDs(req.OrderIDs)
	if err != nil || len(ids) == 0 {
		return nil
	}
	return tx.Model(&model.Payment{}).Where("id IN ?", ids).Update("invoice_status", newStatus).Error
}

func (s *Service) resetPaymentInvoiceStatus(tx *gorm.DB, req *model.InvoiceRequest) error {
	ids, err := parseOrderIDs(req.OrderIDs)
	if err != nil || len(ids) == 0 {
		return nil
	}
	return tx.Model(&model.Payment{}).Where("id IN ?", ids).Updates(map[string]interface{}{
		"invoice_status":     model.PaymentInvoiceStatusNone,
		"invoice_request_id": nil,
	}).Error
}

func parseOrderIDs(raw model.JSON) ([]uint, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var ids []uint
	if err := json.Unmarshal([]byte(raw), &ids); err != nil {
		return nil, err
	}
	return ids, nil
}
