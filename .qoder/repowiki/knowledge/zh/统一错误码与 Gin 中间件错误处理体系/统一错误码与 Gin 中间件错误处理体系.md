---
kind: error_handling
name: 统一错误码与 Gin 中间件错误处理体系
category: error_handling
scope:
    - '**'
source_files:
    - internal/errcode/errcode.go
    - internal/middleware/recovery.go
    - internal/pkg/response/response.go
    - internal/handler/handler.go
    - internal/router/router.go
    - internal/pkg/logger/logger.go
---

## 1. 总体方案

本项目基于 Go + Gin，构建了一套**业务错误码 + 自定义 panic 恢复中间件 + 统一响应体**的错误处理体系：

- 所有业务异常以 `internal/errcode.Error` 形式表达，每个实例绑定一个整数错误码、一段对外文案和一个 HTTP 状态码。
- 非业务错误（数据库、渠道 SDK、网络等）通过 `errcode.From` 归一为 `ErrInternal`，并挂到私有字段 `cause` 上，避免把内部实现细节泄漏给客户端。
- 所有 HTTP 失败响应由 `internal/pkg/response` 统一输出 `{"code","message","data","request_id"}` 结构；panic 由 `internal/middleware.Recovery` 捕获后返回统一的 JSON 500。
- 日志使用 `internal/pkg/logger`（基于 zap），按 request_id 串联一次请求的所有日志，panic 与 5xx 错误记 error 级别并附带堆栈，4xx 记 warn 级别但不带底层原因。

## 2. 关键文件

| 文件 | 职责 |
|---|---|
| `internal/errcode/errcode.go` | 定义 `Error` 类型、分段错误码常量（10xxx/20xxx/30xxx/40xxx）、`From`/`Code` 归一化函数 |
| `internal/middleware/recovery.go` | 自定义 `Recovery` 中间件、`NoRoute`/`NoMethod` 处理器、broken pipe 识别 |
| `internal/pkg/response/response.go` | 统一响应体 `Body`、`OK`/`Created`/`Fail`/`Abort` 等写入方法 |
| `internal/handler/handler.go` | 参数解析与校验错误到 errcode 的映射（`bindJSON`、`pathParam`、`amountParseError`） |
| `internal/router/router.go` | 中间件装配顺序、`HandleMethodNotAllowed` 开关、全局路由注册 |
| `internal/pkg/logger/logger.go` | 基于 zap 的结构化日志、request_id 注入、`Sync` 刷新 |

## 3. 架构与约定

### 3.1 错误码分段规则

`internal/errcode/errcode.go` 在包注释中明确分段：

- `0` — 成功
- `10xxx` — 通用错误（参数、未找到、内部错误、不支持的操作、方法不允许）
- `20xxx` — 订单相关
- `30xxx` — 支付相关
- `40xxx` — 支付渠道相关

每个错误码同时绑定一个 `net/http` 状态码，例如 `ErrOrderAlreadyPaid` → 409 Conflict，`ErrChannelCallFailed` → 502 Bad Gateway。HTTP 状态码供网关/监控/重试策略判断，业务码供客户端做精细分支，二者不可互相替代（见 response 包注释）。

### 3.2 `errcode.Error` 类型设计

```go
type Error struct {
    Code       int
    Msg        string
    HTTPStatus int
    cause error // 私有，仅用于日志与调试
}
```

- `Error()` 输出包含 cause 的完整字符串，便于排查。
- `Unwrap()` 让 `errors.Is` / `errors.As` 能穿透到 cause。
- `Is(target)` 比较时只看 `Code`，使 `WithCause` / `WithMsg` 产生的新实例仍能与原始包级变量相等。
- `WithCause` / `WithMsg` 都先复制再修改，因为包级错误变量是共享的，直接就地修改会在并发请求间串数据（代码注释明确说明这是“极难排查的隐患”）。
- `From(err)` 是唯一的入口：若传入已是 `*Error` 则原样返回，否则包装为 `ErrInternal.WithCause(err)`。

### 3.3 Panic 恢复策略

`internal/middleware/recovery.go` 替换了 Gin 默认的 `gin.Recovery()`，原因有两点（代码注释）：

1. Gin 默认返回 HTML 错误页，与本服务的 JSON 契约不一致，客户端解析会失败。
2. Gin 默认把堆栈打到 stdout，无法与 `request_id` 关联。

