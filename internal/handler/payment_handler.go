package handler

import (
	"strings"

	"github.com/gin-gonic/gin"

	"payment/internal/channel"
	"payment/internal/dto"
	"payment/internal/pkg/logger"
	"payment/internal/pkg/money"
	"payment/internal/pkg/response"
	"payment/internal/service"
)

// PaymentHandler 支付相关接口。
type PaymentHandler struct {
	payments *service.PaymentService
}

// NewPaymentHandler 构造支付 handler。
func NewPaymentHandler(payments *service.PaymentService) *PaymentHandler {
	return &PaymentHandler{payments: payments}
}

// Prepay 发起支付，返回前端调起支付所需参数。
//
//	POST /api/v1/payments/prepay
//
// 前端拿到 invoke_params 后直接传给 wx.requestPayment（小程序）
// 或 WeixinJSBridge.invoke（微信内 H5）。
func (h *PaymentHandler) Prepay(c *gin.Context) {
	var req dto.PrepayRequest
	if err := bindJSON(c, &req); err != nil {
		response.Fail(c, err)
		return
	}

	outcome, err := h.payments.Prepay(c.Request.Context(), service.PrepayParams{
		OutTradeNo: req.OutTradeNo,
		TradeType:  channel.TradeType(req.TradeType),
		OpenID:     req.OpenID,
		ClientIP:   c.ClientIP(),
	})
	if err != nil {
		response.Fail(c, err)
		return
	}

	response.OK(c, dto.FromPrepayResult(outcome.Payment.PaymentNo, outcome.TradeType, outcome.Result))
}

// QueryStatus 查询支付状态，供前端轮询。
//
//	GET /api/v1/payments/:out_trade_no
//	GET /api/v1/payments/:out_trade_no?sync=true   # 强制向渠道查单后返回
//
// 默认只读本地数据：轮询频率高，每次都打渠道会触发限流。
// sync=true 用于「回调迟迟没来」时手动对齐，生产环境应由定时任务批量执行。
func (h *PaymentHandler) QueryStatus(c *gin.Context) {
	outTradeNo, err := pathParam(c, "out_trade_no")
	if err != nil {
		response.Fail(c, err)
		return
	}

	ctx := c.Request.Context()
	if parseBoolQuery(c, "sync") {
		if _, syncErr := h.payments.SyncFromChannel(ctx, outTradeNo); syncErr != nil {
			// 同步失败不阻断查询：渠道抖动时前端仍应拿到本地已知状态，
			// 直接报错会让收银台停在「加载中」，用户体验比返回旧状态更差
			logger.L(ctx).Warn("主动查单失败，返回本地状态",
				logger.String("out_trade_no", outTradeNo),
				logger.Error(syncErr),
			)
		}
	}

	status, err := h.payments.QueryStatus(ctx, outTradeNo)
	if err != nil {
		response.Fail(c, err)
		return
	}
	response.OK(c, toPaymentStatusResponse(status))
}

// toPaymentStatusResponse 把 service 的查询结果转换为响应结构。
func toPaymentStatusResponse(status *service.PaymentStatus) *dto.PaymentStatusResponse {
	if status == nil || status.Order == nil {
		return nil
	}

	items := make([]*dto.PaymentItem, 0, len(status.Payments))
	for _, p := range status.Payments {
		if item := dto.FromPayment(p); item != nil {
			items = append(items, item)
		}
	}

	return &dto.PaymentStatusResponse{
		OutTradeNo:  status.Order.OutTradeNo,
		OrderStatus: status.Order.Status.String(),
		Paid:        status.Order.Status.IsPaid(),
		Amount:      money.FenToYuan(status.Order.AmountTotal),
		AmountFen:   status.Order.AmountTotal,
		Payments:    items,
	}
}

// parseBoolQuery 解析布尔型查询参数，接受 true/1/yes/on（大小写不敏感）。
func parseBoolQuery(c *gin.Context, key string) bool {
	switch strings.ToLower(strings.TrimSpace(c.Query(key))) {
	case "true", "1", "yes", "on":
		return true
	default:
		return false
	}
}
