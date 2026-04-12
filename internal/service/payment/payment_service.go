package payment

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"
	"gorm.io/gorm"

	"tokenhub-server/internal/model"
	"tokenhub-server/internal/pkg/credits"
)

// PaymentService 支付业务服务，管理订单创建、回调处理、查询和退款
type PaymentService struct {
	db             *gorm.DB
	redis          *redis.Client
	gateways       map[string]PaymentGateway
	logger         *zap.Logger
	exchangeSvc    *ExchangeService
}

// NewPaymentService 创建支付服务实例，注册所有已配置的支付网关
func NewPaymentService(db *gorm.DB, redisClient *redis.Client, logger *zap.Logger) *PaymentService {
	svc := &PaymentService{
		db:          db,
		redis:       redisClient,
		gateways:    make(map[string]PaymentGateway),
		logger:      logger,
		exchangeSvc: NewExchangeService(db, redisClient),
	}

	// 注册网关；初始化失败仅记录日志，不阻断服务启动
	if wg, err := NewWechatGateway(logger); err == nil {
		svc.gateways["wechat"] = wg
	} else {
		logger.Warn("wechat gateway init skipped", zap.Error(err))
	}

	if ag, err := NewAlipayGateway(logger); err == nil {
		svc.gateways["alipay"] = ag
	} else {
		logger.Warn("alipay gateway init skipped", zap.Error(err))
	}

	svc.gateways["stripe"] = NewStripeGateway(logger)
	svc.gateways["paypal"] = NewPayPalGateway(logger)

	return svc
}

// CreatePayment 创建支付订单
// 1. 生成唯一订单号
// 2. 计算汇率换算和积分数量
// 3. 创建 Payment 记录（状态=pending）
// 4. 调用对应网关的 CreateOrder
// 5. 返回支付参数
func (s *PaymentService) CreatePayment(ctx context.Context, userID, tenantID uint, gateway string, amount float64, currency, subject, returnURL, clientIP string) (*PaymentResult, error) {
	gw, ok := s.gateways[gateway]
	if !ok {
		return nil, fmt.Errorf("unsupported payment gateway: %s", gateway)
	}

	if amount <= 0 {
		return nil, fmt.Errorf("invalid payment amount: %.2f", amount)
	}

	// 计算汇率换算
	exchangeResult, err := s.exchangeSvc.CalculateExchange(ctx, amount, currency)
	if err != nil {
		return nil, fmt.Errorf("calculate exchange: %w", err)
	}

	orderNo := GenerateOrderNo()

	// 在数据库创建支付记录（包含双轨存储字段）
	payment := &model.Payment{
		TenantID:         tenantID,
		UserID:           userID,
		Amount:           amount,
		OriginalCurrency: currency,
		ExchangeRate:     exchangeResult.ExchangeRate,
		FeeRate:          exchangeResult.FeeRate,
		FeeAmount:        exchangeResult.FeeAmount,
		RMBAmount:        exchangeResult.RMBAmount,
		CreditAmount:     exchangeResult.CreditAmount,
		Currency:         "CREDIT",
		Gateway:          gateway,
		Status:           "pending",
	}

	// 将 order_no 存入 metadata，因为模型没有 OrderNo 字段
	meta := map[string]interface{}{"order_no": orderNo}
	if subject != "" {
		meta["subject"] = subject
	}
	metaJSON, _ := json.Marshal(meta)
	payment.Metadata = metaJSON

	if err := s.db.WithContext(ctx).Create(payment).Error; err != nil {
		return nil, fmt.Errorf("create payment record: %w", err)
	}

	// 根据网关配置确定回调 URL
	notifyURL := ""
	switch gateway {
	case "wechat":
		notifyURL = "https://api.tokenhub.com/api/v1/callback/wechat"
	case "alipay":
		notifyURL = "https://api.tokenhub.com/api/v1/callback/alipay"
	case "stripe":
		notifyURL = "https://api.tokenhub.com/api/v1/callback/stripe"
	case "paypal":
		notifyURL = "https://api.tokenhub.com/api/v1/callback/paypal"
	}

	order := &PaymentOrder{
		OrderNo:     orderNo,
		Amount:      amount,
		Currency:    currency,
		Subject:     subject,
		Description: subject,
		ReturnURL:   returnURL,
		NotifyURL:   notifyURL,
		ClientIP:    clientIP,
	}

	result, err := gw.CreateOrder(ctx, order)
	if err != nil {
		// 更新支付状态为失败
		s.db.WithContext(ctx).Model(payment).Update("status", "failed")
		s.logger.Error("gateway create order failed", zap.String("gateway", gateway), zap.Error(err))
		return nil, fmt.Errorf("gateway create order: %w", err)
	}

	// 更新网关交易号
	if result.GatewayTxnID != "" {
		s.db.WithContext(ctx).Model(payment).Update("gateway_txn_id", result.GatewayTxnID)
	}

	// 缓存订单号到支付 ID 的映射（用于回调查找）
	cacheKey := "payment:order:" + orderNo
	s.redis.Set(ctx, cacheKey, payment.ID, 24*time.Hour)

	// 记录审计日志
	s.recordAuditLog(ctx, tenantID, userID, "payment_create", payment.ID, clientIP, map[string]interface{}{
		"order_no":       orderNo,
		"gateway":        gateway,
		"amount":         amount,
		"currency":       currency,
		"rmb_amount":     exchangeResult.RMBAmount,
		"credit_amount":  exchangeResult.CreditAmount,
		"exchange_rate":  exchangeResult.ExchangeRate,
	})

	return result, nil
}

