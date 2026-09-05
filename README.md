# payment

Go + Gin 经典分层的支付服务骨架。用内存仓储跑通「下单 → 发起支付 → 渠道回调 → 查单」全流程，并把与支付渠道相关的差异全部收敛到 `channel.Gateway` 抽象层之后，为接入微信支付 JSAPI 预留好接缝。

## 当前状态

**已完成**：分层结构、领域状态机、内存仓储、渠道抽象层与 mock 实现、业务幂等与金额校验、HTTP 接入层、依赖装配、工程化脚本与文档。

**未完成**：真实微信支付对接（`internal/channel/wechatpay` 只有占位文档）、数据库持久化、单元与集成测试、鉴权与限流。

因此服务当前**只能以 `mock` 渠道运行**，且 `server.mode=release` 下会直接启动失败——release 模式刻意不注册 mock 渠道，而微信支付尚未实现，此时没有任何可用渠道。本地联调请保持默认的 `debug` 模式，原因会在启动错误里明确说明。

## 快速启动

```bash
# 本机 Go 安装在 /usr/local/go/bin，若未加入 PATH 需要先导出
export PATH=$PATH:/usr/local/go/bin

make build          # 编译到 bin/payment-server
./bin/payment-server -config configs/config.yaml
```

或者直接前台运行（Ctrl-C 触发优雅关闭）：

```bash
make run
```

看到日志 `服务启动 addr=0.0.0.0:8080 mode=debug default_channel=mock` 即表示就绪，可用 `curl localhost:8080/readyz` 确认。

Docker：

```bash
make docker-build
docker run -p 8080:8080 payment-server:latest
```

## 目录结构

```
cmd/server/              进程入口：解析参数、加载配置、启动 HTTP、优雅关闭
configs/config.yaml      配置模板，不含任何真实密钥
internal/
  app/                   装配层：按配置构造仓储/渠道/服务/handler 并串成 Gin 引擎
  channel/               渠道抽象层（本项目的核心接缝）
    gateway.go           Gateway 接口 + 渠道无关的 DTO（金额单位统一为「分」）
    registry.go          渠道注册表，按 Code 索引
    mock/                mock 实现，跑通链路与验证状态机
    wechatpay/           占位包，包注释写明接入方式与安全红线
  config/                Viper 加载 yaml + 环境变量覆盖，含启动期强校验
  dto/                   对外契约（金额用字符串「元」），与 model 严格分离
  errcode/               业务错误码，每个码绑定 HTTP status
  handler/               接入层：解析请求、调 service、统一封装响应
  middleware/            request_id / logger / recovery / cors / 404 / 405
  model/                 领域模型，状态机流转规则固化在此
  pkg/                   通用工具：money / idgen / logger / response
  repository/            仓储接口 + 内存实现
  router/                路由表，所有对外接口一览无余
  service/               业务层：OrderService / PaymentService
docs/                    微信支付接入前准备清单
certs/                   商户证书目录，除 README 外整体 gitignore
```

## 接口清单

| 方法 | 路径 | 说明 |
|---|---|---|
| `GET` | `/healthz` | 存活探针，不检查任何依赖 |
| `GET` | `/readyz` | 就绪探针，无可用渠道时返回 503 |
| `POST` | `/api/v1/orders` | 创建订单 |
| `GET` | `/api/v1/orders/:out_trade_no` | 查询订单 |
| `POST` | `/api/v1/payments/prepay` | 发起支付，返回前端调起参数 |
| `GET` | `/api/v1/payments/:out_trade_no` | 查询支付状态，`?sync=true` 时主动向渠道查单 |
| `POST` | `/api/v1/callbacks/wechatpay` | 渠道异步回调入口 |
| `POST` | `/api/v1/mock/pay-success` | 模拟一次支付成功回调，**仅非 release 模式注册** |

业务接口统一响应体：

```json
{"code": 0, "message": "ok", "data": {}, "request_id": "..."}
```

`code=0` 为成功，HTTP 状态码与业务码同时给出：前者供网关与监控判断，后者供客户端做业务分支。**渠道回调接口是唯一的例外**，它按微信支付的规范应答 `{"code":"SUCCESS"}` / `{"code":"FAIL"}`——若用本服务的统一响应体，微信会判定通知失败并按 `15s/15s/30s/3m/...` 持续重投。

