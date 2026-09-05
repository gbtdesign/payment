// Package idgen 生成业务流水号。
//
// 微信支付对 out_trade_no 的约束：最长 32 字符，同一商户号下必须唯一，
// 仅允许数字、字母及 _ - | * @ 四类符号。本包生成的流水号只使用数字与字母，
// 对所有渠道都安全，也避免符号在 URL 与日志中被转义带来的麻烦。
package idgen

import (
	"crypto/rand"
	"encoding/hex"
	"math/big"
	"strings"
	"time"
)

// MaxOutTradeNoLen 是微信支付 out_trade_no 的长度上限。
const MaxOutTradeNoLen = 32

const (
	charset = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"

	timeLayout = "20060102150405"
	randomLen  = 8

	// requestIDRandomBytes 决定 request_id 的十六进制长度（16 字节 -> 32 字符）。
	requestIDRandomBytes = 16
)

// OutTradeNo 生成商户订单号，格式为 prefix + yyyyMMddHHmmss + 8 位随机字符。
//
// 唯一性依赖同一秒内 62^8（约 2.18e14）的随机空间。这个量级在支付业务下
// 碰撞概率可忽略，但仍建议存储层对 out_trade_no 加唯一索引兜底。
//
// prefix 会被裁剪到 10 字符以内且只保留字母数字，
// 以保证总长度 len(prefix)+14+8 不超过 32。
func OutTradeNo(prefix string) string {
	return generate(prefix, randomLen)
}

// PaymentNo 生成支付流水号，规则与订单号一致。
//
// 与订单号分开生成是因为一个订单可能有多次支付尝试，
// 流水号必须独立才能追溯到具体是哪一次调起。
func PaymentNo(prefix string) string {
	return generate(prefix, randomLen)
}

// NonceStr 生成随机串，用于支付参数签名等需要防重放的场合。
//
// 长度固定 32，与微信支付文档示例一致。
func NonceStr() string {
	return randomString(requestIDRandomBytes * 2)
}

// RequestID 生成请求追踪 ID，32 位十六进制。
//
// 用 crypto/rand 而非 google/uuid，是为了不额外引入依赖：
// 这里只需要「足够随机且可读」，不需要 UUID 的版本与变体语义。
func RequestID() string {
	buf := make([]byte, requestIDRandomBytes)
	if _, err := rand.Read(buf); err != nil {
		// crypto/rand 失败意味着系统熵源异常，此时退化到时间戳，
		// 保证请求链路不会因为生成 ID 失败而中断
		return "fallback" + strings.ReplaceAll(
			time.Now().UTC().Format("20060102150405.000000000"), ".", "")
	}
	return hex.EncodeToString(buf)
}

func generate(prefix string, rndLen int) string {
	// 32 = len(prefix) + 14 + rndLen，反推出 prefix 的最大可用长度
	maxPrefix := MaxOutTradeNoLen - len(timeLayout) - rndLen
	if maxPrefix < 0 {
		maxPrefix = 0
	}

	p := sanitize(prefix)
	if len(p) > maxPrefix {
		p = p[:maxPrefix]
	}
	return p + time.Now().Format(timeLayout) + randomString(rndLen)
}

// sanitize 只保留字母与数字，避免调用方传入的符号破坏渠道侧的字符集约束。
func sanitize(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= '0' && r <= '9',
			r >= 'A' && r <= 'Z',
			r >= 'a' && r <= 'z':
			return r
		default:
			return -1
		}
	}, s)
}

// randomString 用 crypto/rand 生成指定长度的随机字符串。
//
// 这里刻意不用 math/rand：订单号一旦可预测，
// 攻击者就能枚举他人的 out_trade_no 去试探查单接口。
func randomString(n int) string {
	if n <= 0 {
		return ""
	}
	// 用 big.Int 逐个取模而不是取字节再 %62，
	// 后者会因为 256 不能被 62 整除而产生轻微的分布偏斜
	max := big.NewInt(int64(len(charset)))
	b := make([]byte, n)
	for i := range b {
		idx, err := rand.Int(rand.Reader, max)
		if err != nil {
			// 同上，熵源异常时退化到时间派生，不让 ID 生成成为可用性瓶颈
			b[i] = charset[(int(time.Now().UnixNano())+i)%len(charset)]
			continue
		}
		b[i] = charset[idx.Int64()]
	}
	return string(b)
}