// HandleCallback 处理支付网关回调通知
// 1. 验证签名
// 2. 查找 Payment 记录
// 3. 更新状态（幂等：已完成则跳过）
// 4. 成功则充值用户积分
// 5. 记录审计日志
// 6. 发布支付成功事件到 Redis
func (s *PaymentService) HandleCallback(ctx context.Context, gateway string, data []byte, headers map[string]string) error {
	gw, ok := s.gateways[gateway]
	if !ok {
		return fmt.Errorf("unsupported gateway: %s", gateway)
	}

	// 验证回调签名
	result, err := gw.VerifyCallback(ctx, data, headers)
	if err != nil {
		s.logger.Error("callback verification failed", zap.String("gateway", gateway), zap.Error(err))
		return fmt.Errorf("verify callback: %w", err)
	}

	if result.OrderNo == "" {
		return fmt.Errorf("callback: missing order_no")
	}

	// 从 Redis 缓存或数据库查找支付记录
	paymentID, err := s.findPaymentIDByOrderNo(ctx, result.OrderNo)
	if err != nil {
		return fmt.Errorf("find payment: %w", err)
	}

	var payment model.Payment
	if err := s.db.WithContext(ctx).First(&payment, paymentID).Error; err != nil {
		return fmt.Errorf("load payment: %w", err)
	}

	// 幂等校验：已处于终态则跳过
	if payment.Status == "completed" || payment.Status == "refunded" {
		s.logger.Info("callback skipped, payment already in terminal state",
			zap.String("order_no", result.OrderNo),
			zap.String("status", payment.Status))
		return nil
	}

	// 金额校验（以分为单位比较确保精度）
	if AmountToCents(result.Amount) != AmountToCents(payment.Amount) {
		s.logger.Error("callback amount mismatch",
			zap.Float64("expected", payment.Amount),
			zap.Float64("received", result.Amount))
		return fmt.Errorf("callback: amount mismatch, expected=%.2f received=%.2f", payment.Amount, result.Amount)
	}

	// 更新支付状态：根据回调结果映射为内部状态
	newStatus := "failed"
	if result.Status == "success" {
		newStatus = "completed"
	}

	updates := map[string]interface{}{
		"status":         newStatus,
		"gateway_txn_id": result.GatewayTxnID,
	}

	if err := s.db.WithContext(ctx).Model(&payment).Updates(updates).Error; err != nil {
		return fmt.Errorf("update payment: %w", err)
	}

	// 支付成功则充值用户积分
	if newStatus == "completed" {
		if err := s.creditUserBalance(ctx, payment.UserID, payment.CreditAmount); err != nil {
			s.logger.Error("credit user balance failed",
				zap.Uint("user_id", payment.UserID),
				zap.Int64("credits", payment.CreditAmount),
				zap.Error(err))
			// 不返回错误；支付已标记为完成，由对账任务处理差异
		}

		// 发布支付成功事件
		eventData, _ := json.Marshal(map[string]interface{}{
			"order_no":      result.OrderNo,
			"user_id":       payment.UserID,
			"tenant_id":     payment.TenantID,
			"amount":        payment.Amount,
			"currency":      payment.OriginalCurrency,
			"rmb_amount":    payment.RMBAmount,
			"credit_amount": payment.CreditAmount,
			"gateway":       gateway,
			"paid_at":       result.PaidAt,
		})
		s.redis.Publish(ctx, "payment:success", string(eventData))
	}

	// Record audit log
	s.recordAuditLog(ctx, payment.TenantID, payment.UserID, "payment_callback", payment.ID, "", map[string]interface{}{
		"order_no":       result.OrderNo,
		"gateway":        gateway,
		"status":         newStatus,
		"gateway_txn_id": result.GatewayTxnID,
		"credit_amount":  payment.CreditAmount,
	})

	return nil
}

// QueryPayment 根据订单号查询支付记录
func (s *PaymentService) QueryPayment(ctx context.Context, orderNo string) (*model.Payment, error) {
	paymentID, err := s.findPaymentIDByOrderNo(ctx, orderNo)
	if err != nil {
		return nil, err
	}

	var payment model.Payment
	if err := s.db.WithContext(ctx).First(&payment, paymentID).Error; err != nil {
		return nil, fmt.Errorf("payment not found: %w", err)
	}
	return &payment, nil
}

