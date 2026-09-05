// Package mock 提供支付渠道的假实现。
//
// 存在的意义不是「假装能支付」，而是让下单、调起、回调、查单这条完整链路
// 在没有微信支付商户号的情况下也能被端到端执行和自动化验证。
// 状态机流转、回调幂等、金额校验这些最容易出资金事故的逻辑，
// 恰恰需要一个完全可控、可重复触发的渠道才能测得充分。
//
// 与真实实现的唯一区别在于：本包不做任何签名、验签与加解密。
package mock

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"payment/internal/channel"
	"payment/internal/errcode"
	"payment/internal/pkg/idgen"
)

const (
	// maxNotifyBodySize 限制回调报文体大小。
	//
	// 即使是不验签的 mock 实现也必须限制：回调地址是公网可达的，
	// 不设上限的请求体读取等于给外部留了一个打爆内存的入口。
	maxNotifyBodySize = 1 << 20 // 1 MiB

	// prepayIDPrefix 是伪造的 prepay_id 前缀，便于在日志中一眼识别 mock 数据。
	prepayIDPrefix = "mock_prepay_"

	// mockPaySign 是伪造的签名值。
	//
	// 用固定字符串而不是随机值：前端拿它调起支付必然失败，这是预期行为。
	// 固定值能让「误把 mock 渠道带到生产」在联调时立刻暴露，
	// 而不是表现成一个时好时坏的随机故障。
	mockPaySign = "MOCK_PAY_SIGN_NOT_FOR_PRODUCTION"

	// defaultCurrency mock 渠道的默认币种。
	//
	// 本地定义而不引用 model.DefaultCurrency，是为了保持 mock 只依赖 channel 抽象层：
	// 渠道实现不应反向依赖领域模型，否则将来抽出独立渠道包时会被迫一起搬走。
	defaultCurrency = "CNY"
)

// notifyPayload 是 mock 渠道回调报文的 JSON 结构。
//
// 字段命名刻意与微信支付 APIv3 的回调明文保持一致，
// 这样将来接入真实渠道时，构造测试数据的代码可以直接复用。
type notifyPayload struct {
	OutTradeNo       string `json:"out_trade_no"`
	TransactionID    string `json:"transaction_id"`
	TradeState       string `json:"trade_state"`
	TradeStateDesc   string `json:"trade_state_desc"`
	AmountTotal      int64  `json:"amount_total"`
	AmountPayerTotal int64  `json:"amount_payer_total"`
	Currency         string `json:"currency"`
	SuccessTime      string `json:"success_time"`
	OpenID           string `json:"openid"`
}

// record 是 mock 渠道内部维护的一笔交易状态。
type record struct {
	prepayID         string
	tradeState       string
	tradeStateDesc   string
	transactionID    string
	amountTotal      int64
	amountPayerTotal int64
	openID           string
	successTime      time.Time
}

// Gateway 是 channel.Gateway 的假实现。
type Gateway struct {
	appID string

	mu      sync.RWMutex
	records map[string]*record

	// prepayErr 非 nil 时 Prepay 直接返回该错误，用于测试渠道调用失败的分支
	prepayErr error
}

// New 创建 mock 渠道，appID 会被填进前端调起参数中。
func New(appID string) *Gateway {
	if appID == "" {
		appID = "mock_appid"
	}
	return &Gateway{appID: appID, records: make(map[string]*record)}
}

var _ channel.Gateway = (*Gateway)(nil)

// SetPrepayError 设置 Prepay 的强制返回值，传 nil 恢复正常。
//
// 仅供测试使用：需要验证「渠道下单失败时订单状态不被推进」这条分支，
// 而真实渠道的失败无法按需触发。
func (g *Gateway) SetPrepayError(err error) {
	g.mu.Lock()
	defer g.mu.Unlock()

	g.prepayErr = err
}

// Code 返回渠道编码。
func (g *Gateway) Code() channel.Code { return channel.CodeMock }

