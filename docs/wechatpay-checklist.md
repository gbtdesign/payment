# 微信支付接入前准备清单

本清单列出**代码之外**必须先完成的事。这些项目全部在微信商户平台与业务侧，任何一项缺失都会导致接入卡住，且卡住的现象往往是「接口报错信息看不懂」而不是「明确提示缺什么」，所以要在写代码前先逐项确认。

当前代码状态：`internal/channel/wechatpay` 只有占位文档，服务默认走 `mock` 渠道。清单前 8 项完成后，才开始按 `internal/channel/wechatpay/doc.go` 的说明写实现。

## 一、账号与资质

- [ ] **1. 注册并认证微信支付商户号**
      企业/个体工商户资质，走完签约与账户验证。拿到 `mchid`。
      个人主体无法申请 JSAPI 支付。

- [ ] **2. AppID 与商户号完成绑定**
      公众号或小程序的 AppID 必须与 `mchid` 建立绑定关系（商户平台「产品中心 → AppID 账号管理」）。
      未绑定时下单会直接报 `appid和mch_id不匹配`，这是接入期第一个高频坑。

- [ ] **3. 开通 JSAPI 支付产品**
      商户平台「产品中心 → JSAPI 支付」申请开通。
      未开通时下单报 `商户号该产品权限未开通`。本项目目标是 JSAPI / 小程序场景，这一项必须开。

## 二、支付授权目录

- [ ] **4. 配置支付授权目录**（公众号网页支付需要，小程序支付不需要）
      商户平台「产品中心 → 开发配置 → JSAPI 支付授权目录」。
      要求：
      - 必须是已备案的 HTTPS 域名
      - 路径要**精确到发起支付的那个页面所在目录**，以 `/` 结尾
      - 最多可配 5 个

      目录不匹配时报 `当前页面的URL未注册`。这一项与前端路由强相关，改前端路由时要同步来改。

## 三、API 凭据

- [ ] **5. 申请 API 证书，下载 `apiclient_key.pem`**
      商户平台「账户中心 → API 安全 → 申请 API 证书」，需用官方证书工具生成 CSR。
      **私钥只能下载一次**，丢了只能作废重申。存放规则见 [certs/README.md](../certs/README.md)。

- [ ] **6. 确认验签模式：证书模式 or 微信支付公钥模式**
      2024 年之后新申请的商户号默认走**公钥模式**，拿不到平台证书。
      两种模式在代码里对应不同的 SDK 选项，必须先确认自己属于哪一种：

      | | 证书模式 | 公钥模式 |
      |---|---|---|
      | 适用 | 老商户号 | 新商户号 |
      | 验签凭据 | 平台证书（SDK 自动下载轮换） | 微信支付公钥（固定不变） |
      | 需要配置 | `mch_cert_serial_no` | `public_key_id` + `public_key_path` |
      | SDK 选项 | `WithWechatPayAutoAuthCipher` | `WithWechatPayPublicKeyAuthCipher` |

      `internal/config` 的 `WechatPayConfig` 已同时预留两套字段，`Validate()` 会强制要求至少配齐一套。

- [ ] **7. 设置 APIv3 密钥（32 位）**
      商户平台「账户中心 → API 安全 → 设置 APIv3 密钥」。
      用于回调报文的 AES-256-GCM 解密。**只通过环境变量 `WECHATPAY_API_V3_KEY` 注入**，配置文件里没有对应字段——这是刻意设计，见 `internal/config/config.go` 的 `Load`。

      修改密钥会让所有正在运行的实例解密失败，多副本部署时要安排同时重启。

## 四、回调可达性

- [ ] **8. 准备公网可访问的 HTTPS 回调域名**
      硬性要求：
      - 必须 HTTPS，微信支付不接受 HTTP
      - 域名必须已完成 **ICP 备案**
      - 公网可直连，不能在内网或需要认证代理后面
      - 响应必须在 **5 秒内**返回，否则微信判定通知失败并重试

      本服务的回调路径为 `/api/v1/callbacks/wechatpay`（`channels.wechatpay.notify_path`），完整地址由 `payment.notify_base_url` 拼接，见 `config.NotifyURL()`。

      `release` 模式下若 `notify_base_url` 不是 `https://` 开头，进程会直接启动失败。

