# 微信支付对接准备：Go 服务骨架

## Summary

在空仓库 `/home/payment` 中搭建可编译、可运行、可测试的服务骨架。核心目标不是写业务，而是**把接缝（seam）设计好**：所有渠道相关逻辑收敛到 `internal/channel.Gateway` 接口之后，将来接微信支付只需新增一个 `wechatpay` 实现并注册，`service`/`handler` 层零改动。

当前用 `mock` 渠道实现 + 内存仓储，使整条链路今天就能端到端跑通并验证。

## 技术选型（已确认）

| 项 | 选择 |
|---|---|
| 语言 | Go（本机 go1.26.4，`/usr/local/go/bin`，未进 PATH） |
| module path | `payment`（短路径） |
| Web 框架 | Gin |
| 分层 | handler -> service -> repository（经典三层） |
| 存储 | 仅定义 Repository 接口 + 内存实现（`sync.RWMutex` + `map`），不落库 |
| 配置 | Viper（yaml + 环境变量覆盖） |
| 日志 | zap |
| 支付场景 | 微信支付 JSAPI / 小程序（需 openid） |
| 微信支付 SDK | 本次不引入，仅在 `docs` 与占位包中说明 |

## 目录结构

```
payment/
├── cmd/server/main.go                  # 入口：加载配置、装配、优雅退出
├── configs/config.yaml                 # 配置模板（不含任何真实密钥）
├── certs/README.md                     # 证书存放说明（目录整体 gitignore）
├── docs/wechatpay-checklist.md         # 对接前业务准备清单
├── internal/
│   ├── app/app.go                      # 依赖装配 + 生命周期，返回 *gin.Engine
│   ├── config/config.go                # 配置结构体 + Load()
│   ├── model/{order.go,payment.go}     # 领域模型与状态枚举
│   ├── dto/{order.go,payment.go}       # 请求/响应 DTO + binding tag
│   ├── repository/
│   │   ├── repository.go               # OrderRepository / PaymentRepository 接口
│   │   ├── memory_order.go             # 内存实现
│   │   └── memory_payment.go
│   ├── service/{order_service.go,payment_service.go}
│   ├── handler/{order_handler.go,payment_handler.go,callback_handler.go,health.go,mock_trigger.go}
│   ├── router/router.go                # 路由注册 + 中间件挂载
│   ├── middleware/{requestid.go,logger.go,recovery.go,cors.go}
│   ├── errcode/errcode.go              # 业务错误码
│   ├── channel/
│   │   ├── gateway.go                  # Gateway 接口 + 渠道无关 DTO
│   │   ├── registry.go                 # 渠道注册表
│   │   ├── mock/mock_gateway.go        # 假实现，跑通全流程
│   │   └── wechatpay/doc.go            # 占位包，注释说明将来实现什么
│   └── pkg/
│       ├── response/response.go        # 统一响应体
│       ├── logger/logger.go            # zap 封装
│       ├── money/money.go              # 元/分转换（禁用 float64）
│       └── idgen/idgen.go              # 商户订单号生成
├── .env.example
├── .gitignore                          # 追加 certs/、*.pem、.env
├── Makefile
├── Dockerfile
├── go.mod
└── README.md                           # 更新为目录说明 + 启动方式
```

单元测试与被测代码同目录（`*_test.go`），不建独立 `test/` 目录。

## 核心抽象：internal/channel/gateway.go

这是本次骨架的重点。接口按微信支付 APIv3 的能力集设计，但字段保持渠道无关，将来加支付宝也不用改调用方。

