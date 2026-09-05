// Package repository 定义存储层接口。
//
// 本包只声明接口，不绑定任何具体存储。当前提供内存实现用于跑通骨架，
// 换成 MySQL 只需新增一个实现并在 internal/app 的装配处替换，service 层零改动。
//
// 接口设计上有两点刻意为之：
//
//  1. 提供 Mutate 而不是裸 Update。
//     支付回调存在真实的并发重复投递（微信支付在未收到成功应答时会按
//     15s/15s/30s/3m/10m/20m/30m/30m/30m/60m/3h/3h/3h/6h/6h 的间隔重试），
//     「读取-判断-写回」这三步若不原子化，两个 goroutine 可能同时读到
//     PAYING 并各自推进一次状态，幂等就失效了。
//     Mutate 把这段临界区交给存储层保证：内存实现用互斥锁，
//     数据库实现对应 SELECT ... FOR UPDATE 事务。
//
//  2. 对外一律返回副本。
//     返回内部指针会让调用方绕过领域模型的状态机直接改字段，
//     内存仓储中的数据随之被污染，且这类 bug 在压测下才偶发出现。
package repository

import (
	"context"

	"payment/internal/model"
)

// OrderRepository 订单存储。
type OrderRepository interface {
	// Create 写入新订单。out_trade_no 重复时返回错误。
	Create(ctx context.Context, order *model.Order) error

	// GetByOutTradeNo 按商户订单号查询，不存在时返回 errcode.ErrOrderNotFound。
	GetByOutTradeNo(ctx context.Context, outTradeNo string) (*model.Order, error)

	// Mutate 在原子上下文中读取并修改订单，返回修改后的副本。
	//
	// fn 返回错误时本次修改整体不生效（内存实现直接不落盘，
	// 数据库实现由事务回滚保证）。fn 内部不应做 IO 或耗时操作，
	// 因为它运行在锁/事务中，阻塞会放大成整个存储层的抖动。
	Mutate(ctx context.Context, outTradeNo string, fn func(o *model.Order) error) (*model.Order, error)

	// Count 返回订单总数，供健康检查与测试使用。
	Count(ctx context.Context) int
}

// PaymentRepository 支付流水存储。
type PaymentRepository interface {
	// Create 写入新流水。payment_no 重复时返回错误。
	Create(ctx context.Context, payment *model.Payment) error

	// GetByPaymentNo 按流水号查询，不存在时返回 errcode.ErrPaymentNotFound。
	GetByPaymentNo(ctx context.Context, paymentNo string) (*model.Payment, error)

	// ListByOutTradeNo 返回某订单的全部流水，按创建时间升序。
	//
	// 返回空切片而非 nil，让调用方可以直接 range 而不必判空。
	ListByOutTradeNo(ctx context.Context, outTradeNo string) ([]*model.Payment, error)

	// MutateByPaymentNo 在原子上下文中读取并修改流水，返回修改后的副本。
	MutateByPaymentNo(ctx context.Context, paymentNo string, fn func(p *model.Payment) error) (*model.Payment, error)

	// Count 返回流水总数。
	Count(ctx context.Context) int
}

// duplicateKeyError 表示唯一键冲突。
//
// 单独定义类型而不是用 fmt.Errorf，是因为「已存在」在不同上下文里含义不同：
// 创建接口应把它转成冲突错误返回，而重放场景下它可能只是正常的幂等命中。
// 调用方需要能可靠地区分这一情况，字符串匹配做不到这一点。
type duplicateKeyError struct {
	entity string
	key    string
}

func (e *duplicateKeyError) Error() string {
	return "repository: " + e.entity + " 主键重复: " + e.key
}

// NewDuplicateKeyError 构造唯一键冲突错误。
func NewDuplicateKeyError(entity, key string) error {
	return &duplicateKeyError{entity: entity, key: key}
}

// IsDuplicate 判断 err 是否为主键冲突。
func IsDuplicate(err error) bool {
	_, ok := err.(*duplicateKeyError)
	return ok
}
