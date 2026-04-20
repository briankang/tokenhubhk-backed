package payment

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"go.uber.org/zap"

	"tokenhub-server/internal/model"
)

// ===== v3.2 扩展：AccountRouter + EventLogger 可选注入 =====
//
// 为避免破坏现有 CreatePayment/HandleCallback 的向后兼容逻辑，
// 我们用 setter 方式追加新能力，在需要时调用新方法 CreatePaymentV2

// SetAccountRouter 注入多账号路由器
func (s *PaymentService) SetAccountRouter(r *AccountRouter) {
	s.accountRouter = r
}

// SetEventLogger 注入事件日志
func (s *PaymentService) SetEventLogger(el *EventLogger) {
	s.eventLogger = el
}

// SetExchangeFetcher 注入实时汇率获取器（用于 USD 订单双币快照）
func (s *PaymentService) SetExchangeFetcher(f UsdToCnyFetcher) {
	s.exchangeFetcher = f
}

// UsdToCnyFetcher 抽象 USD→CNY 汇率获取
type UsdToCnyFetcher interface {
	GetUSDToCNY(ctx context.Context) (float64, error)
}

// InvokeGatewayRefund 实现 RefundService.RefundGatewayInvoker 接口
//
// 根据 payment 的 gateway 字段寻找对应网关实例并调用其 Refund 方法
//
// 开发旁路（PAYMENT_CALLBACK_DEV_BYPASS=true）：
//   跳过真实网关调用，直接返回模拟成功的 RefundResult。
//   仅用于本地/沙盒环境验证退款审计链路（refund_requests/payments/user_balances/balance_records）。
//   生产环境必须保持 PAYMENT_CALLBACK_DEV_BYPASS=false。
func (s *PaymentService) InvokeGatewayRefund(ctx context.Context, payment *model.Payment, amount float64, reason string) (*RefundResult, error) {
	if payment == nil {
		return nil, fmt.Errorf("nil payment")
	}

	// 提取 order_no
	orderNo := extractOrderNoFromMeta(payment)
	if orderNo == "" {
		return nil, fmt.Errorf("order_no not found in payment metadata")
	}

	// 开发旁路：跳过网关 API，模拟退款成功
	if IsCallbackDevBypassEnabled() {
		s.logger.Warn("refund: dev bypass enabled, skipping real gateway refund",
			zap.String("gateway", payment.Gateway),
			zap.String("order_no", orderNo),
			zap.Float64("amount", amount))
		return &RefundResult{
			GatewayRefundID: fmt.Sprintf("MOCK_REFUND_%s_%d", payment.Gateway, time.Now().UnixNano()),
			Status:          "success",
			Amount:          amount,
		}, nil
	}

	gw, ok := s.gateways[payment.Gateway]
	if !ok {
		return nil, fmt.Errorf("gateway %s not registered", payment.Gateway)
	}
	return gw.Refund(ctx, orderNo, amount, reason)
}

// LogPaymentEvent 对外暴露事件日志接口（供 handler 使用）
func (s *PaymentService) LogPaymentEvent(ctx context.Context, evt PaymentEvent) {
	if s.eventLogger != nil {
		s.eventLogger.Log(ctx, evt)
	}
}

// SelectAccount 选择账号（供 CreatePaymentV2 使用，也供管理员测试连通性等场景）
func (s *PaymentService) SelectAccount(ctx context.Context, req SelectAccountRequest) (*model.PaymentProviderAccount, error) {
	if s.accountRouter == nil {
		return nil, fmt.Errorf("account router not configured")
	}
	return s.accountRouter.SelectAccount(ctx, req)
}

// UpdateAccountAfterPayment 支付成功/失败后更新账号统计
func (s *PaymentService) UpdateAccountAfterPayment(ctx context.Context, accountID uint64, success bool, amountRMB float64, reason string) {
	if s.accountRouter == nil || accountID == 0 {
		return
	}
	if success {
		s.accountRouter.MarkAccountSuccess(ctx, accountID, amountRMB)
	} else {
		s.accountRouter.MarkAccountFailed(ctx, accountID, reason)
	}
}

