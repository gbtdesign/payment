// Package service 承载业务逻辑。
//
// 分层约束：本层只依赖 model、repository 接口与 channel 抽象，
// 不感知 HTTP（不 import gin）、不感知具体渠道 SDK、不感知存储实现。
// 这样做的收益是业务规则可以被纯粹的单元测试覆盖，
// 不必为了验证「重复回调是否幂等」而起一个 HTTP 服务。
package service

import (
	"context"

	"payment/internal/channel"
	"payment/internal/errcode"
	"payment/internal/model"
	"payment/internal/pkg/idgen"
	"payment/internal/pkg/logger"
	"payment/internal/pkg/money"
	"payment/internal/repository"
)

// OrderService 订单业务。
type OrderService struct {
	orders   repository.OrderRepository
	registry *channel.Registry

	// defaultChannel 创建订单时未指定渠道所用的值
	defaultChannel channel.Code
	// outTradeNoPrefix 商户订单号前缀
	outTradeNoPrefix string
}

// NewOrderService 构造订单服务。
//
// 依赖以接口形式注入，不使用全局变量：
// 测试时可以塞入内存仓储或桩实现，生产装配在 internal/app 中完成。
func NewOrderService(
	orders repository.OrderRepository,
	registry *channel.Registry,
	defaultChannel channel.Code,
	outTradeNoPrefix string,
) *OrderService {
	if defaultChannel == "" {
		defaultChannel = channel.CodeMock
	}
	return &OrderService{
		orders:           orders,
		registry:         registry,
		defaultChannel:   defaultChannel,
		outTradeNoPrefix: outTradeNoPrefix,
	}
}

// CreateOrderParams 创建订单入参。
//
// 与 dto.CreateOrderRequest 分开定义：service 不接受 HTTP 层的结构体，
// 否则 dto 的字段变更会直接波及业务逻辑，分层就失去意义了。
// 金额在这里已经是「分」，元分转换由 handler 调用 pkg/money 完成。
type CreateOrderParams struct {
	Subject     string
	AmountTotal int64
	Channel     channel.Code
	OpenID      string
	Attach      string
	ClientIP    string
}

// Create 创建订单，初始状态为 CREATED。
//
// 这一步不调用任何渠道接口：下单与发起支付是两个独立动作，
// 中间可能插入风控、库存校验、优惠券核销等业务环节。
// 把它们合并成一个接口会让这些环节无处安放。
func (s *OrderService) Create(ctx context.Context, params CreateOrderParams) (*model.Order, error) {
	if err := money.ValidateAmount(params.AmountTotal); err != nil {
		return nil, errcode.ErrOrderAmountInvalid.WithCause(err)
	}

	code := params.Channel
	if code == "" {
		code = s.defaultChannel
	}
	// 渠道必须在创建时就确定并落库：发起支付时再解析渠道，
	// 会导致同一订单在不同请求下走到不同渠道，对账时无法解释
	if !s.registry.Has(code) {
		return nil, errcode.ErrChannelNotRegistered.WithMsg("支付渠道未启用: " + code.String())
	}

	outTradeNo := idgen.OutTradeNo(s.outTradeNoPrefix)
	order := model.NewOrder(outTradeNo, params.Subject, params.AmountTotal, code)
	order.OpenID = params.OpenID
	order.Attach = params.Attach
	order.ClientIP = params.ClientIP

	if err := s.orders.Create(ctx, order); err != nil {
		if repository.IsDuplicate(err) {
			// 订单号由本服务生成，重复意味着随机源或时钟异常，属于必须告警的问题
			logger.L(ctx).Error("生成的商户订单号重复",
				logger.String("out_trade_no", outTradeNo),
				logger.Error(err),
			)
		}
		return nil, errcode.ErrInternal.WithCause(err)
	}

	logger.L(ctx).Info("订单创建成功",
		logger.String("out_trade_no", outTradeNo),
		logger.Int64("amount_fen", order.AmountTotal),
		logger.String("channel", code.String()),
	)
	return order, nil
}

// Get 按商户订单号查询订单。
func (s *OrderService) Get(ctx context.Context, outTradeNo string) (*model.Order, error) {
	if outTradeNo == "" {
		return nil, errcode.ErrInvalidParam.WithMsg("out_trade_no 不能为空")
	}
	order, err := s.orders.GetByOutTradeNo(ctx, outTradeNo)
	if err != nil {
		// 仓储已经返回带错误码的 errcode，直接透传，
		// 否则「订单不存在」会被 From 归一成 500 内部错误
		return nil, err
	}
	return order, nil
}
