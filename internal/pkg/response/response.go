// Package response 统一 HTTP 响应格式。
//
// 业务接口一律返回如下结构：
//
//	{"code": 0, "message": "ok", "data": {...}, "request_id": "..."}
//
// code 为 0 表示成功，非 0 时取值见 internal/errcode。
// HTTP 状态码与业务码同时给出：前者供网关、监控与重试策略判断，
// 后者供客户端做精细化的业务分支，二者不可互相替代。
//
// 注意：支付渠道的异步回调要求按渠道自己的规范应答，
// 不能使用本包的封装，详见 internal/handler/callback_handler.go。
package response

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"payment/internal/errcode"
	"payment/internal/pkg/logger"
)

// HeaderRequestID 是贯穿请求链路的追踪 ID 所使用的 HTTP 头。
//
// 定义在本包是为了让 middleware 单向依赖 response，避免两个包循环引用。
const HeaderRequestID = "X-Request-ID"

// SuccessCode 是成功响应固定的业务码。
const SuccessCode = 0

// okMessage 是成功响应的默认文案。
const okMessage = "ok"

// Body 统一响应体。
type Body struct {
	Code      int    `json:"code"`
	Message   string `json:"message"`
	Data      any    `json:"data,omitempty"`
	RequestID string `json:"request_id,omitempty"`
}

// RequestID 取出当前请求的追踪 ID。
//
// 优先读 context（中间件已写入），兜底读 Gin 的 Key，
// 两者取其一即可覆盖中间件链路与直接调用两种场景。
func RequestID(c *gin.Context) string {
	if c == nil {
		return ""
	}
	if id := logger.RequestIDFrom(c.Request.Context()); id != "" {
		return id
	}
	if v, ok := c.Get(KeyRequestID); ok {
		if id, ok := v.(string); ok {
			return id
		}
	}
	return ""
}

// KeyRequestID 是 request_id 在 gin.Context 中的键。
const KeyRequestID = "request_id"

// OK 返回成功响应。
func OK(c *gin.Context, data any) {
	c.JSON(http.StatusOK, Body{
		Code:      SuccessCode,
		Message:   okMessage,
		Data:      data,
		RequestID: RequestID(c),
	})
}

// Created 返回 201，用于资源创建成功的场景。
func Created(c *gin.Context, data any) {
	c.JSON(http.StatusCreated, Body{
		Code:      SuccessCode,
		Message:   okMessage,
		Data:      data,
		RequestID: RequestID(c),
	})
}

// Fail 把 error 转换为统一失败响应，HTTP 状态码取自 errcode。
func Fail(c *gin.Context, err error) {
	e := errcode.From(err)
	writeFail(c, e.HTTPStatus, e.Code, e.Msg)

	// 5xx 记 error 并带上底层原因，4xx 记 warn 且不带原因：
	// 参数错误是客户端行为，大量记 error 会淹没真正的服务端故障告警。
	log := logger.L(c.Request.Context())
	if e.HTTPStatus >= http.StatusInternalServerError {
		log.Error("请求处理失败",
			logger.String("path", c.FullPath()),
			logger.Int("errcode", e.Code),
			logger.Error(err),
		)
	} else {
		log.Warn("请求被拒绝",
			logger.String("path", c.FullPath()),
			logger.Int("errcode", e.Code),
			logger.String("reason", e.Error()),
		)
	}
}

// Abort 中断后续 handler 并返回失败响应，供中间件使用。
//
// 必须用 AbortWithStatusJSON 而不是 JSON：中间件之后还排着业务 handler，
// 不 Abort 的话请求会继续往下走，前置校验就形同虚设。
func Abort(c *gin.Context, err error) {
	e := errcode.From(err)
	c.AbortWithStatusJSON(e.HTTPStatus, Body{
		Code:      e.Code,
		Message:   e.Msg,
		RequestID: RequestID(c),
	})
}

// AbortWithStatus 只按 HTTP 状态码中断，用于不需要业务码的场景（如 CORS 预检）。
func AbortWithStatus(c *gin.Context, status int) {
	c.AbortWithStatus(status)
}

func writeFail(c *gin.Context, httpStatus, code int, msg string) {
	c.JSON(httpStatus, Body{
		Code:      code,
		Message:   msg,
		RequestID: RequestID(c),
	})
}
