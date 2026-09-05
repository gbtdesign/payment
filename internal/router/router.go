// Package router 负责路由注册与中间件挂载。
//
// 本包只做「把 handler 挂到路径上」这件事，不包含任何业务判断。
// 路由表集中在此处的价值是：所有对外暴露的接口一览无余，
// 审计一个支付服务开放了哪些能力时不需要翻遍整个代码库。
package router

import (
	"github.com/gin-gonic/gin"

	"payment/internal/handler"
	"payment/internal/middleware"
)

// APIPrefix 是业务接口的统一前缀。
//
// 带版本号是为了让不兼容的接口变更可以并行存在：
// 支付接口的调用方（前端、商户系统）升级节奏无法统一，
// 直接改老版本的行为会造成线上事故。
const APIPrefix = "/api/v1"

// 健康检查探针路径。放在版本前缀之外，
// 因为探针属于基础设施契约，不应随业务 API 版本演进而变化。
const (
	livenessPath  = "/healthz"
	readinessPath = "/readyz"
)

// Deps 路由注册所需的依赖集合。
//
// 用一个结构体而不是十来个位置参数：handler 的数量必然随业务增长，
// 位置参数的签名会不断变动，且调用处极易传错顺序而编译器无法发现。
type Deps struct {
	Orders    *handler.OrderHandler
	Payments  *handler.PaymentHandler
	Callbacks *handler.CallbackHandler
	Health    *handler.HealthHandler
	Mock      *handler.MockHandler

	// EnableMockRoutes 是否注册调试路由。
	//
	// release 模式下必须为 false：/api/v1/mock/pay-success 能把任意订单
	// 改成已支付，暴露在生产环境等同于资金漏洞。
	EnableMockRoutes bool

	// CORSOrigins 允许跨域的来源，留空表示不开放跨域（仅同源可访问）。
	CORSOrigins []string
}

// New 构造 Gin 引擎并完成全部路由注册。
//
// 用 gin.New() 而不是 gin.Default()：后者会自带 Logger 与 Recovery，
// 与本包的自定义中间件重复，导致每个请求打两遍日志、panic 被恢复两次。
func New(mode string, deps Deps) *gin.Engine {
	gin.SetMode(mode)

	engine := gin.New()
	// 开启后不匹配的方法会走 NoMethod 返回 405，而不是笼统的 404。
	// 客户端能据此区分「路径写错了」和「方法用错了」，排查成本差很多
	engine.HandleMethodNotAllowed = true

	engine.Use(
		// 中间件顺序有实际影响，不能随意调换：
		//   RequestID 必须最先，后面所有日志都依赖它串联
		//   Logger 在 Recovery 之前，才能记录到 panic 请求的耗时与状态码
		//   Recovery 在最内层，保证它捕获的范围覆盖所有后续处理
		middleware.RequestID(),
		middleware.Logger(),
		middleware.Recovery(),
	)
	if len(deps.CORSOrigins) > 0 {
		engine.Use(middleware.CORS(middleware.CORSOptions{AllowOrigins: deps.CORSOrigins}))
	}

	engine.NoRoute(middleware.NoRoute())
	// 405 需要知道该路径允许哪些方法，而 gin.Context 拿不到引擎实例，
	// 所以在这里把 engine 作为路由表注入
	engine.NoMethod(middleware.NoMethod(engine))

	registerHealth(engine, deps)
	registerAPI(engine, deps)

	return engine
}

func registerHealth(engine *gin.Engine, deps Deps) {
	engine.GET(livenessPath, deps.Health.Live)
	engine.GET(readinessPath, deps.Health.Ready)
}

func registerAPI(engine *gin.Engine, deps Deps) {
	v1 := engine.Group(APIPrefix)

	orders := v1.Group("/orders")
	{
		orders.POST("", deps.Orders.Create)
		orders.GET("/:out_trade_no", deps.Orders.Get)
	}

	payments := v1.Group("/payments")
	{
		payments.POST("/prepay", deps.Payments.Prepay)
		payments.GET("/:out_trade_no", deps.Payments.QueryStatus)
	}

	callbacks := v1.Group("/callbacks")
	{
		// 回调地址必须与商户平台配置、以及传给渠道的 notify_url 完全一致。
		// 三者任一处不匹配，微信都会把通知打到不存在的路径上，
		// 表现为「用户已扣款但订单一直是支付中」
		callbacks.POST("/wechatpay", deps.Callbacks.WechatPay)
	}

	if deps.EnableMockRoutes {
		mock := v1.Group("/mock")
		{
			mock.POST("/pay-success", deps.Mock.PaySuccess)
		}
	}
}
