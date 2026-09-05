package handler

import (
	"context"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"payment/internal/channel"
	"payment/internal/errcode"
	"payment/internal/pkg/response"
)

// OrderCounter 用于就绪探针上报当前的订单数量。
//
// 在 handler 内定义最小接口而不是直接依赖 repository：
// 接入层跨过 service 直接依赖存储层会让分层约束失效，
// 而这里只需要一个计数能力，没必要为此打开一整层依赖。
type OrderCounter interface {
	Count(ctx context.Context) int
}

// HealthHandler 健康检查探针。
type HealthHandler struct {
	registry  *channel.Registry
	counter   OrderCounter
	startedAt time.Time
}

// NewHealthHandler 构造健康检查 handler。
func NewHealthHandler(registry *channel.Registry, counter OrderCounter) *HealthHandler {
	return &HealthHandler{registry: registry, counter: counter, startedAt: time.Now()}
}

// Live 存活探针。
//
//	GET /healthz
//
// 只要进程还在处理请求就返回成功，不检查任何依赖。
//
// 这一点很关键：存活探针失败会让 K8s 重启容器。如果把渠道连通性、
// 数据库可用性放进来的检查，一次外部抖动就会导致所有副本被同时重启，
// 正在处理中的支付回调全部中断——把「部分功能不可用」放大成「全站不可用」。
func (h *HealthHandler) Live(c *gin.Context) {
	response.OK(c, gin.H{"status": "ok"})
}

// Ready 就绪探针。
//
//	GET /readyz
//
// 就绪探针失败会让 K8s 把副本从 Service 的 endpoints 中摘除，
// 但不会重启进程，因此这里可以放真正的可用性判断。
func (h *HealthHandler) Ready(c *gin.Context) {
	codes := h.registry.Codes()
	if len(codes) == 0 {
		// 没有任何可用渠道时，服务无法完成任何一笔支付，
		// 继续接收流量只会让用户的支付请求全部失败，不如直接摘掉
		c.JSON(http.StatusServiceUnavailable, response.Body{
			Code:    errcode.ErrChannelNotRegistered.Code,
			Message: "没有可用的支付渠道，服务未就绪",
		})
		return
	}

	names := make([]string, 0, len(codes))
	for _, code := range codes {
		names = append(names, code.String())
	}

	response.OK(c, gin.H{
		"status":         "ready",
		"channels":       names,
		"uptime_seconds": int(time.Since(h.startedAt).Seconds()),
		"order_count":    h.counter.Count(c.Request.Context()),
	})
}
