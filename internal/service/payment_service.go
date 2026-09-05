package service

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"payment/internal/channel"
	"payment/internal/errcode"
	"payment/internal/model"
	"payment/internal/pkg/idgen"
	"payment/internal/pkg/logger"
	"payment/internal/repository"
)

// defaultTradeType 是未显式指定时采用的交易类型。
//
// 本项目对接的是微信支付 JSAPI / 小程序场景，因此默认 JSAPI。
const defaultTradeType = channel.TradeTypeJSAPI

// 以下哨兵错误用于在 repository.Mutate 的回调内部表达「无需修改」。
//
// Mutate 的约定是回调返回错误即放弃本次修改，因此「已经是目标状态」
// 这类幂等命中必须通过错误传出，而不是返回 nil 后让状态机去拒绝流转——
// 后者会产生一条无意义的错误日志，掩盖真正的非法流转。
var (
	errAlreadyPaidConcurrently = errors.New("service: 订单已被其他请求标记为已支付")
	errPaymentAlreadyFinal     = errors.New("service: 支付流水已处于终态")
	errOrderAlreadyFinal       = errors.New("service: 订单已处于终态")
)

// PaymentService 支付业务。
//
// 负责发起支付、处理渠道回调、查询支付状态与主动查单兜底。
type PaymentService struct {
	orders   repository.OrderRepository
	payments repository.PaymentRepository
	registry *channel.Registry

	// notifyURL 传给渠道的完整回调地址
	notifyURL string
	// paymentNoPrefix 支付流水号前缀
	paymentNoPrefix string
}

// PaymentServiceOptions 构造 PaymentService 所需的配置项。
//
// 用选项结构体而不是位置参数：这类依赖后续必然会增长
// （超时时间、重试策略、交易类型白名单），位置参数会不断改变签名。
type PaymentServiceOptions struct {
	// NotifyURL 完整回调地址，必须是公网可达的 HTTPS
	NotifyURL string
	// PaymentNoPrefix 支付流水号前缀
	PaymentNoPrefix string
}

// NewPaymentService 构造支付服务。
func NewPaymentService(
	orders repository.OrderRepository,
	payments repository.PaymentRepository,
	registry *channel.Registry,
	opts PaymentServiceOptions,
) *PaymentService {
	return &PaymentService{
		orders:          orders,
		payments:        payments,
		registry:        registry,
		notifyURL:       opts.NotifyURL,
		paymentNoPrefix: opts.PaymentNoPrefix,
	}
}

// PrepayParams 发起支付入参。
type PrepayParams struct {
	// OutTradeNo 商户订单号
	OutTradeNo string
	// TradeType 交易类型，留空按 JSAPI 处理
	TradeType channel.TradeType
	// OpenID 支付者 openid，留空则回退到订单上已保存的值
	OpenID string
	// ClientIP 客户端 IP
	ClientIP string
}

// PrepayOutcome 发起支付的结果。
type PrepayOutcome struct {
	// Order 推进后的订单
	Order *model.Order
	// Payment 本次支付尝试对应的流水
	Payment *model.Payment
	// Result 渠道返回的下单结果，含前端调起支付所需参数
	Result *channel.PrepayResult
	// TradeType 实际使用的交易类型
	TradeType channel.TradeType
}