- [ ] **9. 本地联调准备内网穿透**
      回调必须公网可达，本地 `localhost:8080` 收不到通知。
      可用 ngrok / frp / cpolar 等把本地端口映射到一个 HTTPS 公网地址，把该地址填进 `payment.notify_base_url`。

      没有穿透工具时的替代方案：用本项目提供的 `POST /api/v1/mock/pay-success` 手动推进状态机（仅非 release 模式可用）。但要注意，这条路验证的是**本系统的状态机逻辑**，验不了微信侧的验签与解密。

## 五、openid 获取链路

- [ ] **10. 打通 openid 的获取方式**
      JSAPI 下单必须传 `payer.openid`，且该 openid 必须归属于绑定的那个 AppID。
      本项目**不实现** openid 获取，由调用方传入（见「不在本次范围」）。接入前需确认业务侧已有可用的获取链路：

      | 场景 | 获取方式 |
      |---|---|
      | 公众号网页 | 网页授权 `snsapi_base` 静默授权 → `code` 换 `access_token` + `openid` |
      | 小程序 | `wx.login()` 拿 `code` → 服务端调 `code2Session` 换 `openid` |

      两个常见坑：
      - 用错 AppID 换取的 openid 下单，会报 `openid 与 appid 不匹配`
      - 公众号与小程序是两个不同 AppID，同一用户在两边的 openid 不同，需要通过 UnionID 关联

## 六、上线前必查

- [ ] **11. 对账与掉单兜底机制确认**
      - 回调会丢失（网络、防火墙、发布重启），必须有主动查单兜底。本项目已提供 `PaymentService.SyncFromChannel`，但**没有引入定时调度器**，生产上线前需要补上「扫描 PAYING 超时订单」的任务
      - 微信支付的重试间隔为 `15s/15s/30s/3m/10m/20m/30m/30m/30m/60m/3h/3h/3h/6h/6h`，回调应答必须返回 `{"code":"SUCCESS"}`，否则会一直重投。应答格式见 `internal/handler/callback_handler.go`
      - 每日账单下载与对账逻辑本项目未实现

- [ ] **12. 密钥与证书的存放方式确认**
      - 私钥通过 Secret / 挂载卷注入，不进仓库、不进镜像
      - `certs/` 已被 `.gitignore`、`.dockerignore`、`Dockerfile` 三处排除
      - 日志中不打印密钥、证书内容与完整回调报文

## 完成后的代码改动点

清单全部完成后，接入微信支付的改动**只涉及三处**，`service` / `handler` / `router` 层零改动——这正是 `channel.Gateway` 抽象层的设计目的：

1. 新增 `internal/channel/wechatpay/gateway.go`，实现 `channel.Gateway` 的 6 个方法
2. `internal/app/app.go` 的 `buildRegistry` 中，按 `cfg.Channels.WechatPay.Enabled` 注册该实现（当前那里是显式报错，见注释）
3. `go get github.com/wechatpay-apiv3/wechatpay-go`，并把 `configs/config.yaml` 的 `payment.default_channel` 改为 `wechatpay`

具体的 SDK 调用方式、验签要点与安全红线，都写在 [internal/channel/wechatpay/doc.go](../internal/channel/wechatpay/doc.go) 的包注释里。

## 不在本次骨架范围

- 不引入 `github.com/wechatpay-apiv3/wechatpay-go`，不写任何真实签名/验签/加解密代码
- 不做数据库持久化（Repository 接口已就位，换 MySQL 只需新增实现并在 `internal/app` 替换）
- 不暴露退款 HTTP 接口（`Gateway.Refund` 已定义，mock 返回「未实现」）
- 不做鉴权中间件、限流、链路追踪
- 不实现 openid 获取（公众号 OAuth / `code2Session`）
- 不实现定时对账与掉单扫描调度器