// RefundPayment 对指定订单号发起退款
func (s *PaymentService) RefundPayment(ctx context.Context, orderNo string, amount float64, reason, clientIP string, operatorID uint) error {
	payment, err := s.QueryPayment(ctx, orderNo)
	if err != nil {
		return err
	}

	if payment.Status != "completed" {
		return fmt.Errorf("cannot refund payment with status: %s", payment.Status)
	}

	if amount <= 0 || AmountToCents(amount) > AmountToCents(payment.Amount) {
		return fmt.Errorf("invalid refund amount: %.2f (order amount: %.2f)", amount, payment.Amount)
	}

	gw, ok := s.gateways[payment.Gateway]
	if !ok {
		return fmt.Errorf("unsupported gateway: %s", payment.Gateway)
	}

	refundResult, err := gw.Refund(ctx, orderNo, amount, reason)
	if err != nil {
		s.logger.Error("refund failed", zap.String("order_no", orderNo), zap.Error(err))
		return fmt.Errorf("gateway refund: %w", err)
	}

	// 更新支付状态为已退款
	if refundResult.Status == "success" {
		s.db.WithContext(ctx).Model(payment).Update("status", "refunded")
	}

	// Record audit log
	s.recordAuditLog(ctx, payment.TenantID, operatorID, "payment_refund", payment.ID, clientIP, map[string]interface{}{
		"order_no":          orderNo,
		"refund_amount":     amount,
		"reason":            reason,
		"refund_status":     refundResult.Status,
		"gateway_refund_id": refundResult.GatewayRefundID,
	})

	return nil
}

// ListPayments 分页查询用户的支付记录
func (s *PaymentService) ListPayments(ctx context.Context, userID uint, page, pageSize int) ([]model.Payment, int64, error) {
	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > 100 {
		pageSize = 20
	}

	var total int64
	var payments []model.Payment

	query := s.db.WithContext(ctx).Model(&model.Payment{}).Where("user_id = ?", userID)

	if err := query.Count(&total).Error; err != nil {
		return nil, 0, fmt.Errorf("count payments: %w", err)
	}

	offset := (page - 1) * pageSize
	if err := query.Order("created_at DESC").Offset(offset).Limit(pageSize).Find(&payments).Error; err != nil {
		return nil, 0, fmt.Errorf("list payments: %w", err)
	}

	return payments, total, nil
}

// findPaymentIDByOrderNo 通过订单号查找支付 ID，优先从 Redis 缓存获取，回退至数据库查询
func (s *PaymentService) findPaymentIDByOrderNo(ctx context.Context, orderNo string) (uint, error) {
	cacheKey := "payment:order:" + orderNo
	val, err := s.redis.Get(ctx, cacheKey).Uint64()
	if err == nil && val > 0 {
		return uint(val), nil
	}

	// 回退：通过 metadata JSON 中的 order_no 查找
	var payment model.Payment
	err = s.db.WithContext(ctx).
		Where("JSON_EXTRACT(metadata, '$.order_no') = ?", orderNo).
		First(&payment).Error
	if err != nil {
		return 0, fmt.Errorf("payment not found for order_no=%s: %w", orderNo, err)
	}

	// 缓存供下次查找使用
	s.redis.Set(ctx, cacheKey, payment.ID, 24*time.Hour)
	return payment.ID, nil
}

// creditUserBalance 将支付金额充入用户积分余额
// 直接调用 BalanceService.Recharge
func (s *PaymentService) creditUserBalance(ctx context.Context, userID uint, creditAmount int64) error {
	// 查询用户的租户ID
	var user model.User
	if err := s.db.WithContext(ctx).First(&user, userID).Error; err != nil {
		return fmt.Errorf("user not found: %w", err)
	}

	// 更新 user_balances 表
	var ub model.UserBalance
	if err := s.db.WithContext(ctx).Where("user_id = ?", userID).First(&ub).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			// 创建余额记录
			ub = model.UserBalance{
				UserID:   userID,
				TenantID: user.TenantID,
				Balance:  creditAmount,
				BalanceRMB: credits.CreditsToRMB(creditAmount),
				Currency: "CREDIT",
			}
			return s.db.WithContext(ctx).Create(&ub).Error
		}
		return err
	}

	// 增加积分余额
	ub.Balance += creditAmount
	ub.BalanceRMB = credits.CreditsToRMB(ub.Balance)
	return s.db.WithContext(ctx).Save(&ub).Error
}

// recordAuditLog 记录支付操作的审计日志
func (s *PaymentService) recordAuditLog(ctx context.Context, tenantID, userID uint, action string, resourceID uint, ip string, details map[string]interface{}) {
	detailsJSON, _ := json.Marshal(details)
	log := &model.AuditLog{
		TenantID:   tenantID,
		UserID:     userID,
		Action:     action,
		Resource:   "payment",
		ResourceID: resourceID,
		Details:    detailsJSON,
		IP:         ip,
	}
	if err := s.db.WithContext(ctx).Create(log).Error; err != nil {
		s.logger.Error("failed to create audit log", zap.Error(err))
	}
}

// GetExchangeService 获取汇率服务实例
func (s *PaymentService) GetExchangeService() *ExchangeService {
	return s.exchangeSvc
}