// Prepay 调用渠道下单，返回前端调起支付所需的参数。
//
// 完整流程：校验订单可支付 -> 创建流水(INIT) -> 调用渠道 -> 流水置 PREPAID
// -> 订单置 PAYING。
//
// 每一步失败的处理方式都不同，这是本方法最需要小心的地方：
//   - 渠道下单失败：流水置 FAILED 留痕，订单状态不动，用户可重新发起
//   - 渠道成功但订单状态推进失败：仍然返回调起参数。宁可让用户付进来
//     再由回调与查单兜底，也不要因为本地状态未更新而拒绝收款
func (s *PaymentService) Prepay(ctx context.Context, params PrepayParams) (*PrepayOutcome, error) {
	log := logger.L(ctx)

	if params.OutTradeNo == "" {
		return nil, errcode.ErrInvalidParam.WithMsg("out_trade_no 不能为空")
	}

	order, err := s.orders.GetByOutTradeNo(ctx, params.OutTradeNo)
	if err != nil {
		return nil, err
	}
	log = log.With(logger.String("out_trade_no", order.OutTradeNo))

	if !order.CanPay() {
		return nil, unpayableError(order)
	}

	// 渠道以订单上记录的为准，不接受发起支付时临时改渠道：
	// 同一订单在不同渠道间跳变会让对账完全无法进行
	code := order.Channel
	gateway, err := s.registry.Get(code)
	if err != nil {
		return nil, err
	}

	tradeType := params.TradeType
	if tradeType == "" {
		tradeType = defaultTradeType
	}
	openID := params.OpenID
	if openID == "" {
		openID = order.OpenID
	}
	// JSAPI 必须有 openid，这是微信侧的硬性要求，提前拦截可以避免一次无谓的渠道调用
	if tradeType == channel.TradeTypeJSAPI && openID == "" {
		return nil, errcode.ErrOpenIDRequired.WithMsg("JSAPI 交易必须提供 openid，" +
			"可通过公众号网页授权或小程序 wx.login 获取")
	}

	paymentNo := idgen.PaymentNo(s.paymentNoPrefix)
	payment := model.NewPayment(paymentNo, order.OutTradeNo, order.AmountTotal, code, tradeType)
	if err := s.payments.Create(ctx, payment); err != nil {
		return nil, errcode.ErrInternal.WithCause(err)
	}

	result, err := gateway.Prepay(ctx, channel.PrepayRequest{
		OutTradeNo:  order.OutTradeNo,
		Description: order.Subject,
		AmountTotal: order.AmountTotal,
		Currency:    order.Currency,
		Attach:      order.Attach,
		NotifyURL:   s.notifyURL,
		TradeType:   tradeType,
		PayerOpenID: openID,
		ClientIP:    params.ClientIP,
	})
	if err != nil {
		now := time.Now()
		if _, mErr := s.payments.MutateByPaymentNo(ctx, paymentNo, func(p *model.Payment) error {
			return p.MarkFailed("", "渠道下单失败", now)
		}); mErr != nil {
			log.Warn("标记失败流水时出错",
				logger.String("payment_no", paymentNo), logger.Error(mErr))
		}
		log.Warn("渠道下单失败",
			logger.String("channel", code.String()),
			logger.String("payment_no", paymentNo),
			logger.Error(err),
		)
		return nil, err
	}

	now := time.Now()
	payment, err = s.payments.MutateByPaymentNo(ctx, paymentNo, func(p *model.Payment) error {
		return p.MarkPrepaid(result.PrepayID, now)
	})
	if err != nil {
		return nil, errcode.ErrInternal.WithCause(err)
	}

	updated, err := s.orders.Mutate(ctx, order.OutTradeNo, func(o *model.Order) error {
		// 渠道调用期间回调可能已经到达并把订单推进到 PAID，
		// 此时绝不能再把它拉回 PAYING
		if o.Status.IsPaid() {
			return errAlreadyPaidConcurrently
		}
		// 补记 openid：本次支付才拿到 openid 的情况下，
		// 落库后后续查单与客服排查才有依据
		if o.OpenID == "" && openID != "" {
			o.OpenID = openID
		}
		return o.MarkPaying(now)
	})
	switch {
	case errors.Is(err, errAlreadyPaidConcurrently):
		// 订单已支付，本次调起参数没有意义，明确告知调用方
		log.Info("发起支付时发现订单已支付")
		return nil, errcode.ErrOrderAlreadyPaid
	case err != nil:
		log.Error("渠道下单成功但订单状态未推进，已返回调起参数由回调兜底",
			logger.String("payment_no", paymentNo),
			logger.String("prepay_id", result.PrepayID),
			logger.Error(err),
		)
		updated = order
	}

	log.Info("发起支付成功",
		logger.String("payment_no", paymentNo),
		logger.String("channel", code.String()),
		logger.String("trade_type", tradeType.String()),
	)
	return &PrepayOutcome{
		Order:     updated,
		Payment:   payment,
		Result:    result,
		TradeType: tradeType,
	}, nil
}

// unpayableError 把「订单不可支付」翻译成对应的业务错误码。
//
// 分开返回 ErrOrderAlreadyPaid / ErrOrderClosed 而不是统一的「状态不允许」，
// 是因为客户端对这两者的处理完全不同：前者应直接跳支付成功页，
// 后者应引导用户重新下单。
func unpayableError(o *model.Order) error {
	switch o.Status {
	case model.OrderStatusPaid:
		return errcode.ErrOrderAlreadyPaid
	case model.OrderStatusClosed:
		return errcode.ErrOrderClosed
	case model.OrderStatusRefunded:
		return errcode.ErrOrderStatusInvalid.WithMsg("订单已退款，不能再次支付")
	default:
		return errcode.ErrOrderStatusInvalid
	}
}

