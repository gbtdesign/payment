package middleware

import (
	"errors"
	"fmt"
	"net"
	"runtime/debug"
	"sort"
	"strings"
	"syscall"

	"github.com/gin-gonic/gin"

	"payment/internal/errcode"
	"payment/internal/pkg/logger"
	"payment/internal/pkg/response"
)

// Recovery 捕获 panic 并返回统一响应，替代 Gin 默认的 gin.Recovery()。
//
// 替换的原因有两点：
//  1. Gin 默认返回 HTML 错误页，与本服务的 JSON 契约不一致，客户端解析会失败
//  2. Gin 默认把堆栈打到 stdout，无法与 request_id 关联，出问题后无从查起
//
// 对支付服务而言这一层是最后防线：一个未捕获的 panic 如果发生在回调处理中，
// 会导致渠道收不到应答从而持续重试，而本地状态可能已经推进了一半。
func Recovery() gin.HandlerFunc {
	return func(c *gin.Context) {
		defer func() {
			r := recover()
			if r == nil {
				return
			}

			log := logger.L(c.Request.Context())
			stack := string(debug.Stack())

			// 连接已被对端关闭时写不进响应，此时唯一能做的是留下日志。
			// 不区分这种情况会把「客户端断开」误报成服务端 panic，
			// 在告警里制造大量噪音。
			if isBrokenPipe(r) {
				log.Error("panic 恢复：连接已被客户端断开，无法应答",
					logger.String("panic", fmt.Sprintf("%v", r)),
					logger.String("path", c.Request.URL.Path),
					logger.String("stack", stack),
				)
				c.Abort()
				return
			}

			log.Error("panic 已恢复",
				logger.String("panic", fmt.Sprintf("%v", r)),
				logger.String("method", c.Request.Method),
				logger.String("path", c.Request.URL.Path),
				logger.String("client_ip", c.ClientIP()),
				logger.String("stack", stack),
			)

			// 对外只暴露「服务内部错误」，panic 的具体内容不进响应体：
			// 其中可能包含数据库连接串、内部路径等敏感信息
			if !c.Writer.Written() {
				response.Abort(c, errcode.ErrInternal)
			} else {
				// 响应已经开始写入（例如流式输出中途 panic），
				// 此时无法再改写状态码，只能中断后续 handler
				c.Abort()
			}
		}()

		c.Next()
	}
}

// isBrokenPipe 判断 panic 值是否为「连接已被对端关闭」类错误。
//
// panic 的值是 any，可能是字符串也可能才是 error，因此先做类型断言。
func isBrokenPipe(v any) bool {
	err, ok := v.(error)
	if !ok {
		return false
	}

	var netErr *net.OpError
	if errors.As(err, &netErr) {
		if errors.Is(netErr.Err, syscall.EPIPE) || errors.Is(netErr.Err, syscall.ECONNRESET) {
			return true
		}
	}

	// 兜底做字符串匹配：部分驱动会把底层 errno 包装成自定义类型，
	// errors.As 拿不到 *net.OpError，但错误文本里仍带这些关键字
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "broken pipe") ||
		strings.Contains(msg, "connection reset by peer")
}

// NoRoute 处理未匹配到任何路由的请求，返回统一的 JSON 404。
//
// Gin 默认返回空的 404，客户端拿到后无法区分「路径写错了」与「服务挂了」。
func NoRoute() gin.HandlerFunc {
	return func(c *gin.Context) {
		response.Abort(c, errcode.ErrNotFound.WithMsg("接口不存在: "+c.Request.Method+" "+c.Request.URL.Path))
	}
}

// RouteLister 抽象「列出已注册路由」的能力。
//
// gin.Context 里的 engine 字段是未导出的，handler 内部拿不到引擎实例，
// 因此路由表只能由 router 在注册 NoMethod 时显式注入。
// 抽成接口而不是直接收 *gin.Engine，是为了让单元测试能塞一个假的路由表。
type RouteLister interface {
	Routes() gin.RoutesInfo
}

// NoMethod 处理方法不被允许的请求，返回统一的 JSON 405。
//
// 需要引擎开启 HandleMethodNotAllowed 才会生效，见 internal/router。
func NoMethod(routes RouteLister) gin.HandlerFunc {
	return func(c *gin.Context) {
		// RFC 9110 要求 405 带上 Allow 头；但 Gin 只能在路由模板完全相等时
		// 反推出允许的方法，带路径参数的路由（/orders/:id）推不出来。
		// 推不出来时宁可不写这个头，也不要写一个错的误导客户端。
		if allowed := allowedMethods(routes, c.Request.URL.Path); len(allowed) > 0 {
			c.Header("Allow", strings.Join(allowed, ", "))
		}
		response.Abort(c, errcode.ErrMethodNotAllowed.WithMsg(
			fmt.Sprintf("接口 %s 不支持 %s 方法", c.Request.URL.Path, c.Request.Method)))
	}
}

// allowedMethods 返回指定路径已注册的方法列表，按字典序排序。
func allowedMethods(routes RouteLister, path string) []string {
	if routes == nil {
		return nil
	}
	seen := make(map[string]struct{}, 4)
	methods := make([]string, 0, 4)

	for _, r := range routes.Routes() {
		if r.Path != path {
			continue
		}
		if _, ok := seen[r.Method]; ok {
			continue
		}
		seen[r.Method] = struct{}{}
		methods = append(methods, r.Method)
	}
	sort.Strings(methods)
	return methods
}