```go
type Code string

const (
    CodeMock      Code = "mock"
    CodeWechatPay Code = "wechatpay"   // 预留，本次无实现
)

type TradeType string

const TradeTypeJSAPI TradeType = "JSAPI"

// PrepayRequest 统一下单请求。金额单位为「分」。
type PrepayRequest struct {
    OutTradeNo  string
    Description string
    AmountTotal int64
    Currency    string // 默认 CNY
    Attach      string
    NotifyURL   string
    TradeType   TradeType
    PayerOpenID string // JSAPI 必填
    ClientIP    string
}

// InvokeParams 前端 wx.requestPayment / WeixinJSBridge 所需参数
type InvokeParams struct {
    AppID     string `json:"appId"`
    TimeStamp string `json:"timeStamp"`
    NonceStr  string `json:"nonceStr"`
    Package   string `json:"package"`
    SignType  string `json:"signType"`
    PaySign   string `json:"paySign"`
}

type PrepayResult struct {
    Channel      Code
    OutTradeNo   string
    PrepayID     string
    InvokeParams *InvokeParams
}

// NotifyPayload 回调解析后的统一结构，也复用于主动查单的返回
type NotifyPayload struct {
    OutTradeNo       string
    TransactionID    string
    TradeState       string
    TradeStateDesc   string
    AmountTotal      int64
    AmountPayerTotal int64
    Currency         string
    SuccessTime      time.Time
    OpenID           string
    Raw              []byte
}

type RefundRequest struct {
    OutTradeNo  string
    OutRefundNo string
    Refund      int64
    Total       int64
    Reason      string
    NotifyURL   string
}

type RefundResult struct {
    RefundID    string
    OutRefundNo string
    Status      string
}

// Gateway 支付渠道网关。微信支付后续实现此接口即可接入。
type Gateway interface {
    Code() Code
    Prepay(ctx context.Context, req PrepayRequest) (*PrepayResult, error)
    QueryOrder(ctx context.Context, outTradeNo string) (*NotifyPayload, error)
    CloseOrder(ctx context.Context, outTradeNo string) error
    ParseNotify(ctx context.Context, r *http.Request) (*NotifyPayload, error)
    Refund(ctx context.Context, req RefundRequest) (*RefundResult, error)
}
```

`registry.go` 提供 `Register(Gateway)` / `Get(Code) (Gateway, error)`，由 `internal/app` 在启动时按配置注册。

`mock/mock_gateway.go` 实现全部方法：`Prepay` 返回伪造的 `prepay_id` 与 `InvokeParams`（`SignType=RSA`，`PaySign` 为固定占位串）；`QueryOrder` 返回内部记录的状态；`ParseNotify` 直接从 JSON body 解析（不验签）。

`wechatpay/doc.go` 只有包注释，写明将来需要：`core.NewClient` + `option.WithWechatPayAutoAuthCipher`（或新商户的公钥模式 `WithWechatPayPublicKeyAuthCipher`）、`jsapi.JsapiApiService.Prepay`、`notify.Handler` 做验签解密，以及四个必备参数（mchID、商户证书序列号、APIv3 密钥、`apiclient_key.pem`）。

## 领域模型与状态机

`model.Order`：`OutTradeNo`、`Subject`、`AmountTotal int64`（分）、`Currency`、`Channel`、`Status`、`OpenID`、`ClientIP`、`CreatedAt`、`UpdatedAt`、`PaidAt`。

状态流转：`CREATED -> PAYING -> PAID`，分支 `CLOSED`、`REFUNDED`。

`model.Payment`（支付流水，一个订单可多次尝试）：`PaymentNo`、`OutTradeNo`、`Channel`、`TradeType`、`PrepayID`、`TransactionID`、`AmountTotal`、`Status`、`TradeStateDesc`、`SuccessTime`。

状态流转：`INIT -> PREPAID -> SUCCESS`，分支 `FAILED`、`CLOSED`。

`PaymentService` 关键约束：
- 回调处理必须**幂等**——先按 `OutTradeNo` 查 Order，若已是 `PAID` 直接返回成功，不重复推进状态。
- 金额校验：回调中的 `AmountTotal` 必须与订单金额一致，不一致记 error 日志并拒绝置为已支付。
- 状态只能前进，禁止从 `PAID` 回退。

## HTTP 接口

统一前缀 `/api/v1`：

| 方法 | 路径 | 说明 |
|---|---|---|
| POST | `/api/v1/orders` | 创建订单 |
| GET | `/api/v1/orders/:out_trade_no` | 查询订单 |
| POST | `/api/v1/payments/prepay` | 发起支付，返回小程序调起参数 |
| GET | `/api/v1/payments/:out_trade_no` | 查询支付状态（前端轮询） |
| POST | `/api/v1/callbacks/wechatpay` | 支付结果回调入口 |
| POST | `/api/v1/mock/pay-success` | 仅非 release 模式注册，模拟支付成功回调 |
| GET | `/healthz` / `/readyz` | 存活 / 就绪探针 |