// NotifyOutcome 回调处理结果。
type NotifyOutcome struct {
	// Order 处理后的订单
	Order *model.Order
	// Payment 被推进的支付流水，可能为 nil
	Payment *model.Payment
	// TradeState 渠道回传的交易状态
	TradeState string
	// Idempotent 为 true 表示订单在本次调用前就已支付，状态未被重复推进
	Idempotent bool
}

// HandleNotify 解析并处理渠道的异步回调。
//
// 直接接收 *http.Request 而不是 []byte，是因为真实渠道的验签
// 依赖 Wechatpay-Signature / Timestamp / Nonce / Serial 四个请求头，
// 提前把 body 读出来会让 Gateway 实现无法完成验签。
func (s *PaymentService) HandleNotify(ctx context.Context, code channel.Code, r *http.Request) (*NotifyOutcome, error) {
	gateway, err := s.registry.Get(code)
	if err != nil {
		return nil, err
	}
	// 验签与解密失败必须原样返回错误：由渠道实现决定这属于哪一类错误码，
	// 上层不能把「验签失败」降级成「内部错误」，否则会丢失安全告警信号
	payload, err := gateway.ParseNotify(ctx, r)
	if err != nil {
		return nil, err
	}
	return s.HandlePayload(ctx, code, payload)
}

// HandlePayload 处理已经解析完成的交易状态。
//
// 回调与主动查单共用这一条路径，保证两者对同一笔交易的处理结果完全一致。
// 这是掉单兜底能够成立的前提：如果查单走另一套逻辑，
// 两套逻辑的细微差异会在生产环境里变成难以复现的状态不一致。
func (s *PaymentService) HandlePayload(ctx context.Context, code channel.Code, payload *channel.NotifyPayload) (*NotifyOutcome, error) {
	if payload == nil {
		return nil, errcode.ErrInvalidParam.WithMsg("交易状态报文为空")
	}
	if payload.OutTradeNo == "" {
		return nil, errcode.ErrInvalidParam.WithMsg("交易状态报文缺少 out_trade_no")
	}

	log := logger.L(ctx).With(
		logger.String("out_trade_no", payload.OutTradeNo),
		logger.String("channel", code.String()),
		logger.String("trade_state", payload.TradeState),
	)

	order, err := s.orders.GetByOutTradeNo(ctx, payload.OutTradeNo)
	if err != nil {
		return nil, err
	}

	// 幂等快速返回。
	//
	// 微信支付在未收到成功应答时会按 15s/15s/30s/3m/... 的间隔重复投递，
	// 同一笔交易收到多次回调是常态而非异常。已支付的订单必须原样返回成功，
	// 让渠道停止重试，而不是报错导致渠道无限重投。
	if order.Status.IsPaid() {
		log.Info("重复回调命中幂等分支，订单已是已支付状态")
		return &NotifyOutcome{
			Order:      order,
			TradeState: payload.TradeState,
			Idempotent: true,
		}, nil
	}

	if !payload.IsSuccess() {
		s.handleNonSuccess(ctx, order, payload)
		return &NotifyOutcome{Order: order, TradeState: payload.TradeState}, nil
	}

	// 金额校验必须在推进状态之前，且不一致时坚决拒绝。
	//
	// 这里防的是「订单被篡改金额后支付」与「串单」两类资金事故：
	// 攻击者改小前端金额、或把 A 订单的回调投给 B 订单，都会表现为金额不符。
	// 一旦置为已支付就无法回退，只能人工追款，所以宁可让渠道重试也不放行。
	if payload.AmountTotal != order.AmountTotal {
		log.Error("回调金额与订单金额不一致，拒绝置为已支付",
			logger.Int64("notify_amount_fen", payload.AmountTotal),
			logger.Int64("order_amount_fen", order.AmountTotal),
			logger.String("transaction_id", payload.TransactionID),
		)
		return nil, errcode.ErrAmountMismatch.WithMsg(fmt.Sprintf(
			"回调金额 %d 分与订单金额 %d 分不一致", payload.AmountTotal, order.AmountTotal))
	}

	now := time.Now()
	successTime := payload.SuccessTime
	if successTime.IsZero() {
		// 渠道未下发成功时间时用本地时间兜底，避免 PaidAt 为零值
		successTime = now
	}

	updated, err := s.orders.Mutate(ctx, order.OutTradeNo, func(o *model.Order) error {
		// 并发的重复回调可能已经推进过，此处必须再判一次：
		// 上面那次 IsPaid 检查在锁外，无法阻止两个回调同时通过
		if o.Status.IsPaid() {
			return errAlreadyPaidConcurrently
		}
		return o.MarkPaid(payload.TransactionID, now)
	})
	if err != nil {
		if errors.Is(err, errAlreadyPaidConcurrently) {
			log.Info("并发回调已被其他请求处理，按幂等返回")
			latest, gErr := s.orders.GetByOutTradeNo(ctx, order.OutTradeNo)
			if gErr != nil {
				return nil, gErr
			}
			return &NotifyOutcome{
				Order:      latest,
				TradeState: payload.TradeState,
				Idempotent: true,
			}, nil
		}
		log.Error("推进订单状态失败", logger.Error(err))
		return nil, err
	}

	payment := s.markPaymentSuccess(ctx, payload, successTime, now)

	log.Info("订单支付成功",
		logger.String("transaction_id", payload.TransactionID),
		logger.Int64("amount_fen", payload.AmountTotal),
		logger.Int64("payer_amount_fen", payload.AmountPayerTotal),
	)
	return &NotifyOutcome{
		Order:      updated,
		Payment:    payment,
		TradeState: payload.TradeState,
	}, nil
}

