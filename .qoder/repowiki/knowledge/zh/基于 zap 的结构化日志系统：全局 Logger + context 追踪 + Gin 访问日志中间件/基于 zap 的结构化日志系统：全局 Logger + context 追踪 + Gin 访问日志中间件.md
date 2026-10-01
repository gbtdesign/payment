---
kind: logging_system
name: 基于 zap 的结构化日志系统：全局 Logger + context 追踪 + Gin 访问日志中间件
category: logging_system
scope:
    - '**'
source_files:
    - internal/pkg/logger/logger.go
    - internal/middleware/logger.go
    - configs/config.yaml
    - internal/app/app.go
---

## 1. 使用的框架与工具

- 日志库：**go.uber.org/zap**（含 `zapcore`）。
- HTTP 框架：Gin，其默认 `gin.Logger()` 被自定义中间件替换。
- 输出目标：直接写入 `os.Stderr`（容器环境下由运行时收集 stdout/stderr），不自行写文件。

## 2. 关键文件

| 文件 | 职责 |
|---|---|
| `internal/pkg/logger/logger.go` | zap 封装、全局 Logger 装配、context 传递 request_id、字段别名导出 |
| `internal/middleware/logger.go` | 替代 Gin 默认日志的访问日志中间件 |
| `configs/config.yaml` | `log.level` / `log.format` 配置项定义 |
| `internal/app/app.go` | 应用启动时构造 logger 并设为全局兜底实例 |

## 3. 架构与设计决策

### 3.1 全局 Logger + context 注入

- `logger.Config` 仅暴露 `Level`（`debug/info/warn/error`）和 `Format`（`console/json`）两个字段，业务代码无需感知 zap。
- `logger.New(cfg)` 根据 `Format` 选择 `ConsoleEncoder` 或 `JSONEncoder`；时间使用 ISO8601，级别小写编码，调用方信息保留。
- 通过 `atomic.Pointer[zap.Logger]` 保存全局 base logger，避免 `-race` 检测到的数据竞争；未初始化时回退到 `zap.NewNop()`，业务代码永远不必判空。
- `logger.SetBase(l)` 在 `app.New` 中调用一次，将装配好的 logger 注入全局。
- `logger.L(ctx)` 每次从 context 取出 `request_id`，并通过 `base.With(String("request_id", id))` 附加为结构化字段。注释明确说明：选择在 L 处 With 而非中间件存 *zap.Logger，是为了覆盖定时任务、启动阶段等未经中间件的调用路径。
- `WithRequestID(ctx, id)` / `RequestIDFrom(ctx)` 提供 request_id 的存取 API，由 `middleware/requestid.go`（不在本卡片范围）负责生成与注入。

### 3.2 结构化字段约定

- 常用字段构造函数以包级变量形式导出：`String`、`Int`、`Int64`、`Bool`、`Any`、`Error`、`Duration`、`ByteString`，调用方通过 `logger.String(...)` 等调用，避免业务包直接 import zap。
- 访问日志中间件记录的标准字段：`status`、`method`、`path`、`latency`、`client_ip`、`user_agent`、`query`（可选）、`route`（路由模板而非真实 URL，避免把订单号等路径参数落进日志）。错误列表以 `errors` 字段聚合。
- 业务层按上下文追加领域字段，例如 `channel`、`out_trade_no`（见 `handler/callback_handler.go`、`service/payment_service.go`）。

### 3.3 日志级别策略

- 非法 level 值会返回错误（`invalidLevelError`），而不是静默回退到 info，防止配置笔误导致排查困难。
- 访问日志中间件按 HTTP 状态码分级：
  - `/healthz`、`/readyz` → `Debug`
  - `>= 500` → `Error`（消息 "请求处理失败"）
  - `>= 400` → `Warn`（消息 "请求被拒绝"）
  - 其他 → `Info`（消息 "请求完成"）
- panic 及以上级别附带堆栈（`zap.AddStacktrace(zapcore.PanicLevel)`）。

### 3.4 生命周期管理

- `logger.Sync(l)` 在进程退出前调用，确保 zap core 缓冲刷新；`App.Close()` 中显式调用。
- 健康检查探针路径（`/healthz`、`/readyz`）单独用 Debug 级别记录，避免 K8s 每秒探测淹没业务日志。
- user_agent 长度限制为 256 字符，超长时截断并追加 `...`，防止 UA 撑爆日志行。

### 3.5 安全与最小化原则

- 访问日志中间件**刻意不记录请求体与响应体**：回调报文含 openid 与加密数据，下单请求含金额与商品信息，落日志违反最小化原则且使日志体积随业务量线性膨胀。需要报文时在 service 层按需打点。
- 只记录路由模板（`c.FullPath()`）而非真实 URL，避免把 `ORD2026...` 这类带业务标识的路径参数写进日志，影响日志聚合。

## 4. 约定与约束

- **生产环境应使用 json 格式**：`logger.New` 的注释明确指出，生产环境应使用 json 格式以便 Loki/ELK 解析结构化字段做检索；console 多行输出会让检索失效。（来源：`internal/pkg/logger/logger.go` 注释）
- **日志级别只能取 debug/info/warn/error**：`parseLevel` 对非法值报错，错误信息列出可选值。（来源：`internal/pkg/logger/logger.go` 中的 `invalidLevelError` 与 `parseLevel`）
- **进程退出前必须调用 Sync**：`logger.Sync` 与 `App.Close` 的注释都强调 flush 是必须的，否则最后几条日志会丢失。（来源：`internal/pkg/logger/logger.go` 与 `internal/app/app.go`）
- **不要直接依赖 zap**：业务代码统一通过 `logger.L(ctx).Info(...)` 调用，包内导出 Field 构造函数以避免业务包 import zap。（来源：`internal/pkg/logger/logger.go` 包注释）
- **不要在访问日志中记录请求/响应体**：中间件注释明确禁止，敏感数据与体积膨胀是原因。（来源：`internal/middleware/logger.go` 注释）
- **健康检查探针走 Debug 级别**：避免 K8s 每秒探测淹没业务日志。（来源：`internal/middleware/logger.go` 注释与实现）
- **日志输出到 stderr**：容器环境下由运行时收集，应用不自行写文件以避免轮转、磁盘占满等问题。（来源：`internal/pkg/logger/logger.go` 注释）
- **全局 Logger 通过 atomic.Pointer 无锁读取**：装配阶段写入、请求阶段并发读取，零值回退 Nop。（来源：`internal/pkg/logger/logger.go` 注释与实现）
