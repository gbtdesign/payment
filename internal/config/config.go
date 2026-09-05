// Package config 负责加载与校验服务配置。
//
// 优先级：环境变量 > 配置文件 > 内置默认值。
//
// 安全约束：所有密钥类配置（如微信支付 APIv3 密钥）只允许通过环境变量注入，
// 配置文件中不存在对应字段，从源头上避免密钥被误提交进代码仓库。
package config

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/viper"
)

const (
	// DefaultPath 是默认配置文件路径。
	DefaultPath = "configs/config.yaml"
	// EnvConfigFile 用于覆盖配置文件路径。
	EnvConfigFile = "PAYMENT_CONFIG"

	// EnvWechatPayAPIv3Key 是微信支付 APIv3 密钥的唯一来源。
	EnvWechatPayAPIv3Key = "WECHATPAY_API_V3_KEY"
)

// 服务模式取值，与 gin.Mode 对齐。
const (
	ModeDebug   = "debug"
	ModeRelease = "release"
	ModeTest    = "test"
)

// apiV3KeyLen 是微信支付 APIv3 密钥的固定长度。
const apiV3KeyLen = 32

// Config 是服务的全部配置。
type Config struct {
	Server   ServerConfig   `mapstructure:"server"`
	Log      LogConfig      `mapstructure:"log"`
	Payment  PaymentConfig  `mapstructure:"payment"`
	Channels ChannelsConfig `mapstructure:"channels"`
}

// ServerConfig HTTP 服务配置。
type ServerConfig struct {
	Mode string `mapstructure:"mode"`
	Host string `mapstructure:"host"`
	Port int    `mapstructure:"port"`

	ReadTimeout     time.Duration `mapstructure:"read_timeout"`
	WriteTimeout    time.Duration `mapstructure:"write_timeout"`
	ShutdownTimeout time.Duration `mapstructure:"shutdown_timeout"`
}

// Addr 拼出 http.Server 需要的监听地址。
func (s ServerConfig) Addr() string {
	return fmt.Sprintf("%s:%d", s.Host, s.Port)
}

// LogConfig 日志配置。
type LogConfig struct {
	Level  string `mapstructure:"level"`
	Format string `mapstructure:"format"`
}

// PaymentConfig 支付通用配置。
type PaymentConfig struct {
	// DefaultChannel 创建订单时未显式指定渠道所用的默认值。
	DefaultChannel string `mapstructure:"default_channel"`
	// NotifyBaseURL 回调地址的域名前缀，必须公网可达且为 HTTPS。
	NotifyBaseURL string `mapstructure:"notify_base_url"`
	// OutTradeNoPrefix 商户订单号前缀。
	OutTradeNoPrefix string `mapstructure:"out_trade_no_prefix"`
	// PaymentNoPrefix 支付流水号前缀。
	PaymentNoPrefix string `mapstructure:"payment_no_prefix"`
}

// ChannelsConfig 各支付渠道配置。
type ChannelsConfig struct {
	WechatPay WechatPayConfig `mapstructure:"wechatpay"`
}

// WechatPayConfig 微信支付配置。
//
// APIv3Key 字段不出现在 yaml 中，只能由环境变量注入，见 Load。
type WechatPayConfig struct {
	Enabled bool `mapstructure:"enabled"`

	AppID string `mapstructure:"app_id"`
	MchID string `mapstructure:"mch_id"`

	// MchCertSerialNo 商户 API 证书序列号，证书模式必填。
	MchCertSerialNo string `mapstructure:"mch_cert_serial_no"`
	// PrivateKeyPath 商户私钥 apiclient_key.pem 的路径。
	PrivateKeyPath string `mapstructure:"private_key_path"`

	// PublicKeyID / PublicKeyPath 用于新商户的「微信支付公钥模式」。
	// 与证书模式二选一：2024 年之后新申请的商户号默认拿不到平台证书，
	// 只能用公钥模式验签，因此这两项必须预留。
	PublicKeyID   string `mapstructure:"public_key_id"`
	PublicKeyPath string `mapstructure:"public_key_path"`

	// NotifyPath 回调相对路径，与 Payment.NotifyBaseURL 拼成完整回调地址。
	NotifyPath string `mapstructure:"notify_path"`

	// APIv3Key 仅从环境变量读取，禁止写入配置文件。
	APIv3Key string `mapstructure:"-"`
}