// handleNonSuccess 处理非成功状态的交易通知。
func (s *PaymentService) handleNonSuccess(ctx context.Context, order *model.Order, payload *channel.NotifyPayload) {
	log := logger.L(ctx).With(logger.String("out_trade_no", order.OutTradeNo))
	now := time.Now()

	switch payload.TradeState {
	case channel.TradeStateClosed:
		if _, err := s.orders.Mutate(ctx, order.OutTradeNo, func(o *model.Order) error {
			if o.Status == model.OrderStatusClosed {
				return errOrderAlreadyFinal
			}
			return o.MarkClosed(now)
		}); err != nil && !errors.Is(err, errOrderAlreadyFinal) {
			log.Warn("关闭订单失败", logger.Error(err))
		}
		s.forEachNonTerminalPayment(ctx, order.OutTradeNo, func(p *model.Payment) error {
			return p.MarkClosed(payload.TradeStateDesc, now)
		})

	case channel.TradeStatePayError:
		// 支付失败只终结流水，不动订单状态：
		// 用户可以换一种支付方式或重新发起，订单本身仍然有效
		s.forEachNonTerminalPayment(ctx, order.OutTradeNo, func(p *model.Payment) error {
			return p.MarkFailed(payload.TradeState, payload.TradeStateDesc, now)
		})

	default:
		// NOTPAY / USERPAYING / REFUND 等：交易尚无结论，不做任何状态变更。
		//
		// USERPAYING 尤其不能当成失败处理——用户已输入密码但结果未定，
		// 此时若把订单置为失败，随后到达的成功回调就会与本地状态冲突，
		// 表现为「用户已扣款但订单显示失败」，是最难处理的一类客诉。
		log.Debug("交易状态非终态，本次通知不做状态变更")
	}
}

// markPaymentSuccess 把对应的支付流水推进到 SUCCESS。
//
// 流水推进失败只记日志、不返回错误：订单已经置为已支付这个事实不能回退，
// 因为钱确实已经收进来了。流水属于事后可以补记的辅助数据，
// 让它去阻断回调应答会导致渠道持续重试，反而放大问题。
func (s *PaymentService) markPaymentSuccess(
	ctx context.Context,
	payload *channel.NotifyPayload,
	successTime, now time.Time,
) *model.Payment {
	log := logger.L(ctx).With(logger.String("out_trade_no", payload.OutTradeNo))

	payment, err := s.pickPayment(ctx, payload)
	if err != nil {
		log.Warn("未找到匹配的支付流水，订单已置为已支付但缺少流水记录", logger.Error(err))
		return nil
	}

	updated, err := s.payments.MutateByPaymentNo(ctx, payment.PaymentNo, func(p *model.Payment) error {
		if p.Status.IsSuccess() {
			return errPaymentAlreadyFinal
		}
		return p.MarkSuccess(
			payload.TransactionID, payload.TradeState, payload.TradeStateDesc,
			payload.OpenID, successTime, now,
		)
	})
	if err != nil {
		if errors.Is(err, errPaymentAlreadyFinal) {
			return payment
		}
		log.Warn("推进支付流水状态失败",
			logger.String("payment_no", payment.PaymentNo), logger.Error(err))
		return payment
	}
	return updated
}

