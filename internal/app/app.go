// Package app 负责依赖装配与应用生命周期。
//
// 这里是整个服务唯一的组装点：配置 -> 日志 -> 渠道注册 -> 仓储 -> 服务
// -> handler -> 路由，全部在此串起来。
//
// 采用手工装配而不引入 wire / fx 这类依赖注入框架：
// 本服务的依赖图是一条单向链，没有循环依赖也没有多实现选择的复杂度，
// 手工装配的代码量更少、跳转更直接，出问题时不需要理解框架的生成规则。
//
// 将来替换存储（内存 -> MySQL）或新增渠道，改动都集中在本文件，
// 这正是分层设计想要达到的效果。
package app

import (
	"errors"
	"fmt"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"payment/internal/channel"
	"payment/internal/channel/mock"
	"payment/internal/config"
	"payment/internal/handler"
	"payment/internal/pkg/logger"
	"payment/internal/repository"
	"payment/internal/router"
	"payment/internal/service"
)

// App 是已装配完成的应用实例。
type App struct {
	engine   *gin.Engine
	log      *zap.Logger
	cfg      *config.Config
	registry *channel.Registry
}

// New 按配置装配应用。
//
// 任何一步失败都直接返回错误让进程启动失败，不做降级：
// 支付服务带着残缺的依赖启动（比如渠道没注册上），
// 后果是所有支付请求失败，而这种状态从外部看和「服务正常」没有区别，
// 排查起来极其困难。宁可起不来。
func New(cfg *config.Config) (*App, error) {
	if cfg == nil {
		return nil, errors.New("app: 配置不能为空")
	}

	log, err := logger.New(logger.Config{Level: cfg.Log.Level, Format: cfg.Log.Format})
	if err != nil {
		return nil, fmt.Errorf("初始化日志失败: %w", err)
	}
	// 设为全局兜底 logger，让 service 层通过 logger.L(ctx) 就能拿到带配置的实例
	logger.SetBase(log)

	registry, err := buildRegistry(cfg)
	if err != nil {
		return nil, err
	}

	// 存储层：当前为内存实现。
	//
	// 换成 MySQL 时只需替换这两行（并补上迁移与连接池初始化），
	// service 层依赖的是 repository 接口，无需任何改动。
	orders := repository.NewMemoryOrderRepository()
	payments := repository.NewMemoryPaymentRepository()

	orderSvc := service.NewOrderService(
		orders,
		registry,
		channel.Code(cfg.Payment.DefaultChannel),
		cfg.Payment.OutTradeNoPrefix,
	)
	paymentSvc := service.NewPaymentService(
		orders,
		payments,
		registry,
		service.PaymentServiceOptions{
			NotifyURL:       cfg.NotifyURL(),
			PaymentNoPrefix: cfg.Payment.PaymentNoPrefix,
		},
	)

	engine := router.New(cfg.Server.Mode, router.Deps{
		Orders:    handler.NewOrderHandler(orderSvc),
		Payments:  handler.NewPaymentHandler(paymentSvc),
		Callbacks: handler.NewCallbackHandler(paymentSvc),
		Health:    handler.NewHealthHandler(registry, orders),
		Mock:      handler.NewMockHandler(paymentSvc),
		// release 模式下绝不能注册调试路由：
		// /api/v1/mock/pay-success 能把任意订单改成已支付
		EnableMockRoutes: !cfg.IsRelease(),
		CORSOrigins:      corsOrigins(cfg),
	})

	log.Info("应用装配完成",
		zap.String("mode", cfg.Server.Mode),
		zap.String("default_channel", cfg.Payment.DefaultChannel),
		zap.Strings("channels", channelNames(registry)),
		zap.String("notify_url", cfg.NotifyURL()),
		zap.Bool("mock_routes", !cfg.IsRelease()),
	)

	return &App{engine: engine, log: log, cfg: cfg, registry: registry}, nil
}