## 全链路演示

```bash
BASE=http://localhost:8080

# 1. 下单。金额用字符串「元」，服务端转为 int64「分」存储
curl -s -X POST $BASE/api/v1/orders \
  -H 'Content-Type: application/json' \
  -d '{"subject":"测试商品","amount":"19.99","openid":"oTest123456"}'
# → data.status = CREATED，记下 data.out_trade_no

NO=<上一步返回的 out_trade_no>

# 2. 发起支付，拿到可直接传给 wx.requestPayment 的 invoke_params
curl -s -X POST $BASE/api/v1/payments/prepay \
  -H 'Content-Type: application/json' \
  -d "{\"out_trade_no\":\"$NO\"}"
# → data.trade_type = JSAPI，data.invoke_params.paySign = MOCK_PAY_SIGN_NOT_FOR_PRODUCTION
# → 订单转为 PAYING，支付流水转为 PREPAID

# 3. 模拟渠道回调（等价于微信打过来的支付结果通知）
curl -s -X POST $BASE/api/v1/mock/pay-success \
  -H 'Content-Type: application/json' \
  -d "{\"out_trade_no\":\"$NO\"}"
# → data.paid = true，data.idempotent = false

# 4. 查单
curl -s $BASE/api/v1/payments/$NO
# → data.order_status = PAID，data.payments[0].status = SUCCESS

# 重复第 3 步：data.idempotent = true，订单不会被重复推进
```

验证几条关键防线：

```bash
# 金额校验：伪造 1 分钱的回调，被拒绝且订单保持 PAYING
curl -s -X POST $BASE/api/v1/mock/pay-success -H 'Content-Type: application/json' \
  -d "{\"out_trade_no\":\"$NO2\",\"amount_fen\":1}"
# → HTTP 400，code = 30002，message = 「回调金额 1 分与订单金额 10000 分不一致」

# 精度校验：最多两位小数
curl -s -X POST $BASE/api/v1/orders -H 'Content-Type: application/json' \
  -d '{"subject":"x","amount":"19.999"}'
# → HTTP 400，code = 20003，message = 「金额最多保留两位小数」

# 并发幂等：8 路同时回调，只有 1 次 idempotent=false
for i in $(seq 8); do curl -s -X POST $BASE/api/v1/mock/pay-success \
  -H 'Content-Type: application/json' -d "{\"out_trade_no\":\"$NO3\"}" & done; wait
```

### 错误码

| 码 | HTTP | 含义 |
|---|---|---|
| `10001` | 400 | 请求参数不合法 |
| `10002` | 400 | 请求无法解析（空体、非法 JSON） |
| `10003` | 404 | 资源不存在（路径写错） |
| `10004` | 500 | 服务内部错误，细节只进日志 |
| `10005` | 403 | 当前环境不支持该操作 |
| `10006` | 405 | 请求方法不被允许，响应带 `Allow` 头 |
| `20001` | 404 | 订单不存在 |
| `20002` | 409 | 订单状态不允许该操作 |
| `20003` | 400 | 订单金额不合法 |
| `20004` | 409 | 订单已关闭 |
| `20005` | 409 | 订单已支付，重复支付被拒绝 |
| `30001` | 404 | 支付记录不存在 |
| `30002` | 400 | **支付金额与订单金额不一致**，需人工介入 |
| `30003` | 400 | 缺少支付者 openid |
| `30004` | 400 | 支付渠道未启用 |
| `40001` | 500 | 支付渠道未注册 |
| `40002` | 502 | 支付渠道调用失败 |
| `40003` | 400 | 支付回调验签或解密失败 |
| `40004` | 501 | 渠道功能未实现（mock 的退款走这里） |

表中「含义」是 `internal/errcode` 里的预设文案。部分错误在抛出时会用 `WithMsg` 换成带上下文的文案（如 `30002` 会带上两边的具体金额）以便定位，因此**客户端只应据 `code` 做分支，不要对 `message` 做字符串匹配**。

## 核心设计

### 渠道抽象层是唯一接缝

`service` 层只认 `channel.Gateway` 接口，不认任何 SDK 类型：

