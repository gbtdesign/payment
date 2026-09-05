package handler

import (
	"github.com/gin-gonic/gin"

	"payment/internal/channel"
	"payment/internal/dto"
	"payment/internal/pkg/logger"
	"payment/internal/pkg/response"
	"payment/internal/service"
)

// OrderHandler 订单相关接口。
type OrderHandler struct {
	orders *service.OrderService
}

// NewOrderHandler 构造订单 handler。
func NewOrderHandler(orders *service.OrderService) *OrderHandler {
	return &OrderHandler{orders: orders}
}

// Create 创建订单。
//
//	POST /api/v1/orders
func (h *OrderHandler) Create(c *gin.Context) {
	var req dto.CreateOrderRequest
	if err := bindJSON(c, &req); err != nil {
		response.Fail(c, err)
		return
	}

	// 元转分在接入层完成，service 只接受「分」。
	// 放在这里而不是 service 内部，是为了让金额解析失败能被归类为
	// 客户端参数错误（400）而不是业务规则错误
	amountFen, err := req.AmountFen()
	if err != nil {
		logger.L(c.Request.Context()).Warn("金额解析失败",
			logger.String("amount", req.Amount), logger.Error(err))
		response.Fail(c, amountParseError(err))
		return
	}

	order, err := h.orders.Create(c.Request.Context(), service.CreateOrderParams{
		Subject:     req.Subject,
		AmountTotal: amountFen,
		Channel:     channel.Code(req.Channel),
		OpenID:      req.OpenID,
		Attach:      req.Attach,
		// ClientIP 由服务端取，不接受客户端传入：
		// 这个字段会用于风控，让调用方自由填写等于放弃风控
		ClientIP: c.ClientIP(),
	})
	if err != nil {
		response.Fail(c, err)
		return
	}

	response.Created(c, dto.FromOrder(order))
}

// Get 查询订单。
//
//	GET /api/v1/orders/:out_trade_no
func (h *OrderHandler) Get(c *gin.Context) {
	outTradeNo, err := pathParam(c, "out_trade_no")
	if err != nil {
		response.Fail(c, err)
		return
	}

	order, err := h.orders.Get(c.Request.Context(), outTradeNo)
	if err != nil {
		response.Fail(c, err)
		return
	}
	response.OK(c, dto.FromOrder(order))
}
