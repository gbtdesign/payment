package middleware

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

// defaultMaxAge 是预检结果的缓存时长。
//
// 设长一些可以减少 OPTIONS 请求数量，支付前端在收银台页面会频繁调用接口，
// 每次都预检会明显拖慢首次支付体验。
const defaultMaxAge = 12 * time.Hour

// defaultAllowHeaders 是预检允许的请求头。
//
// X-Request-ID 必须在列，否则前端无法把追踪 ID 传进来，
// 出问题时前后端日志就串不起来。
var defaultAllowHeaders = []string{
	"Origin",
	"Content-Type",
	"Accept",
	"Authorization",
	"X-Request-ID",
}

// defaultExposeHeaders 是允许浏览器脚本读取的响应头。
//
// 浏览器默认只暴露 CORS 安全列表里的响应头，
// 不显式声明的话前端 JS 读不到 X-Request-ID，用户报障时无法提供追踪 ID。
var defaultExposeHeaders = []string{"X-Request-ID"}

// CORSOptions 跨域配置。
type CORSOptions struct {
	// AllowOrigins 允许的来源列表，"*" 表示不限制。
	//
	// 留空表示不下发任何 CORS 头，即只允许同源访问——这是支付服务的推荐配置，
	// 收银台通常与业务系统同域部署，没有必要开放跨域。
	AllowOrigins []string
	// AllowCredentials 是否允许携带 Cookie / Authorization 凭证。
	//
	// 与 AllowOrigins 为 "*" 互斥：浏览器规范禁止二者同时成立，
	// 同时设置会导致所有跨域请求直接失败。
	AllowCredentials bool
	// MaxAge 预检结果缓存时长，零值取 12 小时。
	MaxAge time.Duration
	// AllowHeaders 预检允许的请求头，留空用默认值。
	AllowHeaders []string
	// ExposeHeaders 允许前端读取的响应头，留空用默认值。
	ExposeHeaders []string
}

// CORS 返回跨域中间件。
func CORS(opts CORSOptions) gin.HandlerFunc {
	allowAll := false
	allowed := make(map[string]struct{}, len(opts.AllowOrigins))
	for _, o := range opts.AllowOrigins {
		o = strings.TrimSpace(o)
		if o == "" {
			continue
		}
		if o == "*" {
			allowAll = true
			continue
		}
		allowed[strings.ToLower(o)] = struct{}{}
	}
	// 凭证优先：Allow-Credentials 与通配 Origin 不能共存，
	// 此时退回白名单模式，避免下发一组浏览器必然拒绝的头
	allowCredentials := opts.AllowCredentials
	if allowAll && allowCredentials {
		allowAll = false
	}

	maxAge := opts.MaxAge
	if maxAge <= 0 {
		maxAge = defaultMaxAge
	}
	allowHeaders := strings.Join(orDefaultList(opts.AllowHeaders, defaultAllowHeaders), ", ")
	exposeHeaders := strings.Join(orDefaultList(opts.ExposeHeaders, defaultExposeHeaders), ", ")
	allowMethods := strings.Join([]string{
		http.MethodGet, http.MethodPost, http.MethodPut,
		http.MethodPatch, http.MethodDelete, http.MethodOptions,
	}, ", ")

	return func(c *gin.Context) {
		origin := strings.TrimSpace(c.Request.Header.Get("Origin"))
		// 没有 Origin 说明是同源请求或服务端调用，CORS 完全不适用，
		// 直接放行且不加任何头
		if origin == "" {
			c.Next()
			return
		}

		allowOrigin := ""
		switch {
		case allowAll:
			allowOrigin = "*"
		default:
			if _, ok := allowed[strings.ToLower(origin)]; ok {
				allowOrigin = origin
			}
		}
		if allowOrigin == "" {
			// 来源不在白名单：不加 CORS 头，由浏览器自行拦截。
			//
			// 这里刻意不返回 403——CORS 是浏览器的安全机制而非服务端的访问控制，
			// 非浏览器客户端（如微信支付的回调、内部服务调用）也可能带上 Origin，
			// 在这里拒绝会把合法的服务端调用一并挡掉。
			// 真正的访问控制必须由鉴权中间件完成。
			c.Next()
			return
		}

		c.Header("Access-Control-Allow-Origin", allowOrigin)
		if allowOrigin != "*" {
			// 回显具体 Origin 时必须声明 Vary: Origin，
			// 否则 CDN / 反向代理会把 A 站点的应答缓存后返给 B 站点
			c.Header("Vary", "Origin")
		}
		if allowCredentials && allowOrigin != "*" {
			c.Header("Access-Control-Allow-Credentials", "true")
		}
		c.Header("Access-Control-Allow-Methods", allowMethods)
		c.Header("Access-Control-Allow-Headers", allowHeaders)
		c.Header("Access-Control-Expose-Headers", exposeHeaders)
		c.Header("Access-Control-Max-Age", strconv.Itoa(int(maxAge/time.Second)))

		// 预检请求不需要走到业务 handler
		if c.Request.Method == http.MethodOptions {
			c.AbortWithStatus(http.StatusNoContent)
			return
		}
		c.Next()
	}
}

// DevCORS 返回开发环境用的宽松跨域配置。
//
// 只应在本地前后端分离联调时使用，生产环境必须换成明确的来源白名单。
func DevCORS() gin.HandlerFunc {
	return CORS(CORSOptions{AllowOrigins: []string{"*"}})
}

func orDefaultList(list, fallback []string) []string {
	if len(list) == 0 {
		return fallback
	}
	return list
}
