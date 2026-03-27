package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/joho/godotenv"
)

// APIKeyEntry 描述一个可用于网关认证的 API Key 及其租户级配置。
type APIKeyEntry struct {
	Key        string
	Label      string
	DailyQuota int64
}

// Config 汇总当前网关启动所需的全部配置。
// 其中 failure mode 字段用于显式区分安全关键依赖和可降级增强依赖。
type Config struct {
	Port string

	APIKeys        []APIKeyEntry
	RateLimitQPS   float64
	RateLimitBurst int
	PythonAddr     string
	RustAddr       string
	RedisAddr      string
	RedisPassword  string
	RedisDB        int
	RouteStrategy  string
	HealthAlpha    float64

	OpenAIApiKey  string
	OpenAIURL     string
	OpenAITimeout time.Duration

	RequestTimeout         time.Duration
	TokenCountTimeout      time.Duration
	CacheTimeout           time.Duration
	GuardrailNitroTimeout  time.Duration
	GuardrailIntellTimeout time.Duration

	GRPCBaseDelay      time.Duration
	GRPCMaxDelay       time.Duration
	GRPCEnableTLS      bool
	GRPCServerName     string
	GRPCCAFile         string
	GRPCClientCertFile string
	GRPCClientKeyFile  string

	OTELCollectorAddr string

	TokenEstimationFactor int
	MaxRetries            int

	MaxConcurrentRequests  int
	CircuitBreakerInterval time.Duration

	NitroFailureMode        string
	PythonInputFailureMode  string
	PythonOutputFailureMode string
	PythonCacheFailureMode  string
}

// LoadConfig 从环境变量和 .env 文件加载配置，并填充合理默认值。
func LoadConfig() *Config {
	_ = godotenv.Load("../../.env", "../.env", ".env")

	rawKeys := getEnv("GATEWAY_API_KEYS", getEnv("GATEWAY_API_KEY", "sk-gw-default-123456"))
	qps, _ := strconv.ParseFloat(getEnv("RATE_LIMIT_QPS", "100"), 64)
	burst, _ := strconv.Atoi(getEnv("RATE_LIMIT_BURST", "200"))
	healthAlpha, _ := strconv.ParseFloat(getEnv("HEALTH_EWMA_ALPHA", "0.3"), 64)
	tokenFactor, _ := strconv.Atoi(getEnv("TOKEN_ESTIMATION_FACTOR", "4"))

	return &Config{
		Port:                    getEnv("PORT", "8080"),
		APIKeys:                 ParseAPIKeys(rawKeys),
		RateLimitQPS:            qps,
		RateLimitBurst:          burst,
		PythonAddr:              getEnv("PYTHON_ADDR", "localhost:50051"),
		RustAddr:                getEnv("RUST_ADDR", "localhost:50052"),
		RedisAddr:               getEnv("REDIS_ADDR", "localhost:6379"),
		RedisPassword:           getEnv("REDIS_PASSWORD", ""),
		RedisDB:                 getIntEnv("REDIS_DB", 0),
		OpenAIApiKey:            os.Getenv("OPENAI_API_KEY"),
		RouteStrategy:           getEnv("ROUTE_STRATEGY", "weighted"),
		HealthAlpha:             healthAlpha,
		OpenAIURL:               getEnv("OPENAI_URL", "https://api.openai.com/v1/chat/completions"),
		OpenAITimeout:           getDuration("OPENAI_TIMEOUT", 30*time.Second),
		RequestTimeout:          getDuration("REQUEST_TIMEOUT", 15*time.Second),
		TokenCountTimeout:       getDuration("TOKEN_COUNT_TIMEOUT", 2*time.Second),
		CacheTimeout:            getDuration("CACHE_TIMEOUT", 500*time.Millisecond),
		GuardrailNitroTimeout:   getDuration("GUARDRAIL_NITRO_TIMEOUT", 200*time.Millisecond),
		GuardrailIntellTimeout:  getDuration("GUARDRAIL_INTELL_TIMEOUT", 1000*time.Millisecond),
		GRPCBaseDelay:           getDuration("GRPC_BASE_DELAY", 1*time.Second),
		GRPCMaxDelay:            getDuration("GRPC_MAX_DELAY", 10*time.Second),
		GRPCEnableTLS:           getBoolEnv("GRPC_ENABLE_TLS", false),
		GRPCServerName:          getEnv("GRPC_SERVER_NAME", ""),
		GRPCCAFile:              getEnv("GRPC_CA_FILE", ""),
		GRPCClientCertFile:      getEnv("GRPC_CLIENT_CERT_FILE", ""),
		GRPCClientKeyFile:       getEnv("GRPC_CLIENT_KEY_FILE", ""),
		TokenEstimationFactor:   tokenFactor,
		MaxRetries:              getIntEnv("MAX_RETRIES", 2),
		OTELCollectorAddr:       os.Getenv("OTEL_COLLECTOR_ADDR"),
		MaxConcurrentRequests:   getIntEnv("MAX_CONCURRENT_REQUESTS", 1000),
		CircuitBreakerInterval:  getDuration("CB_RECOVERY_INTERVAL", 30*time.Second),
		NitroFailureMode:        getEnv("NITRO_FAILURE_MODE", "fail_closed"),
		PythonInputFailureMode:  getEnv("PYTHON_INPUT_FAILURE_MODE", "fail_open_with_audit"),
		PythonOutputFailureMode: getEnv("PYTHON_OUTPUT_FAILURE_MODE", "fail_open_with_audit"),
		PythonCacheFailureMode:  getEnv("PYTHON_CACHE_FAILURE_MODE", "fail_open_with_audit"),
	}
}

