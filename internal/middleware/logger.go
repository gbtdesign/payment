package middleware

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"payment/internal/pkg/logger"
)

// probePaths 是健康检查探针路径。
//
// K8s 默认每秒探测一次，若与业务请求同样记 info，
// 日志里 99% 都是探针记录，真正有价值的支付日志会被彻底淹没。
var probePaths = map[string]struct{}{
	"/healthz": {},
	"/readyz":  {},
}

// maxUserAgentLen 限制记录的用户代理长度，避免超长 UA 撑爆日志行。
const maxUserAgentLen = 256

// Logger 访问日志中间件，替代 Gin 默认的 gin.Logger()。
//
// 替换的原因：Gin 默认输出是给人看的彩色文本，无法被日志系统按字段检索。
// 支付服务排查问题时几乎总是从「某个 out_trade_no」或「某个 request_id」入手，
// 结构化字段是前提。
//
// 刻意不记录请求体与响应体：回调报文含 openid 与加密数据，
// 下单请求含金额与商品信息，落进日志既违反最小化原则，
// 也会让日志体积随业务量线性膨胀。需要报文时应在 service 层按需打点。
func Logger() gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		path := c.Request.URL.Path
		query := c.Request.URL.RawQuery

		c.Next()

		latency := time.Since(start)
		status := c.Writer.Status()

		fields := []zap.Field{
			logger.Int("status", status),
			logger.String("method", c.Request.Method),
			logger.String("path", path),
			logger.Duration("latency", latency),
			logger.String("client_ip", c.ClientIP()),
			logger.String("user_agent", truncate(c.Request.UserAgent(), maxUserAgentLen)),
		}
		if query != "" {
			fields = append(fields, logger.String("query", query))
		}
		// 只记录路由模板而不是真实 URL，避免把路径参数写进日志：
		// /orders/ORD2026... 这类带业务标识的路径会让日志聚合失效
		if route := c.FullPath(); route != "" {
			fields = append(fields, logger.String("route", route))
		}
		if len(c.Errors) > 0 {
			fields = append(fields, logger.String("errors", c.Errors.String()))
		}

		log := logger.L(c.Request.Context())
		switch {
		case isProbePath(path):
			log.Debug("请求完成", fields...)
		case status >= http.StatusInternalServerError:
			log.Error("请求处理失败", fields...)
		case status >= http.StatusBadRequest:
			log.Warn("请求被拒绝", fields...)
		default:
			log.Info("请求完成", fields...)
		}
	}
}

func isProbePath(path string) bool {
	_, ok := probePaths[path]
	return ok
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}
