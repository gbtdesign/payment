// Package logger 基于 zap 的日志封装。
//
// 提供两个能力：
//  1. 按配置构造 Logger（级别 + console/json 两种编码）
//  2. 通过 context 传递带 request_id 的 Logger，使一次请求的所有日志可串联
//
// 调用方只需要 logger.L(ctx).Info(...)，不必关心 zap 的具体类型，
// 也避免业务包为了打日志而直接 import zap。
package logger

import (
	"context"
	"os"
	"strings"
	"sync/atomic"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// Field 是 zap.Field 的别名。
type Field = zap.Field

// 常用字段构造函数，转发 zap 实现，让调用方无需直接依赖 zap。
var (
	String     = zap.String
	Int        = zap.Int
	Int64      = zap.Int64
	Bool       = zap.Bool
	Any        = zap.Any
	Error      = zap.Error
	Duration   = zap.Duration
	ByteString = zap.ByteString
)

// Config 日志配置。
type Config struct {
	// Level 取值 debug / info / warn / error，非法值回退为 info。
	Level string
	// Format 取值 console / json，其余值按 json 处理。
	Format string
}

type ctxKey struct{}

// base 保存全局兜底 Logger。
//
// 用 atomic.Pointer 而不是普通变量：日志是全局共享状态，
// 装配阶段写入、请求阶段并发读取，没有同步会被 -race 直接判定数据竞争。
// 零值是 nil，因此 baseLogger 必须在取出后判空并回退到 Nop。
var base atomic.Pointer[zap.Logger]

// New 按配置构造全局 Logger。
//
// 生产环境应使用 json 格式：日志采集系统（Loki、ELK）解析结构化字段
// 才能按 request_id / out_trade_no 做检索，console 格式的多行输出会让检索失效。
func New(cfg Config) (*zap.Logger, error) {
	level, err := parseLevel(cfg.Level)
	if err != nil {
		return nil, err
	}

	encoderCfg := zapcore.EncoderConfig{
		TimeKey:        "time",
		LevelKey:       "level",
		NameKey:        "logger",
		CallerKey:      "caller",
		MessageKey:     "msg",
		StacktraceKey:  "stacktrace",
		LineEnding:     zapcore.DefaultLineEnding,
		EncodeLevel:    zapcore.LowercaseLevelEncoder,
		EncodeTime:     zapcore.ISO8601TimeEncoder,
		EncodeDuration: zapcore.StringDurationEncoder,
		EncodeCaller:   zapcore.ShortCallerEncoder,
	}

	var encoder zapcore.Encoder
	if strings.EqualFold(strings.TrimSpace(cfg.Format), "console") {
		encoder = zapcore.NewConsoleEncoder(encoderCfg)
	} else {
		encoder = zapcore.NewJSONEncoder(encoderCfg)
	}

	// 直接写 stderr 而不落文件：容器环境下标准做法是由运行时收集 stdout/stderr，
	// 应用自己写文件会带来轮转、磁盘占满、与 sidecar 争抢等一系列运维问题。
	core := zapcore.NewCore(encoder, zapcore.Lock(os.Stderr), level)

	return zap.New(core,
		zap.AddCaller(),
		// panic 及以上级别附带堆栈，便于定位支付流程中的异常中断
		zap.AddStacktrace(zapcore.PanicLevel),
	), nil
}

// Nop 返回不做任何输出的 Logger。
//
// 单元测试用它避免日志污染断言输出；也作为 Logger 未初始化时的兜底，
// 让业务代码永远不必写 if log != nil 这类判断。
func Nop() *zap.Logger { return zap.NewNop() }

// parseLevel 解析日志级别。
//
// 非法值直接报错而不是静默回退：配置里把 level 写成 "infor" 这种笔误，
// 如果静默按 info 处理，排查「为什么看不到 debug 日志」会浪费大量时间。
func parseLevel(s string) (zapcore.Level, error) {
	var level zapcore.Level
	if err := level.UnmarshalText([]byte(strings.ToLower(strings.TrimSpace(s)))); err != nil {
		return zapcore.InfoLevel, errInvalidLevel(s)
	}
	return level, nil
}

type invalidLevelError struct{ value string }

func (e *invalidLevelError) Error() string {
	return "logger: 非法的日志级别 " + e.value + "，可选 debug/info/warn/error"
}

func errInvalidLevel(v string) error { return &invalidLevelError{value: v} }

// WithRequestID 把 request_id 绑到 context 上，后续 L(ctx) 取出的日志都会带该字段。
func WithRequestID(ctx context.Context, requestID string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, ctxKey{}, requestID)
}

// RequestIDFrom 取出 context 中的 request_id，不存在时返回空串。
func RequestIDFrom(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	if id, ok := ctx.Value(ctxKey{}).(string); ok {
		return id
	}
	return ""
}

// L 返回 context 关联的 Logger。
//
// 这里每次调用都 With 一次 request_id，而不是在中间件里就把 *zap.Logger
// 存进 context：前者多一次对象分配但语义清晰，后者会漏掉未经中间件的调用路径
// （如定时任务、启动阶段）。支付服务的日志量不大，可读性优先。
func L(ctx context.Context) *zap.Logger {
	base := baseLogger()
	if ctx == nil {
		return base
	}
	if id := RequestIDFrom(ctx); id != "" {
		return base.With(String("request_id", id))
	}
	return base
}

// SetBase 替换全局兜底 Logger，由 app 装配时调用。
func SetBase(l *zap.Logger) {
	if l == nil {
		return
	}
	base.Store(l)
}

// baseLogger 取出全局兜底 Logger，未初始化时返回 Nop。
func baseLogger() *zap.Logger {
	if l := base.Load(); l != nil {
		return l
	}
	return zap.NewNop()
}

// Sync 刷新日志缓冲。
//
// 进程退出前必须调用：zap 的 core 带缓冲，不 flush 会丢掉最后几条日志，
// 而支付故障排查时最关键的往往正是最后那几条。
func Sync(l *zap.Logger) {
	if l == nil {
		return
	}
	// stderr 不支持 fsync，返回的 error 是预期的，忽略即可
	_ = l.Sync()
}