`mock/pay-success` 是为了在没有微信的情况下能手动推进状态机，验证幂等和金额校验逻辑。生产模式（`server.mode=release`）下不注册该路由。

## 中间件与统一响应

`middleware`：`RequestID`（无则生成 UUID，写入 `context` 与响应头 `X-Request-ID`）、`Logger`（zap，记录 method/path/status/latency/request_id，替换 Gin 默认 Logger）、`Recovery`（zap 记录堆栈，返回 500 统一响应而非 Gin 默认 HTML）、`CORS`。

`pkg/response`：

```go
type Body struct {
    Code      int    `json:"code"`
    Message   string `json:"message"`
    Data      any    `json:"data,omitempty"`
    RequestID string `json:"request_id,omitempty"`
}
```

成功 `code=0`；失败沿用 `errcode`。`errcode` 分段：`10xxx` 通用（参数、未找到、内部错误）、`20xxx` 订单、`30xxx` 支付、`40xxx` 渠道。每个错误码绑定 HTTP status。

注意：微信支付回调要求**按微信规范返回**（成功返回 HTTP 200 + `{"code":"SUCCESS","message":"成功"}`，失败返回 4xx/5xx + `{"code":"FAIL",...}`），因此 `callback_handler` 不走统一 `response` 封装，单独处理。这一点在代码注释中写明。

## 配置与安全

`configs/config.yaml`：

```yaml
server:
  mode: debug            # debug | release | test
  host: 0.0.0.0
  port: 8080
  read_timeout: 15s
  write_timeout: 15s
  shutdown_timeout: 10s

log:
  level: info
  format: console        # console | json

payment:
  default_channel: mock
  notify_base_url: https://pay.example.com   # 必须 HTTPS + ICP 备案域名

channels:
  wechatpay:
    enabled: false
    app_id: ""
    mch_id: ""
    mch_cert_serial_no: ""
    private_key_path: ""
    notify_path: /api/v1/callbacks/wechatpay
```

密钥类配置（`api_v3_key`）**只从环境变量读取**，yaml 中不出现该字段。`config.Load()` 用 Viper 的 `AutomaticEnv` + `SetEnvPrefix` 绑定，并显式用 `os.Getenv` 读取 `WECHATPAY_API_V3_KEY`。

`.env.example` 列出全部待填项：`WECHATPAY_APP_ID`、`WECHATPAY_MCH_ID`、`WECHATPAY_MCH_CERT_SERIAL_NO`、`WECHATPAY_API_V3_KEY`、`WECHATPAY_PRIVATE_KEY_PATH`。

`.gitignore` 追加：
```
# 支付证书与密钥，严禁提交
certs/
*.pem
*.p12
*.key
.env
.env.*
!.env.example
```

这是支付项目最关键的一条防护——`apiclient_key.pem` 一旦进仓库等于商户资金账户失守。`certs/README.md` 说明该目录用途及证书获取途径。

## 工具包

`pkg/money`：金额一律 `int64` 存「分」。`YuanToFen(string) (int64, error)` 用字符串/`math/big` 解析，**禁止经过 float64**（避免 `19.99 * 100 = 1998.9999...` 这类经典事故）；`FenToYuan(int64) string`。

`pkg/idgen`：`OutTradeNo(prefix string) string`，格式为 `前缀 + yyyyMMddHHmmss + 8 位 crypto/rand 随机字符`，长度 <= 32（微信支付 `out_trade_no` 上限），仅含数字与字母。

`pkg/logger`：zap 封装，提供 `L(ctx)` 从 context 取带 request_id 的 logger。

## 入口与生命周期

`cmd/server/main.go`：加载配置 -> 初始化日志 -> 调 `app.New(cfg)` 装配 -> 启动 `http.Server` -> 监听 `SIGINT/SIGTERM` -> `Shutdown(ctx)` 优雅退出（超时由 `shutdown_timeout` 控制）。优雅退出对支付服务是必须的，避免回调请求被硬切断导致掉单。