```go
type Gateway interface {
	Code() Code
	Prepay(ctx, PrepayRequest) (*PrepayResult, error)
	QueryOrder(ctx, outTradeNo) (*NotifyPayload, error)
	CloseOrder(ctx, outTradeNo) error
	ParseNotify(ctx, *http.Request) (*NotifyPayload, error)
	Refund(ctx, RefundRequest) (*RefundResult, error)
}
```

`QueryOrder` 与 `ParseNotify` 返回同一个 `NotifyPayload`：二者的业务语义完全相同（渠道告知本笔交易的当前状态），共用结构让主动查单能直接复用回调的处理路径，避免「回调走一条路、查单走另一条路」导致结论不一致。

### 状态机固化在领域层

```
Order:   CREATED → PAYING → PAID
                     └────→ CLOSED / REFUNDED

Payment: INIT → PREPAID → SUCCESS
                  └─────→ FAILED / CLOSED
```

流转规则写在 `model` 的私有 `transit()` 里，非法流转直接返回错误。service 层不判断状态字符串，只调用领域方法——这样新增状态时不会出现「改了 A 处忘了 B 处」。

### 回调幂等

微信的重试策略意味着同一笔通知会到达多次，且可能并发到达。处理方式：

1. 锁外先做一次快速判断，已支付则直接返回 `idempotent=true`
2. `orders.Mutate` 的回调内**再检查一次**，命中则返回哨兵错误 `errAlreadyPaidConcurrently`
3. 幂等命中同样应答 `SUCCESS`——本地状态已经正确，没有理由让渠道继续重投

只有第 2 步是必要的：第 1 步到第 3 步之间存在窗口期，并发的多个请求会同时通过快速判断。

### 金额全程用 int64 存「分」

`money` 包的元分转换基于字符串与 `math/big`，不经过 `float64`。`19.99` 一旦变成浮点数就再也无法精确还原，而支付系统里「差一分钱」就是资金事故。对外响应同时给出 `amount`（元，字符串，供展示）与 `amount_fen`（分，整数，供计算），避免调用方自己做转换。

### 内存仓储的并发安全

`repository` 的内存实现用 `sync.RWMutex` + map，写操作一律走 **draft-copy-then-swap**：读锁内拷贝一份副本，改副本，再用写锁换回去。对外返回的也总是 `Clone()` 副本，调用方拿到的是快照，改它不会影响仓储内的数据。接口已就位，换 MySQL 只需新增实现并在 `internal/app` 替换，`service` 与 `handler` 零改动。

### request_id 贯穿链路

`middleware.RequestID` 最先执行，客户端传入 `X-Request-Id` 则沿用，否则生成。之后所有日志都带该字段，响应体与响应头也回显，排查问题时可以直接用它把一次请求的所有日志串起来。

## 配置

配置来源优先级：**环境变量 > yaml 文件 > 代码默认值**。映射关系见 `internal/config/config.go` 的 `bindEnvs`（用显式 `BindEnv` 而非 `AutomaticEnv`，避免环境变量意外覆盖到不该覆盖的项）。

```bash
cp .env.example .env        # 填入真实值，.env 已被 gitignore
set -a; source .env; set +a # 本服务不内置 .env 解析，需由部署环境注入
```

关键项：

| 配置 | 环境变量 | 说明 |
|---|---|---|
| `server.mode` | `PAYMENT_SERVER_MODE` | `debug` / `release` / `test`，release 下不注册 mock 路由 |
| `server.port` | `PAYMENT_SERVER_PORT` | 默认 8080 |
| `server.shutdown_timeout` | — | 必须小于容器编排的 `terminationGracePeriodSeconds`，否则优雅关闭形同虚设 |
| `log.format` | `PAYMENT_LOG_FORMAT` | 生产用 `json` 便于日志采集解析 |
| `payment.default_channel` | `PAYMENT_DEFAULT_CHANNEL` | 骨架阶段为 `mock`，接入后改 `wechatpay` |
| `payment.notify_base_url` | `PAYMENT_NOTIFY_BASE_URL` | release 下若非 `https://` 开头，启动失败 |
| `channels.wechatpay.*` | `WECHATPAY_*` | 证书模式与公钥模式二选一，`Validate()` 强制要求配齐一套 |