// CreatePaymentWithRouting 使用 AccountRouter 选择账号的支付入口（v3.2）
//
// 与原 CreatePayment 流程相同，但：
//  1. 若 accountRouter 可用，按 currency+region 选账号
//  2. 支付网关失败时自动重试下一账号（最多 maxRetries 次）
//  3. 每一步写事件日志
//  4. 订单记录 provider_account_id / display_amount_usd / display_amount_cny / exchange_rate_used
func (s *PaymentService) CreatePaymentWithRouting(ctx context.Context, req CreatePaymentWithRoutingInput) (*PaymentResult, error) {
	if req.Amount <= 0 {
		return nil, fmt.Errorf("invalid amount")
	}
	// 预先计算汇率快照（用于审计）
	var displayAmountCNY, displayAmountUSD, rateUsed float64
	switch req.Currency {
	case "USD":
		displayAmountUSD = req.Amount
		if s.exchangeFetcher != nil {
			rate, _ := s.exchangeFetcher.GetUSDToCNY(ctx)
			rateUsed = rate
			displayAmountCNY = roundHalf(req.Amount * rate)
		}
	case "CNY", "":
		displayAmountCNY = req.Amount
		if s.exchangeFetcher != nil {
			rate, _ := s.exchangeFetcher.GetUSDToCNY(ctx)
			if rate > 0 {
				rateUsed = rate
				displayAmountUSD = roundHalf(req.Amount / rate)
			}
		}
	}

	// 重试逻辑
	const maxRetries = 3
	excluded := make([]uint64, 0, maxRetries)
	var lastErr error
	for attempt := 0; attempt < maxRetries; attempt++ {
		var accountID uint64
		// 选账号（若 accountRouter 可用）
		var account *model.PaymentProviderAccount
		if s.accountRouter != nil {
			acc, err := s.accountRouter.SelectAccount(ctx, SelectAccountRequest{
				ProviderType: req.Gateway,
				Currency:     req.Currency,
				Region:       req.Region,
				ExcludeIDs:   excluded,
			})
			if err != nil {
				// 无可用账号 → 降级走单账号路径（原 CreatePayment）
				if attempt == 0 {
					return s.createWithoutRouting(ctx, req, displayAmountCNY, displayAmountUSD, rateUsed)
				}
				return nil, fmt.Errorf("select account: %w", err)
			}
			account = acc
			accountID = acc.ID
		}

		result, err := s.createOrderOnce(ctx, req, account, displayAmountCNY, displayAmountUSD, rateUsed)
		if err == nil {
			// 成功
			s.UpdateAccountAfterPayment(ctx, accountID, true, displayAmountCNY, "")
			return result, nil
		}
		lastErr = err
		s.UpdateAccountAfterPayment(ctx, accountID, false, 0, err.Error())
		if accountID > 0 {
			excluded = append(excluded, accountID)
		}
		s.logger.Warn("payment create attempt failed, retrying",
			zap.Int("attempt", attempt+1),
			zap.Uint64("account_id", accountID),
			zap.Error(err))
	}

	return nil, fmt.Errorf("all attempts failed: %w", lastErr)
}

// CreatePaymentWithRoutingInput 输入参数
type CreatePaymentWithRoutingInput struct {
	UserID    uint
	TenantID  uint
	Gateway   string
	Amount    float64
	Currency  string
	Region    string
	Subject   string
	ReturnURL string
	ClientIP  string
}

