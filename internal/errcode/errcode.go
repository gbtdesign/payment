// Package errcode 定义业务错误码。
//
// 分段规则：
//
//	0        成功
//	10xxx    通用错误（参数、未找到、内部错误）
//	20xxx    订单相关
//	30xxx    支付相关
//	40xxx    支付渠道相关
//
// 每个错误码绑定一个 HTTP 状态码，由 pkg/response 统一转换为响应体。
package errcode

import (
	"errors"
	"fmt"
	"net/http"
)

// Error 业务错误。
//
// 底层原因放在私有字段 cause 中：Error() 会带上它便于排查，
// 但对外暴露的 Msg 始终是预设文案，不会把内部实现细节泄漏给客户端。
type Error struct {
	Code       int
	Msg        string
	HTTPStatus int

	cause error
}

func newError(code, httpStatus int, msg string) *Error {
	return &Error{Code: code, Msg: msg, HTTPStatus: httpStatus}
}

func (e *Error) Error() string {
	if e.cause != nil {
		return fmt.Sprintf("errcode %d: %s: %v", e.Code, e.Msg, e.cause)
	}
	return fmt.Sprintf("errcode %d: %s", e.Code, e.Msg)
}

// Unwrap 让 errors.Is / errors.As 能穿透本类型找到底层原因。
func (e *Error) Unwrap() error { return e.cause }

// Is 使同一错误码的实例相互相等。
//
// 因为 WithCause 每次返回新实例，若只比较指针，
// errors.Is(err, ErrOrderNotFound) 在带原因时会失效。
func (e *Error) Is(target error) bool {
	t, ok := target.(*Error)
	if !ok {
		return false
	}
	return e.Code == t.Code
}

// WithCause 复制一份错误并挂上底层原因。
//
// 必须复制而不能直接改 e.cause：这些错误码是包级共享变量，
// 就地修改会在并发请求之间串数据，是极难排查的隐患。
func (e *Error) WithCause(cause error) *Error {
	cp := *e
	cp.cause = cause
	return &cp
}

// WithMsg 复制一份错误并替换对外文案，用于需要补充上下文的场景。
func (e *Error) WithMsg(msg string) *Error {
	cp := *e
	cp.Msg = msg
	return &cp
}

// Cause 返回底层原因，可能为 nil。
func (e *Error) Cause() error { return e.cause }

// 通用错误 10xxx
var (
	// ErrInvalidParam 请求参数校验不通过。
	ErrInvalidParam = newError(10001, http.StatusBadRequest, "请求参数不合法")
	// ErrBadRequest 请求体无法解析（非法 JSON、类型不匹配等）。
	ErrBadRequest = newError(10002, http.StatusBadRequest, "请求无法解析")
	// ErrNotFound 资源不存在。
	ErrNotFound = newError(10003, http.StatusNotFound, "资源不存在")
	// ErrInternal 服务内部错误。对外不暴露细节，真实原因只进日志。
	ErrInternal = newError(10004, http.StatusInternalServerError, "服务内部错误")
	// ErrUnsupportedOperation 操作在当前配置下不被支持（如生产模式访问调试接口）。
	ErrUnsupportedOperation = newError(10005, http.StatusForbidden, "当前环境不支持该操作")
	// ErrMethodNotAllowed 请求方法不被允许。
	//
	// 必须单独给一个错误码而不能复用 ErrInvalidParam：
	// RFC 9110 规定这种情况返回 405 并带上 Allow 头，
	// 而 ErrInvalidParam 绑定的是 400，两者对客户端与网关的含义完全不同。
	ErrMethodNotAllowed = newError(10006, http.StatusMethodNotAllowed, "请求方法不被允许")
)

// 订单相关 20xxx
var (
	// ErrOrderNotFound 订单不存在。
	ErrOrderNotFound = newError(20001, http.StatusNotFound, "订单不存在")
	// ErrOrderStatusInvalid 订单当前状态不允许执行该操作。
	ErrOrderStatusInvalid = newError(20002, http.StatusConflict, "订单状态不允许该操作")
	// ErrOrderAmountInvalid 金额非法（非正数、超上限、精度超限）。
	ErrOrderAmountInvalid = newError(20003, http.StatusBadRequest, "订单金额不合法")
	// ErrOrderClosed 订单已关闭，不能再支付。
	ErrOrderClosed = newError(20004, http.StatusConflict, "订单已关闭")
	// ErrOrderAlreadyPaid 订单已支付，重复支付被拒绝。
	ErrOrderAlreadyPaid = newError(20005, http.StatusConflict, "订单已支付")
)

// 支付相关 30xxx
var (
	// ErrPaymentNotFound 支付流水不存在。
	ErrPaymentNotFound = newError(30001, http.StatusNotFound, "支付记录不存在")
	// ErrAmountMismatch 回调金额与订单金额不一致，属于严重异常。
	ErrAmountMismatch = newError(30002, http.StatusBadRequest, "支付金额与订单金额不一致")
	// ErrOpenIDRequired JSAPI 交易必须提供 openid。
	ErrOpenIDRequired = newError(30003, http.StatusBadRequest, "缺少支付者 openid")
	// ErrChannelDisabled 渠道未在配置中启用。
	ErrChannelDisabled = newError(30004, http.StatusBadRequest, "支付渠道未启用")
)

// 支付渠道相关 40xxx
var (
	// ErrChannelNotRegistered 请求的渠道没有对应实现。
	ErrChannelNotRegistered = newError(40001, http.StatusInternalServerError, "支付渠道未注册")
	// ErrChannelCallFailed 调用渠道接口失败（网络、超时、渠道返回错误）。
	ErrChannelCallFailed = newError(40002, http.StatusBadGateway, "支付渠道调用失败")
	// ErrChannelVerifyFailed 回调验签或解密失败。
	ErrChannelVerifyFailed = newError(40003, http.StatusBadRequest, "支付回调校验失败")
	// ErrChannelNotImplemented 渠道方法尚未实现，mock 渠道的退款走这里。
	ErrChannelNotImplemented = newError(40004, http.StatusNotImplemented, "支付渠道功能未实现")
)

// From 把任意 error 归一为 *Error。
//
// 非业务错误（如数据库驱动报错、渠道 SDK 报错）统一映射为 ErrInternal，
// 并把原始错误挂在 cause 上，这样 handler 层只需要处理一种类型。
func From(err error) *Error {
	if err == nil {
		return nil
	}
	var e *Error
	if errors.As(err, &e) {
		return e
	}
	return ErrInternal.WithCause(err)
}

// Code 取出错误码，nil 错误返回 0。
func Code(err error) int {
	if e := From(err); e != nil {
		return e.Code
	}
	return 0
}