`WECHATPAY_API_V3_KEY` 是**唯一只能来自环境变量**的配置项，yaml 里没有对应字段——这是刻意设计，避免有人图方便把 32 位密钥写进配置文件提交上去。

配置校验发生在启动期而非请求期：非法的 `mode` / `port` / `log.format`、release 下的 HTTP 回调地址、启用了微信支付却配不齐凭据，都会让进程直接启动失败并给出明确原因。支付服务「带病启动」的后果远重于「启动不了」。

## 安全约束

- **`certs/` 三层排除**：`.gitignore`、`.dockerignore`、`Dockerfile` 都显式排除。商户私钥泄漏的等级等同于账户密码，进了 git 历史或镜像层就无法抹掉，只能到商户平台作废重申。详见 [certs/README.md](certs/README.md)
- **mock 路由不在 release 注册**：`POST /api/v1/mock/pay-success` 能把任意订单改成已支付，暴露在生产环境等同于资金漏洞
- **回调必须验签**：mock 渠道跳过了这一步，真实实现必须在 `ParseNotify` 里完成验签与报文解密，校验失败返回错误而不是把不可信报文交给业务层
- **报文优先于内部记录**：mock 的 `ParseNotify` 以回调报文声明的金额为准构造 `NotifyPayload`，内部下单记录只用于补齐报文里没写的字段。若反过来用记录金额覆盖报文值，「回调金额与订单金额不一致」这条资金防线会被整个绕过
- **日志不打印密钥、证书内容与完整回调报文**

## 工程化

```bash
make help         # 列出全部目标
make check        # 提交前自检：gofmt + go vet + go build
make build        # 编译到 bin/payment-server（CGO_ENABLED=0 静态链接）
make run          # 本地前台运行
make test         # go test ./... -race
make cover        # 测试并输出覆盖率
make tidy         # 整理依赖
make clean        # 清理构建产物
make docker-build # 构建镜像
```

`make lint` 需要 `golangci-lint`，未安装时跳过而不报错：

```bash
curl -sSfL https://raw.githubusercontent.com/golangci/golangci-lint/master/install.sh | sh -s -- -b $(go env GOPATH)/bin
```

Dockerfile 用多阶段构建（`golang:1.26-alpine` → `alpine:3.20`），产物以非 root 用户运行，带 `/healthz` 的 HEALTHCHECK，并且**刻意不 COPY `certs/`**——证书通过只读挂载注入：

```bash
docker run -v "$PWD/certs:/app/certs:ro" --env-file .env payment-server:latest
```

## 接入微信支付

**先完成代码之外的准备**：商户号资质、AppID 绑定、JSAPI 产品开通、支付授权目录、API 证书、验签模式确认、APIv3 密钥、公网 HTTPS 回调域名、openid 获取链路。逐项清单见 [docs/wechatpay-checklist.md](docs/wechatpay-checklist.md)。

清单完成后，代码改动**只涉及三处**，`service` / `handler` / `router` 层零改动：

1. 新增 `internal/channel/wechatpay/gateway.go`，实现 `channel.Gateway` 的 6 个方法
2. `internal/app/app.go` 的 `buildRegistry` 中，按 `cfg.Channels.WechatPay.Enabled` 注册该实现（当前那里是显式报错）
3. `go get github.com/wechatpay-apiv3/wechatpay-go`，并把 `payment.default_channel` 改为 `wechatpay`

SDK 的具体调用方式、证书模式与公钥模式的差异、验签要点，都写在 [internal/channel/wechatpay/doc.go](internal/channel/wechatpay/doc.go) 的包注释里。

## 已知边界

- **没有测试**：本轮刻意未实现（`_test.go` 文件不存在）。写测试时需先 `go get github.com/stretchr/testify`——`go mod tidy` 已把它从依赖中移除
- **没有持久化**：进程重启后订单与流水全部丢失
- **没有定时调度**：`SyncFromChannel` 提供了主动查单能力，但「扫描 PAYING 超时订单」的兜底任务需要自行接入调度器，否则回调丢失就是永久掉单
- **没有退款接口**：`Gateway.Refund` 已定义，mock 返回 40004「未实现」，HTTP 层未暴露
- **没有鉴权、限流、链路追踪**
- **不实现 openid 获取**：公众号 OAuth 与小程序 `code2Session` 由调用方负责