// Validate 校验显式失败策略的取值，避免服务以含糊配置启动。
func (c *Config) Validate() error {
	if err := validateFailureMode("NITRO_FAILURE_MODE", c.NitroFailureMode); err != nil {
		return err
	}
	if err := validateFailureMode("PYTHON_INPUT_FAILURE_MODE", c.PythonInputFailureMode); err != nil {
		return err
	}
	if err := validateFailureMode("PYTHON_OUTPUT_FAILURE_MODE", c.PythonOutputFailureMode); err != nil {
		return err
	}
	if err := validateFailureMode("PYTHON_CACHE_FAILURE_MODE", c.PythonCacheFailureMode); err != nil {
		return err
	}
	return nil
}

func getDuration(key string, fallback time.Duration) time.Duration {
	val := os.Getenv(key)
	if val == "" {
		return fallback
	}
	d, err := time.ParseDuration(val)
	if err != nil {
		return fallback
	}
	return d
}

// ParseAPIKeys 支持解析 key:label:quota 形式的配置串。
func ParseAPIKeys(raw string) []APIKeyEntry {
	var entries []APIKeyEntry
	parts := strings.Split(raw, ",")
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		kv := strings.SplitN(p, ":", 3)
		key := kv[0]
		label := "default"
		var quota int64

		if len(kv) > 1 {
			label = kv[1]
		}
		if len(kv) > 2 {
			quota, _ = strconv.ParseInt(kv[2], 10, 64)
		}
		entries = append(entries, APIKeyEntry{Key: key, Label: label, DailyQuota: quota})
	}
	return entries
}

func getEnv(key, fallback string) string {
	if value, ok := os.LookupEnv(key); ok {
		return value
	}
	return fallback
}

func getIntEnv(key string, fallback int) int {
	val := os.Getenv(key)
	if val == "" {
		return fallback
	}
	iv, err := strconv.Atoi(val)
	if err != nil {
		return fallback
	}
	return iv
}

func getBoolEnv(key string, fallback bool) bool {
	val := strings.TrimSpace(strings.ToLower(os.Getenv(key)))
	if val == "" {
		return fallback
	}
	switch val {
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	default:
		return fallback
	}
}

func validateFailureMode(name, value string) error {
	switch strings.TrimSpace(strings.ToLower(value)) {
	case "fail_closed", "fail_open_with_audit":
		return nil
	default:
		return fmt.Errorf("%s must be fail_closed or fail_open_with_audit, got %q", name, value)
	}
}
