// Package money 提供金额处理工具。
//
// 项目内所有金额一律使用 int64 表示「分」（最小货币单位）。
// 严禁让 float64 参与金额运算：二进制浮点无法精确表示大多数十进制小数，
// 例如 19.99 * 100 在 float64 下得到 1998.9999999999998，截断后少收 1 分钱。
// 本包的元分转换全程基于字符串与整数运算，不经过任何浮点类型。
package money

import (
	"errors"
	"fmt"
	"math/big"
	"strconv"
	"strings"
)

// DecimalScale 是人民币的小数位数（元 -> 分 需要两位）。
const DecimalScale = 2

// MaxAmountFen 是单笔金额上限，取 1 亿元。
//
// 设上限不是为了业务限制，而是防止 int64 溢出后金额变成负数——
// 负的支付金额传给渠道会导致完全不可预期的行为。
const MaxAmountFen int64 = 100_000_000_00

var (
	// ErrInvalidFormat 金额字符串格式非法。
	ErrInvalidFormat = errors.New("money: 金额格式非法")
	// ErrPrecisionLoss 小数位超过 2 位，静默截断会造成金额差错，必须拒绝。
	ErrPrecisionLoss = errors.New("money: 金额小数位超过 2 位")
	// ErrOutOfRange 金额超出允许区间。
	ErrOutOfRange = errors.New("money: 金额超出允许范围")
	// ErrNegative 金额为负数。
	ErrNegative = errors.New("money: 金额不能为负数")
	// ErrZero 金额为零。
	ErrZero = errors.New("money: 金额必须大于零")
)

// YuanToFen 把「元」字符串转换为「分」。
//
// 接受 "19.99"、"19.9"、"20"、"-3.5" 等形式，不接受千分位分隔符与货币符号。
// 小数位超过 2 位时返回 ErrPrecisionLoss 而不是四舍五入：
// 支付场景下任何静默的金额修正都是事故源头。
func YuanToFen(yuan string) (int64, error) {
	s := strings.TrimSpace(yuan)
	if s == "" {
		return 0, fmt.Errorf("%w: 空字符串", ErrInvalidFormat)
	}

	negative := false
	switch s[0] {
	case '-':
		negative = true
		s = s[1:]
	case '+':
		s = s[1:]
	}
	if s == "" {
		return 0, fmt.Errorf("%w: 只有符号位", ErrInvalidFormat)
	}

	integer, fraction := s, ""
	if idx := strings.IndexByte(s, '.'); idx >= 0 {
		integer, fraction = s[:idx], s[idx+1:]
		if strings.ContainsRune(fraction, '.') {
			return 0, fmt.Errorf("%w: 多个小数点", ErrInvalidFormat)
		}
	}
	if integer == "" {
		integer = "0"
	}

	// 逐字符校验，避免 strconv 接受 "1_000" 这类下划线分隔写法
	for i := 0; i < len(integer); i++ {
		if integer[i] < '0' || integer[i] > '9' {
			return 0, fmt.Errorf("%w: 含非数字字符 %q", ErrInvalidFormat, integer[i])
		}
	}
	for i := 0; i < len(fraction); i++ {
		if fraction[i] < '0' || fraction[i] > '9' {
			return 0, fmt.Errorf("%w: 小数部分含非数字字符 %q", ErrInvalidFormat, fraction[i])
		}
	}
	if len(fraction) > DecimalScale {
		return 0, fmt.Errorf("%w: %q", ErrPrecisionLoss, yuan)
	}

	// 补齐到两位小数后拼成整数串，全程无浮点参与
	fraction = fraction + strings.Repeat("0", DecimalScale-len(fraction))
	combined := strings.TrimLeft(integer+fraction, "0")
	if combined == "" {
		return 0, nil
	}

	fen, err := strconv.ParseInt(combined, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%w: %q 超出 int64 范围", ErrOutOfRange, yuan)
	}
	if negative {
		fen = -fen
	}
	if fen > MaxAmountFen || fen < -MaxAmountFen {
		return 0, fmt.Errorf("%w: %q 超过单笔上限", ErrOutOfRange, yuan)
	}
	return fen, nil
}

// FenToYuan 把「分」格式化为「元」字符串，始终保留两位小数。
//
// 返回字符串而非 float64，是因为任何转成浮点的中间步骤都会重新引入精度问题。
func FenToYuan(fen int64) string {
	// 全程用 big.Int：math.MinInt64 取负会溢出，普通整数运算兜不住这个边界
	abs := new(big.Int).Abs(big.NewInt(fen))
	yuan := new(big.Int).Quo(abs, big.NewInt(100))
	fraction := new(big.Int).Rem(abs, big.NewInt(100)).Int64()

	sign := ""
	if fen < 0 {
		sign = "-"
	}
	return fmt.Sprintf("%s%s.%02d", sign, yuan.String(), fraction)
}

// ValidateAmount 校验金额是否为可支付的合法值：正数且不超过上限。
func ValidateAmount(fen int64) error {
	if fen < 0 {
		return fmt.Errorf("%w: %d 分", ErrNegative, fen)
	}
	if fen == 0 {
		return fmt.Errorf("%w", ErrZero)
	}
	if fen > MaxAmountFen {
		return fmt.Errorf("%w: %d 分", ErrOutOfRange, fen)
	}
	return nil
}

// Mul 计算 fen * n，用于「数量 x 单价」这类场景。
//
// 单独提供是因为直接写 fen*n 在极端值下会静默溢出。
func Mul(fen int64, n int64) (int64, error) {
	result := new(big.Int).Mul(big.NewInt(fen), big.NewInt(n))
	if !result.IsInt64() {
		return 0, fmt.Errorf("%w: %d * %d 溢出 int64", ErrOutOfRange, fen, n)
	}
	total := result.Int64()
	if total > MaxAmountFen {
		return 0, fmt.Errorf("%w: %d * %d 超过单笔上限", ErrOutOfRange, fen, n)
	}
	return total, nil
}
