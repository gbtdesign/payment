// Package channel 定义支付渠道抽象层。
//
// 这是整个服务的「接缝」：所有与具体支付渠道相关的差异都被收敛到
// Gateway 接口之后。service 层只认这个接口，不认微信支付、支付宝或任何 SDK 类型。
//
// 这样设计带来两个直接好处：
//  1. 接入新渠道只需新增一个实现并在 registry 注册，service / handler 零改动
//  2. 渠道不可用时可以用 mock 实现顶替，本地开发与自动化测试不依赖外部服务
//
// 本包的所有 DTO 都是渠道无关的：字段命名参考微信支付 APIv3，
// 但语义上对任何渠道都成立，金额单位统一为「分」。
package channel

import (
	"context"
	"net/http"
	"time"
)

// Code 渠道编码。
type Code string

const (
	// CodeMock 内置的假渠道，用于跑通链路与测试。
	CodeMock Code = "mock"
	// CodeWechatPay 微信支付。骨架阶段仅有占位包，尚无实现。
	CodeWechatPay Code = "wechatpay"
)

// Valid 判断渠道编码是否已知。
func (c Code) Valid() bool {
	switch c {
	case CodeMock, CodeWechatPay:
		return true
	default:
		return false
	}
}

// String 实现 fmt.Stringer。
func (c Code) String() string { return string(c) }

// TradeType 交易类型。
//
// 只有 JSAPI 是本次骨架支持的目标场景（微信内网页 / 小程序支付，需 openid）。
// 其余取值先声明出来，是为了让 Gateway 的调用方在扩展时不必改动类型定义。
type TradeType string

const (
	// TradeTypeJSAPI 公众号 / 小程序支付，必须提供 PayerOpenID。
	TradeTypeJSAPI TradeType = "JSAPI"
	// TradeTypeNative 扫码支付，渠道返回 code_url。
	TradeTypeNative TradeType = "NATIVE"
	// TradeTypeH5 手机浏览器（非微信内）支付。
	TradeTypeH5 TradeType = "H5"
	// TradeTypeApp 原生 APP 支付。
	TradeTypeApp TradeType = "APP"
)

// String 实现 fmt.Stringer。
func (t TradeType) String() string { return string(t) }

// 渠道无关的交易状态。取值与微信支付 trade_state 对齐，
// 其他渠道接入时由各自的 Gateway 实现负责映射到这套枚举。
const (
	// TradeStateSuccess 支付成功。
	TradeStateSuccess = "SUCCESS"
	// TradeStateNotPay 未支付，用户尚未完成付款。
	TradeStateNotPay = "NOTPAY"
	// TradeStateClosed 已关闭，订单超时未支付或已关单。
	TradeStateClosed = "CLOSED"
	// TradeStateUserPaying 支付中，用户已输入密码但结果未定，必须继续轮询。
	TradeStateUserPaying = "USERPAYING"
	// TradeStateRefund 转入退款。
	TradeStateRefund = "REFUND"
	// TradeStatePayError 支付失败。
	TradeStatePayError = "PAYERROR"
)

// PrepayRequest 统一下单请求。金额单位为「分」。
type PrepayRequest struct {
	// OutTradeNo 商户订单号
	OutTradeNo string
	// Description 商品描述，会展示在用户的支付凭证上
	Description string
	// AmountTotal 订单总金额（分）
	AmountTotal int64
	// Currency 币种，留空按 CNY 处理
	Currency string
	// Attach 附加数据，渠道回调时原样返回
	Attach string
	// NotifyURL 支付结果回调地址，必须公网可达且为 HTTPS
	NotifyURL string
	// TradeType 交易类型
	TradeType TradeType
	// PayerOpenID 支付者 openid，JSAPI 交易必填
	PayerOpenID string
	// ClientIP 客户端 IP
	ClientIP string
	// TimeExpire 订单过期时间，零值表示不设过期
	TimeExpire time.Time
}

// InvokeParams 前端调起支付所需参数。
//
// 对应微信小程序的 wx.requestPayment 与微信内 H5 的 WeixinJSBridge.invoke。
// JSON tag 采用微信文档的驼峰命名，前端可直接透传使用。
type InvokeParams struct {
	AppID     string `json:"appId"`
	TimeStamp string `json:"timeStamp"`
	NonceStr  string `json:"nonceStr"`
	Package   string `json:"package"`
	SignType  string `json:"signType"`
	PaySign   string `json:"paySign"`
}

