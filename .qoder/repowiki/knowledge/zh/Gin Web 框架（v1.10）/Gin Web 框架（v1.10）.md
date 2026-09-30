---
kind: external_dependency
name: Gin Web 框架（v1.10）
slug: gin
category: external_dependency
category_hints:
    - framework_behavior
scope:
    - '**'
source_files:
    - go.mod
    - internal/router/router.go
    - internal/app/app.go
---

本项目作为 Go + Gin 经典分层支付服务骨架，使用 gin v1.10.0 作为 HTTP 框架。路由、中间件、handler 均基于 Gin 的 Engine/RouterGroup 构建；`internal/app` 负责构造 Gin 实例并挂载 `/healthz`、`/readyz`、业务路由与回调路由。

注意：当前代码在 `middleware/recovery.go` 中曾尝试访问 `c.Engine` 字段，但 gin v1.10 该字段未导出，因此实际通过 router 注册时把路由表注入的方式绕过——后续新增中间件或 handler 时不能直接取 `c.Engine`，应沿用 router 层集中注册的约定。