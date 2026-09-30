---
kind: logging_system
name: 基于 zap 的结构化日志系统
category: logging_system
scope:
    - '**'
source_files:
    - internal/pkg/logger/logger.go
    - internal/middleware/logger.go
    - configs/config.yaml
---

## 1. 使用的框架与工具

- 日志库：`go.uber.org/zap` + `zapcore`，业务代码不直接依赖 zap 类型，而是通过 `internal/pkg/logger` 暴露的别名（`Field`、`String`、`Int`、`Error` 等）使用。
- 输出目标：直接写入 `os.Stderr`，由容器运行时收集 stdout/stderr，应用自身不落盘文件。
- 编码格式：支持 `console` 与 `json` 两种 encoder，默认走 JSON；时间采用 ISO8601，级别小写，调用者信息使用 ShortCallerEncoder。
- 中间件层：自定义 Gin 访问日志中间件替换默认的 `gin.Logger()`，输出结构化字段而非彩色文本。

## 2. 关键文件

- `internal/pkg/logger/logger.go` — 全局 Logger 装配、context 透传 request_id、level/format 解析、Sync 刷新。
- `internal/middleware/logger.go` — 请求级访问日志中间件，按状态码选择 debug/info/warn/error 级别。
- `configs/config.yaml` — `log.level` / `log.format` 配置项定义。

## 3. 架构与设计决策

### 3.1 全局 Logger 装配

- `logger.New(cfg)` 根据 `Config{Level, Format}` 构造 `*zap.Logger`，并通过 `logger.SetBase(l)` 设置到全局 `atomic.Pointer[zap.Logger]` 中。
- 使用 `atomic.Pointer` 而非普通变量，因为日志是全局共享状态，装配阶段写入、请求阶段并发读取，避免 `-race` 数据竞争。
- 未初始化时 `baseLogger()` 返回 `zap.NewNop()`，业务代码永远不必判空。

### 3.2 Context 关联的 Logger

- `logger.WithRequestID(ctx, id)` 将 `request_id` 写入 context。
- `logger.L(ctx)` 每次调用都基于 base logger 用 `With(String("request_id", id))` 派生带该字段的 logger，而不是把 `*zap.Logger` 存进 context。注释明确说明：前者多一次对象分配但语义清晰，后者会漏掉未经中间件的调用路径（定时任务、启动阶段）。
- 配套提供 `logger.RequestIDFrom(ctx)` 从 context 取回 request_id。

### 3.3 日志级别策略

- 合法值：`debug` / `info` / `warn` / `error`，非法值通过 `parseLevel` 返回错误（不是静默回退），错误消息为 `"logger: 非法的日志级别 <value>，可选 debug/info/warn/error"`。
- 访问日志中间件按 HTTP 状态码分级：
  - 健康探针 `/healthz`、`/readyz` → `Debug`
  - `>= 500` → `Error`
  - `>= 400` → `Warn`
  - 其他 → `Info`
- 健康探针单独降级为 Debug，避免 K8s 每秒探测淹没真正有价值的支付日志。

### 3.4 结构化字段约定

访问日志固定字段：`status`、`method`、`path`、`latency`、`client_ip`、`user_agent`、`route`（路由模板而非真实 URL）、`query`（仅非空时记录）、`errors`（仅存在 gin.Errors 时记录）。

### 3.5 安全与最小化原则

- 中间件刻意不记录请求体与响应体：回调报文含 openid 与加密数据，下单请求含金额与商品信息，落日志违反最小化原则且让日志体积随业务量线性膨胀。需要报文时在 service 层按需打点。
- User-Agent 截断至 256 字符，防止超长 UA 撑爆日志行。
- 记录路由模板（`c.FullPath()`）而非真实 URL，避免路径参数中的业务标识（如 `/orders/ORD2026...`）污染日志聚合。

### 3.6 进程退出清理

- `logger.Sync(l)` 在进程退出前必须调用，flush 缓冲日志；stderr 不支持 fsync，返回 error 被忽略。

## 4. 约定与约束

- **日志级别取值**：仅允许 `debug | info | warn | error`，非法值报错（见 `parseLevel` 及 `invalidLevelError`）。
- **日志格式取值**：仅允许 `console | json`，其余值按 json 处理（见 `New` 中 `strings.EqualFold` 分支）。
- **生产环境应使用 json 格式**：注释明确要求「生产环境应使用 json 格式：日志采集系统（Loki、ELK）解析结构化字段才能按 request_id / out_trade_no 做检索」。
- **不要直接 import zap**：业务包通过 `logger.String`、`logger.Int` 等别名使用，避免耦合具体实现（见 package 注释）。
- **不要在中间件记录请求体/响应体**：中间件注释明确禁止，敏感数据与体积膨胀是原因。
- **不要记录真实 URL 作为 route**：应使用 `c.FullPath()` 路由模板，避免业务标识污染聚合键。
- **进程退出前必须调用 `logger.Sync`**：注释声明「进程退出前必须调用」。
- **全局 Logger 通过 `SetBase` 注入**：业务代码通过 `logger.L(ctx)` 获取，不应自行持有全局实例。
- **健康检查路径降级为 Debug**：`/healthz`、`/readyz` 在中间件中显式匹配并记 Debug，避免探针淹没业务日志。