// buildRegistry 按配置注册可用的支付渠道。
func buildRegistry(cfg *config.Config) (*channel.Registry, error) {
	registry := channel.NewRegistry()

	// mock 渠道只在非 release 模式注册。
	//
	// 生产环境保留它是有实际风险的：调用方可以在创建订单时指定 channel=mock，
	// 这类订单永远不会真正收到钱，却会在报表里被当成正常订单统计。
	// 不注册之后，release 环境下 mock 相关的任何调用都会在渠道解析阶段直接失败。
	if !cfg.IsRelease() {
		if err := registry.Register(mock.New(cfg.Channels.WechatPay.AppID)); err != nil {
			return nil, fmt.Errorf("注册 mock 渠道失败: %w", err)
		}
	}

	if cfg.Channels.WechatPay.Enabled {
		// 配置声明启用但代码里没有实现，必须让进程直接失败。
		//
		// 静默跳过是最坏的选择：运维看到 enabled=true 会认为微信支付已可用，
		// 而实际上所有微信支付请求都会失败，这种「配置与行为不一致」
		// 造成的误判可能持续很久。
		return nil, errors.New("channels.wechatpay.enabled=true 但微信支付渠道尚未实现，" +
			"接入方式见 internal/channel/wechatpay/doc.go，" +
			"接入前的准备工作见 docs/wechatpay-checklist.md")
	}

	// 默认渠道必须真实可用，否则创建订单会在第一个请求就失败
	defaultChannel := channel.Code(cfg.Payment.DefaultChannel)
	if !registry.Has(defaultChannel) {
		if registry.Len() == 0 && cfg.IsRelease() {
			// release 模式下 mock 渠道被刻意排除，而微信支付尚未实现，
			// 因此当前版本的服务不可能以 release 模式启动。
			// 把原因说清楚，避免运维按「生产环境要用 release」的惯例配置后，
			// 只看到一句「渠道未注册」而无从下手。
			return nil, errors.New("release 模式下没有可用的支付渠道：" +
				"mock 渠道在生产模式被刻意禁用，微信支付渠道尚未实现。" +
				"本地联调请用 server.mode=debug；上线前需先完成 " +
				"internal/channel/wechatpay 的实现，准备事项见 docs/wechatpay-checklist.md")
		}
		return nil, fmt.Errorf("payment.default_channel=%q 对应的渠道未注册，"+
			"当前可用渠道: %v", cfg.Payment.DefaultChannel, channelNames(registry))
	}

	return registry, nil
}

// corsOrigins 返回跨域白名单。
func corsOrigins(cfg *config.Config) []string {
	if cfg.IsRelease() {
		// 生产环境默认不开放跨域：收银台应与业务系统同域部署。
		// 确有跨域需求时应在此返回明确的来源白名单，
		// 而不是放开 "*"——支付接口配通配跨域会给 CSRF 留出空间
		return nil
	}
	// 本地开发常出现前端跑在 5173、后端跑在 8080 的情况，放开以便联调
	return []string{"*"}
}

func channelNames(registry *channel.Registry) []string {
	codes := registry.Codes()
	names := make([]string, 0, len(codes))
	for _, code := range codes {
		names = append(names, code.String())
	}
	return names
}

// Engine 返回 HTTP 处理器，交给 http.Server 使用。
func (a *App) Engine() *gin.Engine { return a.engine }

// Logger 返回应用日志实例。
func (a *App) Logger() *zap.Logger { return a.log }

// Config 返回当前生效的配置，供诊断接口使用。
//
// 返回的是指针，调用方不应修改其中的值。
func (a *App) Config() *config.Config { return a.cfg }

// Registry 返回渠道注册表。
func (a *App) Registry() *channel.Registry { return a.registry }

// Close 释放应用持有的资源。
//
// 目前只需要 flush 日志缓冲，但这个方法必须存在且必须被调用：
// zap 的 core 带缓冲，进程退出前不 flush 会丢掉最后几条日志，
// 而支付故障排查时最关键的信息往往就在最后那几条里。
func (a *App) Close() error {
	if a.log != nil {
		logger.Sync(a.log)
	}
	return nil
}
