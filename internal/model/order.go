// Package model 定义领域模型与状态机。
//
// 本包是整个服务的语义中心：所有金额一律以 int64 存「分」，
// 所有状态流转都必须通过本包提供的方法完成，不允许外部直接赋值 Status 字段。
// 这样做的目的是让「非法流转」在编译期与运行期都无法悄悄发生——
// 支付订单从 PAID 回退到 PAYING 这类错误，一旦发生就是资金对账事故。
package model

import (
	"errors"
	"fmt"
	"time"

	"payment/internal/channel"
)

// DefaultCurrency 是默认币种。微信支付境内商户固定为 CNY。
const DefaultCurrency = "CNY"

// OrderStatus 订单状态。
type OrderStatus string

const (
	// OrderStatusCreated 订单已创建，尚未调起任何支付。
	OrderStatusCreated OrderStatus = "CREATED"
	// OrderStatusPaying 已调起支付，等待用户完成付款。
	OrderStatusPaying OrderStatus = "PAYING"
	// OrderStatusPaid 支付成功，终态之一（仍可流向 REFUNDED）。
	OrderStatusPaid OrderStatus = "PAID"
	// OrderStatusClosed 订单已关闭（超时未支付或主动关单），终态。
	OrderStatusClosed OrderStatus = "CLOSED"
	// OrderStatusRefunded 已退款，终态。
	OrderStatusRefunded OrderStatus = "REFUNDED"
)

// ErrInvalidTransition 非法的状态流转。
var ErrInvalidTransition = errors.New("model: 非法的订单状态流转")

// orderTransitions 定义订单状态的合法后继。
//
// 显式列举而不是写 if-else，是为了让状态机可被单测穷举校验，
// 也便于 review 时一眼看出「哪些流转被禁止」。
var orderTransitions = map[OrderStatus][]OrderStatus{
	OrderStatusCreated: {OrderStatusPaying, OrderStatusClosed},
	// PAYING -> PAYING 是允许的：用户第一次没付，重新调起支付属于正常路径
	OrderStatusPaying:   {OrderStatusPaying, OrderStatusPaid, OrderStatusClosed},
	OrderStatusPaid:     {OrderStatusRefunded},
	OrderStatusClosed:   {},
	OrderStatusRefunded: {},
}

// CanTransitionTo 判断能否从当前状态流转到目标状态。
func (s OrderStatus) CanTransitionTo(next OrderStatus) bool {
	for _, allowed := range orderTransitions[s] {
		if allowed == next {
			return true
		}
	}
	return false
}

// IsTerminal 是否为终态（不可再流转，REFUNDED 除外，它由 PAID 流向）。
func (s OrderStatus) IsTerminal() bool {
	return len(orderTransitions[s]) == 0
}

// IsPaid 是否已支付成功。
func (s OrderStatus) IsPaid() bool { return s == OrderStatusPaid }

// String 实现 fmt.Stringer。
func (s OrderStatus) String() string { return string(s) }

// Order 支付订单。
//
// AmountTotal 单位为「分」。禁止在任何地方把它转成 float64 参与运算。
type Order struct {
	// OutTradeNo 商户订单号，业务主键，最长 32 字符
	OutTradeNo string
	// Subject 商品标题，会作为描述传给支付渠道
	Subject string
	// AmountTotal 订单总金额（分）
	AmountTotal int64
	// Currency 币种，默认 CNY
	Currency string
	// Channel 支付渠道编码
	Channel channel.Code
	// Status 订单状态，只能通过下方的 Mark* 方法变更
	Status OrderStatus
	// OpenID 支付者 openid，JSAPI 交易必填
	OpenID string
	// ClientIP 下单客户端 IP，用于风控与渠道侧要求
	ClientIP string
	// Attach 附加数据，渠道回调时原样返回，可用于携带业务上下文
	Attach string
	// TransactionID 渠道侧交易号，支付成功后回填
	TransactionID string

	CreatedAt time.Time
	UpdatedAt time.Time
	PaidAt    time.Time
	ClosedAt  time.Time
}

// NewOrder 创建订单，初始状态为 CREATED。
//
// 统一由构造函数创建而不是让调用方拼字面量，
// 是为了保证 Currency、Status、时间戳这几个字段的初值一定正确。
func NewOrder(outTradeNo, subject string, amountTotal int64, ch channel.Code) *Order {
	now := time.Now()
	return &Order{
		OutTradeNo:  outTradeNo,
		Subject:     subject,
		AmountTotal: amountTotal,
		Currency:    DefaultCurrency,
		Channel:     ch,
		Status:      OrderStatusCreated,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
}

// CanPay 是否允许发起支付。
func (o *Order) CanPay() bool {
	return o.Status == OrderStatusCreated || o.Status == OrderStatusPaying
}

// MarkPaying 标记为支付中，用于成功调起渠道下单之后。
func (o *Order) MarkPaying(now time.Time) error {
	return o.transit(OrderStatusPaying, now, nil)
}

// MarkPaid 标记为已支付。
//
// transactionID 允许为空：mock 渠道不产生真实交易号。
// 真实渠道接入后应传入渠道返回的 transaction_id，用于后续对账与退款。
func (o *Order) MarkPaid(transactionID string, now time.Time) error {
	return o.transit(OrderStatusPaid, now, func() {
		o.PaidAt = now
		if transactionID != "" {
			o.TransactionID = transactionID
		}
	})
}

// MarkClosed 标记为已关闭（超时未支付或主动关单）。
func (o *Order) MarkClosed(now time.Time) error {
	return o.transit(OrderStatusClosed, now, func() { o.ClosedAt = now })
}

// MarkRefunded 标记为已退款。只允许从 PAID 流转过来。
func (o *Order) MarkRefunded(now time.Time) error {
	return o.transit(OrderStatusRefunded, now, nil)
}

// transit 是所有状态变更的唯一入口。
//
// 校验通过后才执行 mutate，保证「非法流转不产生任何副作用」——
// 如果先改字段再校验，失败时订单会处于半更新的脏状态。
func (o *Order) transit(next OrderStatus, now time.Time, mutate func()) error {
	if !o.Status.CanTransitionTo(next) {
		return fmt.Errorf("%w: %s -> %s (out_trade_no=%s)",
			ErrInvalidTransition, o.Status, next, o.OutTradeNo)
	}
	if mutate != nil {
		mutate()
	}
	o.Status = next
	o.UpdatedAt = now
	return nil
}

// Clone 返回订单副本。
//
// 仓储层对外返回副本而不是内部指针，否则调用方修改字段会绕过状态机校验，
// 直接破坏内存仓储中保存的数据。这是内存实现的必备防护，
// 换成数据库实现后由 ORM 的扫描语义天然保证。
func (o *Order) Clone() *Order {
	if o == nil {
		return nil
	}
	cp := *o
	return &cp
}
