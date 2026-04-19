package config

import (
	"fmt"
	"os"
	"path/filepath"
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

// PathConfig 汇总了网关运行时所需的所有文件系统资源路径。
type PathConfig struct {
	PolicyFile         string // 动态策略配置文件路径 (policies.yaml)
	AdapterDir         string // 供应商适配器配置目录
	SensitiveRulesFile string // 敏感词过滤规则文件
	NitroWasmFile      string // Nitro 安全引擎的 WASM 字节码文件
	AuditLogFile       string // 审计合规日志文件
}

// Config 汇总了网关在生产环境稳定运行所需的全部配置参数。
// 它通过单一入口管理端口监听、路径依赖、外部中间件地址及核心超时策略。
//
// 关键设计决策 —— 故障处理模式 (Failure Mode)：
// 在分布式环境下，本网关采用了“可调节的安全边际”方案：
// - fail_closed (强校验)：当安全引擎（如 Nitro) 不可用时，默认拦截所有请求，确保绝对合规。适用于高规范领域。
// - fail_open_with_audit (高可用)：安全检查失败时允许请求透出，但强制进行背景审计，确保护核业务连续性。
type Config struct {
	Port  string     // 网关对外 HTTP 服务的监听端口
	Paths PathConfig // 汇总文件系统中的策略文件、WASM 字节码及审计日志的路径

	APIKeys        []APIKeyEntry // 基于配置文件的静态 API Key 映射（应急或开发场景使用）
	RateLimitQPS   float64       // 网关节点的默认每秒最大请求数
	RateLimitBurst int           // 突发流量容忍上限
	PythonAddr     string        // 计算密集型任务（如语义缓存、提示词审计）的 gRPC 地址
	RustAddr       string        // 高并发任务（如 Nitro Token 计数、正则审计）的 gRPC 地址
	RedisAddr      string        // 分布式限流与临时状态同步的 Redis 连接串
	RedisPassword  string        // Redis 认证凭据
	RedisDB        int           // Redis 逻辑数据库索引
	DatabaseDSN    string        // 主关系型数据库连接串（存储租户映射、价格、审计日志）

	RouteStrategy string  // 默认负载均衡算法：支持 weighted (权重), latency (时延优先), cost (成本优先)
	HealthAlpha   float64 // 健康评分系统的指数移动平均 (EWMA) 平滑因子

	OpenAIApiKey  string        // 默认的后端供应商认证 Key (若 Adapter 未指定)
	OpenAIURL     string        // OpenAI 兼容接口的原始端点地址
	OpenAITimeout time.Duration // 向供应商发起网络调用的硬性超时控制

	// Rerank 服务配置：适配各种 RAG 流程的具体后端。
	CohereApiKey string
	CohereURL    string
	JinaApiKey   string
	JinaURL      string

	// 超时管理策略：确保护核路径不被慢速连接或后端拥塞拖垮。
	RequestTimeout         time.Duration // 单次请求从进入到返回的总生存期 (TTL)
	TokenCountTimeout      time.Duration // 向 Nitro 服务请求分词计算的等待上限
	CacheTimeout           time.Duration // 查询语义缓存的响应延迟上限
	GuardrailNitroTimeout  time.Duration // 静态护栏检测的快速失败阈值
	GuardrailIntellTimeout time.Duration // 智能护栏（大模型审计）的等待宽限

	// gRPC 传输安全配置
	GRPCBaseDelay      time.Duration
	GRPCMaxDelay       time.Duration
	GRPCEnableTLS      bool   // 是否启用 gRPC 双向 TLS 认证
	GRPCServerName     string // TLS 证书中的域名匹配标识
	GRPCCAFile         string // 根证书授权文件路径
	GRPCClientCertFile string // 客户端公钥证书
	GRPCClientKeyFile  string // 客户端私钥

	OTELCollectorAddr string // OpenTelemetry 事件/指标的流式收割机地址

	TokenEstimationFactor int // 在流式中断或供应商未返回 Usage 时，用于估算 Token 消耗的倍率系数
	MaxRetries            int // 最大重试次数（受 RetryBudget 控制）

	MaxConcurrentRequests  int           // 单节点并发控制层级，通过信号量实施
	CircuitBreakerInterval time.Duration // 熔断器自动尝试恢复的冷却时间窗

	// 故障自愈策略：明确系统在局部服务不可靠时的鲁棒性边界。
	NitroFailureMode        string // Nitro 服务失联后的决策选择
	PythonInputFailureMode  string // Python 输入审查失联后的决策选择
	PythonOutputFailureMode string // Python 输出审查失联后的决策选择
	PythonCacheFailureMode  string // Python 向量缓存失联后的决策选择
}

// LoadConfig 是网关配置的唯一入口。
// 它从环境变量和 .env 文件加载配置，并填充合理默认值。
func LoadConfig() *Config {
	_ = godotenv.Load("../../.env", "../.env", ".env")

	rawKeys := getEnv("GATEWAY_API_KEYS", getEnv("GATEWAY_API_KEY", "sk-gw-default-123456"))
	qps, _ := strconv.ParseFloat(getEnv("RATE_LIMIT_QPS", "100"), 64)
	burst, _ := strconv.Atoi(getEnv("RATE_LIMIT_BURST", "200"))
	healthAlpha, _ := strconv.ParseFloat(getEnv("HEALTH_EWMA_ALPHA", "0.3"), 64)
	tokenFactor, _ := strconv.Atoi(getEnv("TOKEN_ESTIMATION_FACTOR", "4"))

	return &Config{
		Port: getEnv("PORT", "8080"),
		Paths: PathConfig{
			PolicyFile:         resolvePathEnv("GATEWAY_POLICIES_PATH", filepath.Join("configs", "policies.yaml")),
			AdapterDir:         resolvePathEnv("GATEWAY_ADAPTERS_DIR", filepath.Join("configs", "adapters")),
			SensitiveRulesFile: resolvePathEnv("GATEWAY_SENSITIVE_RULES_PATH", filepath.Join("configs", "sensitive.txt")),
			NitroWasmFile:      resolvePathEnv("GATEWAY_NITRO_WASM_PATH", filepath.Join("wasm", "nitro.wasm")),
			AuditLogFile:       getEnv("GATEWAY_AUDIT_LOG_PATH", "audit_compliance.log"),
		},
		APIKeys:                 ParseAPIKeys(rawKeys),
		RateLimitQPS:            qps,
		RateLimitBurst:          burst,
		PythonAddr:              getEnv("PYTHON_ADDR", "localhost:50051"),
		RustAddr:                getEnv("RUST_ADDR", "localhost:50052"),
		RedisAddr:               getEnv("REDIS_ADDR", "localhost:6379"),
		RedisPassword:           getEnv("REDIS_PASSWORD", ""),
		RedisDB:                 getIntEnv("REDIS_DB", 0),
		DatabaseDSN:             getEnv("DATABASE_DSN", "gateway.db"),
		OpenAIApiKey:            os.Getenv("OPENAI_API_KEY"),
		RouteStrategy:           getEnv("ROUTE_STRATEGY", "weighted"),
		HealthAlpha:             healthAlpha,
		OpenAIURL:               getEnv("OPENAI_URL", "https://api.openai.com/v1/chat/completions"),
		OpenAITimeout:           getDuration("OPENAI_TIMEOUT", 30*time.Second),
		CohereApiKey:            os.Getenv("COHERE_API_KEY"),
		CohereURL:               getEnv("COHERE_URL", "https://api.cohere.ai/v1/rerank"),
		JinaApiKey:              os.Getenv("JINA_API_KEY"),
		JinaURL:                 getEnv("JINA_URL", "https://api.jina.ai/v1/rerank"),
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

func resolvePathEnv(envKey, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(envKey)); value != "" {
		return resolvePath(value)
	}
	return resolvePath(fallback)
}

// resolvePath 采用层次化搜索逻辑尝试定位目标配置文件。
// 它会从当前工作目录（CWD）出发，递归向上搜索 8 层父目录。
// 这一设计极大简化了开发者在不同子目录下执行测试/调试命令时的路径噩梦。
func resolvePath(candidate string) string {
	if candidate == "" {
		return ""
	}
	if filepath.IsAbs(candidate) {
		return candidate
	}

	wd, err := os.Getwd()
	if err != nil {
		return candidate
	}

	dir := wd
	for range 8 {
		resolved := filepath.Join(dir, candidate)
		if _, statErr := os.Stat(resolved); statErr == nil {
			return resolved
		}

		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}

	return candidate
}
