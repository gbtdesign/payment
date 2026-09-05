package channel

import (
	"fmt"
	"sort"
	"sync"

	"payment/internal/errcode"
)

// Registry 渠道注册表，并发安全。
//
// 注册表让「有哪些渠道可用」成为运行期的数据而非编译期的硬编码：
// app 装配时按配置决定注册哪些实现，service 层通过编码查找，
// 二者之间不存在任何 import 依赖。
//
// 用 RWMutex 而不是只在启动时写入后只读：
// 虽然当前装配流程是「启动时一次性注册」，但只读假设一旦被人破坏
// （比如将来支持热加载配置），数据竞争的排查成本极高。
// 读多写少的场景下 RWMutex 的开销可以忽略。
type Registry struct {
	mu       sync.RWMutex
	gateways map[Code]Gateway
}

// NewRegistry 创建空注册表。
func NewRegistry() *Registry {
	return &Registry{gateways: make(map[Code]Gateway)}
}

// Register 注册一个渠道实现。
//
// 重复注册同一编码视为配置错误并直接返回 error，而不是静默覆盖：
// 覆盖会让「哪个实现在生效」变得不可确定，支付场景下这是不可接受的状态。
func (r *Registry) Register(g Gateway) error {
	if g == nil {
		return fmt.Errorf("channel: 不能注册 nil 网关")
	}
	code := g.Code()
	if code == "" {
		return fmt.Errorf("channel: 网关未声明渠道编码")
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if _, exists := r.gateways[code]; exists {
		return fmt.Errorf("channel: 渠道 %s 重复注册", code)
	}
	r.gateways[code] = g
	return nil
}

// MustRegister 注册并在失败时 panic，仅用于启动阶段的装配代码。
//
// 启动期配置错误应当让进程直接起不来，而不是带着残缺的渠道集合对外服务。
func (r *Registry) MustRegister(g Gateway) {
	if err := r.Register(g); err != nil {
		panic(err)
	}
}

// Get 按编码取出渠道实现，不存在时返回 ErrChannelNotRegistered。
func (r *Registry) Get(code Code) (Gateway, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	g, ok := r.gateways[code]
	if !ok {
		return nil, errcode.ErrChannelNotRegistered.WithMsg(
			fmt.Sprintf("支付渠道 %s 未注册", code))
	}
	return g, nil
}

// Has 判断渠道是否已注册。
func (r *Registry) Has(code Code) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()

	_, ok := r.gateways[code]
	return ok
}

// Codes 返回已注册的渠道编码，按字典序排序。
//
// 排序是为了让日志与健康检查的输出稳定，便于 diff 排查配置漂移。
func (r *Registry) Codes() []Code {
	r.mu.RLock()
	defer r.mu.RUnlock()

	codes := make([]Code, 0, len(r.gateways))
	for c := range r.gateways {
		codes = append(codes, c)
	}
	sort.Slice(codes, func(i, j int) bool { return codes[i] < codes[j] })
	return codes
}

// Len 返回已注册渠道数量。
func (r *Registry) Len() int {
	r.mu.RLock()
	defer r.mu.RUnlock()

	return len(r.gateways)
}