对支付服务而言，这一层是最后防线：回调处理中的未捕获 panic 会导致渠道收不到应答而持续重试，本地状态可能已推进一半。

特殊处理：
- `isBrokenPipe` 识别 `syscall.EPIPE` / `syscall.ECONNRESET` 或文本中包含 `broken pipe` / `connection reset by peer` 的情况，只打日志并 `c.Abort()`，不写响应体，避免把“客户端断开”误报成服务端 panic。
- 如果响应已经开始写入（如流式输出中途 panic），无法再改写状态码，只能 `c.Abort()`。
- 对外只暴露 `ErrInternal`，panic 的具体内容不进响应体，防止泄露数据库连接串、内部路径等敏感信息。

### 3.4 未匹配路由与方法

- `NoRoute`：返回 `ErrNotFound`，消息附加 `接口不存在: <Method> <Path>`。
- `NoMethod`：启用 `engine.HandleMethodNotAllowed = true`，根据已注册路由表计算允许的方法列表，按 RFC 9110 要求设置 `Allow` 头（仅在能准确推导时设置，宁可漏掉也不写错）。

### 3.5 参数校验错误分类

`handler.bindJSON` 区分三类失败（代码注释明确说明语义不同）：

| 场景 | 错误码 | 含义 |
|---|---|---|
| 请求体为空或 JSON 语法错误 | `ErrBadRequest` | 客户端要检查序列化 |
| 字段类型不匹配 | `ErrBadRequest` | 客户端要检查字段类型 |
| validator 规则不通过 | `ErrInvalidParam` | 客户端要提示用户改输入 |

validator 错误会被翻译成中文提示，字段名取自 JSON tag 而非 Go 字段名（例如前端看到的是 `subject` 而不是 `Subject`）。详情长度限制为 512 字节，超长截断。

### 3.6 日志分级约定

`response.Fail` 中：

- `HTTPStatus >= 500`：记 `logger.Error`，附带 `path`、`errcode`、原始 `error`。
- `HTTPStatus < 500`：记 `logger.Warn`，附带 `path`、`errcode`、`reason`（即 `e.Error()`），不带底层 cause，避免 4xx 参数错误淹没真正的服务端故障告警。

`logger` 包默认开启 `zap.AddStacktrace(zapcore.PanicLevel)`，panic 及以上级别自动附带堆栈。

### 3.7 中间件装配顺序

`router.New` 中固定顺序（注释强调不能随意调换）：

1. `RequestID` — 最先，后续所有日志依赖它串联。
2. `Logger` — 在 Recovery 之前，才能记录到 panic 请求的耗时与状态码。
3. `Recovery` — 最内层，保证捕获范围覆盖所有后续处理。
4. 可选 `CORS`。

Gin 引擎使用 `gin.New()` 而非 `gin.Default()`，避免自带 Logger/Recovery 与本包自定义中间件重复导致双份日志和双重 panic 恢复。

## 4. 观察到的约定与约束

- **业务错误必须走 `errcode` 包定义的常量**，不要裸传 `fmt.Errorf` 给上层 handler；非业务错误通过 `errcode.From` 归一。
- **对外响应一律经 `response.OK` / `response.Created` / `response.Fail` / `response.Abort`**，禁止 handler 直接调用 `c.JSON` 输出业务结果。
- **回调接口例外**：微信等渠道回调必须按渠道自身规范应答，不走统一响应封装（见 `response` 包注释与 `handler/callback_handler.go`）。
- **panic 内容不得进入响应体**：recovery 中间件显式只返回 `ErrInternal`，具体 panic 值只进日志。
- **broken pipe / connection reset by peer 不算 panic**：recovery 中间件单独识别并只打日志，不产生告警噪音。
- **405 必须带 Allow 头**：遵循 RFC 9110，但仅在能准确推导允许方法时才设置，宁可不写也不误导。
- **错误码与 HTTP 状态码必须一致**：每个 `errcode` 常量同时指定两者，由 `response` 统一消费，避免 handler 各自决定状态码。
- **日志级别按 HTTP 状态码区分**：5xx 记 error，4xx 记 warn，这是 response 包的实现约定。
- **request_id 贯穿全链路**：从 middleware 注入 context，logger 取出来作为结构化字段，response 写入响应体 `request_id` 字段。
- **配置错误（非法 level 等）应报错而非静默回退**：logger 的 `parseLevel` 显式返回错误，注释说明“静默回退会浪费大量时间”。