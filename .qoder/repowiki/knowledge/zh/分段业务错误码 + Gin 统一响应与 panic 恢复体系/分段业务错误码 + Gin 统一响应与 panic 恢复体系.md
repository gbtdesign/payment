---
kind: error_handling
name: 分段业务错误码 + Gin 统一响应与 panic 恢复体系
category: error_handling
scope:
    - '**'
source_files:
    - internal/errcode/errcode.go
    - internal/middleware/recovery.go
    - internal/pkg/response/response.go
    - internal/handler/handler.go
---

## 1. 采用的系统与模式

- **自定义业务错误类型**：`internal/errcode` 包定义 `Error` 结构体，每个业务错误是一个包级共享的 `*errcode.Error` 实例，携带三段信息：业务码（Code）、对外文案（Msg）、HTTP 状态码（HTTPStatus），并通过私有字段 `cause` 保留底层原因。
- **分段错误码规范**：0=成功；10xxx=通用错误（参数、未找到、内部错误）；20xxx=订单相关；30xxx=支付相关；40xxx=支付渠道相关。该规则在 `errcode.go` 的包注释中明确声明。
- **Gin 中间件链**：通过 `internal/middleware/recovery.go` 替换 Gin 默认的 `gin.Recovery()`，实现 panic 捕获、连接断开识别、统一 JSON 500 响应；同时提供 `NoRoute`（404）和 `NoMethod`（405，含 RFC 9110 要求的 Allow 头）。
- **统一响应封装**：`internal/pkg/response` 提供 `OK` / `Created` / `Fail` / `Abort` / `AbortWithStatus`，所有 HTTP 响应体遵循 `{"code":0,"message":"ok","data":...,"request_id":"..."}` 格式。
- **错误归一化入口**：`errcode.From(err)` 把任意 error 归一为 `*errcode.Error`，非业务错误（数据库驱动、渠道 SDK 等）统一映射为 `ErrInternal` 并挂上原始 cause，使 handler 层只需处理一种类型。

## 2. 关键文件与包

| 路径 | 职责 |
|---|---|
| `internal/errcode/errcode.go` | 业务错误码定义、`Error` 类型、`From`/`Code` 归一化函数 |
| `internal/middleware/recovery.go` | panic 恢复、404/405 统一响应、broken pipe 过滤 |
| `internal/pkg/response/response.go` | 统一 JSON 响应体 `Body`、`OK`/`Fail`/`Abort` 等写入方法 |
| `internal/handler/handler.go` | 请求解析校验错误分类（EOF / validator / JSON 语法）、金额解析错误翻译 |
| `internal/handler/callback_handler.go` | 渠道回调专用响应（不走统一封装，见 response 包注释说明） |
| `internal/pkg/money/money.go` | 金额解析错误（`ErrPrecisionLoss`/`ErrInvalidFormat`/`ErrOutOfRange`），被 handler 映射为业务错误码 |

## 3. 架构与设计决策

### 3.1 错误传播模型

```
handler 层 → errcode.* (业务语义) → service/repository → 外部依赖(数据库/渠道SDK)
```

- 领域层和业务层直接返回 `*errcode.Error` 或调用 `errcode.From` 包装第三方错误。
- handler 层只做「输入解析 → 业务码」的转换，不出现订单状态判断等业务逻辑（见 `handler.go` 包注释）。
- 响应层只消费 `errcode.Error`，通过 `e.HTTPStatus` 写 HTTP 状态码、通过 `e.Code` 写业务码，二者不可互相替代（response 包注释明确说明）。

### 3.2 对外文案与内部原因的隔离

`errcode.Error` 的 `Msg` 是面向客户端的固定文案，`cause` 仅用于日志和调试。`Error()` 输出包含 cause，但 `Msg` 不会泄露内部细节。`WithCause` 会复制错误再挂 cause，因为包级变量是共享的，就地修改会在并发请求间串数据（代码注释明确标注这是「极难排查的隐患」）。

### 3.3 panic 恢复策略

`middleware/recovery.go` 替换 Gin 默认 recovery，原因有两点（代码注释原文）：
1. Gin 默认返回 HTML 错误页，与本服务的 JSON 契约不一致，客户端解析会失败。
2. Gin 默认把堆栈打到 stdout，无法与 request_id 关联。

对支付服务而言它是最后防线：回调处理中的未捕获 panic 会导致渠道收不到应答而持续重试，本地状态可能已推进一半。

panic 值会被 `isBrokenPipe` 识别为 EPIPE/ECONNRESET 类连接断开错误，此时只记日志并 `c.Abort()`，避免把「客户端断开」误报成服务端 panic。

### 3.4 请求解析错误的细粒度分类

`bindJSON` 把三类失败区分开（handler.go 注释原文）：
- 请求体为空或 JSON 语法错误 → `ErrBadRequest`，客户端要检查序列化。
- 字段类型不匹配 → `ErrBadRequest`，客户端要检查字段类型。
- 校验规则不通过 → `ErrInvalidParam`，客户端要提示用户改输入。

validator 的错误文本会翻译成中文，字段名取 JSON tag 而非 Go 字段名（便于前端对照 payload）。

### 3.5 4xx vs 5xx 日志级别差异

`response.Fail` 根据 HTTP 状态码决定日志级别：5xx 用 `log.Error` 并带上底层原因；4xx 用 `log.Warn` 且不带原因，理由是「参数错误是客户端行为，大量记 error 会淹没真正的服务端故障告警」。

## 4. 约定与约束

- **业务错误必须来自 `internal/errcode`**：所有业务异常以包级 `*errcode.Error` 常量形式暴露，新增错误需按分段规则分配新码并在 `From` 可识别范围内。
- **HTTP 状态码与业务码并存**：response 包注释规定二者不可互相替代——前者供网关/监控/重试策略判断，后者供客户端做精细化业务分支。
- **回调接口例外**：渠道异步回调不按本包的统一响应格式应答，由 `callback_handler.go` 自行处理（response 包注释明确排除）。
- **panic 一律转 500**：recovery 中间件对外只暴露 `ErrInternal`，panic 的具体内容不进响应体，以防泄露数据库连接串、内部路径等敏感信息。
- **405 必须带 Allow 头**：`NoMethod` 严格遵循 RFC 9110，仅在能反推允许方法时写入 Allow 头；对带路径参数的路由宁可不写也不写错（注释原话）。
- **errors.Is / errors.As 兼容**：`Error.Is` 基于 Code 比较，`Unwrap` 暴露 cause，保证 `errors.Is(err, ErrOrderNotFound)` 即使在使用 `WithCause` 后仍有效（代码注释解释了设计动机）。
- **金额解析错误必须经 `amountParseError` 转换**：`money` 包的错误文本是给开发者看的，不能直接回给客户端，需映射为 `ErrOrderAmountInvalid` 的不同 Msg。
- **handler 层禁止业务分支**：handler 包注释明确要求「handler 里出现 if 判断订单状态或金额，就意味着业务逻辑漏到了接入层」，应下沉到 service 层。