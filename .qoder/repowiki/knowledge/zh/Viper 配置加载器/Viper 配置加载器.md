---
kind: external_dependency
name: Viper 配置加载器
slug: viper
category: external_dependency
category_hints:
    - framework_behavior
scope:
    - '**'
---

使用 Viper 加载 configs/config.yaml，并通过显式 BindEnv 映射环境变量覆盖（如 PAYMENT_SERVER_MODE、WECHATPAY_*），而非 AutomaticEnv，避免环境变量意外覆盖到不该覆盖的配置项。启动期对 mode/port/log.format/release 下的回调地址/wechatpay 凭据做强校验，非法配置直接让进程启动失败。