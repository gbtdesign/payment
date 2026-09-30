---
kind: external_dependency
name: Viper 配置加载器
slug: viper
category: external_dependency
category_hints:
    - framework_behavior
scope:
    - '**'
source_files:
    - go.mod
    - configs/config.yaml
    - internal/config/config.go
---

项目通过 Viper 加载 `configs/config.yaml`，并通过显式 `BindEnv` 映射环境变量覆盖（而非 `AutomaticEnv`），优先级为：环境变量 > yaml 文件 > 代码默认值。敏感项如 `WECHATPAY_API_V3_KEY` 刻意不在 yaml 中声明对应字段，只能从环境变量读取，防止误提交到仓库。

启动期对 `server.mode` / `port` / `log.format` / release 下的回调地址 / 微信支付凭据完整性做强校验，非法配置直接让进程启动失败，而不是带病运行。