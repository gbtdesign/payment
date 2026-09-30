---
kind: configuration_system
name: 基于 Viper 的支付服务配置加载与校验系统
category: configuration_system
scope:
    - '**'
source_files:
    - internal/config/config.go
    - configs/config.yaml
    - cmd/server/main.go
    - .env.example
---

## 1. 使用的系统与工具

- **Viper** (`github.com/spf13/viper`)：作为唯一的配置加载器，负责 YAML 文件解析、默认值注入、环境变量绑定与结构体反序列化。
- **Go `flag`**：进程入口 `cmd/server/main.go` 仅暴露 `-config` 命令行参数用于覆盖配置文件路径。
- **YAML**：主配置文件格式为 `configs/config.yaml`，是模板文件（不含真实密钥）。
- **环境变量**：所有运行时覆盖项通过显式 `BindEnv` 映射到配置键；敏感项（APIv3Key）完全绕过 Viper，直接由 `os.Getenv` 读取。
- **`.env.example`**：仅提供键名与示例值的模板，注释明确说明本服务不内置 `.env` 解析，需由部署环境或本地 `source .env` 注入。

## 2. 关键文件

- `internal/config/config.go` — 配置结构定义、默认值、环境变量绑定、归一化与校验的核心实现。
- `configs/config.yaml` — 可提交的配置模板，包含 server / log / payment / channels.wechatpay 四层结构。
- `cmd/server/main.go` — 进程入口，调用 `config.Load` 并在失败时以退出码 1 终止。
- `.env.example` — 环境变量键名清单与示例值。

## 3. 架构与约定

### 3.1 优先级模型

代码中明确声明并实现：**环境变量 > 配置文件 > 内置默认值**（见 `internal/config/config.go` 包注释与 `Load` 流程）。

具体顺序：
1. `setDefaults(v)` 写入 Viper 默认值（`server.mode=debug`、`server.port=8080`、`log.level=info`、`payment.default_channel=mock`、`channels.wechatpay.enabled=false` 等）。
2. `bindEnvs(v)` 将每个配置键与一个固定环境变量名一一绑定（如 `server.mode` → `PAYMENT_SERVER_MODE`、`channels.wechatpay.app_id` → `WECHATPAY_APP_ID`）。
3. 若传入路径为空，则依次尝试 `PAYMENT_CONFIG` 环境变量与 `DefaultPath = "configs/config.yaml"`。
4. 仅当配置文件存在时才调用 `ReadInConfig`；文件缺失不会报错，全部走默认值——容器只挂环境变量也能启动。
5. `Unmarshal` 后对 `WechatPayConfig.APIv3Key` 单独用 `os.Getenv(EnvWechatPayAPIv3Key)` 赋值，不走 Viper。
6. `normalize()` 统一大小写与空白（Mode/Level/Format/DefaultChannel 转小写，NotifyBaseURL 去尾部 `/`）。
7. `Validate()` 执行业务级合法性检查。

### 3.2 敏感信息隔离策略

- `WechatPayConfig.APIv3Key` 字段使用 ``mapstructure:"-"`` 标记，**不出现在 YAML 中**，只能从环境变量 `WECHATPAY_API_V3_KEY` 注入。
- 代码注释强调“从源头上避免密钥被误提交进代码仓库”，审计时一目了然。
- `configs/config.yaml` 顶部注释也声明“APIv3 密钥等敏感项只从环境变量读取”。

### 3.3 环境变量命名规范

通过 `bindEnvs` 中的映射表集中维护，遵循以下规则：
- 非微信支付项统一前缀 `PAYMENT_` + 配置键大写（如 `PAYMENT_SERVER_MODE`、`PAYMENT_LOG_FORMAT`、`PAYMENT_NOTIFY_BASE_URL`）。
- 微信支付渠道项统一前缀 `WECHATPAY_`（如 `WECHATPAY_ENABLED`、`WECHATPAY_APP_ID`、`WECHATPAY_MCH_CERT_SERIAL_NO`）。
- 不使用 Viper 的 `AutomaticEnv`，因为注释指出其键名推导规则隐蔽，“配置层级一变就可能悄悄失效”，而支付配置读错的后果是资金问题。

### 3.4 配置校验（启动阶段拦截）

`Validate()` 在进程启动时执行，原则是「宁可启动失败，不可带病运行」：
- `server.mode` 必须为 `debug` / `release` / `test` 之一。
- `server.port` 必须在 1–65535 之间。
- `server.read_timeout` / `write_timeout` / `shutdown_timeout` 必须大于 0。
- `log.format` 必须为 `console` / `json`。
- `payment.default_channel` 不能为空。
- `release` 模式下 `payment.notify_base_url` 必须以 `https://` 开头。
- `WechatPayConfig.validate`：当 `enabled=true` 时要求 `app_id`、`mch_id`、`private_key_path`、`WECHATPAY_API_V3_KEY` 均非空，且 APIv3Key 长度必须为 32；证书模式（`mch_cert_serial_no`）与公钥模式（`public_key_id` + `public_key_path`）二选一；生产模式下还会检查私钥/公钥文件是否可读。

### 3.5 配置文件路径覆盖

三种方式（按优先级）：
1. 命令行 `-config <path>`。
2. 环境变量 `PAYMENT_CONFIG`。
3. 默认 `configs/config.yaml`。

## 4. 约定与约束

- **约定**：新增配置项需在 `Config` 结构体中添加字段、在 `setDefaults` 中提供默认值、在 `bindEnvs` 中注册环境变量映射、在 `normalize` 中做大小写/空白处理、在 `Validate` 中做合法性检查。这些位置在现有代码中形成一致的扩展点。
- **约束（由代码强制）**：
  - `APIv3Key` 不允许出现在 YAML 中（`mapstructure:"-"`），只能通过 `WECHATPAY_API_V3_KEY` 环境变量注入。
  - `server.mode` 仅允许 `debug` / `release` / `test` 三个取值。
  - `log.format` 仅允许 `console` / `json`。
  - `release` 模式下回调地址必须是 HTTPS。
  - 启用微信支付时必须同时满足 app_id、mch_id、私钥路径、APIv3Key 长度 32、以及证书模式或公钥模式至少一组凭据。
  - 配置文件缺失不是错误，服务会以默认值启动。
- **约束（由文档/注释声明）**：
  - `configs/config.yaml` 是模板，不得提交真实密钥。
  - `.env.example` 仅供键名参考，实际 `.env` 已被 `.gitignore` 忽略，不应入库。
  - 时长类配置使用 Go `time.Duration` 字符串（如 `15s`、`1m30s`、`500ms`）。
  - `server.shutdown_timeout` 必须小于容器编排的 `terminationGracePeriodSeconds`，否则优雅关闭形同虚设。
  - 回调地址必须公网可达且域名完成 ICP 备案（微信支付要求）。