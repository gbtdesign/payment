---
kind: configuration_system
name: 基于 Viper 的分层配置加载与密钥隔离系统
category: configuration_system
scope:
    - '**'
source_files:
    - internal/config/config.go
    - configs/config.yaml
    - .env.example
    - cmd/server/main.go
---

## 1. 使用的框架与工具

- 使用 `github.com/spf13/viper` 作为统一的配置加载器，负责 YAML 文件解析、默认值注入与环境变量绑定。
- 配置文件采用 YAML 格式，默认路径为 `configs/config.yaml`。
- 环境变量通过显式 `BindEnv` 映射到 viper key，不使用 `AutomaticEnv`。
- 敏感项（微信支付 APIv3 密钥）绕过 viper，直接通过 `os.Getenv` 读取。

## 2. 核心文件

- `internal/config/config.go`：配置结构定义、默认值、环境变量映射、归一化与校验逻辑，是配置系统的唯一实现。
- `configs/config.yaml`：可提交至仓库的配置模板，不含真实密钥。
- `.env.example`：环境变量键名与示例值的模板，说明部署时如何注入。
- `cmd/server/main.go`：进程入口，调用 `config.Load` 并在失败时直接退出（返回码 1）。

## 3. 架构与设计约定

### 3.1 优先级模型

代码注释明确声明：**环境变量 > 配置文件 > 内置默认值**。

加载流程（`Load`）：
1. 确定配置文件路径：命令行 `-config` 参数 → 环境变量 `PAYMENT_CONFIG` → 常量 `DefaultPath = "configs/config.yaml"`。
2. 创建 viper 实例，依次执行 `setDefaults` → `bindEnvs` → `ReadInConfig`（仅当文件存在时才读，缺失不报错）→ `Unmarshal`。
3. 单独用 `os.Getenv("WECHATPAY_API_V3_KEY")` 注入 APIv3 密钥，不走 viper。
4. 调用 `normalize()` 统一大小写与空白，再调用 `Validate()` 做启动期校验。

### 3.2 配置结构分层

```text
Config
├── ServerConfig   (server.mode/host/port/read_timeout/write_timeout/shutdown_timeout)
├── LogConfig      (log.level/format)
├── PaymentConfig  (payment.default_channel/notify_base_url/out_trade_no_prefix/payment_no_prefix)
└── ChannelsConfig
    └── WechatPayConfig (channels.wechatpay.*)
```

每个顶层字段都通过 `mapstructure:"..."` 标签与 YAML 键对应。

### 3.3 密钥隔离策略

- `WechatPayConfig.APIv3Key` 的 mapstructure 标签为 `"-"`，即从 YAML 中忽略该字段。
- 该字段只通过 `os.Getenv(EnvWechatPayAPIv3Key)` 赋值，代码注释强调“在代码层面不存在从 yaml 读到密钥的可能”。
- `Validate()` 对 APIv3 密钥做了长度校验（固定 32 位）。
- 证书模式 (`mch_cert_serial_no` + `private_key_path`) 与公钥模式 (`public_key_id` + `public_key_path`) 二选一，至少提供一套验签凭据。
- 生产模式下还会检查私钥/公钥文件是否可读；开发/CI 环境跳过此检查。

### 3.4 默认值与归一化

`setDefaults` 为所有非密钥配置项提供默认值，保证容器里只挂环境变量也能启动。`normalize` 统一将 `mode`、`level`、`format`、`default_channel` 转为小写并 trim 空白，同时去掉 `NotifyBaseURL` 末尾的 `/`。

### 3.5 启动期校验

`Validate()` 在进程启动阶段拦截非法配置，原则是“宁可启动失败，不可带病运行”：
- `server.mode` 必须是 `debug` / `release` / `test` 之一。
- `server.port` 必须在 1–65535。
- 三个 timeout 必须大于 0。
- `log.format` 必须是 `console` / `json`。
- `payment.default_channel` 不能为空。
- release 模式下 `payment.notify_base_url` 必须以 `https://` 开头。
- 微信支付启用时必须补齐 AppID、MchID、PrivateKeyPath、APIv3Key 以及一组验签凭据。

### 3.6 环境变量命名规范

| 配置路径 | 环境变量 |
|---|---|
| `server.*` | `PAYMENT_SERVER_*` |
| `log.*` | `PAYMENT_LOG_*` |
| `payment.*` | `PAYMENT_*` |
| `channels.wechatpay.*` | `WECHATPAY_*` |
| 配置文件路径 | `PAYMENT_CONFIG` |
| APIv3 密钥 | `WECHATPAY_API_V3_KEY` |

所有映射集中在 `bindEnvs` 的 map 中，便于审计。

## 4. 约定与约束

- **配置文件缺失不视为错误**：`Load` 仅在文件存在时调用 `ReadInConfig`，否则全部走默认值——这是为了支持“容器只挂环境变量”的部署方式。
- **禁止把密钥写入 YAML**：`APIv3Key` 的 mapstructure 标签为 `-`，且 `Validate` 会拒绝空值或长度不为 32 的值。
- **禁止使用 viper 的 `AutomaticEnv`**：代码注释解释原因——支付配置读错的后果是资金问题，逐条 `BindEnv` 让映射关系可直接审计。
- **回调地址在生产环境必须是 HTTPS**：`Validate` 在 `IsRelease()` 为真时强制要求 `NotifyBaseURL` 以 `https://` 开头。
- **证书文件只在 release 模式校验存在性**：避免阻塞本地开发与 CI。
- **服务端口、超时等关键参数在启动阶段校验**，而不是第一次请求时才暴露错误。
- `main.go` 中 `config.Load` 失败直接 `fmt.Fprintf(os.Stderr, ...)` 并返回 1，确保配置错误不会进入应用初始化阶段。
- `configs/config.yaml` 顶部注释明确“本文件不含任何真实密钥，可直接提交进仓库”，`.env.example` 同样声明“严禁写入任何真实密钥”。