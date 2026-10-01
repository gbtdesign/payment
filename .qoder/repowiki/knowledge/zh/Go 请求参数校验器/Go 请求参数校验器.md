---
kind: external_dependency
name: Go 请求参数校验器
slug: validator
category: external_dependency
category_hints:
    - framework_behavior
scope:
    - '**'
---

使用 go-playground/validator/v10 对入参 DTO 做结构体验证（如金额最多两位小数），校验失败统一返回 errcode.ErrInvalidParam。