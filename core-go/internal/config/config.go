// Package config manages system-level settings and environment variables.
package config

import (
	"os"

	"github.com/joho/godotenv"
)

// Config 存储网关的所有全局配置。
type Config struct {
	Port           string // 网关 HTTP 监听端口。
	PythonAddr     string // Python 智能层 gRPC 地址。
	RustAddr       string // Rust 加速层 gRPC 地址。
	GatewayApiKey  string // 访问网关所需的 API Key。
	OpenAIApiKey   string // 转发至 OpenAI 所需的 Key。
}

// LoadConfig 从环境变量或默认值加载配置。
func LoadConfig() *Config {
	// 尝试从项目根目录（向上两级）或当前目录加载 .env
	_ = godotenv.Load("../../.env", "../.env", ".env")

	return &Config{
		Port:          getEnv("PORT", "8080"),
		PythonAddr:    getEnv("PYTHON_ADDR", "localhost:50051"),
		RustAddr:      getEnv("RUST_ADDR", "localhost:50052"),
		GatewayApiKey: getEnv("GATEWAY_API_KEY", "sk-gw-default-123456"),
		OpenAIApiKey:  os.Getenv("OPENAI_API_KEY"),
	}
}

func getEnv(key, fallback string) string {
	if value, ok := os.LookupEnv(key); ok {
		return value
	}
	return fallback
}
