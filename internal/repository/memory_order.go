package repository

import (
	"context"
	"errors"
	"sync"

	"payment/internal/errcode"
	"payment/internal/model"
)

// MemoryOrderRepository 基于内存的订单仓储实现。
//
// 仅用于骨架阶段跑通链路与自动化测试：数据随进程退出而丢失，
// 且多副本部署时各实例数据不一致。生产环境必须换成持久化实现，
// 替换点在 internal/app/app.go，service 层无需改动。
//
// 并发安全由 RWMutex 保证。这里必须加锁而不是「反正骨架阶段没有并发」：
// HTTP 服务天然是并发的，微信支付还会重复投递回调，
// 无锁的 map 在 -race 下会立刻暴露，线上则可能直接 panic。
type MemoryOrderRepository struct {
	mu     sync.RWMutex
	orders map[string]*model.Order
}

// NewMemoryOrderRepository 创建内存订单仓储。
func NewMemoryOrderRepository() *MemoryOrderRepository {
	return &MemoryOrderRepository{orders: make(map[string]*model.Order)}
}

// 编译期断言：实现缺失接口方法时直接在此行报错，
// 而不是等到装配处把它当接口使用时才暴露。
var _ OrderRepository = (*MemoryOrderRepository)(nil)

func (r *MemoryOrderRepository) Create(_ context.Context, order *model.Order) error {
	if order == nil {
		return errors.New("repository: 订单不能为 nil")
	}
	if order.OutTradeNo == "" {
		return errors.New("repository: 订单号不能为空")
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if _, exists := r.orders[order.OutTradeNo]; exists {
		return NewDuplicateKeyError("order", order.OutTradeNo)
	}
	// 存入副本：若直接存调用方传来的指针，调用方后续对该对象的修改
	// 会绕过状态机直接影响仓储数据
	r.orders[order.OutTradeNo] = order.Clone()
	return nil
}

func (r *MemoryOrderRepository) GetByOutTradeNo(_ context.Context, outTradeNo string) (*model.Order, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	order, ok := r.orders[outTradeNo]
	if !ok {
		return nil, errcode.ErrOrderNotFound.WithMsg("订单不存在: " + outTradeNo)
	}
	return order.Clone(), nil
}

func (r *MemoryOrderRepository) Mutate(_ context.Context, outTradeNo string, fn func(o *model.Order) error) (*model.Order, error) {
	if fn == nil {
		return nil, errors.New("repository: 修改回调不能为 nil")
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	stored, ok := r.orders[outTradeNo]
	if !ok {
		return nil, errcode.ErrOrderNotFound.WithMsg("订单不存在: " + outTradeNo)
	}

	// 在副本上执行修改，成功后才替换存储中的对象。
	//
	// 若直接在 stored 上调用 fn，fn 内部的状态机流转失败时会留下
	// 半更新的脏数据（例如 PaidAt 已写入但 Status 流转被拒绝）。
	draft := stored.Clone()
	if err := fn(draft); err != nil {
		return nil, err
	}
	r.orders[outTradeNo] = draft

	return draft.Clone(), nil
}

func (r *MemoryOrderRepository) Count(_ context.Context) int {
	r.mu.RLock()
	defer r.mu.RUnlock()

	return len(r.orders)
}
