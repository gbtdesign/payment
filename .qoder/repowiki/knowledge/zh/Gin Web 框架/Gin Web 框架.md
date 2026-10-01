---
kind: external_dependency
name: Gin Web 框架
slug: gin
category: external_dependency
category_hints:
    - framework_behavior
scope:
    - '**'
---

本项目基于 Gin v1.10.0 构建 HTTP 服务，入口在 cmd/server/main.go，路由集中在 internal/router/router.go，handler 与 middleware 直接依赖 gin.Context。注意：gin v1.10 的 Engine 字段未导出，不能通过 c.Engine 访问底层路由表；本项目采用由 router 在注册时把路由表注入的方式绕过该限制。