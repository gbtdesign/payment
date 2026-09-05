// Package dto 定义 HTTP 接口的请求与响应结构。
//
// 与 model 严格分离的原因：
//   - model 是领域模型，字段语义由业务决定，金额用 int64 存「分」
//   - dto 是对外契约，字段命名与格式由 API 兼容性决定，金额用字符串存「元」
//
// 二者混用会导致「为了改 JSON 字段名而动到领域模型」这类错误的耦合。
// 所有转换集中在本包的 From* 函数中，方便统一维护。
package dto

import (
	"time"

	"payment/internal/model"
	"payment/internal/pkg/money"
)

// timeLayout 是对外输出的时间格式。
//
// 用 RFC3339 而不是 "2006-01-02 15:04:05"：前者带时区信息，
// 跨时区的客户端与对账系统不会把时间理解错。
const timeLayout = time.RFC3339

// CreateOrderRequest 创建订单请求。
type CreateOrderRequest struct {
	// Subject 商品标题，会展示在用户的支付凭证上
	Subject string `json:"subject" binding:"required,min=1,max=128"`

	// Amount 订单金额，单位「元」，字符串形式如 "19.99"。
	//
	// 刻意用 string 而不是 float64：JSON 数字在客户端与服务端都可能
	// 经过浮点解析，19.99 一旦变成 float64 就再也无法精确还原。
	Amount string `json:"amount" binding:"required"`

	// Channel 支付渠道，留空则用配置里的 payment.default_channel
	Channel string `json:"channel" binding:"omitempty,oneof=mock wechatpay"`

	// OpenID 支付者 openid，JSAPI 交易必填
	OpenID string `json:"openid" binding:"omitempty,max=128"`

	// Attach 附加数据，渠道回调时原样返回，可用于携带业务侧的上下文标识
	Attach string `json:"attach" binding:"omitempty,max=512"`
}

// AmountFen 把 Amount 转换为「分」。
//
// 转换失败返回的错误已经带上原始输入，便于直接回给客户端定位问题。
func (r CreateOrderRequest) AmountFen() (int64, error) {
	return money.YuanToFen(r.Amount)
}

// OrderResponse 订单响应。
type OrderResponse struct {
	OutTradeNo string `json:"out_trade_no"`
	Subject    string `json:"subject"`

	// Amount 金额（元，字符串）与 AmountFen 金额（分，整数）同时给出。
	// 前端展示用前者，业务计算用后者，避免调用方自己做元分转换时引入浮点误差。
	Amount    string `json:"amount"`
	AmountFen int64  `json:"amount_fen"`
	Currency  string `json:"currency"`

	Channel string `json:"channel"`
	Status  string `json:"status"`

	OpenID        string `json:"openid,omitempty"`
	TransactionID string `json:"transaction_id,omitempty"`
	Attach        string `json:"attach,omitempty"`

	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
	PaidAt    string `json:"paid_at,omitempty"`
	ClosedAt  string `json:"closed_at,omitempty"`
}

// FromOrder 把领域模型转换为响应结构。
func FromOrder(o *model.Order) *OrderResponse {
	if o == nil {
		return nil
	}
	return &OrderResponse{
		OutTradeNo:    o.OutTradeNo,
		Subject:       o.Subject,
		Amount:        money.FenToYuan(o.AmountTotal),
		AmountFen:     o.AmountTotal,
		Currency:      o.Currency,
		Channel:       o.Channel.String(),
		Status:        o.Status.String(),
		OpenID:        o.OpenID,
		TransactionID: o.TransactionID,
		Attach:        o.Attach,
		CreatedAt:     formatTime(o.CreatedAt),
		UpdatedAt:     formatTime(o.UpdatedAt),
		PaidAt:        formatTime(o.PaidAt),
		ClosedAt:      formatTime(o.ClosedAt),
	}
}

// formatTime 格式化时间，零值返回空串以便配合 omitempty 从 JSON 中省略。
//
// 支付未完成时 PaidAt 是零值，直接输出 "0001-01-01T00:00:00Z"
// 会让客户端误以为存在一个真实的时间点。
func formatTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Format(timeLayout)
}