// PrepayResult 统一下单结果。
type PrepayResult struct {
	// Channel 实际处理本次下单的渠道
	Channel Code
	// OutTradeNo 回显商户订单号，便于调用方核对
	OutTradeNo string
	// PrepayID 渠道返回的预支付交易会话标识
	PrepayID string
	// CodeURL 扫码支付的二维码内容，仅 NATIVE 交易有值
	CodeURL string
	// InvokeParams 前端调起支付所需参数，JSAPI / 小程序交易有值
	InvokeParams *InvokeParams
}

// NotifyPayload 回调解析后的统一结构，也复用于主动查单的返回。
//
// 回调与查单共用一个结构是刻意的：二者的业务语义完全相同
// （都是「渠道告知本笔交易的当前状态」），共用结构可以让 service 层
// 用同一套逻辑处理，避免「回调走一条路径、查单走另一条」造成的行为不一致。
type NotifyPayload struct {
	OutTradeNo     string
	TransactionID  string
	TradeState     string
	TradeStateDesc string

	// AmountTotal 订单总金额（分）
	AmountTotal int64
	// AmountPayerTotal 用户实际支付金额（分），使用优惠券时会小于 AmountTotal
	AmountPayerTotal int64
	Currency         string

	SuccessTime time.Time
	OpenID      string

	// Raw 渠道原始报文，仅用于排查问题落日志，不参与业务判断
	Raw []byte
}

// IsSuccess 交易是否已成功。
//
// 判断依据是归一化后的 TradeState 而非 HTTP 状态码：
// 微信支付的回调即使交易失败也会返回 200，只有报文体里的 trade_state 才可信。
func (p *NotifyPayload) IsSuccess() bool {
	return p != nil && p.TradeState == TradeStateSuccess
}

// RefundRequest 退款请求。金额单位为「分」。
type RefundRequest struct {
	// OutTradeNo 原订单号
	OutTradeNo string
	// OutRefundNo 商户退款单号，同一订单下多次退款必须各不相同
	OutRefundNo string
	// Refund 本次退款金额（分）
	Refund int64
	// Total 原订单总金额（分）
	Total int64
	// Reason 退款原因
	Reason string
	// NotifyURL 退款结果回调地址
	NotifyURL string
}

// RefundResult 退款结果。
type RefundResult struct {
	// RefundID 渠道侧退款单号
	RefundID string
	// OutRefundNo 回显商户退款单号
	OutRefundNo string
	// Status 退款状态，如 PROCESSING / SUCCESS / ABNORMAL
	Status string
}

// Gateway 支付渠道网关。
//
// 接入微信支付只需实现本接口并在 registry 注册，上层代码无需任何改动。
// 所有方法都必须尊重 ctx 的取消与超时：渠道 HTTP 调用若不设超时，
// 一次网络抖动就可能把服务的连接与 goroutine 全部耗尽。
type Gateway interface {
	// Code 返回渠道编码，registry 据此建立索引
	Code() Code

	// Prepay 统一下单，返回前端调起支付所需的参数
	Prepay(ctx context.Context, req PrepayRequest) (*PrepayResult, error)

	// QueryOrder 按商户订单号主动查单。
	//
	// 这是掉单兜底的核心手段：回调可能因为网络、防火墙、服务重启而丢失，
	// 只有主动查单能确认交易的真实状态。
	QueryOrder(ctx context.Context, outTradeNo string) (*NotifyPayload, error)

	// CloseOrder 关闭渠道侧订单，避免用户在本系统已关单后仍然付款成功
	CloseOrder(ctx context.Context, outTradeNo string) error

	// ParseNotify 解析并校验回调请求。
	//
	// 真实渠道实现必须在这里完成验签与报文解密，
	// 校验失败要返回错误而不是把不可信的报文交给业务层。
	ParseNotify(ctx context.Context, r *http.Request) (*NotifyPayload, error)

	// Refund 申请退款
	Refund(ctx context.Context, req RefundRequest) (*RefundResult, error)
}
