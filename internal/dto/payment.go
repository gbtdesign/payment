package dto

import (
	"payment/internal/channel"
	"payment/internal/model"
	"payment/internal/pkg/money"
)

// PrepayRequest 发起支付请求。
type PrepayRequest struct {
	// OutTradeNo 商户订单号
	OutTradeNo string `json:"out_trade_no" binding:"required,max=32"`

	// Channel 支付渠道，留空则用订单创建时确定的渠道
	Channel string `json:"channel" binding:"omitempty,oneof=mock wechatpay"`

	// TradeType 交易类型，留空按 JSAPI 处理（本次骨架唯一支持的场景）
	TradeType string `json:"trade_type" binding:"omitempty,oneof=JSAPI NATIVE H5 APP"`

	// OpenID 支付者 openid。
	//
	// 留空时回退到订单上已保存的 openid。JSAPI 交易二者必须至少有一个，
	// 具体的必填校验在 service 层做，因为只有那里知道最终的 trade_type。
	OpenID string `json:"openid" binding:"omitempty,max=128"`
}

// PrepayResponse 发起支付响应。
//
// 前端拿到 invoke_params 后可直接传给 wx.requestPayment。
type PrepayResponse struct {
	OutTradeNo string `json:"out_trade_no"`
	PaymentNo  string `json:"payment_no"`
	Channel    string `json:"channel"`
	TradeType  string `json:"trade_type"`
	PrepayID   string `json:"prepay_id,omitempty"`

	// CodeURL 扫码支付的二维码内容，仅 NATIVE 交易返回
	CodeURL string `json:"code_url,omitempty"`

	// InvokeParams 前端调起支付所需参数，JSAPI / 小程序交易返回
	InvokeParams *channel.InvokeParams `json:"invoke_params,omitempty"`
}

// FromPrepayResult 把渠道下单结果转换为响应结构。
func FromPrepayResult(paymentNo string, tradeType channel.TradeType, res *channel.PrepayResult) *PrepayResponse {
	if res == nil {
		return nil
	}
	return &PrepayResponse{
		OutTradeNo:   res.OutTradeNo,
		PaymentNo:    paymentNo,
		Channel:      res.Channel.String(),
		TradeType:    tradeType.String(),
		PrepayID:     res.PrepayID,
		CodeURL:      res.CodeURL,
		InvokeParams: res.InvokeParams,
	}
}

// PaymentItem 单条支付流水的对外表示。
type PaymentItem struct {
	PaymentNo     string `json:"payment_no"`
	Channel       string `json:"channel"`
	TradeType     string `json:"trade_type"`
	Status        string `json:"status"`
	PrepayID      string `json:"prepay_id,omitempty"`
	TransactionID string `json:"transaction_id,omitempty"`

	TradeState     string `json:"trade_state,omitempty"`
	TradeStateDesc string `json:"trade_state_desc,omitempty"`

	Amount    string `json:"amount"`
	AmountFen int64  `json:"amount_fen"`

	SuccessTime string `json:"success_time,omitempty"`
	CreatedAt   string `json:"created_at"`
}

// FromPayment 把支付流水领域模型转换为响应结构。
func FromPayment(p *model.Payment) *PaymentItem {
	if p == nil {
		return nil
	}
	return &PaymentItem{
		PaymentNo:      p.PaymentNo,
		Channel:        p.Channel.String(),
		TradeType:      p.TradeType.String(),
		Status:         p.Status.String(),
		PrepayID:       p.PrepayID,
		TransactionID:  p.TransactionID,
		TradeState:     p.TradeState,
		TradeStateDesc: p.TradeStateDesc,
		Amount:         money.FenToYuan(p.AmountTotal),
		AmountFen:      p.AmountTotal,
		SuccessTime:    formatTime(p.SuccessTime),
		CreatedAt:      formatTime(p.CreatedAt),
	}
}

// PaymentStatusResponse 支付状态查询响应，供前端轮询使用。
//
// 同时给出订单状态与全部支付流水：
// 前端只需要看 paid 字段就能决定是否停止轮询，
// 而流水明细在用户反馈「付了钱没到账」时能直接提供给客服定位问题。
type PaymentStatusResponse struct {
	OutTradeNo  string         `json:"out_trade_no"`
	OrderStatus string         `json:"order_status"`
	Paid        bool           `json:"paid"`
	Amount      string         `json:"amount"`
	AmountFen   int64          `json:"amount_fen"`
	Payments    []*PaymentItem `json:"payments,omitempty"`
}

// MockPaySuccessRequest 模拟支付成功回调的请求。
//
// 仅在非 release 模式下可用，用于在没有微信支付商户号的情况下
// 端到端验证状态机、回调幂等与金额校验逻辑。
type MockPaySuccessRequest struct {
	OutTradeNo string `json:"out_trade_no" binding:"required,max=32"`

	// TransactionID 模拟的渠道交易号，留空则自动生成
	TransactionID string `json:"transaction_id" binding:"omitempty,max=64"`

	// AmountFen 模拟回调中的金额（分）。
	//
	// 留 0 表示与订单金额一致；显式传入不同值可用于验证金额校验是否生效。
	AmountFen int64 `json:"amount_fen" binding:"omitempty"`

	// OpenID 模拟付款用户的 openid
	OpenID string `json:"openid" binding:"omitempty,max=128"`
}

// MockNotifyResponse 模拟回调接口的应答，回显本次模拟的结果便于断言。
type MockNotifyResponse struct {
	OutTradeNo    string `json:"out_trade_no"`
	OrderStatus   string `json:"order_status"`
	Paid          bool   `json:"paid"`
	TransactionID string `json:"transaction_id,omitempty"`
	// Idempotent 为 true 表示本次回调命中幂等分支，订单状态未被重复推进
	Idempotent bool `json:"idempotent"`
}
