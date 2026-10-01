---
kind: external_dependency
name: Zap 结构化日志
slug: zap
category: external_dependency
category_hints:
    - framework_behavior
scope:
    - '**'
---

通过 internal/pkg/logger 封装 zap.Logger，支持 json/development 两种 format（由 config.log.format 控制），所有业务日志携带 request_id 以便跨中间件追踪。