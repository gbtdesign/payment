package handler

import (
	"bytes"
	"encoding/json"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"payment/internal/channel"
	"payment/internal/dto"
	"payment/internal/errcode"
	"payment/internal/model"
	"payment/internal/pkg/idgen"
	"payment/internal/pkg/response"
	"payment/internal/service"
)

// mockCallbackPath 是构造内部请求时使用的占位路径。
//
// 它不需要被路由注册：请求不会真的经过网络栈，
// 只是作为 service.HandleNotify 所需的 *http.Request 载体。
const mockCallbackPath = "/internal/mock/callback"

// mockTransactionPrefix 是模拟交易号的前缀，便于在数据中一眼识别。
const mockTransactionPrefix = "MOCKTXN"

// MockHandler 提供不依赖真实渠道的调试接口。
//
// 仅在非 release 模式下注册路由，见 internal/router。
// 生产环境暴露这类接口等同于允许任何人把任意订单改成已支付，
// 因此这里的开关不是「最佳实践」而是硬性要求。
type MockHandler struct {
	payments *service.PaymentService
}

// NewMockHandler 构造调试 handler。
func NewMockHandler(payments *service.PaymentService) *MockHandler {
	return &MockHandler{payments: payments}
}

// PaySuccess 模拟一次支付成功回调。
//
//	POST /api/v1/mock/pay-success
//
// 实现方式是构造一份与真实回调同构的 HTTP 请求，再走 service.HandleNotify，
// 而不是直接调用 service.HandlePayload。多绕这一圈是为了让 mock 渠道的
// ParseNotify 也进入执行路径——否则那段报文解析代码永远不会被覆盖，
// 而它恰恰是接入真实渠道时要整体替换掉的部分，最需要提前验证行为。
func (h *MockHandler) PaySuccess(c *gin.Context) {
	var req dto.MockPaySuccessRequest
	if err := bindJSON(c, &req); err != nil {
		response.Fail(c, err)
		return
	}

	ctx := c.Request.Context()

	// 先查订单，一是校验订单存在，二是取到订单金额用于填充回调报文
	status, err := h.payments.QueryStatus(ctx, req.OutTradeNo)
	if err != nil {
		response.Fail(c, err)
		return
	}

	amountFen := req.AmountFen
	if amountFen == 0 {
		amountFen = status.Order.AmountTotal
	}
	transactionID := req.TransactionID
	if transactionID == "" {
		transactionID = mockTransactionPrefix + idgen.RequestID()[:16]
	}
	openID := req.OpenID
	if openID == "" {
		openID = status.Order.OpenID
	}

	body := map[string]any{
		"out_trade_no":       req.OutTradeNo,
		"transaction_id":     transactionID,
		"trade_state":        channel.TradeStateSuccess,
		"trade_state_desc":   "支付成功（模拟）",
		"amount_total":       amountFen,
		"amount_payer_total": amountFen,
		"currency":           model.DefaultCurrency,
		"success_time":       time.Now().Format(time.RFC3339),
		"openid":             openID,
	}
	raw, err := json.Marshal(body)
	if err != nil {
		response.Fail(c, errcode.ErrInternal.WithCause(err))
		return
	}

	notifyReq, err := http.NewRequestWithContext(ctx, http.MethodPost, mockCallbackPath, bytes.NewReader(raw))
	if err != nil {
		response.Fail(c, errcode.ErrInternal.WithCause(err))
		return
	}
	notifyReq.Header.Set("Content-Type", "application/json")

	outcome, err := h.payments.HandleNotify(ctx, channel.CodeMock, notifyReq)
	if err != nil {
		response.Fail(c, err)
		return
	}

	response.OK(c, dto.MockNotifyResponse{
		OutTradeNo:    outcome.Order.OutTradeNo,
		OrderStatus:   outcome.Order.Status.String(),
		Paid:          outcome.Order.Status.IsPaid(),
		TransactionID: outcome.Order.TransactionID,
		Idempotent:    outcome.Idempotent,
	})
}