// pickPayment 找出本次回调对应的支付流水。
//
// 匹配优先级：渠道交易号精确匹配 > 最新一条未终结流水 > 最后一条流水。
//
// 一个订单可能有多次支付尝试，回调报文里只有 out_trade_no 与 transaction_id，
// 没有本系统的流水号，因此必须按这个优先级推断。
// 找不到时返回错误而不是新建一条：凭空造出的流水没有任何渠道信息，
// 对账时反而会造成困扰。
func (s *PaymentService) pickPayment(ctx context.Context, payload *channel.NotifyPayload) (*model.Payment, error) {
	list, err := s.payments.ListByOutTradeNo(ctx, payload.OutTradeNo)
	if err != nil {
		return nil, err
	}
	if len(list) == 0 {
		return nil, errcode.ErrPaymentNotFound.WithMsg("订单 " + payload.OutTradeNo + " 没有支付流水")
	}

	if payload.TransactionID != "" {
		for _, p := range list {
			if p.TransactionID == payload.TransactionID {
				return p, nil
			}
		}
	}
	// list 已按创建时间升序，倒序遍历即取最新
	for i := len(list) - 1; i >= 0; i-- {
		if !list[i].Status.IsTerminal() {
			return list[i], nil
		}
	}
	return list[len(list)-1], nil
}

// forEachNonTerminalPayment 对某订单下所有未终结的流水执行 fn。
func (s *PaymentService) forEachNonTerminalPayment(
	ctx context.Context,
	outTradeNo string,
	fn func(p *model.Payment) error,
) {
	log := logger.L(ctx)

	list, err := s.payments.ListByOutTradeNo(ctx, outTradeNo)
	if err != nil {
		log.Warn("查询支付流水失败",
			logger.String("out_trade_no", outTradeNo), logger.Error(err))
		return
	}

	for _, item := range list {
		if item.Status.IsTerminal() {
			continue
		}
		paymentNo := item.PaymentNo
		_, err := s.payments.MutateByPaymentNo(ctx, paymentNo, func(p *model.Payment) error {
			// 锁内再判一次：列出流水到执行修改之间，状态可能已被其他请求推进
			if p.Status.IsTerminal() {
				return errPaymentAlreadyFinal
			}
			return fn(p)
		})
		if err != nil && !errors.Is(err, errPaymentAlreadyFinal) {
			log.Warn("更新支付流水失败",
				logger.String("payment_no", paymentNo), logger.Error(err))
		}
	}
}

// PaymentStatus 支付状态查询结果。
type PaymentStatus struct {
	Order    *model.Order
	Payments []*model.Payment
}

// QueryStatus 查询订单的支付状态与全部流水，供前端轮询。
//
// 只读本地数据，不调用渠道接口：轮询频率高，每次都打渠道会触发限流，
// 而本地状态在回调正常时就是准确的。需要强制与渠道对齐时用 SyncFromChannel。
func (s *PaymentService) QueryStatus(ctx context.Context, outTradeNo string) (*PaymentStatus, error) {
	if outTradeNo == "" {
		return nil, errcode.ErrInvalidParam.WithMsg("out_trade_no 不能为空")
	}

	order, err := s.orders.GetByOutTradeNo(ctx, outTradeNo)
	if err != nil {
		return nil, err
	}
	payments, err := s.payments.ListByOutTradeNo(ctx, outTradeNo)
	if err != nil {
		return nil, errcode.ErrInternal.WithCause(err)
	}
	return &PaymentStatus{Order: order, Payments: payments}, nil
}

// SyncFromChannel 主动向渠道查单并同步本地状态，是掉单兜底的核心手段。
//
// 回调可能因为网络抖动、防火墙拦截、服务重启而丢失。
// 生产环境应当由定时任务扫描「PAYING 且超过 N 分钟未终结」的订单批量调用本方法，
// 本次骨架没有引入调度器，改由查询接口的 sync 参数手动触发。
func (s *PaymentService) SyncFromChannel(ctx context.Context, outTradeNo string) (*NotifyOutcome, error) {
	order, err := s.orders.GetByOutTradeNo(ctx, outTradeNo)
	if err != nil {
		return nil, err
	}
	if order.Status.IsPaid() {
		return &NotifyOutcome{
			Order:      order,
			TradeState: channel.TradeStateSuccess,
			Idempotent: true,
		}, nil
	}

	gateway, err := s.registry.Get(order.Channel)
	if err != nil {
		return nil, err
	}

	payload, err := gateway.QueryOrder(ctx, outTradeNo)
	if err != nil {
		// 渠道侧查不到交易，说明从未成功下单（用户压根没点支付），
		// 属于正常情况，不应作为错误抛给调用方
		if errors.Is(err, errcode.ErrOrderNotFound) {
			return &NotifyOutcome{Order: order, TradeState: channel.TradeStateNotPay}, nil
		}
		return nil, err
	}
	// 复用回调的处理路径，保证查单与回调对同一笔交易的结论一致
	return s.HandlePayload(ctx, order.Channel, payload)
}