// createOrderOnce 单次建单（特定账号）
func (s *PaymentService) createOrderOnce(ctx context.Context, req CreatePaymentWithRoutingInput, account *model.PaymentProviderAccount, displayCNY, displayUSD, rateUsed float64) (*PaymentResult, error) {
	gw, ok := s.gateways[req.Gateway]
	if !ok {
		return nil, fmt.Errorf("gateway not registered: %s", req.Gateway)
	}

	// 汇率换算
	exchangeResult, err := s.exchangeSvc.CalculateExchange(ctx, req.Amount, req.Currency)
	if err != nil {
		return nil, fmt.Errorf("calculate exchange: %w", err)
	}
	orderNo := GenerateOrderNo()

	var accID *uint64
	if account != nil {
		id := account.ID
		accID = &id
	}

	payment := &model.Payment{
		TenantID:          req.TenantID,
		UserID:            req.UserID,
		Amount:            req.Amount,
		OriginalCurrency:  req.Currency,
		ExchangeRate:      exchangeResult.ExchangeRate,
		FeeRate:           exchangeResult.FeeRate,
		FeeAmount:         exchangeResult.FeeAmount,
		RMBAmount:         exchangeResult.RMBAmount,
		CreditAmount:      exchangeResult.CreditAmount,
		Currency:          "CREDIT",
		Gateway:           req.Gateway,
		Status:            model.PaymentStatusPending,
		ProviderAccountID: accID,
		DisplayCurrency:   req.Currency,
		DisplayAmountUSD:  displayUSD,
		DisplayAmountCNY:  displayCNY,
		ExchangeRateUsed:  rateUsed,
	}
	meta := map[string]interface{}{"order_no": orderNo}
	if req.Subject != "" {
		meta["subject"] = req.Subject
	}
	metaJSON, _ := json.Marshal(meta)
	payment.Metadata = metaJSON

	if err := s.db.WithContext(ctx).Create(payment).Error; err != nil {
		return nil, fmt.Errorf("create payment record: %w", err)
	}

	uid := uint64(req.UserID)
	s.LogPaymentEvent(ctx, PaymentEvent{
		PaymentID: pUint64Ptr(payment.ID),
		OrderNo:   orderNo,
		EventType: model.EventPaymentCreated,
		ActorType: model.ActorUser,
		ActorID:   &uid,
		Gateway:   req.Gateway,
		AccountID: accID,
		IP:        req.ClientIP,
		Payload: map[string]interface{}{
			"amount":   req.Amount,
			"currency": req.Currency,
			"region":   req.Region,
		},
		Success: true,
	})

	// 通知 URL
	notifyURL := ""
	switch req.Gateway {
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
		Amount:      req.Amount,
		Currency:    req.Currency,
		Subject:     req.Subject,
		Description: req.Subject,
		ReturnURL:   req.ReturnURL,
		NotifyURL:   notifyURL,
		ClientIP:    req.ClientIP,
	}

	// 调网关
	start := time.Now()
	s.LogPaymentEvent(ctx, PaymentEvent{
		PaymentID: pUint64Ptr(payment.ID),
		OrderNo:   orderNo,
		EventType: model.EventPaymentGatewayRequest,
		ActorType: model.ActorSystem,
		Gateway:   req.Gateway,
		AccountID: accID,
		Payload:   order,
		Success:   true,
	})
	result, err := gw.CreateOrder(ctx, order)
	dur := time.Since(start).Milliseconds()

	if err != nil {
		s.db.WithContext(ctx).Model(&model.Payment{}).Where("id = ?", payment.ID).Update("status", model.PaymentStatusFailed)
		s.LogPaymentEvent(ctx, PaymentEvent{
			PaymentID:  pUint64Ptr(payment.ID),
			OrderNo:    orderNo,
			EventType:  model.EventPaymentGatewayResponse,
			ActorType:  model.ActorGateway,
			Gateway:    req.Gateway,
			AccountID:  accID,
			Success:    false,
			Err:        err,
			DurationMs: dur,
		})
		return nil, err
	}

	if result.GatewayTxnID != "" {
		s.db.WithContext(ctx).Model(&model.Payment{}).Where("id = ?", payment.ID).Update("gateway_txn_id", result.GatewayTxnID)
	}

	cacheKey := "payment:order:" + orderNo
	s.redis.Set(ctx, cacheKey, payment.ID, 24*time.Hour)

	s.LogPaymentEvent(ctx, PaymentEvent{
		PaymentID:  pUint64Ptr(payment.ID),
		OrderNo:    orderNo,
		EventType:  model.EventPaymentGatewayResponse,
		ActorType:  model.ActorGateway,
		Gateway:    req.Gateway,
		AccountID:  accID,
		Result:     result,
		Success:    true,
		DurationMs: dur,
	})

	return result, nil
}

// createWithoutRouting fallback 到无 accountRouter 的单账号路径
func (s *PaymentService) createWithoutRouting(ctx context.Context, req CreatePaymentWithRoutingInput, displayCNY, displayUSD, rateUsed float64) (*PaymentResult, error) {
	result, err := s.CreatePayment(ctx, req.UserID, req.TenantID, req.Gateway, req.Amount, req.Currency, req.Subject, req.ReturnURL, req.ClientIP)
	if err != nil {
		return nil, err
	}
	// 补写双币快照（通过 order_no 找回 payment 更新）
	if result != nil && result.OrderNo != "" && (displayCNY > 0 || displayUSD > 0) {
		_ = s.db.WithContext(ctx).Model(&model.Payment{}).
			Where("JSON_EXTRACT(metadata, '$.order_no') = ?", result.OrderNo).
			Updates(map[string]interface{}{
				"display_currency":    req.Currency,
				"display_amount_usd":  displayUSD,
				"display_amount_cny":  displayCNY,
				"exchange_rate_used":  rateUsed,
			}).Error
	}
	return result, nil
}

// pUint64Ptr 将 uint 转为 *uint64
func pUint64Ptr(v uint) *uint64 {
	u := uint64(v)
	return &u
}

// extractOrderNoFromMeta 从 payment.Metadata 中提取 order_no
func extractOrderNoFromMeta(p *model.Payment) string {
	if len(p.Metadata) == 0 {
		return ""
	}
	var meta map[string]interface{}
	if err := json.Unmarshal(p.Metadata, &meta); err != nil {
		return ""
	}
	if v, ok := meta["order_no"]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

// roundHalf 4 舍 5 入保留 2 位
func roundHalf(v float64) float64 {
	return float64(int64(v*100+0.5)) / 100
}