// Prepay 伪造一次渠道下单。
//
// 校验规则与真实渠道对齐（金额为正、JSAPI 必须有 openid），
// 这样接入微信支付后暴露的问题不会与 mock 阶段的行为产生偏差。
func (g *Gateway) Prepay(_ context.Context, req channel.PrepayRequest) (*channel.PrepayResult, error) {
	if req.OutTradeNo == "" {
		return nil, errcode.ErrInvalidParam.WithMsg("mock 渠道: out_trade_no 不能为空")
	}
	if req.AmountTotal <= 0 {
		return nil, errcode.ErrOrderAmountInvalid.WithMsg(
			fmt.Sprintf("mock 渠道: 金额必须为正数，当前 %d 分", req.AmountTotal))
	}
	if req.TradeType == channel.TradeTypeJSAPI && req.PayerOpenID == "" {
		return nil, errcode.ErrOpenIDRequired.WithMsg("mock 渠道: JSAPI 交易必须提供 openid")
	}

	g.mu.Lock()
	defer g.mu.Unlock()

	if g.prepayErr != nil {
		return nil, errcode.ErrChannelCallFailed.WithCause(g.prepayErr)
	}
	// 已支付成功的交易不允许再次下单，与真实渠道行为一致
	if rec, ok := g.records[req.OutTradeNo]; ok && rec.tradeState == channel.TradeStateSuccess {
		return nil, errcode.ErrOrderAlreadyPaid.WithMsg("mock 渠道: 该交易已支付成功")
	}

	prepayID := prepayIDPrefix + idgen.RequestID()[:16]
	g.records[req.OutTradeNo] = &record{
		prepayID:    prepayID,
		tradeState:  channel.TradeStateNotPay,
		amountTotal: req.AmountTotal,
		openID:      req.PayerOpenID,
	}

	result := &channel.PrepayResult{
		Channel:    channel.CodeMock,
		OutTradeNo: req.OutTradeNo,
		PrepayID:   prepayID,
	}

	switch req.TradeType {
	case channel.TradeTypeNative:
		// 扫码支付返回二维码内容，前端据此生成二维码图片
		result.CodeURL = "weixin://wxpay/bizpayurl?mock=" + prepayID
	default:
		// JSAPI / 小程序返回调起参数。
		// timeStamp 必须是字符串而非数字，这是微信 JS-SDK 的硬性要求，
		// 传数字会导致 iOS 上签名校验失败。
		result.InvokeParams = &channel.InvokeParams{
			AppID:     g.appID,
			TimeStamp: fmt.Sprintf("%d", time.Now().Unix()),
			NonceStr:  idgen.NonceStr(),
			Package:   "prepay_id=" + prepayID,
			SignType:  "RSA",
			PaySign:   mockPaySign,
		}
	}
	return result, nil
}

// QueryOrder 返回 mock 渠道内部记录的交易状态。
func (g *Gateway) QueryOrder(_ context.Context, outTradeNo string) (*channel.NotifyPayload, error) {
	g.mu.RLock()
	defer g.mu.RUnlock()

	rec, ok := g.records[outTradeNo]
	if !ok {
		return nil, errcode.ErrOrderNotFound.WithMsg("mock 渠道: 交易不存在 " + outTradeNo)
	}
	return toPayload(outTradeNo, rec), nil
}

// CloseOrder 把交易置为已关闭。
//
// 真实渠道调用关单接口是为了防止「本系统已关单、用户随后又付款成功」，
// mock 实现同样维护这个状态，保证上层逻辑的行为一致。
func (g *Gateway) CloseOrder(_ context.Context, outTradeNo string) error {
	g.mu.Lock()
	defer g.mu.Unlock()

	rec, ok := g.records[outTradeNo]
	if !ok {
		// 渠道侧不存在该交易时关单视为成功：
		// 关单的目的是确保用户付不了款，交易本就不存在时目的已达成。
		return nil
	}
	if rec.tradeState == channel.TradeStateSuccess {
		return errcode.ErrOrderStatusInvalid.WithMsg("mock 渠道: 已支付的交易不能关闭")
	}
	rec.tradeState = channel.TradeStateClosed
	rec.tradeStateDesc = "订单已关闭"
	return nil
}

// ParseNotify 从请求体解析回调报文。
//
// 这里是与真实实现差别最大的地方：不验签、不解密，直接读 JSON。
// 接入微信支付后必须替换为 SDK 的 notify.Handler，
// 由它完成平台证书（或微信支付公钥）验签与 APIv3 密钥解密，
// 任何一步失败都要拒绝处理——未经验签的回调等同于允许任何人把订单改成已支付。
func (g *Gateway) ParseNotify(_ context.Context, r *http.Request) (*channel.NotifyPayload, error) {
	if r == nil || r.Body == nil {
		return nil, errcode.ErrBadRequest.WithMsg("mock 渠道: 回调请求体为空")
	}

	raw, err := io.ReadAll(io.LimitReader(r.Body, maxNotifyBodySize))
	if err != nil {
		return nil, errcode.ErrChannelCallFailed.WithCause(err)
	}
	// 读完即关闭，避免上层复用该 request 时句柄泄漏
	_ = r.Body.Close()

	var p notifyPayload
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, errcode.ErrBadRequest.WithCause(err)
	}
	if p.OutTradeNo == "" {
		return nil, errcode.ErrInvalidParam.WithMsg("mock 渠道: 回调报文缺少 out_trade_no")
	}
	if p.TradeState == "" {
		return nil, errcode.ErrInvalidParam.WithMsg("mock 渠道: 回调报文缺少 trade_state")
	}

	g.mu.Lock()
	defer g.mu.Unlock()

	rec, ok := g.records[p.OutTradeNo]
	if !ok {
		// 渠道侧没有下单记录时也把报文完整交给上层：
		// 真实场景中这可能意味着本地下单记录写入失败但渠道已扣款，
		// 此时吞掉报文会直接造成掉单，必须由上层决定如何处理。
		return buildPayload(p, nil, raw), nil
	}

	applyToRecord(p, rec)
	return buildPayload(p, rec, raw), nil
}