`internal/app/app.go`：手工装配（不引入 wire），按配置注册渠道到 registry，构造 repository -> service -> handler -> router，返回 `*gin.Engine`。将来换 MySQL 只需在此处替换 repository 实现。

## 构建与工程化

`Makefile` 目标：`tidy`、`build`、`run`、`test`（`go test ./... -race -cover`）、`fmt`、`vet`、`lint`（golangci-lint，未安装则跳过并提示）、`docker-build`。所有目标内部用 `GO := $(shell command -v go 2>/dev/null || echo /usr/local/go/bin/go)` 兼容 Go 不在 PATH 的现状。

`Dockerfile`：多阶段构建，`golang:1.26-alpine` 编译 -> `alpine:3.20` 运行，非 root 用户，暴露 8080。

## 文档产出

`docs/wechatpay-checklist.md` 写清对接前的业务侧准备（这是代码之外必须完成的事）：

1. 注册并认证微信支付商户号
2. 公众号/小程序 AppID 与商户号绑定
3. 商户平台开通 JSAPI 支付产品
4. 配置支付授权目录（公众号网页支付需要，路径需精确到目录）
5. 申请 API 证书，下载 `apiclient_key.pem`；或新商户使用「微信支付公钥模式」
6. 设置 APIv3 密钥（32 位）
7. 下载平台证书 / 微信支付公钥，用于回调验签
8. 准备可公网访问的 HTTPS 回调域名（必须 ICP 备案）
9. 打通 openid 获取链路：公众号网页授权 `snsapi_base`，或小程序 `wx.login` -> `code2Session`
10. 本地联调需要内网穿透工具（回调必须公网可达）

`README.md` 更新为：项目定位、目录结构说明、快速启动、当前 mock 模式的使用方式、下一步接入微信支付的改动点清单。

## Test Plan

- `internal/pkg/money/money_test.go`：元分转换边界（`19.99`、`0.01`、负数、超长小数、非法输入）
- `internal/pkg/idgen/idgen_test.go`：长度 <= 32、字符集合规、并发生成不重复
- `internal/channel/mock/mock_gateway_test.go`：Prepay/QueryOrder/ParseNotify 行为
- `internal/service/payment_service_test.go`：状态机推进、回调幂等（重复回调只生效一次）、金额不一致拒绝
- `internal/handler/handler_integration_test.go`：用 `app.New` 装配后走 `httptest`，覆盖「创建订单 -> prepay -> 模拟回调 -> 查询订单已支付」全链路，以及参数校验失败、订单不存在的错误响应

验收标准：
- `go build ./...` 通过
- `go vet ./...` 无输出
- `go test ./... -race` 全绿
- `make run` 后 `curl` 全链路接口可正常返回，`/healthz` 返回 200

## 不在本次范围

- 不引入 `github.com/wechatpay-apiv3/wechatpay-go`，不写任何真实签名/验签/加解密逻辑
- 不做数据库持久化、不写 migration（Repository 接口已就位，后续加 MySQL 实现即可）
- 不做退款接口的 HTTP 暴露（`Gateway.Refund` 接口保留，mock 返回未实现错误）
- 不做鉴权/签名中间件、限流、链路追踪
- 不做 openid 获取（公众号 OAuth / code2Session），本次由调用方直接传入

## Assumptions

- Go 未在 PATH 中，执行构建命令时用 `export PATH=$PATH:/usr/local/go/bin`；建议用户自行将该行加入 `~/.bashrc`（本次不修改用户 shell 配置）
- `proxy.golang.org` 与 `goproxy.cn` 均已验证可达，依赖拉取用默认 GOPROXY；若拉取超时则临时切 `GOPROXY=https://goproxy.cn,direct`
- `go.mod` 声明 `go 1.24`（低于本机 1.26.4，保证协作者兼容）
- 依赖仅四个：gin、viper、zap、testify（测试用）
- 服务监听 8080，单实例部署，暂不考虑分布式锁与多副本下内存仓储的一致性问题（内存实现仅用于跑通骨架）
- 币种固定 CNY，金额单位为分