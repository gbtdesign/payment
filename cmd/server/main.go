// Command server 是支付服务的进程入口。
//
// 本层只负责进程生命周期：解析启动参数、加载配置、装配依赖、启动 HTTP 服务、
// 监听退出信号并优雅关闭。任何支付相关的判断都不应出现在这里，
// 业务逻辑一律下沉到 internal 下的对应分层。
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"go.uber.org/zap"

	"payment/internal/app"
	"payment/internal/config"
)

// readHeaderTimeout 单独限定读取请求头的时长。
//
// server.read_timeout 覆盖的是整个请求（含 body），对慢速攻击的防护不够及时；
// 额外收紧请求头阶段可以有效抵御 Slowloris 一类的连接耗尽攻击。
const readHeaderTimeout = 10 * time.Second

func main() {
	os.Exit(run())
}

// run 承载真实逻辑并返回退出码，由 main 提交给操作系统。
//
// 拆出这一层是因为 os.Exit 会跳过所有 defer：若把逻辑直接写在 main 里，
// 应用资源的清理（如日志 flush）将永远不会执行。
func run() int {
	var configPath string
	flag.StringVar(&configPath, "config", "",
		"配置文件路径，留空则依次尝试环境变量 "+config.EnvConfigFile+" 与 "+config.DefaultPath)
	flag.Parse()

	// 日志系统由 app.New 内部构造，因此这两步失败时只能写 stderr
	cfg, err := config.Load(configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "加载配置失败: %v\n", err)
		return 1
	}

	application, err := app.New(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "初始化应用失败: %v\n", err)
		return 1
	}
	defer application.Close()

	log := application.Logger()

	srv := &http.Server{
		Addr:              cfg.Server.Addr(),
		Handler:           application.Engine(),
		ReadTimeout:       cfg.Server.ReadTimeout,
		ReadHeaderTimeout: readHeaderTimeout,
		WriteTimeout:      cfg.Server.WriteTimeout,
	}

	// 信号监听必须在启动服务之前注册，否则二者之间的窗口期内到达的
	// SIGTERM 会走默认行为直接杀进程，失去优雅关闭的机会。
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)

	serveErr := make(chan error, 1)
	go func() {
		log.Info("服务启动",
			zap.String("addr", srv.Addr),
			zap.String("mode", cfg.Server.Mode),
			zap.String("default_channel", cfg.Payment.DefaultChannel),
			zap.Duration("shutdown_timeout", cfg.Server.ShutdownTimeout),
		)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serveErr <- err
			return
		}
		serveErr <- nil
	}()

	select {
	case err := <-serveErr:
		// 走到这里说明 ListenAndServe 自己返回了，通常是端口被占用或权限不足
		if err != nil {
			log.Error("HTTP 服务异常退出", zap.Error(err))
			return 1
		}
		return 0

	case sig := <-quit:
		log.Info("收到退出信号，开始优雅关闭", zap.String("signal", sig.String()))
	}

	// 优雅关闭：停止接收新连接，等待存量请求处理完成。
	//
	// 这一步对支付服务是硬要求——硬切断正在处理的支付回调会直接导致掉单，
	// 而掉单只能靠人工对账修复，代价远高于多等几秒。
	ctx, cancel := context.WithTimeout(context.Background(), cfg.Server.ShutdownTimeout)
	defer cancel()

	code := 0
	if err := srv.Shutdown(ctx); err != nil {
		log.Error("优雅关闭超时，强制断开存量连接",
			zap.Error(err),
			zap.Duration("timeout", cfg.Server.ShutdownTimeout),
		)
		_ = srv.Close()
		code = 1
	}

	// 等待 ListenAndServe 返回，确保上面的 goroutine 不会泄漏到进程退出之后
	<-serveErr

	log.Info("服务已停止")
	return code
}