// applyToRecord 把交易通知落到渠道侧的内部记录上，供后续 QueryOrder 使用。
//
// 金额与下单记录不符时不更新。这类报文在真实渠道中不可能出现，
// 只可能是伪造或串单；若仍然把记录置为已支付，随后的主动查单
// 会用记录里的原始金额再走一遍处理路径，被拒绝的回调就绕过金额校验重新生效了。
func applyToRecord(p notifyPayload, rec *record) {
	switch p.TradeState {
	case channel.TradeStateSuccess:
		// 报文未声明金额时视为与下单记录一致（buildPayload 会按记录补齐）
		if p.AmountTotal != 0 && p.AmountTotal != rec.amountTotal {
			return
		}
		if rec.successTime.IsZero() {
			// 只有首次成功回调才记录时间，重复回调保留首次时间，
			// 与真实渠道「以首次支付成功为准」的行为一致
			if t := parseTime(p.SuccessTime); !t.IsZero() {
				rec.successTime = t
			} else {
				rec.successTime = time.Now()
			}
		}
		rec.tradeState = channel.TradeStateSuccess
		if p.TransactionID != "" {
			rec.transactionID = p.TransactionID
		}
		rec.amountPayerTotal = payerTotal(p.AmountPayerTotal, rec.amountTotal)
		if p.OpenID != "" {
			rec.openID = p.OpenID
		}
		if p.TradeStateDesc != "" {
			rec.tradeStateDesc = p.TradeStateDesc
		}

	case channel.TradeStateClosed, channel.TradeStatePayError:
		// 已成功的交易不允许被后续回调改成失败：
		// 支付成功是既成事实，任何回退都会导致用户已付款而订单未发货
		if rec.tradeState != channel.TradeStateSuccess {
			rec.tradeState = p.TradeState
			rec.tradeStateDesc = p.TradeStateDesc
		}
	}
}

// buildPayload 以报文为准构造回调结构，rec 只用于补齐报文里没写的字段。
//
// 必须报文优先而不是直接返回渠道内部记录：service 层要靠这个结构做金额校验，
// 用记录里的原始金额覆盖报文值，会把「回调金额与订单金额不一致」这条
// 资金防线整个绕过——伪造金额的回调将永远校验通过。
//
// rec 为 nil 表示渠道侧没有该交易的下单记录，此时无值可补，全部按报文上报。
func buildPayload(p notifyPayload, rec *record, raw []byte) *channel.NotifyPayload {
	transactionID := p.TransactionID
	openID := p.OpenID
	amountTotal := p.AmountTotal
	successTime := parseTime(p.SuccessTime)

	if rec != nil {
		if transactionID == "" {
			transactionID = rec.transactionID
		}
		if openID == "" {
			openID = rec.openID
		}
		if amountTotal == 0 {
			amountTotal = rec.amountTotal
		}
		if successTime.IsZero() {
			successTime = rec.successTime
		}
	}

	return &channel.NotifyPayload{
		OutTradeNo:       p.OutTradeNo,
		TransactionID:    transactionID,
		TradeState:       p.TradeState,
		TradeStateDesc:   p.TradeStateDesc,
		AmountTotal:      amountTotal,
		AmountPayerTotal: payerTotal(p.AmountPayerTotal, amountTotal),
		Currency:         orDefault(p.Currency, defaultCurrency),
		SuccessTime:      successTime,
		OpenID:           openID,
		Raw:              raw,
	}
}

// Refund 未实现。
//
// 骨架阶段不暴露退款接口，但 Gateway 接口要求方法齐全，
// 因此显式返回「未实现」而不是伪造成功——后者会让调用方误以为退款已完成。
func (g *Gateway) Refund(_ context.Context, _ channel.RefundRequest) (*channel.RefundResult, error) {
	return nil, errcode.ErrChannelNotImplemented.WithMsg("mock 渠道不支持退款")
}

// toPayload 把内部记录转换为渠道无关的回调结构。调用方需持有锁。
func toPayload(outTradeNo string, rec *record) *channel.NotifyPayload {
	return &channel.NotifyPayload{
		OutTradeNo:       outTradeNo,
		TransactionID:    rec.transactionID,
		TradeState:       rec.tradeState,
		TradeStateDesc:   rec.tradeStateDesc,
		AmountTotal:      rec.amountTotal,
		AmountPayerTotal: payerTotal(rec.amountPayerTotal, rec.amountTotal),
		Currency:         defaultCurrency,
		SuccessTime:      rec.successTime,
		OpenID:           rec.openID,
	}
}

// payerTotal 用户实付金额缺省时回退到订单总额。
//
// 未使用优惠券时两者相等，真实渠道也会同时下发这两个字段。
func payerTotal(payerTotal, total int64) int64 {
	if payerTotal > 0 {
		return payerTotal
	}
	return total
}

func parseTime(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}
	}
	return t
}

func orDefault(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}
