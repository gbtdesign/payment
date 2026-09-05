package repository

import (
	"context"
	"errors"
	"sort"
	"sync"

	"payment/internal/errcode"
	"payment/internal/model"
)

// MemoryPaymentRepository 基于内存的支付流水仓储实现。
//
// 维护两份索引：payment_no -> 流水，out_trade_no -> 流水号列表。
// 冗余索引是为了让「查某订单的全部支付尝试」这个高频操作不必全表扫描，
// 换成数据库后对应的是 out_trade_no 上的普通索引。
type MemoryPaymentRepository struct {
	mu       sync.RWMutex
	payments map[string]*model.Payment
	byOrder  map[string][]string
}

// NewMemoryPaymentRepository 创建内存流水仓储。
func NewMemoryPaymentRepository() *MemoryPaymentRepository {
	return &MemoryPaymentRepository{
		payments: make(map[string]*model.Payment),
		byOrder:  make(map[string][]string),
	}
}

var _ PaymentRepository = (*MemoryPaymentRepository)(nil)

func (r *MemoryPaymentRepository) Create(_ context.Context, payment *model.Payment) error {
	if payment == nil {
		return errors.New("repository: 支付流水不能为 nil")
	}
	if payment.PaymentNo == "" {
		return errors.New("repository: 流水号不能为空")
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if _, exists := r.payments[payment.PaymentNo]; exists {
		return NewDuplicateKeyError("payment", payment.PaymentNo)
	}

	cp := payment.Clone()
	r.payments[cp.PaymentNo] = cp
	r.byOrder[cp.OutTradeNo] = append(r.byOrder[cp.OutTradeNo], cp.PaymentNo)
	return nil
}

func (r *MemoryPaymentRepository) GetByPaymentNo(_ context.Context, paymentNo string) (*model.Payment, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	p, ok := r.payments[paymentNo]
	if !ok {
		return nil, errcode.ErrPaymentNotFound.WithMsg("支付流水不存在: " + paymentNo)
	}
	return p.Clone(), nil
}

func (r *MemoryPaymentRepository) ListByOutTradeNo(_ context.Context, outTradeNo string) ([]*model.Payment, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	nos := r.byOrder[outTradeNo]
	result := make([]*model.Payment, 0, len(nos))
	for _, no := range nos {
		if p, ok := r.payments[no]; ok {
			result = append(result, p.Clone())
		}
	}

	// 按创建时间升序，时间相同时按流水号兜底。
	//
	// 必须显式排序而不是依赖插入顺序：service 层要取「最新一条未终结流水」，
	// 顺序不稳定会让同一份数据在不同调用下得到不同结果，
	// 这种不确定性的 bug 极难复现。
	sort.SliceStable(result, func(i, j int) bool {
		if result[i].CreatedAt.Equal(result[j].CreatedAt) {
			return result[i].PaymentNo < result[j].PaymentNo
		}
		return result[i].CreatedAt.Before(result[j].CreatedAt)
	})
	return result, nil
}

func (r *MemoryPaymentRepository) MutateByPaymentNo(_ context.Context, paymentNo string, fn func(p *model.Payment) error) (*model.Payment, error) {
	if fn == nil {
		return nil, errors.New("repository: 修改回调不能为 nil")
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	stored, ok := r.payments[paymentNo]
	if !ok {
		return nil, errcode.ErrPaymentNotFound.WithMsg("支付流水不存在: " + paymentNo)
	}

	// 同 MemoryOrderRepository.Mutate：在副本上改，成功才替换
	draft := stored.Clone()
	if err := fn(draft); err != nil {
		return nil, err
	}
	r.payments[paymentNo] = draft

	return draft.Clone(), nil
}

func (r *MemoryPaymentRepository) Count(_ context.Context) int {
	r.mu.RLock()
	defer r.mu.RUnlock()

	return len(r.payments)
}
