---
kind: external_dependency
name: Zap 结构化日志库
slug: zap
category: external_dependency
category_hints:
    - framework_behavior
scope:
    - '**'
source_files:
    - go.mod
    - internal/pkg/logger/logger.go
---

项目使用 zap 作为结构化日志实现，由 `internal/pkg/logger` 统一初始化，支持 console/json 两种格式（生产推荐 json）。所有请求日志携带 `request_id`，密钥、证书内容与完整回调报文严禁打印。