// Load 加载配置。
//
// path 为空时依次尝试环境变量 PAYMENT_CONFIG 与 DefaultPath；
// 文件不存在也不报错，而是全部走默认值——这样容器里只挂环境变量也能启动。
// 加载完成后立即校验，把配置错误挡在进程启动阶段而不是第一次请求时。
func Load(path string) (*Config, error) {
	if strings.TrimSpace(path) == "" {
		path = os.Getenv(EnvConfigFile)
	}
	if strings.TrimSpace(path) == "" {
		path = DefaultPath
	}

	v := viper.New()
	setDefaults(v)
	bindEnvs(v)

	v.SetConfigFile(path)
	// 配置文件缺失是允许的：密钥类配置本就只走环境变量，
	// 其余项都有默认值，强制要求文件存在反而增加部署负担。
	if _, err := os.Stat(path); err == nil {
		if rerr := v.ReadInConfig(); rerr != nil {
			return nil, fmt.Errorf("读取配置文件 %s 失败: %w", path, rerr)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("检查配置文件 %s 失败: %w", path, err)
	}

	var cfg Config
	if err := v.Unmarshal(&cfg); err != nil {
		return nil, fmt.Errorf("解析配置失败: %w", err)
	}

	// 密钥单独用 os.Getenv 读取，不经过 viper：
	// 这样在代码层面就不存在「从 yaml 读到密钥」的可能，审计时一目了然。
	cfg.Channels.WechatPay.APIv3Key = strings.TrimSpace(os.Getenv(EnvWechatPayAPIv3Key))

	cfg.normalize()
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// setDefaults 写入内置默认值，保证配置文件缺项时服务仍可启动。
func setDefaults(v *viper.Viper) {
	v.SetDefault("server.mode", ModeDebug)
	v.SetDefault("server.host", "0.0.0.0")
	v.SetDefault("server.port", 8080)
	v.SetDefault("server.read_timeout", "15s")
	v.SetDefault("server.write_timeout", "15s")
	v.SetDefault("server.shutdown_timeout", "10s")

	v.SetDefault("log.level", "info")
	v.SetDefault("log.format", "console")

	v.SetDefault("payment.default_channel", "mock")
	v.SetDefault("payment.notify_base_url", "")
	v.SetDefault("payment.out_trade_no_prefix", "ORD")
	v.SetDefault("payment.payment_no_prefix", "PAY")

	v.SetDefault("channels.wechatpay.enabled", false)
	v.SetDefault("channels.wechatpay.notify_path", "/api/v1/callbacks/wechatpay")
}

// bindEnvs 显式声明配置项与环境变量的映射关系。
//
// 不用 AutomaticEnv：它的键名推导规则隐蔽，配置层级一变就可能悄悄失效，
// 而支付配置读错的后果是资金问题。逐条 BindEnv 虽然啰嗦，但映射关系可直接审计。
func bindEnvs(v *viper.Viper) {
	bindings := map[string]string{
		"server.mode":                           "PAYMENT_SERVER_MODE",
		"server.host":                           "PAYMENT_SERVER_HOST",
		"server.port":                           "PAYMENT_SERVER_PORT",
		"server.read_timeout":                   "PAYMENT_SERVER_READ_TIMEOUT",
		"server.write_timeout":                  "PAYMENT_SERVER_WRITE_TIMEOUT",
		"server.shutdown_timeout":               "PAYMENT_SERVER_SHUTDOWN_TIMEOUT",
		"log.level":                             "PAYMENT_LOG_LEVEL",
		"log.format":                            "PAYMENT_LOG_FORMAT",
		"payment.default_channel":               "PAYMENT_DEFAULT_CHANNEL",
		"payment.notify_base_url":               "PAYMENT_NOTIFY_BASE_URL",
		"channels.wechatpay.enabled":            "WECHATPAY_ENABLED",
		"channels.wechatpay.app_id":             "WECHATPAY_APP_ID",
		"channels.wechatpay.mch_id":             "WECHATPAY_MCH_ID",
		"channels.wechatpay.mch_cert_serial_no": "WECHATPAY_MCH_CERT_SERIAL_NO",
		"channels.wechatpay.private_key_path":   "WECHATPAY_PRIVATE_KEY_PATH",
		"channels.wechatpay.public_key_id":      "WECHATPAY_PUBLIC_KEY_ID",
		"channels.wechatpay.public_key_path":    "WECHATPAY_PUBLIC_KEY_PATH",
		"channels.wechatpay.notify_path":        "WECHATPAY_NOTIFY_PATH",
	}
	for key, env := range bindings {
		// BindEnv 显式传入环境变量名时不会再加前缀，映射完全可控
		_ = v.BindEnv(key, env)
	}
}

// normalize 统一大小写与首尾空白，避免 "Release" / " release " 这类写法造成误判。
func (c *Config) normalize() {
	c.Server.Mode = strings.ToLower(strings.TrimSpace(c.Server.Mode))
	c.Log.Level = strings.ToLower(strings.TrimSpace(c.Log.Level))
	c.Log.Format = strings.ToLower(strings.TrimSpace(c.Log.Format))
	c.Payment.DefaultChannel = strings.ToLower(strings.TrimSpace(c.Payment.DefaultChannel))
	c.Payment.NotifyBaseURL = strings.TrimRight(strings.TrimSpace(c.Payment.NotifyBaseURL), "/")
}

// IsRelease 是否为生产模式。调试类接口据此决定是否注册。
func (c *Config) IsRelease() bool { return c.Server.Mode == ModeRelease }

// NotifyURL 拼出微信支付的完整回调地址。
func (c *Config) NotifyURL() string {
	return c.Payment.NotifyBaseURL + c.Channels.WechatPay.NotifyPath
}

// Validate 校验配置合法性，启动阶段调用。
//
// 原则是「宁可启动失败，不可带病运行」：端口越界、模式写错这类问题
// 如果留到运行时才暴露，代价是一次线上故障。
func (c *Config) Validate() error {
	switch c.Server.Mode {
	case ModeDebug, ModeRelease, ModeTest:
	default:
		return fmt.Errorf("server.mode 非法: %q，可选 debug/release/test", c.Server.Mode)
	}

	if c.Server.Port < 1 || c.Server.Port > 65535 {
		return fmt.Errorf("server.port 非法: %d，应在 1-65535 之间", c.Server.Port)
	}
	if c.Server.ReadTimeout <= 0 {
		return errors.New("server.read_timeout 必须大于 0")
	}
	if c.Server.WriteTimeout <= 0 {
		return errors.New("server.write_timeout 必须大于 0")
	}
	if c.Server.ShutdownTimeout <= 0 {
		return errors.New("server.shutdown_timeout 必须大于 0")
	}

	switch c.Log.Format {
	case "console", "json":
	default:
		return fmt.Errorf("log.format 非法: %q，可选 console/json", c.Log.Format)
	}

	if c.Payment.DefaultChannel == "" {
		return errors.New("payment.default_channel 不能为空")
	}

	// 生产模式对回调地址的要求最严：微信支付不接受 HTTP 与非备案域名，
	// 这里提前拦截，避免上线后才发现回调打不进来。
	if c.IsRelease() && !strings.HasPrefix(c.Payment.NotifyBaseURL, "https://") {
		return fmt.Errorf("release 模式下 payment.notify_base_url 必须是 https:// 开头的公网地址，当前为 %q",
			c.Payment.NotifyBaseURL)
	}

	return c.Channels.WechatPay.validate(c.IsRelease())
}

// validate 校验微信支付配置。
//
// 未启用时完全跳过：骨架阶段默认走 mock 渠道，
// 强制要求填齐微信支付参数会让本地开发无法启动。
func (w WechatPayConfig) validate(release bool) error {
	if !w.Enabled {
		return nil
	}

	var missing []string
	if strings.TrimSpace(w.AppID) == "" {
		missing = append(missing, "channels.wechatpay.app_id")
	}
	if strings.TrimSpace(w.MchID) == "" {
		missing = append(missing, "channels.wechatpay.mch_id")
	}
	if strings.TrimSpace(w.PrivateKeyPath) == "" {
		missing = append(missing, "channels.wechatpay.private_key_path")
	}
	if len(w.APIv3Key) == 0 {
		missing = append(missing, EnvWechatPayAPIv3Key+"（环境变量）")
	}
	if len(missing) > 0 {
		return fmt.Errorf("微信支付已启用但配置不完整，缺少: %s", strings.Join(missing, ", "))
	}

	if len(w.APIv3Key) != apiV3KeyLen {
		return fmt.Errorf("%s 长度必须为 %d 位，当前 %d 位", EnvWechatPayAPIv3Key, apiV3KeyLen, len(w.APIv3Key))
	}

	// 证书模式与公钥模式二选一，至少要有一套验签凭据
	hasCertMode := strings.TrimSpace(w.MchCertSerialNo) != ""
	hasPublicKeyMode := strings.TrimSpace(w.PublicKeyID) != "" && strings.TrimSpace(w.PublicKeyPath) != ""
	if !hasCertMode && !hasPublicKeyMode {
		return errors.New("微信支付验签凭据缺失：需配置 mch_cert_serial_no（证书模式）" +
			"或 public_key_id + public_key_path（公钥模式）")
	}

	// 只在生产模式校验文件存在：证书由部署时挂载，
	// 本地开发与 CI 环境通常没有这些文件，强制校验会阻塞流程。
	if release {
		for _, p := range []string{w.PrivateKeyPath, w.PublicKeyPath} {
			if strings.TrimSpace(p) == "" {
				continue
			}
			if _, err := os.Stat(p); err != nil {
				return fmt.Errorf("微信支付密钥文件不可读 %s: %w", p, err)
			}
		}
	}
	return nil
}
