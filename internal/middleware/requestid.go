// Package middleware 提供 HTTP 中间件。
//
// 中间件只做与业务无关的横切关注点：请求追踪、访问日志、异常兜底、跨域。
// 任何涉及订单、金额、渠道状态的判断都不允许出现在这里——
// 那属于 service 层的职责，放在中间件里会让业务规则散落到难以测试的位置。
package middleware

import (
	"github.com/gin-gonic/gin"

	"payment/internal/pkg/idgen"
	"payment/internal/pkg/logger"
	"payment/internal/pkg/response"
)

const (
	// maxRequestIDLen 是接受的客户端传入 request_id 的最大长度。
	maxRequestIDLen = 64
	// maxRequestIDCount 限制单个请求中该头的数量，防止客户端塞入大量值。
	maxRequestIDCount = 1
)

// RequestID 为每个请求分配追踪 ID。
//
// 优先沿用客户端传入的 X-Request-ID，便于把前端日志与后端日志串起来；
// 缺失或不合法时生成新的。ID 会同时写入 context、gin.Context 与响应头，
// 三个位置分别服务于业务日志、响应封装与客户端排查。
func RequestID() gin.HandlerFunc {
	return func(c *gin.Context) {
		id := incomingRequestID(c)
		if !validRequestID(id) {
			id = idgen.RequestID()
		}

		c.Set(response.KeyRequestID, id)
		// 必须替换 c.Request 才能把 ID 传进 service 层的 context；
		// 只 c.Set 的话业务代码通过 ctx 取不到，日志就无法串联
		c.Request = c.Request.WithContext(logger.WithRequestID(c.Request.Context(), id))
		c.Header(response.HeaderRequestID, id)

		c.Next()
	}
}

// incomingRequestID 取出客户端传入的追踪 ID。
func incomingRequestID(c *gin.Context) string {
	values := c.Request.Header.Values(response.HeaderRequestID)
	if len(values) == 0 {
		return ""
	}
	// 多个值时只取第一个：把多个 ID 拼接起来会让日志检索失去意义
	if len(values) > maxRequestIDCount {
		return ""
	}
	return values[0]
}

// validRequestID 校验客户端传入的追踪 ID。
//
// 这里必须做字符白名单而不是只判长度：request_id 会原样写进日志与响应头，
// 若允许换行与控制字符，攻击者可以伪造日志行（log forging），
// 在支付系统的审计日志里插入虚假记录是严重问题。
func validRequestID(id string) bool {
	if id == "" || len(id) > maxRequestIDLen {
		return false
	}
	for i := 0; i < len(id); i++ {
		c := id[i]
		switch {
		case c >= '0' && c <= '9',
			c >= 'a' && c <= 'z',
			c >= 'A' && c <= 'Z',
			c == '-', c == '_', c == '.', c == ':':
		default:
			return false
		}
	}
	return true
}

// RequestIDFrom 取出当前请求的追踪 ID，供 handler 使用。
func RequestIDFrom(c *gin.Context) string {
	return response.RequestID(c)
}
