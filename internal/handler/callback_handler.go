package handler

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"payment/internal/channel"
	"payment/internal/errcode"
	"payment/internal/pkg/logger"
	"payment/internal/service"
)

// 微信支付 APIv3 回调应答规范。
//
// 成功：HTTP 200/204 + {"code":"SUCCESS","message":"成功"}
// 失败：HTTP 4xx/5xx + {"code":"FAIL","message":"失败原因"}
//
// 渠道只认这套约定，不认本服务的统一响应体。若按 {"code":0,"message":"ok"}
// 应答，微信会判定通知失败并按 15s/15s/30s/3m/... 持续重试，
// 表面上订单已经支付成功，日志里却全是重复回调——这是接入期最常见的坑。
const (
	replySuccess        = "SUCCESS"
	replyFail           = "FAIL"
	replySuccessMessage = "成功"
)

// channelReply 是回调应答体。
type channelReply struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// CallbackHandler 渠道异步回调入口。
type CallbackHandler struct {
	payments *service.PaymentService
}

// NewCallbackHandler 构造回调 handler。
func NewCallbackHandler(payments *service.PaymentService) *CallbackHandler {
	return &CallbackHandler{payments: payments}
}

// WechatPay 处理微信支付结果通知。
//
//	POST /api/v1/callbacks/wechatpay
func (h *CallbackHandler) WechatPay(c *gin.Context) {
	h.handle(c, channel.CodeWechatPay)
}

// handle 是各渠道回调的公共实现。
//
// 不同渠道的差别只在应答格式，业务处理完全一致，
// 因此这里只按渠道编码分发，处理逻辑全部在 service 层。
func (h *CallbackHandler) handle(c *gin.Context, code channel.Code) {
	ctx := c.Request.Context()
	log := logger.L(ctx).With(logger.String("channel", code.String()))

	outcome, err := h.payments.HandleNotify(ctx, code, c.Request)
	if err != nil {
		e := errcode.From(err)
		// 金额不一致与验签失败属于资金 / 安全事件，必须触发告警人工介入；
		// 其余错误（如订单不存在）记 warn，避免渠道重试期间刷满 error 日志
		// 把真正的告警淹掉
		if e.Code == errcode.ErrAmountMismatch.Code || e.Code == errcode.ErrChannelVerifyFailed.Code {
			log.Error("回调处理失败，需人工介入",
				logger.Int("errcode", e.Code), logger.Error(err))
		} else {
			log.Warn("回调处理失败",
				logger.Int("errcode", e.Code), logger.Error(err))
		}
		// 返回失败状态码让渠道按策略重试：对网络抖动、数据库瞬时不可用
		// 这类临时故障，重试是唯一能自动恢复的手段
		c.JSON(e.HTTPStatus, channelReply{Code: replyFail, Message: e.Msg})
		return
	}

	log.Info("回调处理完成",
		logger.String("out_trade_no", outcome.Order.OutTradeNo),
		logger.String("trade_state", outcome.TradeState),
		logger.Bool("idempotent", outcome.Idempotent),
	)
	// 幂等命中同样返回 SUCCESS：重复投递本来就是渠道的正常行为，
	// 本地状态已经正确时没有任何理由让渠道继续重试
	c.JSON(http.StatusOK, channelReply{Code: replySuccess, Message: replySuccessMessage})
}
