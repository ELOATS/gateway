// Package config 管理系统级别的设置和环境变量。
package config

import (
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/joho/godotenv"
)

// APIKeyEntry 表示一个经过授权的 API Key 及其关联的标签（Label）。
type APIKeyEntry struct {
	Key   string // API Key 字符串。
	Label string // 用于标识调用方（如 admin, user1）的标签。
}

// Config 存储网关的所有全局配置。
type Config struct {
	// 基础网络配置
	Port string // 网关 HTTP 监听端口。

	// 业务逻辑配置
	APIKeys        []APIKeyEntry // 支持多 API Key 校验。
	RateLimitQPS   float64       // 每秒请求数限制。
	RateLimitBurst int           // 令牌桶突发容量。
	PythonAddr     string        // Python 智能层 gRPC 地址。
	RustAddr       string        // Rust 加速层 gRPC 地址。
	RouteStrategy  string        // 默认路由策略（weighted/cost/latency/quality/fallback）。
	HealthAlpha    float64       // 健康追踪 EWMA 衰减因子（0.0~1.0）。

	// 外部供应商配置
	OpenAIApiKey string        // 转发至 OpenAI 所需的 Key。
	OpenAIURL    string        // OpenAI API 基础地址。
	OpenAITimeout time.Duration // OpenAI 调用超时。

	// 细粒度超时控制 (Duration)
	RequestTimeout      time.Duration // 整体请求最大耗时。
	TokenCountTimeout   time.Duration // Token 统计调用超时。
	CacheTimeout        time.Duration // 缓存查询超时。
	GuardrailNitroTimeout time.Duration // Nitro 审计超时。
	GuardrailIntellTimeout time.Duration // 智能审计超时。

	// gRPC 策略
	GRPCBaseDelay time.Duration // gRPC 重试基础延迟。
	GRPCMaxDelay  time.Duration // gRPC 重试最大延迟。

	// 算法参数
	TokenEstimationFactor int // 字符转 Token 估算系数。
	MaxRetries            int // 供应商调用最大重试次数。
}

// LoadConfig 从环境变量或默认值加载配置。
func LoadConfig() *Config {
	// 尝试加载 .env
	_ = godotenv.Load("../../.env", "../.env", ".env")

	rawKeys := getEnv("GATEWAY_API_KEYS", getEnv("GATEWAY_API_KEY", "sk-gw-default-123456"))
	qps, _ := strconv.ParseFloat(getEnv("RATE_LIMIT_QPS", "100"), 64)
	burst, _ := strconv.Atoi(getEnv("RATE_LIMIT_BURST", "200"))
	healthAlpha, _ := strconv.ParseFloat(getEnv("HEALTH_EWMA_ALPHA", "0.3"), 64)
	tokenFactor, _ := strconv.Atoi(getEnv("TOKEN_ESTIMATION_FACTOR", "4"))

	return &Config{
		Port:           getEnv("PORT", "8080"),
		APIKeys:        ParseAPIKeys(rawKeys),
		RateLimitQPS:   qps,
		RateLimitBurst: burst,
		PythonAddr:     getEnv("PYTHON_ADDR", "localhost:50051"),
		RustAddr:       getEnv("RUST_ADDR", "localhost:50052"),
		OpenAIApiKey:   os.Getenv("OPENAI_API_KEY"),
		RouteStrategy:  getEnv("ROUTE_STRATEGY", "weighted"),
		HealthAlpha:    healthAlpha,

		OpenAIURL:     getEnv("OPENAI_URL", "https://api.openai.com/v1/chat/completions"),
		OpenAITimeout: getDuration("OPENAI_TIMEOUT", 30*time.Second),

		RequestTimeout:      getDuration("REQUEST_TIMEOUT", 15*time.Second),
		TokenCountTimeout:   getDuration("TOKEN_COUNT_TIMEOUT", 2*time.Second),
		CacheTimeout:        getDuration("CACHE_TIMEOUT", 500*time.Millisecond),
		GuardrailNitroTimeout: getDuration("GUARDRAIL_NITRO_TIMEOUT", 200*time.Millisecond),
		GuardrailIntellTimeout: getDuration("GUARDRAIL_INTELL_TIMEOUT", 1000*time.Millisecond),

		GRPCBaseDelay: getDuration("GRPC_BASE_DELAY", 1*time.Second),
		GRPCMaxDelay:  getDuration("GRPC_MAX_DELAY", 10*time.Second),

		TokenEstimationFactor: tokenFactor,
		MaxRetries:            getIntEnv("MAX_RETRIES", 2),
	}
}

// getDuration 从环境变量解析持续时间，解析失败返回默认值。
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

// ParseAPIKeys 解析逗号分隔的 key:label 格式。
func ParseAPIKeys(raw string) []APIKeyEntry {
	var entries []APIKeyEntry
	parts := strings.Split(raw, ",")
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		kv := strings.SplitN(p, ":", 2)
		key := kv[0]
		label := "default"
		if len(kv) > 1 {
			label = kv[1]
		}
		entries = append(entries, APIKeyEntry{Key: key, Label: label})
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
