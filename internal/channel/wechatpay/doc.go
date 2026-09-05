// Package wechatpay 是微信支付渠道的占位包。
//
// # 当前状态
//
// 骨架阶段本包只有这份文档，没有任何实现，也未引入
// github.com/wechatpay-apiv3/wechatpay-go 依赖。
// 服务的默认渠道是 internal/channel/mock，整条链路可端到端跑通。
//
// # 接入时需要做什么
//
// 新增 gateway.go，实现 channel.Gateway 接口的六个方法
// （Code / Prepay / QueryOrder / CloseOrder / ParseNotify / Refund），
// 然后在 internal/app/app.go 的装配逻辑中按 channels.wechatpay.enabled 注册。
// service 层与 handler 层不需要任何改动——这是 channel 抽象层存在的意义。
//
// # 官方 SDK 用法要点
//
// 创建客户端（证书模式，适用于 2024 年之前的老商户号）：
//
//	mchPrivateKey, err := utils.LoadPrivateKeyWithPath(cfg.PrivateKeyPath)
//	opts := []core.ClientOption{
//	    option.WithWechatPayAutoAuthCipher(cfg.MchID, cfg.MchCertSerialNo,
//	        mchPrivateKey, cfg.APIv3Key),
//	}
//	client, err := core.NewClient(ctx, opts...)
//
// 创建客户端（微信支付公钥模式，新申请商户号的默认方式）：
//
//	opts := []core.ClientOption{
//	    option.WithWechatPayPublicKeyAuthCipher(cfg.MchID, cfg.MchCertSerialNo,
//	        mchPrivateKey, cfg.APIv3Key, cfg.PublicKeyID, cfg.PublicKeyPath),
//	}
//
// 两种模式的区别在于回调验签用哪把公钥：证书模式需要定期下载并轮换平台证书
// （SDK 的 AutoAuthCipher 会自动处理），公钥模式使用固定不变的微信支付公钥。
// 配置结构 config.WechatPayConfig 已经同时预留了两套字段，接入时按商户实际情况二选一。
//
// JSAPI 下单：
//
//	svc := jsapi.JsapiApiService{Client: client}
//	resp, _, err := svc.Prepay(ctx, payments.PrepayRequest{
//	    Appid:       core.String(cfg.AppID),
//	    Mchid:       core.String(cfg.MchID),
//	    Description: core.String(req.Description),
//	    OutTradeNo:  core.String(req.OutTradeNo),
//	    NotifyUrl:   core.String(req.NotifyURL),
//	    Attach:      core.String(req.Attach),
//	    Amount:      &payments.Amount{Total: core.Int64(req.AmountTotal)},
//	    Payer:       &payments.Payer{Openid: core.String(req.PayerOpenID)},
//	})
//	// resp.PrepayId 即 prepay_id
//
// 生成前端调起参数（务必用 SDK 提供的签名能力，不要自己拼字符串算签名）：
//
//	resp, err := svc.Prepay(ctx, req)
//	// 用商户私钥对 appid\ntimeStamp\nnonceStr\npackage\n 做 SHA256WithRSA 签名
//	sign, err := utils.SignSHA256WithRSA(source, mchPrivateKey)
//	// 组装成 channel.InvokeParams 返回，SignType 固定为 "RSA"
//
// 注意 timeStamp 在签名原文与返回给前端的 JSON 中都必须是字符串，
// 传数字会导致 iOS 微信客户端验签失败。
//
// 回调验签与解密（这是安全边界，绝不能省略）：
//
//	handler, err := notify.NewRSANotifyHandler(cfg.APIv3Key,
//	    verifiers.NewSHA256WithRSAVerifier(verifiers.NewCertificateVisitor(...)))
//	// 公钥模式改用 verifiers.NewPublicKeyVerifier
//	notification := new(payments.Transaction)
//	_, err = handler.ParseNotify(r.Context(), r.Body, notification)
//
// ParseNotify 必须原样使用 *http.Request，因为验签需要读取
// Wechatpay-Signature、Wechatpay-Timestamp、Wechatpay-Nonce、
// Wechatpay-Serial 这四个请求头，这也是 channel.Gateway 的
// ParseNotify 签名接收 *http.Request 而非 []byte 的原因。
//
// 查单：
//
//	svc := jsapi.JsapiApiService{Client: client}
//	resp, _, err := svc.QueryOrderByOutTradeNo(ctx,
//	    jsapi.QueryOrderByOutTradeNoRequest{
//	        Mchid:      core.String(cfg.MchID),
//	        OutTradeNo: core.String(outTradeNo),
//	    })
//	// resp.TradeState 映射到 channel.TradeState* 常量
//
// 关单：svc.CloseOrder(ctx, jsapi.CloseOrderRequest{...})
//
// # 四个必备凭据
//
// 接入前必须在商户平台准备齐全，缺一不可，详见 docs/wechatpay-checklist.md：
//
//  1. 商户号 mchid
//  2. 商户 API 证书序列号（或微信支付公钥 ID）
//  3. APIv3 密钥（32 位，用于回调报文 AES-256-GCM 解密）
//  4. 商户私钥 apiclient_key.pem（申请证书时生成，只能下载一次）
//
// # 安全红线
//
// apiclient_key.pem 与 APIv3 密钥一旦泄漏，等同于商户资金账户失守：
// 攻击者可以伪造请求发起退款。因此
//   - 私钥文件只放在 certs/ 下，该目录已整体 gitignore
//   - APIv3 密钥只从环境变量 WECHATPAY_API_V3_KEY 读取，配置文件中无对应字段
//   - 日志中严禁打印任何密钥、证书内容与完整回调报文
package wechatpay
