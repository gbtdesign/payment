package model

import (
	"fmt"
	"time"

	"payment/internal/channel"
)

// PaymentStatus 支付流水状态。
type PaymentStatus string

const (
	// PaymentStatusInit 流水已创建，尚未调起渠道下单。
	PaymentStatusInit PaymentStatus = "INIT"
	// PaymentStatusPrepaid 渠道下单成功，已拿到 prepay_id，等待用户付款。
	PaymentStatusPrepaid PaymentStatus = "PREPAID"
	// PaymentStatusSuccess 支付成功，终态。
	PaymentStatusSuccess PaymentStatus = "SUCCESS"
	// PaymentStatusFailed 支付失败，终态。
	PaymentStatusFailed PaymentStatus = "FAILED"
	// PaymentStatusClosed 流水已关闭（订单关单或超时），终态。
	PaymentStatusClosed PaymentStatus = "CLOSED"
)

// paymentTransitions 定义流水状态的合法后继。
var paymentTransitions = map[PaymentStatus][]PaymentStatus{
	PaymentStatusInit:    {PaymentStatusPrepaid, PaymentStatusFailed, PaymentStatusClosed},
	PaymentStatusPrepaid: {PaymentStatusSuccess, PaymentStatusFailed, PaymentStatusClosed},
	PaymentStatusSuccess: {},
	PaymentStatusFailed:  {},
	PaymentStatusClosed:  {},
}

// CanTransitionTo 判断流水能否流转到目标状态。
func (s PaymentStatus) CanTransitionTo(next PaymentStatus) bool {
	for _, allowed := range paymentTransitions[s] {
		if allowed == next {
			return true
		}
	}
	return false
}

// IsTerminal 是否为终态。
func (s PaymentStatus) IsTerminal() bool { return len(paymentTransitions[s]) == 0 }

// IsSuccess 是否支付成功。
func (s PaymentStatus) IsSuccess() bool { return s == PaymentStatusSuccess }

// String 实现 fmt.Stringer。
func (s PaymentStatus) String() string { return string(s) }

// Payment 单次支付尝试的流水记录。
//
// Payment 与 Order 是一对多关系：一个订单可能因为用户取消、网络失败、
// 重复点击等原因产生多次支付尝试，每次尝试都是一条独立流水。
// 保留全部流水而不是覆盖更新，是对账与故障排查的前提——
// 「用户说付了钱但订单没变已支付」这类问题，只能靠流水还原当时发生了什么。
type Payment struct {
	// PaymentNo 流水号，业务主键
	PaymentNo string
	// OutTradeNo 所属订单号
	OutTradeNo string
	// Channel 支付渠道
	Channel channel.Code
	// TradeType 交易类型，JSAPI / NATIVE / H5 / APP
	TradeType channel.TradeType

	// PrepayID 渠道返回的预支付交易会话标识
	PrepayID string
	// TransactionID 渠道侧交易号，支付成功后回填
	TransactionID string
	// AmountTotal 本次支付金额（分），必须与订单金额一致
	AmountTotal int64

	// Status 流水状态，只能通过 Mark* 方法变更
	Status PaymentStatus
	// TradeState 渠道返回的原始交易状态，如微信的 SUCCESS / NOTPAY / CLOSED
	TradeState string
	// TradeStateDesc 渠道对交易状态的描述
	TradeStateDesc string
	// OpenID 实际付款用户的 openid，回调中回填
	OpenID string

	SuccessTime time.Time
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// NewPayment 创建流水，初始状态为 INIT。
func NewPayment(paymentNo, outTradeNo string, amountTotal int64, ch channel.Code, tradeType channel.TradeType) *Payment {
	now := time.Now()
	return &Payment{
		PaymentNo:   paymentNo,
		OutTradeNo:  outTradeNo,
		Channel:     ch,
		TradeType:   tradeType,
		AmountTotal: amountTotal,
		Status:      PaymentStatusInit,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
}

// MarkPrepaid 标记渠道下单成功。
func (p *Payment) MarkPrepaid(prepayID string, now time.Time) error {
	return p.transit(PaymentStatusPrepaid, now, func() { p.PrepayID = prepayID })
}

// MarkSuccess 标记支付成功。
//
// tradeState / desc / openID 均来自渠道回调或查单结果，原样保留以便对账。
func (p *Payment) MarkSuccess(transactionID, tradeState, desc, openID string, successTime, now time.Time) error {
	return p.transit(PaymentStatusSuccess, now, func() {
		p.TransactionID = transactionID
		p.TradeState = tradeState
		p.TradeStateDesc = desc
		if openID != "" {
			p.OpenID = openID
		}
		p.SuccessTime = successTime
	})
}

// MarkFailed 标记支付失败。
func (p *Payment) MarkFailed(tradeState, desc string, now time.Time) error {
	return p.transit(PaymentStatusFailed, now, func() {
		p.TradeState = tradeState
		p.TradeStateDesc = desc
	})
}

// MarkClosed 标记流水关闭。
func (p *Payment) MarkClosed(desc string, now time.Time) error {
	return p.transit(PaymentStatusClosed, now, func() { p.TradeStateDesc = desc })
}

func (p *Payment) transit(next PaymentStatus, now time.Time, mutate func()) error {
	if !p.Status.CanTransitionTo(next) {
		return fmt.Errorf("%w: %s -> %s (payment_no=%s)",
			ErrInvalidTransition, p.Status, next, p.PaymentNo)
	}
	if mutate != nil {
		mutate()
	}
	p.Status = next
	p.UpdatedAt = now
	return nil
}

// Clone 返回流水副本，理由同 Order.Clone。
func (p *Payment) Clone() *Payment {
	if p == nil {
		return nil
	}
	cp := *p
	return &cp
}
