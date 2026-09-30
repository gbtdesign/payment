---
kind: external_dependency
name: Go 结构体验证器
slug: go-playground-validator
category: external_dependency
category_hints:
    - framework_behavior
scope:
    - '**'
source_files:
    - go.mod
    - internal/dto/order.go
    - internal/dto/payment.go
---

通过 `github.com/go-playground/validator/v10` 对 DTO 进行绑定与校验，例如金额精度限制（最多两位小数）、订单号长度等。错误统一映射为 `errcode.ErrInvalidParam`（HTTP 400）。