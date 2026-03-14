// Package config 管理系统级别的设置和环境变量。
package config

import (
	"os"
	"strconv"
	"strings"

	"github.com/joho/godotenv"
)

// APIKeyEntry 表示一个经过授权的 API Key 及其关联的标签（Label）。
type APIKeyEntry struct {
	Key   string // API Key 字符串。
	Label string // 用于标识调用方（如 admin, user1）的标签。
}

// Config 存储网关的所有全局配置。
type Config struct {
	Port           string        // 网关 HTTP 监听端口。
	APIKeys        []APIKeyEntry // 支持多 API Key 校验。
	RateLimitQPS   float64       // 每秒请求数限制。
	RateLimitBurst int           // 令牌桶突发容量。
	PythonAddr     string        // Python 智能层 gRPC 地址。
	RustAddr       string        // Rust 加速层 gRPC 地址。
	OpenAIApiKey   string        // 转发至 OpenAI 所需的 Key。
	RouteStrategy  string        // 默认路由策略（weighted/cost/latency/quality/fallback）。
	HealthAlpha    float64       // 健康追踪 EWMA 衰减因子（0.0~1.0）。
}

// LoadConfig 从环境变量或默认值加载配置。
func LoadConfig() *Config {
	// 尝试从项目根目录（向上两级）或当前目录加载 .env
	_ = godotenv.Load("../../.env", "../.env", ".env")

	rawKeys := getEnv("GATEWAY_API_KEYS", getEnv("GATEWAY_API_KEY", "sk-gw-default-123456"))
	qps, _ := strconv.ParseFloat(getEnv("RATE_LIMIT_QPS", "100"), 64)
	burst, _ := strconv.Atoi(getEnv("RATE_LIMIT_BURST", "200"))
	healthAlpha, _ := strconv.ParseFloat(getEnv("HEALTH_EWMA_ALPHA", "0.3"), 64)

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
	}
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
